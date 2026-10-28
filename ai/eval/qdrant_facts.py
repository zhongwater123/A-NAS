"""Pins the Qdrant behaviours the retrieval trial relies on, on tiny collections.

Development only (docs/research/qdrant-retrieval-features.md). Each check
compares Qdrant's answer with a value computed here and exits non-zero when
one drifts, so the same run tells whether a Qdrant upgrade changed them:

- RRF without parameters uses k = 2 and 0-based positions, 1 / (position + k);
  an explicit {"rrf": {"k": 60}} gives the top hit 1/60; DBSF scales each list
  by its mean +- 3 sample standard deviations and sums;
- the multilingual tokenizer segments Chinese into words (charabia's
  segmentation): 照片 finds 相册里的照片, the single character 片 does not;
- query/groups has no offset: REST accepts the field and ignores it, so a
  second page of groups cannot be asked for;
- MaxSim with a one-row query scores the best segment's dot product, and with
  more rows the sum of each row's best;
- a formula naming a payload variable that a point lacks fails without a
  default and uses the default when one is given.

    python -m eval.qdrant_facts [--report]
"""

import argparse
import sys

import numpy as np

from eval import qdrant_common as common

DIM = 8


def unit(rows, seed):
    vectors = np.random.default_rng(seed).normal(size=(rows, DIM)).astype(np.float32)
    return vectors / np.linalg.norm(vectors, axis=1, keepdims=True)


class Checks:
    def __init__(self):
        self.results = {}
        self.failed = []

    def record(self, name, passed, **details):
        self.results[name] = {"passed": bool(passed), **details}
        if not passed:
            self.failed.append(name)
        print(f"{'ok  ' if passed else 'FAIL'} {name}: {details}", flush=True)


def check_fusion(qdrant, checks):
    from qdrant_client import models

    vectors = unit(200, 1)
    qdrant.create_collection("fusion", vectors_config=models.VectorParams(size=DIM, distance=models.Distance.DOT))
    qdrant.upsert("fusion", [models.PointStruct(id=i, vector=v.tolist()) for i, v in enumerate(vectors)], wait=True)
    first, second = unit(2, 2)
    prefetch = [models.Prefetch(query=first.tolist(), limit=30), models.Prefetch(query=second.tolist(), limit=30)]
    lists = [qdrant.query_points("fusion", query=q.tolist(), limit=30).points for q in (first, second)]

    def expected_rrf(k, base):
        scores = {}
        for points in lists:
            for position, point in enumerate(points):
                scores[point.id] = scores.get(point.id, 0.0) + 1.0 / (position + base + k)
        return scores

    def fused(query):
        return {p.id: p.score for p in qdrant.query_points("fusion", prefetch=prefetch, query=query, limit=60).points}

    def error(got, want):
        return max(abs(got[i] - want.get(i, 0.0)) for i in got)

    default = fused(models.FusionQuery(fusion=models.Fusion.RRF))
    explicit2 = fused(models.RrfQuery(rrf=models.Rrf(k=2)))
    explicit60 = fused(models.RrfQuery(rrf=models.Rrf(k=60)))
    checks.record("rrf default is k=2, 0-based", error(default, expected_rrf(2, 0)) < 1e-6
                  and error(default, expected_rrf(2, 1)) > 1e-3 and default == explicit2,
                  topScore=max(default.values()), errorK2=error(default, expected_rrf(2, 0)),
                  errorK1Based=error(default, expected_rrf(2, 1)))
    checks.record("rrf k=60 is 1/(position + 60), 0-based", error(explicit60, expected_rrf(60, 0)) < 1e-6
                  and error(explicit60, expected_rrf(60, 1)) > 1e-6, error=error(explicit60, expected_rrf(60, 0)),
                  error1Based=error(explicit60, expected_rrf(60, 1)))

    want = {}
    for points in lists:
        scores = np.array([p.score for p in points], dtype=np.float64)
        low, high = scores.mean() - 3 * scores.std(ddof=1), scores.mean() + 3 * scores.std(ddof=1)
        for point in points:
            want[point.id] = want.get(point.id, 0.0) + (point.score - low) / (high - low)
    dbsf = fused(models.FusionQuery(fusion=models.Fusion.DBSF))
    checks.record("dbsf is mean +- 3 sample sd, summed", error(dbsf, want) < 1e-4, error=error(dbsf, want))


def check_chinese(qdrant, checks):
    from qdrant_client import models

    texts = ["相册里的照片按时间排列", "南京市长江大桥", "照片向量写进索引", "相冊裡的照片"]
    found = {}
    for tokenizer in ("multilingual", "word"):
        name = f"text-{tokenizer}"
        qdrant.create_collection(name, vectors_config={})
        qdrant.create_payload_index(name, "text", models.TextIndexParams(
            type=models.TextIndexType.TEXT, tokenizer=models.TokenizerType(tokenizer), lowercase=True))
        qdrant.upsert(name, [models.PointStruct(id=i, vector={}, payload={"text": t}) for i, t in enumerate(texts)],
                      wait=True)
        for probe in ("照片", "片", "相册", "相冊", "长江大桥", "长江", "江大", "索引"):
            points, _next = qdrant.scroll(name, scroll_filter=models.Filter(must=[models.FieldCondition(
                key="text", match=models.MatchText(text=probe))]), limit=10)
            found[(tokenizer, probe)] = sorted(p.id for p in points)
    multilingual = {probe: ids for (tokenizer, probe), ids in found.items() if tokenizer == "multilingual"}
    word = {probe: ids for (tokenizer, probe), ids in found.items() if tokenizer == "word"}
    checks.record("multilingual segments Chinese into words",
                  multilingual["照片"] == [0, 2, 3] and multilingual["片"] == [] and multilingual["索引"] == [2],
                  multilingual=multilingual, word=word)


def check_groups_offset(qdrant, checks):
    from qdrant_client import models

    vectors = unit(60, 3)
    qdrant.create_collection("groups", vectors_config=models.VectorParams(size=DIM, distance=models.Distance.DOT))
    qdrant.create_payload_index("groups", "video", models.PayloadSchemaType.KEYWORD)
    qdrant.upsert("groups", [models.PointStruct(id=i, vector=v.tolist(), payload={"video": f"v{i // 3}"})
                             for i, v in enumerate(vectors)], wait=True)
    query = unit(1, 4)[0].tolist()
    body = {"query": query, "group_by": "video", "group_size": 1, "limit": 3}
    first = common.rest("POST", "/collections/groups/points/query/groups", body)
    shifted = common.rest("POST", "/collections/groups/points/query/groups", {**body, "offset": 3})

    def ids(response):
        return [group["id"] for group in response[1]["result"]["groups"]] if response[0] == 200 else None

    larger = common.rest("POST", "/collections/groups/points/query/groups", {**body, "limit": 6})
    ignored = shifted[0] == 200 and ids(shifted) == ids(first)
    checks.record("query/groups has no offset", ignored or shifted[0] >= 400, status=shifted[1].get("status")
                  if isinstance(shifted[1], dict) else shifted[0], firstPage=ids(first), withOffset3=ids(shifted),
                  limit6=ids(larger), offsetIgnored=ignored)


def check_maxsim(qdrant, checks):
    from qdrant_client import models

    qdrant.create_collection("maxsim", vectors_config=models.VectorParams(
        size=DIM, distance=models.Distance.DOT, hnsw_config=models.HnswConfigDiff(m=0),
        multivector_config=models.MultiVectorConfig(comparator=models.MultiVectorComparator.MAX_SIM)))
    videos = {i: unit(5 + 7 * i, 10 + i) for i in range(6)}
    qdrant.upsert("maxsim", [models.PointStruct(id=i, vector=v.tolist()) for i, v in videos.items()], wait=True)
    one, two = unit(1, 30), unit(2, 31)
    scores_one = {p.id: p.score for p in qdrant.query_points("maxsim", query=one.tolist(), limit=6).points}
    scores_two = {p.id: p.score for p in qdrant.query_points("maxsim", query=two.tolist(), limit=6).points}
    error_one = max(abs(scores_one[i] - float((v @ one[0]).max())) for i, v in videos.items())
    error_two = max(abs(scores_two[i] - float((v @ two.T).max(axis=0).sum())) for i, v in videos.items())
    checks.record("maxsim one row = best segment dot", error_one < 1e-5, error=error_one)
    checks.record("maxsim rows = sum of row bests", error_two < 1e-5, error=error_two)


def check_formula(qdrant, checks):
    from qdrant_client import models

    vectors = unit(4, 5)
    payloads = [{"x": 1.0}, {}, {"x": 0.5, "arr": [0.1, 0.9]}, {"obj": {"c7": 0.25}}]
    qdrant.create_collection("formula", vectors_config=models.VectorParams(size=DIM, distance=models.Distance.DOT))
    qdrant.upsert("formula", [models.PointStruct(id=i, vector=v.tolist(), payload=p)
                              for i, (v, p) in enumerate(zip(vectors, payloads, strict=True))], wait=True)
    query = unit(1, 6)[0]
    exact = vectors @ query
    prefetch = models.Prefetch(query=query.tolist(), limit=4)

    def run(formula, defaults=None):
        try:
            points = qdrant.query_points("formula", prefetch=prefetch, limit=4, query=models.FormulaQuery(
                formula=formula, defaults=defaults or {})).points
            return {p.id: p.score for p in points}, None
        except common.query_errors() as error:  # the error is the observation
            return None, common.error_text(error)

    plus_x = models.SumExpression(sum=["$score", models.MultExpression(mult=[0.2, "x"])])
    without, without_error = run(plus_x)
    with_default, _error = run(plus_x, {"x": 0.0})
    want = {i: float(exact[i]) + 0.2 * payloads[i].get("x", 0.0) for i in range(4)}
    defaults_right = with_default is not None and max(abs(with_default[i] - want[i]) for i in want) < 1e-5
    checks.record("formula: missing variable without default fails", without is None and defaults_right,
                  error=without_error, withDefault=with_default)

    nested, nested_error = run(models.SumExpression(sum=["$score", "obj.c7"]), {"obj.c7": 0.0})
    array, array_error = run(models.SumExpression(sum=["$score", "arr"]), {"arr": 0.0})
    indexed, indexed_error = run(models.SumExpression(sum=["$score", "arr[1]"]), {"arr[1]": 0.0})
    smallest, smallest_error = run(models.MinExpression(min=["$score", 0.0]))
    checks.record("formula: observations", True, nestedPath=nested or nested_error, arrayPath=array or array_error,
                  arrayIndex=indexed or indexed_error, minExpression=smallest or smallest_error,
                  exactScores=[round(float(s), 6) for s in exact])


def main():
    parser = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    parser.add_argument("--report", action="store_true", help=f"write the results under {common.RUNS}")
    args = parser.parse_args()
    checks = Checks()
    with common.Server("facts") as server:
        server.start()
        qdrant = common.client()
        for check in (check_fusion, check_chinese, check_groups_offset, check_maxsim, check_formula):
            check(qdrant, checks)
    if args.report:
        report = common.Report("facts")
        report.put("checks", checks.results)
    sys.exit(1 if checks.failed else 0)


if __name__ == "__main__":
    main()
