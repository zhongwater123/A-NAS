"""Tests of ai/finetune/store.py, index.py and vectors.py with the standard library only.

The same contract runs against MemoryStore, a persisted MemoryStore and QdrantStore
talking over real HTTP to the in-process fake Qdrant (tests/fake_qdrant.py).
Set ANAS_QDRANT_TEST_URL (for example http://127.0.0.1:6333) to run the contract
against a real Qdrant as well; it uses a test collection and drops it afterwards.
"""

import io
import json
import os
import tempfile
import unittest
import urllib.request
from contextlib import redirect_stdout
from pathlib import Path

from finetune import vectors
from finetune.embedders import HashEmbedder
from finetune.index import TextIndex
from finetune.store import MemoryStore, QdrantStore, StoreError, build_store, matches_filter, point_id, where
from tests import fake_qdrant

ENV_KEYS = ("ANAS_QDRANT_URL", "ANAS_QDRANT_API_KEY", "ANAS_QDRANT_COLLECTION", "ANAS_EMBED_URL")


def unit(i: int, dim: int = 4) -> list[float]:
    v = [0.0] * dim
    v[i % dim] = 1.0
    return v


class StoreContract:
    """所有实现都要过的用例。子类提供 self.store（空集合）。"""

    store: object

    def test_ensure_collection_then_dim_mismatch(self):
        self.store.ensure_collection(4)
        self.store.ensure_collection(4)                       # 幂等
        with self.assertRaises(StoreError):
            self.store.ensure_collection(8)
        self.store.ensure_collection(8, recreate=True)        # 显式重建才允许换维度
        self.assertEqual(self.store.count(), 0)

    def test_upsert_search_get_delete(self):
        self.store.ensure_collection(4)
        self.store.upsert(["a", "b", 7], [unit(0), unit(1), [0.9, 0.1, 0.0, 0.0]],
                          [{"source": "x"}, {"source": "y"}, {"source": "x", "tags": ["p", "q"]}])
        self.assertEqual(self.store.count(), 3)
        hits = self.store.search(unit(0), k=2)
        self.assertEqual([h.key for h in hits], ["a", 7])
        self.assertAlmostEqual(hits[0].score, 1.0, places=5)
        self.assertEqual(hits[0].payload, {"source": "x"})
        self.assertNotIn("_key", hits[1].payload)
        self.assertEqual(set(hits[0].as_dict()), {"key", "score", "payload"})

        got = self.store.get(["a", 7, "missing"])
        self.assertEqual(set(got), {"a", 7})
        self.assertEqual(got[7]["tags"], ["p", "q"])

        self.store.upsert(["a"], [unit(1)], [{"source": "z"}])          # 同 key 覆盖
        self.assertEqual(self.store.count(), 3)
        self.assertEqual(self.store.get(["a"])["a"], {"source": "z"})

        self.store.delete(["a", "nope"])
        self.assertEqual(self.store.count(), 2)
        self.assertEqual(self.store.get(["a"]), {})

    def test_filter_min_score_and_delete_where(self):
        self.store.ensure_collection(4)
        self.store.upsert([f"k{i}" for i in range(6)], [unit(i) for i in range(6)],
                          [{"source": "n8n" if i % 2 else "nas", "level": i} for i in range(6)])
        hits = self.store.search(unit(0), k=10, filter=where(source="n8n"))
        self.assertTrue(hits and all(h.payload["source"] == "n8n" for h in hits))
        self.assertEqual(self.store.count(where(source=["n8n", "nas"])), 6)
        self.assertEqual(self.store.count({"must": [{"key": "level", "range": {"gte": 4}}]}), 2)
        # unit(0) 和 unit(4) 是同一个方向，min_score 应只留它们
        strong = self.store.search(unit(0), k=10, min_score=0.99)
        self.assertEqual(sorted(h.key for h in strong), ["k0", "k4"])
        self.store.delete_where(where(source="nas"))
        self.assertEqual(self.store.count(), 3)
        self.assertEqual(sorted(k for k, _ in self.store.scroll()), ["k1", "k3", "k5"])

    def test_wrong_dim_and_bad_lengths_raise(self):
        self.store.ensure_collection(4)
        with self.assertRaises(StoreError):
            self.store.upsert(["a"], [[1.0, 0.0]])
        with self.assertRaises(StoreError):
            self.store.upsert(["a", "b"], [unit(0)])
        with self.assertRaises(StoreError):
            self.store.upsert(["a"], [unit(0)], [{}, {}])

    def test_drop_and_info(self):
        self.store.ensure_collection(4)
        self.store.upsert(["a"], [unit(0)])
        info = self.store.info()
        self.assertEqual(info["collection"], self.store.collection)
        self.assertEqual(info["points"], 1)
        self.store.drop_collection()
        self.store.drop_collection()                          # 删不存在的集合不报错
        self.assertEqual(self.store.info().get("points") or 0, 0)


class MemoryStoreTest(StoreContract, unittest.TestCase):
    def setUp(self):
        self.store = MemoryStore("t")


class MemoryStorePersistedTest(StoreContract, unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.path = Path(self.tmp.name) / "t.json"
        self.store = MemoryStore("t", self.path)

    def tearDown(self):
        self.tmp.cleanup()

    def test_survives_reload(self):
        self.store.ensure_collection(4)
        self.store.upsert(["a", 3], [unit(0), unit(1)], [{"x": 1}, {}])
        again = MemoryStore("t", self.path)
        self.assertEqual(again.dim, 4)
        self.assertEqual(again.count(), 2)
        self.assertEqual(again.search(unit(1), 1)[0].key, 3)
        self.assertEqual(again.get(["a"])["a"], {"x": 1})


class QdrantStoreAgainstFakeTest(StoreContract, unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.srv, cls.fake = fake_qdrant.serve("127.0.0.1", 0, api_key="secret")
        cls.url = f"http://127.0.0.1:{cls.srv.server_address[1]}"

    @classmethod
    def tearDownClass(cls):
        cls.srv.shutdown()
        cls.srv.server_close()

    def setUp(self):
        self.store = QdrantStore(self.url, "t", api_key="secret")
        self.store.drop_collection()

    def test_uses_query_api_and_wait(self):
        self.store.ensure_collection(4)
        self.fake.requests.clear()
        self.store.upsert(["a"], [unit(0)])
        self.store.search(unit(0))
        self.assertIn(("PUT", "/collections/t/points"), self.fake.requests)
        self.assertIn(("POST", "/collections/t/points/query"), self.fake.requests)

    def test_api_key_required(self):
        with self.assertRaises(StoreError) as cm:
            QdrantStore(self.url, "t").ensure_collection(4)
        self.assertIn("401", str(cm.exception))

    def test_unreachable_gives_clean_error(self):
        with self.assertRaises(StoreError) as cm:
            QdrantStore("http://127.0.0.1:1", "t", timeout=1).count()
        self.assertIn("连不上", str(cm.exception))

    def test_string_keys_become_uuid_on_the_wire(self):
        self.store.ensure_collection(4)
        self.store.upsert(["反馈-101", "550e8400-e29b-41d4-a716-446655440000", 5], [unit(0), unit(1), unit(2)])
        ids = set(self.fake.collections["t"]["points"])
        self.assertEqual(ids, {point_id("反馈-101"), "550e8400-e29b-41d4-a716-446655440000", 5})
        self.assertEqual({h.key for h in self.store.search(unit(0), 3)},
                         {"反馈-101", "550e8400-e29b-41d4-a716-446655440000", 5})

    def test_collection_name_is_url_encoded(self):
        s = QdrantStore(self.url, "a/b c", api_key="secret")
        s.ensure_collection(4)
        s.upsert(["x"], [unit(0)])
        self.assertEqual(s.count(), 1)
        s.drop_collection()


@unittest.skipUnless(os.environ.get("ANAS_QDRANT_TEST_URL"), "set ANAS_QDRANT_TEST_URL to run against a real Qdrant")
class QdrantStoreAgainstRealTest(StoreContract, unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.url = os.environ["ANAS_QDRANT_TEST_URL"].rstrip("/")
        try:
            urllib.request.urlopen(cls.url + "/", timeout=3).read()
        except Exception as e:  # noqa: BLE001
            raise unittest.SkipTest(f"cannot reach {cls.url}: {e}")

    def setUp(self):
        self.store = QdrantStore(self.url, "test-anas-finetune-store", api_key=os.environ.get("ANAS_QDRANT_API_KEY"))
        self.store.drop_collection()

    def tearDown(self):
        self.store.drop_collection()

    def test_real_server_reports_version(self):
        self.assertTrue(self.store.server_info().get("version"))


class HelpersTest(unittest.TestCase):
    def test_point_id(self):
        self.assertEqual(point_id(12), 12)
        self.assertEqual(point_id("550e8400-e29b-41d4-a716-446655440000"), "550e8400-e29b-41d4-a716-446655440000")
        a, b = point_id("FB-1"), point_id("FB-1")
        self.assertEqual(a, b)
        self.assertNotEqual(a, point_id("FB-2"))
        self.assertEqual(len(a), 36)
        with self.assertRaises(StoreError):
            point_id(-1)
        with self.assertRaises(StoreError):
            point_id(True)

    def test_matches_filter_subset(self):
        pl = {"source": "n8n", "level": 3, "tags": ["a", "b"], "msg": "heap out of memory"}
        self.assertTrue(matches_filter(None, pl))
        self.assertTrue(matches_filter(where(source="n8n", tags="a"), pl))
        self.assertFalse(matches_filter(where(source="nas"), pl))
        self.assertTrue(matches_filter({"should": [{"key": "source", "match": {"value": "nas"}},
                                                   {"key": "level", "range": {"gt": 2, "lte": 3}}]}, pl))
        self.assertTrue(matches_filter({"must": [{"must": [{"key": "level", "range": {"gte": 3}}]}]}, pl))
        # 支持子集以外的写法一律拒绝，而不是当成满足（#152 S3）
        from finetune.store import UnsupportedFilter
        for bad in ({"must_not": [{"key": "msg", "match": {"text": "memory"}}]},
                    {"must": [{"key": "level", "match": {"except": [1, 2]}}]},
                    {"must": [{"is_null": {"key": "owner"}}]}):
            with self.assertRaises(UnsupportedFilter):
                matches_filter(bad, pl)

    def test_build_store_env_and_fallback(self):
        saved = {k: os.environ.pop(k, None) for k in ENV_KEYS}
        try:
            msgs = []
            with tempfile.TemporaryDirectory() as d:
                s = build_store({}, collection="c1", data_dir=Path(d), logger=msgs.append)
                self.assertIsInstance(s, MemoryStore)
                self.assertEqual(Path(s.path).name, "c1.json")
            self.assertTrue(any("进程内" in m for m in msgs))
            os.environ["ANAS_QDRANT_URL"] = "http://127.0.0.1:6333/"
            os.environ["ANAS_QDRANT_API_KEY"] = "k"
            os.environ["ANAS_QDRANT_COLLECTION"] = "envcol"
            s = build_store({"url": "http://ignored", "collection": "cfgcol"})
            self.assertIsInstance(s, QdrantStore)
            self.assertEqual((s.url, s.collection, s.api_key), ("http://127.0.0.1:6333", "envcol", "k"))
        finally:
            for k in ENV_KEYS:
                os.environ.pop(k, None)
                if saved[k] is not None:
                    os.environ[k] = saved[k]


class TextIndexTest(unittest.TestCase):
    def setUp(self):
        self.emb = HashEmbedder(512)
        self.idx = TextIndex(MemoryStore("idx"), self.emb)

    def test_add_search_similar(self):
        texts = ["上传 50MB 文件后下载校验 SHA-256 不一致", "相册回收站里的图片点恢复没有反应",
                 "快照列表在成员账号下返回 500"]
        keys = self.idx.add(texts, keys=["FB-1", "FB-2", "FB-3"],
                            payloads=[{"source": "feedback"}, {"source": "feedback"}, {"source": "regression"}])
        self.assertEqual(keys, ["FB-1", "FB-2", "FB-3"])
        self.assertEqual(len(self.idx), 3)
        hits = self.idx.search("大文件上传后下载回来 SHA-256 校验不一致", k=2)
        self.assertEqual(hits[0].key, "FB-1")
        self.assertEqual(hits[0].text, texts[0])
        self.assertEqual(hits[0].payload, {"source": "feedback"})
        self.assertEqual(set(hits[0].as_dict()), {"key", "score", "text", "payload"})
        only_reg = self.idx.search("校验不一致", k=5, where=where(source="regression"))
        self.assertEqual([h.key for h in only_reg], ["FB-3"])
        with self.assertRaises(StoreError):                     # 检索集合（document 前缀）不能做 similar
            self.idx.similar(texts[0], k=2)
        sts = TextIndex(MemoryStore("sts"), self.emb, task="sts")
        sts.add(texts)
        sim = sts.similar(texts[0], k=2)
        self.assertNotIn(texts[0], [h.text for h in sim])        # 自己被去掉
        self.assertEqual(len(sim), 2)

    def test_default_keys_dedupe_and_remove(self):
        self.idx.add(["a line", "a line", "another"])
        self.assertEqual(len(self.idx), 2)
        self.idx.remove(["a line"])
        self.assertEqual(len(self.idx), 1)
        self.idx.add(["x"], payloads=[{"source": "s"}])
        self.idx.remove_where(where(source="s"))
        self.assertEqual(self.idx.count(), 1)
        self.assertEqual(self.idx.info()["embedder"], self.emb.name)
        self.assertFalse(self.idx.semantic)

    def test_dim_follows_embedder(self):
        store = MemoryStore("d")
        TextIndex(store, HashEmbedder(64))
        with self.assertRaises(StoreError):
            TextIndex(store, HashEmbedder(128))
        TextIndex(store, HashEmbedder(128), recreate=True)
        self.assertEqual(store.dim, 128)

    def test_hash_embedder_dims_do_not_share_cache_keys(self):
        # CachedEmbedder 的键来自向量空间身份（含维度），128 维和 512 维的向量不会串（#152 R1）
        from finetune.embedders import CachedEmbedder
        self.assertNotEqual(CachedEmbedder(HashEmbedder(128), None)._key("sts", "t"),
                            CachedEmbedder(HashEmbedder(512), None)._key("sts", "t"))


class VectorsCliTest(unittest.TestCase):
    def test_add_search_info_export_drop_with_memory_store(self):
        saved = {k: os.environ.pop(k, None) for k in ENV_KEYS}
        try:
            with tempfile.TemporaryDirectory() as d:
                src = Path(d) / "fb.jsonl"
                rows = [{"id": "FB-1", "text": "上传 50MB 文件后下载校验 SHA-256 不一致", "source": "feedback"},
                        {"id": "FB-2", "text": "相册回收站里的图片点恢复没有反应", "source": "feedback"}]
                src.write_text("".join(json.dumps(r, ensure_ascii=False) + "\n" for r in rows), encoding="utf-8")
                common = ["-q", "--data-dir", d, "--collection", "t", "--dim", "512"]
                out = io.StringIO()
                with redirect_stdout(out):
                    vectors.main(common + ["add", str(src), "--key-field", "id", "--payload-fields", "source"])
                    vectors.main(common + ["search", "大文件上传后校验不一致", "-k", "1", "--json"])
                hit = json.loads(out.getvalue().strip().splitlines()[-1])
                self.assertEqual(hit[0]["key"], "FB-1")
                self.assertEqual(hit[0]["payload"]["source"], "feedback")
                out = io.StringIO()
                with redirect_stdout(out):
                    vectors.main(common + ["info"])
                    vectors.main(common + ["export"])
                text = out.getvalue()
                self.assertIn('"points": 2', text)
                self.assertEqual(sum(1 for line in text.splitlines() if line.startswith('{"key"')), 2)
                with redirect_stdout(io.StringIO()):
                    vectors.main(common + ["drop", "--yes"])
                self.assertFalse((Path(d) / "t.json").exists())
        finally:
            for k in ENV_KEYS:
                if saved[k] is not None:
                    os.environ[k] = saved[k]


if __name__ == "__main__":
    unittest.main()
