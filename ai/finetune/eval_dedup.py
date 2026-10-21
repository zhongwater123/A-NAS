"""对一批真实文本做两件事：看相似度分布与近似重复分组（去重 / 聚类），按样例分类（故障归类）。

只用标准库。文本不进仓库，脚本只打印数字和截断后的片段；写进文档的只有数字。

  python -m finetune.eval_dedup --texts feedback.json --key items.text --embed-url http://127.0.0.1:8090
  python -m finetune.eval_dedup --texts logs.json --key groups.message --embed-url ... --examples finetune/data/labeled.jsonl --label-prefix log_
  python -m finetune.eval_dedup --texts n8n.json --key lines.text --embed-url ... --examples ... --label-prefix log_

--key 形如 "items.text"：先取顶层的 items 数组，再取每个元素的 text 字段；文件是纯字符串数组时不用给。
输出：
  - 两两相似度的分位数（STS 前缀）；
  - 阈值 0.95 / 0.90 / 0.85 / 0.80 下的近似重复组数与最大组；
  - 最像的前 N 对（给人判断是不是同一件事）；
  - 给了 --examples 时：每条文本离哪个标签的样例最近，按阈值统计有多少条会被打上标签。
"""

from __future__ import annotations

import argparse
import json
import os
import statistics
import sys
from pathlib import Path

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))
from finetune.embedders import HashEmbedder, HttpEmbedder, cosine  # noqa: E402
from finetune.similarity import Example, classify_by_examples, find_duplicates  # noqa: E402


def load_texts(path: str, key: str | None) -> list[str]:
    raw = Path(path).read_text(encoding="utf-8-sig")
    data = json.loads(raw[raw.index("{") if "{" in raw[:50] else raw.index("["):]) if raw.lstrip()[0] not in "[{" else json.loads(raw)
    if key:
        top, field = key.split(".", 1)
        data = [x.get(field, "") for x in data[top]]
    texts = [" ".join(str(t).split()) for t in data]
    return [t for t in texts if t]


def strip_template(text: str) -> str:
    """反馈模板的固定部分（【模块】、字段名、日期、版本号）会让不同内容的反馈看起来很像，比较前去掉。"""
    import re
    t = re.sub(r"【[^】]*】", " ", text)
    t = re.sub(r"(复现|期望|实际|证据|版本|步骤)[:：]", " ", t)
    t = re.sub(r"\d{4}-\d{2}-\d{2}", " ", t)
    t = re.sub(r"(?<!\S)\d+\.\d+(\.\d+)*(?!\S)", " ", t)
    return " ".join(t.split())


def pct(xs: list[float], p: float) -> float:
    xs = sorted(xs)
    return xs[min(len(xs) - 1, int(round(p * (len(xs) - 1))))]


def main(argv: list[str]) -> None:
    ap = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument("--texts", required=True)
    ap.add_argument("--key")
    ap.add_argument("--embed-url")
    ap.add_argument("--hf-model", default="google/embeddinggemma-2")
    ap.add_argument("--dim", type=int, default=0)
    ap.add_argument("--top", type=int, default=8, help="打印最像的前 N 对")
    ap.add_argument("--snippet", type=int, default=48, help="片段长度")
    ap.add_argument("--examples", help="带标签文本 JSONL（text/label），做按样例分类")
    ap.add_argument("--label-prefix", default="", help="只用标签以此开头的样例")
    ap.add_argument("--strip-template", action="store_true",
                    help="去掉【…】模板头、复现/期望/实际/证据/版本 这类字段名和日期，再比较")
    ap.add_argument("--json")
    args = ap.parse_args(argv)

    texts = load_texts(args.texts, args.key)
    if args.strip_template:
        texts = [t for t in (strip_template(t) for t in texts) if t]
    emb = (HttpEmbedder(args.embed_url, args.hf_model, args.dim or None, timeout=900, batch_size=8)
           if args.embed_url else HashEmbedder(1024))
    vecs = emb.embed(texts, "sts")
    n = len(texts)
    sims = [(cosine(vecs[i], vecs[j]), i, j) for i in range(n) for j in range(i + 1, n)]
    scores = [s for s, _, _ in sims]
    report: dict = {"embedder": emb.name, "texts": n, "pairs": len(sims)}
    if scores:
        report["similarity"] = {"median": round(statistics.median(scores), 3),
                                "p90": round(pct(scores, 0.90), 3), "p99": round(pct(scores, 0.99), 3),
                                "max": round(max(scores), 3)}
    groups = {}
    for thr in (0.95, 0.90, 0.85, 0.80):
        g = find_duplicates(texts, _Fixed(vecs, emb), thr)
        groups[str(thr)] = {"groups": len(g), "texts_in_groups": sum(len(x) for x in g),
                            "largest": max((len(x) for x in g), default=0)}
    report["duplicate_groups"] = groups

    print(f"文本 {n} 条，两两 {len(sims)} 对，embedder {emb.name}")
    if scores:
        s = report["similarity"]
        print(f"相似度：中位 {s['median']}，P90 {s['p90']}，P99 {s['p99']}，最高 {s['max']}")
    for thr, g in groups.items():
        print(f"阈值 {thr}：{g['groups']} 组近似重复，覆盖 {g['texts_in_groups']} 条，最大组 {g['largest']} 条")
    print(f"最像的前 {args.top} 对：")
    for s, i, j in sorted(sims, reverse=True)[: args.top]:
        print(f"  {s:.3f}  {texts[i][:args.snippet]!r}  <->  {texts[j][:args.snippet]!r}")

    if args.examples:
        rows = [json.loads(l) for l in Path(args.examples).read_text(encoding="utf-8-sig").splitlines() if l.strip()]
        examples = [Example(r["label"], r["text"]) for r in rows if r.get("label", "").startswith(args.label_prefix)]
        clf = classify_by_examples(examples, emb, threshold=0.0)
        labeled = [(clf(t), t) for t in texts]
        by_thr = {}
        for thr in (0.95, 0.90, 0.85, 0.80):
            hit = [(lab, sc, t) for (lab, sc, ex), t in labeled if sc >= thr]
            counts: dict[str, int] = {}
            for lab, _, _ in hit:
                counts[lab] = counts.get(lab, 0) + 1
            by_thr[str(thr)] = {"labeled": len(hit), "by_label": counts}
        report["classification"] = {"examples": len(examples), "by_threshold": by_thr}
        print(f"按样例分类（{len(examples)} 条样例，前缀 {args.label_prefix!r}）：")
        for thr, r in by_thr.items():
            print(f"  阈值 {thr}：{r['labeled']}/{n} 条被打标签 {r['by_label']}")
        print("  分数最高的 6 条：")
        for (lab, sc, ex), t in sorted(labeled, key=lambda x: -x[0][1])[:6]:
            print(f"    {sc:.3f}  {lab:18s}  {t[:args.snippet]!r}")
    if args.json:
        Path(args.json).write_text(json.dumps(report, ensure_ascii=False, indent=2), encoding="utf-8")


class _Fixed:
    """把算好的向量按文本顺序喂给 find_duplicates，避免重复请求。"""

    def __init__(self, vecs, inner):
        self._vecs, self.name, self.dim, self.semantic = vecs, inner.name, inner.dim, inner.semantic
        self._i = 0

    def embed(self, texts, kind="sts"):
        return self._vecs[: len(texts)]


if __name__ == "__main__":
    main(sys.argv[1:])
