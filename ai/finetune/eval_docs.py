"""文档检索评估：把一个目录下的 Markdown 按标题切段当语料，用自然语言问题找答案段落，算 Hit@k / MRR。

只用标准库。语料就是仓库自己的 docs/，问题文件由人写（每条标明答案在哪个文件的哪个标题下），
所以结果能复现，也不涉及任何私有数据。这是"文件语义检索"在真实中文技术文档上的基线。

  python -m finetune.eval_docs --docs ../docs --queries finetune/docs_queries.json --embed-url http://127.0.0.1:8089
  python -m finetune.eval_docs --docs ../docs --list                     # 列出全部段落 id，写问题时查
  python -m finetune.eval_docs --docs ../docs --queries ... --mode sts   # 问题和段落都用 STS 前缀（对照）
  python -m finetune.eval_docs --docs ../docs --queries ... --dim 256    # Matryoshka 截断

问题文件格式（JSON 数组）：
  {"q": "数据卷掉线时相册服务会怎样？", "target": "architecture/photo-library.md#运行期数据卷故障"}
target 是 "相对 docs 的路径#标题文字"（标题文字取该段的标题，不含 # 号）；一个问题可以给多个 target（数组）。
段落 id 用同样的形式，--list 会打印出来。
"""

from __future__ import annotations

import argparse
import json
import os
import re
import sys
import time
from dataclasses import dataclass
from pathlib import Path

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))
from finetune.embedders import CachedEmbedder, HashEmbedder, HttpEmbedder, cosine, truncate  # noqa: E402

HEADING = re.compile(r"^(#{1,4})\s+(.+?)\s*$")


@dataclass
class Chunk:
    id: str          # 相对路径#标题
    path: str
    heading: str
    text: str        # 标题 + 正文


def _read_text(path: Path) -> str:
    """Windows 上超过 260 字符的路径要加长路径前缀（两个反斜杠、问号、反斜杠）才能打开。"""
    p = str(path.resolve())
    prefix = chr(92) * 2 + "?" + chr(92)
    if os.name == "nt" and not p.startswith(prefix):
        p = prefix + p
    return Path(p).read_text(encoding="utf-8")


def chunk_markdown(docs_dir: Path, min_chars: int = 80, max_chars: int = 1800,
                   skip_names: tuple[str, ...] = ("README.md", "_TEMPLATE.md")) -> list[Chunk]:
    """按标题切段。超长的段按空行再切，并在 id 后加 ~2、~3；太短的段并入前一段。"""
    chunks: list[Chunk] = []
    for path in sorted(docs_dir.rglob("*.md")):
        if path.name in skip_names:
            continue
        rel = path.relative_to(docs_dir).as_posix()
        heading, buf = path.stem, []
        sections: list[tuple[str, str]] = []

        def flush():
            body = "\n".join(buf).strip()
            if body:
                sections.append((heading, body))
            buf.clear()

        in_code = False
        for line in _read_text(path).splitlines():
            if line.startswith("```"):
                in_code = not in_code
            m = None if in_code else HEADING.match(line)
            if m:
                flush()
                heading = m.group(2)
            else:
                buf.append(line)
        flush()

        for h, body in sections:
            if len(body) < min_chars and chunks and chunks[-1].path == rel:
                chunks[-1].text += "\n" + body
                continue
            parts = [body]
            if len(body) > max_chars:
                parts, cur = [], ""
                for para in re.split(r"\n\s*\n", body):
                    if cur and len(cur) + len(para) > max_chars:
                        parts.append(cur)
                        cur = para
                    else:
                        cur = (cur + "\n\n" + para) if cur else para
                if cur:
                    parts.append(cur)
                hard = []
                for part in parts:   # 没有空行的大表格之类：按行硬切
                    while len(part) > max_chars:
                        cut = part.rfind(chr(10), 0, max_chars)
                        cut = cut if cut > max_chars // 2 else max_chars
                        hard.append(part[:cut])
                        part = part[cut:].lstrip(chr(10))
                    hard.append(part)
                parts = hard
            for i, part in enumerate(parts):
                cid = f"{rel}#{h}" + (f"~{i + 1}" if len(parts) > 1 else "")
                chunks.append(Chunk(cid, rel, h, f"{h}\n{part}"))
    return chunks


class Truncated:
    """把任意 embedder 的向量截到 dim 维并重新归一化（Matryoshka）。"""

    def __init__(self, inner, dim: int):
        self.inner, self.dim, self.semantic = inner, dim, inner.semantic
        self.name = f"{inner.name}->{dim}"

    def embed(self, texts, kind="sts"):
        return [truncate(v, self.dim) for v in self.inner.embed(texts, kind)]


def evaluate(chunks: list[Chunk], queries: list[dict], embedder, mode: str) -> dict:
    """mode: retrieval（问题 query 前缀、段落 document 前缀）或 sts（两边都 STS）。"""
    q_kind, d_kind = ("query", "document") if mode == "retrieval" else ("sts", "sts")
    t0 = time.time()
    dvecs = []
    texts = [c.text for c in chunks]
    for i in range(0, len(texts), 64):      # 分组过 embedder：缓存逐组落盘，长文档在 CPU 上慢也不怕中断
        dvecs.extend(embedder.embed(texts[i:i + 64], d_kind))
        print(f"  已编码 {min(i + 64, len(texts))}/{len(texts)} 段，{time.time() - t0:.0f} s", file=sys.stderr, flush=True)
    t_docs = time.time() - t0
    t0 = time.time()
    qvecs = embedder.embed([q["q"] for q in queries], q_kind)
    t_q = time.time() - t0
    base_id = lambda cid: cid.split("~")[0]  # noqa: E731
    hits = {1: 0, 5: 0, 10: 0}
    file_hits = {1: 0, 5: 0}      # 文件级：命中同一篇文档的任意一段也算（界面按文件展示时的口径）
    rr = 0.0
    misses = []
    for q, qv in zip(queries, qvecs):
        targets = q["target"] if isinstance(q["target"], list) else [q["target"]]
        target_files = {t.split("#")[0] for t in targets}
        ranked = sorted(range(len(chunks)), key=lambda i: cosine(qv, dvecs[i]), reverse=True)
        rank = next((r for r, i in enumerate(ranked) if base_id(chunks[i].id) in targets), None)
        if rank is not None:
            for k in hits:
                hits[k] += rank < k
            rr += 1 / (rank + 1) if rank < 10 else 0
        seen_files: list[str] = []
        for i in ranked:
            if chunks[i].path not in seen_files:
                seen_files.append(chunks[i].path)
            if len(seen_files) >= 5:
                break
        file_rank = next((r for r, f in enumerate(seen_files) if f in target_files), None)
        if file_rank is not None:
            for k in file_hits:
                file_hits[k] += file_rank < k
        if rank is None or rank >= 5:
            misses.append({"q": q["q"], "rank": None if rank is None else rank + 1,
                           "top1": chunks[ranked[0]].id, "want": targets[0]})
    n = len(queries)
    return {"embedder": embedder.name, "mode": mode, "chunks": len(chunks), "queries": n,
            "hit@1": round(hits[1] / n, 3), "hit@5": round(hits[5] / n, 3), "hit@10": round(hits[10] / n, 3),
            "mrr@10": round(rr / n, 3),
            "file_hit@1": round(file_hits[1] / n, 3), "file_hit@5": round(file_hits[5] / n, 3), "seconds_docs": round(t_docs, 1), "seconds_queries": round(t_q, 1),
            "misses": misses}


def main(argv: list[str]) -> None:
    ap = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument("--docs", required=True)
    ap.add_argument("--queries")
    ap.add_argument("--list", action="store_true", help="只列出段落 id 和长度")
    ap.add_argument("--embed-url")
    ap.add_argument("--hf-model", default="google/embeddinggemma-2")
    ap.add_argument("--dim", type=int, default=0)
    ap.add_argument("--mode", choices=("retrieval", "sts"), default="retrieval")
    ap.add_argument("--cache", help="向量缓存文件（JSONL），同一语料反复评估不重算")
    ap.add_argument("--json")
    args = ap.parse_args(argv)

    chunks = chunk_markdown(Path(args.docs))
    if args.list:
        for c in chunks:
            print(f"{len(c.text):5d}  {c.id}")
        print(f"{len(chunks)} 段，共 {sum(len(c.text) for c in chunks)} 字符")
        return
    queries = json.loads(Path(args.queries).read_text(encoding="utf-8-sig"))
    ids = {c.id.split("~")[0] for c in chunks}
    bad = [q for q in queries if not any(t in ids for t in (q["target"] if isinstance(q["target"], list) else [q["target"]]))]
    if bad:
        sys.exit("这些问题的 target 不存在，用 --list 核对：\n" + "\n".join(f"  {q['q']} -> {q['target']}" for q in bad))
    # 文档段落长，CPU 上一批要几十秒：每批 8 段、超时放宽到 15 分钟
    emb = (HttpEmbedder(args.embed_url, args.hf_model, None, timeout=900, batch_size=8)
           if args.embed_url else HashEmbedder(1024))
    if args.cache:
        emb = CachedEmbedder(emb, Path(args.cache))
    if args.dim:
        emb = Truncated(emb, args.dim)   # 从全维缓存截断，换维度不重算
    r = evaluate(chunks, queries, emb, args.mode)
    print("| 指标 | 值 |\n| --- | --- |")
    for k, v in r.items():
        if k != "misses":
            print(f"| {k} | {v} |")
    print(f"\n前 5 之外的问题（{len(r['misses'])} 个）：")
    for m in r["misses"]:
        print(f"  rank={m['rank']}  {m['q']}\n      想要 {m['want']}\n      top1 {m['top1']}")
    if args.json:
        Path(args.json).write_text(json.dumps(r, ensure_ascii=False, indent=2), encoding="utf-8")


if __name__ == "__main__":
    main(sys.argv[1:])
