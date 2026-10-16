#!/usr/bin/env bash
# `hermes serve` through a member's socket, as root outside the sandbox (the product
# service's position): cold start, how REST and /api/ws authenticate, memory.
set -uo pipefail
user=${1:-alice}
other=$([ "$user" = alice ] && echo bob || echo alice)
sock=/run/a-nas-assistant/$user.sock
py=/opt/a-nas/assistant-runtimes/spike/venv/bin/python
token=$(sed -n 's/^HERMES_DASHBOARD_SESSION_TOKEN=//p' "/etc/a-nas-assistant/tokens/$user.env")
other_token=$(sed -n 's/^HERMES_DASHBOARD_SESSION_TOKEN=//p' "/etc/a-nas-assistant/tokens/$other.env")
rest() { curl -sS --max-time 150 --unix-socket "$sock" -o /dev/null -w '%{http_code}' "$@"; }

systemctl stop "anas-assistant-api@$user.service" "anas-assistant@$user.service" 2>/dev/null
start=$(date +%s.%N)
echo "first GET /api/health (public): HTTP $(rest http://127.0.0.1:9119/api/health)"
python3 -c "import sys,time; print('cold start through socket activation: %.1f s' % (time.time() - float(sys.argv[1])))" "$start"
systemctl is-active "anas-assistant@$user.service" | sed 's/^/assistant unit: /'

echo "--- REST: /api/sessions"
echo "no token:                 HTTP $(rest http://127.0.0.1:9119/api/sessions)"
echo "other member's token:     HTTP $(rest -H "X-Hermes-Session-Token: $other_token" http://127.0.0.1:9119/api/sessions)"
echo "token in header:          HTTP $(rest -H "X-Hermes-Session-Token: $token" http://127.0.0.1:9119/api/sessions)"
echo "token as Bearer:          HTTP $(rest -H "Authorization: Bearer $token" http://127.0.0.1:9119/api/sessions)"
echo "token as ?token= on REST: HTTP $(rest "http://127.0.0.1:9119/api/sessions?token=$token")"
echo "Host: a (not loopback):   HTTP $(rest -H "X-Hermes-Session-Token: $token" http://a/api/sessions)"
echo "Origin: other site:       HTTP $(rest -H "X-Hermes-Session-Token: $token" -H 'Origin: http://evil.example' http://127.0.0.1:9119/api/sessions)"
echo "public paths without token: $(for p in health status config/defaults config/schema model/info dashboard/themes dashboard/plugins; do printf '%s=%s ' "$p" "$(rest "http://127.0.0.1:9119/api/$p")"; done)"

echo "--- /api/ws upgrade"
"$py" - "$sock" "$token" "$other_token" <<'PY'
import asyncio, sys
from websockets.asyncio.client import unix_connect
from websockets.exceptions import InvalidStatus, ConnectionClosed
sock, token, other = sys.argv[1:4]
async def attempt(label, query, **kw):
    try:
        async with unix_connect(sock, f"ws://{kw.pop('host', '127.0.0.1:9119')}/api/ws{query}", open_timeout=60, **kw) as ws:
            first = await asyncio.wait_for(ws.recv(), 30)
            print(f"{label:28} accepted, first frame {first[:60]}…")
    except InvalidStatus as exc:
        print(f"{label:28} HTTP {exc.response.status_code}")
    except ConnectionClosed as exc:
        print(f"{label:28} closed {exc.rcvd.code if exc.rcvd else '?'} {exc.rcvd.reason if exc.rcvd else ''}")
async def main():
    await attempt("no token", "")
    await attempt("other member's token", f"?token={other}")
    await attempt("token in a header only", "", additional_headers={"X-Hermes-Session-Token": token})
    await attempt("?token=", f"?token={token}")
    await attempt("?token= with Host a", f"?token={token}", host="a")
    await attempt("?token= with foreign Origin", f"?token={token}", origin="http://evil.example")
    await attempt("?token= with app:// Origin", f"?token={token}", origin="app://hermes")
asyncio.run(main())
PY

echo "--- one fake-model turn over /api/ws"
"$py" /spike/serve_client.py "$user" chat hello
systemctl show "anas-assistant@$user.service" -p MemoryCurrent -p MemoryPeak -p TasksCurrent | sed 's/^/mem /'
echo "--- does the token reach the logs?"
grep -rlF "$token" "/srv/a-nas/data/assistant/$user/hermes" 2>/dev/null | sed 's/^/token found in: /' || true
journalctl -u "anas-assistant@$user.service" -u "anas-assistant-api@$user.service" -o cat --no-pager | grep -cF "$token" | sed 's/^/journal lines with the token: /'
