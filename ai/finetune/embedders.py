"""文本向量（embedding）的统一接口，只用标准库（开发机用，不进 Worker 运行环境）。

把"怎么拿到向量"抽成一个可替换的接口，评估和微调脚本不关心模型跑在哪：

- ``ProviderEmbedder`` 包装 ``anas_ai.providers.MediaPipeProvider``：和 Worker 同一个
                       ``.litertlm`` 文件、同一条 MediaPipe 路径，只是自己加任务前缀，
                       所以能评估检索以外的任务（句子相似、去重、按样例分类）。
- ``HttpEmbedder``     调任何 OpenAI 兼容的 ``/v1/embeddings`` 服务（本目录的 server.py
                       用 sentence-transformers 跑 Hugging Face 权重，微调前后对比用）。
- ``HashEmbedder``     不需要模型的退路：字符 n-gram 特征哈希，给单元测试和分数下限用。
- ``CachedEmbedder``   给任意 embedder 加一层磁盘缓存，同一段文本只算一次。

向量运算（cosine / truncate / top_k）都是纯函数，只用标准库。

EmbeddingGemma 系列是"带任务前缀"训练的，前缀不对，相似度会整体偏移。
前缀常量见 ``PROMPTS``，``HttpEmbedder`` 默认自动加前缀；如果服务端自己会加
（TEI 的 prompt_name），把 ``add_prefix`` 关掉。
"""

from __future__ import annotations

import hashlib
import http.client
import json
import math
import os
import urllib.error
import urllib.request
from pathlib import Path
from typing import Iterable, Optional, Protocol

# EmbeddingGemma / EmbeddingGemma 2 官方模型卡里的任务前缀（原样，含空格）
PROMPTS = {
    "query": "task: search result | query: ",
    "document": "title: none | text: ",
    "sts": "task: sentence similarity | query: ",
    "clustering": "task: clustering | query: ",
    "classification": "task: classification | query: ",
}

Vector = list[float]

# 前缀表的版本：前缀改一个字，同样的文本就落到不同的向量上，所以它是向量空间身份的一部分
PROMPTS_VERSION = hashlib.sha256(json.dumps(PROMPTS, sort_keys=True, ensure_ascii=False).encode("utf-8")).hexdigest()[:12]


def space_of(emb) -> dict:
    """embedder 的向量空间身份：哪些东西变了，同一段文本的向量就不再可比（A-NAS #152 R1）。

    包括实现、权重（服务端报告的指纹）、运行时、前缀版本、维度、归一化方式。只有名字和维度相同不算同一空间。
    """
    if hasattr(emb, "space"):
        return emb.space()
    return {"impl": type(emb).__name__, "name": getattr(emb, "name", None), "dim": getattr(emb, "dim", None)}


def space_fingerprint(space: dict) -> str:
    return hashlib.sha256(json.dumps(space, sort_keys=True, ensure_ascii=False).encode("utf-8")).hexdigest()


# ---------- 纯函数 ----------

def normalize(v: Iterable[float]) -> Vector:
    v = list(v)
    n = math.sqrt(sum(x * x for x in v))
    return [x / n for x in v] if n > 0 else v


def truncate(v: Vector, dim: Optional[int]) -> Vector:
    """Matryoshka 截断：取前 dim 维再归一化（官方说明 256 维几乎无损）。"""
    if not dim or dim >= len(v):
        return normalize(v)
    return normalize(v[:dim])


def cosine(a: Vector, b: Vector) -> float:
    if len(a) != len(b) or not a:
        return 0.0
    dot = sum(x * y for x, y in zip(a, b))
    na = math.sqrt(sum(x * x for x in a))
    nb = math.sqrt(sum(y * y for y in b))
    return dot / (na * nb) if na and nb else 0.0


def top_k(query: Vector, candidates: list[Vector], k: int = 5) -> list[tuple[int, float]]:
    """返回 [(候选下标, 相似度)]，按相似度从高到低。"""
    scored = [(i, cosine(query, c)) for i, c in enumerate(candidates)]
    scored.sort(key=lambda x: x[1], reverse=True)
    return scored[:k]


# ---------- 接口 ----------

class Embedder(Protocol):
    name: str
    dim: int
    semantic: bool          # True = 真模型；False = 哈希退路（阈值要低、结论要弱）

    def embed(self, texts: list[str], kind: str = "sts") -> list[Vector]: ...


class HashEmbedder:
    """字符 n-gram 特征哈希。

    不理解语义，只看字面重叠，但对中英混排的日志 / 反馈文本够用来做
    "明显是同一句话换了个说法"这种粗匹配，而且零依赖、确定性、可测试。
    n 取 2 和 3：中文两三个字就是一个词，英文则靠 3-gram。
    """

    semantic = False

    def __init__(self, dim: int = 256, ngrams: tuple[int, ...] = (2, 3)):
        self.dim = dim
        self.ngrams = ngrams
        self.name = "hash-ngram"          # 缓存键改用 space()，名字只用于显示

    @staticmethod
    def _tokens(text: str, ngrams: tuple[int, ...]) -> list[str]:
        t = " ".join(text.lower().split())
        out: list[str] = []
        for n in ngrams:
            out.extend(t[i:i + n] for i in range(max(0, len(t) - n + 1)))
        # 再加整词（英文/路径/错误码），让 "EACCES" 这种 token 权重更稳
        out.extend(w for w in t.replace("/", " ").replace(":", " ").split() if len(w) > 1)
        return out

    def _vec(self, text: str) -> Vector:
        v = [0.0] * self.dim
        for tok in self._tokens(text, self.ngrams):
            h = hashlib.blake2b(tok.encode("utf-8"), digest_size=8).digest()
            idx = int.from_bytes(h[:4], "little") % self.dim
            sign = 1.0 if h[4] & 1 else -1.0
            v[idx] += sign
        return normalize(v)

    def embed(self, texts: list[str], kind: str = "sts") -> list[Vector]:
        # 哈希向量不区分 query/document，前缀对它没有意义
        return [self._vec(t) for t in texts]

    def space(self) -> dict:
        return {"impl": "hash-ngram", "version": 1, "dim": self.dim, "ngrams": list(self.ngrams), "normalize": "l2"}


class HttpEmbedder:
    """OpenAI 兼容的 /v1/embeddings 客户端（只用 urllib）。"""

    semantic = True

    def __init__(self, url: str, model: str = "google/embeddinggemma-2", dim: Optional[int] = 256,
                 timeout: float = 60.0, add_prefix: bool = True, api_key: Optional[str] = None,
                 batch_size: int = 32):
        self.url = url.rstrip("/")
        self.model = model
        self.dim = dim or 768
        self.timeout = timeout
        self.add_prefix = add_prefix
        self.api_key = api_key
        self.batch_size = batch_size
        self.name = f"http:{model}@{dim or 768}"
        self._server: Optional[dict] = None

    def _post(self, inputs: list[str]) -> list[Vector]:
        body = json.dumps({"model": self.model, "input": inputs}).encode("utf-8")
        headers = {"Content-Type": "application/json", "Accept": "application/json"}
        if self.api_key:
            headers["Authorization"] = f"Bearer {self.api_key}"
        req = urllib.request.Request(self.url + "/v1/embeddings", data=body, headers=headers, method="POST")
        try:
            with urllib.request.urlopen(req, timeout=self.timeout) as resp:
                payload = json.loads(resp.read().decode("utf-8"))
        except urllib.error.URLError as e:
            raise EmbedError(f"向量服务 {self.url} 不可用: {e}") from None
        data = sorted(payload.get("data", []), key=lambda d: d.get("index", 0))
        if len(data) != len(inputs):
            raise EmbedError(f"向量服务返回 {len(data)} 条，期望 {len(inputs)} 条")
        return [truncate(d["embedding"], self.dim) for d in data]

    def server_identity(self) -> dict:
        """向服务端 /health 要它实际加载的权重指纹和运行时。请求里的 model 名字代替不了实际权重。"""
        if self._server is None:
            req = urllib.request.Request(self.url + "/health", headers={"Accept": "application/json"})
            try:
                with urllib.request.urlopen(req, timeout=self.timeout) as resp:
                    h = json.loads(resp.read().decode("utf-8"))
            except (OSError, http.client.HTTPException, ValueError) as e:
                raise EmbedError(f"向量服务 {self.url} 报不出身份（/health）: {e}") from None
            self._server = {k: h.get(k) for k in ("model", "weights", "runtime", "dims", "fake")}
        return self._server

    def space(self) -> dict:
        return {"impl": "http", "url": self.url, "model": self.model, "dim": self.dim,
                "add_prefix": self.add_prefix, "prompts": PROMPTS_VERSION if self.add_prefix else None,
                "normalize": "truncate+l2", "server": self.server_identity()}

    def embed(self, texts: list[str], kind: str = "sts") -> list[Vector]:
        prefix = PROMPTS.get(kind, PROMPTS["sts"]) if self.add_prefix else ""
        inputs = [prefix + t for t in texts]
        out: list[Vector] = []
        for i in range(0, len(inputs), self.batch_size):
            out.extend(self._post(inputs[i:i + self.batch_size]))
        return out


class EmbedError(RuntimeError):
    pass


class ProviderEmbedder:
    """把 anas_ai.providers 的 Provider（Worker 用的 MediaPipe 路径）包成 Embedder。

    Worker 的 embed_query 固定加检索前缀；这里走 embed_text 自己加前缀，同一个模型文件
    就能做其他任务的评估。FakeProvider 没有 embed_text，测试里包一层即可。
    """

    semantic = True

    def __init__(self, provider, dim: Optional[int] = None, add_prefix: bool = True):
        self.provider = provider
        self.dim = dim or getattr(provider, "dimensions", 768)
        self.add_prefix = add_prefix
        self.name = f"provider:{getattr(provider, 'model', '?')}@{self.dim}"

    def embed(self, texts: list[str], kind: str = "sts") -> list[Vector]:
        prefix = PROMPTS.get(kind, PROMPTS["sts"]) if self.add_prefix else ""
        return [truncate([float(x) for x in self.provider.embed_text(prefix + t)], self.dim) for t in texts]

    def space(self) -> dict:
        # provider.model 已含权重哈希、MediaPipe 版本、视觉 token 与归一化（anas_ai.providers）
        return {"impl": "provider", "provider": type(self.provider).__name__, "model": getattr(self.provider, "model", None),
                "dim": self.dim, "add_prefix": self.add_prefix, "prompts": PROMPTS_VERSION if self.add_prefix else None,
                "normalize": "truncate+l2"}


class CachedEmbedder:
    """磁盘缓存：按 (embedder 名, kind, 文本) 的 sha256 存一行 JSON。

    反馈列表、规则示例这类文本几乎不变，缓存后每轮只对新文本调模型。
    """

    def __init__(self, inner: Embedder, path: Optional[Path]):
        self.inner = inner
        self.name = inner.name
        self.dim = inner.dim
        self.semantic = inner.semantic
        self._fp: Optional[str] = None
        self.path = Path(path) if path else None
        self._mem: dict[str, Vector] = {}
        if self.path and self.path.exists():
            for line in self.path.read_text(encoding="utf-8").splitlines():
                try:
                    k, v = json.loads(line)
                    self._mem[k] = v
                except Exception:
                    continue

    def space(self) -> dict:
        return space_of(self.inner)

    def _key(self, kind: str, text: str) -> str:
        # 键用完整的向量空间身份（含服务地址、前缀开关、服务端权重指纹与运行参数），不只是名字和维度（A-NAS #152 R1）
        if self._fp is None:
            self._fp = space_fingerprint(self.space())
        return hashlib.sha256(f"{self._fp}\x00{kind}\x00{text}".encode("utf-8")).hexdigest()


    def embed(self, texts: list[str], kind: str = "sts") -> list[Vector]:
        keys = [self._key(kind, t) for t in texts]
        missing = [(k, t) for k, t in zip(keys, texts) if k not in self._mem]
        if missing:
            vecs = self.inner.embed([t for _, t in missing], kind)
            new_lines = []
            for (k, _), v in zip(missing, vecs):
                self._mem[k] = v
                new_lines.append(json.dumps([k, v]))
            if self.path:
                self.path.parent.mkdir(parents=True, exist_ok=True)
                with self.path.open("a", encoding="utf-8") as f:
                    f.write("\n".join(new_lines) + "\n")
        return [self._mem[k] for k in keys]


# ---------- 装配 ----------

def build_embedder(cfg: Optional[dict], cache_dir: Optional[Path] = None, logger=None) -> Embedder:
    """按配置装配 embedder。

    cfg 形如 {"url": ..., "model": ..., "dim": 256, "cache": true}；url 为空或环境变量
    ANAS_EMBED_URL 未设置时退回 HashEmbedder，并在日志里说明。
    """
    cfg = cfg or {}
    url = os.environ.get("ANAS_EMBED_URL") or cfg.get("url")
    dim = cfg.get("dim", 256)
    if url:
        emb: Embedder = HttpEmbedder(url, cfg.get("model", "google/embeddinggemma-2"), dim,
                                     timeout=cfg.get("timeout_seconds", 60),
                                     add_prefix=cfg.get("add_prefix", True),
                                     api_key=os.environ.get("ANAS_EMBED_API_KEY") or cfg.get("api_key"))
        if logger:
            logger(f"[embed] 使用向量服务 {url}（{emb.name}）")
    else:
        emb = HashEmbedder(dim)
        if logger:
            logger("[embed] 未配置向量服务（url / ANAS_EMBED_URL），退回 n-gram 哈希，只能做字面相似")
    if cfg.get("cache", True) and cache_dir is not None:
        safe = "".join(c if c.isalnum() else "_" for c in emb.name)
        emb = CachedEmbedder(emb, Path(cache_dir) / f"{safe}.jsonl")
    return emb


def threshold_for(emb: Embedder, cfg: Optional[dict], key: str, semantic_default: float,
                  hash_default: float) -> float:
    """真模型和哈希退路用不同阈值：cosine 的量纲完全不同。"""
    cfg = cfg or {}
    if emb.semantic:
        return float(cfg.get(key, semantic_default))
    return float(cfg.get(key + "_hash", hash_default))
