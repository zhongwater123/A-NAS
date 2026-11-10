"""Exercise the actual module entry point and its graceful release (#427)."""

import os
import socket
import subprocess
import sys
import tempfile
import time
import unittest

from tests.test_worker import call


class WorkerStartupTest(unittest.TestCase):
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
