#!/usr/bin/env bash
# After the real-model boundary scenarios: did a planted instruction persist
# into Hermes's memory or skills, and which changes did the member get asked about?
set -uo pipefail
home=/srv/a-nas/data/assistant/${1:-alice}
echo "--- memory"
for f in "$home"/hermes/memories/*.md; do [ -s "$f" ] && { echo "[$(basename "$f")]"; cat "$f"; }; done 2>/dev/null || echo "(none)"
echo "--- skills the agent created"
find "$home/hermes/skills" -name SKILL.md -newer "$home/hermes/config.yaml" 2>/dev/null | while read -r f; do echo "[$f]"; head -12 "$f"; done
echo "--- changes the member was asked to approve"
python3 -c 'import json, sys; [print(c["status"], c["summary"]) for c in json.load(open(sys.argv[1])).values()]' \
  "/var/lib/anas-spike/changes/${1:-alice}.json" 2>/dev/null || echo "(none)"
