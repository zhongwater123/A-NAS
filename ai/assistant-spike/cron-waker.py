"""Starts a member's round of scheduled tasks when one of their Hermes jobs is due.

`hermes serve` does not run Hermes's scheduler: only Hermes Desktop's backend does, with
HERMES_DESKTOP=1, which also hands the agent desktop tools. So every due job runs through
anas-assistant-cron@<account>.service, Hermes's own `hermes cron tick` in the member's sandbox,
whether their assistant is running or not. This loop (anas-assistant-cron-waker.service, root)
looks at each member's job store every INTERVAL seconds and starts that unit when a job is due.
It reads the store only to learn when; what runs and how stays Hermes's.
"""
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


def accounts():
    """The members with an assistant: one session token each."""
    try:
        names = os.listdir(TOKENS)
    except OSError:
        return []
    return sorted(name[:-4] for name in names if name.endswith(".env") and ACCOUNT.match(name[:-4]))


def due(account, now):
    """Whether a job in the member's store is enabled and due. The store is the member's file,
    so only a regular file is read, without following its final link or blocking
    while opening a FIFO, and at most LIMIT bytes are parsed."""
    try:
        fd = os.open(f"{DATA}/{account}/hermes/cron/jobs.json", os.O_RDONLY | os.O_NOFOLLOW | os.O_NONBLOCK)
        try:
            info = os.fstat(fd)
            if not stat.S_ISREG(info.st_mode) or info.st_size > LIMIT:
                return False
            with os.fdopen(fd, "rb", closefd=False) as store:
                raw = store.read(LIMIT + 1)
        finally:
            os.close(fd)
        if len(raw) > LIMIT:
            return False
        jobs = json.loads(raw).get("jobs")
    except (OSError, ValueError, AttributeError, RecursionError):
        return False
    for job in jobs if isinstance(jobs, list) else []:
        if not isinstance(job, dict) or not job.get("enabled", True) or job.get("state") == "paused":
            continue
        try:
            when = datetime.fromisoformat(str(job.get("next_run_at")))
        except ValueError:
            continue
        if when.tzinfo is None:
            when = when.replace(tzinfo=UTC)
        if when <= now:
            return True
    return False


def running(unit):
    return subprocess.run(
        ["systemctl", "is-active", "--quiet", unit], check=False, timeout=COMMAND_TIMEOUT,
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
    if due(account, datetime.now(UTC)) and not running(unit) and active_account(account):
        subprocess.run(
            ["systemctl", "start", "--no-block", unit], check=True, timeout=COMMAND_TIMEOUT,
        )
        print(f"started {unit}", flush=True)


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
