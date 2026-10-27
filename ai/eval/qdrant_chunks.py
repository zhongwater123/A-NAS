"""File chunks in Qdrant: BM25 on a multilingual full-text index, dense vectors, and their fusions.

Development only (docs/research/qdrant-retrieval-features.md, experiment 3).
The corpus is the repository's own Chinese docs at a fixed commit, cut by
eval.qdrant_docs; the 42 questions name the section that answers each. One
collection holds both sides: a full-text index on the chunk's `text` with the
`multilingual` tokenizer and BM25 scoring, queried with a text query, and the
EmbeddingGemma 2 vectors that eval.qdrant_embed_docs cached.

Rankings: BM25 alone, dense alone, and both prefetched (50 each) and fused
with RRF at Qdrant's default k, RRF at k = 60 and DBSF; each also grouped by
file (query/groups on `path`, one chunk per file). Every fused ranking is
recomputed here from the two prefetch lists, and the dense one from numpy.

The tokenizer's Chinese words are read back on a sample: the chunks go through
Qdrant's local BM25 inference (the same tokenizer) into a sparse vector, whose
indices are murmur3 hashes of the tokens; hashing every substring of the text
(also spelt with the variant characters charabia's Chinese normalization was
seen to produce) finds which words they are.

    python -m eval.qdrant_chunks --docs /qe/docs-6f25568/docs
"""

import argparse
import os
import re
import time

import numpy as np

from eval import datasets, qdrant_common as common, qdrant_docs
from eval.embed import Cache

MODEL = "embeddinggemma-2-740m@e7a8a2204b91+mediapipe-1.1.0+tok70+l2"
PREFETCH = 50
LIMIT = 10
SAMPLE_CHUNKS, SAMPLE_QUESTIONS = 8, 6
# Short probes of the tokenizer's Chinese handling, beside the sampled chunks and questions.
PROBES = ("南京市长江大桥", "实现最简单，但产品服务会读到整台机器的日志")
# charabia's chinese-normalization rewrites some simplified characters to another form
# before the token is hashed; these are the ones the probes above were seen to produce.
VARIANTS = {"长": "長", "桥": "橋", "实": "實", "现": "現", "简": "簡", "单": "單", "务": "務", "会": "會",
            "读": "讀", "为": "為", "么": "麼", "传": "傳", "时": "時", "吗": "嗎", "视": "視", "剧": "劇"}


def build(qdrant, chunks, vectors):
    from qdrant_client import models

    qdrant.create_collection("chunks", vectors_config={
        "dense": models.VectorParams(size=vectors.shape[1], distance=models.Distance.DOT)},
        strict_mode_config=models.StrictModeConfig(enabled=False))
    qdrant.create_payload_index("chunks", "text", models.TextIndexParams(
        type=models.TextIndexType.TEXT, tokenizer=models.TokenizerType.MULTILINGUAL, lowercase=True, scoring=True),
        wait=True)
    qdrant.create_payload_index("chunks", "path", models.PayloadSchemaType.KEYWORD, wait=True)
    for start in range(0, len(chunks), 256):
        stop = min(len(chunks), start + 256)
        qdrant.upsert("chunks", [models.PointStruct(id=i, vector={"dense": vectors[i].tolist()}, payload={
            "text": chunks[i].text, "path": chunks[i].path, "chunk": chunks[i].id}) for i in range(start, stop)],
            wait=True)


def methods():
    """{name: (prefetches or None, query builder)}: query builders take (text, vector)."""
    from qdrant_client import models

    def bm25(text, _vector, limit):
        return {"query": models.TextQuery(text=text), "using": "text", "limit": limit}

    def dense(_text, vector, limit):
        return {"query": vector.tolist(), "using": "dense", "limit": limit}

    def fused(fusion):
        def build_query(text, vector, limit):
            return {"prefetch": [models.Prefetch(query=models.TextQuery(text=text), using="text", limit=PREFETCH),
                                 models.Prefetch(query=vector.tolist(), using="dense", limit=PREFETCH)],
                    "query": fusion, "limit": limit}
        return build_query

    return {"bm25": bm25, "dense": dense,
            "rrf (k=2, default)": fused(models.FusionQuery(fusion=models.Fusion.RRF)),
            "rrf k=60": fused(models.RrfQuery(rrf=models.Rrf(k=60))),
            "dbsf": fused(models.FusionQuery(fusion=models.Fusion.DBSF))}


def expected_fusion(name, bm25, dense):
    """Fused (id, score) best first, recomputed from the two prefetch lists as Qdrant 1.20 defines them."""
    scores = {}
    if name.startswith("rrf"):
        k = 60 if "60" in name else 2
        for points in (bm25, dense):
            for position, (point, _score) in enumerate(points):
                scores[point] = scores.get(point, 0.0) + 1.0 / (position + k)
    else:
        for points in (bm25, dense):
            if len(points) < 2:
                for point, _score in points:
                    scores[point] = scores.get(point, 0.0) + 0.5
                continue
            values = np.array([score for _point, score in points], dtype=np.float64)
            low, high = values.mean() - 3 * values.std(ddof=1), values.mean() + 3 * values.std(ddof=1)
            for point, score in points:
                scores[point] = scores.get(point, 0.0) + (score - low) / (high - low)
    return sorted(scores.items(), key=lambda item: -item[1])


def compare(found, expected):
    """(largest score difference, whether the lists agree): the same ids above the last place's
    score, any order among tied scores."""
    worst = max((abs(a[1] - b[1]) for a, b in zip(found, expected)), default=0.0)
    if len(found) != len(expected):
        return worst, False
    last = expected[-1][1] if expected else 0.0
    certain = {point for point, score in expected if score > last + common.TIE}
    return worst, certain <= {point for point, _score in found} and worst < 1e-4


def run_methods(qdrant, chunks, vectors, queries, query_vectors):
    rankings, results = {}, {}
    plain = {"bm25": [], "dense": []}
    for name, build_query in methods().items():
        took, ranked, grouped, mismatches, worst = [], [], [], 0, 0.0
        for number, query in enumerate(queries):
            arguments = build_query(query["q"], query_vectors[number], LIMIT)
            ms, response = common.timed_ms(qdrant.query_points, "chunks", with_payload=False, **arguments)
            took.append(ms)
            found = [(p.id, p.score) for p in response.points]
            ranked.append([point for point, _score in found])
            groups = qdrant.query_points_groups("chunks", group_by="path", group_size=1, with_payload=False,
                                                **{**arguments, "limit": 5})
            grouped.append([str(group.id) for group in groups.groups])
            if name in plain:
                wide = qdrant.query_points("chunks", with_payload=False,
                                           **build_query(query["q"], query_vectors[number], PREFETCH)).points
                plain[name].append([(p.id, p.score) for p in wide])
            else:
                expected = expected_fusion(name, plain["bm25"][number], plain["dense"][number])[:LIMIT]
                difference, agree = compare(found, expected)
                worst, mismatches = max(worst, difference), mismatches + (not agree)
        scores, ranks = qdrant_docs.hits(chunks, queries, ranked)
        file_scores, _file_ranks = qdrant_docs.hits(chunks, queries, ranked, files=grouped)
        results[name] = {**scores, "groupedFileHit@1": file_scores["fileHit@1"],
                         "groupedFileHit@5": file_scores["fileHit@5"], "latencyMs": common.latency(took)}
        if name not in plain:
            results[name]["recomputed"] = {"maxScoreDiff": worst, "queriesWithOtherOrder": mismatches}
        rankings[name] = ranks
    # Dense against numpy: the same top 10 (ties aside) and scores.
    exact = query_vectors @ vectors.T
    worst, same = 0.0, 0
    for number, ranked in enumerate(plain["dense"]):
        worst = max(worst, max(abs(score - exact[number][point]) for point, score in ranked))
        same += common.recall(np.array([point for point, _s in ranked]), exact[number], None, PREFETCH) == 1.0
    results["dense"]["numpy"] = {"maxScoreDiff": float(worst), "queriesWithSameTop50": same}
    # Paired against dense: which questions each ranking gets in its top 5 that dense does not, and back.
    for name, ranks in rankings.items():
        dense_ranks = rankings["dense"]
        results[name]["vsDenseTop5"] = {
            "onlyThis": sum(1 for a, b in zip(ranks, dense_ranks) if a is not None and a < 5 and (b is None or b >= 5)),
            "onlyDense": sum(1 for a, b in zip(ranks, dense_ranks) if b is not None and b < 5 and (a is None or a >= 5))}
    return results


def candidates(text):
    """Every substring a token could be: Chinese runs up to 8 characters, and whole ASCII-ish words."""
    text = text.lower()
    found = set(re.findall(r"[a-z0-9_]+", text))
    for run in re.findall(r"[^\x00-\x7f]+", text):
        for start in range(len(run)):
            for stop in range(start + 1, min(len(run), start + 8) + 1):
                found.add(run[start:stop])
                found.add("".join(VARIANTS.get(char, char) for char in run[start:stop]))
    for start in range(len(text)):
        for stop in range(start + 1, min(len(text), start + 12) + 1):
            found.add(text[start:stop])
    return found


def segmentation(chunks, queries):
    """Tokens of sample chunks and questions, read back from Qdrant's BM25 sparse vectors."""
    import mmh3

    options = {"tokenizer": "multilingual", "lowercase": True, "stemmer": {"type": "none"},
               "stopwords": {"custom": []}}
    status, created = common.rest("PUT", "/collections/tokens", {"sparse_vectors": {"bm25": {"modifier": "idf"}}})
    if status != 200:
        return {"error": created}
    sample = [chunks[i].text for i in common.stable_sample(range(len(chunks)), SAMPLE_CHUNKS, "segmentation")]
    texts = sample + [query["q"] for query in queries[:SAMPLE_QUESTIONS]] + list(PROBES)
    points = [{"id": i, "vector": {"bm25": {"text": text, "model": "qdrant/bm25", "options": options}}}
              for i, text in enumerate(texts)]
    status, answer = common.rest("PUT", "/collections/tokens/points?wait=true", {"points": points})
    if status != 200:
        return {"error": answer}
    status, stored = common.rest("POST", "/collections/tokens/points",
                                 {"ids": list(range(len(texts))), "with_vector": True, "with_payload": False})
    found = []
    for point in sorted(stored["result"], key=lambda p: p["id"]):
        text = texts[point["id"]]
        sparse = point["vector"]["bm25"]
        lookup = {abs(mmh3.hash(word.encode(), 0, signed=True)): word for word in candidates(text)}
        tokens = [lookup.get(index) for index in sparse["indices"]]
        simplified = {variant: original for original, variant in VARIANTS.items()}
        known = sorted((word for word in tokens if word), key=lambda word: text.lower().find(
            "".join(simplified.get(char, char) for char in word)))
        found.append({"text": text[:160], "tokens": known, "unknownTokens": tokens.count(None)})
    return {"options": options, "samples": found}


def main():
    parser = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    parser.add_argument("--docs", required=True, help="docs/ at the corpus commit")
    args = parser.parse_args()
    report = common.Report("chunks")
    chunks = qdrant_docs.chunk_markdown(args.docs)
    queries = qdrant_docs.load_queries()
    qdrant_docs.check_targets(chunks, queries)
    directory = os.path.join(datasets.ROOT, "cache", f"{MODEL}-docs-{qdrant_docs.CORPUS_COMMIT}")
    cached = Cache(os.path.join(directory, "chunks.npz")).vectors
    vectors = np.stack([cached[chunk.id] for chunk in chunks]).astype(np.float32)
    asked = Cache(os.path.join(directory, "queries.npz")).vectors
    query_vectors = np.stack([asked[query["q"]] for query in queries]).astype(np.float32)
    report.put("data", {"chunks": len(chunks), "files": len({c.path for c in chunks}), "queries": len(queries),
                        "commit": qdrant_docs.CORPUS_COMMIT, "prefetch": PREFETCH})
    entry = {}
    with common.Server("chunks") as server:
        entry["start"] = server.start()
        qdrant = common.client()
        began = time.perf_counter()
        build(qdrant, chunks, vectors)
        entry["uploadS"] = round(time.perf_counter() - began, 2)
        entry["optimizeS"] = round(common.wait_indexed(qdrant, "chunks"), 2)
        entry["collection"] = common.collection_facts(qdrant, "chunks")
        entry["disk"] = server.disk("chunks")
        entry["memoryAfterBuild"] = server.memory()
        entry["roundTripFloorMs"] = common.round_trip_ms(qdrant, "chunks")
        report.put("qdrant", entry)
        report.put("methods", run_methods(qdrant, chunks, vectors, queries, query_vectors))
        entry["memoryAfterQueries"] = server.memory()
        report.put("segmentation", segmentation(chunks, queries))
        qdrant.close()
        first = methods()["rrf (k=2, default)"](queries[0]["q"], query_vectors[0], LIMIT)
        entry["coldStart"] = common.cold_start(server, lambda fresh: fresh.query_points("chunks", **first))
        report.put("qdrant", entry)


if __name__ == "__main__":
    main()
