"""三个通用的相似度用法，只依赖 Embedder 接口：

- rank()                  一段文本在一组候选里找最像的 Top-K（检索 / 去重前的候选筛选）
- find_duplicates()       一组文本里两两过阈值的归成一组（近似重复）
- classify_by_examples()  "按样例分类"：每个标签给几句样例，看新文本离哪个标签的样例最近

真模型和哈希退路的余弦量纲不同，阈值要分别给（见 embedders.threshold_for）。
"""

from __future__ import annotations

from dataclasses import dataclass
from typing import Optional

from .embedders import Embedder, cosine


@dataclass
class Hit:
    index: int
    score: float
    text: str

    def as_dict(self) -> dict:
        return {"index": self.index, "score": round(self.score, 3), "text": self.text[:200]}


def rank(query: str, candidates: list[str], embedder: Embedder, k: int = 5,
         kind: str = "sts", query_kind: Optional[str] = None) -> list[Hit]:
    """对称比对（句子相似）用默认的 sts；"问句找段落"时传 query_kind="query", kind="document"。"""
    if not candidates:
        return []
    q = embedder.embed([query], query_kind or kind)[0]
    docs = embedder.embed(candidates, kind)
    hits = [Hit(i, cosine(q, d), t) for i, (t, d) in enumerate(zip(candidates, docs))]
    hits.sort(key=lambda h: h.score, reverse=True)
    return hits[:k]


def find_duplicates(texts: list[str], embedder: Embedder, threshold: float) -> list[list[int]]:
    """返回下标分组，每组内任意两条和组内某条的相似度 ≥ threshold（单链接聚类）。只返回 ≥2 条的组。"""
    if not texts:
        return []
    vecs = embedder.embed(texts, "sts")
    parent = list(range(len(texts)))

    def find(x: int) -> int:
        while parent[x] != x:
            parent[x] = parent[parent[x]]
            x = parent[x]
        return x

    for i in range(len(texts)):
        for j in range(i + 1, len(texts)):
            if cosine(vecs[i], vecs[j]) >= threshold:
                parent[find(i)] = find(j)
    groups: dict[int, list[int]] = {}
    for i in range(len(texts)):
        groups.setdefault(find(i), []).append(i)
    return sorted((g for g in groups.values() if len(g) >= 2), key=len, reverse=True)


@dataclass
class Example:
    label: str
    text: str


class classify_by_examples:  # noqa: N801  用法像函数，但缓存样例向量，所以做成可调用对象
    """按样例分类。

    用法：
        clf = classify_by_examples(examples, embedder, threshold=0.6)
        label, score, nearest = clf("要分类的文本")   # 低于阈值返回 (None, score, nearest)
    """

    def __init__(self, examples: list[Example], embedder: Embedder, threshold: float):
        self.examples = examples
        self.embedder = embedder
        self.threshold = threshold
        self._vecs: Optional[list[list[float]]] = None

    def _vectors(self) -> list[list[float]]:
        if self._vecs is None:
            self._vecs = self.embedder.embed([e.text for e in self.examples], "sts")
        return self._vecs

    def __call__(self, text: str) -> tuple[Optional[str], float, Optional[Example]]:
        if not self.examples:
            return None, 0.0, None
        v = self.embedder.embed([text], "sts")[0]
        best_i, best_s = -1, -2.0
        for i, ev in enumerate(self._vectors()):
            s = cosine(v, ev)
            if s > best_s:
                best_i, best_s = i, s
        ex = self.examples[best_i]
        return (ex.label if best_s >= self.threshold else None), best_s, ex
