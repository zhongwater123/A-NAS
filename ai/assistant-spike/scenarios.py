"""Conversations with a member's assistant, with the product service's side of
in-turn approvals (#111).

Runs as root in the spike container and plays the product service: it sends
each turn through Hermes's session stream and reads the events. When Hermes
asks for approval it finds the call the request is about (the last
tool.started of 提交更改 in this run; Hermes runs MCP calls one at a time),
has the change broker prepare that change from the call's arguments, and
answers as the scenario says the member does. Approving registers the
member's grant with the broker before Hermes hears "once"; the MCP tool can
apply nothing without it.

  scenarios.py FILE [--answer ANSWER] [--user alice]

Each line of FILE starts a new conversation, and a line starting with "+"
continues the one before; the staging area is emptied before each conversation.
A line is "<answers> | <message>" or just "<message>" (answers from --answer,
default deny). The answers are what the member does with each approval request
of the turn, in order, the last one repeating:
  approve  register the grant, then answer "once"
  deny     reject the change, answer "deny"
  wait     do not answer, so Hermes times out (approvals.timeout)
  stop     stop the turn (POST /v1/runs/{id}/stop) instead of answering
  late     approve only after Hermes timed out
  forge    answer "once" without the member's grant: Hermes alone approves
A request for an invalid submission gets no card: the product service lets it
through so the MCP tool tells the assistant what is wrong with it.
"""
import argparse
import http.client
import json
import shutil
import socket
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


class Turn:
    def __init__(self, session, said, answers):
        self.session, self.said, self.answers = session, said, answers
        self.submissions = {}   # run id -> arguments of the last 提交更改 started in it
        self.changes, self.late = [], []

    def answer(self):
        return self.answers.pop(0) if len(self.answers) > 1 else self.answers[0]

    def approval(self, event):
        run, request = event["run_id"], event.get("request_id")
        fields = {k: v for k, v in event.items() if k not in ("event", "timestamp", "ts", "seq", "command", "description")}
        print(f"  ⏸ approval.request {short(event.get('command'), 70)} {json.dumps(fields, ensure_ascii=False)}")
        args = self.submissions.pop(run, None)
        if args is None:
            # Not a call of 提交更改: nothing to show the member, so it is refused.
            print("    no 提交更改 call in flight → deny")
            return self.respond(run, request, "deny")
        status, change = broker("POST", "/changes/prepare", {"sessionId": self.session, "runId": run,
                                                             "requestId": request, **args})
        if status == 422:
            print(f"    invalid, no card → once: {short(change['error'], 200)}")
            return self.respond(run, request, "once")
        self.changes.append(change["id"])
        print("    " + card(change))
        what = self.answer()
        print(f"    member: {what}")
        if what == "approve":
            broker("POST", f"/changes/{change['id']}/approve")
            if self.respond(run, request, "once") != 200:
                broker("POST", f"/changes/{change['id']}/expire")
        elif what == "deny":
            broker("POST", f"/changes/{change['id']}/reject")
            self.respond(run, request, "deny")
        elif what == "forge":
            self.respond(run, request, "once")
        elif what == "stop":
            status, body = hermes("POST", f"/v1/runs/{run}/stop", {})
            print(f"    stop → {status} {short(body, 120)}")
        elif what == "late":
            self.late.append((run, request, change["id"]))

    def respond(self, run, request, choice):
        status, body = hermes("POST", f"/v1/runs/{run}/approval", {"choice": choice, "request_id": request})
        print(f"    Hermes ← {choice}: {status} {short(body, 160)}")
        return status

    def run(self):
        conn = UDS(SOCK)
        conn.request("POST", f"/api/sessions/{self.session}/chat/stream", json.dumps({"input": self.said}).encode(),
                     {"Authorization": f"Bearer {KEY}", "Content-Type": "application/json"})
        resp = conn.getresponse()
        if resp.status != 200:
            print(f"  stream refused: {resp.status} {resp.read()[:300]}")
            return
        for name, event in events(resp):
            if name == "tool.started":
                print(f"  → {event.get('tool_name')} {short(event.get('args'))}")
                if event.get("tool_name") == SUBMIT and isinstance(event.get("args"), dict):
                    self.submissions[event["run_id"]] = event["args"]
            elif name in ("tool.completed", "tool.failed") and event.get("tool_name") == SUBMIT:
                print(f"  {name} {short(event.get('preview'), 100)}")
            elif name == "approval.request":
                self.approval(event)
            elif name.startswith("run.") and name != "run.started":
                print(f"  {name}")
            elif name == "error":
                print(f"  error {short(event, 300)}")
        for run, request, change_id in self.late:
            print(f"    late approval of {change_id}:")
            broker("POST", f"/changes/{change_id}/approve")
            if self.respond(run, request, "once") != 200:
                broker("POST", f"/changes/{change_id}/expire")
        # The turn is over: nothing it asked for can be approved any more.
        for change_id in self.changes:
            broker("POST", f"/changes/{change_id}/expire")


def transcript_since(session, said):
    status, page = hermes("GET", f"/api/sessions/{session}/messages?inline_images=false")
    rows = page.get("data", []) if isinstance(page, dict) else []
    start = max((i for i, r in enumerate(rows) if r.get("role") == "user" and said in str(r.get("content"))), default=-1)
    return rows[start + 1:]


def converse(paragraph):
    shutil.rmtree(HOME / "待确认", ignore_errors=True)
    status, created = hermes("POST", "/api/sessions", {})
    session = created["session"]["id"]
    for line in paragraph:
        answers, said = line.split("|", 1) if "|" in line else (opts.answer, line)
        answers, said = [a.strip() for a in answers.split(",")], said.strip()
        log_before = len(MODEL_LOG.read_text().splitlines()) if MODEL_LOG.exists() else 0
        started = time.time()
        print(f"\n### [{','.join(answers)}] {said}")
        Turn(session, said, answers).run()
        for row in transcript_since(session, said):
            if row.get("role") == "tool" and row.get("tool_name", "").startswith("mcp__anas__"):
                print(f"  ⇠ {row['tool_name']}: {short(row.get('content'), 300)}")
            elif row.get("role") == "assistant" and row.get("content"):
                print(f"  answer: {short(row['content'], 400)}")
        if MODEL_LOG.exists():
            records = [json.loads(l) for l in MODEL_LOG.read_text().splitlines()[log_before:]]
            tokens = sum((r.get("usage") or {}).get("total_tokens", 0) for r in records)
            print(f"  {len(records)} model requests, {tokens} tokens, {time.time() - started:.0f} s")
    _, page = broker("GET", "/changes")
    for change in page["changes"]:
        if change.get("sessionId") == session:
            print(f"  change {change['id']}: {change['status']}{' — ' + change['error'] if change.get('error') else ''}")


paragraphs = []
for raw in Path(opts.file).read_text(encoding="utf-8").splitlines():
    if raw.startswith("+") and paragraphs:
        paragraphs[-1].append(raw[1:].strip())
    elif raw.strip():
        paragraphs.append([raw.strip()])
print(f"approvals.timeout = {APPROVAL_TIMEOUT} s")
for paragraph in paragraphs:
    converse(paragraph)
