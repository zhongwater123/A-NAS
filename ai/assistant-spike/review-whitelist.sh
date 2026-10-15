#!/usr/bin/env bash
# What Hermes's background review (#114) may run, with the fake model: the turn carries a
# "REVIEW CALL <tool> <json>" line that the fake model replays when Hermes asks it to review the
# conversation, so the review fork attempts exactly that call. Prints what each attempt got, then
# what a client that stays connected sees when the review attempts 提交更改.
set -uo pipefail
py=/opt/a-nas/assistant-runtimes/spike/venv/bin/python
log=/var/log/fake-model.jsonl
policy() {  # policy YAML: merge into the managed policy (restored by restore)
  cp -p /etc/hermes/config.yaml /tmp/managed-config.yaml
  "$py" - "$1" <<'PY'
import sys
from ruamel.yaml import YAML
yaml = YAML(typ="safe")
yaml.default_flow_style, yaml.allow_unicode = False, True
def merge(base, extra):
    for key, value in extra.items():
        base[key] = merge(base.get(key) or {}, value) if isinstance(value, dict) else value
    return base
policy = merge(yaml.load(open("/etc/hermes/config.yaml")), yaml.load(sys.argv[1]))
with open("/etc/hermes/config.yaml", "w") as out:
    yaml.dump(policy, out)
PY
  systemctl stop anas-assistant-api@alice.service anas-assistant@alice.service 2>/dev/null
}
restore() { cp -p /tmp/managed-config.yaml /etc/hermes/config.yaml; }
touch "$log"
try() {  # try POLICY_YAML TOOL JSON
  policy "$1"
  before=$(wc -l < "$log")
  "$py" /spike/serve_client.py alice chat "REVIEW CALL $2 $3" > /dev/null
  sleep 6
  printf '%-38s %-26s ' "$1" "$2"
  tail -n +$((before + 1)) "$log" | python3 -c '
import json, sys
rows = [json.loads(l) for l in sys.stdin]
# The review fork: its requests carry the turn plus the review prompt (more than 2 messages).
i = next((n for n, r in enumerate(rows) if r["decision"].get("tool") == sys.argv[1] and r["messages"] > 2), None)
print("review never attempted it" if i is None else
      (rows[i + 1]["decision"].get("text", "?")[:200] if i + 1 < len(rows) else "no result (yet)").replace("\n", " "))' "$2"
  restore
}
memory_review='{memory: {nudge_interval: 1}}'
skill_review='{skills: {creation_nudge_interval: 1}}'
try "$memory_review" write_file '{"path": "/nas/助手/复盘写的.txt", "content": "x"}'
try "$memory_review" mcp__anas__submit_changes '{"summary": "复盘删除", "operations": [{"op": "delete", "path": "/nas/个人空间/secret.txt"}]}'
try "$memory_review" memory '{"action": "add", "target": "memory", "content": "记忆复盘写入的一条"}'
try "$skill_review" memory '{"action": "add", "target": "memory", "content": "技能复盘写入的一条"}'
try "$skill_review" skill_manage '{"operations": [{"action": "create", "name": "review-made", "content": "---\nname: review-made\ndescription: 复盘建的技能\n---\n正文\n"}]}'
echo "files in the assistant directory: $(find /srv/a-nas/data/assistant/alice/files -mindepth 1 -maxdepth 1 -printf '%f ')"
echo "secret.txt still there: $(ls /srv/a-nas/data/spaces/private/alice/secret.txt 2>&1)"

echo "=== a client that stays connected while the review attempts 提交更改"
policy "$memory_review"
"$py" - <<'PY'
import asyncio, json, sys
sys.path.insert(0, "/spike")
from serve_client import Gateway
said = 'REVIEW CALL mcp__anas__submit_changes {"summary": "复盘删除", "operations": [{"op": "delete", "path": "/nas/个人空间/secret.txt"}]}'
async def main():
    async with Gateway("alice", timeout=120) as gw:
        await gw.call("client.capabilities", {"server_requests": True})
        sid = await gw.new_session()
        await gw.call("prompt.submit", {"session_id": sid, "text": said})
        loop, after = asyncio.get_running_loop(), ""
        end = loop.time() + 25
        while (left := end - loop.time()) > 0:
            try:
                frame = await asyncio.wait_for(gw.events.get(), left)
            except TimeoutError:
                break
            kind, payload = frame.get("type"), frame.get("payload") or {}
            if kind == "message.complete":
                after = " (after message.complete)"
                print("  message.complete")
            elif kind == "server.request":
                params = payload.get("params") or {}
                print(f"  server request {payload.get('method')} pattern_key={params.get('pattern_key')}{after}")
            elif kind in ("tool.start", "tool.complete"):
                print(f"  {kind} {payload.get('name')}{after}")
asyncio.run(main())
PY
restore
