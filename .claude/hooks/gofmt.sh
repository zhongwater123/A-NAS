#!/usr/bin/env bash
# Claude Code PostToolUse hook (.claude/settings.json): gofmt the Go file that
# an Edit or Write just changed, so `make fmt-check` never fails on it.
#
# It touches only files in the checkout Claude works in: the nearest directory
# with a go.mod above the hook input's cwd. CLAUDE_PROJECT_DIR does not name
# that checkout in a worktree session, where it stays at the main checkout.
#
# The Windows development machines have Go and jq only inside WSL
# (docs/development/LOCAL_ENVIRONMENT.md), so there this script runs itself
# again in the default WSL distribution. Where neither has gofmt it does
# nothing, and `make fmt-check` still catches the file.
set -o pipefail

input=$(cat)
# Most edits are not Go files: let them pass before starting WSL.
[[ $input == *'.go"'* ]] || exit 0

if [[ -f ~/.anas-env ]]; then
  # shellcheck source=/dev/null
  . ~/.anas-env
fi

if ! command -v gofmt >/dev/null || ! command -v jq >/dev/null; then
  if [[ -z ${ANAS_GOFMT_HOOK_IN_WSL:-} ]] && command -v wsl.exe >/dev/null && command -v cygpath >/dev/null; then
    # shellcheck disable=SC2016 # $1 expands in WSL's bash, not here
    printf '%s' "$input" | MSYS_NO_PATHCONV=1 wsl.exe -e bash -c \
      'ANAS_GOFMT_HOOK_IN_WSL=1 exec bash "$(wslpath -a "$1")"' _ "$(cygpath -w "$0")"
    exit
  fi
  exit 0
fi

local_path() {
  if [[ -n ${ANAS_GOFMT_HOOK_IN_WSL:-} ]]; then
    wslpath -a "$1"
  elif command -v cygpath >/dev/null; then
    cygpath -u "$1"
  else
    printf '%s\n' "$1"
  fi
}

file=$(jq -r '.tool_input.file_path // empty' <<<"$input")
cwd=$(jq -r '.cwd // empty' <<<"$input")
[[ $file == *.go && -n $cwd ]] || exit 0
file=$(local_path "$file") || exit 0
checkout=$(local_path "$cwd") || exit 0
while [[ $checkout == /?* && ! -f $checkout/go.mod ]]; do
  checkout=${checkout%/*}
done
[[ -f $checkout/go.mod && -f $file && $file == "$checkout"/* && $file != "$checkout"/.claude/worktrees/* ]] || exit 0

# A file that does not parse is reported to Claude (exit 2) and left as it is.
if ! output=$(gofmt -w "$file" 2>&1); then
  printf '%s\n' "$output" >&2
  exit 2
fi
