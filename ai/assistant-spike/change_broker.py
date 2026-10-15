"""Pending-change broker for the UI prototype: the stand-in for the product
service's change store and the File Broker.

Runs as root in the spike container on a Unix socket the host's product
service reaches. It lists what a member's assistant submitted through
anas_mcp.py, previews it, and on confirmation applies it as the member
(setpriv to the member's UID, so the kernel ACL still decides), moving
replaced and deleted files into the member's recycle bin. In A-NAS the
product service keeps the pending changes and the File Broker applies them.
"""
import hashlib
import json
import os
import pwd
import socketserver
import subprocess
import sys
import time
from http.server import BaseHTTPRequestHandler
from pathlib import Path
from urllib.parse import parse_qs, urlparse

SOCK = sys.argv[1]
DATA = Path("/srv/a-nas/data")
STATE = Path("/var/lib/anas-spike/changes")
PREVIEW_LIMIT = 64 * 1024

# Paths inside an assistant's sandbox and where they are on the data volume.
def host_path(account: str, sandbox_path: str) -> Path:
    for prefix, root in (
        ("/nas/个人空间", DATA / "spaces/private" / account),
        ("/nas/共享空间/Agent空间/" + account, DATA / "spaces/shared/Agent空间" / account),
        ("/nas/助手", DATA / "assistant" / account / "files"),
    ):
        if sandbox_path == prefix or sandbox_path.startswith(prefix + "/"):
            return root / sandbox_path[len(prefix):].lstrip("/")
    raise ValueError(f"{sandbox_path}: outside the member's areas")


def trash_for(account: str, sandbox_path: str) -> Path:
    if sandbox_path.startswith("/nas/共享空间/"):
        return DATA / "spaces/shared/.a-nas-trash" / account
    return DATA / "spaces/private" / account / ".a-nas-trash" / account


def proposals(account: str) -> list[dict]:
    record = DATA / "assistant" / account / "files/.spike-proposals.jsonl"
    if not record.exists():
        return []
    items = [json.loads(line) for line in record.read_text(encoding="utf-8").splitlines() if line.strip()]
    status = load_status(account)
    for item in items:
        item.update({"status": "pending", **status.get(str(item["id"]), {})})
    return items


def load_status(account: str) -> dict:
    path = STATE / f"{account}.json"
    return json.loads(path.read_text()) if path.exists() else {}


def save_status(account: str, change_id: int, **fields) -> None:
    STATE.mkdir(parents=True, exist_ok=True)
    status = load_status(account)
    status[str(change_id)] = {**status.get(str(change_id), {}), **fields}
    (STATE / f"{account}.json").write_text(json.dumps(status, ensure_ascii=False))


def submission_key(summary, operations) -> tuple:
    return summary, [tuple(op.get(key) for key in ("op", "path", "to", "source")) for op in operations or []]


def attach(account: str, session: str, submission: dict) -> int | None:
    """Give the newest unattached change with this summary and these
    operations the conversation it was submitted in (the product service saw
    the submission in that conversation's stream)."""
    key = submission_key(submission.get("summary"), submission.get("operations"))
    for item in reversed(proposals(account)):
        if not item.get("sessionId") and submission_key(item.get("summary"), item.get("operations")) == key:
            save_status(account, item["id"], sessionId=session)
            return item["id"]
    return None


def read_text(path: Path) -> str | None:
    try:
        data = path.read_bytes()[:PREVIEW_LIMIT]
        return data.decode("utf-8")
    except (OSError, UnicodeDecodeError):
        return None


def preview(account: str, item: dict) -> dict:
    ops = []
    for op in item["operations"]:
        entry = {key: op.get(key) for key in ("op", "path", "to") if op.get(key)}
        try:
            target = host_path(account, op["path"])
            entry["exists"] = target.exists()
            if op["op"] == "write":
                source = host_path(account, op["source"])
                if item.get("status") == "pending" and (not source.is_file() or hashlib.sha256(source.read_bytes()).hexdigest() != op.get("sha256")):
                    # A later draft replaced what was submitted; the old preview would show content this change no longer writes.
                    entry["problem"] = "暂存内容在提交后又被改动"
                else:
                    entry["before"] = read_text(target) if target.is_file() else None
                    entry["after"] = read_text(source)
        except (ValueError, KeyError) as exc:
            entry["problem"] = str(exc)
        # Absent rather than null, like the product API (api/openapi.yaml AssistantChange).
        ops.append({key: value for key, value in entry.items() if value is not None})
    fields = {key: item.get(key) for key in ("id", "sessionId", "summary", "status", "decidedAt", "error")}
    return {key: value for key, value in fields.items() if value is not None} | {"operations": ops}


# The member-side half: runs as the member, so ACLs apply to every step.
APPLY = r"""
import json, os, shutil, sys, time
plan = json.loads(sys.argv[1])
for step in plan:
    kind, target = step["op"], step["target"]
    if kind in ("write", "delete") and os.path.lexists(target):
        os.makedirs(step["trash"], exist_ok=True)
        os.rename(target, os.path.join(step["trash"], time.strftime("%Y%m%d-%H%M%S-") + os.path.basename(target)))
    if kind == "write":
        os.makedirs(os.path.dirname(target), exist_ok=True)
        shutil.copyfile(step["source"], target)
    elif kind == "move":
        os.makedirs(os.path.dirname(step["to"]), exist_ok=True)
        os.rename(target, step["to"])
    elif kind == "mkdir":
        os.makedirs(target, exist_ok=True)
"""


def apply(account: str, item: dict) -> None:
    plan = []
    for op in item["operations"]:
        step = {"op": op["op"], "target": str(host_path(account, op["path"])), "trash": str(trash_for(account, op["path"]))}
        if op["op"] == "write":
            source = host_path(account, op["source"])
            if hashlib.sha256(source.read_bytes()).hexdigest() != op.get("sha256"):
                raise ValueError("暂存内容在提交后被改动，请让助手重新提交")
            step["source"] = str(source)
        if op["op"] == "move":
            step["to"] = str(host_path(account, op["to"]))
        plan.append(step)
    user = pwd.getpwnam(account)
    subprocess.run(["setpriv", f"--reuid={user.pw_uid}", f"--regid={user.pw_gid}", "--groups=20000", "--",
                    "python3", "-c", APPLY, json.dumps(plan)], check=True, capture_output=True, text=True, timeout=60)


class Handler(BaseHTTPRequestHandler):
    protocol_version = "HTTP/1.1"

    def log_message(self, *args):
        pass

    def address_string(self):
        return "uds"

    def reply(self, code: int, body) -> None:
        data = json.dumps(body, ensure_ascii=False).encode()
        self.send_response(code)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(data)))
        self.end_headers()
        self.wfile.write(data)

    def account(self) -> str | None:
        account = parse_qs(urlparse(self.path).query).get("account", [""])[0]
        try:
            pwd.getpwnam(account)
            return account
        except KeyError:
            return None

    def do_GET(self):
        account = self.account()
        if not account or urlparse(self.path).path != "/changes":
            return self.reply(404, {"error": "not found"})
        self.reply(200, {"changes": [preview(account, item) for item in reversed(proposals(account))]})

    def do_POST(self):
        account = self.account()
        parts = urlparse(self.path).path.strip("/").split("/")
        if account and parts == ["changes", "attach"]:
            body = json.loads(self.rfile.read(int(self.headers.get("Content-Length", 0))) or b"{}")
            attached = attach(account, str(body.get("sessionId", "")), body.get("submission") or {})
            return self.reply(200, {"id": attached}) if attached is not None else self.reply(404, {"error": "no matching change"})
        if not account or len(parts) != 3 or parts[0] != "changes" or parts[2] not in ("confirm", "reject"):
            return self.reply(404, {"error": "not found"})
        item = next((p for p in proposals(account) if str(p["id"]) == parts[1]), None)
        if item is None:
            return self.reply(404, {"error": "no such change"})
        if item["status"] != "pending":
            return self.reply(409, {"error": "这项更改已经处理过了"})
        now = int(time.time())
        if parts[2] == "reject":
            save_status(account, item["id"], status="rejected", decidedAt=now)
        else:
            try:
                apply(account, item)
                save_status(account, item["id"], status="applied", decidedAt=now)
            except (ValueError, OSError, subprocess.CalledProcessError) as exc:
                detail = getattr(exc, "stderr", "") or str(exc)
                save_status(account, item["id"], status="failed", decidedAt=now, error=detail.strip().splitlines()[-1][:300])
        self.reply(200, preview(account, next(p for p in proposals(account) if p["id"] == item["id"])))


class Server(socketserver.ThreadingMixIn, socketserver.UnixStreamServer):
    daemon_threads = True


if os.path.exists(SOCK):
    os.unlink(SOCK)
with Server(SOCK, Handler) as server:
    os.chmod(SOCK, 0o666)
    server.serve_forever()
