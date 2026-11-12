"""Starts a member's round of scheduled tasks when one of their Hermes jobs is due.

`hermes serve` does not run Hermes's scheduler: only Hermes Desktop's backend does, with
HERMES_DESKTOP=1, which also hands the agent desktop tools. So every due job runs through
anas-assistant-cron@<account>.service, Hermes's gateway for one round in the member's sandbox
(cron_round.py), whether their assistant is running or not. This loop (anas-assistant-cron-waker.service, root)
looks at each member's job store every INTERVAL seconds and starts that unit when a job is due.
It reads the store only to learn when; what runs and how stays Hermes's.

A task the member asked to run now starts a round too. The File Broker starts it itself; a
request that came while a round was running waits (run-requests.sh), and this loop starts the
round that takes it.

A round costs a gateway's start, so a round that left every job it was started for as due as
before holds the next one back: HOLD seconds, doubling up to HOLD_MAX, until a job is newly due
or one of them moves. Such a job is one Hermes does not take up, or one the member's own
processes wrote into the store, which is the member's file.
"""
import hashlib
import http.client
import json
import os
import pwd
import re
import socket
import stat
import subprocess
import sys
import time
from datetime import UTC, datetime

DATA = os.environ.get("ANAS_ASSISTANT_DATA", "/srv/a-nas/data/assistant")
TOKENS = "/etc/a-nas-assistant/tokens"
ACCOUNT = re.compile(r"^[a-z_][a-z0-9_-]{0,31}$")
INTERVAL = int(os.environ.get("ANAS_CRON_WAKER_INTERVAL", "30"))
LIMIT = 4 << 20
ACCOUNT_TIMEOUT = 10
COMMAND_TIMEOUT = 3
HOST_SOCKET = os.environ.get("ANAS_HOST_AGENT_SOCKET", "/run/a-nas/host-agent.sock")
# Root's own notes on the last round started for each member (the unit's RuntimeDirectory).
STATE = os.environ.get("ANAS_CRON_WAKER_STATE", "/run/anas-assistant-cron-waker")
# The tasks members asked to run now that no round has taken yet, the File Broker's notes.
RUNS = os.environ.get("ANAS_ASSISTANT_RUN_DIR", "/run/a-nas/assistant-runs")
HOLD = 120
HOLD_MAX = 3600
MARKS = 256


def accounts():
    """The members with an assistant: one session token each."""
    try:
        names = os.listdir(TOKENS)
    except OSError:
        return []
    return sorted(name[:-4] for name in names if name.endswith(".env") and ACCOUNT.match(name[:-4]))


def due(account, now):
    """The jobs in the member's store that are enabled and due, each as a mark of the job and
    the instant it is due for: a job that ran is due for another instant, or for none. Empty
    when none is due. The store is the member's file, so only a regular file is read, without
    following its final link or blocking while opening a FIFO, and at most LIMIT bytes are
    parsed and MARKS jobs marked."""
    try:
        fd = os.open(f"{DATA}/{account}/hermes/cron/jobs.json", os.O_RDONLY | os.O_NOFOLLOW | os.O_NONBLOCK)
        try:
            info = os.fstat(fd)
            if not stat.S_ISREG(info.st_mode) or info.st_size > LIMIT:
                return []
            with os.fdopen(fd, "rb", closefd=False) as store:
                raw = store.read(LIMIT + 1)
        finally:
            os.close(fd)
        if len(raw) > LIMIT:
            return []
        jobs = json.loads(raw).get("jobs")
    except (OSError, ValueError, AttributeError, RecursionError):
        return []
    marks = set()
    for job in jobs if isinstance(jobs, list) else []:
        if not isinstance(job, dict) or not job.get("enabled", True) or job.get("state") == "paused":
            continue
        try:
            when = datetime.fromisoformat(str(job.get("next_run_at")))
        except ValueError:
            continue
        if when.tzinfo is None:
            when = when.replace(tzinfo=UTC)
        if when <= now and len(marks) < MARKS:
            marks.add(hashlib.sha256(f"{job.get('id')}\0{job.get('next_run_at')}".encode()).hexdigest()[:16])
    return sorted(marks)


def rounds_spent(account, marks, now):
    """How many rounds were already started for exactly these due jobs, none of which moved
    since; None while the next one is held back. 0 when one of them is newly due."""
    try:
        with open(f"{STATE}/{account}", "rb") as notes:
            last = json.loads(notes.read(LIMIT))
        spent, at, before = int(last["rounds"]), float(last["at"]), set(last["marks"])
    except (OSError, ValueError, KeyError, TypeError):
        return 0
    if spent < 1 or not set(marks) <= before:
        return 0
    wait = min(HOLD * 2 ** min(spent - 1, 16), HOLD_MAX)
    # A clock that went back holds nothing.
    return None if 0 <= now - at < wait else spent


def note_round(account, marks, rounds, now):
    """Notes the round just started. Without the notes rounds are not held back, which costs
    time and memory but no run, so a failure here stops nothing."""
    try:
        os.makedirs(STATE, mode=0o700, exist_ok=True)
        with open(f"{STATE}/{account}.new", "w", encoding="utf-8") as notes:
            json.dump({"marks": marks, "rounds": rounds, "at": now}, notes)
        os.replace(f"{STATE}/{account}.new", f"{STATE}/{account}")
    except OSError as error:
        print(f"round of {account} not noted: {type(error).__name__}", file=sys.stderr, flush=True)


def asked(account):
    """Whether a task the member asked to run now still waits for a round."""
    try:
        return bool(os.listdir(f"{RUNS}/{account}"))
    except OSError:
        return False


def running(unit):
    return subprocess.run(
        ["/usr/bin/systemctl", "is-active", "--quiet", unit], check=False, timeout=COMMAND_TIMEOUT,
    ).returncode == 0


class HostConnection(http.client.HTTPConnection):
    """Use the Host Agent's private socket with the standard HTTP client."""

    def connect(self):
        self.sock = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
        self.sock.settimeout(self.timeout)
        self.sock.connect(HOST_SOCKET)


def active_account(account):
    uid = pwd.getpwnam(account).pw_uid
    if not 20100 <= uid <= 29999:
        return False
    connection = HostConnection("host-agent", timeout=COMMAND_TIMEOUT)
    try:
        connection.request(
            "POST", "/v1/accounts/resolve-uid", json.dumps({"uid": uid}),
            {"Content-Type": "application/json"},
        )
        response = connection.getresponse()
        if response.status == 401:
            return False
        if response.status != 200:
            raise RuntimeError(f"Host Agent identity query failed: HTTP {response.status}")
        user = json.loads(response.read(4097))
        return isinstance(user, dict) and user.get("username") == account
    finally:
        connection.close()


def wake(account):
    if not ACCOUNT.fullmatch(account):
        raise ValueError("invalid account")
    unit = f"anas-assistant-cron@{account}.service"
    marks = due(account, datetime.now(UTC))
    waiting = asked(account)
    if not (marks or waiting) or running(unit):
        return
    # A task asked for is run whatever the earlier rounds left unmoved.
    spent = 0 if waiting else rounds_spent(account, marks, time.time())
    if spent is None or not active_account(account):
        return
    subprocess.run(
        ["/usr/bin/systemctl", "start", "--no-block", unit], check=True, timeout=COMMAND_TIMEOUT,
    )
    note_round(account, marks, spent + 1, time.time())
    print(f"started {unit}" + (f" (round {spent + 1} for the same due jobs)" if spent else ""), flush=True)


def run_cycle():
    # A member's file, filesystem or systemctl request cannot indefinitely hold
    # up the others. subprocess.run kills and reaps a timed-out worker.
    for account in accounts():
        try:
            subprocess.run(
                [sys.executable, "-I", os.path.abspath(__file__), "--account", account],
                check=True, timeout=ACCOUNT_TIMEOUT,
            )
        except Exception as error:  # noqa: BLE001 - failure containment between members (#178).
            print(f"skipped {account}: {type(error).__name__}", file=sys.stderr, flush=True)


def main():
    while True:
        run_cycle()
        time.sleep(INTERVAL)


if __name__ == "__main__":
    if len(sys.argv) == 3 and sys.argv[1] in ("--account", "--active"):
        try:
            if sys.argv[1] == "--active":
                sys.exit(0 if active_account(sys.argv[2]) else 1)
            wake(sys.argv[2])
        except Exception as error:  # noqa: BLE001 - this worker handles one untrusted member store.
            print(f"skipped {sys.argv[2]}: {type(error).__name__}", file=sys.stderr, flush=True)
            sys.exit(2)
    elif len(sys.argv) == 1:
        main()
    else:
        sys.exit("usage: cron-waker.py [--account ACCOUNT | --active ACCOUNT]")
