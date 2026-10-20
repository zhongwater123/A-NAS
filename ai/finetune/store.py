"""向量库：把向量和附带信息（payload）存起来，按向量找最近的若干条（开发机用，不进 Worker 运行环境）。

两个实现，同一个接口（``VectorStore``）：

- ``QdrantStore``  连 Qdrant（默认 http://127.0.0.1:6333），只用 urllib 走 REST，
                   不依赖 qdrant-client。需要 Qdrant ≥ 1.10（用的是 points/query 接口）。
- ``MemoryStore``  进程内字典 + 暴力余弦，没起 Qdrant 的机器、测试、几百条规模用它；
                   可选落盘成一个 JSON 文件。

调用方拿到的是 ``VectorStore``，换实现只改装配（``build_store``）。
为什么选 Qdrant、怎么起、边界在哪，见 docs/research/qdrant-vector-store.md。

关于点的 id：Qdrant 只接受无符号整数或 UUID 字符串。调用方用任意字符串当 key
（文件路径、反馈编号、日志指纹……），本模块把它映射成 uuid5 当点 id，原 key
存在 payload 的 ``_key`` 字段里，查询结果再还回来。整数 key 原样使用。

过滤条件用 Qdrant 的 filter 语法（``{"must": [{"key": "source", "match": {"value": "n8n"}}]}``），
``MemoryStore`` 实现了其中最常用的子集（must / should / must_not + match value / any / range）。
``where(**kv)`` 是构造等值过滤的快捷方式。
"""

from __future__ import annotations

import json
import os
import urllib.error
import urllib.parse
import urllib.request
import uuid
from dataclasses import dataclass, field
from pathlib import Path
from typing import Any, Iterable, Optional, Protocol

from .embedders import Vector, cosine, normalize

# 统一的 uuid5 命名空间：同一个 key 在任何机器上都映射到同一个点 id
_NS = uuid.UUID("6f1c9a2e-0b7d-5e4a-9c33-a7d2e1f0b8c4")

Payload = dict[str, Any]
PointId = str | int


class StoreError(RuntimeError):
    pass


@dataclass
class ScoredPoint:
    key: str | int
    score: float
    payload: Payload = field(default_factory=dict)

    def as_dict(self) -> dict:
        return {"key": self.key, "score": round(self.score, 4), "payload": self.payload}


def point_id(key: str | int) -> PointId:
    """任意字符串 key -> Qdrant 能接受的点 id（整数原样，字符串转 uuid5）。"""
    if isinstance(key, bool):
        raise StoreError("key 不能是 bool")
    if isinstance(key, int):
        if key < 0:
            raise StoreError(f"整数 key 必须 ≥ 0，收到 {key}")
        return key
    try:
        return str(uuid.UUID(str(key)))      # 已经是 UUID 就直接用
    except ValueError:
        return str(uuid.uuid5(_NS, str(key)))


def where(**kv: Any) -> dict:
    """等值过滤的快捷写法：where(source="n8n", level="error")。值给列表表示"任一"。"""
    must = []
    for k, v in kv.items():
        if isinstance(v, (list, tuple, set)):
            must.append({"key": k, "match": {"any": list(v)}})
        else:
            must.append({"key": k, "match": {"value": v}})
    return {"must": must}


# ---------- 接口 ----------

class VectorStore(Protocol):
    name: str
    collection: str

    def ensure_collection(self, dim: int, recreate: bool = False) -> None: ...
    def drop_collection(self) -> None: ...
    def upsert(self, keys: list[str | int], vectors: list[Vector],
               payloads: Optional[list[Payload]] = None) -> None: ...
    def search(self, vector: Vector, k: int = 5, filter: Optional[dict] = None,
               min_score: Optional[float] = None) -> list[ScoredPoint]: ...
    def delete(self, keys: list[str | int]) -> None: ...
    def delete_where(self, filter: dict) -> None: ...
    def get(self, keys: list[str | int]) -> dict[str | int, Payload]: ...
    def count(self, filter: Optional[dict] = None) -> int: ...
    def scroll(self, filter: Optional[dict] = None, limit: int = 100) -> Iterable[tuple[str | int, Payload]]: ...
    def info(self) -> dict: ...


# ---------- 过滤器（MemoryStore 和测试里的假 Qdrant 共用） ----------

def _match_one(cond: dict, payload: Payload) -> bool:
    key = cond.get("key")
    val = payload.get(key) if key is not None else None
    if "match" in cond:
        m = cond["match"]
        if "value" in m:
            return val == m["value"] or (isinstance(val, list) and m["value"] in val)
        if "any" in m:
            opts = m["any"]
            if isinstance(val, list):
                return any(x in opts for x in val)
            return val in opts
        if "except" in m:
            return val not in m["except"]
        if "text" in m:
            return isinstance(val, str) and m["text"] in val
    if "range" in cond:
        r = cond["range"]
        if not isinstance(val, (int, float)) or isinstance(val, bool):
            return False
        return all([
            r.get("gt") is None or val > r["gt"],
            r.get("gte") is None or val >= r["gte"],
            r.get("lt") is None or val < r["lt"],
            r.get("lte") is None or val <= r["lte"],
        ])
    if "is_null" in cond:
        return payload.get(cond["is_null"].get("key")) is None
    if "has_id" in cond:
        return payload.get("_id") in cond["has_id"]
    if any(k in cond for k in ("must", "should", "must_not")):      # 嵌套
        return matches_filter(cond, payload)
    return True


def matches_filter(flt: Optional[dict], payload: Payload) -> bool:
    """Qdrant filter 子集：must（全部满足）、should（至少一个）、must_not（全部不满足）。"""
    if not flt:
        return True
    must = flt.get("must") or []
    should = flt.get("should") or []
    must_not = flt.get("must_not") or []
    if isinstance(must, dict):
        must = [must]
    if isinstance(should, dict):
        should = [should]
    if isinstance(must_not, dict):
        must_not = [must_not]
    if not all(_match_one(c, payload) for c in must):
        return False
    if should and not any(_match_one(c, payload) for c in should):
        return False
    if any(_match_one(c, payload) for c in must_not):
        return False
    return True


def _split_key(pid: PointId, payload: Payload) -> tuple[str | int, Payload]:
    pl = dict(payload)
    key = pl.pop("_key", pid)
    return key, pl


# ---------- 进程内实现 ----------

class MemoryStore:
    """字典 + 暴力余弦。几百到几千条够用；可选 path 落成 JSON，进程重启后还在。"""

    name = "memory"

    def __init__(self, collection: str = "default", path: Optional[Path] = None):
        self.collection = collection
        self.path = Path(path) if path else None
        self.dim: Optional[int] = None
        self._points: dict[PointId, tuple[Vector, Payload]] = {}
        if self.path and self.path.exists():
            data = json.loads(self.path.read_text(encoding="utf-8"))
            self.dim = data.get("dim")
            for pid, vec, pl in data.get("points", []):
                self._points[pid] = (vec, pl)

    def _save(self) -> None:
        if not self.path:
            return
        self.path.parent.mkdir(parents=True, exist_ok=True)
        tmp = self.path.with_suffix(self.path.suffix + ".tmp")
        tmp.write_text(json.dumps({
            "collection": self.collection, "dim": self.dim,
            "points": [[pid, vec, pl] for pid, (vec, pl) in self._points.items()],
        }, ensure_ascii=False), encoding="utf-8")
        tmp.replace(self.path)

    def ensure_collection(self, dim: int, recreate: bool = False) -> None:
        if recreate:
            self._points.clear()
            self.dim = dim
            self._save()
        elif self.dim is None:
            self.dim = dim
            self._save()
        elif self.dim != dim:
            raise StoreError(f"集合 {self.collection} 已是 {self.dim} 维，调用方要 {dim} 维；"
                             f"换维度要 recreate=True（会清空）")

    def drop_collection(self) -> None:
        self._points.clear()
        self.dim = None
        if self.path and self.path.exists():
            self.path.unlink()

    def upsert(self, keys, vectors, payloads=None) -> None:
        if len(keys) != len(vectors):
            raise StoreError("keys 和 vectors 数量不一致")
        payloads = payloads or [{} for _ in keys]
        if len(payloads) != len(keys):
            raise StoreError("payloads 数量和 keys 不一致")
        if self.dim is None and vectors:
            self.dim = len(vectors[0])
        for k, v, pl in zip(keys, vectors, payloads):
            if self.dim is not None and len(v) != self.dim:
                raise StoreError(f"向量 {len(v)} 维，集合是 {self.dim} 维")
            self._points[point_id(k)] = (normalize(v), {**pl, "_key": k})
        self._save()

    def search(self, vector, k=5, filter=None, min_score=None) -> list[ScoredPoint]:
        q = normalize(vector)
        hits = []
        for pid, (v, pl) in self._points.items():
            if not matches_filter(filter, {**pl, "_id": pid}):
                continue
            s = cosine(q, v)
            if min_score is not None and s < min_score:
                continue
            key, clean = _split_key(pid, pl)
            hits.append(ScoredPoint(key, s, clean))
        hits.sort(key=lambda h: h.score, reverse=True)
        return hits[:k]

    def delete(self, keys) -> None:
        for k in keys:
            self._points.pop(point_id(k), None)
        self._save()

    def delete_where(self, filter) -> None:
        self._points = {pid: (v, pl) for pid, (v, pl) in self._points.items()
                        if not matches_filter(filter, {**pl, "_id": pid})}
        self._save()

    def get(self, keys) -> dict:
        out = {}
        for k in keys:
            hit = self._points.get(point_id(k))
            if hit:
                out[k] = _split_key(point_id(k), hit[1])[1]
        return out

    def count(self, filter=None) -> int:
        if not filter:
            return len(self._points)
        return sum(1 for pid, (_, pl) in self._points.items() if matches_filter(filter, {**pl, "_id": pid}))

    def scroll(self, filter=None, limit=100):
        for pid, (_, pl) in list(self._points.items()):
            if matches_filter(filter, {**pl, "_id": pid}):
                yield _split_key(pid, pl)

    def info(self) -> dict:
        return {"store": self.name, "collection": self.collection, "dim": self.dim,
                "points": len(self._points), "path": str(self.path) if self.path else None}


# ---------- Qdrant ----------

class QdrantStore:
    """Qdrant REST 客户端（urllib）。

    - 集合用 Cosine 距离，向量入库前已归一化，所以分数就是余弦相似度，和 MemoryStore 可比。
    - 所有写操作带 wait=true，返回即可见，方便脚本"写完就查"。
    - api_key 走 ``api-key`` 请求头（Qdrant Cloud / 开了鉴权的自建实例）。
    """

    name = "qdrant"

    def __init__(self, url: str = "http://127.0.0.1:6333", collection: str = "default",
                 api_key: Optional[str] = None, timeout: float = 30.0):
        self.url = url.rstrip("/")
        self.collection = collection
        self.api_key = api_key
        self.timeout = timeout

    # -- HTTP --
    def _req(self, method: str, path: str, body: Optional[dict] = None, ok404: bool = False) -> Any:
        data = json.dumps(body, ensure_ascii=False).encode("utf-8") if body is not None else None
        headers = {"Accept": "application/json"}
        if data is not None:
            headers["Content-Type"] = "application/json"
        if self.api_key:
            headers["api-key"] = self.api_key
        req = urllib.request.Request(self.url + path, data=data, headers=headers, method=method)
        try:
            with urllib.request.urlopen(req, timeout=self.timeout) as resp:
                raw = resp.read().decode("utf-8")
        except urllib.error.HTTPError as e:
            if e.code == 404 and ok404:
                return None
            detail = ""
            try:
                detail = json.loads(e.read().decode("utf-8")).get("status", {}).get("error", "")
            except Exception:
                pass
            raise StoreError(f"Qdrant {method} {path} 返回 {e.code}: {detail or e.reason}") from None
        except urllib.error.URLError as e:
            raise StoreError(f"连不上 Qdrant {self.url}: {e.reason}。本地没起的话见 ai/finetune/qdrant-up.ps1") from None
        if not raw:
            return None
        payload = json.loads(raw)
        if isinstance(payload, dict) and "result" in payload:
            return payload["result"]
        if isinstance(payload, dict) and isinstance(payload.get("status"), dict) and payload["status"].get("error"):
            raise StoreError(f"Qdrant {method} {path}: {payload['status']['error']}")
        return payload

    def _col(self, suffix: str = "") -> str:
        return f"/collections/{urllib.parse.quote(self.collection, safe='')}{suffix}"

    # -- 集合 --
    def server_info(self) -> dict:
        r = self._req("GET", "/")
        return r if isinstance(r, dict) else {}

    def collection_info(self) -> Optional[dict]:
        return self._req("GET", self._col(), ok404=True)

    def current_dim(self) -> Optional[int]:
        info = self.collection_info()
        if not info:
            return None
        vectors = info.get("config", {}).get("params", {}).get("vectors", {})
        if "size" in vectors:
            return int(vectors["size"])
        for v in vectors.values():          # 命名向量的情况取第一个
            if isinstance(v, dict) and "size" in v:
                return int(v["size"])
        return None

    def ensure_collection(self, dim: int, recreate: bool = False) -> None:
        cur = self.current_dim()
        if cur is not None and not recreate:
            if cur != dim:
                raise StoreError(f"Qdrant 集合 {self.collection} 已是 {cur} 维，调用方要 {dim} 维；"
                                 f"换维度要 recreate=True（会清空）")
            return
        if cur is not None:
            self.drop_collection()
        self._req("PUT", self._col(), {"vectors": {"size": dim, "distance": "Cosine"}})

    def drop_collection(self) -> None:
        self._req("DELETE", self._col(), ok404=True)

    # -- 点 --
    def upsert(self, keys, vectors, payloads=None) -> None:
        if len(keys) != len(vectors):
            raise StoreError("keys 和 vectors 数量不一致")
        payloads = payloads or [{} for _ in keys]
        if len(payloads) != len(keys):
            raise StoreError("payloads 数量和 keys 不一致")
        if not keys:
            return
        points = [{"id": point_id(k), "vector": normalize(v), "payload": {**pl, "_key": k}}
                  for k, v, pl in zip(keys, vectors, payloads)]
        for i in range(0, len(points), 256):
            self._req("PUT", self._col("/points?wait=true"), {"points": points[i:i + 256]})

    def search(self, vector, k=5, filter=None, min_score=None) -> list[ScoredPoint]:
        body: dict = {"query": normalize(vector), "limit": k, "with_payload": True}
        if filter:
            body["filter"] = filter
        if min_score is not None:
            body["score_threshold"] = min_score
        r = self._req("POST", self._col("/points/query"), body) or {}
        pts = r.get("points", []) if isinstance(r, dict) else r
        out = []
        for p in pts or []:
            key, pl = _split_key(p.get("id"), p.get("payload") or {})
            out.append(ScoredPoint(key, float(p.get("score", 0.0)), pl))
        return out

    def delete(self, keys) -> None:
        if keys:
            self._req("POST", self._col("/points/delete?wait=true"), {"points": [point_id(k) for k in keys]})

    def delete_where(self, filter) -> None:
        self._req("POST", self._col("/points/delete?wait=true"), {"filter": filter})

    def get(self, keys) -> dict:
        if not keys:
            return {}
        r = self._req("POST", self._col("/points"), {"ids": [point_id(k) for k in keys],
                                                      "with_payload": True, "with_vector": False}) or []
        out = {}
        for p in r:
            key, pl = _split_key(p.get("id"), p.get("payload") or {})
            out[key] = pl
        return out

    def count(self, filter=None) -> int:
        body: dict = {"exact": True}
        if filter:
            body["filter"] = filter
        r = self._req("POST", self._col("/points/count"), body) or {}
        return int(r.get("count", 0))

    def scroll(self, filter=None, limit=100):
        """遍历集合里的点（不带向量），给导出 / 迁移用。"""
        offset = None
        while True:
            body: dict = {"limit": limit, "with_payload": True, "with_vector": False}
            if filter:
                body["filter"] = filter
            if offset is not None:
                body["offset"] = offset
            r = self._req("POST", self._col("/points/scroll"), body) or {}
            for p in r.get("points", []):
                yield _split_key(p.get("id"), p.get("payload") or {})
            offset = r.get("next_page_offset")
            if offset is None:
                break

    def info(self) -> dict:
        srv = self.server_info()
        col = self.collection_info()
        return {
            "store": self.name, "url": self.url, "version": srv.get("version"),
            "collection": self.collection, "exists": col is not None,
            "dim": self.current_dim() if col else None,
            "points": (col or {}).get("points_count"),
            "status": (col or {}).get("status"),
        }


# ---------- 装配 ----------

def build_store(cfg: Optional[dict] = None, collection: Optional[str] = None,
                data_dir: Optional[Path] = None, logger=None) -> VectorStore:
    """按配置装配向量库。

    cfg 形如 {"url": "http://127.0.0.1:6333", "collection": "feedback", "api_key": "..."}；
    环境变量 ANAS_QDRANT_URL / ANAS_QDRANT_API_KEY / ANAS_QDRANT_COLLECTION 优先于 cfg。
    url 为空（且 ANAS_QDRANT_URL 未设）时退回 MemoryStore，data_dir 给了就落盘到 data_dir/<collection>.json。
    """
    cfg = cfg or {}
    url = os.environ.get("ANAS_QDRANT_URL") or cfg.get("url")
    col = collection or os.environ.get("ANAS_QDRANT_COLLECTION") or cfg.get("collection", "default")
    if url:
        store: VectorStore = QdrantStore(url, col, api_key=os.environ.get("ANAS_QDRANT_API_KEY") or cfg.get("api_key"),
                                         timeout=cfg.get("timeout_seconds", 30))
        if logger:
            logger(f"[store] 使用 Qdrant {url} 集合 {col}")
    else:
        path = Path(data_dir) / f"{col}.json" if data_dir is not None else None
        store = MemoryStore(col, path)
        if logger:
            logger("[store] 未配置 Qdrant（url / ANAS_QDRANT_URL），用进程内向量库" + (f"，落盘 {path}" if path else ""))
    return store
