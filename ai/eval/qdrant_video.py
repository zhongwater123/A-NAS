"""Video segments in Qdrant: three layouts against "a video scores its best segment".

Development only (docs/research/qdrant-retrieval-features.md, experiment 2).
There are no video vectors yet, so pseudo-videos stand in: each is a random
walk over the 20-nearest-neighbour graph of the cached photo vectors, 5 to 200
steps long (log-uniform), so that consecutive segments look alike as a
video's do. Query photos are held out of every walk; queries are those photos
and COCO-CN captions through the retrieval prompt.

The reference is numpy: a video's score is its best segment's dot product.
Layouts:

- multivector: one point per video, the segments as a `max_sim` multivector,
  with no HNSW graph (m = 0) and with the default graph;
- groups: one point per segment with a keyword `video_id`, searched with
  query/groups (group_size 1), with Qdrant's default index and HNSW forced,
  each also with exact search;
- pooled: one point per video with a normalised mean vector (HNSW) that a
  prefetch searches, then a MaxSim rescore of the prefetched videos on their
  segments (no graph).

Each reports recall@10/100 of videos, the largest score difference from the
reference, latency, upload and optimisation time, cold start, RSS and disk.

    python -m eval.qdrant_video [--videos 600] [--layouts multivector,groups,...]
"""

import argparse
import time

import numpy as np

from eval import qdrant_common as common
from eval.qdrant_filtered import nearest

TEXT_QUERIES, IMAGE_QUERIES = 300, 100
MIN_SEGMENTS, MAX_SEGMENTS, NEIGHBOURS = 5, 200, 20
LIMITS = (10, 100)
PREFETCH = (20, 50, 100, 200)
FORCED_HNSW = {"indexing_threshold": 1}


def walks(graph, count, seed=5):
    """count lists of node indices: random walks over the graph, never revisiting a node within a walk."""
    rng = np.random.default_rng(seed)
    lengths = np.exp(rng.uniform(np.log(MIN_SEGMENTS), np.log(MAX_SEGMENTS + 1), size=count)).astype(int)
    lengths = np.clip(lengths, MIN_SEGMENTS, MAX_SEGMENTS)
    videos = []
    for length in lengths:
        node = int(rng.integers(len(graph)))
        walk, seen = [node], {node}
        while len(walk) < length:
            options = [int(n) for n in graph[node] if int(n) not in seen]
            node = int(rng.choice(options)) if options else int(rng.integers(len(graph)))
            if node in seen:
                continue
            walk.append(node)
            seen.add(node)
        videos.append(walk)
    return videos


def reference(segments, owner, queries, video_count):
    """(queries x videos) best segment dot product per video."""
    order = np.argsort(owner, kind="stable")
    starts = np.flatnonzero(np.r_[True, np.diff(owner[order]) != 0])
    scores = queries @ segments[order].T
    return np.maximum.reduceat(scores, starts, axis=1)[:, :video_count]


def measure(name, run, best, entry):
    """Recall@k of videos and the largest score difference from the reference, with latency per limit."""
    results = {}
    for limit in LIMITS:
        took, recalls, worst = [], [], 0.0
        for row, expected in enumerate(best):
            ms, found = common.timed_ms(run, row, limit)
            took.append(ms)
            ids = np.array([video for video, _score in found])
            recalls.append(common.recall(ids, expected, None, limit))
            if len(found):
                worst = max(worst, max(abs(score - expected[video]) for video, score in found))
        results[f"@{limit}"] = {"recall": round(float(np.mean(recalls)), 4), "min": round(float(np.min(recalls)), 4),
                                "maxScoreDiff": float(worst), "latencyMs": common.latency(took)}
    entry.setdefault("queries", {})[name] = results


def build(server, layout, segments, owner, videos, entry):
    from qdrant_client import models

    qdrant = common.client()
    dim = segments.shape[1]
    multi = models.MultiVectorConfig(comparator=models.MultiVectorComparator.MAX_SIM)
    no_graph = models.HnswConfigDiff(m=0)
    strict = models.StrictModeConfig(enabled=False)
    began = time.perf_counter()
    if layout.startswith("multivector"):
        graph = None if layout == "multivector-hnsw" else no_graph
        qdrant.create_collection("videos", vectors_config={"segments": models.VectorParams(
            size=dim, distance=models.Distance.DOT, multivector_config=multi, hnsw_config=graph)},
            strict_mode_config=strict)
        for start in range(0, len(videos), 16):
            chosen = range(start, min(len(videos), start + 16))
            qdrant.upsert("videos", [models.PointStruct(id=v, vector={"segments": segments[videos[v]].tolist()})
                                     for v in chosen], wait=True)
    elif layout.startswith("groups"):
        optimizers = models.OptimizersConfigDiff(**FORCED_HNSW) if layout == "groups-hnsw" else None
        qdrant.create_collection("segments", vectors_config=models.VectorParams(size=dim, distance=models.Distance.DOT),
                                 optimizers_config=optimizers, strict_mode_config=strict)
        qdrant.create_payload_index("segments", "video_id", models.PayloadSchemaType.KEYWORD, wait=True)
        for start in range(0, len(owner), 512):
            stop = min(len(owner), start + 512)
            qdrant.upsert("segments", models.Batch(ids=list(range(start, stop)), vectors=segments[start:stop].tolist(),
                                                   payloads=[{"video_id": f"v{owner[i]}"} for i in range(start, stop)]),
                          wait=True)
    else:
        qdrant.create_collection("videos", vectors_config={
            "mean": models.VectorParams(size=dim, distance=models.Distance.DOT),
            "segments": models.VectorParams(size=dim, distance=models.Distance.DOT, multivector_config=multi,
                                            hnsw_config=no_graph)},
            optimizers_config=models.OptimizersConfigDiff(**FORCED_HNSW), strict_mode_config=strict)
        for start in range(0, len(videos), 16):
            points = []
            for video in range(start, min(len(videos), start + 16)):
                rows = segments[videos[video]]
                mean = rows.mean(axis=0)
                points.append(models.PointStruct(id=video, vector={"mean": (mean / np.linalg.norm(mean)).tolist(),
                                                                   "segments": rows.tolist()}))
            qdrant.upsert("videos", points, wait=True)
    entry["uploadS"] = round(time.perf_counter() - began, 2)
    collection = "segments" if layout.startswith("groups") else "videos"
    entry["optimizeS"] = round(common.wait_indexed(qdrant, collection), 2)
    entry["collection"] = common.collection_facts(qdrant, collection)
    entry["memoryAfterBuild"] = server.memory()
    entry["disk"] = server.disk(collection)
    return qdrant, collection


def searches(layout, qdrant, queries):
    """{name: run(query row, limit) -> [(video, score)]} for the layout."""
    from qdrant_client import models

    def multivector(row, limit):
        points = qdrant.query_points("videos", query=[queries[row].tolist()], using="segments", limit=limit,
                                     with_payload=False).points
        return [(p.id, p.score) for p in points]

    def groups(params):
        def run(row, limit):
            result = qdrant.query_points_groups("segments", query=queries[row].tolist(), group_by="video_id",
                                               group_size=1, limit=limit, with_payload=False, search_params=params)
            return [(int(str(g.id)[1:]), g.hits[0].score) for g in result.groups]
        return run

    def pooled(size):
        def run(row, limit):
            points = qdrant.query_points("videos", prefetch=models.Prefetch(
                query=queries[row].tolist(), using="mean", limit=max(size, limit)),
                query=[queries[row].tolist()], using="segments", limit=limit, with_payload=False).points
            return [(p.id, p.score) for p in points]
        return run

    if layout.startswith("multivector"):
        return {"maxsim": multivector}
    if layout.startswith("groups"):
        return {"groups": groups(None), "groups-exact": groups(models.SearchParams(exact=True))}
    return {f"prefetch{size}": pooled(size) for size in PREFETCH}


def main():
    parser = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    parser.add_argument("--videos", type=int, default=600)
    parser.add_argument("--pool", type=int, help="walk over only this many photos (a smoke test)")
    parser.add_argument("--layouts", default="multivector,multivector-hnsw,groups,groups-hnsw,pooled")
    args = parser.parse_args()
    report = common.Report("video")
    _keys, real = common.image_vectors()
    held = common.stable_sample(range(len(real)), IMAGE_QUERIES, "video-queries")
    pool = np.setdiff1d(np.arange(len(real)), held)[:args.pool]
    graph = nearest(real[pool], NEIGHBOURS)
    videos = [[int(pool[node]) for node in walk] for walk in walks(graph, args.videos)]
    owner = np.concatenate([np.full(len(walk), video) for video, walk in enumerate(videos)])
    segments = real[np.concatenate(videos)]
    flat_videos, start = [], 0
    for walk in videos:
        flat_videos.append(list(range(start, start + len(walk))))
        start += len(walk)
    _texts, text_matrix = common.caption_queries(TEXT_QUERIES)
    queries = np.concatenate([text_matrix, real[held]])
    best = reference(segments, owner, queries, len(videos))
    lengths = [len(walk) for walk in videos]
    report.put("data", {"videos": len(videos), "segments": len(segments), "queries": len(queries),
                        "segmentsPerVideo": {"min": min(lengths), "median": float(np.median(lengths)),
                                             "max": max(lengths)},
                        "distinctPhotosUsed": len(np.unique(np.concatenate(videos)))})
    with common.one_thread():
        took = []
        for query in queries:
            began = time.perf_counter()
            scores = segments @ query
            per_video = np.maximum.reduceat(scores, np.r_[0, np.cumsum(lengths)[:-1]])
            _top = np.argsort(-per_video)[:10]
            took.append((time.perf_counter() - began) * 1000)
    report.put("numpy", {"latencyMs": common.latency(took)})
    for layout in args.layouts.split(","):
        print(layout, flush=True)
        entry = {}
        with common.Server(f"video-{layout}") as server:
            entry["start"] = server.start()
            qdrant, collection = build(server, layout, segments, owner, flat_videos, entry)
            report.put(layout, entry)
            entry["roundTripFloorMs"] = common.round_trip_ms(qdrant, collection)
            for name, run in searches(layout, qdrant, queries).items():
                measure(name, run, best, entry)
                report.put(layout, entry)
            entry["memoryAfterQueries"] = server.memory()
            qdrant.close()
            entry["coldStart"] = common.cold_start(
                server, lambda fresh, layout=layout: next(iter(searches(layout, fresh, queries).values()))(0, 10))
            report.put(layout, entry)


if __name__ == "__main__":
    main()
