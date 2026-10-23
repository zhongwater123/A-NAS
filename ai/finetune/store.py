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
只接受一个白名单子集（must / should / must_not + match value / any + range），两个实现都先校验，
认不出的写法直接报 ``UnsupportedFilter``，不会当成满足。``where(**kv)`` 是构造等值过滤的快捷方式。

集合可以带一份元数据（``ensure_collection(..., meta=...)``）：MemoryStore 存在日志头里，Qdrant 存在集合
metadata 里。``TextIndex`` 用它记录向量空间身份、任务前缀和作用域设置，重新打开时不一致就拒绝。
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

    def ensure_collection(self, dim: int, recreate: bool = False, meta: Optional[dict] = None) -> None: ...
    def meta(self) -> Optional[dict]: ...
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


# ---------- 过滤器 ----------
#
# 支持的子集是白名单，两个实现都先过 validate_filter：认不出的写法一律报 UnsupportedFilter，
# 绝不当成"满足"处理——否则 search 会多读、delete_where 会多删（A-NAS #152 S3 复现过：未实现的 nested 条件
# 被当成 True，条件删除把全部点删光）。QdrantStore 发请求前也走同一个校验，保证两个后端接受的语法一样。
#
#   Filter    = {"must" | "should" | "must_not": Condition 或 [Condition, ...]}（至少一个键，不能有别的键）
#   Condition = Filter（嵌套的布尔组合）
#             | {"key": 字段, "match": {"value": 字符串/整数/布尔}}
#             | {"key": 字段, "match": {"any": [字符串/整数, ...]}}（非空，类型一致）
#             | {"key": 字段, "range": {"gt"/"gte"/"lt"/"lte": 数字, ...}}（至少一个界）
#
# 语义按 Qdrant 文档：字段缺失时 match / range 不满足；字段是数组时任一元素满足即满足；布尔和整数不互相等于；
# should 至少满足一个；must_not 全部不满足。真实 Qdrant 的独立对照见 tests/test_store.py 的 RealQdrantFilterParity。

_FILTER_KEYS = ("must", "should", "must_not")
_RANGE_KEYS = ("gt", "gte", "lt", "lte")


class UnsupportedFilter(StoreError):
    """过滤条件超出支持的子集。宁可拒绝，也不放宽。"""


def _is_scalar_match(v: Any) -> bool:
    return isinstance(v, (str, bool)) or (isinstance(v, int) and not isinstance(v, bool))


def _validate_condition(cond: Any, path: str) -> None:
    if not isinstance(cond, dict) or not cond:
        raise UnsupportedFilter(f"{path}：条件必须是非空对象，收到 {cond!r}")
    if any(k in cond for k in _FILTER_KEYS):
        validate_filter(cond, path)
        return
    extra = set(cond) - {"key", "match", "range"}
    if extra or "key" not in cond or not isinstance(cond["key"], str) or not cond["key"]:
        raise UnsupportedFilter(f"{path}：不支持的条件 {sorted(cond)}（支持 must/should/must_not、key+match、key+range）")
    has_match, has_range = "match" in cond, "range" in cond
    if has_match == has_range:
        raise UnsupportedFilter(f"{path}：一个条件里 match 和 range 只能有一个")
    if has_match:
        m = cond["match"]
        if not isinstance(m, dict) or len(m) != 1:
            raise UnsupportedFilter(f"{path}.match：只支持 value 或 any 其中一个，收到 {m!r}")
        (op, v), = m.items()
        if op == "value":
            if not _is_scalar_match(v):
                raise UnsupportedFilter(f"{path}.match.value 只支持字符串、整数、布尔，收到 {v!r}")
        elif op == "any":
            if not isinstance(v, list) or not v or not all(_is_scalar_match(x) and not isinstance(x, bool) for x in v) \
                    or len({type(x) for x in v}) != 1:
                raise UnsupportedFilter(f"{path}.match.any 要非空、同类型的字符串或整数列表，收到 {v!r}")
        else:
            raise UnsupportedFilter(f"{path}.match：不支持 {op!r}（只支持 value、any）")
    else:
        r = cond["range"]
        if not isinstance(r, dict) or not r or set(r) - set(_RANGE_KEYS):
            raise UnsupportedFilter(f"{path}.range：只支持 gt/gte/lt/lte，收到 {r!r}")
        for k, v in r.items():
            if isinstance(v, bool) or not isinstance(v, (int, float)):
                raise UnsupportedFilter(f"{path}.range.{k} 必须是数字，收到 {v!r}")


def validate_filter(flt: Any, path: str = "filter") -> None:
    """不在支持子集里就抛 UnsupportedFilter。None 表示不过滤，合法。"""
    if flt is None:
        return
    if not isinstance(flt, dict) or not flt:
        raise UnsupportedFilter(f"{path}：过滤条件必须是非空对象，收到 {flt!r}")
    extra = set(flt) - set(_FILTER_KEYS)
    if extra:
        raise UnsupportedFilter(f"{path}：不支持的键 {sorted(extra)}（只支持 must/should/must_not）")
    for k in _FILTER_KEYS:
        if k not in flt:
            continue
        conds = flt[k] if isinstance(flt[k], list) else [flt[k]]
        if isinstance(flt[k], list) and not flt[k]:
            raise UnsupportedFilter(f"{path}.{k}：不能是空列表")
        for i, c in enumerate(conds):
            _validate_condition(c, f"{path}.{k}[{i}]")


def _eq(a: Any, b: Any) -> bool:
    if isinstance(a, bool) or isinstance(b, bool):
        return type(a) is type(b) and a == b
    return a == b


def _values(payload: Payload, key: str) -> Optional[list]:
    if key not in payload or payload[key] is None:
        return None
    v = payload[key]
    return v if isinstance(v, list) else [v]


def _cond_true(cond: dict, payload: Payload) -> bool:
    if any(k in cond for k in _FILTER_KEYS):
        return _filter_true(cond, payload)
    vals = _values(payload, cond["key"])
    if vals is None:
        return False
    if "match" in cond:
        (op, target), = cond["match"].items()
        if op == "value":
            return any(_eq(x, target) for x in vals)
        return any(_eq(x, t) for x in vals for t in target)
    r = cond["range"]
    for x in vals:
        if isinstance(x, bool) or not isinstance(x, (int, float)):
            continue
        if all([r.get("gt") is None or x > r["gt"], r.get("gte") is None or x >= r["gte"],
                r.get("lt") is None or x < r["lt"], r.get("lte") is None or x <= r["lte"]]):
            return True
    return False


def _as_list(v: Any) -> list:
    return v if isinstance(v, list) else [v]


def _filter_true(flt: dict, payload: Payload) -> bool:
    if "must" in flt and not all(_cond_true(c, payload) for c in _as_list(flt["must"])):
        return False
    if "should" in flt and not any(_cond_true(c, payload) for c in _as_list(flt["should"])):
        return False
    if "must_not" in flt and any(_cond_true(c, payload) for c in _as_list(flt["must_not"])):
        return False
    return True


def matches_filter(flt: Optional[dict], payload: Payload) -> bool:
    """先校验再求值；不支持的写法抛 UnsupportedFilter。"""
    validate_filter(flt)
    return True if flt is None else _filter_true(flt, payload)


def and_filters(*filters: Optional[dict]) -> Optional[dict]:
    """把几个过滤条件用 must 串起来（只会收窄，不会放宽）；全是 None 时返回 None。"""
    parts = [f for f in filters if f]
    if not parts:
        return None
    for f in parts:
        validate_filter(f)
    return parts[0] if len(parts) == 1 else {"must": parts}


def _split_key(pid: PointId, payload: Payload) -> tuple[str | int, Payload]:
    pl = dict(payload)
    key = pl.pop("_key", pid)
    return key, pl


# ---------- 进程内实现 ----------

def _check_delete_filter(flt: Optional[dict]) -> None:
    """条件删除必须带非空、受支持的过滤；删全部请显式用 drop_collection。"""
    if not flt:
        raise StoreError("delete_where 需要非空的过滤条件；要清空整个集合请用 drop_collection")
    validate_filter(flt)


def check_meta(collection: str, stored: Optional[dict], wanted: Optional[dict], has_points: bool) -> None:
    """集合已存在时，调用方声明的元数据（向量空间身份等）必须与集合里记录的完全一致。

    - 调用方不声明（wanted=None）：不校验，兼容只用存储层的调用；TextIndex 总会声明。
    - 集合没记录（旧数据）但有点：拒绝，没法确认这些向量来自哪个空间，只能重建。
    - 都有但不同：拒绝，并指出哪些字段不同。
    """
    if wanted is None:
        return
    if stored is None:
        if has_points:
            raise StoreError(f"集合 {collection} 没有记录向量空间身份，无法确认已有向量与当前 embedder 兼容；"
                             f"用 recreate=True 重建（会清空）")
        return
    if stored != wanted:
        diff = sorted(k for k in set(stored) | set(wanted) if stored.get(k) != wanted.get(k))
        raise StoreError(f"集合 {collection} 的元数据与调用方不一致（{', '.join(diff)}）："
                         f"集合 {json.dumps({k: stored.get(k) for k in diff}, ensure_ascii=False)}，"
                         f"调用方 {json.dumps({k: wanted.get(k) for k in diff}, ensure_ascii=False)}；"
                         f"换了模型、前缀、任务或作用域设置就要 recreate=True 重建")


class MemoryStore:
    """字典 + 暴力余弦。几百到几千条够用；可选 path 落成 JSON，进程重启后还在。"""

    name = "memory"

    def __init__(self, collection: str = "default", path: Optional[Path] = None):
        self.collection = collection
        self.path = Path(path) if path else None
        self.dim: Optional[int] = None
        self._points: dict[PointId, tuple[Vector, Payload]] = {}
        self._meta: Optional[dict] = None               # 集合元数据（向量空间身份、任务、作用域），见 TextIndex
        if self.path and self.path.exists():
            data = json.loads(self.path.read_text(encoding="utf-8"))
            self.dim = data.get("dim")
            self._meta = data.get("meta")
            for pid, vec, pl in data.get("points", []):
                self._points[pid] = (vec, pl)

    def _save(self) -> None:
        if not self.path:
            return
        self.path.parent.mkdir(parents=True, exist_ok=True)
        tmp = self.path.with_suffix(self.path.suffix + ".tmp")
        tmp.write_text(json.dumps({
            "collection": self.collection, "dim": self.dim, "meta": self._meta,
            "points": [[pid, vec, pl] for pid, (vec, pl) in self._points.items()],
        }, ensure_ascii=False), encoding="utf-8")
        tmp.replace(self.path)

    def ensure_collection(self, dim: int, recreate: bool = False, meta: Optional[dict] = None) -> None:
        if recreate or self.dim is None:
            if recreate:
                self._points.clear()
            self.dim, self._meta = dim, meta
            self._save()
            return
        if self.dim != dim:
            raise StoreError(f"集合 {self.collection} 已是 {self.dim} 维，调用方要 {dim} 维；"
                             f"换维度要 recreate=True（会清空）")
        check_meta(self.collection, self._meta, meta, has_points=bool(self._points))

    def meta(self) -> Optional[dict]:
        return self._meta

    def drop_collection(self) -> None:
        self._points.clear()
        self.dim = None
        self._meta = None
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
        validate_filter(filter)
        q = normalize(vector)
        hits = []
        for pid, (v, pl) in self._points.items():
            if filter is not None and not _filter_true(filter, pl):
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
        _check_delete_filter(filter)
        self._points = {pid: (v, pl) for pid, (v, pl) in self._points.items() if not _filter_true(filter, pl)}
        self._save()

    def get(self, keys) -> dict:
        out = {}
        for k in keys:
            hit = self._points.get(point_id(k))
            if hit:
                out[k] = _split_key(point_id(k), hit[1])[1]
        return out

    def count(self, filter=None) -> int:
        validate_filter(filter)
        if filter is None:
            return len(self._points)
        return sum(1 for _, pl in self._points.values() if _filter_true(filter, pl))

    def scroll(self, filter=None, limit=100):
        validate_filter(filter)
        for pid, (_, pl) in list(self._points.items()):
            if filter is None or _filter_true(filter, pl):
                yield _split_key(pid, pl)

    def info(self) -> dict:
        return {"store": self.name, "collection": self.collection, "dim": self.dim,
                "points": len(self._points), "path": str(self.path) if self.path else None, "meta": self._meta}


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

    META_KEY = "anas_index"          # 集合级 metadata 里放向量空间身份等（Qdrant ≥ 1.16 支持集合 metadata）

    def meta(self) -> Optional[dict]:
        info = self.collection_info()
        if not info:
            return None
        return ((info.get("config") or {}).get("metadata") or {}).get(self.META_KEY)

    def ensure_collection(self, dim: int, recreate: bool = False, meta: Optional[dict] = None) -> None:
        info = self.collection_info()
        cur = self.current_dim() if info else None
        if cur is not None and not recreate:
            if cur != dim:
                raise StoreError(f"Qdrant 集合 {self.collection} 已是 {cur} 维，调用方要 {dim} 维；"
                                 f"换维度要 recreate=True（会清空）")
            stored = ((info.get("config") or {}).get("metadata") or {}).get(self.META_KEY)
            check_meta(self.collection, stored, meta, has_points=bool(info.get("points_count")))
            return
        if cur is not None:
            self.drop_collection()
        body: dict = {"vectors": {"size": dim, "distance": "Cosine"}}
        if meta is not None:
            body["metadata"] = {self.META_KEY: meta}
        self._req("PUT", self._col(), body)



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
        validate_filter(filter)          # 与 MemoryStore 同一白名单：真实 Qdrant 认得的写法也不放行，两端语义才一致
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
        _check_delete_filter(filter)
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
        validate_filter(filter)
        body: dict = {"exact": True}
        if filter:
            body["filter"] = filter
        r = self._req("POST", self._col("/points/count"), body) or {}
        return int(r.get("count", 0))

    def scroll(self, filter=None, limit=100):
        """遍历集合里的点（不带向量），给导出 / 迁移用。"""
        validate_filter(filter)
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
            "meta": (((col or {}).get("config") or {}).get("metadata") or {}).get(self.META_KEY),
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
