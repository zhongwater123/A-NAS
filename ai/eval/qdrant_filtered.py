"""Filtered dense search in Qdrant against an exact numpy scan of the same libraries.

Development only (docs/research/qdrant-retrieval-features.md, experiment 1).
The photos are the cached EmbeddingGemma 2 vectors (768-d, L2-normalised, Dot
distance), split into synthetic libraries the way a family NAS is: five
libraries of 30/25/20/15/10%, a fifth of the photos also in a second one, and
a small shared library holding 2%. Each member searches what their libraries
hold, through `match any` on a keyword `libraries` field indexed before the
upload, as internal/photos/search.go filters by library before scanning.

Tiers: the COCO train subset (20k), every distinct cached photo (about 37k),
and 100k, where the photos beyond the real ones are mixtures of a real photo
and one of its ten nearest neighbours, a synthetic stand-in reported as such.

Variants per tier rebuild the collection: Qdrant's defaults; HNSW forced
(indexing threshold 1 KB, full-scan threshold
10 KB, its minimum) and searched at several hnsw_ef,
exact, and with ACORN; a 1 MB WAL; one optimisation thread and CPU budget 1;
vectors held `cold`; and `libraries` as a tenant field. Each reports recall
against numpy (ties count either way), warm latency, cold start, RSS and disk.

    python -m eval.qdrant_filtered [--tiers 20k,all,100k] [--variants default,hnsw,...]
"""

import argparse
import time

import numpy as np

from eval import qdrant_common as common

COLLECTION = "photos"
WEIGHTS = (0.30, 0.25, 0.20, 0.15, 0.10)
SECOND_LIBRARY, SMALL_LIBRARY = 0.2, 0.02
MEMBERS = {"a": ["L0", "L1"], "b": ["L1", "L2", "L3"], "c": ["L4"], "small": ["L5"], "everything": None}
TEXT_QUERIES, IMAGE_QUERIES = 300, 100
SMALL_LIMIT, LARGE_LIMIT = 50, 500
BATCH = 512  # points per upsert: 1.5 MB of vectors, larger than the 1 MB WAL variant's segment

FORCED_HNSW = {"optimizers": {"indexing_threshold": 1}, "hnsw": {"full_scan_threshold": 10}}
VARIANTS = {
    "default": {},
    "hnsw": {**FORCED_HNSW, "searches": True},
    "wal1": {"wal": 1},
    "onethread": {**FORCED_HNSW, "optimizers": {"indexing_threshold": 1, "max_optimization_threads": 1},
                  "hnsw": {"full_scan_threshold": 10, "max_indexing_threads": 1},
                  "server": {"QDRANT__STORAGE__PERFORMANCE__OPTIMIZER_CPU_BUDGET": "1"}},
    "cold": {**FORCED_HNSW, "memory": "cold"},
    "tenant": {**FORCED_HNSW, "tenant": True},
}


def assign_libraries(count, seed=7):
    rng = np.random.default_rng(seed)
    primary = rng.choice(len(WEIGHTS), size=count, p=WEIGHTS)
    other = (primary + rng.integers(1, len(WEIGHTS), size=count)) % len(WEIGHTS)
    second = rng.random(count) < SECOND_LIBRARY
    small = rng.random(count) < SMALL_LIBRARY
    libraries = []
    for index in range(count):
        found = [f"L{primary[index]}"]
        if second[index]:
            found.append(f"L{other[index]}")
        if small[index]:
            found.append("L5")
        libraries.append(found)
    return libraries


def nearest(matrix, count, rows=None, block=2048):
    """Indices of each row's `count` nearest other rows."""
    rows = np.arange(len(matrix)) if rows is None else rows
    found = np.empty((len(rows), count), dtype=np.int64)
    for start in range(0, len(rows), block):
        chosen = rows[start:start + block]
        scores = matrix[chosen] @ matrix.T
        scores[np.arange(len(chosen)), chosen] = -np.inf
        top = np.argpartition(-scores, count, axis=1)[:, :count]
        order = np.argsort(-np.take_along_axis(scores, top, axis=1), axis=1)
        found[start:start + len(chosen)] = np.take_along_axis(top, order, axis=1)
    return found


def synthesize(real, total, seed=11):
    """Real photos plus mixtures of a real photo and one of its ten nearest neighbours, renormalised."""
    rng = np.random.default_rng(seed)
    extra = total - len(real)
    bases = rng.integers(0, len(real), size=extra)
    neighbours = nearest(real, 10, np.unique(bases))
    lookup = {base: row for row, base in enumerate(np.unique(bases))}
    partners = np.array([neighbours[lookup[base], rng.integers(0, 10)] for base in bases])
    share = rng.uniform(0.5, 0.9, size=(extra, 1)).astype(np.float32)
    mixed = share * real[bases] + (1 - share) * real[partners]
    mixed /= np.linalg.norm(mixed, axis=1, keepdims=True)
    return np.concatenate([real, mixed.astype(np.float32)])


def nearest_cosine(matrix, rows):
    """Median cosine of the given rows to their nearest other row: how crowded the space is."""
    return round(float(np.median([np.max(np.delete(matrix @ matrix[row], row)) for row in rows])), 4)


def tier_data(tier, keys, real):
    if tier == "smoke":
        return real[:3000], {"source": "first 3000 photos, a smoke test", "real": 3000}
    if tier == "20k":
        chosen = [index for index, key in enumerate(keys) if key.startswith("coco-train:")]
        return real[chosen], {"source": "COCO train subset", "real": len(chosen)}
    if tier == "all":
        return real, {"source": "every distinct cached photo", "real": len(real)}
    total = int(tier.rstrip("k")) * 1000
    matrix = synthesize(real, total)
    rng = np.random.default_rng(3)
    return matrix, {"source": "real photos plus neighbour mixtures", "real": len(real),
                    "synthetic": total - len(real),
                    "nearestCosineReal": nearest_cosine(matrix[:len(real)], rng.integers(0, len(real), 300)),
                    "nearestCosineSynthetic": nearest_cosine(matrix, rng.integers(len(real), total, 300))}


def create(qdrant, variant, dim):
    from qdrant_client import models

    settings = VARIANTS[variant]
    qdrant.create_collection(
        COLLECTION,
        vectors_config=models.VectorParams(size=dim, distance=models.Distance.DOT,
                                           memory=models.Memory(settings["memory"]) if "memory" in settings else None),
        hnsw_config=models.HnswConfigDiff(**settings.get("hnsw", {})) if "hnsw" in settings else None,
        optimizers_config=(models.OptimizersConfigDiff(**settings["optimizers"])
                           if "optimizers" in settings else None),
        wal_config=models.WalConfigDiff(wal_capacity_mb=settings["wal"]) if "wal" in settings else None,
        strict_mode_config=models.StrictModeConfig(enabled=False))
    qdrant.create_payload_index(COLLECTION, "libraries", models.KeywordIndexParams(
        type=models.KeywordIndexType.KEYWORD, is_tenant=True if settings.get("tenant") else None), wait=True)


def upload(qdrant, matrix, libraries):
    from qdrant_client import models

    for start in range(0, len(matrix), BATCH):
        stop = min(len(matrix), start + BATCH)
        qdrant.upsert(COLLECTION, models.Batch(ids=list(range(start, stop)), vectors=matrix[start:stop].tolist(),
                                               payloads=[{"libraries": libraries[i]} for i in range(start, stop)]),
                      wait=True)


def searches(variant):
    """(name, limit, search params) the variant is queried with."""
    from qdrant_client import models

    plans = [("default", SMALL_LIMIT, None), ("default", LARGE_LIMIT, None),
             ("exact", SMALL_LIMIT, models.SearchParams(exact=True)),
             ("exact", LARGE_LIMIT, models.SearchParams(exact=True))]
    if VARIANTS[variant].get("searches"):
        # Qdrant searches with max(hnsw_ef, limit), so only ef above the limit changes anything.
        plans += [(f"ef{ef}", SMALL_LIMIT, models.SearchParams(hnsw_ef=ef)) for ef in (64, 128, 256, 512)]
        plans += [(f"ef{ef}", LARGE_LIMIT, models.SearchParams(hnsw_ef=ef)) for ef in (1000, 2000)]
        plans += [("acorn", SMALL_LIMIT, models.SearchParams(acorn=models.AcornSearchParams(enable=True)))]
    return plans


def member_filter(libraries):
    from qdrant_client import models

    if libraries is None:
        return None
    return models.Filter(must=[models.FieldCondition(key="libraries", match=models.MatchAny(any=libraries))])


def run_queries(qdrant, variant, matrix, masks, queries):
    """{member: {search@limit: recall@10/50 or @500 and latency}}."""
    results = {}
    exact_all = [(vector @ matrix.T, own) for vector, own in queries]
    for member, libraries in MEMBERS.items():
        allowed = masks[member]
        query_filter = member_filter(libraries)
        per_member = {}
        for name, limit, params in searches(variant):
            took, recalls = [], {k: [] for k in ((10, 50) if limit == SMALL_LIMIT else (100, 500))}
            for (vector, own), (exact, _own) in zip(queries, exact_all):
                ms, response = common.timed_ms(qdrant.query_points, COLLECTION, query=vector.tolist(),
                                               query_filter=query_filter, search_params=params,
                                               limit=limit + (own is not None), with_payload=False)
                took.append(ms)
                found = [p.id for p in response.points if p.id != own][:limit]
                if own is not None:
                    exact = exact.copy()
                    exact[own] = -np.inf
                for k, values in recalls.items():
                    value = common.recall(np.array(found), exact, allowed, k)
                    if value is not None:
                        values.append(value)
            per_member[f"{name}@{limit}"] = {
                **{f"recall@{k}": round(float(np.mean(v)), 4) for k, v in recalls.items() if v},
                **{f"min@{k}": round(float(np.min(v)), 4) for k, v in recalls.items() if v},
                "latencyMs": common.latency(took)}
        results[member] = per_member
    return results


def numpy_baseline(matrix, masks, queries):
    """One-thread numpy over each member's photos, as the photo service scans: dot products, then the top 500."""
    results = {}
    with common.one_thread():
        for member, allowed in masks.items():
            subset = matrix[allowed] if allowed is not None else matrix
            took = []
            for vector, _own in queries:
                began = time.perf_counter()
                scores = subset @ vector
                top = (np.argpartition(-scores, LARGE_LIMIT)[:LARGE_LIMIT] if len(scores) > LARGE_LIMIT
                       else np.arange(len(scores)))
                _ranked = top[np.argsort(-scores[top])]
                took.append((time.perf_counter() - began) * 1000)
            results[member] = {"photos": len(subset), "latencyMs": common.latency(took)}
    return results


def run_variant(report, tier, variant, matrix, libraries, masks, queries):
    entry = {}
    with common.Server(f"filtered-{tier}-{variant}", VARIANTS[variant].get("server")) as server:
        entry["start"] = server.start()
        qdrant = common.client()
        create(qdrant, variant, matrix.shape[1])
        began = time.perf_counter()
        upload(qdrant, matrix, libraries)
        entry["uploadS"] = round(time.perf_counter() - began, 2)
        entry["optimizeS"] = round(common.wait_indexed(qdrant, COLLECTION), 2)
        entry["collection"] = common.collection_facts(qdrant, COLLECTION)
        entry["memoryAfterBuild"] = server.memory()
        entry["disk"] = server.disk(COLLECTION)
        report.put(f"{tier}/{variant}", entry)
        entry["roundTripFloorMs"] = common.round_trip_ms(qdrant, COLLECTION)
        entry["queries"] = run_queries(qdrant, variant, matrix, masks, queries)
        entry["memoryAfterQueries"] = server.memory()
        report.put(f"{tier}/{variant}", entry)
        qdrant.close()
        first_vector = queries[0][0].tolist()
        first_filter = member_filter(MEMBERS["a"])
        entry["coldStart"] = common.cold_start(server, lambda q: q.query_points(
            COLLECTION, query=first_vector, query_filter=first_filter, limit=SMALL_LIMIT, with_payload=False))
        entry["diskAfterRestart"] = server.disk(COLLECTION)
        qdrant = common.client()
        entry["collectionAfterRestart"] = common.collection_facts(qdrant, COLLECTION)
        qdrant.close()
        report.put(f"{tier}/{variant}", entry)


def main():
    parser = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    parser.add_argument("--tiers", default="20k,all,100k")
    parser.add_argument("--variants", default=",".join(VARIANTS))
    parser.add_argument("--full-variants-on", default="20k,100k",
                        help="tiers that run every variant; the others run default and hnsw only")
    args = parser.parse_args()
    report = common.Report("filtered")
    keys, real = common.image_vectors()
    texts, text_matrix = common.caption_queries(TEXT_QUERIES)
    report.put("data", {"distinctPhotos": len(real), "textQueries": len(texts), "imageQueries": IMAGE_QUERIES,
                        "members": MEMBERS, "weights": WEIGHTS, "secondLibrary": SECOND_LIBRARY,
                        "smallLibrary": SMALL_LIBRARY})
    for tier in args.tiers.split(","):
        matrix, about = tier_data(tier, keys, real)
        libraries = assign_libraries(len(matrix))
        masks = {member: (None if libs is None else
                          np.array([any(lib in libs for lib in found) for found in libraries]))
                 for member, libs in MEMBERS.items()}
        about["visible"] = {member: int(mask.sum()) if mask is not None else len(matrix)
                            for member, mask in masks.items()}
        image_rows = common.stable_sample(range(len(matrix)), IMAGE_QUERIES, f"images-{tier}")
        queries = [(vector, None) for vector in text_matrix] + [(matrix[row], row) for row in image_rows]
        about["numpy"] = numpy_baseline(matrix, masks, queries)
        report.put(f"{tier}/data", about)
        variants = args.variants.split(",")
        if tier not in args.full_variants_on.split(","):
            variants = [v for v in variants if v in ("default", "hnsw")]
        for variant in variants:
            print(f"{tier} {variant}", flush=True)
            run_variant(report, tier, variant, matrix, libraries, masks, queries)


if __name__ == "__main__":
    main()
