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

The montage center's planning (docs/architecture/montage-center.md, 发起与结束) starts rounds
too. The montage service leaves an empty file in MARKERS for each member with a job waiting for
a plan; this loop picks the first member who may plan now (an account in use with an assistant,
a planning grant under seven days old with rounds left, their unit not running, not backing off),
gives the round a token in PLAN_RUN (run-requests.sh moves it into the round, where
montage_round.py claims a round), and starts their unit. One planning round on the whole device
at a time: the next waits until the unit that took the token stops, or runs again under another
invocation. Each grant's rounds are counted in PLAN_STATE, before the token is given; a token
the unit never took is given back. A member whose marker outlived their round backs off.
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
# The Host Agent's instance secrets, one for each member it prepared an assistant for.
TOKENS = os.environ.get("ANAS_ASSISTANT_TOKEN_DIR", "/var/lib/a-nas-host/assistant/tokens")
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
# The montage center's planning markers, its grants, the tokens given to rounds, and the counts.
MARKERS = os.environ.get("ANAS_MONTAGE_PLAN_MARKERS", "/run/a-nas-montage/plans")
GRANTS = os.environ.get("ANAS_ASSISTANT_PLAN_DIR", "/var/lib/a-nas/assistant-plans")
PLAN_RUN = os.environ.get("ANAS_MONTAGE_PLAN_RUN", "/run/anas-assistant-montage-plans")
PLAN_STATE = os.environ.get("ANAS_MONTAGE_PLAN_STATE", "/var/lib/anas-assistant-cron-waker/plans.json")
GRANT_LIFE = 7 * 24 * 3600
PLAN_BACKOFF = 120
PLAN_BACKOFF_MAX = 3600
START_GRACE = 60


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
    """Whether a round of the unit is under way.

    A round is a oneshot unit: activating for as long as it runs and never
    active, so the exit status of is-active, which is 0 for active alone,
    says "not running" throughout the round.
    """
    state = subprocess.run(
        ["/usr/bin/systemctl", "is-active", unit],
        check=False, timeout=COMMAND_TIMEOUT, capture_output=True, text=True,
    ).stdout.strip()
    return state in {"activating", "active", "reloading", "deactivating"}


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


def unit_state(unit):
    """(running, invocation) of a unit: a oneshot round is activating while it runs."""
    shown = subprocess.run(
        ["/usr/bin/systemctl", "show", "-p", "ActiveState", "-p", "InvocationID", unit],
        check=False, timeout=COMMAND_TIMEOUT, capture_output=True, text=True,
    ).stdout
    fields = dict(line.split("=", 1) for line in shown.splitlines() if "=" in line)
    return fields.get("ActiveState") in {"activating", "active", "reloading", "deactivating"}, fields.get("InvocationID", "")


def load_plans():
    try:
        with open(PLAN_STATE, encoding="utf-8") as source:
            state = json.load(source)
    except (OSError, ValueError):
        state = {}
    if not isinstance(state, dict):
        state = {}
    state.setdefault("counts", {})
    state.setdefault("backoff", {})
    state.setdefault("slot", None)
    return state


def save_plans(state):
    os.makedirs(os.path.dirname(PLAN_STATE), mode=0o700, exist_ok=True)
    partial = f"{PLAN_STATE}.{os.getpid()}"
    with open(partial, "w", encoding="utf-8") as out:
        json.dump(state, out)
        out.flush()
        os.fsync(out.fileno())
    os.replace(partial, PLAN_STATE)


def plan_grant(account, now):
    """(issued, rounds) of the member's planning grant while it lasts, or None."""
    try:
        info = os.lstat(f"{GRANTS}/{account}")
        if not stat.S_ISREG(info.st_mode) or info.st_size > 4096:
            return None
        with open(f"{GRANTS}/{account}", encoding="utf-8") as source:
            grant = json.load(source)
        issued = datetime.fromisoformat(grant["issuedAt"]).timestamp()
        rounds = int(grant["rounds"])
    except (OSError, ValueError, KeyError, TypeError):
        return None
    if not 0 <= now - issued <= GRANT_LIFE:
        return None
    return grant["issuedAt"], rounds


def marked():
    """The members with a job waiting for a plan, the longest waiting first."""
    try:
        entries = [(e.stat(follow_symlinks=False).st_mtime, e.name) for e in os.scandir(MARKERS)
                   if ACCOUNT.fullmatch(e.name) and e.is_file(follow_symlinks=False)]
    except OSError:
        return []
    return [name for _, name in sorted(entries)]


def slot_over(state, now):
    """Whether the planning round given out has stopped; settles its token and its count."""
    slot = state["slot"]
    if not slot:
        return True
    account, unit = slot["account"], f"anas-assistant-cron@{slot['account']}.service"
    running, invocation = unit_state(unit)
    if running and (not slot["invocation"] or slot["invocation"] == invocation):
        slot["invocation"] = invocation
        return False
    if not running and not slot["invocation"] and now - slot["released"] < START_GRACE:
        return False  # not started yet
    token = f"{PLAN_RUN}/{account}"
    if os.path.lexists(token):
        # No round took it: the unit did not start, or ran before it was there. Given back.
        os.unlink(token)
        key = slot["grant"]
        state["counts"][key] = max(0, state["counts"].get(key, 1) - 1)
    if account in marked():
        # Its marker outlived the round: nothing claimed. Wait before the member's next one.
        held = state["backoff"].get(account, {"n": 0})
        n = held["n"] + 1
        state["backoff"][account] = {"n": n, "until": now + min(PLAN_BACKOFF * 2 ** (n - 1), PLAN_BACKOFF_MAX)}
    else:
        state["backoff"].pop(account, None)
    state["slot"] = None
    return True


def plan_cycle():
    """Give one member waiting for a plan a round, while no planning round runs."""
    now = time.time()
    state = load_plans()
    if not slot_over(state, now):
        save_plans(state)
        return
    for account in marked():
        unit = f"anas-assistant-cron@{account}.service"
        grant = plan_grant(account, now)
        if grant is None or not os.path.isfile(f"{TOKENS}/{account}.env"):
            continue
        key = f"{account}|{grant[0]}"
        if state["counts"].get(key, 0) >= grant[1]:
            continue
        if state["backoff"].get(account, {}).get("until", 0) > now or running(unit) or not active_account(account):
            continue
        state["counts"][key] = state["counts"].get(key, 0) + 1
        state["slot"] = {"account": account, "grant": key, "invocation": "", "released": now}
        save_plans(state)
        os.makedirs(PLAN_RUN, mode=0o700, exist_ok=True)
        with open(f"{PLAN_RUN}/{account}", "x", encoding="utf-8"):
            pass
        try:
            subprocess.run(["/usr/bin/systemctl", "start", "--no-block", unit], check=True, timeout=COMMAND_TIMEOUT)
        except (OSError, subprocess.SubprocessError):
            os.unlink(f"{PLAN_RUN}/{account}")
            state["counts"][key] -= 1
            state["slot"] = None
            save_plans(state)
            raise
        print(f"started {unit} to plan (round {state['counts'][key]} of the grant)", flush=True)
        return
    save_plans(state)


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
        try:
            plan_cycle()
        except Exception as error:  # noqa: BLE001 - planning never stops the members' own tasks.
            print(f"planning skipped: {type(error).__name__}: {error}", file=sys.stderr, flush=True)
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
