"""One round of a member's scheduled tasks, inside their sandbox (anas-assistant-cron@.service).

Hermes runs agent jobs only inside its gateway, and `hermes cron tick` never starts one: without
a gateway it skips the jobs. So a round is: start the gateway, wait until it serves this Hermes
home, hand it the jobs that are due (`hermes cron tick`, which returns before they finish), wait
until they were taken up and none is running, and stop the gateway with Hermes's own `hermes
gateway stop`. Stopped while a job runs, the gateway would leave that run recorded as failed.

A gateway that already serves the home (the API Server channel runs one in the instance) is
used as it is and left running: the round stops only the gateway it started.

Whether a run is live is read from Hermes's execution ledger and whether the gateway is ready
from its own discovery, both internal to Hermes: an upgrade that moves them fails this round
loudly (docs/research/hermes-agent-runtime.md, "以后怎么升级").
"""

import os
import subprocess
import sys
import time
from datetime import UTC, datetime

from cron.executions import live_executions
from cron.jobs import load_jobs
from hermes_cli.gateway_runtime import discover_gateway_endpoint
from hermes_constants import get_hermes_home

HERMES = os.path.join(os.path.dirname(sys.executable), "hermes")
READY = 180  # seconds for the gateway to serve the home; about 8 on the development machine
HANDOVER = 90  # seconds for a job that was due to be taken up once the tick handed it over
QUIET = 5  # seconds without a live run before the round ends
ROUND = 55 * 60  # the unit's own limit is an hour; Hermes's watchdog ends a job that stalls
STOP = 120  # seconds for one `hermes gateway stop`, and for the gateway to end after it
GONE = 20  # seconds for the gateway to end after a stop that said it stopped it


def say(text):
    print(f"cron round: {text}", flush=True)


def live():
    return len(live_executions([str(job["id"]) for job in load_jobs()]))


def due():
    """The jobs that are due now, each with what a run changes: when it is next due and when it last ran."""
    now, marks = datetime.now(UTC), {}
    for job in load_jobs():
        if not job.get("enabled", True) or job.get("state") == "paused":
            continue
        try:
            when = datetime.fromisoformat(str(job.get("next_run_at")))
        except ValueError:
            continue
        if when.tzinfo is None:
            when = when.replace(tzinfo=UTC)
        if when <= now:
            marks[str(job["id"])] = (job.get("next_run_at"), job.get("last_run_at"))
    return marks


def ready(home):
    return discover_gateway_endpoint(home, timeout=2.0)


def wait_ready(gateway, home):
    deadline = time.monotonic() + READY
    while True:
        found = ready(home)
        if found.state == "ready":
            return
        if gateway.poll() is not None:
            sys.exit(f"cron round: the gateway ended with {gateway.returncode} before it was ready")
        if time.monotonic() > deadline:
            sys.exit(f"cron round: the gateway is {found.state} ({found.reason_code}) after {READY} s")
        time.sleep(0.5)


def wait_handover(before, deadline):
    """Until a job that was due is running or has run, for HANDOVER seconds at most: a tick
    returns once it handed the jobs over, and on a busy machine a job shows as live later."""
    if not before:
        return
    limit = min(deadline, time.monotonic() + HANDOVER)
    while time.monotonic() < limit:
        if live():
            return
        now = {str(job["id"]): (job.get("next_run_at"), job.get("last_run_at")) for job in load_jobs()}
        if any(now.get(job) != marks for job, marks in before.items()):
            return
        time.sleep(1)
    say(f"no job that was due was taken up within {HANDOVER} s")


def wait_idle(deadline):
    """Until no run has been live for QUIET seconds; False when the round's time is up."""
    quiet_since = time.monotonic()
    while time.monotonic() < deadline:
        if live():
            quiet_since = time.monotonic()
        elif time.monotonic() - quiet_since >= QUIET:
            return True
        time.sleep(1)
    return False


def ended(gateway, seconds):
    try:
        gateway.wait(timeout=seconds)
    except subprocess.TimeoutExpired:
        return False
    return True


def stop(gateway, deadline):
    """Stops the gateway once nothing runs; False when it had to be stopped over running jobs.
    Hermes refuses to stop over live work, so a job that came due meanwhile runs first; a stop
    that claims success while the gateway goes on is taken for a refusal."""
    idle = wait_idle(deadline)
    for _ in range(3):
        if not idle:
            break
        said = subprocess.run([HERMES, "gateway", "stop"], check=False, timeout=STOP).returncode
        if said == 0 and ended(gateway, GONE):
            return True
        if gateway.poll() is not None:
            return True
        idle = wait_idle(deadline)
    say(
        "the round's time is up with jobs still running; stopping them"
        if not idle
        else "hermes gateway stop did not stop the gateway; stopping over what runs"
    )
    subprocess.run([HERMES, "gateway", "stop", "--yes"], check=False, timeout=STOP)
    return False


def main():
    home = get_hermes_home().resolve()
    started = time.monotonic()
    deadline = started + ROUND
    if ready(home).state == "ready":
        # Somebody else's gateway serves the home: its jobs run there, and it is not this round's to stop.
        say("a gateway already serves this home; handing it the jobs that are due")
        before = due()
        tick = subprocess.run([HERMES, "cron", "tick"], check=False).returncode
        wait_handover(before, deadline)
        idle = wait_idle(deadline)
        say(f"ended after {time.monotonic() - started:.0f} s")
        return 0 if tick == 0 and idle else 1
    gateway = subprocess.Popen([HERMES, "gateway", "run", "--quiet"])
    status, asked = 1, False
    try:
        wait_ready(gateway, home)
        say(f"the gateway is ready after {time.monotonic() - started:.0f} s")
        before = due()
        tick = subprocess.run([HERMES, "cron", "tick"], check=False).returncode
        wait_handover(before, deadline)
        asked = True
        stopped = stop(gateway, deadline)
        status = 0 if tick == 0 and stopped else 1
    finally:
        if gateway.poll() is None:
            if not asked:
                gateway.terminate()
            if not ended(gateway, STOP):
                gateway.kill()
                gateway.wait()
    say(f"ended after {time.monotonic() - started:.0f} s, the gateway with {gateway.returncode}")
    return status


if __name__ == "__main__":
    sys.exit(main())
