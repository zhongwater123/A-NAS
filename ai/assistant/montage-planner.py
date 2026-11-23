"""Hands the montage center's planning to members' VNAS (docs/architecture/montage-planning-session.md).

anas-montage-planner.service runs this as root with the Hermes runtime's Python, which has websockets. Every
INTERVAL seconds it gives one member with a job waiting for a plan a round, one round on the whole device at a time:

1. Who: a marker the montage service left in MARKERS, the longest waiting first; a planning grant, read only as root's
   own file in root's own directory (GRANTS, the File Broker's), under seven days old and with rounds left, which are
   counted here (STATE); an account in use with VNAS (its instance secret in TOKENS, the Host Agent's word).
2. Claim: POST /v1/rounds/claim on the montage center's assistant socket (MONTAGE), naming the account in
   X-A-NAS-Account, which the montage service hears from root alone.
3. Converse: Hermes's own login (/auth/password-login, the instance's secret as the member's password) on the socket
   of the member's planning instance (PLAN_SOCKETS/<account>.sock, which starts it), then Hermes's JSON-RPC on /api/ws:
   client.capabilities, session.create, session.title ("剪辑规划 · <project>"), the round reported started with the
   conversation's stored id, and prompt.submit with the fixed template (PROMPTS/<template>.md) filled with the claim's
   checked ids, token and target seconds only. The planning instance gives the model the montage, memory and skill
   tools alone.
4. Wait: events until the prompt's message.complete, at most TURN seconds; every approval Hermes asks for is refused.
   Then, the round reported, the conversation stays open until Hermes's background review of it says what it learned,
   at most REVIEW seconds: a review whose client is gone is reclaimed with its session about 20 seconds later.
5. Report: the end with its result (complete, or the error, timeout or cut-off that stopped it); a round whose
   conversation never got its prompt is abandoned, saying whether VNAS could not be reached or the planning failed.
6. Count: a round counts on its grant from the moment its prompt goes in; it is given back when the model never
   called a montage tool and the conversation did not complete, which planned nothing.

When the montage center's socket does not answer, nothing is claimed for 2, 4, 8 and then 15 minutes, or until a
member registers a new grant. Anything else that waits is the montage center's to say (internal/montage/planning.go).
"""
import asyncio
import contextlib
import http.client
import json
import os
import pwd
import re
import socket
import stat
import string
import sys
import time
from datetime import datetime

MARKERS = os.environ.get("ANAS_MONTAGE_PLAN_MARKERS", "/run/a-nas-montage/plans")
GRANTS = os.environ.get("ANAS_ASSISTANT_PLAN_DIR", "/var/lib/a-nas-host/assistant/plans")
TOKENS = os.environ.get("ANAS_ASSISTANT_TOKEN_DIR", "/var/lib/a-nas-host/assistant/tokens")
STATE = os.environ.get("ANAS_MONTAGE_PLANNER_STATE", "/var/lib/anas-montage-planner/state.json")
MONTAGE = os.environ.get("ANAS_MONTAGE_ASSISTANT_SOCKET", "/run/a-nas-montage-assistant/assistant.sock")
PLAN_SOCKETS = os.environ.get("ANAS_ASSISTANT_PLAN_SOCKETS", "/run/a-nas-assistant-plan")
MODEL = os.environ.get("ANAS_ASSISTANT_MODEL_SOCKET", "/run/anas-assistant-gw/model.sock")
HOST_SOCKET = os.environ.get("ANAS_HOST_AGENT_SOCKET", "/run/a-nas/host-agent.sock")
PROMPTS = os.environ.get("ANAS_MONTAGE_PROMPTS", "/usr/lib/a-nas/assistant/montage-prompts")
INTERVAL = int(os.environ.get("ANAS_MONTAGE_PLANNER_INTERVAL", "15"))

ACCOUNT = re.compile(r"[a-z_][a-z0-9_-]{0,31}")
# The montage center's round and job ids are a prefix and crypto/rand.Text (26 base32 letters), its tokens 16 random
# bytes in hex (internal/montage); internal/montage/planner_ids_test.go holds them to these.
ROUND_ID = re.compile(r"round:[A-Z2-7]{26}")
JOB_ID = re.compile(r"job:[A-Z2-7]{26}")
TOKEN = re.compile(r"[0-9a-f]{32}")
TEMPLATE = re.compile(r"plan-v[0-9]+")
STORED = re.compile(r"[A-Za-z0-9_.:-]{1,128}")
LOGIN_LINE = "HERMES_DASHBOARD_SESSION_TOKEN="  # the line that holds the instance's secret
GRANT_LIFE = 7 * 24 * 3600
# The planning instance's `hermes serve`, behind its socket; Hermes's login gate wants this Host.
HERMES_HOST = "127.0.0.1:9120"
START = 180  # seconds for the planning instance to start and log in
CALL = 60  # seconds for one JSON-RPC call
TURN = 60 * 60  # the montage center ends a started round after 65 minutes whatever happens
REVIEW = 150
MONTAGE_WAITS = (120, 240, 480, 900)
TITLE = "剪辑规划 · "


def say(text):
    print(text, flush=True)


class Unreachable(Exception):
    """What the round needs does not answer now: tried again later, not counted."""


class Broken(Exception):
    """What the round needs answered what it should not: the planning failed."""


class UnixHTTP(http.client.HTTPConnection):
    """The standard HTTP client over a Unix socket."""

    def __init__(self, path, timeout):
        super().__init__("localhost", timeout=timeout)
        self.socket_path = path

    def connect(self):
        self.sock = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
        self.sock.settimeout(self.timeout)
        self.sock.connect(self.socket_path)


def request(path, method, route, body=None, headers=None, timeout=30):
    """(status, headers, body) of one HTTP request on a Unix socket; Unreachable when it does not answer."""
    conn = UnixHTTP(path, timeout)
    try:
        conn.request(method, route, None if body is None else json.dumps(body).encode(), headers or {})
        response = conn.getresponse()
        return response.status, response.headers, response.read(1 << 20)
    except (OSError, http.client.HTTPException) as broken:
        raise Unreachable(f"{os.path.basename(path)}: {broken}") from None
    finally:
        conn.close()


def montage(account, route, body=None):
    """(status, answer) of one call on the montage center's assistant socket, for account."""
    status, _, raw = request(MONTAGE, "POST", route, body or {},
                             {"Content-Type": "application/json", "X-A-NAS-Account": account})
    if status == 503:
        raise Unreachable("the montage center is not available")
    try:
        return status, json.loads(raw) if raw else {}
    except ValueError:
        return status, {}


def regular_file(path, limit, dir_fd=None, owner=None):
    """What a file holds that somebody else may have written: None unless it is a regular file of at most limit
    bytes, read without following its final link or blocking while opening a FIFO. With owner, also None unless it
    is that user's and nobody else may write to it. path is relative to dir_fd when one is given."""
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


def own_file(directory, name, limit):
    """A file in directory that is this user's own and nobody else's to change, in a directory of which the same
    holds, neither reached through a link; None for anything else, which may be somebody's own making."""
    try:
        fd = os.open(directory, os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW)
    except OSError:
        return None
    try:
        info = os.fstat(fd)
        if info.st_uid != os.geteuid() or info.st_mode & 0o022:
            return None
        return regular_file(name, limit, dir_fd=fd, owner=os.geteuid())
    finally:
        os.close(fd)


def plan_grant(account, now):
    """(issued, rounds) of the member's planning grant while it lasts, or None. A grant counts only as the File
    Broker writes it, root's own file in root's own directory: one anybody else could write would let them have a
    member's VNAS plan."""
    try:
        grant = json.loads(own_file(GRANTS, account, 4096) or b"null")
        stamp, rounds = grant["issuedAt"], int(grant["rounds"])
        issued = datetime.fromisoformat(stamp).timestamp()
    except (ValueError, KeyError, TypeError, AttributeError, RecursionError):
        return None
    if not isinstance(stamp, str) or not 0 <= now - issued <= GRANT_LIFE:
        return None
    return stamp, rounds


def secret(account):
    """The instance's secret, the member's password for Hermes's login (written by the Host Agent), or None."""
    raw = own_file(TOKENS, f"{account}.env", 4096)
    for line in (raw or b"").decode("ascii", "replace").splitlines():
        if line.startswith(LOGIN_LINE):
            return line[len(LOGIN_LINE):].strip() or None
    return None


def marked():
    """The members with a job waiting for a plan, the longest waiting first."""
    try:
        entries = [(e.stat(follow_symlinks=False).st_mtime, e.name) for e in os.scandir(MARKERS)
                   if ACCOUNT.fullmatch(e.name) and e.is_file(follow_symlinks=False)]
    except OSError:
        return []
    return [name for _, name in sorted(entries)]


def active_account(account):
    """Whether the account is in use under this name: its UID is a member's, and the Host Agent says so."""
    try:
        uid = pwd.getpwnam(account).pw_uid
    except KeyError:
        return False
    if not 20100 <= uid <= 29999:
        return False
    status, _, raw = request(HOST_SOCKET, "POST", "/v1/accounts/resolve-uid", {"uid": uid},
                             {"Content-Type": "application/json"}, timeout=5)
    if status != 200:
        return False
    try:
        user = json.loads(raw)
    except ValueError:
        return False
    return isinstance(user, dict) and user.get("username") == account


def model_answers():
    """Whether the model's gateway answers, without calling the model or spending tokens."""
    try:
        status, _, _ = request(MODEL, "GET", "/v1/models", timeout=10)
    except Unreachable:
        return False
    return status == 200


# --- the counts of each grant ---

def load_state():
    try:
        with open(STATE, encoding="utf-8") as source:
            state = json.load(source)
    except (OSError, ValueError):
        state = {}
    counts = state.get("counts") if isinstance(state, dict) else None
    # Notes that do not read as this file writes them are dropped: a cycle that tripped over them would trip over
    # them every time.
    return {"counts": {k: v for k, v in counts.items() if isinstance(k, str) and isinstance(v, int)}
            if isinstance(counts, dict) else {}}


def save_state(state):
    os.makedirs(os.path.dirname(STATE), mode=0o700, exist_ok=True)
    partial = f"{STATE}.{os.getpid()}"
    with open(partial, "w", encoding="utf-8") as out:
        json.dump(state, out)
        out.flush()
        os.fsync(out.fileno())
    os.replace(partial, STATE)


def count(key, change):
    """Add change to the grant's count, on disk before anything goes on."""
    state = load_state()
    state["counts"][key] = max(0, state["counts"].get(key, 0) + change)
    save_state(state)


def prune(now):
    """Forget the counts of grants that ran out."""
    state = load_state()

    def lasts(key):
        try:
            return 0 <= now - datetime.fromisoformat(key.rsplit("|", 1)[1]).timestamp() <= GRANT_LIFE
        except (ValueError, IndexError):
            return False

    kept = {key: n for key, n in state["counts"].items() if lasts(key)}
    if kept != state["counts"]:
        save_state({"counts": kept})


# --- one round ---

def prompt(claim):
    """The template filled with the round's checked ids, token and target seconds, and the conversation's title."""
    template = claim.get("template", "")
    if not isinstance(template, str) or not TEMPLATE.fullmatch(template):
        raise ValueError(f"template {template!r}")
    for key, pattern in (("roundId", ROUND_ID), ("jobId", JOB_ID), ("attemptToken", TOKEN)):
        if not isinstance(claim.get(key), str) or not pattern.fullmatch(claim[key]):
            raise ValueError(f"{key} is not what a round's is")
    target = claim.get("targetSeconds")
    if not isinstance(target, int) or isinstance(target, bool) or not 1 <= target <= 3600:
        raise ValueError("targetSeconds is not a whole number of seconds")
    with open(os.path.join(PROMPTS, template + ".md"), encoding="utf-8") as source:
        text = source.read()
    filled = string.Template(text).substitute(round_id=claim["roundId"], job_id=claim["jobId"],
                                              token=claim["attemptToken"], target=target)
    project = claim.get("project") if isinstance(claim.get("project"), str) else ""
    project = "".join(ch for ch in project if ch.isprintable()).strip()[:60] or "未命名项目"
    return filled, TITLE + project


def login(account):
    """An access token of Hermes's own login to the member's planning instance, which the connection starts."""
    password = secret(account)
    if password is None:
        raise Broken("the planning instance's secret cannot be read")
    status, headers, _ = request(os.path.join(PLAN_SOCKETS, f"{account}.sock"), "POST", "/auth/password-login",
                                 {"provider": "basic", "username": account, "password": password},
                                 {"Content-Type": "application/json", "Host": HERMES_HOST}, timeout=START)
    if status != 200:
        # The instance answered, and refused: its settings are not what the Host Agent wrote.
        raise Broken(f"the planning instance refused the login ({status})")
    for header in headers.get_all("Set-Cookie") or []:
        name, _, rest = header.partition("=")
        if name.strip().endswith("hermes_session_at"):
            return rest.split(";", 1)[0]
    raise Broken("the planning instance's login set no session")


async def connect(account, token):
    """Hermes's JSON-RPC on /api/ws of the member's planning instance."""
    from websockets.asyncio.client import unix_connect  # the runtime's; the tests use a stand-in

    try:
        ws = await unix_connect(os.path.join(PLAN_SOCKETS, f"{account}.sock"), f"ws://{HERMES_HOST}/api/ws?token={token}",
                                max_size=None, open_timeout=START)
    except (OSError, TimeoutError) as broken:
        raise Unreachable(f"the planning instance's /api/ws: {broken}") from None
    return Hermes(ws)


class Closed(Unreachable):
    """The planning instance closed the connection."""


class Hermes:
    """Hermes's JSON-RPC (tui_gateway): calls and their answers, events, and the server's own requests, of which
    approvals are refused and every other is answered as one this client does not handle."""

    def __init__(self, ws):
        self.ws, self.next_id, self.pending = ws, 0, {}
        self.events = asyncio.Queue()
        self.approvals = 0
        self.reader = None

    async def start(self):
        self.reader = asyncio.get_running_loop().create_task(self._read())
        deadline = time.monotonic() + START
        while True:
            try:
                frame = await self.event(max(0.0, deadline - time.monotonic()))
            except TimeoutError:
                raise Unreachable(f"the planning instance was not ready within {START} s") from None
            if frame.get("type") == "closed":
                raise Closed(frame.get("payload", {}).get("reason", "the connection closed"))
            if frame.get("type") == "gateway.ready":
                return

    async def close(self):
        if self.reader:
            self.reader.cancel()
            with contextlib.suppress(asyncio.CancelledError, Exception):
                await self.reader
        with contextlib.suppress(Exception):
            await self.ws.close()

    async def _read(self):
        try:
            async for raw in self.ws:
                for line in (raw if isinstance(raw, str) else raw.decode()).splitlines():
                    if line.strip():
                        await self._take(json.loads(line))
        except Exception as broken:  # noqa: BLE001 - however the connection ends, every waiter is told.
            reason = f"{type(broken).__name__}: {broken}"
        else:
            reason = "the planning instance closed the connection"
        for future in self.pending.values():
            if not future.done():
                future.set_exception(Closed(reason))
        await self.events.put({"type": "closed", "payload": {"reason": reason}})

    async def _take(self, message):
        if message.get("method") == "event":
            await self.events.put(message.get("params") or {})
        elif "method" in message and "id" in message:
            # A request of the server's: nobody is here to approve anything, and the planning needs nothing else.
            if message["method"] == "approval":
                self.approvals += 1
                answer = {"result": {"choice": "deny"}}
            else:
                answer = {"error": {"code": -32601, "message": "the planner answers no such request"}}
            await self.ws.send(json.dumps({"jsonrpc": "2.0", "id": message["id"], **answer}))
        elif message.get("id") in self.pending:
            self.pending.pop(message["id"]).set_result(message)

    async def call(self, method, params=None):
        self.next_id += 1
        future = asyncio.get_running_loop().create_future()
        self.pending[self.next_id] = future
        await self.ws.send(json.dumps({"jsonrpc": "2.0", "id": self.next_id, "method": method, "params": params or {}}))
        try:
            reply = await asyncio.wait_for(future, CALL)
        except TimeoutError:
            raise Broken(f"{method} was not answered within {CALL} s") from None
        if "error" in reply:
            raise Broken(f"{method}: {json.dumps(reply['error'], ensure_ascii=False)[:300]}")
        return reply.get("result") or {}

    async def event(self, timeout):
        try:
            return await asyncio.wait_for(self.events.get(), timeout)
        except TimeoutError:
            raise TimeoutError from None

    async def turn(self, session, limit):
        """(result, what to report, montage tools called) of the prompt's turn: until its message.complete, the
        connection closing, or limit seconds."""
        deadline, tools = time.monotonic() + limit, 0
        while True:
            try:
                frame = await self.event(max(0.0, deadline - time.monotonic()))
            except TimeoutError:
                with contextlib.suppress(Exception):
                    await self.call("session.interrupt", {"session_id": session})
                return "timeout", f"the conversation had not ended after {limit} s", tools
            kind, payload = frame.get("type"), frame.get("payload") or {}
            if kind == "closed":
                return "interrupted", payload.get("reason", "the connection closed"), tools
            if frame.get("session_id") not in (None, session):
                continue
            if kind == "tool.start" and "montage_" in str(payload.get("name", "")):
                tools += 1
            elif kind == "message.complete":
                status = payload.get("status") or "complete"
                if status == "complete":
                    return "complete", "complete", tools
                said = payload.get("error") or payload.get("failure_reason") or payload.get("text") or ""
                return "error", f"{status}: {said}"[:500], tools

    async def review(self, session, limit):
        """Whether Hermes's background review of the conversation reported, waiting limit seconds at most."""
        deadline = time.monotonic() + limit
        while True:
            try:
                frame = await self.event(max(0.0, deadline - time.monotonic()))
            except TimeoutError:
                return False
            if frame.get("type") == "closed":
                return False
            if frame.get("type") == "review.summary" and frame.get("session_id") in (None, session):
                return True


def abandon(account, round_id, failure, reason):
    say(f"{account}: round {round_id} not started ({failure}): {reason}")
    # Unreachable, the montage center abandons it itself five minutes after its claim.
    with contextlib.suppress(Unreachable):
        montage(account, f"/v1/rounds/{round_id}/abandon", {"outcome": reason[:500], "failure": failure})


def end(account, round_id, result, outcome):
    say(f"{account}: round {round_id} ended: {result}" + ("" if outcome == result else f" ({outcome})"))
    try:
        montage(account, f"/v1/rounds/{round_id}/end", {"result": result, "outcome": outcome[:500]})
    except Unreachable as broken:
        say(f"{account}: the end of round {round_id} was not reported ({broken}); the montage center ends it itself")


async def converse(account, key, round_id, text, title):
    """Hand the claimed round to the member's planning instance and see it to its end. Reports it either way."""
    if not model_answers():
        return abandon(account, round_id, "unreachable", "VNAS 的模型连不上：模型网关没有回应")
    try:
        token = await asyncio.to_thread(login, account)
        hermes = await connect(account, token)
    except Unreachable as broken:
        return abandon(account, round_id, "unreachable", f"VNAS 的规划进程连不上：{broken}")
    except Exception as broken:  # noqa: BLE001 - any failure before the prompt is the planning's, reported as such.
        return abandon(account, round_id, "error", f"{type(broken).__name__}: {broken}")
    stage = "before"  # before the prompt went in, while its turn runs, after it ended
    try:
        await hermes.start()
        await hermes.call("client.capabilities", {"server_requests": True})
        created = await hermes.call("session.create", {"cols": 100})
        session, stored = created.get("session_id"), created.get("stored_session_id")
        if not isinstance(session, str) or not isinstance(stored, str) or not STORED.fullmatch(stored):
            raise Broken(f"session.create answered {json.dumps(created, ensure_ascii=False)[:200]}")
        await hermes.call("session.title", {"session_id": session, "title": title})
        status, _ = await asyncio.to_thread(montage, account, f"/v1/rounds/{round_id}/started", {"runRef": stored})
        if status != 204:
            # Abandoned or over meanwhile: no model call without a recorded start.
            say(f"{account}: round {round_id} was not recorded as started ({status}); its prompt is not sent")
            return
        say(f"{account}: round {round_id} handed to VNAS as conversation {stored}")
        # Counted before the prompt goes in: a round is never planned uncounted, also if this process dies.
        count(key, 1)
        stage = "turn"
        await hermes.call("prompt.submit", {"session_id": session, "text": text})
        result, outcome, tools = await hermes.turn(session, TURN)
        stage = "after"
        if tools == 0 and result != "complete":
            count(key, -1)  # the model planned nothing: the round is given back
        end(account, round_id, result, outcome)
        if hermes.approvals:
            say(f"{account}: refused {hermes.approvals} approval(s) the planning asked for")
        if result == "complete":
            reviewed = await hermes.review(session, REVIEW)
            say(f"{account}: the background review of round {round_id} " + ("reported" if reviewed else f"said nothing within {REVIEW} s"))
    except Exception as broken:  # noqa: BLE001 - whatever stops the conversation is reported.
        reason = f"{type(broken).__name__}: {broken}"
        cut = isinstance(broken, Unreachable)
        if stage == "before":
            abandon(account, round_id, "unreachable" if cut else "error", reason)
        elif stage == "turn":
            count(key, -1)  # the prompt was refused or the turn's events were lost before any of them was read
            end(account, round_id, "interrupted" if cut else "error", reason)
        else:
            say(f"{account}: round {round_id} ended, then {reason}")
    finally:
        await hermes.close()


def plan_round(account, key):
    """Claim a round for the member and see it through. Unreachable when the montage center does not answer."""
    status, claim = montage(account, "/v1/rounds/claim")
    if status == 409:
        say(f"{account}: no job to plan")
        return
    if status != 200:
        say(f"{account}: the claim answered {status}")
        return
    round_id = claim.get("roundId", "") if isinstance(claim, dict) else ""
    if not isinstance(round_id, str) or not ROUND_ID.fullmatch(round_id):
        say(f"{account}: the claim named no round")
        return
    say(f"{account}: round {round_id} of job {claim.get('jobId')} claimed")
    try:
        text, title = prompt(claim)
    except (OSError, ValueError, KeyError) as broken:
        abandon(account, round_id, "error", f"the claim cannot be planned here: {broken}")
        return
    asyncio.run(converse(account, key, round_id, text, title))


# --- the loop ---

class Planner:
    """The loop's own memory: how long the montage center has not answered."""

    def __init__(self):
        self.waits = 0
        self.until = 0.0
        self.since = 0.0

    def cycle(self, now):
        """Give the first member who may plan now a round."""
        members = []
        for account in marked():
            grant = plan_grant(account, now)
            if grant is not None:
                members.append((account, grant))
        if now < self.until:
            # A member who registered a grant since the montage center stopped answering is tried at once.
            if not any(datetime.fromisoformat(grant[0]).timestamp() > self.since for _, grant in members):
                return
            self.until = 0.0
        prune(now)
        counts = load_state()["counts"]
        for account, (issued, rounds) in members:
            key = f"{account}|{issued}"
            if counts.get(key, 0) >= rounds or not os.path.isfile(os.path.join(TOKENS, f"{account}.env")):
                continue
            try:
                if not active_account(account):
                    continue
            except Unreachable:
                continue
            try:
                plan_round(account, key)
            except Unreachable as broken:
                wait = MONTAGE_WAITS[min(self.waits, len(MONTAGE_WAITS) - 1)]
                self.waits += 1
                self.since, self.until = now, now + wait
                say(f"the montage center does not answer ({broken}); claiming again in {wait // 60} minutes")
                return
            self.waits = 0
            return


def main():
    planner = Planner()
    while True:
        try:
            planner.cycle(time.time())
        except Exception as broken:  # noqa: BLE001 - one member's round never stops the loop.
            say(f"planning skipped: {type(broken).__name__}: {broken}")
        time.sleep(INTERVAL)


if __name__ == "__main__":
    if len(sys.argv) != 1:
        sys.exit("usage: montage-planner.py")
    main()
