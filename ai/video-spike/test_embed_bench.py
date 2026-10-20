"""embed_bench 的离线测试：不加载模型、不用 ffmpeg，只验证打分、指标、预处理和整条 fake 流程。

    python -m unittest test_embed_bench -v
"""

import json
import tempfile
import unittest
from pathlib import Path

import numpy as np
from PIL import Image

import embed_bench as eb


def unit(*xs):
    v = np.array(xs, np.float32)
    return v / np.linalg.norm(v)


class Scoring(unittest.TestCase):
    def test_single_is_cosine(self):
        q = np.stack([unit(1, 0), unit(0, 1)])
        c = np.stack([unit(1, 0), unit(1, 1)])
        s = eb.score_single(q, c)
        self.assertAlmostEqual(s[0, 0], 1.0, places=5)
        self.assertAlmostEqual(s[0, 1], 0.7071, places=3)
        self.assertAlmostEqual(s[1, 0], 0.0, places=5)

    def test_maxsim_takes_best_frame_per_clip(self):
        q = np.stack([unit(1, 0)])
        frames = np.stack([unit(0, 1), unit(1, 0), unit(1, 1)])   # 片段 0 有两帧，片段 1 一帧
        owner = np.array([0, 0, 1])
        s = eb.score_maxsim(q, frames, owner, 2)
        self.assertAlmostEqual(s[0, 0], 1.0, places=5)           # 片段 0 取到了 (1,0) 那一帧
        self.assertAlmostEqual(s[0, 1], 0.7071, places=3)

    def test_maxsim_clip_without_frames_scores_minus_one(self):
        s = eb.score_maxsim(np.stack([unit(1, 0)]), np.stack([unit(1, 0)]), np.array([0]), 2)
        self.assertEqual(s[0, 1], -1.0)

    def test_mean_by_owner_normalises(self):
        frames = np.stack([unit(1, 0), unit(0, 1)])
        m = eb.mean_by_owner(frames, np.array([0, 0]), 1)
        self.assertAlmostEqual(float(np.linalg.norm(m[0])), 1.0, places=5)
        self.assertAlmostEqual(m[0, 0], m[0, 1], places=5)


class Metrics(unittest.TestCase):
    clip_ids = ["a", "b", "c", "d", "e", "f"]

    def scores(self, order_for_q0):
        """让查询 0 的排序恰好是 order_for_q0。"""
        s = np.zeros((1, 6), np.float32)
        for rank, cid in enumerate(order_for_q0):
            s[0, self.clip_ids.index(cid)] = 1.0 - rank * 0.1
        return s

    def test_hit_and_mrr(self):
        s = self.scores(["b", "a", "c", "d", "e", "f"])
        m = eb.metrics(s, [{"query": "x", "relevant": ["a"]}], self.clip_ids)
        self.assertEqual(m["hit@1"], 0.0)
        self.assertEqual(m["hit@5"], 1.0)
        self.assertEqual(m["recall@5"], 1.0)
        self.assertEqual(m["mrr"], 0.5)

    def test_recall_counts_partial(self):
        s = self.scores(["a", "b", "c", "d", "e", "f"])
        m = eb.metrics(s, [{"query": "x", "relevant": ["a", "f"]}], self.clip_ids)
        self.assertEqual(m["hit@1"], 1.0)
        self.assertEqual(m["recall@5"], 0.5)          # f 在第 6 位，前 5 只命中 a
        self.assertEqual(m["mrr"], 1.0)

    def test_unlabelled_queries_are_skipped(self):
        s = np.zeros((2, 6), np.float32)
        m = eb.metrics(s, [{"query": "x", "relevant": []}, {"query": "y", "relevant": []}], self.clip_ids)
        self.assertEqual(m, {"labelled_queries": 0})

    def test_topk_marks_relevant(self):
        s = self.scores(["a", "b", "c", "d", "e", "f"])
        rows = eb.topk_table(s, [{"query": "x", "relevant": ["b"]}], self.clip_ids)
        self.assertEqual([h["clip"] for h in rows[0]["top"]], ["a", "b", "c", "d", "e"])
        self.assertEqual([h["relevant"] for h in rows[0]["top"]], [False, True, False, False, False])


class Preprocessing(unittest.TestCase):
    def test_load_frame_resizes_long_edge_and_flattens_alpha(self):
        with tempfile.TemporaryDirectory() as d:
            p = Path(d) / "a.png"
            Image.new("RGBA", (1600, 900), (0, 0, 0, 0)).save(p)     # 全透明
            img = eb.load_frame(p, 512)
            self.assertEqual(img.mode, "RGB")
            self.assertEqual(max(img.size), 512)
            self.assertEqual(img.getpixel((10, 10)), (255, 255, 255))  # 透明合成了白底

    def test_load_frame_keeps_small_images(self):
        with tempfile.TemporaryDirectory() as d:
            p = Path(d) / "a.jpg"
            Image.new("RGB", (300, 200)).save(p)
            self.assertEqual(eb.load_frame(p, 512).size, (300, 200))

    def test_per_hour_estimate(self):
        # 10 段共 60 帧、1 fps 即 60 秒素材，花了 6 秒 → 一小时 360 秒
        self.assertEqual(eb.per_hour_estimate(6.0, 10, 60, 1.0), 360.0)
        self.assertIsNone(eb.per_hour_estimate(6.0, 0, 0, 1.0))


class FakeEndToEnd(unittest.TestCase):
    def make_clips(self, root: Path, n=4, frames=3):
        for i in range(1, n + 1):
            d = root / f"clip_{i:03d}"
            d.mkdir(parents=True)
            for f in range(1, frames + 1):
                Image.new("RGB", (64, 48), (i * 50 % 256, f * 70 % 256, 0)).save(d / f"{f:04d}.jpg")

    def test_template_then_run_then_reuse(self):
        with tempfile.TemporaryDirectory() as d:
            root = Path(d)
            self.make_clips(root / "clips")
            eb.main(["--clips", str(root / "clips"), "--make-template", str(root / "t.json")])
            template = json.loads((root / "t.json").read_text(encoding="utf-8"))
            self.assertEqual(template[-1]["_clips"], ["clip_001", "clip_002", "clip_003", "clip_004"])

            (root / "q.json").write_text(json.dumps([
                {"query": "一", "relevant": ["clip_001"]},
                {"query": "二", "relevant": []},
            ], ensure_ascii=False), encoding="utf-8")
            (root / "cap.json").write_text(json.dumps({"clip_001": "描述一"}, ensure_ascii=False), encoding="utf-8")
            eb.main(["--clips", str(root / "clips"), "--queries", str(root / "q.json"),
                     "--out", str(root / "out"), "--fake", "--captions", str(root / "cap.json")])
            report = json.loads((root / "out" / "report.json").read_text(encoding="utf-8"))
            self.assertEqual(set(report["metrics"]), {"native", "mean", "maxsim", "nativetext"})
            self.assertEqual(report["metrics"]["native"]["labelled_queries"], 1)
            self.assertEqual(report["frames"], 12)
            self.assertIn("seconds_per_hour_of_footage", report["timing"]["frames"])
            self.assertTrue((root / "out" / "embeddings.npz").exists())

            # 复用缓存只跑一个方法，指标表只剩它
            eb.main(["--clips", str(root / "clips"), "--queries", str(root / "q.json"),
                     "--out", str(root / "out"), "--fake", "--reuse", "--methods", "maxsim"])
            report = json.loads((root / "out" / "report.json").read_text(encoding="utf-8"))
            self.assertEqual(list(report["metrics"]), ["maxsim"])

    def test_nativetext_skipped_without_captions(self):
        with tempfile.TemporaryDirectory() as d:
            root = Path(d)
            self.make_clips(root / "clips", n=2)
            (root / "q.json").write_text(json.dumps([{"query": "一"}]), encoding="utf-8")
            eb.main(["--clips", str(root / "clips"), "--queries", str(root / "q.json"),
                     "--out", str(root / "out"), "--fake"])
            report = json.loads((root / "out" / "report.json").read_text(encoding="utf-8"))
            self.assertNotIn("nativetext", report["metrics"])


if __name__ == "__main__":
    unittest.main()
