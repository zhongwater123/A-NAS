"""EmbeddingGemma 2 视频片段编码方式对比。

对同一批视频片段，用同一批抽帧，分别按几种方式编码，再用一组中文查询比较命中率和耗时：

  native      原生视频模式：整段抽帧一次送进模型，得到一个向量（模型内部加时间戳）
  mean        多帧均值：每帧单独编成图片向量，取平均后归一化，得到一个向量
  maxsim      多帧 MaxSim：保留每帧向量，查询与每帧算余弦取最大值
  nativetext  原生视频模式加文字描述：描述和画面交错输入，得到一个向量（需要 --captions）

mean 和 maxsim 共用同一批帧向量，所以两者的编码耗时相同，只是打分方式不同。

抽帧按 A-NAS 相册缩略图的规格对齐（长边 512 px、EXIF 转正、透明合成白底），
这样编出来的向量才有可能和相册里的互通；--edge 可以改。

用法：
  python embed_bench.py --clips 片段目录 --queries queries.json --out out
  python embed_bench.py --clips 片段目录 --make-template queries.json   # 生成标注模板
  python embed_bench.py --clips 片段目录 --queries queries.json --out out --fake   # 不加载模型，只验证流程

片段目录里每个视频文件是一个片段（.mp4 .mov .mkv .avi .m4v .webm），或者每个子目录是一个片段、
里面放已抽好的帧（.jpg .png）。抽帧用 ffmpeg，结果缓存在 out/frames/<片段>/ 下，第二次运行不再抽。
查询文件格式见 queries.example.json，relevant 为空时只列 top-k 不算命中率。
"""

from __future__ import annotations

import argparse
import hashlib
import json
import shutil
import subprocess
import sys
import time
from pathlib import Path

import numpy as np

VIDEO_EXTS = {".mp4", ".mov", ".mkv", ".avi", ".m4v", ".webm", ".insv", ".lrv"}
IMAGE_EXTS = {".jpg", ".jpeg", ".png", ".webp"}
MODEL_ID = "google/embeddinggemma-2"
# 官方检索提示词：只加在查询文本上，图片、视频不加。
QUERY_PROMPT = "task: search result | query: "
ALL_METHODS = ("native", "mean", "maxsim", "nativetext")
TOP_K = 5


# ---------------------------------------------------------------- 输入


def find_clips(clips_dir: Path) -> list[tuple[str, Path]]:
    """返回 (片段 ID, 路径)。路径是视频文件或装着帧的目录。"""
    clips = []
    for p in sorted(clips_dir.iterdir()):
        if p.is_file() and p.suffix.lower() in VIDEO_EXTS:
            clips.append((p.stem, p))
        elif p.is_dir() and any(f.suffix.lower() in IMAGE_EXTS for f in p.iterdir()):
            clips.append((p.name, p))
    if not clips:
        sys.exit(f"{clips_dir} 里没有视频文件，也没有装着帧的子目录")
    return clips


def extract_frames(clip_id: str, source: Path, frames_root: Path, fps: float, max_frames: int,
                   edge: int) -> list[Path]:
    """抽帧到 frames_root/clip_id/，已有则直接用。返回帧文件列表（按时间顺序，至多 max_frames 张）。

    视频经 ffmpeg 抽帧并缩到长边 edge；预先抽好的帧目录原样返回，缩放在读帧时做。
    """
    out = frames_root / clip_id
    if source.is_dir():
        frames = sorted(f for f in source.iterdir() if f.suffix.lower() in IMAGE_EXTS)
        return frames[:max_frames]
    if out.is_dir() and any(out.iterdir()):
        return sorted(out.glob("*.jpg"))[:max_frames]
    if shutil.which("ffmpeg") is None:
        sys.exit("找不到 ffmpeg。装一个加进 PATH，或者把片段预先抽成帧目录再跑。")
    out.mkdir(parents=True, exist_ok=True)
    scale = (f"scale=w='if(gt(iw,ih),min({edge},iw),-2)':h='if(gt(iw,ih),-2,min({edge},ih))'")
    cmd = [
        "ffmpeg", "-hide_banner", "-loglevel", "error", "-y",
        "-i", str(source),
        "-vf", f"fps={fps},{scale}",
        "-frames:v", str(max_frames),
        "-q:v", "2",
        str(out / "%04d.jpg"),
    ]
    subprocess.run(cmd, check=True)
    frames = sorted(out.glob("*.jpg"))
    if not frames:
        sys.exit(f"{source} 没抽出任何帧")
    return frames


def load_queries(path: Path) -> list[dict]:
    data = json.loads(path.read_text(encoding="utf-8-sig"))
    if not isinstance(data, list) or not data:
        sys.exit("查询文件要是一个非空列表")
    for q in data:
        if "query" not in q:
            sys.exit(f"查询缺少 query 字段：{q}")
        q.setdefault("relevant", [])
    return data


def load_captions(path: Path | None, clip_ids: list[str]) -> dict[str, str]:
    """clip_id → 描述文字。文件是 {"clip_001": "小孩在沙滩上跑", ...}。"""
    if path is None:
        return {}
    data = json.loads(path.read_text(encoding="utf-8-sig"))
    missing = [c for c in clip_ids if not data.get(c)]
    if missing:
        print(f"警告：{len(missing)} 个片段没有描述，nativetext 对它们退回只用画面：{missing[:5]}…", file=sys.stderr)
    return {c: str(t) for c, t in data.items() if t}


def write_template(clips: list[tuple[str, Path]], path: Path) -> None:
    """生成标注模板：几条示例查询，relevant 留空，末尾附全部片段 ID 方便填。"""
    template = [
        {"query": "小孩在沙滩上跑", "relevant": []},
        {"query": "一群人在餐桌上吃饭", "relevant": []},
        {"query": "日落时的海边", "relevant": []},
        {"_clips": [c for c, _ in clips], "_note": "把片段 ID 填进上面各条的 relevant；这一项本身会被忽略"},
    ]
    path.write_text(json.dumps(template, ensure_ascii=False, indent=2), encoding="utf-8")
    print(f"模板写到 {path}，共 {len(clips)} 个片段")


# ---------------------------------------------------------------- 图片预处理（对齐 A-NAS 相册缩略图）


def load_frame(path: Path, edge: int):
    """按相册 thumbnail/v1 的规格读帧：EXIF 转正、透明合成白底、长边缩到 edge。"""
    from PIL import Image, ImageOps

    img = Image.open(path)
    img = ImageOps.exif_transpose(img)
    if img.mode in ("RGBA", "LA") or (img.mode == "P" and "transparency" in img.info):
        rgba = img.convert("RGBA")
        white = Image.new("RGBA", rgba.size, (255, 255, 255, 255))
        img = Image.alpha_composite(white, rgba)
    img = img.convert("RGB")
    if max(img.size) > edge:
        img.thumbnail((edge, edge), Image.LANCZOS)
    return img


# ---------------------------------------------------------------- 编码器


class FakeEmbedder:
    """不加载模型：按输入内容哈希出确定性向量，只用来验证流程。"""

    load_seconds = 0.0
    notes: list[str]

    def __init__(self, truncate_dim: int | None):
        self.dims = truncate_dim or 768
        self.notes = []

    def _vec(self, key: bytes) -> np.ndarray:
        seed = int.from_bytes(hashlib.sha256(key).digest()[:8], "little")
        v = np.random.default_rng(seed).standard_normal(self.dims).astype(np.float32)
        return v / np.linalg.norm(v)

    def warmup(self) -> None:
        pass

    def encode_queries(self, texts: list[str]) -> np.ndarray:
        return np.stack([self._vec(t.encode("utf-8")) for t in texts])

    def encode_images(self, frames: list[Path]) -> np.ndarray:
        return np.stack([self._vec(f.read_bytes()) for f in frames])

    def encode_video(self, frames: list[Path], source: Path | None = None, caption: str = "") -> np.ndarray:
        return self._vec(caption.encode("utf-8") + b"".join(f.read_bytes() for f in frames))

    def peak_memory_mb(self) -> float:
        return 0.0

    def reset_peak(self) -> None:
        pass

    def sync(self) -> None:
        pass


class GemmaEmbedder:
    """sentence-transformers 6.1+ 上的 EmbeddingGemma 2，只加载文本和视觉编码器（440M）。

    多模态的 processing_kwargs 按官方文档写；库版本不兼容时自动去掉这些参数再试一次，
    并把降级记在 notes 里，报告会带上。
    """

    def __init__(self, device: str, image_tokens: int, truncate_dim: int | None, batch_size: int,
                 edge: int, native_from_file: bool):
        import torch
        from sentence_transformers import SentenceTransformer

        self.torch = torch
        self.device = device
        self.image_tokens = image_tokens
        self.truncate_dim = truncate_dim
        self.batch_size = batch_size
        self.edge = edge
        self.native_from_file = native_from_file
        self.notes: list[str] = []
        t0 = time.perf_counter()
        self.model = SentenceTransformer(
            MODEL_ID,
            device=device,
            model_kwargs={"torch_dtype": torch.bfloat16},   # 官方：不要用 float16，会出 NaN
            config_kwargs={"audio_config": None},            # 不加载音频编码器
        )
        self.load_seconds = time.perf_counter() - t0
        self.dims = truncate_dim or self.model.get_sentence_embedding_dimension() or 768

    def _encode(self, inputs, processing_kwargs: dict | None = None, **kw) -> np.ndarray:
        common = dict(normalize_embeddings=True, truncate_dim=self.truncate_dim, convert_to_numpy=True, **kw)
        try:
            out = self.model.encode(inputs, processing_kwargs=processing_kwargs, **common)
        except TypeError as e:
            if processing_kwargs is None:
                raise
            note = f"processing_kwargs 不被接受（{e}），已按库默认参数编码"
            if note not in self.notes:
                self.notes.append(note)
                print("警告：" + note, file=sys.stderr)
            out = self.model.encode(inputs, **common)
        return np.asarray(out, dtype=np.float32)

    def warmup(self) -> None:
        """跑一次很小的输入，把模型加载、XNNPack 之类的一次性开销排除在计时之外。"""
        from PIL import Image

        self._encode(["预热"], prompt=QUERY_PROMPT)
        self._encode([{"image": Image.new("RGB", (64, 64), (128, 128, 128))}],
                     processing_kwargs={"image": {"max_soft_tokens": self.image_tokens}})
        self.sync()

    def encode_queries(self, texts: list[str]) -> np.ndarray:
        return self._encode(texts, prompt=QUERY_PROMPT, batch_size=self.batch_size)

    def encode_images(self, frames: list[Path]) -> np.ndarray:
        images = [{"image": load_frame(f, self.edge)} for f in frames]
        return self._encode(images, processing_kwargs={"image": {"max_soft_tokens": self.image_tokens}},
                            batch_size=self.batch_size)

    def encode_video(self, frames: list[Path], source: Path | None = None, caption: str = "") -> np.ndarray:
        """一段视频一个向量。默认把同一批抽帧交给模型；--native-from-file 时让模型自己解码文件。

        有 caption 时按官方交错格式：文字里放 <|video|> 占位符，画面插在那个位置。
        """
        if self.native_from_file and source is not None and source.is_file():
            item: dict = {"video": str(source)}
            kw = {"video": {"fps": 1, "max_frames": len(frames), "add_timestamps": True}}
        else:
            item = {"video": [load_frame(f, self.edge) for f in frames]}
            kw = {"video": {"fps": None, "max_frames": len(frames), "add_timestamps": True}}
        if caption:
            item["text"] = f"{caption} <|video|>"
        vec = self._encode(item, processing_kwargs=kw)
        return vec.reshape(-1)

    def peak_memory_mb(self) -> float:
        if self.device.startswith("cuda"):
            return self.torch.cuda.max_memory_allocated() / 2**20
        return 0.0

    def reset_peak(self) -> None:
        if self.device.startswith("cuda"):
            self.torch.cuda.synchronize()
            self.torch.cuda.reset_peak_memory_stats()

    def sync(self) -> None:
        if self.device.startswith("cuda"):
            self.torch.cuda.synchronize()


# ---------------------------------------------------------------- 打分与指标


def l2(v: np.ndarray) -> np.ndarray:
    n = np.linalg.norm(v, axis=-1, keepdims=True)
    return v / np.maximum(n, 1e-12)


def score_single(q: np.ndarray, clips: np.ndarray) -> np.ndarray:
    """每个片段一个向量：返回 (nq, nc) 余弦。"""
    return q @ clips.T


def score_maxsim(q: np.ndarray, frames: np.ndarray, owner: np.ndarray, n_clips: int) -> np.ndarray:
    """每帧一个向量：对每个片段取其所有帧中的最大余弦。owner[i] 是第 i 帧属于哪个片段。"""
    per_frame = q @ frames.T                                   # (nq, nf)
    out = np.full((q.shape[0], n_clips), -1.0, dtype=np.float32)
    for c in range(n_clips):
        cols = owner == c
        if cols.any():
            out[:, c] = per_frame[:, cols].max(axis=1)
    return out


def mean_by_owner(frames: np.ndarray, owner: np.ndarray, n_clips: int) -> np.ndarray:
    """每个片段的帧向量取均值再归一化。没有帧的片段是零向量。"""
    out = np.zeros((n_clips, frames.shape[1]), np.float32)
    for c in range(n_clips):
        rows = owner == c
        if rows.any():
            out[c] = frames[rows].mean(axis=0)
    return l2(out)


def metrics(scores: np.ndarray, queries: list[dict], clip_ids: list[str]) -> dict:
    """Hit@1、Hit@5、Recall@5、MRR，只对有 relevant 标注的查询算。"""
    ranked = np.argsort(-scores, axis=1)
    hit1 = hit5 = rec5 = mrr = 0.0
    labelled = 0
    for i, q in enumerate(queries):
        rel = set(q["relevant"])
        if not rel:
            continue
        labelled += 1
        order = [clip_ids[j] for j in ranked[i]]
        hit1 += order[0] in rel
        top5 = order[:TOP_K]
        hit5 += any(c in rel for c in top5)
        rec5 += len(rel & set(top5)) / len(rel)
        for rank, c in enumerate(order, 1):
            if c in rel:
                mrr += 1 / rank
                break
    if labelled == 0:
        return {"labelled_queries": 0}
    return {
        "labelled_queries": labelled,
        "hit@1": round(hit1 / labelled, 4),
        "hit@5": round(hit5 / labelled, 4),
        "recall@5": round(rec5 / labelled, 4),
        "mrr": round(mrr / labelled, 4),
    }


def topk_table(scores: np.ndarray, queries: list[dict], clip_ids: list[str]) -> list[dict]:
    ranked = np.argsort(-scores, axis=1)
    rows = []
    for i, q in enumerate(queries):
        rel = set(q["relevant"])
        top = [{"clip": clip_ids[j], "score": round(float(scores[i, j]), 4), "relevant": clip_ids[j] in rel}
               for j in ranked[i][:TOP_K]]
        rows.append({"query": q["query"], "top": top})
    return rows


def per_hour_estimate(seconds: float, clips: int, frames: int, fps: float) -> float | None:
    """按本次的每帧或每段耗时，估算一小时素材要多少秒。按抽帧数折算素材时长，是估算。"""
    if clips == 0 or frames == 0 or fps <= 0:
        return None
    footage_seconds = frames / fps
    return round(seconds * 3600 / footage_seconds, 1)


# ---------------------------------------------------------------- 主流程


def parse_args(argv: list[str] | None = None) -> argparse.Namespace:
    ap = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument("--clips", required=True, type=Path, help="片段目录")
    ap.add_argument("--queries", type=Path, help="查询 JSON")
    ap.add_argument("--out", type=Path, help="输出目录")
    ap.add_argument("--make-template", type=Path, metavar="PATH", help="只生成查询标注模板到这个路径，然后退出")
    ap.add_argument("--captions", type=Path, help="片段描述 JSON（clip_id → 文字），有它才跑 nativetext")
    ap.add_argument("--fps", type=float, default=1.0, help="抽帧率，默认 1（官方视频模式默认值）")
    ap.add_argument("--max-frames", type=int, default=32, help="每段最多几帧，默认 32（官方 max_frames 默认值）")
    ap.add_argument("--edge", type=int, default=512, help="帧的长边像素，默认 512，对齐 A-NAS 相册缩略图")
    ap.add_argument("--image-tokens", type=int, default=140, choices=[70, 140, 280, 560, 1120],
                    help="单帧图片的 token 预算，默认 140（原生视频模式每帧也是 140）")
    ap.add_argument("--truncate-dim", type=int, default=None, choices=[128, 256, 512, 768],
                    help="Matryoshka 截断，默认 768")
    ap.add_argument("--device", default="cuda", help="cuda / cuda:0 / cpu")
    ap.add_argument("--batch-size", type=int, default=16)
    ap.add_argument("--methods", default="native,mean,maxsim,nativetext",
                    help="逗号分隔；nativetext 没有 --captions 时自动跳过")
    ap.add_argument("--native-from-file", action="store_true",
                    help="原生视频模式直接把视频文件交给模型自己解码（默认把同一批抽帧交给它，保证各方法看的是同样的帧）")
    ap.add_argument("--fake", action="store_true", help="不加载模型，用哈希向量验证流程")
    ap.add_argument("--reuse", action="store_true", help="out/embeddings.npz 存在时直接复用，只重算指标")
    args = ap.parse_args(argv)
    if args.make_template is None and (args.queries is None or args.out is None):
        ap.error("要么给 --queries 和 --out，要么给 --make-template")
    return args


def main(argv: list[str] | None = None) -> None:
    args = parse_args(argv)
    clips = find_clips(args.clips)
    clip_ids = [c for c, _ in clips]
    if args.make_template is not None:
        write_template(clips, args.make_template)
        return

    methods = [m.strip() for m in args.methods.split(",") if m.strip()]
    bad = [m for m in methods if m not in ALL_METHODS]
    if bad:
        sys.exit(f"不认识的方法：{bad}，可选 {list(ALL_METHODS)}")
    captions = load_captions(args.captions, clip_ids)
    if "nativetext" in methods and not captions:
        print("没有 --captions，跳过 nativetext")
        methods = [m for m in methods if m != "nativetext"]

    args.out.mkdir(parents=True, exist_ok=True)
    frames_root = args.out / "frames"
    queries = load_queries(args.queries)
    unknown = {c for q in queries for c in q["relevant"]} - set(clip_ids)
    if unknown:
        print(f"警告：查询里标注的这些片段不存在：{sorted(unknown)}", file=sys.stderr)

    print(f"片段 {len(clips)} 个，查询 {len(queries)} 条，方法 {methods}")
    t0 = time.perf_counter()
    frames_of: dict[str, list[Path]] = {}
    for clip_id, source in clips:
        frames_of[clip_id] = extract_frames(clip_id, source, frames_root, args.fps, args.max_frames, args.edge)
    n_frames = sum(len(v) for v in frames_of.values())
    extract_seconds = time.perf_counter() - t0
    print(f"抽帧 {n_frames} 张，{extract_seconds:.1f} 秒（已缓存的不计时）")

    cache = args.out / "embeddings.npz"
    timing: dict = {}
    notes: list[str] = []
    if args.reuse and cache.exists():
        data = np.load(cache, allow_pickle=True)
        q_vec, frame_vec, owner = data["queries"], data["frames"], data["owner"]
        native_vec, nativetext_vec = data["native"], data["nativetext"]
        timing = json.loads(str(data["timing"]))
        notes = list(json.loads(str(data["notes"])))
        print(f"复用 {cache}")
    else:
        if args.fake:
            emb = FakeEmbedder(args.truncate_dim)
        else:
            emb = GemmaEmbedder(args.device, args.image_tokens, args.truncate_dim, args.batch_size,
                                args.edge, args.native_from_file)
        print(f"模型加载 {emb.load_seconds:.1f} 秒，向量 {emb.dims} 维")
        timing["model_load_seconds"] = round(emb.load_seconds, 2)
        t0 = time.perf_counter()
        emb.warmup()
        timing["warmup_seconds"] = round(time.perf_counter() - t0, 2)

        t0 = time.perf_counter()
        q_vec = l2(emb.encode_queries([q["query"] for q in queries]))
        emb.sync()
        secs = time.perf_counter() - t0
        timing["queries"] = {"seconds": round(secs, 3), "count": len(queries),
                             "ms_per_query": round(secs * 1000 / len(queries), 1)}

        frame_vec = np.zeros((0, emb.dims), np.float32)
        owner = np.zeros(0, np.int32)
        if "mean" in methods or "maxsim" in methods:
            emb.reset_peak()
            t0 = time.perf_counter()
            parts, owners = [], []
            for ci, clip_id in enumerate(clip_ids):
                parts.append(emb.encode_images(frames_of[clip_id]))
                owners.append(np.full(len(frames_of[clip_id]), ci, np.int32))
            emb.sync()
            secs = time.perf_counter() - t0
            frame_vec, owner = l2(np.concatenate(parts)), np.concatenate(owners)
            timing["frames"] = {"seconds": round(secs, 2), "frames": n_frames, "clips": len(clips),
                                "seconds_per_clip": round(secs / len(clips), 3),
                                "seconds_per_frame": round(secs / max(n_frames, 1), 4),
                                "seconds_per_hour_of_footage": per_hour_estimate(secs, len(clips), n_frames, args.fps),
                                "peak_memory_mb": round(emb.peak_memory_mb(), 1)}
            print(f"帧向量 {n_frames} 张 {secs:.1f} 秒")

        def run_native(key: str, with_caption: bool) -> np.ndarray:
            emb.reset_peak()
            t0 = time.perf_counter()
            rows = []
            for clip_id, source in clips:
                cap = captions.get(clip_id, "") if with_caption else ""
                rows.append(emb.encode_video(frames_of[clip_id], source, cap))
            emb.sync()
            secs = time.perf_counter() - t0
            timing[key] = {"seconds": round(secs, 2), "clips": len(clips),
                           "seconds_per_clip": round(secs / len(clips), 3),
                           "seconds_per_hour_of_footage": per_hour_estimate(secs, len(clips), n_frames, args.fps),
                           "peak_memory_mb": round(emb.peak_memory_mb(), 1)}
            print(f"{key} {len(clips)} 段 {secs:.1f} 秒")
            return l2(np.stack(rows))

        native_vec = run_native("native", False) if "native" in methods else np.zeros((0, emb.dims), np.float32)
        nativetext_vec = (run_native("nativetext", True) if "nativetext" in methods
                          else np.zeros((0, emb.dims), np.float32))
        notes = list(emb.notes)
        np.savez(cache, queries=q_vec, frames=frame_vec, owner=owner, native=native_vec,
                 nativetext=nativetext_vec, timing=json.dumps(timing, ensure_ascii=False),
                 notes=json.dumps(notes, ensure_ascii=False))

    results: dict[str, np.ndarray] = {}
    score_ms: dict[str, float] = {}

    def timed(name: str, fn):
        t0 = time.perf_counter()
        results[name] = fn()
        score_ms[name] = round((time.perf_counter() - t0) * 1000, 2)

    if "native" in methods and len(native_vec):
        timed("native", lambda: score_single(q_vec, native_vec))
    if "mean" in methods and len(frame_vec):
        timed("mean", lambda: score_single(q_vec, mean_by_owner(frame_vec, owner, len(clips))))
    if "maxsim" in methods and len(frame_vec):
        timed("maxsim", lambda: score_maxsim(q_vec, frame_vec, owner, len(clips)))
    if "nativetext" in methods and len(nativetext_vec):
        timed("nativetext", lambda: score_single(q_vec, nativetext_vec))

    report = {
        "config": {k: (str(v) if isinstance(v, Path) else v) for k, v in vars(args).items()},
        "clips": len(clips), "frames": n_frames, "queries": len(queries),
        "extract_seconds": round(extract_seconds, 2),
        "timing": timing,
        "scoring_ms_all_queries": score_ms,
        "notes": notes,
        "metrics": {m: metrics(s, queries, clip_ids) for m, s in results.items()},
        "topk": {m: topk_table(s, queries, clip_ids) for m, s in results.items()},
    }
    (args.out / "report.json").write_text(json.dumps(report, ensure_ascii=False, indent=2), encoding="utf-8")
    (args.out / "report.md").write_text(render_markdown(report), encoding="utf-8")
    print(render_markdown(report, with_topk=False))
    print(f"报告：{args.out / 'report.md'}")


def render_markdown(r: dict, with_topk: bool = True) -> str:
    c = r["config"]
    lines = ["# EmbeddingGemma 2 片段编码对比", "",
             f"片段 {r['clips']} 段，抽帧 {r['frames']} 张，查询 {r['queries']} 条；"
             f"fps={c['fps']}，max_frames={c['max_frames']}，edge={c['edge']}，"
             f"image_tokens={c['image_tokens']}，dim={c['truncate_dim'] or 768}，fake={c['fake']}", ""]
    t = r["timing"]
    lines += ["## 耗时", "", "| 项目 | 秒 | 每段 | 一小时素材估算（秒） | 峰值显存 MB |", "|---|---|---|---|---|"]
    if "frames" in t:
        f = t["frames"]
        lines.append(f"| 逐帧编码（mean 和 maxsim 共用） | {f['seconds']} | {f['seconds_per_clip']} | "
                     f"{f['seconds_per_hour_of_footage']} | {f['peak_memory_mb']} |")
    for key, label in (("native", "原生视频模式"), ("nativetext", "原生视频加描述")):
        if key in t:
            n = t[key]
            lines.append(f"| {label} | {n['seconds']} | {n['seconds_per_clip']} | "
                         f"{n['seconds_per_hour_of_footage']} | {n['peak_memory_mb']} |")
    if "queries" in t:
        q = t["queries"]
        lines.append(f"| 查询编码 {q['count']} 条 | {q['seconds']} | {q['ms_per_query']} ms/条 | | |")
    lines.append(f"| 模型加载 / 预热 | {t.get('model_load_seconds', 0)} / {t.get('warmup_seconds', 0)} | | | |")
    if r.get("scoring_ms_all_queries"):
        parts = ", ".join(f"{m} {ms} ms" for m, ms in r["scoring_ms_all_queries"].items())
        lines += ["", f"打分（全部查询对全部片段）：{parts}"]
    lines += ["", "## 命中率", "", "| 方法 | Hit@1 | Hit@5 | Recall@5 | MRR | 标注查询数 |", "|---|---|---|---|---|---|"]
    for m, v in r["metrics"].items():
        if v.get("labelled_queries"):
            lines.append(f"| {m} | {v['hit@1']} | {v['hit@5']} | {v['recall@5']} | {v['mrr']} | {v['labelled_queries']} |")
        else:
            lines.append(f"| {m} | 无标注 | | | | 0 |")
    if r.get("notes"):
        lines += ["", "## 运行中的降级", ""] + [f"- {n}" for n in r["notes"]]
    if with_topk:
        lines += ["", "## 每条查询的前 5", ""]
        for m, rows in r["topk"].items():
            lines += [f"### {m}", ""]
            for row in rows:
                hits = ", ".join(f"{'**' if h['relevant'] else ''}{h['clip']} {h['score']}{'**' if h['relevant'] else ''}"
                                 for h in row["top"])
                lines.append(f"- {row['query']}：{hits}")
            lines.append("")
    return "\n".join(lines)


if __name__ == "__main__":
    main()
