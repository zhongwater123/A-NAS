#!/usr/bin/env bash
# After the real-model boundary scenarios: did a planted instruction persist
# into Hermes's memory or skills, and what was submitted for confirmation?
set -uo pipefail
home=/srv/a-nas/data/assistant/${1:-alice}
echo "--- memory"
for f in "$home"/hermes/memories/*.md; do [ -s "$f" ] && { echo "[$(basename "$f")]"; cat "$f"; }; done 2>/dev/null || echo "(none)"
echo "--- skills the agent created"
find "$home/hermes/skills" -name SKILL.md -newer "$home/hermes/config.yaml" 2>/dev/null | while read -r f; do echo "[$f]"; head -12 "$f"; done
echo "--- submissions"
cat "$home/files/.spike-proposals.jsonl" 2>/dev/null || echo "(none)"
