#!/usr/bin/env bash
# Scheduled tasks (Hermes's cronjob; user decision, 2026-10-20) in the assistant's sandbox, with
# the fake model. The member schedules jobs in a conversation with Hermes's own tool; when one is
# due, anas-assistant-cron-waker starts anas-assistant-cron@<member>, Hermes's own
# `hermes cron tick` in the member's sandbox, with the assistant stopped. A step that needs the
# member's approval is refused (approvals.cron_mode: deny), so a job changes nothing outside the
# assistant's folder. Runs as root in a spike container.
set -uo pipefail
user=${1:-alice}
py=/opt/a-nas/assistant-runtimes/spike/venv/bin/python
home=/srv/a-nas/data/assistant/$user/hermes
files=/srv/a-nas/data/assistant/$user/files
private=/srv/a-nas/data/spaces/private/$user
pass=0 fail=0
check() {
  local name=$1
  shift
  if "$@" >/dev/null 2>&1; then echo "PASS $name"; pass=$((pass + 1)); else echo "FAIL $name"; fail=$((fail + 1)); fi
}
jobs() { python3 -c 'import json, sys; print(json.dumps(json.load(open(sys.argv[1]))["jobs"], ensure_ascii=False))' "$home/cron/jobs.json"; }
ran() { python3 -c 'import json, sys; jobs = json.load(open(sys.argv[1]))["jobs"]; sys.exit(0 if jobs and all(j.get("last_run_at") for j in jobs) else 1)' "$home/cron/jobs.json"; }

rm -f "$files/cron-ran.txt"
schedule() { "$py" /spike/serve_client.py "$user" chat "CALL cronjob_manage $1" >/dev/null 2>&1; }
schedule '{"action": "create", "name": "写个文件", "schedule": "every 1m", "prompt": "CALL terminal {\"command\": \"date > /nas/助手/cron-ran.txt\"}"}'
schedule '{"action": "create", "name": "改个人空间", "schedule": "every 1m", "prompt": "CALL mcp__anas__submit_changes {\"summary\": \"定时删除\", \"operations\": [{\"op\": \"delete\", \"path\": \"/nas/个人空间/secret.txt\"}]}"}'
check "a conversation schedules two jobs with Hermes's own tool" test "$(jobs | python3 -c 'import json, sys; print(len(json.load(sys.stdin)))')" = 2
check "the waker that starts due jobs runs" systemctl is-active anas-assistant-cron-waker.service

systemctl stop "anas-assistant-api@$user.service" "anas-assistant@$user.service" 2>/dev/null
for _ in $(seq 1 60); do ran && break; sleep 5; done
check "the jobs ran when due, with the assistant stopped" ran
check "the waker started them in the member's own unit" \
  bash -c "journalctl -u anas-assistant-cron-waker.service -o cat --no-pager | grep -q 'started anas-assistant-cron@$user.service'"
check "a job runs as the member, in the assistant's folder" test "$(stat -c %U "$files/cron-ran.txt" 2>/dev/null)" = "$user"
check "a job that would change the personal space is refused unattended" test -e "$private/secret.txt"
check "Hermes keeps each run as a session of its own" \
  test "$(python3 -c 'import sqlite3, sys; print(sqlite3.connect(sys.argv[1]).execute("SELECT count(*) FROM sessions WHERE source = \"cron\"").fetchone()[0])' "$home/state.db")" -ge 2
check "the assistant itself stayed stopped" test "$(systemctl is-active "anas-assistant@$user.service")" != active
echo "INFO runs: $(jobs | python3 -c 'import json, sys; print([(j["name"], j.get("last_status"), (j.get("last_error") or "")[:80]) for j in json.load(sys.stdin)])')"
echo "INFO cron unit: $(journalctl -u "anas-assistant-cron@$user.service" -o cat --no-pager | grep -vE '^$|Supervising' | tail -3 | tr '\n' ' ' | cut -c1-300)"

# Remove the jobs so they do not keep running.
python3 - "$home/cron/jobs.json" <<'PY'
import json, sys
path = sys.argv[1]
store = json.load(open(path))
store["jobs"] = []
json.dump(store, open(path, "w"))
PY
echo "cron: $pass passed, $fail failed"
((fail == 0))
