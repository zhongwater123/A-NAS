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

SOURCE = Path(__file__).resolve().parents[1] / "assistant" / "cron-waker.py"
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
            "m.DATA=sys.argv[2]; assert m.due('alice', datetime.now(UTC)) == []"
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
                self.assertEqual(bool(waker.due("alice", self.now)), expected)

    def test_a_due_job_is_marked_by_the_instant_it_is_due_for(self):
        def marks(*jobs):
            self.path.write_text(json.dumps({"jobs": list(jobs)}))
            return waker.due("alice", self.now)

        first = {"id": "a1", "next_run_at": "2026-10-26T00:00:00Z"}
        other = {"id": "b2", "next_run_at": "2026-10-26T00:00:00Z"}
        self.assertEqual(marks(first), marks(dict(first, last_status="error")))
        self.assertNotEqual(marks(first), marks(dict(first, next_run_at="2026-10-26T00:01:00Z")))
        self.assertNotEqual(marks(first), marks(other))
        self.assertEqual(len(marks(first, other)), 2)
        many = [{"id": f"job{n}", "next_run_at": "2026-10-26T00:00:00Z"} for n in range(waker.MARKS + 50)]
        self.assertEqual(len(marks(*many)), waker.MARKS)


class WakeTests(unittest.TestCase):
    def setUp(self):
        # Root's notes on the rounds it started go to a directory of the test's own.
        temporary = tempfile.TemporaryDirectory()
        self.addCleanup(temporary.cleanup)
        state = patch.object(waker, "STATE", temporary.name)
        state.start()
        self.addCleanup(state.stop)
        # And nobody asked to run a task now, unless a test says so.
        self.runs = Path(temporary.name) / "runs"
        runs = patch.object(waker, "RUNS", str(self.runs))
        runs.start()
        self.addCleanup(runs.stop)

    def test_activity_is_verified_over_the_host_agents_unix_socket(self):
        for status, name, expected in ((200, "alice", True), (200, "bob", False), (401, "alice", False), (404, "alice", None), (503, "alice", None)):
            with self.subTest(status=status, name=name), tempfile.TemporaryDirectory() as root:
                requests = []

                class Handler(BaseHTTPRequestHandler):
                    def do_POST(self, request_log=requests, response_status=status, response_name=name):
                        request_log.append((self.path, json.loads(self.rfile.read(int(self.headers["Content-Length"])))))
                        self.send_response(response_status)
                        self.end_headers()
                        self.wfile.write(json.dumps({"username": response_name}).encode())

                    def log_message(self, *_):
                        pass

                path = str(Path(root) / "host.sock")
                with socketserver.UnixStreamServer(path, Handler) as server:
                    thread = threading.Thread(target=server.serve_forever, daemon=True)
                    thread.start()
                    try:
                        with patch.object(waker, "HOST_SOCKET", path), patch.object(waker.pwd, "getpwnam") as account:
                            account.return_value.pw_uid = 20100
                            if expected is None:
                                with self.assertRaises(RuntimeError):
                                    waker.active_account("alice")
                            else:
                                self.assertEqual(waker.active_account("alice"), expected)
                        self.assertEqual(requests, [("/v1/accounts/resolve-uid", {"uid": 20100})])
                    finally:
                        server.shutdown()
                        thread.join()

    def test_disabled_account_is_never_started(self):
        with patch.object(waker, "due", return_value=["a"]), \
             patch.object(waker, "running", return_value=False), \
             patch.object(waker, "active_account", return_value=False), \
             patch.object(waker.subprocess, "run") as run:
            waker.wake("alice")
        run.assert_not_called()
        self.assertEqual(os.listdir(waker.STATE), [])

    def test_active_check_precedes_start(self):
        order = []
        with patch.object(waker, "due", return_value=["a"]), \
             patch.object(waker, "running", return_value=False), \
             patch.object(waker, "active_account", side_effect=lambda _: order.append("check") or True), \
             patch.object(waker.subprocess, "run", side_effect=lambda *a, **k: order.append("start")) as run:
            waker.wake("alice")
        self.assertEqual(order, ["check", "start"])
        self.assertEqual(run.call_args.args[0], ["/usr/bin/systemctl", "start", "--no-block", "anas-assistant-cron@alice.service"])

    def starts(self, marks, at):
        """Whether a look at the member's store at second `at`, with these jobs due, starts a round."""
        with patch.object(waker, "due", return_value=marks), \
             patch.object(waker, "running", return_value=False), \
             patch.object(waker, "active_account", return_value=True), \
             patch.object(waker.time, "time", return_value=1_000_000.0 + at), \
             patch.object(waker.subprocess, "run") as run:
            waker.wake("alice")
        return run.called

    def test_a_round_that_moved_no_due_job_holds_the_next_one_back_longer_each_time(self):
        self.assertTrue(self.starts(["a"], 0))
        self.assertFalse(self.starts(["a"], waker.HOLD - 1))
        self.assertTrue(self.starts(["a"], waker.HOLD))
        # The second round moved it no more than the first: twice as long now.
        self.assertFalse(self.starts(["a"], waker.HOLD + 2 * waker.HOLD - 1))
        self.assertTrue(self.starts(["a"], waker.HOLD + 2 * waker.HOLD))

    def test_the_hold_never_exceeds_its_limit(self):
        at = 0
        for _ in range(12):
            self.assertTrue(self.starts(["a"], at))
            at += waker.HOLD_MAX
        self.assertFalse(self.starts(["a"], at - 1))
        self.assertTrue(self.starts(["a"], at))

    def test_a_job_that_is_newly_due_or_moved_starts_a_round_at_once(self):
        self.assertTrue(self.starts(["a"], 0))
        # Another job came due beside the one that did not move.
        self.assertTrue(self.starts(["a", "b"], 30))
        # The round ran b; a is still there and is held again.
        self.assertFalse(self.starts(["a"], 60))
        # a ran and is due for its next instant: another mark.
        self.assertTrue(self.starts(["a-next"], 90))
        self.assertTrue(self.starts(["a-next-again"], 120))

    def test_a_clock_that_went_back_or_damaged_notes_hold_nothing(self):
        self.assertTrue(self.starts(["a"], 0))
        self.assertTrue(self.starts(["a"], -3600))
        Path(waker.STATE, "alice").write_text("{")
        self.assertTrue(self.starts(["a"], -3599))

    def test_a_task_asked_to_run_now_starts_a_round_with_nothing_due(self):
        self.assertFalse(self.starts([], 0))
        (self.runs / "alice").mkdir(parents=True)
        self.assertFalse(self.starts([], 1))
        (self.runs / "alice" / "a1b2c3d4e5f6").touch()
        self.assertTrue(self.starts([], 2))
        # Another member's request is not hers.
        (self.runs / "alice" / "a1b2c3d4e5f6").unlink()
        (self.runs / "bob").mkdir()
        (self.runs / "bob" / "a1b2c3d4e5f6").touch()
        self.assertFalse(self.starts([], 3))

    def test_a_task_asked_to_run_now_is_not_held_back_by_jobs_that_did_not_move(self):
        self.assertTrue(self.starts(["a"], 0))
        self.assertFalse(self.starts(["a"], 30))
        (self.runs / "alice").mkdir(parents=True)
        (self.runs / "alice" / "a1b2c3d4e5f6").touch()
        self.assertTrue(self.starts(["a"], 31))

    def test_a_round_under_way_is_seen_as_running(self):
        # systemctl is-active prints the state; its exit status is 0 for
        # "active" alone, which a oneshot round never is.
        for state, code, expected in [
            ("activating", 3, True), ("deactivating", 3, True), ("active", 0, True),
            ("inactive", 3, False), ("failed", 3, False), ("", 4, False),
        ]:
            with self.subTest(state=state):
                done = subprocess.CompletedProcess([], code, stdout=state + "\n", stderr="")
                with patch.object(waker.subprocess, "run", return_value=done) as run:
                    self.assertEqual(waker.running("anas-assistant-cron@alice.service"), expected)
                self.assertEqual(run.call_args.args[0], ["/usr/bin/systemctl", "is-active", "anas-assistant-cron@alice.service"])

    def test_nothing_is_started_or_noted_while_a_round_runs(self):
        (self.runs / "alice").mkdir(parents=True)
        (self.runs / "alice" / "a1b2c3d4e5f6").touch()
        with patch.object(waker, "due", return_value=["a"]), \
             patch.object(waker, "running", return_value=True), \
             patch.object(waker, "active_account", return_value=True) as active, \
             patch.object(waker.subprocess, "run") as run:
            waker.wake("alice")
        run.assert_not_called()
        active.assert_not_called()
        # The round's own note stands: the request waits for the next round,
        # and the jobs that are due keep the wait they had.
        self.assertEqual(os.listdir(waker.STATE), ["runs"])

    def test_a_round_starts_even_when_it_cannot_be_noted(self):
        blocked = Path(waker.STATE) / "a-file"
        blocked.write_text("")
        with patch.object(waker, "STATE", str(blocked)):
            self.assertTrue(self.starts(["a"], 0))
            self.assertTrue(self.starts(["a"], 1))

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


class PlanningTests(unittest.TestCase):
    """The montage center's planning rounds (docs/architecture/montage-center.md, 发起与结束)."""

    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory()
        self.addCleanup(self.temporary.cleanup)
        root = Path(self.temporary.name)
        paths = {"MARKERS": root / "markers", "GRANTS": root / "grants", "PLAN_RUN": root / "run",
                 "PLAN_STATE": root / "state/plans.json", "TOKENS": root / "tokens"}
        for name, path in paths.items():
            self.addCleanup(setattr, waker, name, getattr(waker, name))
            setattr(waker, name, str(path))
        for path in ("markers", "grants", "tokens"):
            (root / path).mkdir()
        self.root = root
        self.now = 1_800_000_000.0
        self.units = {}  # unit: (running, invocation)
        self.started = []

        def start(command, **_kwargs):
            unit = command[-1]
            self.started.append(unit)
            self.units[unit] = (True, f"inv-{len(self.started)}")
            return subprocess.CompletedProcess(command, 0)

        patches = [patch.object(waker, "unit_state", lambda unit: self.units.get(unit, (False, ""))),
                   patch.object(waker, "running", lambda unit: self.units.get(unit, (False, ""))[0]),
                   patch.object(waker, "active_account", lambda account: account != "carol"),
                   patch.object(waker.subprocess, "run", start),
                   patch.object(waker.time, "time", lambda: self.now)]
        for p in patches:
            p.start()
            self.addCleanup(p.stop)

    def member(self, account, *, issued=None, rounds=5, marker_age=0):
        (self.root / "tokens" / f"{account}.env").write_text("x")
        issued = issued if issued is not None else self.now - 60
        stamp = datetime.fromtimestamp(issued, UTC).isoformat()
        (self.root / "grants" / account).write_text(json.dumps({"issuedAt": stamp, "rounds": rounds}))
        marker = self.root / "markers" / account
        marker.write_text("")
        os.utime(marker, (self.now - marker_age, self.now - marker_age))

    def end_round(self, account, *, took_token=True, claimed=True):
        unit = f"anas-assistant-cron@{account}.service"
        self.units[unit] = (False, "")
        if took_token:
            (self.root / "run" / account).unlink(missing_ok=True)
        if claimed:
            (self.root / "markers" / account).unlink(missing_ok=True)

    def test_one_planning_round_at_a_time_for_the_member_waiting_longest(self):
        self.member("alice", marker_age=10)
        self.member("bob", marker_age=60)
        waker.plan_cycle()
        self.assertEqual(self.started, ["anas-assistant-cron@bob.service"])
        self.assertTrue((self.root / "run" / "bob").exists())
        waker.plan_cycle()
        self.assertEqual(len(self.started), 1, "a second planning round while one runs")
        self.end_round("bob")
        waker.plan_cycle()
        self.assertEqual(self.started[-1], "anas-assistant-cron@alice.service")

    def test_without_a_grant_rounds_left_or_an_active_account_no_unit_starts(self):
        self.member("alice", issued=self.now - 8 * 24 * 3600)
        self.member("bob", rounds=0)
        self.member("carol")
        (self.root / "tokens" / "dave.env").unlink(missing_ok=True)
        (self.root / "markers" / "dave").write_text("")
        waker.plan_cycle()
        self.assertEqual(self.started, [])

    def test_a_grant_gives_five_rounds_and_a_token_not_taken_is_given_back(self):
        self.member("alice", rounds=2)
        waker.plan_cycle()
        # The unit ran without taking the token: the round is given back.
        self.end_round("alice", took_token=False, claimed=False)
        self.now += waker.START_GRACE + 1
        waker.plan_cycle()
        state = json.loads(Path(waker.PLAN_STATE).read_text())
        self.assertEqual(sum(state["counts"].values()), 0)
        self.assertIn("alice", state["backoff"])
        # After the backoff: two rounds taken and claimed, then no more.
        for _ in range(2):
            self.now += 3600
            (self.root / "markers" / "alice").write_text("")
            waker.plan_cycle()
            self.end_round("alice")
            self.now += waker.START_GRACE + 1
            waker.plan_cycle()  # settles the round: claimed, so no backoff
        self.assertEqual(len(self.started), 3)
        self.now += 3600
        (self.root / "markers" / "alice").write_text("")
        waker.plan_cycle()
        self.assertEqual(len(self.started), 3, "a third round on a grant of two")

    def test_the_members_own_run_under_another_invocation_frees_the_slot(self):
        self.member("alice")
        self.member("bob", marker_age=5)
        waker.plan_cycle()
        first = self.started[-1]
        waker.plan_cycle()  # records the invocation
        self.units[first] = (True, "another-invocation")
        self.end_round(first.split("@")[1].split(".")[0], claimed=True)
        self.units[first] = (True, "another-invocation")
        waker.plan_cycle()
        self.assertEqual(len(self.started), 2)
