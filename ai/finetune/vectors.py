"""向量库命令行：把文本灌进 Qdrant、按意思查、看状态、导出。只用标准库（开发机用）。

    export ANAS_QDRANT_URL=http://127.0.0.1:6333   # 不设就用进程内向量库，落盘到 --data-dir（默认 ai/finetune/data/vectors）
    export ANAS_EMBED_URL=http://127.0.0.1:8089    # 不设就用哈希向量（只能做字面相似）

    cd ai
    python -m finetune.vectors info
    python -m finetune.vectors add feedback.jsonl --key-field id --payload-fields source,level
    python -m finetune.vectors add notes.txt                      # 一行一条，文本本身当 key
    python -m finetune.vectors search "大文件上传后校验不一致" -k 5 --where source=feedback
    python -m finetune.vectors similar "Out of memory: Killed process 777"   # 对称比对（去重）
    python -m finetune.vectors export > backup.jsonl
    python -m finetune.vectors drop --yes

集合维度跟 embedder 走（--dim，默认 256）；维度变了要 --recreate（会清空集合）。
"""

from __future__ import annotations

import argparse
import json
import os
import sys
from pathlib import Path

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))
from finetune.embedders import build_embedder  # noqa: E402
from finetune.index import TextIndex  # noqa: E402
from finetune.store import StoreError, build_store  # noqa: E402

HERE = Path(__file__).resolve().parent


def read_rows(path: Path, text_field: str) -> list[dict]:
    """.jsonl / .json（对象列表）/ 其他当纯文本一行一条。"""
    raw = path.read_text(encoding="utf-8-sig")
    if path.suffix == ".jsonl":
        rows = [json.loads(line) for line in raw.splitlines() if line.strip()]
    elif path.suffix == ".json":
        rows = json.loads(raw)
        if not isinstance(rows, list):
            raise SystemExit("JSON 文件必须是对象列表")
    else:
        rows = [{text_field: line.strip()} for line in raw.splitlines() if line.strip()]
    for r in rows:
        if not isinstance(r, dict) or not isinstance(r.get(text_field), str):
            raise SystemExit(f"每条记录都要有字符串字段 {text_field!r}，出错的记录：{str(r)[:120]}")
    return rows


def parse_where(items: list[str] | None) -> dict | None:
    """--where key=value（可多次；同 key 多次 = 任一）。"""
    if not items:
        return None
    by_key: dict[str, list] = {}
    for it in items:
        if "=" not in it:
            raise SystemExit(f"--where 要写成 key=value，收到 {it!r}")
        k, v = it.split("=", 1)
        by_key.setdefault(k, []).append(_coerce(v))
    must = [{"key": k, "match": ({"value": vs[0]} if len(vs) == 1 else {"any": vs})} for k, vs in by_key.items()]
    return {"must": must}


def _coerce(v: str):
    if v.lower() in ("true", "false"):
        return v.lower() == "true"
    try:
        return int(v)
    except ValueError:
        pass
    try:
        return float(v)
    except ValueError:
        return v


def _logger(args):
    return None if args.quiet else (lambda m: print(m, file=sys.stderr))


def open_index(args, recreate: bool = False) -> TextIndex:
    log = _logger(args)
    emb = build_embedder({"dim": args.dim, "url": args.embed_url}, cache_dir=HERE / "cache", logger=log)
    store = build_store({"url": args.qdrant}, collection=args.collection, data_dir=Path(args.data_dir), logger=log)
    try:
        return TextIndex(store, emb, recreate=recreate)
    except StoreError as e:
        raise SystemExit(f"{e}\n提示：维度不一致时加 --recreate 重建集合（会清空）。")


def cmd_info(args):
    # 只看不建：不经过 TextIndex，免得"看一眼"就把集合建出来
    log = _logger(args)
    emb = build_embedder({"dim": args.dim, "url": args.embed_url}, cache_dir=None, logger=log)
    store = build_store({"url": args.qdrant}, collection=args.collection, data_dir=Path(args.data_dir), logger=log)
    print(json.dumps({**store.info(), "embedder": emb.name, "embedder_dim": emb.dim, "semantic": emb.semantic},
                     ensure_ascii=False, indent=2))


def cmd_add(args):
    idx = open_index(args, recreate=args.recreate)
    rows = read_rows(Path(args.file), args.text_field)
    texts = [r[args.text_field] for r in rows]
    keys = [r[args.key_field] for r in rows] if args.key_field else None
    if keys is not None and any(k is None for k in keys):
        raise SystemExit(f"有记录缺少 key 字段 {args.key_field!r}")
    fields = [f for f in (args.payload_fields or "").split(",") if f]
    payloads = [{f: r[f] for f in fields if f in r} for r in rows] if fields else None
    idx.add(texts, keys=keys, payloads=payloads, kind=args.kind)
    print(f"已写入 {len(texts)} 条到集合 {idx.store.collection}（共 {len(idx)} 条，"
          f"{'语义' if idx.semantic else '哈希'}向量 {idx.embedder.dim} 维）")


def _print_hits(hits, as_json: bool):
    if as_json:
        print(json.dumps([h.as_dict() for h in hits], ensure_ascii=False))
        return
    if not hits:
        print("（没有结果）")
    for i, h in enumerate(hits, 1):
        extra = f"  {json.dumps(h.payload, ensure_ascii=False)}" if h.payload else ""
        print(f"{i}. [{h.score:.3f}] {h.key}  {h.text[:120]}{extra}")


def cmd_search(args):
    idx = open_index(args)
    if not idx.semantic and not args.quiet:
        print("提示：当前是哈希向量，只能找字面相近的；要语义检索请设置 ANAS_EMBED_URL。", file=sys.stderr)
    _print_hits(idx.search(args.query, k=args.k, where=parse_where(args.where), min_score=args.min_score), args.json)


def cmd_similar(args):
    idx = open_index(args)
    _print_hits(idx.similar(args.text, k=args.k, where=parse_where(args.where), min_score=args.min_score), args.json)


def cmd_export(args):
    idx = open_index(args)
    n = 0
    for key, payload in idx.store.scroll(parse_where(args.where)):
        text = payload.pop("text", "")
        print(json.dumps({"key": key, "text": text, **payload}, ensure_ascii=False))
        n += 1
    print(f"导出 {n} 条", file=sys.stderr)


def cmd_drop(args):
    if not args.yes:
        raise SystemExit("会删掉整个集合，确认请加 --yes")
    store = build_store({"url": args.qdrant}, collection=args.collection, data_dir=Path(args.data_dir),
                        logger=_logger(args))
    store.drop_collection()
    print(f"已删除集合 {store.collection}")


def main(argv: list[str]) -> None:
    ap = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument("--qdrant", default=None, help="Qdrant 地址，默认取 ANAS_QDRANT_URL；都没有则用进程内向量库")
    ap.add_argument("--collection", default=None, help="集合名，默认取 ANAS_QDRANT_COLLECTION，再默认 default")
    ap.add_argument("--embed-url", default=None, help="向量服务地址，默认取 ANAS_EMBED_URL；都没有则用哈希向量")
    ap.add_argument("--dim", type=int, default=int(os.environ.get("ANAS_EMBED_DIM", "256")), help="向量维度，默认 256")
    ap.add_argument("--data-dir", default=str(HERE / "data" / "vectors"), help="进程内向量库的落盘目录")
    ap.add_argument("-q", "--quiet", action="store_true")
    sub = ap.add_subparsers(dest="cmd", required=True)

    sub.add_parser("info", help="服务版本、集合维度与条数").set_defaults(fn=cmd_info)

    p = sub.add_parser("add", help="从 .jsonl / .json / .txt 灌文本")
    p.add_argument("file")
    p.add_argument("--text-field", default="text")
    p.add_argument("--key-field", default=None, help="当 key 的字段；不给则用文本本身")
    p.add_argument("--payload-fields", default=None, help="逗号分隔，要一起存进 payload 的字段")
    p.add_argument("--kind", default=None, help="任务前缀，默认 document；做去重库用 sts")
    p.add_argument("--recreate", action="store_true", help="先删掉集合再写（换维度时用）")
    p.set_defaults(fn=cmd_add)

    for name, fn, arg in (("search", cmd_search, "query"), ("similar", cmd_similar, "text")):
        p = sub.add_parser(name, help="按意思找最像的" if name == "search" else "对称比对（去重 / 相近反馈）")
        p.add_argument(arg)
        p.add_argument("-k", type=int, default=5)
        p.add_argument("--where", action="append", help="key=value，可多次")
        p.add_argument("--min-score", type=float, default=None)
        p.add_argument("--json", action="store_true")
        p.set_defaults(fn=fn)

    p = sub.add_parser("export", help="把集合导成 jsonl 到标准输出")
    p.add_argument("--where", action="append")
    p.set_defaults(fn=cmd_export)

    p = sub.add_parser("drop", help="删除整个集合")
    p.add_argument("--yes", action="store_true")
    p.set_defaults(fn=cmd_drop)

    args = ap.parse_args(argv)
    try:
        args.fn(args)
    except StoreError as e:
        raise SystemExit(f"向量库错误：{e}")


if __name__ == "__main__":
    main(sys.argv[1:])
