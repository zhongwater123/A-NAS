"""Starts a member's round of scheduled tasks when one of their Hermes jobs is due.

`hermes serve` does not run Hermes's scheduler: only Hermes Desktop's backend does, with
HERMES_DESKTOP=1, which also hands the agent desktop tools. So every due job runs through
anas-assistant-cron@<account>.service, Hermes's own `hermes cron tick` in the member's sandbox,
whether their assistant is running or not. This loop (anas-assistant-cron-waker.service, root)
looks at each member's job store every INTERVAL seconds and starts that unit when a job is due.
It reads the store only to learn when; what runs and how stays Hermes's.
"""
import json
import os
import re
import subprocess
import time
from datetime import UTC, datetime

DATA = os.environ.get("ANAS_ASSISTANT_DATA", "/srv/a-nas/data/assistant")
TOKENS = "/etc/a-nas-assistant/tokens"
ACCOUNT = re.compile(r"^[a-z_][a-z0-9_-]{0,31}$")
INTERVAL = int(os.environ.get("ANAS_CRON_WAKER_INTERVAL", "30"))
LIMIT = 4 << 20


def accounts():
    """The members with an assistant: one session token each."""
    try:
        names = os.listdir(TOKENS)
    except OSError:
        return []
    return sorted(name[:-4] for name in names if name.endswith(".env") and ACCOUNT.match(name[:-4]))


def due(account, now):
    """Whether a job in the member's store is enabled and due. The store is the member's file,
    so it is opened without following a link at its end and read up to LIMIT bytes."""
    try:
        fd = os.open(f"{DATA}/{account}/hermes/cron/jobs.json", os.O_RDONLY | os.O_NOFOLLOW)
    except OSError:
        return False
    with os.fdopen(fd, "rb") as store:
        raw = store.read(LIMIT + 1)
    if len(raw) > LIMIT:
        return False
    try:
        jobs = json.loads(raw).get("jobs")
    except (ValueError, AttributeError):
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
    return subprocess.run(["/usr/bin/systemctl", "is-active", "--quiet", unit], check=False).returncode == 0


def main():
    while True:
        now = datetime.now(UTC)
        for account in accounts():
            unit = f"anas-assistant-cron@{account}.service"
            if due(account, now) and not running(unit):
                subprocess.run(["/usr/bin/systemctl", "start", "--no-block", unit], check=False)
                print(f"started {unit}", flush=True)
        time.sleep(INTERVAL)


if __name__ == "__main__":
    main()
