#!/usr/bin/env bash
# Runs as root in the spike container (outside the sandbox), like the product
# service would: talks to a member's assistant only through its Unix socket.
set -uo pipefail
user=${1:-alice}
sock=/run/a-nas-assistant/$user.sock
api() { curl -sS --max-time 120 --unix-socket "$sock" -H "Authorization: Bearer k7Qx9vT2mR4pL8wNz3Jd-$user" "$@"; }
start=$(date +%s.%N)
api -o /dev/null -w 'first /health: HTTP %{http_code}\n' http://a/health
python3 -c "import sys,time; print('cold start through socket activation: %.1f s' % (time.time() - float(sys.argv[1])))" "$start"
systemctl is-active "anas-assistant@$user.service" | sed 's/^/assistant unit: /'
api http://a/v1/toolsets > "/tmp/toolsets-$user.json"
python3 - "$user" <<'PY'
import json, sys
data = json.load(open(f"/tmp/toolsets-{sys.argv[1]}.json"))
# /v1/toolsets lists every toolset Hermes knows; only the enabled ones reach the model.
for ts in data["data"]:
    if ts.get("enabled"):
        print("enabled toolset", ts["name"], "->", ", ".join(ts.get("tools", [])))
PY
api -H 'Content-Type: application/json' http://a/v1/chat/completions \
  -d '{"model":"hermes-agent","messages":[{"role":"user","content":"hello"}]}' | python3 -c 'import json,sys; d=json.load(sys.stdin); print("hello ->", d["choices"][0]["message"]["content"] if "choices" in d else d)'
systemctl show "anas-assistant@$user.service" -p MemoryCurrent -p MemoryPeak -p TasksCurrent | sed 's/^/mem /'
