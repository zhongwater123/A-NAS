#!/usr/bin/env bash
# What Hermes does with an image an MCP tool returns (#113). The view_photo tool, which
# anas_mcp.py offers only with ANAS_SPIKE_PHOTO_PIXELS, paints a picture: sky, sea, sand
# and a red sun. It runs under three settings of the managed scope:
#   pinned   as the spike ships it: agent.image_input_mode: text
#   vision   the model also declared vision-capable (model.supports_vision: true)
#   native   declared vision-capable and image_input_mode back to its default, auto
# For each: image parts in the model request, what tool.complete carried to the client
# and the image files left in the Hermes home. With a real model it asks for a description.
set -uo pipefail
user=${1:-alice}
py=/opt/a-nas/assistant-runtimes/spike/venv/bin/python
managed=/etc/hermes/config.yaml
member=/etc/a-nas-assistant/members/$user/config.yaml
cache=/srv/a-nas/data/assistant/$user/hermes/cache/images
real=$([[ -f /etc/anas-spike/model.key ]] && echo 1)
log=$([[ -n $real ]] && echo /var/log/gateway.jsonl || echo /var/log/fake-model.jsonl)
cp "$managed" /var/tmp/vision-managed.orig
cp "$member" /var/tmp/vision-member.orig
restore() {
  cp /var/tmp/vision-managed.orig "$managed"
  cp /var/tmp/vision-member.orig "$member"
  systemctl stop "anas-assistant@$user.service" 2>/dev/null
}
trap restore EXIT
if [[ -n $real ]]; then
  ask="看看照片 photo:demo 的画面，用一句话说说里面有什么。"
else
  ask='CALL mcp__anas__view_photo {"photo_id": "photo:demo"}'
fi
# One turn over /api/ws: what tool.complete carried to the client, and the answer.
turn() {
  "$py" - "$user" "$ask" <<'PY'
import asyncio, json, sys
sys.path.insert(0, "/spike")
from serve_client import Gateway

async def main(user, text):
    async with Gateway(user) as gw:
        sid = await gw.new_session()
        await gw.call("prompt.submit", {"session_id": sid, "text": text})
        while True:
            frame = await asyncio.wait_for(gw.events.get(), 300)
            if frame.get("session_id") != sid:
                continue
            payload = frame.get("payload") or {}
            if frame.get("type") == "tool.complete":
                raw = json.dumps(payload, ensure_ascii=False)
                print(f"  tool.complete to the client: {len(raw)} characters, "
                      f"{'a base64 image in it' if 'data:image' in raw else 'no image data'}")
            if frame.get("type") == "message.complete":
                print(f"  answer: {(payload.get('text') or '').replace(chr(10), ' ')[:200]}")
                return

asyncio.run(main(*sys.argv[1:3]))
PY
}

for setting in pinned vision native; do
  for file in "$managed" "$member"; do
    cp "/var/tmp/vision-$([[ $file == "$managed" ]] && echo managed || echo member).orig" "$file"
    sed -i 's#^    args: \[/spike/anas_mcp.py\]$#&\n    env: {ANAS_SPIKE_PHOTO_PIXELS: "1"}#' "$file"
  done
  [[ $setting != pinned ]] && sed -i 's/^  api_mode: chat_completions$/&\n  supports_vision: true/' "$managed"
  [[ $setting == native ]] && sed -i 's/^  image_input_mode: text$/  image_input_mode: auto/' "$managed"
  systemctl stop "anas-assistant@$user.service" 2>/dev/null
  rm -rf "$cache"
  before=$(wc -l < "$log" 2>/dev/null || echo 0)
  echo "=== $setting"
  turn
  tail -n +$((before + 1)) "$log" | python3 -c '
import json, sys
for line in sys.stdin:
    record = json.loads(line)
    if "image_parts" in record:
        parts = record["image_parts"]
    else:
        parts = sum(1 for m in record.get("messages", []) if isinstance(m.get("content"), list)
                    for p in m["content"] if isinstance(p, dict) and p.get("type") == "image_url")
    media = "MEDIA:" in json.dumps([record.get("messages"), record.get("decision")], ensure_ascii=False)
    status = "; status %s" % record["status"] if "status" in record else ""
    print("  model request: %d image parts%s%s" % (parts, status, "; a MEDIA: path in what the model saw" if media else ""))'
  echo "  image files in the Hermes home: $(find "$cache" -type f 2>/dev/null | wc -l)"
done
