"""The montage run's time and outside calls, as #343 reports them; a stand-in clock and memory peak."""

import json
import os
import shutil
import tempfile
import unittest

from montage import timing


class Clock:
    def __init__(self):
        self.now = 100.0

    def __call__(self):
        return self.now


class TimingTest(unittest.TestCase):
    def setUp(self):
        self.work = tempfile.mkdtemp()
        self.addCleanup(shutil.rmtree, self.work)
        self.clock = Clock()
        self.run = timing.Run(clock=self.clock, peak=os.path.join(self.work, "none"))

    def test_each_step_has_its_start_and_seconds_and_categories_add_up(self):
        self.clock.now += 0.5
        with self.run.step("probe", "读取与解码"):
            self.clock.now += 2
        with self.run.step("caption", "理解") as step:
            step["cards"] = 12
            self.clock.now += 10
        with self.run.step("measure", "理解"):
            self.clock.now += 3
        self.clock.now += 0.25
        summary = self.run.summary()
        self.assertEqual(summary["seconds"], 15.75)
        self.assertEqual([(s["id"], s["start"], s["seconds"]) for s in summary["steps"]],
                         [("probe", 0.5, 2), ("caption", 2.5, 10), ("measure", 12.5, 3)])
        self.assertEqual(summary["steps"][1]["cards"], 12)
        self.assertEqual(summary["categories"], {"读取与解码": 2, "理解": 13})
        self.assertEqual(summary["uncovered"], 0.75)
        self.assertNotIn("memoryPeakMiB", summary)

    def test_steps_side_by_side_count_whole_but_cover_the_wall_once(self):
        first, second = self.run.step("caption", "理解"), self.run.step("measure", "理解")
        first.__enter__()
        self.clock.now += 1
        second.__enter__()
        self.clock.now += 4
        first.__exit__(None, None, None)
        self.clock.now += 1
        second.__exit__(None, None, None)
        summary = self.run.summary()
        # 5 s each over a 6 s wall.
        self.assertEqual(summary["categories"], {"理解": 10})
        self.assertEqual(summary["uncovered"], 0)

    def test_a_step_that_raises_is_kept_and_the_error_goes_on(self):
        with self.assertRaises(RuntimeError), self.run.step("render", "渲染"):
            self.clock.now += 2
            raise RuntimeError("ffmpeg")
        self.assertEqual(self.run.summary()["steps"], [{"id": "render", "category": "渲染", "start": 0,
                                                        "failed": "RuntimeError", "seconds": 2}])

    def test_a_category_outside_the_rules_is_refused(self):
        with self.assertRaises(ValueError), self.run.step("plan", "规划"):
            pass

    def test_outside_calls_add_up_into_the_report(self):
        with self.run.step("decode", "读取与解码"):
            self.clock.now += 3.04
        self.assertEqual(timing.report(self.run.summary()), {"外部调用": "无", "各步耗时": "读取与解码 3.0"})
        with self.run.step("plan", "选择与编排"):
            self.clock.now += 20
            self.run.call("plan", "anthropic", "claude-opus-5-5", input_tokens=30000, output_tokens=2000,
                          upload_bytes=90 * 1024, cost=0.21, seconds=12)
            self.run.call("plan", "anthropic", "claude-opus-5-5", input_tokens=32000, output_tokens=1500,
                          cache_read_tokens=28000, upload_bytes=6 * 1024, cost=0.08, seconds=7)
        self.clock.now += 1
        summary = self.run.summary()
        self.assertEqual(summary["external"], {"count": 2, "inputTokens": 62000, "outputTokens": 3500,
                                               "cacheReadTokens": 28000, "cacheWriteTokens": 0,
                                               "uploadBytes": 96 * 1024, "cost": 0.29})
        self.assertEqual(timing.report(summary), {"外部调用": "2 次 / 96 KiB / $0.29",
                                                  "各步耗时": "读取与解码 3.0；选择与编排 20.0；步骤之外 1.0"})

    def test_the_memory_peak_is_read_after_each_step_and_at_the_end(self):
        peak = os.path.join(self.work, "memory.peak")
        run = timing.Run(clock=self.clock, peak=peak)
        with open(peak, "w", encoding="ascii") as file:
            file.write(f"{512 * timing.MIB}\n")
        with run.step("decode", "读取与解码"):
            pass
        with open(peak, "w", encoding="ascii") as file:
            file.write(f"{1536 * timing.MIB}\n")
        summary = run.summary()
        self.assertEqual(summary["steps"][0]["memoryPeakMiB"], 512)
        self.assertEqual(summary["memoryPeakMiB"], 1536)

    def test_write_replaces_the_file_whole(self):
        path = os.path.join(self.work, "pipeline.json")
        with self.run.step("render", "渲染"):
            self.clock.now += 1
        written = self.run.write(path)
        with open(path, encoding="utf-8") as file:
            self.assertEqual(json.load(file), written)
        self.assertEqual(os.listdir(self.work), ["pipeline.json"])


if __name__ == "__main__":
    unittest.main()
