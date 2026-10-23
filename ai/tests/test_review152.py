"""Regression tests for the four issues reproduced in the review of #152 (S3 filters, R1 vector-space identity,
R2 similar() prefixes, S1 scope overwrite). No model and no network: a deterministic stand-in embedder and the
in-process fake Qdrant (tests/fake_qdrant.py, with its own filter evaluator). With ANAS_QDRANT_TEST_URL set and
reachable, RealQdrantFilterParity also compares every filter against a real Qdrant.
"""

import os
import random
import tempfile
import threading
import unittest
import urllib.request
from pathlib import Path

from finetune import server
from finetune import store as store_mod
from finetune.embedders import CachedEmbedder, HashEmbedder, HttpEmbedder, space_fingerprint, space_of
from finetune.index import TextIndex
from finetune.store import MemoryStore, QdrantStore, StoreError, UnsupportedFilter, validate_filter, where
from tests import fake_qdrant

HERE = Path(__file__).resolve().parent

NESTED = {"must": [{"nested": {"key": "access",
                               "filter": {"must": [{"key": "owner", "match": {"value": "alice"}}]}}}]}


class RecEmb:
    """确定性替身：记录每次调用的 kind，向量由文本哈希得到。"""

    semantic = False

    def __init__(self, name: str = "rec-a", dim: int = 8):
        self.name, self.dim = name, dim
        self.kinds: list[str] = []
        self._h = HashEmbedder(dim)

    def space(self) -> dict:
        return {"impl": "rec", "name": self.name, "dim": self.dim}

    def embed(self, texts, kind="sts"):
        self.kinds.append(kind)
        return self._h.embed(texts, kind)


def unit(i: int, dim: int = 4) -> list[float]:
    v = [0.0] * dim
    v[i % dim] = 1.0
    return v


class FakeQdrantMixin:
    @classmethod
    def setUpClass(cls):
        cls.srv, cls.fake = fake_qdrant.serve("127.0.0.1", 0)
        cls.url = f"http://127.0.0.1:{cls.srv.server_address[1]}"

    @classmethod
    def tearDownClass(cls):
        cls.srv.shutdown()
        cls.srv.server_close()

    def stores(self, name: str):
        q = QdrantStore(self.url, name)
        q.drop_collection()
        return [("memory", MemoryStore(name)), ("fake-qdrant", q)]


# ---------- S3：过滤器 ----------

class S3UnknownFilterRejected(FakeQdrantMixin, unittest.TestCase):
    def _fill(self, st):
        st.ensure_collection(4)
        st.upsert(["a", "b"], [unit(0), unit(1)], [{"owner": "alice"}, {"owner": "bob"}])

    def test_unknown_nested_filter_reads_nothing(self):
        for label, st in self.stores("s3-read"):
            with self.subTest(store=label):
                self._fill(st)
                with self.assertRaises(UnsupportedFilter):
                    st.search(unit(0), 10, NESTED)
                with self.assertRaises(UnsupportedFilter):
                    st.count(NESTED)
                with self.assertRaises(UnsupportedFilter):
                    list(st.scroll(NESTED))

    def test_unknown_nested_filter_deletes_nothing(self):
        for label, st in self.stores("s3-del"):
            with self.subTest(store=label):
                self._fill(st)
                with self.assertRaises(UnsupportedFilter):
                    st.delete_where(NESTED)
                self.assertEqual(st.count(), 2)
                with self.assertRaises(StoreError):          # 空过滤不等于"全删"
                    st.delete_where({})
                with self.assertRaises(StoreError):
                    st.delete_where(None)
                self.assertEqual(st.count(), 2)

    def test_validate_filter_rejects_everything_outside_subset(self):
        bad = [{}, {"filter": {}}, {"must": []}, {"must": [{}]}, {"must": [{"key": "a"}]},
               {"must": [{"key": "a", "match": {"value": 1.5}}]}, {"must": [{"key": "a", "match": {"value": None}}]},
               {"must": [{"key": "a", "match": {"any": []}}]}, {"must": [{"key": "a", "match": {"any": ["x", 1]}}]},
               {"must": [{"key": "a", "match": {"value": "x", "any": ["y"]}}]},
               {"must": [{"key": "a", "match": {"text": "x"}}]}, {"must": [{"key": "a", "match": {"except": ["x"]}}]},
               {"must": [{"key": "a", "range": {"gt": "1"}}]}, {"must": [{"key": "a", "range": {"between": 1}}]},
               {"must": [{"key": "a", "match": {"value": "x"}, "range": {"gt": 1}}]},
               {"must": [{"is_empty": {"key": "a"}}]}, {"must": [{"has_id": [1]}]}, {"must": [{"geo_radius": {}}]},
               NESTED, "not a dict", ["must"]]
        for b in bad:
            with self.subTest(filter=b):
                with self.assertRaises(UnsupportedFilter):
                    validate_filter(b)
        for ok in (None, where(a="x"), where(a=["x", "y"]), {"must": [{"key": "n", "range": {"gte": 1, "lt": 2.5}}]},
                   {"should": {"key": "a", "match": {"value": True}}}, {"must_not": [{"must": [{"key": "a", "match": {"value": 3}}]}]}):
            validate_filter(ok)

    def test_fake_qdrant_has_its_own_filter(self):
        # 假服务若和 MemoryStore 共用同一个求值函数，两边一起错也照样通过契约测试
        self.assertIsNot(fake_qdrant.matches_filter, store_mod.matches_filter)
        self.assertNotIn("finetune.store", (HERE / "fake_qdrant.py").read_text(encoding="utf-8").split('"""', 2)[-1])

    def test_fake_qdrant_and_memory_agree_on_tricky_payloads(self):
        rnd = random.Random(7)
        pls = [_random_payload(rnd) for _ in range(120)]
        mem = MemoryStore("par")
        q = QdrantStore(self.url, "par")
        q.drop_collection()
        for st in (mem, q):
            st.ensure_collection(4)
            st.upsert(list(range(len(pls))), [unit(i) for i in range(len(pls))], pls)
        for flt in PARITY_FILTERS:
            with self.subTest(filter=flt):
                self.assertEqual(sorted(k for k, _ in mem.scroll(flt)), sorted(k for k, _ in q.scroll(flt)))


def _random_payload(rnd: random.Random) -> dict:
    pl = {}
    if rnd.random() < 0.8:
        pl["owner"] = rnd.choice(["alice", "bob", "carol"])
    r = rnd.random()
    if r < 0.3:
        pl["n"] = rnd.randint(0, 5)
    elif r < 0.5:
        pl["n"] = rnd.choice([True, False])
    elif r < 0.6:
        pl["n"] = rnd.random() * 5
    elif r < 0.75:
        pl["n"] = [rnd.randint(0, 5) for _ in range(rnd.randint(0, 3))]
    if rnd.random() < 0.5:
        pl["tags"] = rnd.sample(["x", "y", "z"], rnd.randint(0, 2))
    return pl


PARITY_FILTERS = [
    where(owner="alice"), where(owner=["alice", "bob"]), {"must_not": [where(owner="alice")]},
    {"must": [{"key": "n", "match": {"value": 1}}]}, {"must": [{"key": "n", "match": {"value": True}}]},
    {"must": [{"key": "n", "match": {"any": [0, 2, 4]}}]}, {"must": [{"key": "n", "range": {"gte": 2, "lt": 4}}]},
    {"must": [{"key": "n", "range": {"gt": 2.5}}]}, {"must_not": [{"key": "n", "range": {"lte": 1}}]},
    {"must": [{"key": "tags", "match": {"value": "x"}}]}, {"must": [{"key": "tags", "match": {"any": ["y", "z"]}}]},
    {"should": [where(owner="carol"), {"key": "tags", "match": {"value": "z"}}]},
    {"must": [where(owner="bob"), {"should": [{"key": "n", "range": {"gt": 3}}, {"key": "tags", "match": {"value": "x"}}]}]},
    {"must_not": [{"key": "missing", "match": {"value": "x"}}]}, {"must": [{"key": "missing", "range": {"gt": 0}}]},
]


@unittest.skipUnless(os.environ.get("ANAS_QDRANT_TEST_URL"),
                     "set ANAS_QDRANT_TEST_URL to compare against a real Qdrant")
class RealQdrantFilterParity(unittest.TestCase):
    """同一批 payload 与过滤条件，MemoryStore 的结果必须与真实 Qdrant 一模一样。"""

    @classmethod
    def setUpClass(cls):
        cls.url = os.environ["ANAS_QDRANT_TEST_URL"].rstrip("/")
        try:
            urllib.request.urlopen(cls.url + "/", timeout=3).read()
        except Exception as e:  # noqa: BLE001
            raise unittest.SkipTest(f"连不上 {cls.url}: {e}")

    def setUp(self):
        self.q = QdrantStore(self.url, "test-review152-parity")
        self.q.drop_collection()

    def tearDown(self):
        self.q.drop_collection()

    def test_filters_match_real_qdrant(self):
        rnd = random.Random(11)
        pls = [_random_payload(rnd) for _ in range(300)]
        mem = MemoryStore("parity")
        for st in (mem, self.q):
            st.ensure_collection(4)
            st.upsert(list(range(len(pls))), [unit(i) for i in range(len(pls))], pls)
        for flt in PARITY_FILTERS:
            with self.subTest(filter=flt):
                self.assertEqual(sorted(k for k, _ in mem.scroll(flt)), sorted(k for k, _ in self.q.scroll(flt)))
                self.assertEqual(mem.count(flt), self.q.count(flt))

    def test_client_refuses_before_reaching_qdrant(self):
        # 真实 Qdrant 本身认得 nested，但客户端按白名单在发请求前就拒绝，两端接受的语法一致
        self.q.ensure_collection(4)
        self.q.upsert(["a"], [unit(0)], [{"owner": "alice"}])
        with self.assertRaises(UnsupportedFilter):
            self.q.delete_where(NESTED)
        self.assertEqual(self.q.count(), 1)

    def test_metadata_round_trip(self):
        meta = {"version": 1, "space": {"impl": "rec", "dim": 4}, "task": "retrieval", "scoped": False}
        self.q.ensure_collection(4, meta=meta)
        self.assertEqual(self.q.meta(), meta)
        with self.assertRaises(StoreError):
            self.q.ensure_collection(4, meta={**meta, "task": "sts"})


# ---------- R1：向量空间身份 ----------

class R1VectorSpaceIdentity(FakeQdrantMixin, unittest.TestCase):
    def test_same_dim_different_model_rejected(self):
        for label, st in self.stores("r1"):
            with self.subTest(store=label):
                TextIndex(st, RecEmb("model-A")).add(["旧向量"])
                with self.assertRaises(StoreError):
                    TextIndex(st, RecEmb("model-B"))
                self.assertEqual(st.count(), 1)                  # 拒绝时不动已有数据
                TextIndex(st, RecEmb("model-B"), recreate=True)   # 显式重建才行
                self.assertEqual(st.count(), 0)

    def test_hash_fallback_and_real_model_are_different_spaces(self):
        st = MemoryStore("r1-hash")
        TextIndex(st, HashEmbedder(8)).add(["x"])
        with self.assertRaises(StoreError):
            TextIndex(st, RecEmb("real", 8))

    def test_identity_survives_reload(self):
        with tempfile.TemporaryDirectory() as d:
            p = Path(d) / "c.json"
            TextIndex(MemoryStore("c", p), RecEmb("model-A")).add(["x"])
            TextIndex(MemoryStore("c", p), RecEmb("model-A"))      # 同一空间：可以重新打开
            with self.assertRaises(StoreError):
                TextIndex(MemoryStore("c", p), RecEmb("model-B"))

    def test_collection_without_identity_but_with_points_rejected(self):
        st = MemoryStore("legacy")
        st.ensure_collection(8)
        st.upsert(["old"], [[1.0] + [0.0] * 7])
        with self.assertRaises(StoreError):
            TextIndex(st, RecEmb("model-A"))

    def test_open_index_refuses_after_collection_rebuilt_by_other_model(self):
        st = MemoryStore("swap")
        a = TextIndex(st, RecEmb("model-A"))
        a.add(["x"])
        TextIndex(st, RecEmb("model-B"), recreate=True).add(["y"])
        for op in (lambda: a.search("x"), lambda: a.add(["z"]), lambda: a.count()):
            with self.assertRaises(StoreError):
                op()


class R1CacheKeys(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.servers = []
        for dims in (32, 32):
            srv = server.serve("127.0.0.1", 0, server.FakeBackend(dims), fake=True)
            threading.Thread(target=srv.serve_forever, daemon=True).start()
            cls.servers.append(srv)
        cls.urls = [f"http://127.0.0.1:{s.server_address[1]}" for s in cls.servers]

    @classmethod
    def tearDownClass(cls):
        for s in cls.servers:
            s.shutdown()
            s.server_close()

    def test_server_reports_weights_and_runtime(self):
        ident = HttpEmbedder(self.urls[0], "m", dim=None).server_identity()
        self.assertEqual(ident["weights"], "fake-hash@32")
        self.assertEqual(ident["runtime"], "fake")

    def test_same_name_and_dim_but_different_url_or_prefix_get_different_keys(self):
        base = HttpEmbedder(self.urls[0], "m", dim=None)
        other_url = HttpEmbedder(self.urls[1], "m", dim=None)
        no_prefix = HttpEmbedder(self.urls[0], "m", dim=None, add_prefix=False)
        self.assertEqual(base.name, other_url.name)                 # 旧的缓存键只看这个，三者都一样
        self.assertEqual(base.name, no_prefix.name)
        fps = {space_fingerprint(space_of(e)) for e in (base, other_url, no_prefix)}
        self.assertEqual(len(fps), 3)
        keys = {CachedEmbedder(e, None)._key("query", "同一段文本") for e in (base, other_url, no_prefix)}
        self.assertEqual(len(keys), 3)

    def test_different_server_weights_get_different_keys(self):
        a = HttpEmbedder(self.urls[0], "m", dim=None)
        b = HttpEmbedder(self.urls[0], "m", dim=None)
        b._server = {**a.server_identity(), "weights": "dir:finetuned"}  # 服务端换了权重，名字没变
        self.assertNotEqual(CachedEmbedder(a, None)._key("sts", "t"), CachedEmbedder(b, None)._key("sts", "t"))

    def test_hash_embedder_params_change_key(self):
        k1 = CachedEmbedder(HashEmbedder(64), None)._key("sts", "t")
        k2 = CachedEmbedder(HashEmbedder(64, ngrams=(2,)), None)._key("sts", "t")
        self.assertNotEqual(k1, k2)


# ---------- R2：任务前缀 ----------

class R2TaskPrefix(FakeQdrantMixin, unittest.TestCase):
    def test_default_add_then_similar_rejected(self):
        for label, st in self.stores("r2"):
            with self.subTest(store=label):
                emb = RecEmb()
                idx = TextIndex(st, emb)
                idx.add(["a", "b"])
                with self.assertRaises(StoreError):
                    idx.similar("a")
                self.assertEqual(emb.kinds, ["document"])      # 拒绝时没有用 sts 去查 document 向量

    def test_sts_collection_uses_sts_on_both_sides(self):
        emb = RecEmb()
        idx = TextIndex(MemoryStore("r2-sts"), emb, task="sts")
        idx.add(["a", "b"])
        idx.similar("a")
        idx.search("b")
        self.assertEqual(emb.kinds, ["sts", "sts", "sts"])

    def test_mixed_kind_write_rejected(self):
        idx = TextIndex(MemoryStore("r2-mix"), RecEmb())
        with self.assertRaises(StoreError):
            idx.add(["x"], kind="sts")
        idx.add(["x"], kind="document")                         # 与集合任务一致的显式写法可以
        self.assertEqual(idx.count(), 1)

    def test_reopen_with_other_task_rejected(self):
        st = MemoryStore("r2-reopen")
        TextIndex(st, RecEmb()).add(["x"])
        with self.assertRaises(StoreError):
            TextIndex(st, RecEmb(), task="sts")


# ---------- S1：作用域 ----------

class S1Scopes(FakeQdrantMixin, unittest.TestCase):
    def test_same_text_in_two_scopes_does_not_overwrite(self):
        for label, st in self.stores("s1-same"):
            with self.subTest(store=label):
                alice = TextIndex(st, RecEmb(), scope="alice")
                bob = TextIndex(st, RecEmb(), scope="bob")
                alice.add(["同一段文本"], payloads=[{"owner": "alice"}])
                bob.add(["同一段文本"], payloads=[{"owner": "bob"}])
                self.assertEqual(st.count(), 2)
                self.assertEqual(alice.get(["同一段文本"])["同一段文本"]["owner"], "alice")
                self.assertEqual(bob.get(["同一段文本"])["同一段文本"]["owner"], "bob")
                self.assertEqual([h.payload["owner"] for h in alice.search("同一段文本", k=10)], ["alice"])

    def test_same_explicit_key_in_two_scopes(self):
        st = MemoryStore("s1-key")
        TextIndex(st, RecEmb(), scope="alice").add(["a 的笔记"], keys=["note-1"])
        TextIndex(st, RecEmb(), scope="bob").add(["b 的笔记"], keys=["note-1"])
        self.assertEqual(TextIndex(st, RecEmb(), scope="alice").get(["note-1"])["note-1"]["text"], "a 的笔记")

    def test_missing_scope_rejected(self):
        st = MemoryStore("s1-missing")
        TextIndex(st, RecEmb(), scope="alice").add(["x"])
        with self.assertRaises(StoreError):
            TextIndex(st, RecEmb())                             # 漏传 scope：集合是分作用域的，拒绝
        with self.assertRaises(StoreError):
            TextIndex(st, RecEmb(), scoped=True)                # 声明分作用域但不给 scope
        with self.assertRaises(StoreError):
            TextIndex(st, RecEmb(), scope="a\x1fb")             # 作用域里夹分隔符，想拼出别人的 key

    def test_forged_owner_fields_rejected(self):
        alice = TextIndex(MemoryStore("s1-forge"), RecEmb(), scope="alice")
        for pl in ({"_scope": "bob"}, {"_ukey": "x"}, {"_key": "x"}, {"text": "伪造原文"}):
            with self.assertRaises(StoreError):
                alice.add(["x"], payloads=[pl])
        with self.assertRaises(StoreError):
            alice.search("x", where=where(_scope="bob"))       # 过滤里引用保留字段
        alice.add(["x"], payloads=[{"owner": "bob"}])            # 普通字段写成 bob 也只落在 alice 的作用域
        bob = TextIndex(alice.store, RecEmb(), scope="bob")
        self.assertEqual(bob.count(), 0)
        self.assertEqual(bob.search("x", where=where(owner="bob")), [])

    def test_get_remove_and_remove_where_stay_in_scope(self):
        for label, st in self.stores("s1-del"):
            with self.subTest(store=label):
                alice = TextIndex(st, RecEmb(), scope="alice")
                bob = TextIndex(st, RecEmb(), scope="bob")
                bob.add(["b1", "b2"], keys=["k1", "k2"], payloads=[{"tag": "t"}, {"tag": "t"}])
                alice.add(["a1"], keys=["k9"], payloads=[{"tag": "t"}])
                self.assertEqual(alice.get(["k1", "k2"]), {})       # 按 key 读不到别人的
                alice.remove(["k1", "k2"])                          # 按 key 删不到别人的
                alice.remove_where(where(tag="t"))                  # 条件删除只删自己的
                self.assertEqual(bob.count(), 2)
                self.assertEqual(alice.count(), 0)
                self.assertEqual(sorted(k for k, _, _ in bob.items()), ["k1", "k2"])
                with self.assertRaises(StoreError):
                    alice.remove_where({})


if __name__ == "__main__":
    unittest.main()
