#!/usr/bin/env bash
# After serve-tools.sh: memory after a conversation, idle exit through the socket proxy,
# and two members cold-starting at once. Compare with api-after.sh on the API Server.
set -uo pipefail
py=/opt/a-nas/assistant-runtimes/spike/venv/bin/python
mem() { systemctl show "anas-assistant@$1.service" -p ActiveState -p MemoryCurrent -p MemoryPeak -p TasksCurrent | tr '\n' ' '; echo; }
echo "alice after conversation: $(mem alice)"
echo "--- idle exit (proxy --exit-idle-time=45s, assistant StopWhenUnneeded)"
for i in $(seq 1 30); do
  sleep 5
  state=$(systemctl is-active anas-assistant@alice.service)
  [ "$state" != active ] && { echo "alice stopped after ~$((i*5)) s idle: $state ($(systemctl show -p Result --value anas-assistant@alice.service))"; break; }
done
echo "--- an open /api/ws keeps the instance up past the idle time"
"$py" - <<'PY' &
import asyncio, sys
sys.path.insert(0, "/spike")
from serve_client import Gateway
async def main():
    async with Gateway("alice") as gw:
        await asyncio.sleep(70)
asyncio.run(main())
PY
sleep 65; echo "after 65 s with one idle client: $(systemctl is-active anas-assistant@alice.service)"; wait
echo "--- two members at once"
systemctl stop anas-assistant-api@alice.service anas-assistant@alice.service anas-assistant-api@bob.service anas-assistant@bob.service 2>/dev/null
start=$(date +%s.%N)
"$py" /spike/serve_client.py alice ready & "$py" /spike/serve_client.py bob ready & wait
python3 -c "import sys,time; print('both ready in %.1f s' % (time.time() - float(sys.argv[1])))" "$start"
sleep 10
echo "alice: $(mem alice)"
echo "bob:   $(mem bob)"
