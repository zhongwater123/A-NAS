"""Exercise the actual module entry point and its graceful release (#427)."""

import os
import socket
import struct
import subprocess
import sys
import tempfile
import threading
import time
import unittest
from concurrent.futures import ThreadPoolExecutor

from tests.test_worker import call


class WorkerStartupTest(unittest.TestCase):
    def test_requests_fail_promptly_when_worker_does_not_answer_or_truncates_response(self):
        for response, error in ((None, TimeoutError), (b"\x00\x00", EOFError),
                                (struct.pack(">I", 10) + b"{", EOFError)):
            with self.subTest(response=response), tempfile.TemporaryDirectory() as directory:
                path = os.path.join(directory, "ai.sock")
                finished = threading.Event()
                with socket.socket(socket.AF_UNIX, socket.SOCK_STREAM) as listener:
                    listener.settimeout(2)
                    listener.bind(path)
                    listener.listen(1)

                    def reply(response=response, finished=finished):
                        conn, _ = listener.accept()
                        with conn:
                            conn.settimeout(2)
                            conn.recv(4096)
                            if response is None:
                                finished.wait(2)
                            else:
                                conn.sendall(response)

                    with ThreadPoolExecutor(max_workers=1) as executor:
                        result = executor.submit(reply)
                        try:
                            with self.assertRaises(error):
                                call(path, {"op": "info"}, timeout=0.1 if response is None else 2)
                        finally:
                            finished.set()
                        result.result(timeout=3)

    def test_cli_serves_fake_requests_and_exits_after_release(self):
        with tempfile.TemporaryDirectory() as directory, tempfile.TemporaryFile(mode="w+") as errors:
            path = os.path.join(directory, "ai.sock")
            env = os.environ.copy()
            env.pop("LISTEN_PID", None)
            env.pop("LISTEN_FDS", None)
            with subprocess.Popen([sys.executable, "-m", "anas_ai.worker", "--fake", "--socket", path,
                                   "--idle-seconds", "10"], env=env, stderr=errors,
                                  stdout=subprocess.DEVNULL) as process:
                try:
                    deadline = time.monotonic() + 5
                    while True:
                        try:
                            with socket.socket(socket.AF_UNIX, socket.SOCK_STREAM) as probe:
                                probe.settimeout(1)
                                probe.connect(path)
                            break
                        except (FileNotFoundError, ConnectionRefusedError):
                            if process.poll() is not None or time.monotonic() >= deadline:
                                errors.seek(0)
                                self.fail(f"Worker did not start: {errors.read()}")
                            time.sleep(0.01)
                    info = call(path, {"op": "info"})
                    self.assertTrue(info["ok"])
                    self.assertEqual(info["model"], "fake-sha256-768")
                    self.assertEqual(info["dimensions"], 768)
                    self.assertGreater(info["memoryBytes"], 0)
                    self.assertEqual(call(path, {"op": "release", "what": "all"}), {"ok": True})
                    self.assertEqual(process.wait(timeout=5), 0)
                finally:
                    if process.poll() is None:
                        process.kill()
                    process.wait(timeout=5)
