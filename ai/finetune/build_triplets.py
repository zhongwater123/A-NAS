"""把带标签的文本整理成微调三元组 (anchor, positive, negative)。

只用标准库。产出 JSONL，每行 {"anchor": ..., "positive": ..., "negative": ..., "source": ...}，
finetune.train 直接读，finetune.eval 直接评。

输入（可同时给多份）：
  --labeled  JSONL，每行 {"text": "...", "label": "..."}：同一 label 下任意两句互为正例，其他 label 的句子做负例。
  --pairs    JSONL，每行 {"a": "...", "b": "..."}：人工标注的"说的是同一件事"的对，负例从所有文本里随机抽。
难负例（默认开启）：用当前模型对每个 anchor 取 Top-K，把"分高但不是正例"的那条当负例。
  LawVault 的经验是难负例对 Top-1 提升最大。--embed-url 指向 finetune.server；不给则用哈希向量挖，弱一些但流程一样。

用法（在 ai/ 目录下）：
  python -m finetune.build_triplets --labeled finetune/data/labeled.jsonl --out finetune/data/triplets.jsonl
  python -m finetune.build_triplets --labeled finetune/data/labeled.jsonl --pairs finetune/data/pairs.jsonl \\
      --embed-url http://127.0.0.1:8089 --out finetune/data/triplets.jsonl
"""

from __future__ import annotations

import argparse
import json
import os
import random
import sys
from pathlib import Path

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))
from finetune.embedders import HashEmbedder, HttpEmbedder, cosine  # noqa: E402


def read_jsonl(path: str) -> list[dict]:
    return [json.loads(l) for l in Path(path).read_text(encoding="utf-8-sig").splitlines() if l.strip()]


def labeled_triplets(rows: list[dict], rng: random.Random) -> list[dict]:
    by_label: dict[str, list[str]] = {}
    for r in rows:
        if r.get("text") and r.get("label"):
            by_label.setdefault(str(r["label"]), []).append(r["text"])
    out = []
    for lab, texts in by_label.items():
        others = [t for ol, ts in by_label.items() if ol != lab for t in ts]
        if len(texts) < 2 or not others:
            continue
        for a in texts:
            for p in texts:
                if p != a:
                    out.append({"anchor": a, "positive": p, "negative": rng.choice(others), "source": f"label:{lab}"})
    return out


def pair_triplets(pairs: list[dict], pool: list[str], rng: random.Random) -> list[dict]:
    out = []
    for i, pr in enumerate(pairs):
        a, b = pr.get("a"), pr.get("b")
        if not a or not b:
            continue
        cands = [t for t in pool if t not in (a, b)]
        if not cands:
            continue
        for x, y in ((a, b), (b, a)):
            out.append({"anchor": x, "positive": y, "negative": rng.choice(cands), "source": f"pair:{i}"})
    return out


def mine_hard_negatives(triplets: list[dict], embedder, k: int = 5) -> int:
    """用当前模型把每条的 negative 换成"最像 anchor 但不是正例、也不同组"的句子。返回替换了几条。"""
    texts = sorted({t["positive"] for t in triplets} | {t["negative"] for t in triplets})
    vecs = dict(zip(texts, embedder.embed(texts, "sts")))
    anchors = sorted({t["anchor"] for t in triplets})
    avecs = dict(zip(anchors, embedder.embed(anchors, "sts")))
    positives: dict[str, set[str]] = {}
    family: dict[str, str] = {}
    for t in triplets:
        positives.setdefault(t["anchor"], set()).add(t["positive"])
        family[t["positive"]] = t["source"]
        family.setdefault(t["anchor"], t["source"])
    changed = 0
    for t in triplets:
        a = t["anchor"]
        ranked = sorted(((cosine(avecs[a], vecs[x]), x) for x in texts
                         if x != a and x not in positives[a] and family.get(x) != family.get(a)),
                        reverse=True)[:k]
        if ranked and ranked[0][1] != t["negative"]:
            t["negative"] = ranked[0][1]
            changed += 1
    return changed


def main(argv: list[str]) -> None:
    ap = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument("--out", required=True)
    ap.add_argument("--labeled", nargs="*", default=[])
    ap.add_argument("--pairs", nargs="*", default=[])
    ap.add_argument("--embed-url", help="挖难负例用的向量服务；不给则用哈希向量")
    ap.add_argument("--no-hard", action="store_true", help="不挖难负例，负例随机")
    ap.add_argument("--seed", type=int, default=7)
    args = ap.parse_args(argv)

    rng = random.Random(args.seed)
    labeled = [r for p in args.labeled for r in read_jsonl(p)]
    pairs = [r for p in args.pairs for r in read_jsonl(p)]
    pool = sorted({r["text"] for r in labeled if r.get("text")} | {pr[k] for pr in pairs for k in ("a", "b") if pr.get(k)})
    triplets = labeled_triplets(labeled, rng) + pair_triplets(pairs, pool, rng)
    if not triplets:
        sys.exit("没有产出任何三元组：--labeled 需要同一 label 至少 2 条且存在其他 label；--pairs 需要 a/b 字段。")
    changed = 0
    if not args.no_hard:
        emb = HttpEmbedder(args.embed_url, dim=None) if args.embed_url else HashEmbedder(1024)
        changed = mine_hard_negatives(triplets, emb)
    rng.shuffle(triplets)
    out = Path(args.out)
    out.parent.mkdir(parents=True, exist_ok=True)
    out.write_text("".join(json.dumps(t, ensure_ascii=False) + "\n" for t in triplets), encoding="utf-8")
    print(f"写出 {len(triplets)} 条三元组到 {out}；难负例替换 {changed} 条；标签文本 {len(labeled)} 条，标注对 {len(pairs)} 对")
    if len(triplets) < 200:
        print("提示：官方和社区经验是几百到几千条才有稳定收益。")


if __name__ == "__main__":
    main(sys.argv[1:])
