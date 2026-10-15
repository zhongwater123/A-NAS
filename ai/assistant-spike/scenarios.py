"""Conversations with a member's assistant, with the product service's side of
in-turn approvals (#111).

Runs as root in the spike container, with the Hermes runtime's Python, and
plays the product service on whichever channel the sandbox runs: `hermes serve`
(JSON-RPC on /api/ws through serve_client.py, the default) or the API Server
(session stream, with SPIKE_CHANNEL=api_server). When Hermes asks for approval
it finds the call the request is about (the last start of 提交更改 in that
session or run; Hermes runs MCP calls one at a time), has the change broker
prepare that change from the call's arguments, and answers as the scenario
says the member does. Approving registers the member's grant with the broker
before Hermes hears "once"; the MCP tool can apply nothing without it.

  scenarios.py FILE [--answer ANSWER] [--user alice]

Each line of FILE starts a new conversation, and a line starting with "+"
continues the one before; the staging area is emptied before each conversation.
A line is "<answers> | <message>" or just "<message>" (answers from --answer,
default deny). The answers are what the member does with each approval request
of the turn, in order, the last one repeating:
  approve  register the grant, then answer "once"
  deny     reject the change, answer "deny"
  wait     do not answer, so Hermes times out (approvals.timeout)
  stop     stop the turn (session.interrupt; POST /v1/runs/{id}/stop) instead of answering
  late     approve only after Hermes timed out
  forge    answer "once" without the member's grant: Hermes alone approves
A request for an invalid submission gets no card: the product service lets it
through so the MCP tool tells the assistant what is wrong with it.
"""
import argparse
import asyncio
import http.client
import json
import shutil
import socket
import sys
import time
from pathlib import Path

parser = argparse.ArgumentParser()
parser.add_argument("file")
parser.add_argument("--answer", default="deny")
parser.add_argument("--user", default="alice")
opts = parser.parse_args()

USER = opts.user
SOCK = f"/run/a-nas-assistant/{USER}.sock"
KEY = f"k7Qx9vT2mR4pL8wNz3Jd-{USER}"
BROKER = "/run/anas-spike-broker/broker.sock"
SUBMIT = "mcp__anas__submit_changes"
HOME = Path(f"/srv/a-nas/data/assistant/{USER}/files")
CHANNEL = "api_server" if Path("/etc/systemd/system/anas-assistant@.service.d/channel.conf").exists() else "serve"
# The real model's gateway or the fake model, whichever this container runs.
MODEL_LOG = Path("/var/log/gateway.jsonl" if Path("/etc/anas-spike/model.key").exists() else "/var/log/fake-model.jsonl")
APPROVAL_TIMEOUT = int(next((line.split(":")[1] for line in open("/etc/hermes/config.yaml")
                             if line.startswith("  timeout:")), "300"))


class UDS(http.client.HTTPConnection):
    def __init__(self, path, timeout=900):
        super().__init__("uds", timeout=timeout)
        self.path = path

    def connect(self):
        self.sock = socket.socket(socket.AF_UNIX)
        self.sock.settimeout(self.timeout)
        self.sock.connect(self.path)


def call(sock, method, path, body=None, headers=None):
    conn = UDS(sock)
    conn.request(method, path, json.dumps(body, ensure_ascii=False).encode() if body is not None else None,
                 {"Content-Type": "application/json", **(headers or {})})
    resp = conn.getresponse()
    data = resp.read()
    try:
        return resp.status, json.loads(data or b"{}")
    except ValueError:
        return resp.status, data.decode(errors="replace")


def hermes(method, path, body=None):
    return call(SOCK, method, path, body, {"Authorization": f"Bearer {KEY}"})


def broker(method, path, body=None):
    sep = "&" if "?" in path else "?"
    return call(BROKER, method, f"{path}{sep}account={USER}", body)


def short(value, n=160):
    text = value if isinstance(value, str) else json.dumps(value, ensure_ascii=False)
    text = text.replace("\n", "⏎")
    return text if len(text) <= n else text[:n] + "…"


def card(change):
    """What the member sees: one line per operation, with the new text of each write."""
    lines = [f"card {change['id']}: {change['summary']}"]
    for op in change["operations"]:
        line = f"      {op['op']} {op['path']}" + (f" → {op['to']}" if op.get("to") else "")
        if op["op"] == "write":
            line += f"  {'改写' if op.get('before') is not None else '新建'}为 {short(op.get('after'), 80)}"
        lines.append(line)
    return "\n".join(lines)


class Turn:
    """The product service's part of one turn, whatever the channel: respond(choice) answers
    Hermes and says whether the approval was still pending, stop() stops the turn."""

    def __init__(self, session, said, answers):
        self.session, self.said, self.answers = session, said, answers
        self.submissions = {}   # session or run -> arguments of the last 提交更改 started in it
        self.changes, self.late = [], []

    def answer(self):
        return self.answers.pop(0) if len(self.answers) > 1 else self.answers[0]

    def started(self, key, name, args):
        print(f"  → {name} {short(args)}")
        if name == SUBMIT and isinstance(args, dict):
            self.submissions[key] = args

    async def approval(self, key, request, fields, respond, stop):
        print(f"  ⏸ approval {short(fields.get('command'), 70)} "
              f"{json.dumps({k: v for k, v in fields.items() if k not in ('command', 'description')}, ensure_ascii=False)}")
        args = self.submissions.pop(key, None)
        if args is None:
            # Not a call of 提交更改: nothing to show the member, so it is refused.
            print("    no 提交更改 call in flight → deny")
            return await respond("deny")
        status, change = broker("POST", "/changes/prepare", {"sessionId": self.session, "runId": fields.get("run_id", ""),
                                                             "requestId": request, **args})
        if status == 422:
            print(f"    invalid, no card → once: {short(change['error'], 200)}")
            return await respond("once")
        self.changes.append(change["id"])
        print("    " + card(change))
        what = self.answer()
        print(f"    member: {what}")
        if what == "approve":
            broker("POST", f"/changes/{change['id']}/approve")
            if not await respond("once"):
                broker("POST", f"/changes/{change['id']}/expire")
        elif what == "deny":
            broker("POST", f"/changes/{change['id']}/reject")
            await respond("deny")
        elif what == "forge":
            await respond("once")
        elif what == "stop":
            await stop()
        elif what == "late":
            self.late.append((change["id"], respond))

    async def finish(self):
        for change_id, respond in self.late:
            print(f"    late approval of {change_id}:")
            broker("POST", f"/changes/{change_id}/approve")
            if not await respond("once"):
                broker("POST", f"/changes/{change_id}/expire")
        # The turn is over: nothing it asked for can be approved any more.
        for change_id in self.changes:
            broker("POST", f"/changes/{change_id}/expire")


# ---- hermes serve: JSON-RPC on /api/ws, as Hermes Desktop ----

async def serve_turn(gw, sid, turn):
    from serve_client import RpcError

    def approval_answer(rid):
        async def respond(choice):
            try:
                result = await gw.call("approval.respond", {"session_id": sid, "request_id": rid, "choice": choice})
            except RpcError as exc:
                print(f"    Hermes ← {choice}: error {short(str(exc), 160)}")
                return False
            print(f"    Hermes ← {choice}: {short(result, 160)}")
            return bool((result or {}).get("resolved"))
        return respond

    async def stop():
        print(f"    session.interrupt → {short(await gw.call('session.interrupt', {'session_id': sid}), 120)}")

    await gw.call("prompt.submit", {"session_id": sid, "text": turn.said})
    while True:
        frame = await asyncio.wait_for(gw.events.get(), APPROVAL_TIMEOUT + 600)
        kind, payload = frame.get("type"), frame.get("payload") or {}
        if kind == "server.request":
            params = payload.get("params") or {}
            if payload.get("method") == "approval" and params.get("session_id") == sid:
                await turn.approval(sid, params.get("request_id"), params, approval_answer(params.get("request_id")), stop)
            continue
        if frame.get("session_id") not in (None, sid):
            continue
        if kind == "tool.start":
            turn.started(sid, payload.get("name"), payload.get("args"))
        elif kind == "tool.complete" and str(payload.get("name", "")).startswith("mcp__anas__"):
            print(f"  ⇠ {payload['name']}: {short(payload.get('result') or payload.get('result_text') or payload.get('error'), 300)}")
        elif kind == "message.complete":
            if payload.get("text"):
                print(f"  answer: {short(payload['text'], 400)}")
            print(f"  turn {payload.get('status') or 'complete'}")
            break
    await turn.finish()


async def serve_conversation(lines):
    sys.path.insert(0, "/spike")
    from serve_client import Gateway

    async with Gateway(USER, timeout=APPROVAL_TIMEOUT + 600) as gw:
        # Without this Hermes withdraws every approval at once: this client answers server requests.
        await gw.call("client.capabilities", {"server_requests": True})
        sid = await gw.new_session()
        for answers, said in lines:
            yield sid, Turn(sid, said, answers), lambda turn: serve_turn(gw, sid, turn)


# ---- API Server: session stream, /v1/runs/{id}/approval ----

def events(resp):
    name, data = None, []
    while True:
        line = resp.readline()
        if not line:
            return
        line = line.decode().rstrip("\r\n")
        if line.startswith("event:"):
            name = line[6:].strip()
        elif line.startswith("data:"):
            data.append(line[5:].strip())
        elif not line and name:
            yield name, json.loads("".join(data) or "{}")
            name, data = None, []


async def api_turn(session, turn):
    def approval_answer(run, request):
        async def respond(choice):
            status, body = hermes("POST", f"/v1/runs/{run}/approval", {"choice": choice, "request_id": request})
            print(f"    Hermes ← {choice}: {status} {short(body, 160)}")
            return status == 200
        return respond

    conn = UDS(SOCK)
    conn.request("POST", f"/api/sessions/{session}/chat/stream", json.dumps({"input": turn.said}).encode(),
                 {"Authorization": f"Bearer {KEY}", "Content-Type": "application/json"})
    resp = conn.getresponse()
    if resp.status != 200:
        print(f"  stream refused: {resp.status} {resp.read()[:300]}")
        return
    for name, event in events(resp):
        if name == "tool.started":
            turn.started(event["run_id"], event.get("tool_name"), event.get("args"))
        elif name == "approval.request":
            run = event["run_id"]

            async def stop(run=run):
                status, body = hermes("POST", f"/v1/runs/{run}/stop", {})
                print(f"    stop → {status} {short(body, 120)}")
            fields = {k: v for k, v in event.items() if k not in ("event", "timestamp", "ts", "seq")}
            await turn.approval(run, event.get("request_id"), fields, approval_answer(run, event.get("request_id")), stop)
        elif name.startswith("run.") and name != "run.started":
            print(f"  {name}")
        elif name == "error":
            print(f"  error {short(event, 300)}")
    await turn.finish()
    # What the model got from the A-NAS tools, and the answer, from the stored transcript.
    _, page = hermes("GET", f"/api/sessions/{session}/messages?inline_images=false")
    rows = page.get("data", []) if isinstance(page, dict) else []
    start = max((i for i, r in enumerate(rows) if r.get("role") == "user" and turn.said in str(r.get("content"))), default=-1)
    for row in rows[start + 1:]:
        if row.get("role") == "tool" and row.get("tool_name", "").startswith("mcp__anas__"):
            print(f"  ⇠ {row['tool_name']}: {short(row.get('content'), 300)}")
        elif row.get("role") == "assistant" and row.get("content"):
            print(f"  answer: {short(row['content'], 400)}")


async def api_conversation(lines):
    status, created = hermes("POST", "/api/sessions", {})
    session = created["session"]["id"]
    for answers, said in lines:
        yield session, Turn(session, said, answers), lambda turn: api_turn(session, turn)


async def converse(paragraph):
    shutil.rmtree(HOME / "待确认", ignore_errors=True)
    lines = []
    for line in paragraph:
        answers, said = line.split("|", 1) if "|" in line else (opts.answer, line)
        lines.append(([a.strip() for a in answers.split(",")], said.strip()))
    session = None
    async for session, turn, run in (serve_conversation if CHANNEL == "serve" else api_conversation)(lines):
        log_before = len(MODEL_LOG.read_text().splitlines()) if MODEL_LOG.exists() else 0
        started = time.time()
        print(f"\n### [{','.join(turn.answers)}] {turn.said}")
        await run(turn)
        if MODEL_LOG.exists():
            records = [json.loads(l) for l in MODEL_LOG.read_text().splitlines()[log_before:]]
            tokens = sum((r.get("usage") or {}).get("total_tokens", 0) for r in records)
            print(f"  {len(records)} model requests, {tokens} tokens, {time.time() - started:.0f} s")
    _, page = broker("GET", "/changes")
    for change in page["changes"]:
        if change.get("sessionId") == session:
            print(f"  change {change['id']}: {change['status']}{' — ' + change['error'] if change.get('error') else ''}")


async def main():
    paragraphs = []
    for raw in Path(opts.file).read_text(encoding="utf-8").splitlines():
        if raw.startswith("+") and paragraphs:
            paragraphs[-1].append(raw[1:].strip())
        elif raw.strip():
            paragraphs.append([raw.strip()])
    print(f"channel {CHANNEL}, approvals.timeout = {APPROVAL_TIMEOUT} s")
    for paragraph in paragraphs:
        await converse(paragraph)


asyncio.run(main())
