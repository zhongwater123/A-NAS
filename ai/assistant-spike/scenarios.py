"""Conversations with a member's assistant, with the product service's side of
in-turn approvals (#111), and the scored scenarios that check what it did.

Runs as root in the spike container, with the Hermes runtime's Python, and
plays the product service on whichever channel the sandbox runs: `hermes serve`
(JSON-RPC on /api/ws through serve_client.py, the default) or the API Server
(session stream, with SPIKE_CHANNEL=api_server). When Hermes asks for approval
it finds the call the request is about (the last start of 提交更改 in that
session or run; Hermes runs MCP calls one at a time), has the change broker
prepare that change from the call's arguments, and answers as the scenario
says the member does. Approving registers the member's grant with the broker
before Hermes hears "once"; the MCP tool can apply nothing without it.

  scenarios.py FILE.txt [--answer ANSWER] [--user alice]
  scenarios.py FILE.yaml [--case ID ...] [--repeat N] [--all-runs] [--out DIR] [--baseline FILE] [--user alice]

A .txt file only prints the conversations. Each line starts a new conversation,
and a line starting with "+" continues the one before; the staging area is
emptied before each conversation. A line is "<answers> | <message>" or just
"<message>" (answers from --answer, default deny). The answers are what the
member does with each approval request of the turn, in order, the last one
repeating:
  approve  register the grant, then answer "once"
  deny     reject the change, answer "deny"
  wait     do not answer, so Hermes times out (approvals.timeout)
  stop     stop the turn (session.interrupt; POST /v1/runs/{id}/stop) instead of answering
  late     approve only after Hermes timed out
  forge    answer "once" without the member's grant: Hermes alone approves
A request for an invalid submission gets no card: the product service lets it
through so the MCP tool tells the assistant what is wrong with it.

A .yaml file is a suite with assertions (docs/development/assistant-scenarios.md):
each case runs as often as its kind asks, from the seed put back before every
run, and is scored by vnas_eval. The runs, the summary and the report go to
--out; with --baseline the report lists what changed against it, and the exit
status is the report's (0 nothing to stop, 1 a safety case newly fails, 2 a
safety case could not complete). VNAS_JUDGE_KEY_FILE switches on the
llm-rubric judge (vnas_eval/judge.py).
"""
import argparse
import asyncio
import functools
import hashlib
import http.client
import json
import os
import shutil
import socket
import subprocess
import sys
import time
from pathlib import Path

from vnas_eval.member import clarify_reply, described

parser = argparse.ArgumentParser()
parser.add_argument("file")
parser.add_argument("--answer", default="deny")
parser.add_argument("--clarify", default="cancel", help="how the member answers a clarify request in a .txt file "
                    "(cancel, wait, first or the answer's text; vnas_eval/member.py)")
parser.add_argument("--user", default="alice")
parser.add_argument("--case", action="append", default=[], help="only this case of a .yaml suite (repeatable)")
parser.add_argument("--repeat", type=int, help="runs per case, instead of the kind's")
parser.add_argument("--out", help="where a .yaml suite's results go (default /var/lib/vnas-eval/results/...)")
parser.add_argument("--baseline", help="the accepted summary of the same suite and model to compare with")
parser.add_argument("--model", help="the model under test, if not the one in /etc/hermes/config.yaml")
parser.add_argument("--all-runs", action="store_true",
                    help="run every one of a case's runs even after one fails, to see how often it fails")
opts = parser.parse_args()

USER = opts.user
SOCK = f"/run/a-nas-assistant/{USER}.sock"
KEY = f"k7Qx9vT2mR4pL8wNz3Jd-{USER}"
BROKER = "/run/anas-spike-broker/broker.sock"
SUBMIT = "mcp__anas__submit_changes"
# The tools whose results a scenario prints: A-NAS's own and the companion's (#412).
A_NAS_TOOLS = ("mcp__anas__", "mcp__companion__")
HOME = Path(f"/srv/a-nas/data/assistant/{USER}/files")
CHANNEL = "api_server" if Path("/etc/systemd/system/anas-assistant@.service.d/channel.conf").exists() else "serve"
# The real model's gateway or the fake model, whichever this container runs.
REAL_MODEL = Path("/etc/anas-spike/model.key").exists()
MODEL_LOG = Path("/var/log/gateway.jsonl" if REAL_MODEL else "/var/log/fake-model.jsonl")
APPROVAL_TIMEOUT = int(next((line.split(":")[1] for line in Path("/etc/hermes/config.yaml").read_text().splitlines()
                             if line.startswith("  timeout:")), "300"))
RESULT_CHARS = 2000   # of a tool's result kept in a run record
RUNTIME_PYTHON = "/opt/a-nas/assistant-runtimes/spike/venv/bin/python"


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


def kept(value):
    """A tool's result as a run record keeps it: text, cut to RESULT_CHARS."""
    text = value if isinstance(value, str) else json.dumps(value, ensure_ascii=False)
    return text if len(text) <= RESULT_CHARS else text[:RESULT_CHARS] + "…"


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
    Hermes and says whether the approval was still pending, stop() stops the turn. record is
    the turn's evidence for scoring (vnas_eval.asserts)."""

    def __init__(self, session, said, answers, clarify=("cancel",)):
        self.session, self.said, self.answers, self.clarifies = session, said, list(answers), list(clarify)
        self.submissions = {}   # session or run -> arguments of the last 提交更改 started in it
        self.changes, self.late = [], []
        self.record = {"say": said, "answers": list(answers), "tools": [], "approvals": [], "changes": [],
                       "clarifications": [], "answer": "", "status": "complete"}

    def answer(self):
        return self.answers.pop(0) if len(self.answers) > 1 else self.answers[0]

    async def clarify(self, questions, reply):
        """The member's part of a clarify request (vnas_eval.member): reply(result) answers it."""
        how = self.clarifies.pop(0) if len(self.clarifies) > 1 else self.clarifies[0]
        response = clarify_reply(how, questions)
        print(f"  ? clarify {short([q.get('question') for q in questions], 200)}")
        print(f"    member: {how} → {short(response, 160)}")
        self.record["clarifications"].append({"how": how, "questions": described(questions, response)})
        if response is not None:
            await reply(response)

    def started(self, key, name, args):
        print(f"  → {name} {short(args)}")
        self.record["tools"].append({"name": name, "args": args})
        if name == SUBMIT and isinstance(args, dict):
            self.submissions[key] = args

    def completed(self, name, result):
        """The result of the latest call of that tool still without one."""
        for tool in reversed(self.record["tools"]):
            if tool["name"] == name and "result" not in tool:
                tool["result"] = kept(result)
                return

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
        self.record["approvals"].append({"answer": what, "change": {
            "id": change["id"], "summary": change["summary"],
            "operations": [{k: op[k] for k in ("op", "path", "to") if op.get(k)} for op in change["operations"]]}})
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
            elif payload.get("method") == "clarify" and params.get("session_id") == sid:
                # Answered as a response to the request's own id, not through an RPC like approvals.
                await turn.clarify(params.get("questions") or [], functools.partial(gw.reply, payload.get("id")))
            continue
        if frame.get("session_id") not in (None, sid):
            continue
        if kind == "tool.start":
            turn.started(sid, payload.get("name"), payload.get("args"))
        elif kind == "tool.complete":
            result = payload.get("result") or payload.get("result_text") or payload.get("error")
            turn.completed(payload.get("name"), result)
            if str(payload.get("name", "")).startswith(A_NAS_TOOLS):
                print(f"  ⇠ {payload['name']}: {short(result, 300)}")
        elif kind == "message.complete":
            if payload.get("text"):
                print(f"  answer: {short(payload['text'], 400)}")
            turn.record["answer"] = payload.get("text") or ""
            turn.record["status"] = payload.get("status") or "complete"
            print(f"  turn {turn.record['status']}")
            break
    await turn.finish()


async def serve_conversation(lines):
    sys.path.insert(0, "/spike")
    from serve_client import Gateway

    async with Gateway(USER, timeout=APPROVAL_TIMEOUT + 600) as gw:
        # Without this Hermes withdraws every approval at once: this client answers server requests.
        await gw.call("client.capabilities", {"server_requests": True})
        sid = await gw.new_session()
        for answers, said, clarify in lines:
            yield sid, Turn(sid, said, answers, clarify), lambda turn: serve_turn(gw, sid, turn)


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
        turn.record["status"] = "failed"
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
            turn.record["status"] = name
        elif name == "error":
            print(f"  error {short(event, 300)}")
    await turn.finish()
    # What the model got from the tools, and the answer, from the stored transcript.
    _, page = hermes("GET", f"/api/sessions/{session}/messages?inline_images=false")
    rows = page.get("data", []) if isinstance(page, dict) else []
    start = max((i for i, r in enumerate(rows) if r.get("role") == "user" and turn.said in str(r.get("content"))), default=-1)
    for row in rows[start + 1:]:
        if row.get("role") == "tool":
            turn.completed(row.get("tool_name", ""), row.get("content"))
            if row.get("tool_name", "").startswith(A_NAS_TOOLS):
                print(f"  ⇠ {row['tool_name']}: {short(row.get('content'), 300)}")
        elif row.get("role") == "assistant" and row.get("content"):
            print(f"  answer: {short(row['content'], 400)}")
            turn.record["answer"] = row["content"] if isinstance(row["content"], str) else json.dumps(row["content"])


async def api_conversation(lines):
    _, created = hermes("POST", "/api/sessions", {})
    session = created["session"]["id"]
    # The API Server has no clarify request: Turn.clarifies goes unused there.
    for answers, said, clarify in lines:
        yield session, Turn(session, said, answers, clarify), lambda turn: api_turn(session, turn)


# ---- one conversation ----

def model_records(before):
    return [json.loads(record) for record in MODEL_LOG.read_text().splitlines()[before:]] if MODEL_LOG.exists() else []


def model_error(records, status):
    """A model failure that explains the turn: the turn failed or its last request did not succeed."""
    bad = [r for r in records if r.get("status", 200) != 200]
    if bad and (status in ("failed", "error", "run.failed") or records[-1].get("status", 200) != 200):
        return f"HTTP {bad[-1].get('status')} ({len(bad)} of {len(records)} requests)"
    return None


async def converse(lines):
    """One conversation of (answers, said) turns: the turns' records and what went wrong around them."""
    shutil.rmtree(HOME / "待确认", ignore_errors=True)
    turns, errors, session = [], [], None
    try:
        async for session, turn, run in (serve_conversation if CHANNEL == "serve" else api_conversation)(lines):  # noqa: B007
            log_before = len(MODEL_LOG.read_text().splitlines()) if MODEL_LOG.exists() else 0
            started = time.time()
            print(f"\n### [{','.join(turn.answers)}] {turn.said}")
            await run(turn)
            records = model_records(log_before)
            tokens = sum((r.get("usage") or {}).get("total_tokens", 0) for r in records)
            turn.record.update(requests=len(records), tokens=tokens, seconds=round(time.time() - started, 1))
            if problem := model_error(records, turn.record["status"]):
                errors.append({"kind": "model", "turn": len(turns) + 1, "detail": problem})
            turns.append(turn.record)
            if MODEL_LOG.exists():
                print(f"  {len(records)} model requests, {tokens} tokens, {turn.record['seconds']:.0f} s")
    except TimeoutError:
        errors.append({"kind": "timeout", "turn": len(turns) + 1, "detail": "the turn did not end"})
        print(f"  timeout in turn {len(turns) + 1}")
    except Exception as exc:  # noqa: BLE001  any failure of the harness itself makes the run an error, not a crash
        errors.append({"kind": "runner", "turn": len(turns) + 1, "detail": f"{type(exc).__name__}: {exc}"})
        print(f"  runner error in turn {len(turns) + 1}: {type(exc).__name__}: {exc}")
    _, page = broker("GET", "/changes")
    final = {c["id"]: c for c in page["changes"] if c.get("sessionId") == session} if session else {}
    for change in final.values():
        print(f"  change {change['id']}: {change['status']}{' — ' + change['error'] if change.get('error') else ''}")
    for record in turns:
        record["changes"] = [{"id": a["change"]["id"], "status": final.get(a["change"]["id"], {}).get("status"),
                              "error": final.get(a["change"]["id"], {}).get("error")} for a in record["approvals"]]
    return turns, errors


async def print_only(path):
    paragraphs = []
    for raw in Path(path).read_text(encoding="utf-8").splitlines():
        if raw.startswith("+") and paragraphs:
            paragraphs[-1].append(raw[1:].strip())
        elif raw.strip():
            paragraphs.append([raw.strip()])
    for paragraph in paragraphs:
        lines = []
        for line in paragraph:
            answers, said = line.split("|", 1) if "|" in line else (opts.answer, line)
            lines.append(([a.strip() for a in answers.split(",")], said.strip(), [opts.clarify]))
        await converse(lines)


# ---- a scored suite ----

def digest(paths):
    """One short hash over the files' names and contents, read in chunks (the runtime is ~130 MB)."""
    h = hashlib.sha256()
    for path in sorted(paths):
        h.update(str(path).encode() + b"\0")
        with open(path, "rb") as f:
            while chunk := f.read(1 << 20):
                h.update(chunk)
    return "sha256:" + h.hexdigest()[:16]


def environment():
    """What a result depends on besides the scenarios, for the report's comparison."""
    from ruamel.yaml import YAML
    config = YAML(typ="safe", pure=True).load(Path("/etc/hermes/config.yaml").read_text(encoding="utf-8")) or {}
    pin = next((line.split("=", 1)[1] for line in Path("/spike/run.sh").read_text().splitlines()
                if line.startswith("commit=")), None)
    runtime = Path("/spike-out/runtime.tar.gz")
    spike = Path("/spike")
    return {"model": opts.model or (config.get("model") or {}).get("default"), "channel": CHANNEL, "hermes": pin,
            "runtime": digest([runtime]) if runtime.exists() else None,
            "repo": os.environ.get("VNAS_EVAL_REPO"),
            "skills": digest([*spike.glob("skills/*/SKILL.md"), spike / "member-skill.md", spike / "managed-config.yaml"]),
            "upstream_cache": None if not REAL_MODEL else "bypassed" if Path("/etc/anas-spike/upstream-no-cache").exists() else "allowed",
            "approval_timeout": APPROVAL_TIMEOUT}


def stop_instance():
    """The member's instance stopped, so it starts again from the restored home on the next connection."""
    subprocess.run(["/usr/bin/systemctl", "stop", f"anas-assistant-api@{USER}.service", f"anas-assistant@{USER}.service"], check=False)


def sync_bundled_skills(layout):
    """Hermes copies its bundled skills into the member's home on a first session, which counted
    them as created by the first run. Copy them as the member before the seed is taken, so every
    run starts with them (vnas_eval.sandbox.Seed keeps the folder whole)."""
    subprocess.run(["/usr/bin/setpriv", f"--reuid={USER}", f"--regid={USER}", "--init-groups", "--",
                    "/usr/bin/env", f"HERMES_HOME={layout.hermes}", f"HOME={HOME}", RUNTIME_PYTHON, "-c",
                    "from tools.skills_sync import sync_skills; sync_skills(quiet=True)"], check=True)


def failures(result):
    rows = [(f"turn {i + 1}", r) for i, rs in enumerate(result["turns"]) for r in rs] + [("after", r) for r in result["after"]]
    return [f"    ✗ {where} {r['type']}: {r['detail']}" + (f"（{r['note']}）" if r["note"] else "")
            for where, r in rows if r["pass"] is False]


async def run_suite(path):
    from ruamel.yaml import YAML

    from vnas_eval.asserts import load_suite, needs_retry, score_run
    from vnas_eval.judge import Judge
    from vnas_eval.report import compare, render, summarize
    from vnas_eval.sandbox import HostWorld, Layout, Seed, plant, skill_files

    data = YAML(typ="safe", pure=True).load(Path(path).read_text(encoding="utf-8"))
    cases = load_suite(data)
    if opts.case:
        unknown = set(opts.case) - {c.id for c in cases}
        if unknown:
            sys.exit(f"no such case: {sorted(unknown)}")
        cases = [c for c in cases if c.id in opts.case]
    other_users = sorted({c.user for c in cases} - {USER})
    if other_users:
        sys.exit(f"cases for {other_users}: run them with --user")
    for case in cases:
        case.repeat = opts.repeat or case.repeat
    have = {"real-model": REAL_MODEL}
    missing = [r for r in data.get("requires") or [] if not have.get(r)]
    if missing:
        print(f"skipped {data['suite']}: needs {missing}")
        return 0

    env = environment()
    judge = Judge.from_env(subject_model=env["model"])
    env["judge"] = judge.model if judge else None
    model_slug = str(env["model"]).replace("/", "_")
    out = Path(opts.out or f"/var/lib/vnas-eval/results/{time.strftime('%Y%m%d-%H%M%S')}-{data['suite']}-{model_slug}")
    out.mkdir(parents=True, exist_ok=True)
    layout = Layout(USER)
    seed = None
    if data.get("reset", "per-run") == "per-run":
        # The seed as seed.sh writes it, kept for every run to start from.
        subprocess.run(["/usr/bin/bash", "/spike/seed.sh", str(layout.data)], check=True)
        stop_instance()
        sync_bundled_skills(layout)
        seed = Seed(layout, Path("/var/lib/vnas-eval/seed"))
        seed.take()
    print(f"suite {data['suite']}: {len(cases)} cases, channel {CHANNEL}, model {env['model']}, judge {env['judge']}")

    records = {}
    with (out / "runs.jsonl").open("a", encoding="utf-8") as runs:
        for case in cases:
            outcomes = []
            while True:
                if seed:
                    stop_instance()
                    seed.restore()
                plant(layout, case.files)
                skills_before = skill_files(layout)
                print(f"\n======== {case.id} ({case.kind}) run {len(outcomes) + 1}")
                turns, errors = await converse([(t.answers, t.say, t.clarify) for t in case.turns])
                result = score_run(case, {"turns": turns, "errors": errors}, HostWorld(layout, skills_before), judge)
                record = {"schema": 1, "suite": case.suite, "case": case.id, "run": len(outcomes) + 1,
                          "env": env, "turns": turns, "errors": errors + result.get("errors", []), "result": result}
                runs.write(json.dumps(record, ensure_ascii=False) + "\n")
                runs.flush()
                records.setdefault(case.id, []).append(record)
                outcomes.append(result["outcome"])
                print(f"  ⇒ {result['outcome']}  should {result['should'][0]}/{result['should'][1]}"
                      f"{', ' + str(result['unjudged']) + ' unjudged' if result['unjudged'] else ''}")
                for line in failures(result):
                    print(line)
                if not needs_retry(case, outcomes, stop_on_fail=not opts.all_runs):
                    break
    if seed:
        stop_instance()
        seed.restore()
    if judge:
        env["judge_served"] = judge.served
    summary = summarize(data["suite"], env, cases, records)
    (out / "summary.json").write_text(json.dumps(summary, ensure_ascii=False, indent=1), encoding="utf-8")
    baseline = json.loads(Path(opts.baseline).read_text(encoding="utf-8")) if opts.baseline else None
    rep = compare(summary, baseline)
    text = render(rep)
    (out / "report.md").write_text(text, encoding="utf-8")
    print("\n" + text + f"\nresults: {out}")
    return rep["exit_code"]


async def main():
    print(f"channel {CHANNEL}, approvals.timeout = {APPROVAL_TIMEOUT} s")
    if opts.file.endswith((".yaml", ".yml")):
        return await run_suite(opts.file)
    await print_only(opts.file)
    return 0


sys.exit(asyncio.run(main()))
