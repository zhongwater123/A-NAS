#!/usr/bin/env bash
# After api-tools.sh: memory after a conversation, whether a config the agent
# wrote for itself survives a restart, idle exit, and a second member.
set -uo pipefail
mem() { systemctl show "anas-assistant@$1.service" -p ActiveState -p MemoryCurrent -p MemoryPeak | tr '\n' ' '; echo; }
echo "alice after conversation: $(mem alice)"
echo "--- config the agent wrote into its own Hermes home:"
cat /srv/a-nas/data/assistant/alice/hermes/config.yaml 2>/dev/null | head -5 || echo "(none)"
systemctl stop anas-assistant-api@alice.service anas-assistant@alice.service
curl -sS --max-time 150 --unix-socket /run/a-nas-assistant/alice.sock -H "Authorization: Bearer k7Qx9vT2mR4pL8wNz3Jd-alice" http://a/v1/toolsets |
  python3 -c 'import json,sys; d=json.load(sys.stdin); print("enabled toolsets after restart:", [ts["name"] for ts in d["data"] if ts.get("enabled")])'
echo "--- idle exit (proxy --exit-idle-time=45s, assistant StopWhenUnneeded)"
for i in $(seq 1 30); do
  sleep 5
  state=$(systemctl is-active anas-assistant@alice.service)
  [ "$state" != active ] && { echo "alice stopped after ~$((i*5)) s idle: $state"; break; }
done
echo "--- two members at once"
start=$(date +%s.%N)
curl -sS -o /dev/null --max-time 150 --unix-socket /run/a-nas-assistant/alice.sock -H "Authorization: Bearer k7Qx9vT2mR4pL8wNz3Jd-alice" http://a/health &
curl -sS -o /dev/null --max-time 150 --unix-socket /run/a-nas-assistant/bob.sock -H "Authorization: Bearer k7Qx9vT2mR4pL8wNz3Jd-bob" http://a/health &
wait
python3 -c "import sys,time; print('both cold-started in %.1f s' % (time.time() - float(sys.argv[1])))" "$start"
sleep 10
echo "alice: $(mem alice)"
echo "bob:   $(mem bob)"
echo "--- wrong key and cross-member key"
curl -sS -o /dev/null -w 'alice socket, bob key: HTTP %{http_code}\n' --unix-socket /run/a-nas-assistant/alice.sock -H "Authorization: Bearer k7Qx9vT2mR4pL8wNz3Jd-bob" http://a/v1/toolsets
curl -sS -o /dev/null -w 'alice socket, no key: HTTP %{http_code}\n' --unix-socket /run/a-nas-assistant/alice.sock http://a/v1/toolsets
echo "--- fake model saw these tool lists:"
python3 -c '
import json
seen = {}
for line in open("/var/log/fake-model.jsonl"):
    r = json.loads(line); seen[tuple(r["tools"])] = seen.get(tuple(r["tools"]), 0) + 1
for tools, n in seen.items(): print(n, "requests offered", list(tools))'
python3 -c 'import json; print("system prompt chars:", max(json.loads(l)["system_chars"] for l in open("/var/log/fake-model.jsonl")))'
