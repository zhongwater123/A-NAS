"""Tests of ai/finetune with the standard library only: no model, no network.

HttpEmbedder talks to an in-process finetune.server --fake over real HTTP;
ProviderEmbedder wraps the fake provider the Worker tests use.
"""

import json
import os
import tempfile
import threading
import unittest
from pathlib import Path

from anas_ai import providers
from finetune import build_triplets, server, similarity
from finetune import eval as finetune_eval
from finetune.embedders import (PROMPTS, CachedEmbedder, EmbedError, HashEmbedder, HttpEmbedder, ProviderEmbedder,
                                build_embedder, cosine, threshold_for, top_k, truncate)


class VectorMathTest(unittest.TestCase):
    def test_truncate_renormalizes(self):
        v = truncate([3.0, 4.0, 100.0], 2)
        self.assertEqual(len(v), 2)
        self.assertAlmostEqual(cosine(v, v), 1.0, places=6)
        self.assertAlmostEqual(v[0], 0.6, places=6)

    def test_truncate_noop_when_dim_large(self):
        self.assertEqual(len(truncate([1.0, 0.0], 768)), 2)

    def test_top_k_orders_by_similarity(self):
        self.assertEqual([i for i, _ in top_k([1.0, 0.0], [[0.0, 1.0], [1.0, 0.1], [-1.0, 0.0]], 2)], [1, 0])


class HashEmbedderTest(unittest.TestCase):
    def setUp(self):
        self.e = HashEmbedder(128)

    def test_deterministic_and_normalized(self):
        a, b = self.e.embed(["EACCES: permission denied"] * 2)
        self.assertEqual(a, b)
        self.assertAlmostEqual(cosine(a, a), 1.0, places=6)
        self.assertEqual(len(a), 128)

    def test_similar_text_scores_higher_than_unrelated(self):
        v1, v2, v3 = self.e.embed(["EACCES: permission denied, mkdir '/.n8n'",
                                   "Error: EACCES permission denied mkdir /.n8n/config",
                                   "listen EADDRINUSE: address already in use :::5678"])
        self.assertGreater(cosine(v1, v2), cosine(v1, v3))

    def test_chinese_ngrams(self):
        a, b, c = self.e.embed(["上传大文件后校验失败", "大文件上传后校验不通过", "相册回收站无法恢复"])
        self.assertGreater(cosine(a, b), cosine(a, c))


class TextFakeProvider(providers.FakeProvider):
    """The Worker's fake provider plus the raw embed_text the real one has."""

    def embed_text(self, text):
        return self._vector(text.encode())


class ProviderEmbedderTest(unittest.TestCase):
    def test_adds_prompt_and_truncates(self):
        provider = TextFakeProvider(768)
        e = ProviderEmbedder(provider, dim=256)
        self.assertEqual(e.name, f"provider:{provider.model}@256")
        v = e.embed(["猫"], "query")[0]
        self.assertEqual(len(v), 256)
        # The fake provider hashes its input, so the prompt the adapter adds shows in the vector.
        self.assertAlmostEqual(cosine(v, truncate(provider.embed_text(PROMPTS["query"] + "猫"), 256)), 1.0, places=6)
        self.assertLess(cosine(v, truncate(provider.embed_text("猫"), 256)), 0.5)

    def test_default_dim_from_provider(self):
        e = ProviderEmbedder(TextFakeProvider(64), add_prefix=False)
        self.assertEqual(e.dim, 64)
        self.assertEqual(len(e.embed(["x"])[0]), 64)


class HttpEmbedderTest(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.srv = server.serve("127.0.0.1", 0, server.FakeBackend(768), fake=True)
        cls.port = cls.srv.server_address[1]
        threading.Thread(target=cls.srv.serve_forever, daemon=True).start()

    @classmethod
    def tearDownClass(cls):
        cls.srv.shutdown()
        cls.srv.server_close()

    def test_round_trip_with_prefix_and_truncation(self):
        e = HttpEmbedder(f"http://127.0.0.1:{self.port}", "fake", dim=256)
        vecs = e.embed(["hello", "world"], "query")
        self.assertEqual((len(vecs), len(vecs[0])), (2, 256))
        # The server does not rewrite its input, so the client's prompt shows in the vector.
        local = truncate(HashEmbedder(768).embed([PROMPTS["query"] + "hello"])[0], 256)
        self.assertAlmostEqual(cosine(vecs[0], local), 1.0, places=6)

    def test_batches_preserve_order(self):
        e = HttpEmbedder(f"http://127.0.0.1:{self.port}", "fake", dim=None, batch_size=2)
        texts = [f"t{i}" for i in range(5)]
        for a, b in zip(e.embed(texts), [e.embed([t])[0] for t in texts]):
            self.assertAlmostEqual(cosine(a, b), 1.0, places=6)

    def test_unavailable_server_raises_clean_error(self):
        with self.assertRaises(EmbedError):
            HttpEmbedder("http://127.0.0.1:1", "fake", timeout=1).embed(["x"])

    def test_health(self):
        import urllib.request
        h = json.loads(urllib.request.urlopen(f"http://127.0.0.1:{self.port}/health").read())
        self.assertTrue(h["ok"] and h["fake"])


class CacheAndBuildTest(unittest.TestCase):
    def test_cache_hits_disk_and_memory(self):
        calls = []

        class Counting:
            name, dim, semantic = "counting", 8, False

            def embed(self, texts, kind="sts"):
                calls.append(list(texts))
                return [[1.0] + [0.0] * 7 for _ in texts]

        with tempfile.TemporaryDirectory() as d:
            p = os.path.join(d, "c.jsonl")
            c = CachedEmbedder(Counting(), p)
            c.embed(["a", "b"])
            c.embed(["a", "c"])
            self.assertEqual(calls, [["a", "b"], ["c"]])
            CachedEmbedder(Counting(), p).embed(["a", "b", "c"])   # a new process loads the file
            self.assertEqual(calls, [["a", "b"], ["c"]])
            with open(p, encoding="utf-8") as f:
                self.assertEqual(len(f.read().splitlines()), 3)

    def test_build_embedder_falls_back_to_hash_without_url(self):
        os.environ.pop("ANAS_EMBED_URL", None)
        msgs = []
        e = build_embedder({"dim": 64}, cache_dir=None, logger=msgs.append)
        self.assertFalse(e.semantic)
        self.assertEqual(e.dim, 64)
        self.assertTrue(any("退回" in m for m in msgs))
        self.assertEqual(threshold_for(e, {"a": 0.6, "a_hash": 0.3}, "a", 0.5, 0.2), 0.3)


class SimilarityToolsTest(unittest.TestCase):
    def setUp(self):
        self.e = HashEmbedder(512)

    def test_rank_puts_same_issue_first(self):
        cands = ["上传 50MB 文件后下载校验 SHA-256 不一致", "相册回收站里的图片点恢复没有反应", "快照列表在成员账号下返回 500"]
        hits = similarity.rank("大文件上传后下载回来 SHA-256 校验不一致", cands, self.e, k=2)
        self.assertEqual(hits[0].index, 0)
        self.assertEqual(len(hits), 2)
        self.assertEqual(set(hits[0].as_dict()), {"index", "score", "text"})
        self.assertEqual(similarity.rank("x", [], self.e), [])

    def test_find_duplicates_groups_near_copies(self):
        texts = ["Out of memory: Killed process 777 (python)", "Out of memory: Killed process 778 (node)",
                 "listen EADDRINUSE :::3000", "wholly unrelated line about scheduling"]
        self.assertEqual(similarity.find_duplicates(texts, self.e, threshold=0.5), [[0, 1]])
        self.assertEqual(similarity.find_duplicates([], self.e, 0.5), [])

    def test_classify_by_examples(self):
        clf = similarity.classify_by_examples([
            similarity.Example("oom", "Out of memory: Killed process 1234 (node)"),
            similarity.Example("oom", "container exited with code 137 (OOMKilled)"),
            similarity.Example("port", "listen EADDRINUSE: address already in use :::5678"),
        ], self.e, threshold=0.30)
        label, _score, ex = clf("Out of virtual memory: Killed process 777 (python)")
        self.assertEqual((label, ex.label), ("oom", "oom"))
        label, _score, _ = clf("Scheduled job finished in 3 ms")
        self.assertIsNone(label)
        self.assertEqual(similarity.classify_by_examples([], self.e, 0.3)("x"), (None, 0.0, None))


class FinetuneScriptsTest(unittest.TestCase):
    def test_build_triplets_and_eval(self):
        with tempfile.TemporaryDirectory() as d:
            labeled = os.path.join(d, "labeled.jsonl")
            rows = [{"text": "Out of memory: Killed process 1 (node)", "label": "oom"},
                    {"text": "JavaScript heap out of memory", "label": "oom"},
                    {"text": "listen EADDRINUSE :::5678", "label": "port"},
                    {"text": "bind: address already in use 0.0.0.0:8080", "label": "port"},
                    {"text": "no space left on device", "label": "disk"},
                    {"text": "ENOSPC: no space left on device, write", "label": "disk"}]
            with open(labeled, "w", encoding="utf-8") as f:
                f.write("".join(json.dumps(r, ensure_ascii=False) + "\n" for r in rows))
            pairs = os.path.join(d, "pairs.jsonl")
            with open(pairs, "w", encoding="utf-8") as f:
                f.write(json.dumps({"a": "上传后校验失败", "b": "上传的文件校验不通过"}) + "\n")
            out = os.path.join(d, "triplets.jsonl")
            build_triplets.main(["--labeled", labeled, "--pairs", pairs, "--out", out])
            with open(out, encoding="utf-8") as f:
                trip = [json.loads(line) for line in f.read().splitlines()]
            self.assertEqual(len(trip), 6 + 2)
            for t in trip:
                self.assertNotEqual(t["negative"], t["positive"])
                self.assertNotEqual(t["negative"], t["anchor"])
            r = finetune_eval.evaluate(trip, HashEmbedder(512))
            self.assertEqual(set(r), {"embedder", "anchors", "corpus", "hit@1", "hit@5", "mrr@10",
                                      "mean_margin", "neg_margin_ratio"})
            self.assertGreaterEqual(r["hit@5"], r["hit@1"])
            r2 = finetune_eval.evaluate(trip, ProviderEmbedder(TextFakeProvider(64)))
            self.assertEqual(r2["anchors"], r["anchors"])


class EvalDocsTest(unittest.TestCase):
    def test_chunking_and_evaluation(self):
        from finetune import eval_docs
        nl = chr(10)
        with tempfile.TemporaryDirectory() as d:
            docs = os.path.join(d, "docs")
            os.makedirs(os.path.join(docs, "specs"))
            body = nl.join(["# 相册", "", "开头一段。", "", "## 回收站", "",
                            "照片删除后进入回收站，三十天内可以恢复，恢复后回到原相册。",
                            "```", "# 代码块里的井号不是标题", "```", "", "## 搜索", "",
                            "用中文描述搜索照片，结果按相似度排序。", ""])
            with open(os.path.join(docs, "specs", "a.md"), "w", encoding="utf-8") as f:
                f.write(body)
            with open(os.path.join(docs, "README.md"), "w", encoding="utf-8") as f:
                f.write(nl.join(["# 索引", "", "被跳过的文件。", ""]))
            chunks = eval_docs.chunk_markdown(Path(docs), min_chars=5)
            ids = [c.id for c in chunks]
            self.assertIn("specs/a.md#回收站", ids)
            self.assertIn("specs/a.md#搜索", ids)
            self.assertTrue(all(c.path == "specs/a.md" for c in chunks))
            self.assertNotIn("specs/a.md#代码块里的井号不是标题", ids)
            queries = [{"q": "删掉的照片多久内还能恢复", "target": "specs/a.md#回收站"},
                       {"q": "怎么用一句话找照片", "target": ["specs/a.md#搜索"]}]
            r = eval_docs.evaluate(chunks, queries, HashEmbedder(256), "retrieval")
            for k in ("hit@1", "hit@5", "hit@10", "mrr@10", "file_hit@1", "file_hit@5"):
                self.assertIn(k, r)
            self.assertEqual(r["file_hit@1"], 1.0)   # 只有一篇文档
            self.assertGreaterEqual(r["hit@5"], r["hit@1"])

    def test_truncated_wrapper(self):
        from finetune.eval_docs import Truncated
        t = Truncated(HashEmbedder(64), 16)
        v = t.embed(["hello world"])[0]
        self.assertEqual(len(v), 16)
        self.assertAlmostEqual(cosine(v, v), 1.0, places=6)
        self.assertEqual(t.name, "hash-ngram->16")

    def test_long_path_reader(self):
        from finetune.eval_docs import _read_text
        with tempfile.TemporaryDirectory() as d:
            p = Path(d) / "x.md"
            p.write_text("内容", encoding="utf-8")
            self.assertEqual(_read_text(p), "内容")

class EvalDedupTest(unittest.TestCase):
    def test_strip_template_and_load_texts(self):
        from finetune import eval_dedup
        t = eval_dedup.strip_template("【A 测试｜文件】上传失败 版本：0.3.1 复现：2026-10-13 点上传")
        self.assertNotIn("【", t)
        self.assertNotIn("0.3.1", t)
        self.assertNotIn("2026-10-13", t)
        self.assertIn("上传失败", t)
        with tempfile.TemporaryDirectory() as d:
            path = os.path.join(d, "t.json")
            with open(path, "w", encoding="utf-8") as f:
                f.write("已登录" + chr(10) + json.dumps({"items": [{"text": " a  b "}, {"text": ""}, {"text": "c"}]}, ensure_ascii=False))
            self.assertEqual(eval_dedup.load_texts(path, "items.text"), ["a b", "c"])

    def test_doc_query_pairs(self):
        from finetune import build_triplets
        nl = chr(10)
        with tempfile.TemporaryDirectory() as d:
            docs = os.path.join(d, "docs")
            os.makedirs(docs)
            with open(os.path.join(docs, "a.md"), "w", encoding="utf-8") as f:
                f.write(nl.join(["# 标题", "", "## 回收站", "", "照片删除后进入回收站，三十天内可以恢复。", ""]))
            q = os.path.join(d, "q.json")
            with open(q, "w", encoding="utf-8") as f:
                json.dump([{"q": "删掉的照片多久能恢复", "target": "a.md#回收站"},
                           {"q": "不存在的", "target": "a.md#没有这节"}], f, ensure_ascii=False)
            pairs = build_triplets.doc_query_pairs(q, docs)
            self.assertEqual(len(pairs), 1)
            self.assertEqual(pairs[0]["a"], "删掉的照片多久能恢复")
            self.assertIn("三十天", pairs[0]["b"])

class PromptSelectionTest(unittest.TestCase):
    ROWS = [{"anchor": "问题", "positive": "段落", "negative": "别的段落", "source": "docq:0"},
            {"anchor": "句子甲", "positive": "句子乙", "negative": "句子丙", "source": "label:x"}]

    def test_eval_prompt_kinds_by_source(self):
        akind, ckind = finetune_eval.prompt_kinds(self.ROWS)
        self.assertEqual(akind, {"问题": "query", "句子甲": "sts"})
        self.assertEqual(ckind, {"段落": "document", "别的段落": "document", "句子乙": "sts", "句子丙": "sts"})
        r = finetune_eval.evaluate(self.ROWS, HashEmbedder(128))
        self.assertEqual(r["anchors"], 2)

    def test_train_with_prompts_prepends_by_source(self):
        from finetune import train
        rows = train.with_prompts(self.ROWS)
        self.assertTrue(rows[0]["anchor"].startswith(train.QUERY_PROMPT))
        self.assertTrue(rows[0]["positive"].startswith(train.DOC_PROMPT))
        self.assertTrue(rows[1]["anchor"].startswith(train.STS_PROMPT))
        self.assertTrue(rows[1]["negative"].startswith(train.STS_PROMPT))
        self.assertEqual(set(rows[0]), {"anchor", "positive", "negative"})

if __name__ == "__main__":
    unittest.main()
