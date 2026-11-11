"""One round of a member's scheduled tasks, inside their sandbox (anas-assistant-cron@.service).

Hermes runs agent jobs only inside its gateway, and `hermes cron tick` never starts one: without
a gateway it skips the jobs. So a round is: start the gateway, wait until it serves this Hermes
home, hand it the jobs that are due (`hermes cron tick`, which returns before they finish), wait
until none is running, and stop the gateway with Hermes's own `hermes gateway stop`. Stopped
while a job runs, the gateway would leave that run recorded as failed.

Whether a run is live is read from Hermes's execution ledger and whether the gateway is ready
from its own discovery, both internal to Hermes: an upgrade that moves them fails this round
loudly (docs/research/hermes-agent-runtime.md, "以后怎么升级").
"""

import os
import subprocess
import sys
import time

from cron.executions import live_executions
from cron.jobs import load_jobs
from hermes_cli.gateway_runtime import discover_gateway_endpoint
from hermes_constants import get_hermes_home

HERMES = os.path.join(os.path.dirname(sys.executable), "hermes")
READY = 180  # seconds for the gateway to serve the home; about 8 on the development machine
QUIET = 5  # seconds without a live run before the round ends: a hand-over shows within one
ROUND = 55 * 60  # the unit's own limit is an hour; Hermes's watchdog ends a job that stalls
STOP = 120


def say(text):
    print(f"cron round: {text}", flush=True)


def live():
    return len(live_executions([str(job["id"]) for job in load_jobs()]))


def wait_ready(gateway, home):
    deadline = time.monotonic() + READY
    while True:
        found = discover_gateway_endpoint(home, timeout=2.0)
        if found.state == "ready":
            return
        if gateway.poll() is not None:
            sys.exit(f"cron round: the gateway ended with {gateway.returncode} before it was ready")
        if time.monotonic() > deadline:
            sys.exit(f"cron round: the gateway is {found.state} ({found.reason_code}) after {READY} s")
        time.sleep(0.5)


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


def main():
    home = get_hermes_home().resolve()
    started = time.monotonic()
    gateway = subprocess.Popen([HERMES, "gateway", "run", "--quiet"])
    status, asked = 1, False
    try:
        wait_ready(gateway, home)
        say(f"the gateway is ready after {time.monotonic() - started:.0f} s")
        tick = subprocess.run([HERMES, "cron", "tick"], check=False).returncode
        idle, stopped = wait_idle(started + ROUND), False
        # Hermes refuses to stop over live work: a job that came due meanwhile runs first.
        for _ in range(3):
            if not idle:
                break
            asked = True
            if subprocess.run([HERMES, "gateway", "stop"], check=False, timeout=STOP).returncode == 0:
                stopped = True
                break
            idle = wait_idle(started + ROUND)
        if not stopped:
            say(
                "the round's time is up with jobs still running; stopping them"
                if not idle
                else "hermes gateway stop was refused three times; stopping over what runs"
            )
            asked = True
            subprocess.run([HERMES, "gateway", "stop", "--yes"], check=False, timeout=STOP)
        status = 0 if tick == 0 and stopped else 1
    finally:
        if gateway.poll() is None:
            if not asked:
                gateway.terminate()
            try:
                gateway.wait(timeout=STOP)
            except subprocess.TimeoutExpired:
                gateway.kill()
                gateway.wait()
    say(f"ended after {time.monotonic() - started:.0f} s, the gateway with {gateway.returncode}")
    return status


if __name__ == "__main__":
    sys.exit(main())
