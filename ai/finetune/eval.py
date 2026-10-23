"""微调前后对比：用同一份三元组当检索任务算 Hit@1 / Hit@5 / MRR@10，以及正负例的相似度差。

只用标准库（--model 走 MediaPipe 时需要 Worker 的运行环境）。可以评三种来源的向量，在 ai/ 目录下：
  python -m finetune.eval finetune/data/triplets.jsonl --model /models/embeddinggemma-2-740m.litertlm   # Worker 同一条 MediaPipe 路径（基座）
  python -m finetune.eval finetune/data/triplets.jsonl --embed-url http://127.0.0.1:8089                 # finetune.server（HF 权重，基座或微调后）
  python -m finetune.eval finetune/data/triplets.jsonl                                                   # 哈希退路（分数下限）

做法和 LawVault / 官方教程一致：每个 anchor 在"全部 positive + negative"里检索，看正例排第几。
微调前后各跑一次，把两张表贴进 docs/research/embeddinggemma-2-text-finetuning.md。
"""

from __future__ import annotations

import argparse
import json
import os
import sys
from pathlib import Path

AI_DIR = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
sys.path.insert(0, AI_DIR)
from finetune.embedders import HashEmbedder, HttpEmbedder, ProviderEmbedder, cosine  # noqa: E402

MANIFEST = os.path.join(AI_DIR, "..", "deploy", "models", "embeddinggemma-2-740m.json")


def prompt_kinds(triplets: list[dict]) -> tuple[dict[str, str], dict[str, str]]:
    """每条 anchor 和语料文本该用的前缀：来源为 docq:（问答对）的用 query / document，其余 STS。"""
    akind: dict[str, str] = {}
    ckind: dict[str, str] = {}
    for t in triplets:
        asym = str(t.get("source", "")).startswith("docq:")
        akind.setdefault(t["anchor"], "query" if asym else "sts")
        for key in ("positive", "negative"):
            ckind.setdefault(t[key], "document" if asym else "sts")
    return akind, ckind


def _embed_by_kind(texts: list[str], kinds: dict[str, str], embedder) -> dict[str, list[float]]:
    out: dict[str, list[float]] = {}
    for kind in sorted({kinds[t] for t in texts}):
        group = [t for t in texts if kinds[t] == kind]
        out.update(zip(group, embedder.embed(group, kind)))
    return out


def evaluate(triplets: list[dict], embedder) -> dict:
    akind, ckind = prompt_kinds(triplets)
    corpus = sorted({t["positive"] for t in triplets} | {t["negative"] for t in triplets})
    cvecs = _embed_by_kind(corpus, ckind, embedder)
    anchors = sorted({t["anchor"] for t in triplets})
    avecs = _embed_by_kind(anchors, akind, embedder)
    positives: dict[str, set[str]] = {}
    for t in triplets:
        positives.setdefault(t["anchor"], set()).add(t["positive"])

    hit1 = hit5 = 0
    rr = 0.0
    margins = []
    for a in anchors:
        ranked = sorted(corpus, key=lambda x: cosine(avecs[a], cvecs[x]), reverse=True)
        ranked = [x for x in ranked if x != a]
        first = next((i for i, x in enumerate(ranked) if x in positives[a]), None)
        if first is not None:
            hit1 += first == 0
            hit5 += first < 5
            rr += 1.0 / (first + 1) if first < 10 else 0.0
    for t in triplets:
        margins.append(cosine(avecs[t["anchor"]], cvecs[t["positive"]])
                       - cosine(avecs[t["anchor"]], cvecs[t["negative"]]))
    n = len(anchors)
    return {"embedder": embedder.name, "anchors": n, "corpus": len(corpus),
            "hit@1": round(hit1 / n, 3), "hit@5": round(hit5 / n, 3), "mrr@10": round(rr / n, 3),
            "mean_margin": round(sum(margins) / len(margins), 3),
            "neg_margin_ratio": round(sum(m <= 0 for m in margins) / len(margins), 3)}


def main(argv: list[str]) -> None:
    ap = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument("triplets")
    ap.add_argument("--model", help="Worker 用的 .litertlm 文件，经 anas_ai.providers.MediaPipeProvider 取向量")
    ap.add_argument("--weight-cache", help="MediaPipe 重打包权重的可写目录（同 eval.embed 的 --weight-cache）")
    ap.add_argument("--embed-url", help="finetune.server 的地址")
    ap.add_argument("--hf-model", default="google/embeddinggemma-2", help="--embed-url 时报给服务的模型名")
    ap.add_argument("--dim", type=int, default=0, help="Matryoshka 截断，0=不截")
    ap.add_argument("--json", help="结果另存为 JSON")
    args = ap.parse_args(argv)
    triplets = [json.loads(l) for l in Path(args.triplets).read_text(encoding="utf-8").splitlines() if l.strip()]
    provider = None
    if args.model:
        from anas_ai import providers
        provider = providers.MediaPipeProvider(args.model, MANIFEST, cache_dir=args.weight_cache)
        emb = ProviderEmbedder(provider, args.dim or None)
    elif args.embed_url:
        emb = HttpEmbedder(args.embed_url, args.hf_model, args.dim or None)
    else:
        emb = HashEmbedder(1024)
    try:
        r = evaluate(triplets, emb)
    finally:
        if provider is not None:
            provider.close()
    print("| 指标 | 值 |\n| --- | --- |")
    for k, v in r.items():
        print(f"| {k} | {v} |")
    if args.json:
        Path(args.json).write_text(json.dumps(r, ensure_ascii=False, indent=2), encoding="utf-8")


if __name__ == "__main__":
    main(sys.argv[1:])
