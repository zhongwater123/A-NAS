"""Every face above a threshold: Qdrant's threshold queries and search matrix against numpy.

Development only (docs/research/qdrant-retrieval-features.md, experiment 6).
Clustering faces (internal/photos/people.go) needs, for each face, all the
faces at least as similar as the threshold, not a nearest few; #66 found HNSW
unsuited to that. Two sets:

- synthetic, 30,000 512-d faces of 600 people whose sizes follow a Zipf law
  (exponent 0.8: the largest about 2,200 faces, the median about 23, as in
  #66), drawn from LFW's AuraFace statistics: person centres from the
  principal components of the spread between people, faces around them from
  those of the spread within a person, plus isotropic rest, scaled so that two
  faces of one person have LFW's median cosine;
- LFW itself, every AuraFace vector the face cache holds (about 17k).

Both use the product's AuraFace threshold. numpy is the reference (all pairs in
blocks, each pair once). Qdrant answers by batched queries by point id with
score_threshold, 256 per page and offset paging while a page comes back full,
on its default index and with HNSW forced; and by search_matrix_pairs over
every point, which has no threshold and a fixed limit per point. New faces are
timed one by one against a one-row numpy product.

    python -m eval.qdrant_faces [--faces 30000]
"""

import argparse
import collections
import os
import time

import numpy as np

from eval import datasets, qdrant_common as common

FACES = os.path.join(datasets.ROOT, "cache", "faces", "face_detection_yunet_2026may.onnx-dfull-s0.5", "lfw.npz")
THRESHOLD = 0.48  # internal/photos/people.go, AuraFace
PEOPLE, ZIPF, COMPONENTS = 600, 0.8, 64
PAGE, BATCH, NEW_FACES = 256, 64, 200
MATRIX_LIMIT = 64


def lfw():
    """(vectors, person per row or None): AuraFace vectors of LFW; a person only for photos with one face."""
    with np.load(FACES, allow_pickle=False) as data:
        keys, counts = data["keys"].tolist(), data["counts"].tolist()
        vectors, missing = data["v_auraface"], data["m_auraface"]
    rows, people, start = [], [], 0
    for index, (key, count) in enumerate(zip(keys, counts)):
        if not missing[index]:
            for offset in range(count):
                rows.append(start + offset)
                people.append(key.split(":", 1)[1].rsplit("_", 1)[0] if count == 1 else None)
        start += count
    return vectors[rows].astype(np.float32), people


def synthesize(vectors, people, total, seed=17):
    """(faces, person per face) drawn from LFW's between- and within-person spread.

    The spread within a person is scaled once so that the median cosine of two
    faces of one person matches LFW's.
    """
    model = fit(vectors, people)
    target = similarity_summary(vectors, people)["sameMedian"]
    low, high = 0.2, 2.0
    for _ in range(20):
        middle = (low + high) / 2
        faces, owner = draw(model, 2000, 100, middle, np.random.default_rng(seed + 1))
        if similarity_summary(faces, owner.tolist())["sameMedian"] > target:
            low = middle
        else:
            high = middle
    faces, owner = draw(model, total, PEOPLE, (low + high) / 2, np.random.default_rng(seed))
    return faces, owner, round((low + high) / 2, 3)


def fit(vectors, people):
    """LFW's centre, the principal spread between people and within a person, and the isotropic rest."""
    groups = collections.defaultdict(list)
    for row, person in enumerate(people):
        if person is not None:
            groups[person].append(row)
    groups = {person: rows for person, rows in groups.items() if len(rows) >= 2}
    means = np.stack([vectors[rows].mean(axis=0) for rows in groups.values()])
    centre = means.mean(axis=0)
    _u, spread, between = np.linalg.svd(means - centre, full_matrices=False)
    between_scale = spread[:COMPONENTS] / np.sqrt(len(means) - 1)
    residuals = np.concatenate([vectors[rows] - vectors[rows].mean(axis=0) for rows in groups.values()])
    _u, spread, within = np.linalg.svd(residuals, full_matrices=False)
    within_scale = spread[:COMPONENTS] / np.sqrt(len(residuals) - len(groups))
    rest = np.sqrt((spread[COMPONENTS:] ** 2).sum() / (len(residuals) - len(groups)) / vectors.shape[1])
    return centre, between[:COMPONENTS], between_scale, within[:COMPONENTS], within_scale, rest


def draw(model, total, people, scale, rng):
    """total faces of `people` people with Zipf sizes; the within-person spread times scale."""
    centre, between, between_scale, within, within_scale, rest = model
    weights = 1 / np.arange(1, people + 1) ** ZIPF
    sizes = np.maximum(1, np.round(weights / weights.sum() * total)).astype(int)
    sizes[0] += total - sizes.sum()
    faces, owner = [], []
    for person, size in enumerate(sizes):
        middle = centre + (rng.normal(size=len(between)) * between_scale) @ between
        found = middle + scale * ((rng.normal(size=(size, len(within))) * within_scale) @ within
                                  + rng.normal(size=(size, len(centre))) * rest)
        faces.append(found)
        owner.extend([person] * size)
    faces = np.concatenate(faces)
    return (faces / np.linalg.norm(faces, axis=1, keepdims=True)).astype(np.float32), np.array(owner)


def similarity_summary(vectors, people, pairs=200_000, seed=19):
    """Same-person and different-person cosine quantiles over random pairs of faces with a known person."""
    rng = np.random.default_rng(seed)
    known = np.array([row for row, person in enumerate(people) if person is not None])
    by_person = collections.defaultdict(list)
    for row in known:
        by_person[people[row]].append(row)
    same = []
    for rows in by_person.values():
        if len(rows) >= 2:
            chosen = vectors[rows[:60]]
            same.extend((chosen @ chosen.T)[np.triu_indices(len(chosen), 1)].tolist())
    left, right = rng.choice(known, pairs), rng.choice(known, pairs)
    different = np.einsum("ij,ij->i", vectors[left], vectors[right])[[people[a] != people[b] for a, b in zip(left, right)]]
    return {"sameMedian": round(float(np.median(same)), 4), "sameP10": round(float(np.percentile(same, 10)), 4),
            "differentMedian": round(float(np.median(different)), 4),
            "differentP999": round(float(np.percentile(different, 99.9)), 4),
            "differentAboveThreshold": round(float((different >= THRESHOLD).mean()), 5)}


def all_pairs(vectors, threshold, block=1024):
    """Sorted codes i * n + j (i < j) of every pair at or above the threshold."""
    count, found = len(vectors), []
    for start in range(0, count, block):
        scores = vectors[start:start + block] @ vectors.T
        rows, columns = np.nonzero(scores >= threshold)
        rows += start
        keep = columns > rows
        found.append(rows[keep].astype(np.int64) * count + columns[keep])
    return np.sort(np.concatenate(found))


def pair_recall(found, truth, count):
    """Share of true pairs found, and of faces whose every true neighbour was found."""
    found = np.unique(found)
    hit = np.isin(truth, found, assume_unique=True)
    degree = np.bincount(np.concatenate([truth // count, truth % count]), minlength=count)
    missed = truth[~hit]
    lacking = np.bincount(np.concatenate([missed // count, missed % count]), minlength=count)
    has_neighbours = degree > 0
    return {"pairs": round(float(hit.mean()), 4) if len(truth) else None,
            "facesComplete": round(float((lacking[has_neighbours] == 0).mean()), 4),
            "extraPairs": len(np.setdiff1d(found, truth, assume_unique=True))}


def threshold_pass(qdrant, count, params):
    """Every face's neighbours at or above the threshold, by batched id queries with offset paging."""
    from qdrant_client import models

    found, requests, pages = [], 0, 0
    pending = [(point, 0) for point in range(count)]
    while pending:
        batch, pending = pending[:BATCH], pending[BATCH:]
        answers = qdrant.query_batch_points("faces", [models.QueryRequest(
            query=point, score_threshold=THRESHOLD, limit=PAGE, offset=offset, params=params)
            for point, offset in batch])
        requests += 1
        for (point, offset), answer in zip(batch, answers):
            pages += 1
            ids = [p.id for p in answer.points if p.id != point]
            found.extend(min(point, other) * count + max(point, other) for other in ids)
            if len(answer.points) == PAGE:
                pending.append((point, offset + PAGE))
    return np.array(found, dtype=np.int64), {"batches": requests, "pages": pages}


def run_set(report, name, vectors):
    from qdrant_client import models

    held = np.array(common.stable_sample(range(len(vectors)), NEW_FACES, f"new-{name}"))
    stored = np.setdiff1d(np.arange(len(vectors)), held)
    library, newcomers = vectors[stored], vectors[held]
    entry = {"faces": len(library)}
    with common.one_thread():
        began = time.perf_counter()
        truth = all_pairs(library, THRESHOLD)
        entry["numpyOneThreadS"] = round(time.perf_counter() - began, 2)
        took = []
        for face in newcomers:
            ms, _near = common.timed_ms(lambda v: np.flatnonzero(library @ v >= THRESHOLD), face)
            took.append(ms)
        entry["numpyNewFaceMs"] = common.latency(took)
    began = time.perf_counter()
    all_pairs(library, THRESHOLD)
    entry["numpyAllThreadsS"] = round(time.perf_counter() - began, 2)
    degree = np.bincount(np.concatenate([truth // len(library), truth % len(library)]), minlength=len(library))
    entry["truth"] = {"pairs": len(truth), "meanNeighbours": round(float(degree.mean()), 1),
                      "maxNeighbours": int(degree.max()), "facesWithMoreThanAPage": int((degree >= PAGE).sum())}
    report.put(name, entry)
    for index in ("default", "hnsw"):
        result = {}
        optimizers = models.OptimizersConfigDiff(indexing_threshold=1) if index == "hnsw" else None
        hnsw = models.HnswConfigDiff(full_scan_threshold=10) if index == "hnsw" else None
        with common.Server(f"faces-{name}-{index}") as server:
            server.start()
            qdrant = common.client()
            qdrant.create_collection("faces", vectors_config=models.VectorParams(size=vectors.shape[1],
                                                                                distance=models.Distance.DOT),
                                     optimizers_config=optimizers, hnsw_config=hnsw,
                                     strict_mode_config=models.StrictModeConfig(enabled=False))
            for start in range(0, len(library), 1024):
                stop = min(len(library), start + 1024)
                qdrant.upsert("faces", models.Batch(ids=list(range(start, stop)),
                                                    vectors=library[start:stop].tolist()), wait=True)
            result["optimizeS"] = round(common.wait_indexed(qdrant, "faces"), 2)
            result["collection"] = common.collection_facts(qdrant, "faces")
            searches = {"planner": None, "exact": models.SearchParams(exact=True)}
            if index == "hnsw":
                searches = {"ef128": models.SearchParams(hnsw_ef=128), "ef512": models.SearchParams(hnsw_ef=512)}
            for search, params in searches.items():
                began = time.perf_counter()
                found, work = threshold_pass(qdrant, len(library), params)
                result[f"threshold/{search}"] = {"seconds": round(time.perf_counter() - began, 2), **work,
                                                 "recall": pair_recall(found, truth, len(library))}
                report.put(f"{name}/{index}", result)
                took, complete = [], 0
                for face in newcomers:
                    want = set(np.flatnonzero(library @ face >= THRESHOLD).tolist())
                    began, got, offset = time.perf_counter(), set(), 0
                    while True:
                        page = qdrant.query_points("faces", query=face.tolist(), score_threshold=THRESHOLD,
                                                   limit=PAGE, offset=offset, search_params=params).points
                        got.update(p.id for p in page)
                        if len(page) < PAGE:
                            break
                        offset += PAGE
                    took.append((time.perf_counter() - began) * 1000)
                    complete += got >= want
                result[f"newFace/{search}"] = {"latencyMs": common.latency(took),
                                               "complete": round(complete / len(newcomers), 4)}
                report.put(f"{name}/{index}", result)
            began = time.perf_counter()
            matrix = qdrant.search_matrix_pairs("faces", sample=len(library), limit=MATRIX_LIMIT, timeout=3600)
            seconds = time.perf_counter() - began
            codes = np.array([min(p.a, p.b) * len(library) + max(p.a, p.b) for p in matrix.pairs
                              if p.score >= THRESHOLD], dtype=np.int64)
            result[f"matrix/limit{MATRIX_LIMIT}"] = {"seconds": round(seconds, 2), "pairsReturned": len(matrix.pairs),
                                                     "recall": pair_recall(codes, truth, len(library))}
            result["memory"] = server.memory()
            result["disk"] = server.disk("faces")
            report.put(f"{name}/{index}", result)


def main():
    parser = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    parser.add_argument("--faces", type=int, default=30000)
    parser.add_argument("--sets", default="synthetic,lfw")
    args = parser.parse_args()
    report = common.Report("faces")
    vectors, people = lfw()
    synthetic, owner, scale = synthesize(vectors, people, args.faces)
    report.put("data", {
        "threshold": THRESHOLD, "page": PAGE, "batch": BATCH,
        "lfw": {"faces": len(vectors), **similarity_summary(vectors, people)},
        "synthetic": {"faces": len(synthetic), "people": PEOPLE, "zipf": ZIPF, "withinScale": scale,
                      "largestPerson": int(np.bincount(owner).max()), "medianPerson": float(np.median(np.bincount(owner))),
                      **similarity_summary(synthetic, [int(o) for o in owner])}})
    sets = {"synthetic": synthetic, "lfw": vectors}
    for name in args.sets.split(","):
        print(name, flush=True)
        run_set(report, name, sets[name])


if __name__ == "__main__":
    main()
