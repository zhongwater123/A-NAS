"""EmbeddingGemma 2 的 Hugging Face 权重起成一个 OpenAI 兼容的 /v1/embeddings 服务（开发机用）。

Worker 跑的是 LiteRT 文件（MediaPipe，见 anas_ai/providers.py）；微调产出的是 Hugging Face
权重，要和基座对比、或在转成 LiteRT 之前先看效果，就用这个服务 + embedders.HttpEmbedder。

用法（在 ai/ 目录下）：
  python -m finetune.server                        # 默认 127.0.0.1:8089，加载 google/embeddinggemma-2，只加载文本编码器
  python -m finetune.server --model finetune/out/my-embeddinggemma-2   # 用微调后的权重
  python -m finetune.server --fake                 # 不加载模型，用哈希向量，只为联调接口

依赖（只有真模型需要）：pip install -r finetune/requirements.txt
  - 要先在 Hugging Face 接受 Gemma 条款并 huggingface-cli login。
  - 官方要求 bf16 或 fp32，不要 fp16（会出 NaN）。
  - 没有 GPU 也能跑：270M 文本编码器在 CPU 上一条短文本几十毫秒量级。

接口：
  POST /v1/embeddings  {"model": "...", "input": ["..."] 或 "..."}
      -> {"data": [{"index": 0, "embedding": [...]}, ...], "model": "...", "dims": 768}
      输入文本应已带任务前缀（embedders.PROMPTS）；本服务不改写输入。
  GET  /health         -> {"ok": true, "model": "...", "fake": bool}
"""

from __future__ import annotations

import argparse
import json
import os
import sys
import threading
import time
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))
from finetune.embedders import HashEmbedder  # noqa: E402


class GemmaBackend:
    """sentence-transformers 封装；只在真的要用时才 import，保持脚本在无依赖机器上可读可测。"""

    def __init__(self, model_id: str, device: str | None, dtype: str = "auto", max_seq_length: int = 0):
        from sentence_transformers import SentenceTransformer  # type: ignore
        import torch  # type: ignore
        kwargs: dict = {
            # 只要文本编码器：不加载视觉/音频，参数从 740M 降到 270M
            "config_kwargs": {"vision_config": None, "audio_config": None},
        }
        if device:
            kwargs["device"] = device
        # 精度：GPU 用 bf16；CPU 用 fp32——没有 AVX-512/AMX 的 CPU 上 bf16 靠软件模拟，
        # 实测比 fp32 慢 6 倍（文档段落 30.7 s 对 5.4 s）。官方只禁 fp16。
        on_cpu = (device or "").startswith("cpu") or (not device and not torch.cuda.is_available())
        if dtype == "auto":
            dtype = "fp32" if on_cpu else "bf16"
        kwargs["model_kwargs"] = {"torch_dtype": torch.float32 if dtype == "fp32" else torch.bfloat16}
        self.model = SentenceTransformer(model_id, **kwargs)
        if max_seq_length:
            self.model.max_seq_length = max_seq_length   # 超长段落截断，CPU 上控制单段耗时
        self.dtype = dtype
        self.name = model_id
        self.dims = self.model.get_sentence_embedding_dimension() or 768
        self.model.encode(["预热"], normalize_embeddings=True)

    def embed(self, texts: list[str]) -> list[list[float]]:
        out = self.model.encode(texts, normalize_embeddings=True, convert_to_numpy=True, batch_size=16)
        return [[float(x) for x in row] for row in out]


class FakeBackend:
    def __init__(self, dims: int = 768):
        self.h = HashEmbedder(dims)
        self.name = "fake-hash"
        self.dims = dims

    def embed(self, texts: list[str]) -> list[list[float]]:
        return self.h.embed(texts)


def make_handler(backend, fake: bool, lock: threading.Lock):
    class Handler(BaseHTTPRequestHandler):
        def _json(self, code: int, obj: dict) -> None:
            body = json.dumps(obj, ensure_ascii=False).encode("utf-8")
            self.send_response(code)
            self.send_header("Content-Type", "application/json; charset=utf-8")
            self.send_header("Content-Length", str(len(body)))
            self.end_headers()
            self.wfile.write(body)

        def do_GET(self):  # noqa: N802
            if self.path.rstrip("/") in ("/health", ""):
                self._json(200, {"ok": True, "model": backend.name, "dims": backend.dims, "fake": fake})
            else:
                self._json(404, {"error": "not found"})

        def do_POST(self):  # noqa: N802
            if self.path.rstrip("/") != "/v1/embeddings":
                self._json(404, {"error": "not found"})
                return
            try:
                n = int(self.headers.get("Content-Length", "0"))
                req = json.loads(self.rfile.read(n).decode("utf-8") or "{}")
                inputs = req.get("input", [])
                if isinstance(inputs, str):
                    inputs = [inputs]
                if not isinstance(inputs, list) or not all(isinstance(t, str) for t in inputs):
                    self._json(400, {"error": "input 必须是字符串或字符串数组"})
                    return
                if len(inputs) > 256:
                    self._json(400, {"error": "一次最多 256 条"})
                    return
                t0 = time.time()
                with lock:
                    vecs = backend.embed(inputs) if inputs else []
                self._json(200, {
                    "object": "list", "model": backend.name, "dims": backend.dims,
                    "data": [{"object": "embedding", "index": i, "embedding": v} for i, v in enumerate(vecs)],
                    "usage": {"prompt_texts": len(inputs), "seconds": round(time.time() - t0, 3)},
                })
            except Exception as e:  # 不让一次坏请求弄死服务
                self._json(500, {"error": str(e)})

        def log_message(self, fmt, *args):  # 安静一点
            if "/v1/embeddings" not in (args[0] if args else ""):
                super().log_message(fmt, *args)

    return Handler


def serve(host: str, port: int, backend, fake: bool) -> ThreadingHTTPServer:
    return ThreadingHTTPServer((host, port), make_handler(backend, fake, threading.Lock()))


def main(argv: list[str]) -> None:
    ap = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument("--host", default="127.0.0.1")
    ap.add_argument("--port", type=int, default=8089)
    ap.add_argument("--model", default="google/embeddinggemma-2", help="HF 模型 id 或本地微调目录")
    ap.add_argument("--device", default=None, help="cuda / cpu，默认自动")
    ap.add_argument("--fake", action="store_true", help="不加载模型，用哈希向量联调")
    ap.add_argument("--dtype", choices=("auto", "fp32", "bf16"), default="auto", help="auto：CPU 用 fp32，GPU 用 bf16")
    ap.add_argument("--max-seq-length", type=int, default=0, help="超过此 token 数的输入截断，0=模型默认（8192）")
    args = ap.parse_args(argv)

    if args.fake:
        backend = FakeBackend()
    else:
        try:
            backend = GemmaBackend(args.model, args.device, args.dtype, args.max_seq_length)
        except ImportError:
            sys.exit("缺少 sentence-transformers：pip install -r finetune/requirements.txt，或加 --fake 只联调接口。")
    srv = serve(args.host, args.port, backend, args.fake)
    print(f"向量服务就绪 http://{args.host}:{args.port}  模型 {backend.name}  {backend.dims} 维  fake={args.fake}"
          f"  dtype={getattr(backend, 'dtype', '-')}  max_seq_length={args.max_seq_length or '默认'}", flush=True)
    print("调用方这样接：ANAS_EMBED_URL=http://%s:%d" % (args.host, args.port), flush=True)
    try:
        srv.serve_forever()
    except KeyboardInterrupt:
        pass


if __name__ == "__main__":
    main(sys.argv[1:])
