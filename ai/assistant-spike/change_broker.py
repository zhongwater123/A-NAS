"""Change broker for in-turn approvals (#111): the stand-in for the product
service's change store and the File Broker.

Runs as root in the spike container with two Unix sockets:

- the product socket (argv[1]), reachable only by the product service. When
  Hermes asks the member to approve a call of 提交更改, the product service
  prepares the change from the call's arguments: the broker checks the
  targets, copies the staged files into its own state and returns a preview.
  The member's own click then approves or rejects it; anything left
  unanswered is expired.
- the agent socket (argv[2]), bound into every assistant sandbox. The account
  is the caller's UID (SO_PEERCRED). The MCP tool asks it to apply a
  submission; it applies only a change the member approved with exactly
  these arguments, once, from the copies the member saw, and as the member
  (setpriv to the member's UID, so the kernel ACL still decides). Replaced
  and deleted files go to the member's recycle bin.

Hermes's own "approved" never applies anything: without the member's grant
here, the call changes nothing.
"""
import base64
import grp
import hashlib
import json
import os
import pwd
import secrets
import socket
import socketserver
import struct
import subprocess
import sys
import threading
import time
from http.server import BaseHTTPRequestHandler
from pathlib import Path, PurePosixPath
from urllib.parse import parse_qs, urlparse

PRODUCT_SOCK, AGENT_SOCK = sys.argv[1], sys.argv[2]
DATA = Path("/srv/a-nas/data")
STATE = Path("/var/lib/anas-spike/changes")
PREVIEW_LIMIT = 64 * 1024
# An approval is used by the call Hermes makes right after it; a grant nobody
# used by then is void.
GRANT_SECONDS = 120
lock = threading.Lock()

STAGING = "/nas/助手/待确认"


def areas(account: str) -> tuple:
    """Sandbox paths a change may touch, and where they are on the data volume."""
    return (
        ("/nas/个人空间", DATA / "spaces/private" / account, True),
        ("/nas/共享空间/Agent空间/" + account, DATA / "spaces/shared/Agent空间" / account, True),
        (STAGING, DATA / "assistant" / account / "files/待确认", False),
    )


def clean(raw) -> str:
    """The path as written, made canonical without following links; '..' is refused."""
    path = PurePosixPath(str(raw or ""))
    if not path.is_absolute():
        raise ValueError(f"{raw}: 需要绝对路径")
    if ".." in path.parts:
        raise ValueError(f"{raw}: 路径中不能有 ..")
    return str(path)


def host_path(account: str, sandbox_path: str, *, target: bool) -> Path:
    for prefix, root, is_target in areas(account):
        if is_target == target and (sandbox_path == prefix or sandbox_path.startswith(prefix + "/")):
            return root / sandbox_path[len(prefix):].lstrip("/")
    if target:
        raise ValueError(f"{sandbox_path}: 只能更改 /nas/个人空间 或 /nas/共享空间/Agent空间/{account} 中的文件")
    raise ValueError(f"{sandbox_path}: source 必须是 {STAGING} 中已写好的文件")


def trash_for(account: str, sandbox_path: str) -> Path:
    if sandbox_path.startswith("/nas/共享空间/"):
        return DATA / "spaces/shared/.a-nas-trash" / account
    return DATA / "spaces/private" / account / ".a-nas-trash" / account


def submission(summary, operations) -> dict:
    """The arguments of one call of 提交更改 in canonical form; the grant is bound to their digest."""
    ops = [{key: op.get(key) for key in ("op", "path", "to", "source") if op.get(key) is not None}
           for op in operations or [] if isinstance(op, dict)]
    return {"summary": str(summary or ""), "operations": ops}


def digest(sub: dict) -> str:
    return hashlib.sha256(json.dumps(sub, ensure_ascii=False, sort_keys=True).encode()).hexdigest()


def fingerprint(path: Path) -> dict:
    """What the target is now; the change applies only if it is still the same."""
    try:
        st = path.lstat()
    except FileNotFoundError:
        return {"exists": False}
    if os.path.islink(path):
        return {"exists": True, "kind": "link", "to": os.readlink(path)}
    if path.is_dir():
        return {"exists": True, "kind": "dir"}
    return {"exists": True, "kind": "file", "size": st.st_size, "sha256": hashlib.sha256(path.read_bytes()).hexdigest()}


def read_staged(path: Path) -> bytes:
    """A staged file, never through a link (the assistant could point one anywhere root can read)."""
    fd = os.open(path, os.O_RDONLY | os.O_NOFOLLOW)
    try:
        if not os.path.isfile(f"/proc/self/fd/{fd}"):
            raise ValueError("不是普通文件")
        with os.fdopen(fd, "rb", closefd=False) as f:
            return f.read()
    finally:
        os.close(fd)


def check(account: str, sub: dict) -> tuple[list[dict], list[str]]:
    """Validate a submission; returns the planned steps and the problems."""
    steps, errors = [], []
    if not sub["operations"]:
        errors.append("没有任何操作")
    for i, op in enumerate(sub["operations"], 1):
        kind = op.get("op")
        if kind not in ("write", "delete", "move", "mkdir"):
            errors.append(f"第 {i} 项：未知操作 {kind!r}")
            continue
        try:
            step = {"op": kind, "path": clean(op.get("path"))}
            target = host_path(account, step["path"], target=True)
            step["before"] = fingerprint(target)
            if kind == "move":
                step["to"] = clean(op.get("to"))
                if fingerprint(host_path(account, step["to"], target=True))["exists"]:
                    raise ValueError(f"{step['to']} 已经存在")
            if kind in ("delete", "move") and not step["before"]["exists"]:
                raise ValueError(f"{step['path']} 不存在")
            if kind == "write":
                step["source"] = clean(op.get("source"))
                try:
                    step["content"] = read_staged(host_path(account, step["source"], target=False))
                except (OSError, ValueError):
                    raise ValueError(f"source 必须是 {STAGING} 中已写好的文件") from None
                if step["before"].get("kind") == "file":
                    step["beforeText"] = text_of(target.read_bytes())
            steps.append(step)
        except ValueError as exc:
            errors.append(f"第 {i} 项：{exc}")
    return steps, errors


# ---- state ----

def load(account: str) -> dict:
    path = STATE / f"{account}.json"
    return json.loads(path.read_text()) if path.exists() else {}


def save(account: str, changes: dict) -> None:
    tmp = STATE / f".{account}.json.tmp"
    tmp.write_text(json.dumps(changes, ensure_ascii=False))
    tmp.replace(STATE / f"{account}.json")


def snapshot_dir(account: str, change_id: str) -> Path:
    return STATE / "snapshots" / account / change_id


def text_of(data: bytes | None) -> str | None:
    if data is None:
        return None
    try:
        return data[:PREVIEW_LIMIT].decode("utf-8")
    except UnicodeDecodeError:
        return None


def preview(account: str, change: dict) -> dict:
    """The card: each operation, with the old and new text of every write."""
    ops = []
    for n, step in enumerate(change["steps"]):
        entry = {key: step.get(key) for key in ("op", "path", "to") if step.get(key)}
        entry["exists"] = step["before"]["exists"]
        if step["op"] == "write":
            entry["before"] = step.get("beforeText")
            entry["after"] = text_of((snapshot_dir(account, change["id"]) / str(n)).read_bytes())
        ops.append({key: value for key, value in entry.items() if value is not None})
    fields = ("id", "sessionId", "runId", "requestId", "summary", "status", "createdAt", "decidedAt", "error")
    return {key: change[key] for key in fields if change.get(key) is not None} | {"operations": ops}


def prepare(account: str, body: dict) -> tuple[int, dict]:
    sub = submission(body.get("summary"), body.get("operations"))
    steps, errors = check(account, sub)
    if errors:
        return 422, {"error": "提交被拒绝：\n" + "\n".join(errors)}
    change_id = secrets.token_hex(4)
    snap = snapshot_dir(account, change_id)
    snap.mkdir(parents=True)
    for n, step in enumerate(steps):
        if step["op"] == "write":
            (snap / str(n)).write_bytes(step.pop("content"))
    change = {"id": change_id, "digest": digest(sub), "summary": sub["summary"], "steps": steps, "status": "awaiting",
              "createdAt": int(time.time()), **{key: str(body.get(key) or "") for key in ("sessionId", "runId", "requestId")}}
    with lock:
        changes = load(account)
        changes[change_id] = change
        save(account, changes)
    return 200, preview(account, change)


def decide(account: str, change_id: str, verb: str) -> tuple[int, dict]:
    moves = {"approve": ({"awaiting"}, "approved"), "reject": ({"awaiting"}, "rejected"),
             "expire": ({"awaiting", "approved"}, "expired")}
    allowed, status = moves[verb]
    with lock:
        changes = load(account)
        change = changes.get(change_id)
        if change is None:
            return 404, {"error": "no such change"}
        if change["status"] not in allowed:
            return 409, {"error": "这项更改已经处理过了", "status": change["status"]}
        change.update(status=status, decidedAt=int(time.time()))
        save(account, changes)
    return 200, preview(account, change)


# The member-side half: runs as the member, so ACLs apply to every step.
APPLY = r"""
import base64, json, os, sys, time
plan = json.loads(sys.stdin.read())
for n, step in enumerate(plan):
    try:
        kind, target = step["op"], step["target"]
        if kind in ("write", "delete") and os.path.lexists(target):
            os.makedirs(step["trash"], exist_ok=True)
            os.rename(target, os.path.join(step["trash"], time.strftime("%Y%m%d-%H%M%S-") + os.path.basename(target)))
        if kind == "write":
            os.makedirs(os.path.dirname(target), exist_ok=True)
            fd = os.open(target, os.O_WRONLY | os.O_CREAT | os.O_EXCL | os.O_NOFOLLOW, 0o666)
            with os.fdopen(fd, "wb") as out:
                out.write(base64.b64decode(step["content"]))
        elif kind == "move":
            os.makedirs(os.path.dirname(step["to"]), exist_ok=True)
            if os.path.lexists(step["to"]):
                raise FileExistsError(17, "目标已经存在", step["to"])
            os.rename(target, step["to"])
        elif kind == "mkdir":
            os.makedirs(target, exist_ok=True)
    except OSError as exc:
        print(json.dumps({"done": n, "error": f"{exc.strerror}: {step['path']}"}))
        sys.exit(1)
print(json.dumps({"done": len(plan)}))
"""


def apply(account: str, sub: dict) -> str:
    """Called through the agent socket by the MCP tool, after Hermes's approval."""
    want, now = digest(sub), int(time.time())
    with lock:
        changes = load(account)
        change = next((c for c in changes.values() if c["status"] == "approved" and c["digest"] == want
                       and now - c.get("decidedAt", 0) <= GRANT_SECONDS), None)
        if change is not None:
            change["status"] = "applying"
            save(account, changes)
    if change is None:
        # The product service lets an invalid submission through without a card, so the
        # assistant learns what is wrong with it instead of hearing that the member said no.
        _, errors = check(account, sub)
        if errors:
            return "提交被拒绝，什么都没有改变：\n" + "\n".join(errors)
        return "这批更改没有得到用户本人的确认，什么都没有改变。"
    plan, problem = [], None
    for n, seen in enumerate(change["steps"]):
        if fingerprint(host_path(account, seen["path"], target=True)) != seen["before"]:
            problem = f"{seen['path']} 在用户确认前后被改动过，请重新提交"
            break
        entry = {"op": seen["op"], "path": seen["path"], "target": str(host_path(account, seen["path"], target=True)),
                 "trash": str(trash_for(account, seen["path"]))}
        if seen["op"] == "write":
            entry["content"] = base64.b64encode((snapshot_dir(account, change["id"]) / str(n)).read_bytes()).decode()
        if seen["op"] == "move":
            entry["to"] = str(host_path(account, seen["to"], target=True))
        plan.append(entry)
    done, error = 0, problem
    if problem is None:
        user = pwd.getpwnam(account)
        run = subprocess.run(["setpriv", f"--reuid={user.pw_uid}", f"--regid={user.pw_gid}", "--groups=20000", "--",
                              "python3", "-c", APPLY], input=json.dumps(plan), capture_output=True, text=True, timeout=60)
        try:
            outcome = json.loads(run.stdout.strip().splitlines()[-1])
            done, error = outcome["done"], outcome.get("error")
        except (IndexError, ValueError, KeyError):
            error = (run.stderr.strip().splitlines() or ["执行进程没有返回结果"])[-1][:300]
    with lock:
        changes = load(account)
        changes[change["id"]].update(status="failed" if error else "applied", appliedAt=int(time.time()),
                                     done=done, **({"error": error} if error else {}))
        save(account, changes)
    total = len(change["steps"])
    if error is None:
        return f"用户已确认，{total} 项更改已执行（编号 {change['id']}）。被覆盖和删除的旧文件在用户的回收站中。"
    if done == 0:
        return f"用户已确认，但更改没有执行（编号 {change['id']}）：{error}。什么都没有改变。"
    return f"用户已确认，执行到第 {done + 1} 项时失败（编号 {change['id']}）：{error}。前 {done} 项已经生效，后面的没有执行。"


def recent(account: str) -> str:
    items = sorted(load(account).values(), key=lambda c: c["createdAt"], reverse=True)[:10]
    if not items:
        return "最近没有更改。"
    names = {"awaiting": "等待确认", "approved": "已确认", "applying": "执行中", "applied": "已执行",
             "failed": "执行失败", "rejected": "被拒绝", "expired": "未确认"}
    return "\n".join(f"{time.strftime('%m-%d %H:%M', time.localtime(c['createdAt']))} {names.get(c['status'], c['status'])}："
                     f"{c['summary']}（编号 {c['id']}）" for c in items)


# ---- HTTP on the two sockets ----

class Handler(BaseHTTPRequestHandler):
    protocol_version = "HTTP/1.1"

    def log_message(self, *args):
        pass

    def address_string(self):
        return "uds"

    def reply(self, code: int, body) -> None:
        data = (json.dumps(body, ensure_ascii=False) if not isinstance(body, str) else body).encode()
        self.send_response(code)
        self.send_header("Content-Type", "application/json" if not isinstance(body, str) else "text/plain; charset=utf-8")
        self.send_header("Content-Length", str(len(data)))
        self.end_headers()
        self.wfile.write(data)

    def body(self) -> dict:
        return json.loads(self.rfile.read(int(self.headers.get("Content-Length", 0))) or b"{}")


class ProductHandler(Handler):
    def account(self) -> str | None:
        account = parse_qs(urlparse(self.path).query).get("account", [""])[0]
        try:
            pwd.getpwnam(account)
            return account
        except KeyError:
            return None

    def do_GET(self):
        account, parts = self.account(), urlparse(self.path).path.strip("/").split("/")
        if account and parts == ["changes"]:
            items = sorted(load(account).values(), key=lambda c: c["createdAt"], reverse=True)
            return self.reply(200, {"changes": [preview(account, c) for c in items]})
        if account and len(parts) == 2 and parts[0] == "changes" and parts[1] in load(account):
            return self.reply(200, preview(account, load(account)[parts[1]]))
        self.reply(404, {"error": "not found"})

    def do_POST(self):
        account, parts = self.account(), urlparse(self.path).path.strip("/").split("/")
        if account and parts == ["changes", "prepare"]:
            return self.reply(*prepare(account, self.body()))
        if account and len(parts) == 3 and parts[0] == "changes" and parts[2] in ("approve", "reject", "expire"):
            return self.reply(*decide(account, parts[1], parts[2]))
        self.reply(404, {"error": "not found"})


class AgentHandler(Handler):
    def account(self) -> str | None:
        """The member whose assistant is calling: instances run as the member's UID."""
        _, uid, _ = struct.unpack("3i", self.connection.getsockopt(socket.SOL_SOCKET, socket.SO_PEERCRED, struct.calcsize("3i")))
        try:
            user = pwd.getpwuid(uid)
            return user.pw_name if grp.getgrnam("a-nas-users").gr_gid in os.getgrouplist(user.pw_name, user.pw_gid) else None
        except KeyError:
            return None

    def do_GET(self):
        account = self.account()
        if account and self.path == "/recent":
            return self.reply(200, recent(account))
        self.reply(404, {"error": "not found"})

    def do_POST(self):
        account = self.account()
        if account and self.path == "/apply":
            body = self.body()
            return self.reply(200, apply(account, submission(body.get("summary"), body.get("operations"))))
        self.reply(404, {"error": "not found"})


class Server(socketserver.ThreadingMixIn, socketserver.UnixStreamServer):
    daemon_threads = True


def serve(path: str, handler, mode: int, owner: int) -> Server:
    if os.path.exists(path):
        os.unlink(path)
    server = Server(path, handler)
    os.chown(path, owner, -1)
    os.chmod(path, mode)
    threading.Thread(target=server.serve_forever, daemon=True).start()
    return server


STATE.mkdir(parents=True, exist_ok=True)
os.chmod(STATE, 0o700)
product_uid = int(os.environ.get("SPIKE_PRODUCT_UID") or 0)
serve(PRODUCT_SOCK, ProductHandler, 0o600, product_uid)
serve(AGENT_SOCK, AgentHandler, 0o666, 0)
threading.Event().wait()
