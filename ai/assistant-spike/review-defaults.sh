#!/usr/bin/env bash
# The background review under Hermes's default triggers (#114), with the fake model: twelve turns
# in one conversation, back to back, each reading a file (two tool iterations a turn). Which turns
# were followed by a review request (memory every 10 member messages, skills every 10 tool
# iterations), and what the Hermes log says about each review. Logs at INFO for this run.
set -uo pipefail
py=/opt/a-nas/assistant-runtimes/spike/venv/bin/python
log=/var/log/fake-model.jsonl
home=/srv/a-nas/data/assistant/alice/hermes
# shellcheck source=policy.sh
. /spike/policy.sh
policy '{logging: {level: INFO}}'
touch "$log"
before=$(wc -l < "$log")
since=$(date '+%Y-%m-%d %H:%M:%S')
turns=()
for i in $(seq 1 12); do turns+=("CALL read_file {\"path\": \"/nas/个人空间/计划.md\", \"offset\": $i}"); done
"$py" /spike/review.py alice 20 "${turns[@]}" | grep -c '^>' | sed 's/^/turns sent: /'
tail -n +$((before + 1)) "$log" | python3 -c '
import json, sys
turn = 0
for line in sys.stdin:
    r = json.loads(line)
    if r.get("review"):
        print(f"  review request after turn {turn}")
    elif r["tools"] and r["decision"].get("tool"):
        turn += 1'
grep -a -E 'background.review|review superseded' "$home/logs/agent.log" | awk -v t="$since" '$1" "$2 >= t' | cut -c1-200
