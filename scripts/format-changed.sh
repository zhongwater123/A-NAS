#!/usr/bin/env bash
# Format the changed files of a checkout the way `make check` expects them:
# Go files with gofmt, Web files with the Biome that web/package.json pins
# (what `cd web && npm run format` does, for the given files only).
#
#   scripts/format-changed.sh              the files Git sees as changed or new
#   scripts/format-changed.sh FILE...      these files
#   scripts/format-changed.sh --hook       a coding agent's hook: reads the hook
#                                          input on stdin (Claude Code or Codex)
#
# It is the one formatter for every tool and person (AGENTS.md "编码工具"):
# `make fmt` runs it, and the Claude Code and Codex hooks call it after each
# edit. A hook formats the files the edit names: Claude Code's Edit and Write
# give file_path, Codex's apply_patch names them in its patch; an input that
# names none formats the checkout's changed files. Other files are left alone;
# Python has no formatter in the gate (ai-lint runs only `ruff check`).
#
# A tool it needs and does not find here (the Windows development machines
# have Go only inside WSL, docs/development/LOCAL_ENVIRONMENT.md) moves the
# formatting into the default WSL distribution, given the files by path; Git
# stays here, since WSL's Git cannot read a Windows worktree's gitdir. A hook exits 2
# with the formatter's message when a file does not parse or keeps a Biome
# error, so that the agent sees it; otherwise a hook never fails the edit.
set -uo pipefail

# Internal: format FILE... (relative to ROOT) without asking Git; this is how
# the script runs itself again in WSL.
if [[ ${1:-} == --files-in ]]; then
  # shellcheck source=/dev/null
  [[ -f ~/.anas-env ]] && . ~/.anas-env
  root=$2
  shift 2
  go_files=()
  web_files=()
  for relative in "$@"; do
    case $relative in
      *.go) go_files+=("$root/$relative") ;;
      web/*) web_files+=("${relative#web/}") ;;
    esac
  done
  failed=0
  if ((${#go_files[@]})) && ! output=$(gofmt -w "${go_files[@]}" 2>&1); then
    printf '%s\n' "$output" >&2
    failed=1
  fi
  if ((${#web_files[@]})); then
    biome=$root/web/node_modules/.bin/biome
    if [[ ! -x $biome ]]; then
      # A checkout without web/node_modules (the Windows worktrees) installs
      # the pinned version once into a cache of its own: npx would add a
      # second per edit.
      version=$(sed -n 's/.*"@biomejs\/biome": "\([0-9.]*\)".*/\1/p' "$root/web/package.json")
      [[ -n $version ]] || { echo "format-changed: web/package.json pins no @biomejs/biome" >&2; exit 1; }
      cache=${XDG_CACHE_HOME:-$HOME/.cache}/a-nas/biome-$version
      biome=$cache/node_modules/.bin/biome
      if [[ ! -x $biome ]] && ! output=$(npm install --prefix "$cache" --no-save --no-audit --no-fund "@biomejs/biome@$version" 2>&1); then
        printf 'format-changed: installing Biome %s failed:\n%s\n' "$version" "$output" >&2
        exit 1
      fi
    fi
    # The package's biome is a Node launcher for the native binary beside it: call that directly.
    for native in "${biome%/.bin/biome}"/@biomejs/cli-*/biome "${biome%/.bin/biome}"/@biomejs/cli-*/biome.exe; do
      [[ -x $native ]] && biome=$native && break
    done
    if ! output=$(cd "$root/web" && "$biome" check --write --no-errors-on-unmatched --files-ignore-unknown=true "${web_files[@]}" 2>&1); then
      printf '%s\n' "$output" >&2
      failed=1
    fi
  fi
  exit "$failed"
fi

hook=false
if [[ ${1:-} == --hook ]]; then
  hook=true
  shift
fi

# A Windows path as Git Bash writes it (D:\a\b → /d/a/b); other paths as they are.
unix_path() {
  if [[ $1 =~ ^([A-Za-z]):[\\/](.*)$ ]]; then
    local rest=${BASH_REMATCH[2]}
    printf '/%s/%s\n' "${BASH_REMATCH[1],,}" "${rest//\\//}"
  else
    printf '%s\n' "${1//\\//}"
  fi
}

# A string field of the hook input, JSON escapes for \ and " undone; no jq,
# which the Windows machines have only inside WSL.
json_field() {
  local pattern="\"$1\"[[:space:]]*:[[:space:]]*\"(([^\"\\\\]|\\\\.)*)\""
  [[ $input =~ $pattern ]] || return 1
  local value=${BASH_REMATCH[1]}
  value=${value//\\\"/\"}
  printf '%s\n' "${value//\\\\/\\}"
}

files=()
dir=$PWD
if $hook; then
  input=$(cat)
  if named=$(json_field file_path); then
    files+=("$named")
  else
    # apply_patch: "*** Add File: <path>", "*** Update File: <path>" and "*** Move to: <path>",
    # each ended by the JSON-escaped newline (or a real one).
    rest=$input
    pattern=$'\*\*\* (Add File|Update File|Move to): ([^\\\\"\n\r]+)'
    while [[ $rest =~ $pattern ]]; do
      files+=("${BASH_REMATCH[2]}")
      rest=${rest#*"${BASH_REMATCH[0]}"}
    done
  fi
  # Most edits touch neither Go nor Web files: let them pass before Git or WSL starts.
  if ((${#files[@]})); then
    relevant=false
    for file in "${files[@]}"; do
      case ${file//\\//} in
        *.go | web/* | */web/*) relevant=true ;;
      esac
    done
    $relevant || exit 0
  fi
  if cwd=$(json_field cwd); then
    dir=$(unix_path "$cwd")
  fi
else
  files=("$@")
fi

checkout=$(git -C "$dir" rev-parse --show-toplevel 2>/dev/null) || {
  $hook && exit 0
  echo "format-changed: $dir is not in a Git checkout" >&2
  exit 1
}
checkout=$(unix_path "$checkout")

if ((${#files[@]} == 0)); then
  # Changed, staged and new files; a rename lists its new name.
  while IFS= read -r -d '' entry; do
    status=${entry:0:2}
    [[ $status == R* || $status == C* ]] && IFS= read -r -d '' _
    [[ $status == *D* ]] && continue
    files+=("$checkout/${entry:3}")
  done < <(git -C "$checkout" status --porcelain=v1 -z --untracked-files=all)
fi

relatives=()
need_go=false
need_node=false
for file in "${files[@]}"; do
  file=$(unix_path "$file")
  [[ $file == /* ]] || file=$dir/$file
  [[ -f $file && $file == "$checkout"/* ]] || continue
  relative=${file#"$checkout"/}
  case $relative in
    .claude/worktrees/* | */node_modules/*) continue ;;
    *.go) need_go=true ;;
    web/*.ts | web/*.tsx | web/*.js | web/*.jsx | web/*.mjs | web/*.cjs | web/*.json | web/*.jsonc | web/*.css) need_node=true ;;
    *) continue ;;
  esac
  relatives+=("$relative")
done
((${#relatives[@]})) || exit 0

# shellcheck source=/dev/null
[[ -f ~/.anas-env ]] && . ~/.anas-env
if { $need_go && ! command -v gofmt >/dev/null; } || { $need_node && ! command -v npm >/dev/null; }; then
  if [[ $checkout =~ ^/([a-z])/(.*)$ ]] && command -v wsl.exe >/dev/null; then
    # Git Bash's /d/… is WSL's /mnt/d/….
    wsl_checkout=/mnt/${BASH_REMATCH[1]}/${BASH_REMATCH[2]}
    MSYS_NO_PATHCONV=1 wsl.exe -e bash "$wsl_checkout/scripts/format-changed.sh" --files-in "$wsl_checkout" "${relatives[@]}" </dev/null
    rc=$?
  else
    $hook && exit 0 # No formatter here; `make check` still reports the files.
    echo "format-changed: needs gofmt and Node.js (see docs/development/LOCAL_ENVIRONMENT.md)" >&2
    exit 1
  fi
else
  bash "$0" --files-in "$checkout" "${relatives[@]}"
  rc=$?
fi

if ((rc != 0)); then
  $hook && exit 2
  exit 1
fi
exit 0
