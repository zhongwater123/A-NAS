"""The root scheduler must survive member-controlled job files."""

import importlib.util
import json
import os
import socketserver
import subprocess
import sys
import tempfile
import threading
import unittest
from datetime import UTC, datetime
from http.server import BaseHTTPRequestHandler
from pathlib import Path
from unittest.mock import patch

SOURCE = Path(__file__).resolve().parents[1] / "assistant-spike" / "cron-waker.py"
SPEC = importlib.util.spec_from_file_location("cron_waker", SOURCE)
waker = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(waker)


class JobFileTests(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory()
        self.addCleanup(self.temporary.cleanup)
        self.previous_data = waker.DATA
        self.addCleanup(setattr, waker, "DATA", self.previous_data)
        waker.DATA = self.temporary.name
        self.path = Path(waker.DATA) / "alice/hermes/cron/jobs.json"
        self.path.parent.mkdir(parents=True)
        self.now = datetime(2026, 10, 27, tzinfo=UTC)

    def test_fifo_does_not_block(self):
        os.mkfifo(self.path)
        script = (
            "import importlib.util, sys; from datetime import UTC, datetime; "
            "s=importlib.util.spec_from_file_location('waker', sys.argv[1]); "
            "m=importlib.util.module_from_spec(s); s.loader.exec_module(m); "
            "m.DATA=sys.argv[2]; assert m.due('alice', datetime.now(UTC)) is False"
        )
        try:
            result = subprocess.run(
                [sys.executable, "-c", script, str(SOURCE), waker.DATA],
                capture_output=True, text=True, timeout=2, check=False,
            )
        except subprocess.TimeoutExpired:
            self.fail("a member's FIFO blocked the account loop")
        self.assertEqual(result.returncode, 0, result.stderr)

    def test_directory_is_rejected(self):
        self.path.mkdir()
        self.assertFalse(waker.due("alice", self.now))

    def test_oversized_and_malformed_documents_are_rejected(self):
        for document in (b" " * (waker.LIMIT + 1), b"{", b"[]", b"[" * 100000 + b"0" + b"]" * 100000):
            with self.subTest(size=len(document)):
                self.path.write_bytes(document)
                self.assertFalse(waker.due("alice", self.now))

    def test_symlink_is_rejected(self):
        target = self.path.with_name("other.json")
        target.write_text('{"jobs":[{"next_run_at":"2026-10-26T00:00:00Z"}]}')
        self.path.symlink_to(target)
        self.assertFalse(waker.due("alice", self.now))

    def test_only_enabled_due_jobs_trigger_a_wake(self):
        for fields, expected in (
            ('"next_run_at":"2026-10-26T00:00:00Z"', True),
            ('"next_run_at":"2026-10-28T00:00:00Z"', False),
            ('"next_run_at":"2026-10-26T00:00:00Z","enabled":false', False),
            ('"next_run_at":"2026-10-26T00:00:00Z","state":"paused"', False),
        ):
            with self.subTest(fields=fields):
                self.path.write_text('{"jobs":[{' + fields + '}]}')
                self.assertEqual(waker.due("alice", self.now), expected)


class WakeTests(unittest.TestCase):
    def test_activity_is_verified_over_the_host_agents_unix_socket(self):
        for status, name, expected in ((200, "alice", True), (200, "bob", False), (401, "alice", False), (503, "alice", False)):
            with self.subTest(status=status, name=name), tempfile.TemporaryDirectory() as root:
                requests = []

                class Handler(BaseHTTPRequestHandler):
                    def do_POST(self):
                        requests.append((self.path, json.loads(self.rfile.read(int(self.headers["Content-Length"])))))
                        self.send_response(status)
                        self.end_headers()
                        self.wfile.write(json.dumps({"username": name}).encode())

                    def log_message(self, *_):
                        pass

                path = str(Path(root) / "host.sock")
                with socketserver.UnixStreamServer(path, Handler) as server:
                    thread = threading.Thread(target=server.serve_forever, daemon=True)
                    thread.start()
                    try:
                        with patch.object(waker, "HOST_SOCKET", path), patch.object(waker.pwd, "getpwnam") as account:
                            account.return_value.pw_uid = 20100
                            self.assertEqual(waker.active_account("alice"), expected)
                        self.assertEqual(requests, [("/v1/accounts/resolve-uid", {"uid": 20100})])
                    finally:
                        server.shutdown()
                        thread.join()

    def test_disabled_account_is_never_started(self):
        with patch.object(waker, "due", return_value=True), \
             patch.object(waker, "running", return_value=False), \
             patch.object(waker, "active_account", return_value=False), \
             patch.object(waker.subprocess, "run") as run:
            waker.wake("alice")
        run.assert_not_called()

    def test_active_check_precedes_start(self):
        order = []
        with patch.object(waker, "due", return_value=True), \
             patch.object(waker, "running", return_value=False), \
             patch.object(waker, "active_account", side_effect=lambda _: order.append("check") or True), \
             patch.object(waker.subprocess, "run", side_effect=lambda *a, **k: order.append("start")) as run:
            waker.wake("alice")
        self.assertEqual(order, ["check", "start"])
        self.assertEqual(run.call_args.args[0], ["systemctl", "start", "--no-block", "anas-assistant-cron@alice.service"])

    def test_an_account_failure_or_timeout_does_not_skip_the_next_account(self):
        for error in (RuntimeError("bad job"), subprocess.TimeoutExpired("worker", 10)):
            with self.subTest(error=type(error).__name__), \
                 patch.object(waker, "accounts", return_value=["alice", "bob"]), \
                 patch.object(waker.subprocess, "run", side_effect=[error, None]) as run:
                waker.run_cycle()
                self.assertEqual([call.args[0][-1] for call in run.call_args_list], ["alice", "bob"])
                self.assertTrue(all(call.kwargs["timeout"] == waker.ACCOUNT_TIMEOUT for call in run.call_args_list))


if __name__ == "__main__":
    unittest.main()
