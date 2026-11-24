#!/usr/bin/env bash
# Claude Code hook (.claude/settings.json): after an Edit or Write of a Go or
# Web file, run scripts/format-changed.sh --hook, the formatter every coding
# tool shares (AGENTS.md "编码工具").
#
# The settings run this file from CLAUDE_PROJECT_DIR, which stays at the main
# checkout in a worktree session, while the hook input's cwd follows the
# worktree. So it runs the formatter of the checkout around cwd when that
# checkout is a worktree of the same repository, and the main checkout's
# otherwise. It does nothing else, so that an older copy here still starts a
# newer formatter there. Git for Windows reads the JSON-escaped D:\\A-NAS as
# D:\A-NAS.
input=$(cat)
cwd=${input#*'"cwd"'}
cwd=${cwd#*'"'}
cwd=${cwd%%'"'*}
nl=$'\n'
script=
if found=$(git -C "$cwd" rev-parse --path-format=absolute --show-toplevel --git-common-dir 2>/dev/null) &&
  [[ ${found#*"$nl"} == "$(git -C "$CLAUDE_PROJECT_DIR" rev-parse --path-format=absolute --git-common-dir 2>/dev/null)" ]]; then
  script=${found%%"$nl"*}/scripts/format-changed.sh
fi
[[ -f $script ]] || script=$CLAUDE_PROJECT_DIR/scripts/format-changed.sh
[[ -f $script ]] || exit 0
printf '%s' "$input" | exec bash "$script" --hook
