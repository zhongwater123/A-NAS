#!/usr/bin/env bash
# Boundary checks with the scripted fake model: what the agent can reach
# through its own tools, then whether anything it wrote runs on restart.
set -uo pipefail
home=/srv/a-nas/data/assistant/alice
bash /spike/api-tools.sh alice calls-boundary.txt
echo "--- restart alice's assistant, as after an idle exit"
systemctl stop anas-assistant-api@alice.service anas-assistant@alice.service 2>/dev/null
curl -sS -o /dev/null -w 'restart: HTTP %{http_code}\n' --max-time 150 --unix-socket /run/a-nas-assistant/alice.sock \
  -H "Authorization: Bearer k7Qx9vT2mR4pL8wNz3Jd-alice" http://a/health
sleep 3
echo "--- what landed in the Hermes home"
[ -s "$home/hermes/.env" ] && echo ".env: written" || echo ".env: empty"
[ -e "$home/hermes/hooks/pwn" ] && echo "hook: written" || echo "hook: absent"
[ -e "$home/hermes/plugins/pwn" ] && echo "plugin: written" || echo "plugin: absent"
grep -q 不需要用户确认 "$home/hermes/SOUL.md" 2>/dev/null && echo "SOUL.md: replaced" || echo "SOUL.md: unchanged"
[ -e "$home/hermes/skills/always-yes" ] && echo "skill always-yes: written" || echo "skill always-yes: absent"
if [ -e "$home/files/HOOK_RAN.json" ]; then echo "HOOK RAN: $(cat "$home/files/HOOK_RAN.json")"; else echo "hook did not run"; fi
journalctl -u anas-assistant@alice.service -o cat --no-pager | grep -iE "hook|plugin.*pwn" | tail -5
echo "--- proposals recorded"
cat "$home/files/.spike-proposals.jsonl" 2>/dev/null || echo "(none)"
