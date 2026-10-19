#!/usr/bin/env bash
# A turn that outlives its client. `hermes serve` keeps a detached turn running after the
# /api/ws client leaves (tui_gateway session_lifecycle: orphan reap only once the turn is
# idle); the socket proxy here stops the whole instance 45 s after its last connection.
# The fake model answers "SLEEP <s>" after s seconds.
#   serve-background.sh [USER] [SECONDS]
set -uo pipefail
user=${1:-alice}
secs=${2:-30}
py=/opt/a-nas/assistant-runtimes/spike/venv/bin/python
sock=/run/a-nas-assistant/$user.sock
token=$(sed -n 's/^HERMES_DASHBOARD_SESSION_TOKEN=//p' "/etc/a-nas-assistant/tokens/$user.env")
systemctl stop "anas-assistant-api@$user.service" "anas-assistant@$user.service" 2>/dev/null
sid=$("$py" - "$user" "$secs" <<'PY'
import asyncio, sys
sys.path.insert(0, "/spike")
from serve_client import Gateway
async def main():
    async with Gateway(sys.argv[1]) as gw:
        sid = await gw.new_session()
        await gw.call("prompt.submit", {"session_id": sid, "text": f"SLEEP {sys.argv[2]}"})
        print(sid)  # the client leaves while the model is still answering
asyncio.run(main())
PY
)
echo "submitted SLEEP $secs in session $sid; client gone at $(date +%T)"
for i in $(seq 1 $(( (secs + 60) / 5 ))); do
  sleep 5
  state=$(systemctl is-active "anas-assistant@$user.service")
  [ "$state" != active ] && { echo "instance stopped ~$((i * 5)) s after the client left: $state"; break; }
done
[ "$state" = active ] && echo "instance still active $(( (secs + 60) / 5 * 5 )) s after the client left"
grep -hE "client_gone|orphan|nterrupt" "/srv/a-nas/data/assistant/$user/hermes/logs/agent.log" | tail -n 4 | cut -c1-200
# Reconnect (cold-starts the instance if it stopped) and read the stored transcript.
key=$(curl -sS --max-time 150 --unix-socket "$sock" -H "X-Hermes-Session-Token: $token" "http://127.0.0.1:9119/api/sessions?limit=5" |
  python3 -c 'import json,sys; d=json.load(sys.stdin); s=d.get("sessions", d) if isinstance(d, dict) else d; print(s[0]["id"] if s else "")')
curl -sS --max-time 150 --unix-socket "$sock" -H "X-Hermes-Session-Token: $token" "http://127.0.0.1:9119/api/sessions/$key/messages" |
  python3 -c '
import json, sys
d = json.load(sys.stdin)
msgs = d.get("messages", d) if isinstance(d, dict) else d
for m in msgs:
    meta = m.get("display_metadata")
    print("  stored", m.get("role"), repr(str(m.get("content"))[:80]), "display_metadata=" + json.dumps(meta) if meta else "")'
