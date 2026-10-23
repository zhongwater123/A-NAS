#!/usr/bin/env bash
# Prints docs-only=true for GITHUB_OUTPUT when a pull request changes only
# documentation, so CI checks the documents and skips the builds and tests;
# prints docs-only=false otherwise, and always for a push to main.
#
# Documentation is whatever is under docs/, a Markdown file at the root, and the
# project skills' Markdown under .claude/skills/. Markdown elsewhere ships with
# the product (the assistant's skills, the app catalog's notices) and counts as
# code. A failure prints nothing, and CI then runs every check.
#
# Usage: scripts/ci-scope.sh EVENT
# EVENT is GITHUB_EVENT_NAME. For a pull request, run it on the merge commit
# that actions/checkout checks out: its first parent is the base branch, so the
# difference to it is the pull request's change.
set -euo pipefail

event=${1:?usage: scripts/ci-scope.sh EVENT}
if [[ $event != pull_request ]]; then
  echo docs-only=false
  exit 0
fi

# --no-renames lists both ends of a move, so a file moved into docs/ still
# counts where it came from. Unusual names stay quoted and count as code.
changed=$(git -c core.quotePath=false diff --name-only --no-renames HEAD^1 HEAD)
if [[ -z $changed ]]; then
  echo docs-only=false
  exit 0
fi

docs_only=true
while IFS= read -r path; do
  case $path in
    docs/* | .claude/skills/*.md) ;;
    */*) docs_only=false ;;
    *.md) ;;
    *) docs_only=false ;;
  esac
done <<<"$changed"
echo "docs-only=$docs_only"
