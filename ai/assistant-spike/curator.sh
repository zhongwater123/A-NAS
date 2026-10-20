#!/usr/bin/env bash
# Hermes's curator in the assistant's sandbox (anas-assistant-curator@.service, weekly by
# anas-assistant-curator@.timer): it runs Hermes's own `hermes curator run` while the member's
# assistant is not running, and is skipped while it runs; Hermes's own pruning of its image
# cache follows it (media-cache.py). Runs as root in a spike container.
set -uo pipefail
user=${1:-alice}
py=/opt/a-nas/assistant-runtimes/spike/venv/bin/python
unit=anas-assistant-curator@$user.service
skills=/srv/a-nas/data/assistant/$user/hermes/skills
pass=0 fail=0
check() {
  local name=$1
  shift
  if "$@" >/dev/null 2>&1; then echo "PASS $name"; pass=$((pass + 1)); else echo "FAIL $name"; fail=$((fail + 1)); fi
}

check "the weekly timer is enabled" systemctl is-enabled "anas-assistant-curator@$user.timer"
echo "INFO next run: $(systemctl show -p NextElapseUSecRealtime --value "anas-assistant-curator@$user.timer")"

systemctl stop "anas-assistant@$user.service" 2>/dev/null
# Images the model looked at, two days ago and today, in Hermes's image cache.
cache=/srv/a-nas/data/assistant/$user/hermes/cache/images
setpriv --reuid "$user" --regid "$user" --init-groups -- sh -c "mkdir -p $cache && : > $cache/img_old.jpg && : > $cache/img_new.jpg"
touch -d '2 days ago' "$cache/img_old.jpg"
journalctl --rotate >/dev/null 2>&1
since=$(date '+%Y-%m-%d %H:%M:%S')
systemctl start "$unit"
check "it runs while the assistant is not running" test "$(systemctl show -p Result --value "$unit")" = success
echo "INFO curator: $(journalctl -u "$unit" --since "$since" -o cat --no-pager | grep -v '^$' | tail -4 | tr '\n' ' ' | cut -c1-300)"
check "Hermes's curator recorded the run" test -s "$skills/.curator_state"
echo "INFO state: $(python3 -c 'import json,sys; s=json.load(open(sys.argv[1])); print({k: s[k] for k in s if "run" in k})' "$skills/.curator_state" 2>&1 | cut -c1-200)"
check "Hermes's own pruning then drops the image cache's day-old files" test ! -e "$cache/img_old.jpg" -a -e "$cache/img_new.jpg"
check "it leaves the assistant stopped" test "$(systemctl is-active "anas-assistant@$user.service")" != active

"$py" /spike/serve_client.py "$user" ready >/dev/null
since=$(date '+%Y-%m-%d %H:%M:%S')
systemctl start "$unit"
check "it is skipped while the assistant runs" test "$(systemctl show -p Result --value "$unit")" = exec-condition
check "and the running assistant is left alone" test "$(systemctl is-active "anas-assistant@$user.service")" = active

echo "curator: $pass passed, $fail failed"
((fail == 0))
