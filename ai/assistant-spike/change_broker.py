"""Change broker for in-turn approvals (#111): the stand-in for the product
service's change store and the File Broker.

Runs as root in the spike container with two Unix sockets:

- the product socket (argv[1]), reachable only by the product service. When
  Hermes asks the member to approve a call of 提交更改, the product service
  prepares the change from the call's arguments: the broker checks the
  targets, copies the staged files into its own state and returns a preview.
  The member's own click then approves or rejects it; anything left
  unanswered is expired. With SPIKE_SESSION_LOOKUP naming the Host Agent's
  session lookup socket, every product request must carry the member's live
  session (X-A-NAS-Session) and the broker asks the Host Agent whose it is,
  as the File Broker does (ADR 0022 decision 10): the product service cannot
  prepare or grant anything for a member who is not signed in.
- the agent socket (argv[2]), bound into every assistant sandbox. The account
  is the caller's UID (SO_PEERCRED). The MCP tool asks it to apply a
  submission; it applies only a change the member approved with exactly
  these arguments, once, from the copies the member saw. Replaced and
  deleted files go to the member's recycle bin where Samba's recycle would
  put them, so the Web recycle bin shows them with their folder and name.

The broker never opens the member's paths itself: reading for the card and
applying both run as the member (setpriv to the member's UID, so the kernel
ACL decides) and never through a link (see MEMBER).

Hermes's own "approved" never applies anything: without the member's grant
here, the call changes nothing.
"""
import base64
import grp
import hashlib
import http.client
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
SESSION_LOOKUP = os.environ.get("SPIKE_SESSION_LOOKUP", "")
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
        ("/nas/个人空间", f"spaces/private/{account}", True),
        ("/nas/共享空间/Agent空间/" + account, f"spaces/shared/Agent空间/{account}", True),
        (STAGING, f"assistant/{account}/files/待确认", False),
    )


def clean(raw) -> str:
    """The path as written, made canonical without following links; '..' is refused."""
    path = PurePosixPath(str(raw or ""))
    if not path.is_absolute():
        raise ValueError(f"{raw}: 需要绝对路径")
    if ".." in path.parts:
        raise ValueError(f"{raw}: 路径中不能有 ..")
    return str(path)


def volume_path(account: str, sandbox_path: str, *, target: bool) -> str:
    """Where a sandbox path is, relative to the data volume."""
    for prefix, root, is_target in areas(account):
        if is_target == target and sandbox_path.startswith(prefix + "/"):
            return root + "/" + sandbox_path[len(prefix) + 1:]
    if target:
        raise ValueError(f"{sandbox_path}: 只能更改 /nas/个人空间 或 /nas/共享空间/Agent空间/{account} 中的文件")
    raise ValueError(f"{sandbox_path}: source 必须是 {STAGING} 中已写好的文件")


def trash_for(account: str, sandbox_path: str) -> str:
    """The folder Samba's recycle (keeptree) would move this path into: the member's recycle
    bin in its space, then the folders it was in. The Web recycle bin imports files found
    there under their own name and original folder (files.Service.reconcileSambaTrash)."""
    if sandbox_path.startswith("/nas/共享空间/"):
        bin_, inside = f"spaces/shared/.a-nas-trash/{account}", sandbox_path[len("/nas/共享空间/"):]
    else:
        bin_, inside = f"spaces/private/{account}/.a-nas-trash/{account}", sandbox_path[len("/nas/个人空间/"):]
    parent = str(PurePosixPath(inside).parent)
    return bin_ if parent == "." else f"{bin_}/{parent}"


def submission(summary, operations) -> dict:
    """The arguments of one call of 提交更改 in canonical form; the grant is bound to their digest."""
    ops = [{key: op.get(key) for key in ("op", "path", "to", "source") if op.get(key) is not None}
           for op in operations or [] if isinstance(op, dict)]
    return {"summary": str(summary or ""), "operations": ops}


def digest(sub: dict) -> str:
    return hashlib.sha256(json.dumps(sub, ensure_ascii=False, sort_keys=True).encode()).hexdigest()


# The member-side half. The broker never opens the member's paths as root: this
# runs as the member (so the kernel ACL decides) and reaches every path one name
# at a time from the data volume without following a link, the way the File
# Broker's openat2(RESOLVE_NO_SYMLINKS) does. A link anywhere along a path, even
# one the member made, stops it: nothing is read or written through it.
MEMBER = r'''
import base64, hashlib, json, os, stat, sys

DATA, LIMIT = "/srv/a-nas/data", 64 * 1024
WALK = os.O_PATH | os.O_DIRECTORY | os.O_NOFOLLOW


class Refused(Exception):
    pass


def folder(rel, create=False):
    fd = os.open(DATA, WALK)
    try:
        for name in filter(None, rel.split("/")):
            try:
                nxt = os.open(name, WALK, dir_fd=fd)
            except FileNotFoundError:
                if not create:
                    raise
                os.mkdir(name, dir_fd=fd)
                nxt = os.open(name, WALK, dir_fd=fd)
            os.close(fd)
            fd = nxt
        return fd
    except BaseException:
        os.close(fd)
        raise


def parent(rel, create=False):
    head, _, name = rel.rpartition("/")
    return folder(head, create), name


def read(dfd, name):
    fd = os.open(name, os.O_RDONLY | os.O_NOFOLLOW, dir_fd=dfd)
    with os.fdopen(fd, "rb") as f:
        return f.read()


def fingerprint(rel):
    try:
        dfd, name = parent(rel)
    except FileNotFoundError:
        return {"exists": False}, None
    try:
        st = os.stat(name, dir_fd=dfd, follow_symlinks=False)
        if stat.S_ISLNK(st.st_mode):
            return {"exists": True, "kind": "link", "to": os.readlink(name, dir_fd=dfd)}, None
        if stat.S_ISDIR(st.st_mode):
            return {"exists": True, "kind": "dir"}, None
        if not stat.S_ISREG(st.st_mode):
            return {"exists": True, "kind": "other"}, None
        data = read(dfd, name)
        return {"exists": True, "kind": "file", "size": len(data), "sha256": hashlib.sha256(data).hexdigest()}, data
    except FileNotFoundError:
        return {"exists": False}, None
    finally:
        os.close(dfd)


def problem(exc, path):
    if isinstance(exc, NotADirectoryError):
        return f"{path} 经过链接或文件，不能更改"
    if isinstance(exc, PermissionError):
        return f"没有权限：{path}"
    if isinstance(exc, FileExistsError):
        return f"{path} 已经存在"
    return f"{exc.strerror or exc}：{path}"


def inspect(items):
    out = []
    for item in items:
        try:
            if item["kind"] == "source":
                dfd, name = parent(item["rel"])
                try:
                    if not stat.S_ISREG(os.stat(name, dir_fd=dfd, follow_symlinks=False).st_mode):
                        raise Refused()
                    out.append({"content": base64.b64encode(read(dfd, name)).decode()})
                finally:
                    os.close(dfd)
            else:
                seen, data = fingerprint(item["rel"])
                out.append({"before": seen, **({"head": base64.b64encode(data[:LIMIT]).decode()} if data is not None else {})})
        except (OSError, Refused) as exc:
            out.append({"error": problem(exc, item["path"]) if isinstance(exc, OSError) else "不是普通文件"})
    return out


def to_trash(dfd, name, trash):
    # Samba's recycle:versions keeps an earlier copy and names the next "Copy #1 of <name>".
    # The File Broker renames with RENAME_NOREPLACE; os.rename has no flags, so this spike
    # checks first.
    tfd = folder(trash, create=True)
    try:
        target, n = name, 1
        while True:
            try:
                os.stat(target, dir_fd=tfd, follow_symlinks=False)
            except FileNotFoundError:
                break
            target, n = f"Copy #{n} of {name}", n + 1
        os.rename(name, target, src_dir_fd=dfd, dst_dir_fd=tfd)
    finally:
        os.close(tfd)


def apply(plan):
    for step in plan:
        try:
            if fingerprint(step["rel"])[0] != step["before"]:
                return {"done": 0, "error": f"{step['path']} 在用户确认前后被改动过，请重新提交"}
        except OSError as exc:
            return {"done": 0, "error": problem(exc, step["path"])}
    for n, step in enumerate(plan):
        try:
            kind = step["op"]
            if kind == "mkdir":
                os.close(folder(step["rel"], create=True))
                continue
            dfd, name = parent(step["rel"], create=kind == "write")
            try:
                if kind in ("write", "delete") and step["before"]["exists"]:
                    to_trash(dfd, name, step["trash"])
                if kind == "write":
                    fd = os.open(name, os.O_WRONLY | os.O_CREAT | os.O_EXCL | os.O_NOFOLLOW, 0o666, dir_fd=dfd)
                    with os.fdopen(fd, "wb") as out:
                        out.write(base64.b64decode(step["content"]))
                elif kind == "move":
                    tfd, to = parent(step["toRel"], create=True)
                    try:
                        try:
                            os.stat(to, dir_fd=tfd, follow_symlinks=False)
                            raise FileExistsError(17, "exists")
                        except FileNotFoundError:
                            pass
                        os.rename(name, to, src_dir_fd=dfd, dst_dir_fd=tfd)
                    finally:
                        os.close(tfd)
            finally:
                os.close(dfd)
        except OSError as exc:
            return {"done": n, "error": problem(exc, step["path"])}
    return {"done": len(plan)}


command, payload = sys.argv[1], json.loads(sys.stdin.read())
print(json.dumps(inspect(payload) if command == "inspect" else apply(payload), ensure_ascii=False))
'''


def member(account: str, command: str, payload) -> object:
    user = pwd.getpwnam(account)
    try:
        run = subprocess.run(["setpriv", f"--reuid={user.pw_uid}", f"--regid={user.pw_gid}", "--groups=20000", "--",
                              "python3", "-c", MEMBER, command], input=json.dumps(payload, ensure_ascii=False),
                             capture_output=True, text=True, timeout=60)
    except subprocess.TimeoutExpired:
        raise RuntimeError("执行进程超时") from None
    try:
        return json.loads(run.stdout.strip().splitlines()[-1])
    except (IndexError, ValueError):
        raise RuntimeError((run.stderr.strip().splitlines() or ["执行进程没有返回结果"])[-1][:300]) from None


def text_of(data: bytes | None) -> str | None:
    if data is None:
        return None
    try:
        return data[:PREVIEW_LIMIT].decode("utf-8")
    except UnicodeDecodeError:
        return None


def check(account: str, sub: dict) -> tuple[list[dict], list[str]]:
    """Validate a submission against what the member's paths hold now; returns the planned steps and the problems."""
    steps, errors, items = [], [], []
    if not sub["operations"]:
        errors.append("没有任何操作")
    for i, op in enumerate(sub["operations"], 1):
        kind = op.get("op")
        if kind not in ("write", "delete", "move", "mkdir"):
            errors.append(f"第 {i} 项：未知操作 {kind!r}")
            continue
        try:
            step = {"n": i, "op": kind, "path": clean(op.get("path"))}
            step["rel"] = volume_path(account, step["path"], target=True)
            step["trash"] = trash_for(account, step["path"])
            items.append({"kind": "target", "rel": step["rel"], "path": step["path"]})
            if kind == "move":
                step["to"] = clean(op.get("to"))
                step["toRel"] = volume_path(account, step["to"], target=True)
                items.append({"kind": "target", "rel": step["toRel"], "path": step["to"]})
            if kind == "write":
                step["source"] = clean(op.get("source"))
                items.append({"kind": "source", "rel": volume_path(account, step["source"], target=False), "path": step["source"]})
            steps.append(step)
        except ValueError as exc:
            errors.append(f"第 {i} 项：{exc}")
    if errors:
        return [], errors
    found = iter(member(account, "inspect", items))
    for step in steps:
        prefix = f"第 {step.pop('n')} 项："
        seen = next(found)
        to = next(found) if step["op"] == "move" else None
        source = next(found) if step["op"] == "write" else None
        if "error" in seen:
            errors.append(prefix + seen["error"])
            continue
        step["before"] = seen["before"]
        if step["op"] in ("delete", "move") and not seen["before"]["exists"]:
            errors.append(f"{prefix}{step['path']} 不存在")
        if to is not None and ("error" in to or to["before"]["exists"]):
            errors.append(prefix + to.get("error", f"{step['to']} 已经存在"))
        if source is not None:
            if "error" in source:
                errors.append(f"{prefix}source 必须是 {STAGING} 中已写好的文件")
            else:
                step["content"] = base64.b64decode(source["content"])
            step["beforeText"] = text_of(base64.b64decode(seen["head"])) if "head" in seen else None
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
    plan = []
    for n, seen in enumerate(change["steps"]):
        step = {key: seen[key] for key in ("op", "path", "rel", "trash", "before", "toRel") if key in seen}
        if seen["op"] == "write":
            step["content"] = base64.b64encode((snapshot_dir(account, change["id"]) / str(n)).read_bytes()).decode()
        plan.append(step)
    try:
        outcome = member(account, "apply", plan)
        done, error = outcome["done"], outcome.get("error")
    except RuntimeError as exc:
        done, error = 0, str(exc)
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


def session_user(token: str) -> str | None:
    """The member signed in on a session token, from the Host Agent's lookup."""
    if not token:
        return None
    conn = http.client.HTTPConnection("host-agent", timeout=5)
    conn.sock = socket.socket(socket.AF_UNIX)
    conn.sock.settimeout(5)
    conn.sock.connect(SESSION_LOOKUP)
    conn.request("POST", "/v1/sessions/resolve", json.dumps({"token": token}), {"Content-Type": "application/json"})
    reply = conn.getresponse()
    body = json.loads(reply.read() or b"{}")
    return body.get("username") if reply.status == 200 else None


class ProductHandler(Handler):
    def account(self) -> str | bool | None:
        """The member named in the request; False when their session was refused (answered)."""
        account = parse_qs(urlparse(self.path).query).get("account", [""])[0]
        try:
            pwd.getpwnam(account)
        except KeyError:
            return None
        if SESSION_LOOKUP:
            try:
                signed_in = session_user(self.headers.get("X-A-NAS-Session", ""))
            except OSError:
                signed_in = None
            if signed_in != account:
                self.reply(401, {"error": "the member's session was not accepted"})
                return False
        return account

    def do_GET(self):
        account, parts = self.account(), urlparse(self.path).path.strip("/").split("/")
        if account is False:
            return
        if account and parts == ["changes"]:
            items = sorted(load(account).values(), key=lambda c: c["createdAt"], reverse=True)
            return self.reply(200, {"changes": [preview(account, c) for c in items]})
        if account and len(parts) == 2 and parts[0] == "changes" and parts[1] in load(account):
            return self.reply(200, preview(account, load(account)[parts[1]]))
        self.reply(404, {"error": "not found"})

    def do_POST(self):
        account, parts = self.account(), urlparse(self.path).path.strip("/").split("/")
        if account is False:
            return
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
