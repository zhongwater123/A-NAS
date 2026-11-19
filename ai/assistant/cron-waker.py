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
invocation.

A planning grant is the File Broker's record that the member, signed in, asked for something
the montage center may plan for. It is read only from GRANTS, the Host Agent's own directory,
and only as a file that is root's and nobody else's to change: a grant anyone else could have
written would let them start a member's assistant.

Each grant's rounds are counted in PLAN_STATE, before the token is given. A round counts when
it claimed a round of planning: montage_round.py then leaves the token's nonce in the member's
Hermes home (RECEIPT), which the montage service cannot write. A round that claimed nothing
(another gateway served the home, the montage center had nothing to plan or did not answer, the
unit never started) leaves no receipt: its round is given back, and the member backs off. A
round that did claim holds nobody back, though the member's marker may be there again for
another of their jobs.
"""
import hashlib
import http.client
import json
import os
import pwd
import re
import secrets
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
# The montage center's planning markers, the File Broker's grants (the Host Agent's
# AssistantPlanDir), the tokens given to rounds, and the counts.
MARKERS = os.environ.get("ANAS_MONTAGE_PLAN_MARKERS", "/run/a-nas-montage/plans")
GRANTS = os.environ.get("ANAS_ASSISTANT_PLAN_DIR", "/var/lib/a-nas-host/assistant/plans")
PLAN_RUN = os.environ.get("ANAS_MONTAGE_PLAN_RUN", "/run/anas-assistant-montage-plans")
PLAN_STATE = os.environ.get("ANAS_MONTAGE_PLAN_STATE", "/var/lib/anas-assistant-cron-waker/plans.json")
# The receipt of a claim, in the member's Hermes home (montage_round.py).
RECEIPT = "anas-montage-claim"
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


def regular_file(path, limit, dir_fd=None, owner=None):
    """What a file holds that somebody else may have written: None unless it is a regular
    file of at most limit bytes, read without following its final link or blocking while
    opening a FIFO. With owner, also None unless it is that user's and nobody else may write
    to it. path is relative to dir_fd when one is given."""
    try:
        fd = os.open(path, os.O_RDONLY | os.O_NOFOLLOW | os.O_NONBLOCK, dir_fd=dir_fd)
    except OSError:
        return None
    try:
        info = os.fstat(fd)
        if not stat.S_ISREG(info.st_mode) or info.st_size > limit:
            return None
        if owner is not None and (info.st_uid != owner or info.st_mode & 0o022):
            return None
        with os.fdopen(fd, "rb", closefd=False) as source:
            raw = source.read(limit + 1)
    except OSError:
        return None
    finally:
        os.close(fd)
    return raw if len(raw) <= limit else None


def due(account, now):
    """The jobs in the member's store that are enabled and due, each as a mark of the job and
    the instant it is due for: a job that ran is due for another instant, or for none. Empty
    when none is due. The store is the member's file (regular_file), and at most LIMIT bytes
    are parsed and MARKS jobs marked."""
    try:
        jobs = json.loads(regular_file(f"{DATA}/{account}/hermes/cron/jobs.json", LIMIT) or b"null").get("jobs")
    except (ValueError, AttributeError, RecursionError):
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


def unit_state(unit):
    """(running, invocation) of a unit.

    A round is a oneshot unit: activating for as long as it runs and never
    active, so the exit status of is-active, which is 0 for active alone,
    says "not running" throughout the round. Its invocation tells one run of
    the unit from the next.
    """
    shown = subprocess.run(
        ["/usr/bin/systemctl", "show", "-p", "ActiveState", "-p", "InvocationID", unit],
        check=False, timeout=COMMAND_TIMEOUT, capture_output=True, text=True,
    ).stdout
    fields = dict(line.split("=", 1) for line in shown.splitlines() if "=" in line)
    return fields.get("ActiveState") in {"activating", "active", "reloading", "deactivating"}, fields.get("InvocationID", "")


def running(unit):
    """Whether a round of the unit is under way."""
    return unit_state(unit)[0]


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


def load_plans():
    try:
        with open(PLAN_STATE, encoding="utf-8") as source:
            state = json.load(source)
    except (OSError, ValueError):
        state = {}
    if not isinstance(state, dict):
        state = {}
    # Notes that do not read as this loop writes them are dropped: a cycle that tripped over
    # them would trip over them every time.
    for name in ("counts", "backoff"):
        if not isinstance(state.get(name), dict):
            state[name] = {}
    state["counts"] = {key: count for key, count in state["counts"].items() if isinstance(count, int)}
    state["backoff"] = {account: held for account, held in state["backoff"].items()
                        if isinstance(held, dict) and isinstance(held.get("n"), int)}
    slot = state.get("slot")
    if not (isinstance(slot, dict) and isinstance(slot.get("account"), str) and ACCOUNT.fullmatch(slot["account"])
            and isinstance(slot.get("grant"), str) and isinstance(slot.get("invocation"), str)
            and isinstance(slot.get("released"), (int, float))):
        slot = None
    state["slot"] = slot
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
    """(issued, rounds) of the member's planning grant while it lasts, or None.

    A grant counts only as the File Broker wrote it: a regular file that is this user's own
    (root's) and nobody else's to change, in a directory of which the same holds, neither
    reached through a link. Anything else may be somebody's own making."""
    try:
        directory = os.open(GRANTS, os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW)
    except OSError:
        return None
    try:
        info = os.fstat(directory)
        if info.st_uid != os.geteuid() or info.st_mode & 0o022:
            return None
        raw = regular_file(account, 4096, dir_fd=directory, owner=os.geteuid())
    finally:
        os.close(directory)
    try:
        grant = json.loads(raw or b"null")
        stamp, rounds = grant["issuedAt"], int(grant["rounds"])
        issued = datetime.fromisoformat(stamp).timestamp()
    except (ValueError, KeyError, TypeError, AttributeError, RecursionError):
        return None
    if not isinstance(stamp, str) or not 0 <= now - issued <= GRANT_LIFE:
        return None
    return stamp, rounds


def marked():
    """The members with a job waiting for a plan, the longest waiting first."""
    try:
        entries = [(e.stat(follow_symlinks=False).st_mtime, e.name) for e in os.scandir(MARKERS)
                   if ACCOUNT.fullmatch(e.name) and e.is_file(follow_symlinks=False)]
    except OSError:
        return []
    return [name for _, name in sorted(entries)]


def claimed(account, nonce):
    """Whether the round given the token with this nonce claimed a round of planning: its
    receipt in the member's Hermes home (montage_round.py). The file is the member's."""
    if not nonce:
        return False
    try:
        receipt = json.loads(regular_file(f"{DATA}/{account}/hermes/{RECEIPT}", 4096) or b"null")
    except (ValueError, RecursionError):
        return False
    return isinstance(receipt, dict) and receipt.get("nonce") == nonce


def slot_over(state, now):
    """Whether the planning round given out has stopped; settles its token, its count and
    the member's backoff."""
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
    took = not os.path.lexists(token)
    if not took:
        os.unlink(token)  # the unit did not start, or ran before the token was there
    if took and claimed(account, slot.get("nonce")):
        # A round of planning ran: it counts, and nothing holds the member's next one back,
        # for another of their jobs.
        state["backoff"].pop(account, None)
    else:
        # Nothing was claimed. The grant's round is given back, and the member waits
        # before the next one: twice as long each time.
        key = slot["grant"]
        state["counts"][key] = max(0, state["counts"].get(key, 1) - 1)
        n = state["backoff"].get(account, {"n": 0})["n"] + 1
        state["backoff"][account] = {"n": n, "until": now + min(PLAN_BACKOFF * 2 ** (n - 1), PLAN_BACKOFF_MAX)}
    state["slot"] = None
    return True


def prune(state, now):
    """Forget the counts of grants that ran out and the backoffs long over."""
    def lasts(key):
        try:
            return 0 <= now - datetime.fromisoformat(key.rsplit("|", 1)[1]).timestamp() <= GRANT_LIFE
        except (ValueError, IndexError):
            return False

    state["counts"] = {key: count for key, count in state["counts"].items() if lasts(key)}
    state["backoff"] = {account: held for account, held in state["backoff"].items()
                        if held.get("until", 0) + PLAN_BACKOFF_MAX > now}


def give_round(state, now):
    """Start the unit of the first member who may plan now, with a token for the round."""
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
        nonce = secrets.token_hex(16)
        state["counts"][key] = state["counts"].get(key, 0) + 1
        state["slot"] = {"account": account, "grant": key, "invocation": "", "released": now, "nonce": nonce}
        # On disk before the token exists: a round is never given out uncounted.
        save_plans(state)
        os.makedirs(PLAN_RUN, mode=0o700, exist_ok=True)
        with open(f"{PLAN_RUN}/{account}", "x", encoding="ascii") as token:
            token.write(nonce)
        try:
            subprocess.run(["/usr/bin/systemctl", "start", "--no-block", unit], check=True, timeout=COMMAND_TIMEOUT)
        except (OSError, subprocess.SubprocessError):
            os.unlink(f"{PLAN_RUN}/{account}")
            state["counts"][key] -= 1
            state["slot"] = None
            raise
        print(f"started {unit} to plan (round {state['counts'][key]} of the grant)", flush=True)
        return


def plan_cycle():
    """Give one member waiting for a plan a round, while no planning round runs. The state
    is written only when it changed."""
    now = time.time()
    state = load_plans()
    before = json.dumps(state, sort_keys=True)
    try:
        if slot_over(state, now):
            prune(state, now)
            give_round(state, now)
    finally:
        if json.dumps(state, sort_keys=True) != before:
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
