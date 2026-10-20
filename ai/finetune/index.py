"""TextIndex：embedder + 向量库 = 一个"存文本、按意思找"的索引（开发机用）。

    emb = build_embedder({"dim": 256})
    store = build_store({"url": "http://127.0.0.1:6333"}, collection="feedback")
    idx = TextIndex(store, emb)                       # 自动建集合，维度跟 embedder 走
    idx.add(["上传 50MB 文件后校验不一致", "相册回收站恢复无反应"],
            keys=["FB-101", "FB-102"], payloads=[{"source": "feedback"}] * 2)
    idx.search("大文件上传后校验失败", k=3)              # -> [IndexHit(key="FB-101", score=..., text=..., payload=...)]
    idx.search("...", where=where(source="feedback"))

文本本身存在 payload 的 ``text`` 字段，所以结果里直接带原文，不用再回表。
入库用 ``document`` 前缀、查询用 ``query`` 前缀（EmbeddingGemma 的检索任务）；
做"句子相似"（去重）时两边都传 kind="sts"。
"""

from __future__ import annotations

from dataclasses import dataclass, field
from typing import Any, Optional

from .embedders import Embedder
from .store import Payload, ScoredPoint, VectorStore, where  # noqa: F401  （where 方便调用方一起 import）


@dataclass
class IndexHit:
    key: str | int
    score: float
    text: str
    payload: Payload = field(default_factory=dict)

    def as_dict(self) -> dict:
        return {"key": self.key, "score": round(self.score, 4), "text": self.text[:200], "payload": self.payload}


class TextIndex:
    def __init__(self, store: VectorStore, embedder: Embedder, doc_kind: str = "document",
                 query_kind: str = "query", recreate: bool = False):
        self.store = store
        self.embedder = embedder
        self.doc_kind = doc_kind
        self.query_kind = query_kind
        store.ensure_collection(embedder.dim, recreate=recreate)

    @property
    def semantic(self) -> bool:
        return self.embedder.semantic

    def add(self, texts: list[str], keys: Optional[list[str | int]] = None,
            payloads: Optional[list[Payload]] = None, kind: Optional[str] = None,
            batch_size: int = 64) -> list[str | int]:
        """存一批文本。不给 keys 就用文本本身当 key（同文本重复 add 只占一条）。"""
        if not texts:
            return []
        keys = list(keys) if keys is not None else list(texts)
        payloads = list(payloads) if payloads is not None else [{} for _ in texts]
        if not (len(keys) == len(texts) == len(payloads)):
            raise ValueError("texts / keys / payloads 数量不一致")
        for i in range(0, len(texts), batch_size):
            chunk = slice(i, i + batch_size)
            vecs = self.embedder.embed(texts[chunk], kind or self.doc_kind)
            self.store.upsert(keys[chunk], vecs,
                              [{**pl, "text": t} for pl, t in zip(payloads[chunk], texts[chunk])])
        return keys

    def search(self, query: str, k: int = 5, where: Optional[dict] = None,
               min_score: Optional[float] = None, kind: Optional[str] = None) -> list[IndexHit]:
        q = self.embedder.embed([query], kind or self.query_kind)[0]
        return [self._hit(p) for p in self.store.search(q, k, where, min_score)]

    def similar(self, text: str, k: int = 5, where: Optional[dict] = None,
                min_score: Optional[float] = None) -> list[IndexHit]:
        """对称比对（去重 / 找相近反馈）：两边都用 sts 前缀，并把自己（同文本）去掉。"""
        q = self.embedder.embed([text], "sts")[0]
        hits = [self._hit(p) for p in self.store.search(q, k + 1, where, min_score)]
        return [h for h in hits if h.text != text][:k]

    def remove(self, keys: list[str | int]) -> None:
        self.store.delete(keys)

    def remove_where(self, where: dict) -> None:
        self.store.delete_where(where)

    def get(self, keys: list[str | int]) -> dict[str | int, Payload]:
        return self.store.get(keys)

    def count(self, where: Optional[dict] = None) -> int:
        return self.store.count(where)

    def __len__(self) -> int:
        return self.store.count()

    def info(self) -> dict[str, Any]:
        return {**self.store.info(), "embedder": self.embedder.name, "semantic": self.embedder.semantic}

    @staticmethod
    def _hit(p: ScoredPoint) -> IndexHit:
        pl = dict(p.payload)
        text = pl.pop("text", "")
        return IndexHit(p.key, p.score, text, pl)
