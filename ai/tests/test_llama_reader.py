"""Tests of LlamaReader against a stand-in llama-server (standard library only)."""

import base64
import hashlib
import importlib.util
import io
import json
import os
import shutil
import socket
import struct
import subprocess
import sys
import tempfile
import threading
import time
import unittest
from unittest import mock

from anas_ai import describe, providers

# A description that fits the schema for three beats.
ANSWER = {"camera": "地面", "beats": [{"shot_size": "远景", "what": "雪山"}, {"shot_size": "全景", "what": "走路"},
                                    {"shot_size": "特写", "what": "手"}],
          "process": ["走向雪山"], "summary": "两人走向雪山", "scene": "雪山下"}
STAND_IN = os.path.join(os.path.dirname(os.path.abspath(__file__)), "fake_llama_server.py")
AI = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))


class LlamaReaderTest(unittest.TestCase):
    def setUp(self):
        self.directory = tempfile.mkdtemp()
        self.addCleanup(shutil.rmtree, self.directory, True)
        self.record = os.path.join(self.directory, "request.json")
        self.environ = {"FAKE_LLAMA_RECORD": self.record}
        for name in ("FAKE_LLAMA_DELAY", "FAKE_LLAMA_STATUS", "FAKE_LLAMA_REPEAT", "FAKE_LLAMA_PIECE",
                     "FAKE_LLAMA_PIECE_DELAY", "FAKE_LLAMA_ERROR", "FAKE_LLAMA_NO_FINISH", "FAKE_LLAMA_START_DELAY",
                     "FAKE_LLAMA_EXIT", "FAKE_LLAMA_ANSWERS", "FAKE_LLAMA_FINISH", "FAKE_LLAMA_PROMPT_TOKENS"):
            self.environ[name] = None

    def reader(self, sha=None, cache_dir=None):
        self.addCleanup(self.restore_environ, dict(os.environ))
        for name, value in self.environ.items():
            if value is None:
                os.environ.pop(name, None)
            else:
                os.environ[name] = value
        files = []
        for name, content in (("model.gguf", b"language"), ("mmproj.gguf", b"vision")):
            path = os.path.join(self.directory, name)
            with open(path, "wb") as file:
                file.write(content)
            manifest = os.path.join(self.directory, name + ".json")
            with open(manifest, "w", encoding="utf-8") as file:
                json.dump({"name": name.split(".")[0], "sizeBytes": len(content), "memoryBytes": 1 << 30,
                           "sha256": sha or hashlib.sha256(content).hexdigest()}, file)
            files += [path, manifest]
        runtime = os.path.join(self.directory, "llama.json")
        with open(runtime, "w", encoding="utf-8") as file:
            json.dump({"version": "b11655"}, file)
        reader = providers.LlamaReader([sys.executable, STAND_IN], *files, runtime, threads=2, cache_dir=cache_dir)
        self.addCleanup(reader.release)
        return reader

    @staticmethod
    def restore_environ(saved):
        os.environ.clear()
        os.environ.update(saved)

    def test_reads_through_the_server_with_the_evaluated_settings(self):
        reader = self.reader()
        self.assertEqual(reader.memory, 2 << 30)
        self.assertRegex(reader.model, r"^model@[0-9a-f]{12}\+mmproj@[0-9a-f]{12}\+llama\.cpp-b11655\+img1024$")
        reading = reader.read(b"jpeg bytes", "text", None, reader.run_options())
        self.assertEqual(reading, {"text": "第一行\n第二行", "finish": "stop",
                                   "logprobs": [["第一行", -0.01], ["\n", -0.01], ["第二行", -0.01]]})
        with open(self.record, encoding="utf-8") as file:
            recorded = json.load(file)
        argv, body = recorded["argv"], recorded["body"]
        # A UNIX socket, no prompt cache, the evaluated image token cap.
        self.assertTrue(argv[argv.index("--host") + 1].endswith(".sock"))
        for flag, value in (("--cache-ram", "0"), ("--ctx-checkpoints", "0"), ("--image-max-tokens", "1024"),
                            ("--load-mode", "mmap"), ("-t", "2"), ("-np", "1")):
            self.assertEqual(argv[argv.index(flag) + 1], value, flag)
        image, prompt = body["messages"][0]["content"]
        self.assertEqual(image["image_url"]["url"], "data:image/jpeg;base64," + base64.b64encode(b"jpeg bytes").decode())
        self.assertEqual(prompt["text"], providers.OCR_TEXT_PROMPT)
        self.assertEqual((body["temperature"], body["cache_prompt"], body["logprobs"]), (0, False, True))
        self.assertEqual(body["chat_template_kwargs"], {"enable_thinking": False})

        schema = {"金额": "", "日期": ""}
        reader.read(b"jpeg bytes", "fields", schema, reader.run_options())
        with open(self.record, encoding="utf-8") as file:
            prompt = json.load(file)["body"]["messages"][0]["content"][1]["text"]
        self.assertEqual(prompt, providers.OCR_FIELDS_PROMPT + json.dumps(schema, ensure_ascii=False))

    def test_a_model_in_parts_is_checked_part_by_part_and_read_from_the_first(self):
        # As the content packages install it: the parts in one directory,
        # the role's link pointing at the first.
        store = os.path.join(self.directory, "store")
        os.mkdir(store)
        parts = []
        for index, content in enumerate((b"first half", b"second half"), start=1):
            name = f"model-{index:05d}-of-00002.gguf"
            with open(os.path.join(store, name), "wb") as file:
                file.write(content)
            parts.append({"file": name, "sizeBytes": len(content), "sha256": hashlib.sha256(content).hexdigest()})
        reader = self.reader()
        link = os.path.join(self.directory, "model.gguf")
        os.remove(link)
        os.symlink(os.path.join(store, parts[0]["file"]), link)
        manifest = os.path.join(self.directory, "model.gguf.json")
        with open(manifest, encoding="utf-8") as file:
            pinned = json.load(file)
        pinned["parts"] = parts
        with open(manifest, "w", encoding="utf-8") as file:
            json.dump(pinned, file)
        reader = providers.LlamaReader([sys.executable, STAND_IN], link, manifest,
                                       os.path.join(self.directory, "mmproj.gguf"),
                                       os.path.join(self.directory, "mmproj.gguf.json"),
                                       os.path.join(self.directory, "llama.json"), threads=2)
        self.addCleanup(reader.release)
        reader.read(b"jpeg", "text", None, reader.run_options())
        with open(self.record, encoding="utf-8") as file:
            argv = json.load(file)["argv"]
        self.assertEqual(argv[argv.index("-m") + 1], os.path.join(os.path.realpath(store), parts[0]["file"]))
        reader.release()
        # A changed second part is caught before the next start.
        with open(os.path.join(store, parts[1]["file"]), "wb") as file:
            file.write(b"second HALF")
        reader = providers.LlamaReader([sys.executable, STAND_IN], link, manifest,
                                       os.path.join(self.directory, "mmproj.gguf"),
                                       os.path.join(self.directory, "mmproj.gguf.json"),
                                       os.path.join(self.directory, "llama.json"), threads=2)
        self.addCleanup(reader.release)
        with self.assertRaises(providers.Unavailable):
            reader.read(b"jpeg", "text", None, reader.run_options())

    def test_a_part_that_is_not_a_plain_file_name_is_refused(self):
        with self.assertRaises(providers.Unavailable):
            providers.model_parts("/x/model.gguf", {"parts": [{"file": "../etc/passwd"}]})

    def test_the_server_runs_until_released(self):
        reader = self.reader()
        reader.read(b"jpeg", "text", None, reader.run_options())
        process, directory = reader._process, reader._directory
        reader.read(b"jpeg", "text", None, reader.run_options())
        self.assertIs(reader._process, process)
        reader.release()
        self.assertIsNotNone(process.poll())
        self.assertFalse(os.path.exists(directory))
        # The next reading starts it again.
        self.assertEqual(reader.read(b"jpeg", "text", None, reader.run_options())["finish"], "stop")

    def test_hanging_up_while_the_image_is_encoded_stops_the_server(self):
        # llama-server would evaluate the whole prompt before it noticed.
        self.environ["FAKE_LLAMA_DELAY"] = "30"
        reader = self.reader()
        reader._start()
        process = reader._process
        run = reader.run_options()
        threading.Timer(0.5, setattr, (run, "terminate", True)).start()
        started = time.monotonic()
        with self.assertRaises(providers.Cancelled):
            reader.read(b"jpeg", "text", None, run)
        self.assertLess(time.monotonic() - started, 15)
        self.assertIsNotNone(process.poll())
        self.assertIsNone(reader._process)

    def test_hanging_up_while_generating_keeps_the_server(self):
        # llama-server stops generating when it next writes a token.
        self.environ["FAKE_LLAMA_REPEAT"] = "200"
        self.environ["FAKE_LLAMA_PIECE_DELAY"] = "0.05"
        reader = self.reader()
        run = reader.run_options()
        threading.Timer(1, setattr, (run, "terminate", True)).start()
        started = time.monotonic()
        with self.assertRaises(providers.Cancelled):
            reader.read(b"jpeg", "text", None, run)
        self.assertLess(time.monotonic() - started, 5)
        self.assertIsNone(reader._process.poll())

    def test_describes_a_card_with_the_prototypes_settings(self):
        self.environ["FAKE_LLAMA_ANSWERS"] = json.dumps([json.dumps(ANSWER, ensure_ascii=False)])
        reader = self.reader()
        frames = [b"one", b"two", b"three"]
        answer = reader.describe(frames, [0, 2.4, 5], [1, 2, 3], 6, reader.run_options())
        self.assertEqual(answer["summary"], "两人走向雪山")
        self.assertEqual([beat["frames"] for beat in answer["beats"]], [[1, 1], [2, 2], [3, 3]])
        with open(self.record, encoding="utf-8") as file:
            body = json.load(file)["body"]
        system, user = body["messages"]
        self.assertEqual(system, {"role": "system", "content": describe.SYSTEM_PROMPT})
        parts = user["content"]
        content, schema, _ = describe.request([0, 2.4, 5], [1, 2, 3], 6)
        self.assertEqual(len(parts), len(content))
        self.assertEqual(parts[0]["text"], content[0][1])
        # Each key frame follows its label, in order.
        images = [part["image_url"]["url"] for part in parts if part["type"] == "image_url"]
        self.assertEqual(images, ["data:image/jpeg;base64," + base64.b64encode(frame).decode() for frame in frames])
        self.assertEqual(parts[1]["text"], "第 1 张（第 1 个节拍，第 0 秒）")
        self.assertEqual((body["temperature"], body["repeat_penalty"], body["max_tokens"], body["cache_prompt"]),
                         (0.2, 1.1, 1800, True))
        self.assertEqual(body["response_format"], {"type": "json_schema", "json_schema": {"name": "answer", "schema": schema}})
        self.assertEqual(body["chat_template_kwargs"], {"enable_thinking": False})

    def described(self, count, size, beats):
        """The answer and the request of a card of count key frames of size, which the model answers with beats."""
        answer = {**ANSWER, "beats": [{"shot_size": "远景", "what": f"画面 {n + 1}"} for n in range(beats)]}
        self.environ["FAKE_LLAMA_ANSWERS"] = json.dumps([json.dumps(answer, ensure_ascii=False)])
        reader = self.reader()
        frames = [b"frame %d" % k for k in range(count)]
        with mock.patch.object(providers, "frame_size", return_value=size):
            answer = reader.describe(frames, [2.0 * k for k in range(count)], list(range(1, count + 1)), 2.0 * count,
                                     reader.run_options())
        with open(self.record, encoding="utf-8") as file:
            body = json.load(file)["body"]
        parts = body["messages"][1]["content"]
        sent = [frames.index(base64.b64decode(part["image_url"]["url"].split(",", 1)[1]))
                for part in parts if part["type"] == "image_url"]
        return answer, body, parts, sent

    def test_a_card_with_more_key_frames_than_fit_is_described_from_some_of_them(self):
        # A video filmed upright: about 487 tokens a key frame beside the
        # prompts' 680, so 11 leave the answer its 1,800 of the 8,192.
        answer, body, parts, sent = self.described(14, (512, 910), 11)
        kept = [0, 1, 3, 4, 5, 7, 8, 9, 10, 12, 13]
        self.assertEqual(sent, kept)
        self.assertTrue(parts[0]["text"].startswith("下面 11 张图按时间先后取自同一段视频（共 28 秒）"))
        self.assertEqual(parts[-2]["text"], "第 11 张（第 11 个节拍，第 26 秒）")
        beats = body["response_format"]["json_schema"]["schema"]["properties"]["beats"]
        self.assertEqual((beats["minItems"], beats["maxItems"], body["max_tokens"]), (11, 11, 1800))
        # Each beat names its key frame among the card's fourteen.
        self.assertEqual([beat["frames"] for beat in answer["beats"]], [[k + 1, k + 1] for k in kept])

    def test_a_card_whose_key_frames_fit_is_described_from_all_of_them(self):
        # 11 upright frames, or the protocol's 20 of a video filmed level, 165 tokens each.
        for count, size in ((11, (512, 910)), (20, (512, 288))):
            with self.subTest(count=count, size=size):
                answer, body, _, sent = self.described(count, size, count)
                self.assertEqual(sent, list(range(count)))
                self.assertEqual(body["max_tokens"], 1800)
                self.assertEqual([beat["frames"] for beat in answer["beats"]], [[n, n] for n in range(1, count + 1)])

    def test_key_frames_of_unknown_size_are_all_sent(self):
        # Then the server refuses a prompt past the context, or the check after the answer does.
        self.assertIsNone(providers.frame_size(b"not a picture"))
        _, _, _, sent = self.described(20, None, 20)
        self.assertEqual(sent, list(range(20)))

    @unittest.skipUnless(importlib.util.find_spec("PIL"), "Pillow is in the AI runtime")
    def test_a_key_frames_tokens_are_estimated_from_its_size(self):
        from PIL import Image

        def jpeg(width, height):
            buffer = io.BytesIO()
            Image.new("RGB", (width, height)).save(buffer, "JPEG")
            return buffer.getvalue()

        upright, level = jpeg(512, 910), jpeg(512, 288)
        self.assertEqual(providers.frame_size(upright), (512, 910))
        # The development machine's count for a frame of 512 × 910.
        self.assertEqual(providers.describe_frame_tokens([upright] * 3), 487)
        self.assertEqual(providers.describe_frame_tokens([level] * 3), 167)
        self.assertEqual(providers.describe_frame_tokens([level, upright, level]), 487)
        # No frame takes more than the server's cap for an image.
        self.assertEqual(providers.describe_frame_tokens([jpeg(2048, 2048)] * 3),
                         providers.OCR_IMAGE_TOKENS + providers.DESCRIBE_LABEL_TOKENS)
        self.assertIsNone(providers.describe_frame_tokens([upright, b"not a picture", upright]))

    def test_an_answer_that_does_not_fit_is_asked_again_three_times(self):
        good = json.dumps(ANSWER, ensure_ascii=False)
        self.environ["FAKE_LLAMA_ANSWERS"] = json.dumps(['{"camera": "地面"}', good])
        reader = self.reader()
        answer = reader.describe([b"1", b"2", b"3"], [0, 1, 2], [1, 2, 3], 3, reader.run_options())
        self.assertEqual(answer["scene"], "雪山下")
        with open(self.record, encoding="utf-8") as file:
            self.assertEqual(json.load(file)["body"]["repeat_penalty"], 1.2)

        self.environ["FAKE_LLAMA_ANSWERS"] = json.dumps(["{", "[]", json.dumps({**ANSWER, "camera": "海上"}), good])
        reader = self.reader()
        with self.assertRaises(providers.InvalidResult) as raised:
            reader.describe([b"1", b"2", b"3"], [0, 1, 2], [1, 2, 3], 3, reader.run_options())
        self.assertIn("repeat_penalty=1.3", str(raised.exception))
        with open(self.record, encoding="utf-8") as file:
            self.assertEqual(json.load(file)["body"]["repeat_penalty"], 1.3)

    def test_a_card_that_runs_out_of_context_is_not_reencoded(self):
        self.environ.update(FAKE_LLAMA_FINISH="length", FAKE_LLAMA_PROMPT_TOKENS="7400")
        reader = self.reader()
        with self.assertRaises(providers.InvalidInput):
            reader.describe([b"1", b"2", b"3"], [0, 1, 2], [1, 2, 3], 3, reader.run_options())
        with open(self.record, encoding="utf-8") as file:
            self.assertEqual(json.load(file)["body"]["repeat_penalty"], 1.1)

    def test_a_card_past_the_context_is_refused(self):
        self.environ["FAKE_LLAMA_STATUS"] = "400"
        reader = self.reader()
        with self.assertRaises(providers.InvalidInput):
            reader.describe([b"1", b"2", b"3"], [0, 1, 2], [1, 2, 3], 3, reader.run_options())

    def test_a_loop_ends_the_reading(self):
        self.environ["FAKE_LLAMA_REPEAT"] = "2000"
        reader = self.reader()
        reading = reader.read(b"jpeg", "text", None, reader.run_options())
        self.assertEqual(reading["finish"], "repetition")
        self.assertLess(len(reading["text"]), 2000 * len("回单"))

    def test_an_image_the_server_refuses_is_invalid(self):
        self.environ["FAKE_LLAMA_STATUS"] = "400"
        reader = self.reader()
        with self.assertRaises(providers.InvalidInput):
            reader.read(b"not an image", "text", None, reader.run_options())

    def test_a_server_that_fails_is_tried_again_not_refused(self):
        # A passing fault, such as a 503, must not fail the photo for good,
        # nor may a stream cut short pass for a whole reading.
        for name, value in (("FAKE_LLAMA_STATUS", "503"), ("FAKE_LLAMA_ERROR", "failed to decode"),
                            ("FAKE_LLAMA_NO_FINISH", "1")):
            with self.subTest(name):
                self.environ.update({"FAKE_LLAMA_STATUS": None, "FAKE_LLAMA_ERROR": None,
                                     "FAKE_LLAMA_NO_FINISH": None, name: value})
                reader = self.reader()
                with self.assertRaises(providers.Failed):
                    reader.read(b"jpeg", "text", None, reader.run_options())
                # The server may be broken: the next reading starts a new one.
                self.assertIsNone(reader._process)

    def test_hanging_up_while_the_server_starts_stops_it(self):
        # The Worker answers one request at a time: a search must not wait
        # for the model to load.
        self.environ["FAKE_LLAMA_START_DELAY"] = "30"
        reader = self.reader()
        run = reader.run_options()
        threading.Timer(0.5, setattr, (run, "terminate", True)).start()
        started = time.monotonic()
        with self.assertRaises(providers.Cancelled):
            reader.read(b"jpeg", "text", None, run)
        self.assertLess(time.monotonic() - started, 10)
        self.assertIsNone(reader._process)

    def test_hashing_stops_when_told(self):
        self.reader()  # writes the stand-in model file and its manifest
        path = os.path.join(self.directory, "model.gguf")
        with open(path + ".json", encoding="utf-8") as file:
            manifest = json.load(file)
        with self.assertRaises(providers.Cancelled):
            providers.verify_file(path, manifest, stopped=lambda: True)

    def test_a_file_that_matched_is_not_hashed_again(self):
        cache = os.path.join(self.directory, "cache")
        os.mkdir(cache)
        self.reader(cache_dir=cache).read(b"jpeg", "text", None, providers.ReadingRun())

        def again():
            # The Worker started again with the same files.
            files = [os.path.join(self.directory, name) for name in
                     ("model.gguf", "model.gguf.json", "mmproj.gguf", "mmproj.gguf.json", "llama.json")]
            reader = providers.LlamaReader([sys.executable, STAND_IN], *files, threads=2, cache_dir=cache)
            self.addCleanup(reader.release)
            return reader

        with mock.patch.object(providers, "verify_file", side_effect=AssertionError("hashed again")):
            self.assertEqual(again().read(b"jpeg", "text", None, providers.ReadingRun())["finish"], "stop")
        # A file replaced since is hashed again, and fails.
        path = os.path.join(self.directory, "model.gguf")
        os.replace(path, path + ".old")
        with open(path, "wb") as file:
            file.write(b"LANGUAGE")
        with self.assertRaises(providers.Unavailable):
            again().read(b"jpeg", "text", None, providers.ReadingRun())

    def test_a_mark_that_is_not_text_is_hashed_again(self):
        cache = os.path.join(self.directory, "cache")
        os.mkdir(cache)
        reader = self.reader(cache_dir=cache)
        for name in ("model.gguf.json", "mmproj.gguf.json"):
            with open(os.path.join(self.directory, name), encoding="utf-8") as file:
                sha = json.load(file)["sha256"]
            with open(os.path.join(cache, f"verified-{sha}"), "wb") as file:
                file.write(bytes([0xFF, 0xFE]) + b" not text")
        self.assertEqual(reader.read(b"jpeg", "text", None, reader.run_options())["finish"], "stop")

    def test_files_are_checked_before_the_first_start(self):
        reader = self.reader(sha="0" * 64)
        with self.assertRaises(providers.Unavailable):
            reader.read(b"jpeg", "text", None, reader.run_options())
        self.assertIsNone(reader._process)
        self.assertFalse(os.path.exists(self.record))

    def test_a_stopped_worker_stops_its_server(self):
        # Outside systemd nothing else would end llama-server.
        self.reader()  # writes the stand-in model files and manifests
        server = os.path.join(self.directory, "llama-server")
        with open(server, "w", encoding="utf-8") as file:
            file.write(f'#!/bin/sh\nexec "{sys.executable}" "{STAND_IN}" "$@"\n')
        os.chmod(server, 0o700)
        path = os.path.join(self.directory, "ai.sock")
        files = {name: os.path.join(self.directory, name) for name in ("model.gguf", "mmproj.gguf", "llama.json")}
        worker = subprocess.Popen(
            [sys.executable, "-m", "anas_ai.worker", "--fake", "--socket", path, "--ocr-server", server,
             "--ocr-runtime-manifest", files["llama.json"], "--ocr-model", files["model.gguf"],
             "--ocr-model-manifest", files["model.gguf"] + ".json", "--ocr-mmproj", files["mmproj.gguf"],
             "--ocr-mmproj-manifest", files["mmproj.gguf"] + ".json"],
            cwd=AI, env={**os.environ, "PYTHONPATH": AI}, stderr=subprocess.DEVNULL)
        self.addCleanup(worker.kill)
        deadline = time.monotonic() + 30
        while not os.path.exists(path):
            self.assertLess(time.monotonic(), deadline, "the Worker did not start")
            time.sleep(0.1)
        image = os.path.join(self.directory, "page.jpg")
        with open(image, "wb") as file:
            file.write(b"jpeg")
        with socket.socket(socket.AF_UNIX) as conn, open(image, "rb") as file:
            conn.connect(path)
            payload = json.dumps({"op": "read_text", "kind": "text"}).encode()
            socket.send_fds(conn, [struct.pack(">I", len(payload)) + payload], [file.fileno()])
            with conn.makefile("rb") as reply:
                (length,) = struct.unpack(">I", reply.read(4))
                self.assertTrue(json.loads(reply.read(length))["ok"])
        with open(self.record, encoding="utf-8") as file:
            pid = json.load(file)["pid"]
        worker.terminate()
        self.assertEqual(worker.wait(30), 0)
        with self.assertRaises(ProcessLookupError):
            os.kill(pid, 0)

    def test_a_server_that_does_not_start_is_unavailable(self):
        self.environ["FAKE_LLAMA_EXIT"] = "1"
        reader = self.reader()
        with self.assertRaisesRegex(providers.Unavailable, "exited .*failed to load the model"):
            reader.read(b"jpeg", "text", None, reader.run_options())
        self.assertIsNone(reader._process)


class LoopingTest(unittest.TestCase):
    def test_documents_repeat_a_character_for_a_line_and_are_no_loop(self):
        for text in ("合计 35.50\n" + "-" * 48, "目录\n第一章 起步" + "." * 60, "姓名：" + "_" * 64,
                     "收据\n" + "=" * 100, "单价\n" + "0.00\n" * 8, "-" * 200,
                     # A form's blank rows, as on the evaluation page the first rule cut.
                     "| 项目 | 金额 |\n" + "|  |  |  |  |\n" * 20):
            with self.subTest(text=text[:12]):
                self.assertFalse(providers.looping(text))

    def test_a_unit_repeated_on_and_on_is_a_loop(self):
        for text in ("开头" + "回单" * 100, "开头" + "-" * 600, "开头" + "这是一段重复的话。" * 20,
                     "开头" + "|  |  |  |  |\n" * 40):
            with self.subTest(text=text[:12]):
                self.assertTrue(providers.looping(text))


if __name__ == "__main__":
    unittest.main()
