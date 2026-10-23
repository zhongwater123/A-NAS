"""TextIndex：embedder + 向量库 = 一个"存文本、按意思找"的索引。

    emb = build_embedder({"dim": 256})
    store = build_store({"url": "http://127.0.0.1:6333"}, collection="feedback")
    idx = TextIndex(store, emb)                              # 默认 task="retrieval"：入库 document 前缀，查询 query 前缀
    idx.add(["上传 50MB 文件后校验不一致"], keys=["FB-101"], payloads=[{"source": "feedback"}])
    idx.search("大文件上传后校验失败", k=3, where=where(source="feedback"))

    dedup = TextIndex(store2, emb, task="sts")               # 对称比对（去重）要单独一个 sts 集合
    dedup.similar("上传 50MB 文件后校验不一致")

    alice = TextIndex(store3, emb, scope="alice")            # 按作用域分区：key 带作用域，读写删都只在本作用域内

集合会记录一份元数据，重新打开时必须完全一致，否则拒绝（A-NAS #152 的 R1 / R2 / S1）：

- **向量空间身份**（``client.space_of``）：实现、服务地址、模型名、服务端报告的权重指纹与运行时、前缀版本、维度、
  归一化方式。只有维度相同不算兼容：哈希退路换真模型、基座换微调权重、关掉前缀，都要重建集合。
- **任务**：``retrieval``（document 入库 / query 查询）或 ``sts``（两边都用 sts）。``similar()`` 只在 sts 集合上可用，
  ``add(kind=...)`` 也不能写入与集合任务不同的前缀。
- **是否分作用域**：分作用域的集合每个操作都必须带 ``scope``，点的 key 是"作用域 + 调用方 key"，不同作用域
  写同一段文本互不覆盖；搜索、按 key 读取、删除、条件删除、计数、导出都自动只在本作用域内进行。

作用域只解决这段研究代码自身的正确性：谁是 Alice 由调用方说了算。成员身份要由可信的服务端绑定、
调用方不能自选作用域，这是产品接入前要解决的问题，不在这里；首版不对成员开放共享索引。
"""

from __future__ import annotations

from dataclasses import dataclass, field
from typing import Any, Optional

from .embedders import Embedder, space_of
from .store import Payload, ScoredPoint, StoreError, VectorStore, and_filters, validate_filter, where  # noqa: F401

TASKS = {"retrieval": ("document", "query"), "sts": ("sts", "sts")}
META_VERSION = 1
SEP = "\x1f"                                   # 作用域与 key 之间的分隔符，作用域里不许出现
RESERVED = ("_key", "_scope", "_ukey", "text")  # 调用方 payload 不能用的字段


@dataclass
class IndexHit:
    key: str | int
    score: float
    text: str
    payload: Payload = field(default_factory=dict)

    def as_dict(self) -> dict:
        return {"key": self.key, "score": round(self.score, 4), "text": self.text[:200], "payload": self.payload}


def _uses_reserved(flt: Any) -> bool:
    if isinstance(flt, dict):
        if flt.get("key") in RESERVED:
            return True
        return any(_uses_reserved(v) for v in flt.values())
    if isinstance(flt, list):
        return any(_uses_reserved(v) for v in flt)
    return False


class TextIndex:
    def __init__(self, store: VectorStore, embedder: Embedder, task: str = "retrieval",
                 scope: Optional[str] = None, scoped: Optional[bool] = None, recreate: bool = False):
        if task not in TASKS:
            raise ValueError(f"task 只能是 {sorted(TASKS)}，收到 {task!r}")
        self.scoped = (scope is not None) if scoped is None else bool(scoped)
        if self.scoped:
            if not isinstance(scope, str) or not scope or SEP in scope:
                raise StoreError("分作用域的集合必须给一个非空的 scope 字符串")
        elif scope is not None:
            raise StoreError("scoped=False 的集合不接受 scope")
        self.store = store
        self.embedder = embedder
        self.task = task
        self.doc_kind, self.query_kind = TASKS[task]
        self.scope = scope
        self.meta = {"version": META_VERSION, "space": space_of(embedder), "task": task, "scoped": self.scoped}
        store.ensure_collection(embedder.dim, recreate=recreate, meta=self.meta)

    # ---- 内部 ----
    def _check(self) -> None:
        """每次读写前都核对集合元数据：别的进程可能已经用另一个模型重建了这个集合。"""
        stored = self.store.meta()
        if stored != self.meta:
            raise StoreError(f"集合 {self.store.collection} 的元数据已变化（现在是 {stored}），与本索引不一致，拒绝读写")

    def _pkey(self, key: str | int) -> str | int:
        if isinstance(key, bool) or not isinstance(key, (str, int)):
            raise StoreError(f"key 只能是字符串或整数，收到 {key!r}")
        return f"{self.scope}{SEP}{key}" if self.scoped else key

    def _where(self, user: Optional[dict]) -> Optional[dict]:
        validate_filter(user)
        if user is not None and _uses_reserved(user):
            raise StoreError(f"过滤条件不能引用保留字段 {RESERVED}")
        scope_f = where(_scope=self.scope) if self.scoped else None
        return and_filters(scope_f, user)

    @property
    def semantic(self) -> bool:
        return self.embedder.semantic

    # ---- 写 ----
    def add(self, texts: list[str], keys: Optional[list[str | int]] = None,
            payloads: Optional[list[Payload]] = None, kind: Optional[str] = None,
            batch_size: int = 64) -> list[str | int]:
        """存一批文本。不给 keys 就用文本本身当 key（同一作用域里同文本只占一条；不同作用域互不影响）。"""
        if kind is not None and kind != self.doc_kind:
            raise StoreError(f"集合任务是 {self.task}，入库前缀是 {self.doc_kind}，不接受 kind={kind!r}；"
                             f"不同前缀的向量不能混在一个集合里")
        if not texts:
            return []
        keys = list(keys) if keys is not None else list(texts)
        payloads = list(payloads) if payloads is not None else [{} for _ in texts]
        if not (len(keys) == len(texts) == len(payloads)):
            raise ValueError("texts / keys / payloads 数量不一致")
        for pl in payloads:
            bad = [k for k in RESERVED if k in pl]
            if bad:
                raise StoreError(f"payload 不能带保留字段 {bad}")
        self._check()
        for i in range(0, len(texts), batch_size):
            chunk = slice(i, i + batch_size)
            vecs = self.embedder.embed(texts[chunk], self.doc_kind)
            pls = []
            for pl, t, k in zip(payloads[chunk], texts[chunk], keys[chunk]):
                extra = {"_scope": self.scope, "_ukey": k} if self.scoped else {}
                pls.append({**pl, "text": t, **extra})
            self.store.upsert([self._pkey(k) for k in keys[chunk]], vecs, pls)
        return keys

    # ---- 读 ----
    def search(self, query: str, k: int = 5, where: Optional[dict] = None,
               min_score: Optional[float] = None) -> list[IndexHit]:
        flt = self._where(where)
        self._check()
        q = self.embedder.embed([query], self.query_kind)[0]
        return [self._hit(p) for p in self.store.search(q, k, flt, min_score)]

    def similar(self, text: str, k: int = 5, where: Optional[dict] = None,
                min_score: Optional[float] = None) -> list[IndexHit]:
        """对称比对（去重 / 找相近反馈），只在 task="sts" 的集合上可用；结果里去掉与输入相同的文本。"""
        if self.task != "sts":
            raise StoreError(f"集合任务是 {self.task}（入库用 {self.doc_kind} 前缀），similar 需要两边都是 sts；"
                             f"去重请用 task=\"sts\" 的单独集合")
        flt = self._where(where)
        self._check()
        q = self.embedder.embed([text], "sts")[0]
        hits = [self._hit(p) for p in self.store.search(q, k + 1, flt, min_score)]
        return [h for h in hits if h.text != text][:k]

    def get(self, keys: list[str | int]) -> dict[str | int, Payload]:
        self._check()
        got = self.store.get([self._pkey(k) for k in keys])
        out = {}
        for k in keys:
            pl = got.get(self._pkey(k))
            if pl is None or (self.scoped and pl.get("_scope") != self.scope):
                continue
            out[k] = self._clean(pl)
        return out

    def count(self, where: Optional[dict] = None) -> int:
        flt = self._where(where)
        self._check()
        return self.store.count(flt)

    def __len__(self) -> int:
        return self.count()

    def items(self, where: Optional[dict] = None):
        """遍历本作用域内的条目：(key, text, payload)。"""
        flt = self._where(where)
        self._check()
        for store_key, pl in self.store.scroll(flt):
            key = pl.get("_ukey") if self.scoped else store_key
            clean = self._clean(pl)
            text = clean.pop("text", "")
            yield key, text, clean

    # ---- 删 ----
    def remove(self, keys: list[str | int]) -> None:
        self._check()
        if self.scoped:      # 只删确实属于本作用域的点
            mine = self.get(keys)
            keys = [k for k in keys if k in mine]
        self.store.delete([self._pkey(k) for k in keys])

    def remove_where(self, where: dict) -> None:
        if not where:
            raise StoreError("remove_where 需要非空的过滤条件")
        flt = self._where(where)
        self._check()
        self.store.delete_where(flt)

    def info(self) -> dict[str, Any]:
        return {**self.store.info(), "embedder": self.embedder.name, "semantic": self.embedder.semantic,
                "task": self.task, "scope": self.scope}

    @staticmethod
    def _clean(pl: Payload) -> Payload:
        return {k: v for k, v in pl.items() if k not in ("_scope", "_ukey")}

    def _hit(self, p: ScoredPoint) -> IndexHit:
        pl = dict(p.payload)
        key = pl.get("_ukey", p.key) if self.scoped else p.key
        pl = self._clean(pl)
        text = pl.pop("text", "")
        return IndexHit(key, p.score, text, pl)
