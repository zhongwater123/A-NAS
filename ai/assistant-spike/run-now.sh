#!/usr/bin/env bash
# A task the member asked to run now ("立即运行", #469) in the assistant's sandbox, with the fake
# model: it runs in a round of the cron unit, like a task that is due, and the member's
# instance never gets a gateway of its own. The requests are written here as the File Broker
# writes them (internal/filebroker/rootbroker/assistant_run.go); the way from the window to
# the File Broker is in the system test. Runs as root in a spike container.
#
#   run-now.sh [USER]
set -uo pipefail
user=${1:-alice}
py=/opt/a-nas/assistant-runtimes/spike/venv/bin/python
sock=/run/a-nas-assistant/$user.sock
store=/srv/a-nas/data/assistant/$user/hermes/cron/jobs.json
instance=anas-assistant@$user.service
cron=anas-assistant-cron@$user.service
waiting=/run/a-nas/assistant-runs
pass=0 fail=0
check() {
  local name=$1
  shift
  if "$@" >/dev/null 2>&1; then echo "PASS $name"; pass=$((pass + 1)); else echo "FAIL $name"; fail=$((fail + 1)); fi
}

# The Product Service holds a connection while the member is signed in; without one the
# bridge idles out and the instance stops.
sign_in() {
  systemd-run --quiet --unit run-now-keeper "$py" -c \
    "import socket,time; s=socket.socket(socket.AF_UNIX); s.connect('$sock'); time.sleep(3600)"
  token=$("$py" /spike/hermes_login.py "$user")
}
sign_out() {
  systemctl stop run-now-keeper 2>/dev/null; systemctl reset-failed run-now-keeper 2>/dev/null
  systemctl stop "anas-assistant-api@$user.service" "$instance" 2>/dev/null
}
api() { curl -sS --max-time 150 --unix-socket "$sock" -X "$1" -H "Authorization: Bearer $token" -H 'Content-Type: application/json' ${3:+-d "$3"} -o /dev/null "http://127.0.0.1:9119$2"; }
field() { jq -r --arg id "$job" ".jobs[] | select(.id == \$id) | $1 // empty" "$store" 2>/dev/null; }
ask() { install -d -m 0700 "$waiting" "$waiting/$user" && : > "$waiting/$user/$1"; }
memory() { echo $(($(systemctl show -p MemoryCurrent --value "$instance") / 1048576)); }

sign_out
sign_in
api POST /api/cron/jobs '{"schedule": "every 60m", "name": "run-now-probe", "prompt": "SAY 手动运行完成 END"}'
job=$(jq -r '.jobs[] | select(.name == "run-now-probe") | .id' "$store" 2>/dev/null | head -n1)
check "an hourly task is there, not due" test -n "$job" -a -z "$(field .last_run_at)"
slot=$(field .next_run_at)

sign_out
ask "$job"
systemctl start "$cron"
check "asked to run now with the member signed out, it runs in a round" test "$(field .last_status)" = ok
check "the run it was waiting for stays where it was" test "$(field .next_run_at)" = "$slot"
check "the instance was not started for it" test "$(systemctl is-active "$instance")" != active
check "the round left no request behind" test -z "$(ls -A "$waiting/$user" /run/anas-assistant-run-now/"$user" 2>/dev/null)"
echo "INFO round: $(journalctl -u "$cron" -o cat --no-pager | grep 'cron round' | tail -n 3 | tr '\n' ';' | cut -c1-240)"

sign_in
api POST "/api/cron/jobs/$job/pause"
check "the task is paused" test "$(field .state)" = paused
before=$(memory)
ran=$(field .last_run_at)
ask "$job"
ask 0123456789ab
systemctl start "$cron"
check "asked to run now while paused, it runs and is resumed" test "$(field .last_status):$(field .state)" = ok:scheduled -a "$(field .last_run_at)" != "$ran"
check "the member signed in, their instance gets no gateway" bash -c "systemctl is-active --quiet $instance && ! COLUMNS=400 systemd-cgls --no-pager -l -u $instance | grep -q 'gateway run'"
echo "INFO instance memory: $before MB before, $(memory) MB after"
check "a task that is not theirs is passed over" bash -c "journalctl -u $cron -o cat --no-pager | grep -q '0123456789ab is not a job of this member'"

# The model's own way to run a task, Hermes's cronjob_manage, would start a gateway inside
# the instance: there the start is refused (serve_nondumpable.py), and the skill tells the
# model to send the member to "立即运行" instead.
"$py" /spike/serve_client.py "$user" chat "CALL cronjob_manage {\"action\": \"run\", \"job_id\": \"$job\"}" >/dev/null 2>&1 || true
for _ in $(seq 1 20); do [[ "$(field .last_status)" == error ]] && break; sleep 2; done
check "the model's cronjob_manage run starts no gateway in the instance" bash -c "! COLUMNS=400 systemd-cgls --no-pager -l -u $instance | grep -q 'gateway run'"
echo "INFO the model's run: last_status=$(field .last_status) last_error=$(field .last_error | cut -c1-120)"

api DELETE "/api/cron/jobs/$job"
sign_out
echo "run now: $fail failed"
exit "$((fail > 0))"
