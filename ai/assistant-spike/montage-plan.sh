#!/usr/bin/env bash
# Hermes 探针 P1（docs/research/montage-hermes-probe.md）：剪辑中心的规划这一轮，作为一个只开放
# montage 工具组的 Hermes 任务，跑在成员的定时任务单元里（剪辑中心实施方案的“规划”）。用替身
# 模型和替身的 montage 服务器（montage_stub_mcp.py）；在 spike 容器里以 root 运行：
#
#   run.sh test montage-plan.sh [USER]
#
# 规划这一轮的任务由 montage_jobs.py 在成员自己的沙箱里建、触发和删，和回合脚本将来的做法一样。
set -uo pipefail
user=${1:-alice}
py=/opt/a-nas/assistant-runtimes/spike/venv/bin/python
home=/srv/a-nas/data/assistant/$user/hermes
files=/srv/a-nas/data/assistant/$user/files
stub=$files/montage-stub.log
fake=/var/log/fake-model.jsonl
cron=anas-assistant-cron@$user.service
jobs=anas-assistant-montage-jobs@$user.service
instance=anas-assistant@$user.service
marker="MONTAGE-PROBE-MEMORY-$RANDOM"
pass=0 fail=0
check() {
  local name=$1
  shift
  if "$@" >/dev/null 2>&1; then echo "PASS $name"; pass=$((pass + 1)); else echo "FAIL $name"; fail=$((fail + 1)); fi
}
info() { echo "INFO $*"; }

# --- 准备 ------------------------------------------------------------------------------------
# montage 服务器写进托管配置和成员的配置（mcp_servers 整段复制进成员配置，assistant-layout.sh），
# 但不加进任何 platform_toolsets：规划这一轮靠任务自己的 enabled_toolsets 拿到它。
server='  montage:\n    command: /opt/a-nas/assistant-runtimes/spike/venv/bin/python\n    args: [/spike/montage_stub_mcp.py]\n    trust: full\n    enabled: true\n    lazy: false\n    timeout: 120\n    tools:\n      prompts: false\n      resources: false'
for f in /etc/hermes/config.yaml "/etc/a-nas-assistant/members/$user/config.yaml"; do
  grep -q '^  montage:' "$f" || sed -i "/^mcp_servers:/a\\$server" "$f"
done
# 方案里剪辑的套接字只绑定进定时任务单元；这里给它绑一个占位文件，替身据此列出工具。
install -d -m 0755 /run/anas-assistant-montage
install -d /etc/systemd/system/anas-assistant-cron@.service.d
printf '[Service]\nTemporaryFileSystem=/run/anas-assistant-montage\nBindReadOnlyPaths=/etc/hostname:/run/anas-assistant-montage/montage.sock\n' \
  > /etc/systemd/system/anas-assistant-cron@.service.d/montage-probe.conf
# 在成员沙箱里跑 montage_jobs.py 的单元，照探针单元的做法（assistant-install.sh）。
sed -e 's#^Description=.*#Description=Montage planning jobs of %i (Hermes probe P1)#' \
  -e 's#^ExecStart=.*#ExecStart=/opt/a-nas/assistant-runtimes/spike/venv/bin/python -P /spike/montage_jobs.py @/nas/助手/montage-jobs-args.json#' \
  -e '/^ExecStartPost=/d' -e 's/^Type=.*/Type=oneshot/' -e '/^StopWhenUnneeded/d' \
  /etc/systemd/system/anas-assistant@.service > /etc/systemd/system/anas-assistant-montage-jobs@.service
systemctl daemon-reload
systemctl stop "anas-assistant-api@$user.service" "$instance" 2>/dev/null

mj() {
  # 以成员身份在沙箱里跑一次 montage_jobs.py，回答它的 JSON。
  "$py" -c 'import json, sys; print(json.dumps(sys.argv[1:], ensure_ascii=False))' "$@" > "$files/montage-jobs-args.json"
  chown "$user:$user" "$files/montage-jobs-args.json"
  rm -f "$files/montage-jobs-out.json"
  # 杀掉定时任务单元之后，同一网络的单元可能留在失败状态。
  systemctl reset-failed "$jobs" "anas-assistant-net@$user.service" 2>/dev/null
  systemctl start "$jobs" >/dev/null 2>&1
  cat "$files/montage-jobs-out.json" 2>/dev/null
}
field() { "$py" -c 'import json, sys; d = json.loads(sys.argv[1]); [d := (d or {}).get(k) for k in sys.argv[2:]]; print("" if d is None else d)' "$1" "${@:2}"; }
lines() { wc -l < "$1" 2>/dev/null || echo 0; }
since() { tail -n "+$(($2 + 1))" "$1" 2>/dev/null; }
round() { systemctl start "$cron" >/dev/null 2>&1; }

# 成员的记忆里放一个记号，看它会不会进规划这一轮的系统提示（定时任务的 Agent 带着 skip_memory=False）。
install -d -o "$user" -g "$user" -m 0700 "$home/memories"
printf '§\n%s\n' "$marker" >> "$home/memories/MEMORY.md"
chown "$user:$user" "$home/memories/MEMORY.md"
: > "$stub"
chown "$user:$user" "$stub"

# --- 1. 建成暂停的任务，不会自己运行 -------------------------------------------------------------
call='CALL mcp__montage__montage_brief {"jobId": "j1", "attemptToken": "t1"}'
out=$(mj create "剪辑规划 r1" "$call")
job=$(field "$out" id)
check "a planning job is created paused, with only the montage toolset" \
  test -n "$job" -a "$(field "$(mj show "$job")" job state)" = paused
check "it lists montage as its only toolset" test "$(field "$(mj show "$job")" job enabled_toolsets)" = "['montage']"
round
check "a round does not run a paused planning job" bash -c "test -z \"$(field "$(mj show "$job")" job last_run_at)\" && ! grep -q '\"call\"' $stub"

# --- 2. 触发后在一轮里运行，只看得到 montage 的工具 --------------------------------------------------
before=$(lines "$fake")
check "trigger_job makes it due" test "$(field "$(mj trigger "$job")" triggered)" = True
round
show=$(mj show "$job")
check "the round runs it" test "$(field "$show" job last_status)" = ok
check "its montage call reaches the server" grep -q '"call": "mcp__montage__montage_brief"\|"call": "montage_brief"' "$stub"
offered=$(since "$fake" "$before" | "$py" -c 'import json, sys; print(sorted({t for l in sys.stdin for t in json.loads(l)["tools"]}))')
info "tools offered to the model in this run: $offered"
check "the model is offered only montage's tools" "$py" -c 'import ast, sys; t = ast.literal_eval(sys.argv[1]); sys.exit(0 if t and all(x.startswith("mcp__montage__") for x in t) else 1)' "$offered"
reviews=$(since "$fake" "$before" | grep -c '"review": true')
check "no background review runs after it" test "$reviews" = 0
check "the member's memory is in the run's system prompt (skip_memory=False)" grep -q "$marker" "$fake.system.txt"
info "run sessions: $(field "$show" runs)"

# --- 3. 这一轮调不到终端 ----------------------------------------------------------------------------
rm -f "$files/montage-terminal.txt"
job2=$(field "$(mj create "剪辑规划 r2" 'CALL terminal {"command": "date > /nas/助手/montage-terminal.txt"}')" id)
mj trigger "$job2" >/dev/null
round
check "a terminal call in the planning run does not run" test ! -e "$files/montage-terminal.txt"
info "r2: $(field "$(mj show "$job2")" job last_status) $(field "$(mj show "$job2")" job last_error | cut -c1-160)"

# --- 4. 成员自己的定时任务看得到 montage 吗 -----------------------------------------------------------
before=$(lines "$fake")
job3=$(field "$(mj plain "普通任务" 'SAY 好 END')" id)
mj trigger "$job3" >/dev/null
round
info "tools offered to a member's own job: $(since "$fake" "$before" | "$py" -c 'import json, sys; print(sorted({t for l in sys.stdin for t in json.loads(l)["tools"] if "montage" in t}))') (montage tools only)"
mj remove "$job3" >/dev/null

# --- 5. 删任务连同运行的会话 -------------------------------------------------------------------------
removed=$(mj remove "$job")
info "remove: $removed"
check "removing the job deletes its run sessions" test "$(field "$removed" removed)" = True -a "$(field "$removed" sessions_deleted)" != "[]"
check "neither the job nor its runs are left" test -z "$(field "$(mj show "$job")" job)" -a "$(field "$(mj show "$job")" runs)" = "[]"
mj remove "$job2" >/dev/null

# --- 6. 运行中途杀掉单元 ------------------------------------------------------------------------------
job4=$(field "$(mj create "剪辑规划 r4" 'CALL mcp__montage__montage_brief {"jobId": "j4", "sleep": 60}')" id)
mj trigger "$job4" >/dev/null
calls=$(grep -c '"sleep": 60' "$stub")
systemctl start --no-block "$cron"
for _ in $(seq 1 90); do [[ $(grep -c '"sleep": 60' "$stub") -gt $calls ]] && break; sleep 2; done
info "cron unit while the planning run waits on montage: ActiveState=$(systemctl show -p ActiveState --value "$cron"), memory $(($(systemctl show -p MemoryCurrent --value "$cron") / 1048576)) MiB"
systemctl kill --signal=SIGKILL "$cron"
sleep 3
systemctl reset-failed "$cron" 2>/dev/null
show=$(mj show "$job4")
info "after the kill: next_run_at=$(field "$show" job next_run_at) last_status=$(field "$show" job last_status) runs=$(field "$show" runs)"
check "a killed planning job is not due again (Hermes moved it a period on when the run began)" \
  "$py" -c 'import datetime, json, sys; n = json.loads(sys.argv[1])["job"]["next_run_at"]; sys.exit(0 if datetime.datetime.fromisoformat(n) > datetime.datetime.now(datetime.UTC) else 1)' "$show"
calls=$(grep -c '"sleep": 60' "$stub")
round
check "the next round does not run it again" test "$(grep -c '"sleep": 60' "$stub")" = "$calls"
check "the next round records it as interrupted" test "$(field "$(mj show "$job4")" job last_error)" = interrupted
removed=$(mj remove "$job4")
check "the cleanup removes the leftover job and its run session" test "$(field "$removed" removed)" = True -a "$(field "$removed" sessions_deleted)" != "[]"

# --- 7. 对话实例里的零工具服务器 ----------------------------------------------------------------------
before=$(lines "$fake")
starts=$(grep -c '"start": true' "$stub")
"$py" /spike/serve_client.py "$user" chat "SAY 你好 END" > /tmp/montage-chat.txt 2>&1
check "a conversation still works with montage listing no tools there" grep -q '你好' /tmp/montage-chat.txt
info "montage server starts in the instance: $(( $(grep -c '"start": true' "$stub") - starts )); tools it listed there: $(grep '"list_tools"' "$stub" | tail -n 1)"
check "the conversation is offered no montage tools" bash -c "! tail -n +$((before + 1)) $fake | grep -q mcp__montage__"
info "instance log about montage: $(journalctl -u "$instance" -o cat --no-pager | grep -i montage | tail -n 2 | tr '\n' ';' | cut -c1-240)"
systemctl stop "anas-assistant-api@$user.service" "$instance" 2>/dev/null

# --- 8. 不调用模型就能查出模型通不通 -------------------------------------------------------------------
info "GET /v1/models through the model bridge: $(curl -s -o /dev/null -w '%{http_code}' --unix-socket /run/anas-assistant-gw/model.sock http://model/v1/models)"

rm -f /etc/systemd/system/anas-assistant-cron@.service.d/montage-probe.conf
systemctl daemon-reload
echo "montage plan: $fail failed"
exit "$((fail > 0))"
