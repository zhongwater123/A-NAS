"""Real-model scenarios: does the model follow the anas-files skill?

Runs as root in the spike container. Each line of the scenario file is one new
conversation with alice's assistant. Before each, the staging area is emptied;
after it, the model's tool calls are read back from the gateway log, together
with what landed in staging and what was submitted for confirmation.
"""
import http.client
import json
import shutil
import socket
import sys
import time
from pathlib import Path

USER = "alice"
SOCK = f"/run/a-nas-assistant/{USER}.sock"
KEY = f"k7Qx9vT2mR4pL8wNz3Jd-{USER}"
HOME = Path(f"/srv/a-nas/data/assistant/{USER}/files")
LOG = Path("/var/log/gateway.jsonl")
PROPOSALS = HOME / ".spike-proposals.jsonl"


class UDS(http.client.HTTPConnection):
    def connect(self):
        self.sock = socket.socket(socket.AF_UNIX)
        self.sock.settimeout(600)
        self.sock.connect(SOCK)


def lines(path):
    return path.read_text(encoding="utf-8").splitlines() if path.exists() else []


def short(value, n=160):
    text = value if isinstance(value, str) else json.dumps(value, ensure_ascii=False)
    text = text.replace("\n", "⏎")
    return text if len(text) <= n else text[:n] + "…"


def run(said):
    shutil.rmtree(HOME / "待确认", ignore_errors=True)
    log_before, proposals_before = len(lines(LOG)), len(lines(PROPOSALS))
    started = time.time()
    conn = UDS("a")
    conn.request("POST", "/v1/chat/completions",
                 json.dumps({"model": "hermes-agent", "messages": [{"role": "user", "content": said}]}),
                 {"Authorization": f"Bearer {KEY}", "Content-Type": "application/json"})
    reply = json.loads(conn.getresponse().read() or b"{}")
    answer = reply["choices"][0]["message"]["content"] if "choices" in reply else reply
    records = [json.loads(l) for l in lines(LOG)[log_before:]]
    turns = [r for r in records if r["tools"]]
    tokens = sum((r.get("usage") or {}).get("total_tokens", 0) for r in records)
    print(f"\n### {said}")
    print(f"{len(turns)} model turns, {len(records)} requests, {tokens} tokens, {time.time() - started:.0f} s")
    final = max(turns, key=lambda r: len(r["messages"]), default={"messages": []})
    for m in final["messages"]:
        if m.get("role") == "assistant":
            for call in m.get("tool_calls") or []:
                f = call["function"]
                print(f"  → {f['name']} {short(f.get('arguments', ''))}")
        elif m.get("role") == "tool":
            print(f"    ← {short(m.get('content', ''), 140)}")
    print(f"  answer: {short(answer, 400)}")
    staged = sorted(str(p.relative_to(HOME)) for p in (HOME / "待确认").rglob("*") if p.is_file())
    print(f"  staged: {staged or 'nothing'}")
    for p in lines(PROPOSALS)[proposals_before:]:
        print(f"  submitted: {short(json.loads(p), 400)}")


for line in Path(sys.argv[1]).read_text(encoding="utf-8").splitlines():
    if line.strip():
        run(line.strip())
