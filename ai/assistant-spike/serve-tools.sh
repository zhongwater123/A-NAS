#!/usr/bin/env bash
# Which tools a `hermes serve` session offers the model, from a cold start: are the
# A-NAS MCP tools there on the very first turn, and do the switched-off tools stay off?
# The scripted calls (calls.txt by default) then go through one session over /api/ws.
set -uo pipefail
user=${1:-alice}
calls=${2:-calls.txt}
py=/opt/a-nas/assistant-runtimes/spike/venv/bin/python
log=/var/log/fake-model.jsonl
systemctl stop "anas-assistant-api@$user.service" "anas-assistant@$user.service" 2>/dev/null
before=$(wc -l < "$log" 2>/dev/null || echo 0)
"$py" /spike/serve_client.py "$user" calls "$calls"
echo "--- tools offered, in order of first appearance (fake model log)"
tail -n +$((before + 1)) "$log" | python3 -c '
import json, sys
seen = []
for n, line in enumerate(sys.stdin, 1):
    tools = json.loads(line)["tools"]
    if not tools:
        continue
    if not seen:
        print("first request after the cold start:", "MCP tools present" if any(t.startswith("mcp_") for t in tools) else "NO MCP tools")
    if tools not in [s for s, _ in seen]:
        seen.append((tools, n))
for tools, n in seen:
    print(f"from request {n}: {tools}")'
systemctl show "anas-assistant@$user.service" -p MemoryCurrent -p MemoryPeak -p TasksCurrent | tr '\n' ' '; echo
