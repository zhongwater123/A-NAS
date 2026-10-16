#!/usr/bin/env bash
# What Hermes's background review (#114) may run, with the fake model: the turn carries a
# "REVIEW CALL <tool> <json>" line that the fake model replays when Hermes asks it to review the
# conversation, so the review fork attempts exactly that call. Hermes means the review to run
# only its own tools: skills, read_file and search_files, and memory in a memory review
# (agent/background_review.py _review_tool_whitelist). At the pinned commit that list never
# reached the threads tools run in; hermes-patches/ fixes it. Then what a client that stays
# connected sees when the review attempts 提交更改.
set -uo pipefail
py=/opt/a-nas/assistant-runtimes/spike/venv/bin/python
log=/var/log/fake-model.jsonl
files=/srv/a-nas/data/assistant/alice/files
# shellcheck source=policy.sh
. /spike/policy.sh
touch "$log"
pass=0 fail=0
# try NAME POLICY_YAML TOOL JSON allowed|denied: the review's attempt, and what it got.
try() {
  local name=$1 want=$5 before got
  policy "$2"
  before=$(wc -l < "$log")
  "$py" /spike/serve_client.py alice chat "REVIEW CALL $3 $4" > /dev/null
  sleep 6
  got=$(tail -n +$((before + 1)) "$log" | python3 -c '
import json, sys
rows = [json.loads(l) for l in sys.stdin]
# The review fork asked for the planted call (the fake model marks review requests).
i = next((n for n, r in enumerate(rows) if r["decision"].get("tool") == sys.argv[1] and r.get("review")), None)
print("review never attempted it" if i is None else
      (rows[i + 1]["decision"].get("text", "?")[:3000] if i + 1 < len(rows) else "no result (yet)").replace("\n", " "))' "$3")
  local denied=0
  grep -qiE "denied|not in this thread|whitelist|only call" <<<"$got" && denied=1
  if [[ $want == denied && $denied == 1 ]] || [[ $want == allowed && $denied == 0 && $got != "review never attempted it" ]]; then
    echo "PASS $name: $want (${got:0:120})"; pass=$((pass + 1))
  else
    echo "FAIL $name: want $want, got ${got:0:200}"; fail=$((fail + 1))
  fi
}
memory_review='{memory: {nudge_interval: 1}}'
skill_review='{skills: {creation_nudge_interval: 1}}'
rm -f "$files/复盘写的.txt" "$files/复盘终端.txt" "$files/复盘代码.txt"
try "a memory review may not write files" "$memory_review" write_file '{"path": "/nas/助手/复盘写的.txt", "content": "x"}' denied
try "a memory review may not run commands" "$memory_review" terminal '{"command": "echo x > /nas/助手/复盘终端.txt"}' denied
try "a memory review may not run code" "$memory_review" execute_code '{"code": "open(\"/nas/助手/复盘代码.txt\", \"w\").write(\"x\")"}' denied
try "a memory review may not submit changes" "$memory_review" mcp__anas__submit_changes '{"summary": "复盘删除", "operations": [{"op": "delete", "path": "/nas/个人空间/secret.txt"}]}' denied
try "a memory review may not start subagents" "$memory_review" delegate_task '{"goal": "x"}' denied
try "a memory review writes memory" "$memory_review" memory '{"action": "add", "target": "memory", "content": "记忆复盘写入的一条"}' allowed
try "a memory review reads files" "$memory_review" read_file '{"path": "/nas/个人空间/计划.md"}' allowed
try "a skill review may not write memory" "$skill_review" memory '{"action": "add", "target": "memory", "content": "技能复盘写入的一条"}' denied
try "a skill review may not run commands" "$skill_review" terminal '{"command": "echo x > /nas/助手/复盘终端.txt"}' denied
try "a skill review manages skills" "$skill_review" skill_manage '{"operations": [{"action": "create", "name": "review-made", "content": "---\nname: review-made\ndescription: 复盘建的技能\n---\n正文\n"}]}' allowed
for name in 复盘写的.txt 复盘终端.txt 复盘代码.txt; do
  if [[ -e $files/$name ]]; then echo "FAIL the review left $name in the assistant directory"; fail=$((fail + 1))
  else echo "PASS nothing the review was denied left $name"; pass=$((pass + 1)); fi
done
if [[ -e /srv/a-nas/data/spaces/private/alice/secret.txt ]]; then echo "PASS secret.txt still there"; pass=$((pass + 1))
else echo "FAIL secret.txt is gone"; fail=$((fail + 1)); fi

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
        # A turn right after a restart can take a while (MCP discovery); watch 15 s past its end.
        loop, after = asyncio.get_running_loop(), ""
        end = loop.time() + 90
        while (left := end - loop.time()) > 0:
            try:
                frame = await asyncio.wait_for(gw.events.get(), left)
            except TimeoutError:
                break
            kind, payload = frame.get("type"), frame.get("payload") or {}
            if kind == "message.complete":
                after = " (after message.complete)"
                end = min(end, loop.time() + 15)
                print("  message.complete")
            elif kind == "server.request":
                params = payload.get("params") or {}
                print(f"  server request {payload.get('method')} pattern_key={params.get('pattern_key')}{after}")
            elif kind in ("tool.start", "tool.complete", "review.summary"):
                print(f"  {kind} {payload.get('name') or payload.get('text')}{after}")
asyncio.run(main())
PY
echo "=== a review that writes memory says so, in Hermes's own Chinese (display.language)"
summary=$("$py" - <<'PY'
import asyncio, sys
sys.path.insert(0, "/spike")
from serve_client import Gateway
said = 'REVIEW CALL memory {"action": "add", "target": "memory", "content": "复盘记下：alice 周日大扫除"}'
async def main():
    async with Gateway("alice", timeout=120) as gw:
        sid = await gw.new_session()
        await gw.call("prompt.submit", {"session_id": sid, "text": said})
        loop = asyncio.get_running_loop()
        end = loop.time() + 90
        while (left := end - loop.time()) > 0:
            try:
                frame = await asyncio.wait_for(gw.events.get(), left)
            except TimeoutError:
                break
            if frame.get("type") == "review.summary":
                print((frame.get("payload") or {}).get("text", ""))
                return
asyncio.run(main())
PY
)
echo "  review.summary: ${summary:-none}"
if grep -q "自我改进" <<<"$summary"; then echo "PASS the review reports what it noted, in Chinese"; pass=$((pass + 1))
else echo "FAIL no Chinese review.summary"; fail=$((fail + 1)); fi
echo "review-whitelist: $pass passed, $fail failed"
((fail == 0))
