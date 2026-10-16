#!/usr/bin/env bash
# Cold start and memory of alice's instance on whichever channel setup.sh installed,
# measured one after another on an otherwise idle container (serve or API Server).
set -uo pipefail
py=/opt/a-nas/assistant-runtimes/spike/venv/bin/python
sock=/run/a-nas-assistant/alice.sock
if [ -e /etc/systemd/system/anas-assistant@.service.d/channel.conf ]; then
  channel=api_server; health=(-H "Authorization: Bearer k7Qx9vT2mR4pL8wNz3Jd-alice" http://a/health)
else
  channel=serve; health=(http://127.0.0.1:9119/api/health)
fi
mem() { systemctl show anas-assistant@alice.service -p MemoryCurrent -p MemoryPeak --value | awk '{printf "%d MB ", $1/1048576}'; }
stop() { systemctl stop anas-assistant-api@alice.service anas-assistant@alice.service anas-assistant-api@bob.service anas-assistant@bob.service 2>/dev/null; sleep 2; }
echo "channel: $channel"
for i in 1 2 3; do
  stop; start=$(date +%s.%N)
  curl -sS -o /dev/null --max-time 150 --unix-socket "$sock" "${health[@]}"
  python3 -c "import sys,time; print('cold start %d: %.1f s' % (int(sys.argv[2]), time.time() - float(sys.argv[1])))" "$start" "$i"
done
sleep 5; echo "idle after start (current, peak): $(mem)"
if [ "$channel" = serve ]; then
  "$py" /spike/serve_client.py alice chat hello >/dev/null
  echo "after one turn: $(mem)"
  "$py" /spike/serve_client.py alice calls calls.txt >/dev/null
else
  curl -sS -o /dev/null --max-time 150 --unix-socket "$sock" -H "Authorization: Bearer k7Qx9vT2mR4pL8wNz3Jd-alice"     -H 'Content-Type: application/json' -d '{"model":"hermes-agent","messages":[{"role":"user","content":"hello"}]}' http://a/v1/chat/completions
  echo "after one turn: $(mem)"
  bash /spike/api-tools.sh alice calls.txt >/dev/null
fi
echo "after the 21 scripted calls: $(mem)"
echo "processes: $(systemctl show anas-assistant@alice.service -p TasksCurrent --value) tasks"
