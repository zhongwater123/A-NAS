"""The timed pipeline records what each stage took, without running the steps (#326)."""

import importlib.util
import json
import os
import tempfile
import unittest
from unittest import mock

HAVE = all(importlib.util.find_spec(name) for name in ("numpy", "PIL"))


@unittest.skipUnless(HAVE, "eval.video_moments needs NumPy and Pillow")
class PipelineTest(unittest.TestCase):
    def setUp(self):
        from eval import video_moments as vm, video_pipeline

        self.vm, self.pipeline = vm, video_pipeline
        self.root = tempfile.TemporaryDirectory()
        self.addCleanup(self.root.cleanup)
        patch = mock.patch.multiple(vm, CACHE=self.root.name, SET="set.json")
        patch.start()
        self.addCleanup(patch.stop)
        self.directory = os.path.join(self.root.name, "model")

    def write(self, name, data):
        os.makedirs(self.directory, exist_ok=True)
        with open(os.path.join(self.directory, name), "w", encoding="utf-8") as file:
            json.dump(data, file)

    def times(self):
        with open(os.path.join(self.directory, self.pipeline.TIMES), encoding="utf-8") as file:
            return json.load(file)

    def test_prepare_runs_the_steps_in_order_and_times_them(self):
        ran = []

        def run(command, **_):
            ran.append(command[command.index("--set") + 2])
            if ran[-1] == "index":
                self.write("index.json", {"videos": {"10": {"duration": 60.04}, "11": {"duration": 30.0}}})

        def popen(command, **_):
            ran.append(command[command.index("--set") + 2])
            self.write("captions.json", {"cards": {"10-0-0": {}, "11-0-0": {}, "11-1-0": {}}})
            return mock.Mock(wait=mock.Mock(return_value=0))

        args = mock.Mock(python="vm python", model="m.litertlm", vision="http://v")
        with mock.patch("subprocess.run", side_effect=run), mock.patch("subprocess.Popen", side_effect=popen):
            self.assertEqual(self.pipeline.prepare(args), self.directory)
        self.assertEqual(ran, ["frames", "embed", "index", "describe", "measure"])
        prepared = self.times()["prepare"]
        self.assertEqual((prepared["videos"], prepared["videoSeconds"], prepared["cards"]), (2, 90.0, 3))
        self.assertEqual(set(prepared["steps"]), {"frames", "embed", "index", "describe", "measure"})

    def test_a_cut_splits_its_time_and_keeps_the_earlier_cuts(self):
        self.write("index.json", {"videos": {}})
        self.write(self.pipeline.TIMES, {"prepare": {"seconds": 1}, "cuts": [{"at": "before"}]})
        turn = {"seconds": 300, "modelSeconds": 200, "calls": ["list_video_contents", "make_montage", "make_montage"],
                "refused": 1, "cost": 0.5, "cut": "montage-x"}

        def copy_out(_container, _name, target):
            with open(target + ".json", "w", encoding="utf-8") as file:
                json.dump({"segments": [{"start": 1, "end": 5.5}, {"start": 10, "end": 14}]}, file)

        args = mock.Mock(container="sbx", planner=None)
        with mock.patch.object(self.pipeline, "served", return_value=True), \
                mock.patch.object(self.pipeline.video_planner, "turn", return_value=turn), \
                mock.patch.object(self.pipeline.video_planner, "copy_out", side_effect=copy_out):
            self.pipeline.cut(args, self.directory, {"id": "noir", "text": "预告片"})
        times = self.times()
        self.assertEqual(times["prepare"], {"seconds": 1})
        made = times["cuts"][-1]
        self.assertEqual(len(times["cuts"]), 2)
        self.assertEqual((made["model"], made["tools"], made["plans"], made["refused"], made["cutSeconds"]),
                         (200, 100, 2, 1, 8.5))

    def test_instructions_come_one_or_from_a_file(self):
        path = os.path.join(self.root.name, "asks.json")
        with open(path, "w", encoding="utf-8") as file:
            json.dump({"instructions": [{"id": "a", "text": "大海"}, {"id": "b", "text": "城堡"}]}, file)
        one = mock.Mock(instruction="大海", instructions=None, style="旅行", only=None)
        self.assertEqual(self.pipeline.instructions(one), [{"text": "大海", "style": "旅行"}])
        listed = mock.Mock(instruction=None, instructions=path, style=None, only=["b"])
        self.assertEqual(self.pipeline.instructions(listed), [{"id": "b", "text": "城堡"}])

    def test_a_cut_needs_the_sandbox_to_serve_the_set(self):
        self.write("index.json", {"videos": {}})
        args = mock.Mock(container="sbx", planner=None)
        with mock.patch.object(self.pipeline, "served", return_value=False), self.assertRaises(SystemExit):
            self.pipeline.cut(args, self.directory, {"id": "noir", "text": "预告片"})


if __name__ == "__main__":
    unittest.main()
