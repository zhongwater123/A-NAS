#!/usr/bin/env bash
# Lists the pull requests merged into REF after the commit that the
# Experimental NAS runs, as Markdown. That commit is the one on the 已部署
# line of docs/status/CURRENT.md; the list itself is not kept in any document,
# because it changes with every merge (#191).
#
# Usage: scripts/undeployed.sh [REF]
# REF defaults to origin/main: run `git fetch` first. Needs only Git, so it
# runs in Git Bash on Windows as well as in WSL and CI.
set -euo pipefail

ref=${1:-origin/main}
root=$(git rev-parse --show-toplevel)
current="$root/docs/status/CURRENT.md"

# shellcheck disable=SC2016 # the backticks are Markdown code, not a command
deployed=$(sed -n 's/^- 已部署：`\([0-9a-f]\{7,40\}\)`.*/\1/p' "$current")
if [[ -z $deployed || $deployed == *$'\n'* ]]; then
  echo "undeployed: $current needs exactly one line starting with - 已部署：\`<commit>\`" >&2
  exit 1
fi
if ! git rev-parse --quiet --verify "$deployed^{commit}" >/dev/null; then
  echo "undeployed: the deployed commit $deployed is not in this clone; run git fetch" >&2
  exit 1
fi

merges=$(git rev-list --merges --first-parent "$deployed..$ref")
count=$(grep -c . <<<"$merges" || true)
echo "已部署 \`$deployed\` 之后合入 $ref（\`$(git rev-parse --short=12 "$ref")\`，$(git log -1 --format=%cs "$ref")）的 PR：$count 个"
echo
for merge in $merges; do
  subject=$(git log -1 --format=%s "$merge")
  # GitHub's merge commit: "Merge pull request #N from owner/branch", with
  # the pull request's title as the first line of the body.
  if [[ $subject =~ ^Merge\ pull\ request\ \#([0-9]+)\  ]]; then
    title=$(git log -1 --format=%b "$merge" | sed -n '/./{p;q;}')
    echo "- #${BASH_REMATCH[1]} ${title:-$subject}"
  else
    echo "- $(git rev-parse --short=12 "$merge") $subject"
  fi
done
