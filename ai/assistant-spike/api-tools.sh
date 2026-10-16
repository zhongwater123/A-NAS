#!/usr/bin/env bash
# Drives scripted tool calls through alice's assistant and prints what Hermes
# returned to the model for each (the fake model echoes the tool result).
set -uo pipefail
user=${1:-alice}
sock=/run/a-nas-assistant/$user.sock
chat() {
  local said=$1
  python3 - "$said" "$sock" "$user" <<'PY'
import json, socket, sys, http.client
said, sock, user = sys.argv[1:4]
class UDS(http.client.HTTPConnection):
    def connect(self):
        self.sock = socket.socket(socket.AF_UNIX); self.sock.settimeout(180); self.sock.connect(sock)
c = UDS("a")
body = json.dumps({"model": "hermes-agent", "messages": [{"role": "user", "content": said}]})
c.request("POST", "/v1/chat/completions", body, {"Authorization": f"Bearer k7Qx9vT2mR4pL8wNz3Jd-{user}", "Content-Type": "application/json"})
r = c.getresponse(); d = json.loads(r.read() or b"{}")
text = d["choices"][0]["message"]["content"] if "choices" in d else d
print(f"> {said[:110]}\n< {str(text)[:700]}\n")
PY
}
while IFS= read -r line; do
  [ -n "$line" ] && chat "$line"
done < "/spike/${2:-calls.txt}"
