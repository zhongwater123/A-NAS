"""Tests of LlamaReader against a stand-in llama-server (standard library only)."""

import base64
import hashlib
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

from anas_ai import providers

STAND_IN = os.path.join(os.path.dirname(os.path.abspath(__file__)), "fake_llama_server.py")
AI = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))


class LlamaReaderTest(unittest.TestCase):
    def setUp(self):
        self.directory = tempfile.mkdtemp()
        self.addCleanup(shutil.rmtree, self.directory, True)
        self.record = os.path.join(self.directory, "request.json")
        self.environ = {"FAKE_LLAMA_RECORD": self.record}
        for name in ("FAKE_LLAMA_DELAY", "FAKE_LLAMA_STATUS", "FAKE_LLAMA_REPEAT", "FAKE_LLAMA_PIECE_DELAY",
                     "FAKE_LLAMA_EXIT"):
            self.environ[name] = None

    def reader(self, sha=None):
        for name, value in self.environ.items():
            if value is None:
                os.environ.pop(name, None)
            else:
                os.environ[name] = value
        self.addCleanup(self.restore_environ, dict(os.environ))
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
        reader = providers.LlamaReader([sys.executable, STAND_IN], *files, runtime, threads=2)
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


if __name__ == "__main__":
    unittest.main()
