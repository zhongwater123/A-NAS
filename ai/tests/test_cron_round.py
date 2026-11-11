"""A round of scheduled tasks stops the gateway it started, and only once the due jobs have run.

cron_round.py runs inside the assistant's sandbox with Hermes's own modules; here they, the
clock and the processes are stand-ins, so the round's decisions can be driven: a hand-over that
shows late, a stop Hermes refuses, a gateway that is somebody else's.
"""

import importlib.util
import subprocess
import sys
import types
import unittest
from pathlib import Path

SOURCE = Path(__file__).resolve().parents[1] / "assistant-spike" / "cron_round.py"
PAST = "2020-01-01T00:00:00+00:00"


class _Found:
    def __init__(self, state):
        self.state, self.reason_code = state, None


class _Gateway:
    """The gateway process the round starts."""

    def __init__(self, world):
        self.world, self.returncode, self.signals = world, None, []

    def poll(self):
        return self.returncode

    def wait(self, timeout=None):
        if self.returncode is None:
            if not self.world.ends_when_stopped or not self.world.stop_asked:
                self.world.now += timeout or 0
                raise subprocess.TimeoutExpired("hermes", timeout)
            self.returncode = 0
        return self.returncode

    def terminate(self):
        self.signals.append("terminate")
        self.returncode = -15

    def kill(self):
        self.signals.append("kill")
        self.returncode = -9


class _World:
    """Hermes as the round sees it, on a clock that only moves when the round sleeps or waits."""

    def __init__(self, due=True, live=(), foreign=False, ready_after=3.0):
        self.now = 0.0
        self.jobs = [{"id": "a1", "next_run_at": PAST if due else "2999-01-01T00:00:00+00:00", "last_run_at": None}]
        # live: the spans, in seconds after the tick, in which a run of the job is live.
        self.live, self.foreign, self.ready_after = list(live), foreign, ready_after
        self.ticked, self.calls, self.gateway = None, [], None
        self.stop_answers, self.stop_asked, self.ends_when_stopped = [], False, True
        self.exits_early = False

    # --- what cron_round imports from Hermes
    def load_jobs(self):
        if self.ticked is not None and self.live and self.now - self.ticked >= self.live[-1][1]:
            self.jobs[0]["last_run_at"] = "ran"
        return [dict(job) for job in self.jobs]

    def live_executions(self, _ids):
        if self.ticked is None:
            return {}
        since = self.now - self.ticked
        return {"a1": {}} if any(begin <= since < end for begin, end in self.live) else {}

    def discover(self, _home, timeout=2.0):
        if self.foreign:
            return _Found("ready")
        started = self.gateway is not None and not self.exits_early
        return _Found("ready" if started and self.now >= self.ready_after else "absent")

    # --- processes and the clock
    def popen(self, command):
        self.calls.append((" ".join(command[1:]), self.now))
        self.gateway = _Gateway(self)
        if self.exits_early:
            self.gateway.returncode = 1
        return self.gateway

    def run(self, command, check=False, timeout=None):
        name = " ".join(command[1:])
        self.calls.append((name, self.now))
        code = 0
        if name == "cron tick":
            self.ticked = self.now
        elif name == "gateway stop":
            code = self.stop_answers.pop(0) if self.stop_answers else 0
            self.stop_asked = code == 0
        elif name == "gateway stop --yes":
            self.stop_asked = self.ends_when_stopped = True
        return types.SimpleNamespace(returncode=code)

    def sleep(self, seconds):
        self.now += seconds

    def said(self, name):
        return [at for call, at in self.calls if call == name]


def load(world):
    """cron_round with stand-ins for Hermes's modules, the clock and the processes."""
    stand_ins = {
        "cron": types.ModuleType("cron"),
        "cron.executions": types.ModuleType("cron.executions"),
        "cron.jobs": types.ModuleType("cron.jobs"),
        "hermes_cli": types.ModuleType("hermes_cli"),
        "hermes_cli.gateway_runtime": types.ModuleType("hermes_cli.gateway_runtime"),
        "hermes_constants": types.ModuleType("hermes_constants"),
    }
    stand_ins["cron.executions"].live_executions = world.live_executions
    stand_ins["cron.jobs"].load_jobs = world.load_jobs
    stand_ins["hermes_cli.gateway_runtime"].discover_gateway_endpoint = world.discover
    stand_ins["hermes_constants"].get_hermes_home = lambda: Path("/var/lib/hermes-home")
    saved = {name: sys.modules.get(name) for name in stand_ins}
    sys.modules.update(stand_ins)
    try:
        spec = importlib.util.spec_from_file_location("cron_round_under_test", SOURCE)
        module = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(module)
    finally:
        for name, old in saved.items():
            if old is None:
                sys.modules.pop(name, None)
            else:
                sys.modules[name] = old
    module.time = types.SimpleNamespace(monotonic=lambda: world.now, sleep=world.sleep)
    module.subprocess = types.SimpleNamespace(
        run=world.run, Popen=world.popen, TimeoutExpired=subprocess.TimeoutExpired
    )
    module.say = lambda _text: None
    return module


class CronRoundTests(unittest.TestCase):
    def round(self, **world):
        self.world = _World(**world)
        self.module = load(self.world)
        return self.world

    def test_a_round_starts_the_gateway_hands_over_and_stops_it_after_the_run(self):
        world = self.round(live=[(1, 12)])
        self.assertEqual(self.module.main(), 0)
        self.assertEqual([call for call, _ in world.calls], ["gateway run --quiet", "cron tick", "gateway stop"])
        # Not before the run ended and nothing had been live for QUIET seconds; the last look
        # that found it live was a second before its end.
        self.assertGreaterEqual(world.said("gateway stop")[0] - world.ticked, 11 + self.module.QUIET)
        self.assertEqual(world.gateway.signals, [])

    def test_a_hand_over_that_shows_late_is_waited_for(self):
        world = self.round(live=[(40, 50)])
        self.assertEqual(self.module.main(), 0)
        self.assertGreaterEqual(world.said("gateway stop")[0] - world.ticked, 50)

    def test_a_job_that_is_never_taken_up_holds_the_round_for_the_hand_over_only(self):
        world = self.round(live=[])
        self.assertEqual(self.module.main(), 0)
        waited = world.said("gateway stop")[0] - world.ticked
        self.assertGreaterEqual(waited, self.module.HANDOVER)
        self.assertLess(waited, self.module.HANDOVER + 30)

    def test_with_nothing_due_the_round_ends_at_once(self):
        world = self.round(due=False)
        self.assertEqual(self.module.main(), 0)
        self.assertLess(world.said("gateway stop")[0] - world.ticked, 10)

    def test_a_stop_hermes_refuses_is_asked_again_once_nothing_runs(self):
        world = self.round(live=[(1, 5), (11, 30)])
        world.stop_answers = [1, 0]
        self.assertEqual(self.module.main(), 0)
        first, second = world.said("gateway stop")
        self.assertLess(first - world.ticked, 30)
        self.assertGreaterEqual(second - world.ticked, 29 + self.module.QUIET)
        self.assertEqual(world.said("gateway stop --yes"), [])

    def test_a_stop_that_leaves_the_gateway_running_ends_in_a_stop_over_what_runs(self):
        world = self.round(live=[(1, 5)])
        world.ends_when_stopped = False
        self.assertEqual(self.module.main(), 1)
        self.assertEqual(len(world.said("gateway stop")), 3)
        self.assertEqual(len(world.said("gateway stop --yes")), 1)
        self.assertEqual(world.gateway.signals, [])

    def test_a_gateway_that_is_not_the_rounds_is_used_and_left_running(self):
        world = self.round(live=[(1, 8)], foreign=True)
        self.assertEqual(self.module.main(), 0)
        self.assertEqual([call for call, _ in world.calls], ["cron tick"])
        self.assertIsNone(world.gateway)

    def test_a_gateway_that_ends_before_it_is_ready_fails_the_round(self):
        world = self.round()
        world.exits_early = True
        with self.assertRaises(SystemExit) as raised:
            self.module.main()
        self.assertIn("before it was ready", str(raised.exception))
        self.assertEqual([call for call, _ in world.calls], ["gateway run --quiet"])

    def test_a_gateway_that_never_serves_the_home_is_ended(self):
        world = self.round(ready_after=float("inf"))
        with self.assertRaises(SystemExit):
            self.module.main()
        self.assertEqual(world.gateway.signals, ["terminate"])


if __name__ == "__main__":
    unittest.main()
