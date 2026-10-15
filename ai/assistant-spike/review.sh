#!/usr/bin/env bash
# Hermes's background review (#114): after a turn Hermes may fork the agent to save memory or
# create and patch skills. One conversation (review.py), the connection kept HOLD seconds after
# the last turn, then WATCH seconds of watching: which model requests were the review's, what
# it cost, what it wrote, and whether it finished before the instance went away.
#   review.sh HOLD WATCH [--managed YAML] -- TEXT [TEXT...]
# --managed merges YAML into the managed policy for this run (e.g. memory.nudge_interval: 1, or
# auxiliary.background_review.enabled: false). Real model: the gateway log has the messages.
set -uo pipefail
user=alice
hold=$1 watch=$2
shift 2
extra=""
if [[ ${1:-} == --managed ]]; then extra=$2; shift 2; fi
[[ ${1:-} == -- ]] && shift
home=/srv/a-nas/data/assistant/$user/hermes
log=/var/log/gateway.jsonl
py=/opt/a-nas/assistant-runtimes/spike/venv/bin/python
# Skills in the Hermes home other than the bundled copies (.bundled_manifest, written at the first start).
skills() {
  (cd "$home/skills" 2>/dev/null && find . -name SKILL.md -not -path './.archive/*' | sort |
    grep -v -F -f <(sed -n 's#^\([^:]*\):.*#/\1/SKILL.md#p' .bundled_manifest 2>/dev/null; echo /no-such-skill/))
}

cp -p /etc/hermes/config.yaml /tmp/managed-config.yaml
if [[ -n $extra ]]; then
  # Merged key by key, so a block the policy already has (skills:) keeps its other keys.
  "$py" - "$extra" <<'PY'
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
  echo "managed policy for this run adds: $(tr '\n' ' ' <<<"$extra")"
fi
systemctl stop "anas-assistant-api@$user.service" "anas-assistant@$user.service" 2>/dev/null
touch "$log"
before=$(wc -l < "$log")
skills > /tmp/skills.before
cat "$home"/memories/*.md > /tmp/memories.before 2>/dev/null
touch /tmp/review-start

"$py" -u /spike/review.py "$user" "$hold" "$@"
start=$(date +%s) last=""
while (( $(date +%s) - start < watch )); do
  now="model requests $(( $(wc -l < "$log") - before )), instance $(systemctl is-active "anas-assistant@$user.service")"
  [[ $now != "$last" ]] && echo "  +$(( $(date +%s) - start )) s after leaving: $now" && last=$now
  sleep 2
done

echo "--- model requests since the conversation started"
python3 /spike/review_report.py "$log" "$before"
echo "--- review lines in the Hermes log"
since=$(date -r /tmp/review-start '+%Y-%m-%d %H:%M:%S')
grep -a -i -E 'background.review|review fork|session ended' "$home/logs/agent.log" 2>/dev/null |
  awk -v t="$since" '$1" "$2 >= t' | cut -c1-220 | tail -8
echo "--- what persisted"
cat "$home"/memories/*.md 2>/dev/null | diff /tmp/memories.before - | sed -n 's/^> /  memory + /p'
skills | diff /tmp/skills.before - | sed -n 's/^> /  new skill /p'
(cd "$home/skills" && find . -name SKILL.md -not -path './.archive/*' -newer /tmp/review-start) | while read -r f; do
  echo "  [written during the run: $f]"; sed -n '1,30p' "$home/skills/$f" | sed 's/^/    /'
done
cp -p /tmp/managed-config.yaml /etc/hermes/config.yaml
systemctl stop "anas-assistant-api@$user.service" "anas-assistant@$user.service" 2>/dev/null
