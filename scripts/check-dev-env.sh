#!/usr/bin/env bash
set -uo pipefail

project_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
required_tools=(
  git
  go
  gofmt
  rg
  curl
  jq
  file
  ffprobe
  gcc
  g++
  make
  cmake
  ninja
  pkg-config
  python3
  shellcheck
  rsync
  sqlite3
  ansible
)
optional_tools=(node npm docker rustc cargo)
missing=0

printf 'A-NAS development environment\n'
printf '  user:      %s\n' "$(id -un)"
printf '  system:    %s\n' "$(uname -srmo)"
printf '  workspace: %s\n' "$project_root"

if [[ "$(id -u)" -eq 0 ]]; then
  printf 'ERROR: development commands must not run as root.\n' >&2
  missing=1
fi

if [[ ! -f "$project_root/PROJECT_CONTEXT.md" ]]; then
  printf 'ERROR: PROJECT_CONTEXT.md was not found at the workspace root.\n' >&2
  missing=1
fi

printf '\nRequired tools:\n'
for tool in "${required_tools[@]}"; do
  if command -v "$tool" >/dev/null 2>&1; then
    printf '  [ok]      %s\n' "$tool"
  else
    printf '  [missing] %s\n' "$tool"
    missing=1
  fi
done

# make ai-sdk-test builds a virtual environment and runs pip in it. Debian and
# Ubuntu ship ensurepip in python3-venv, not with python3 itself.
if command -v python3 >/dev/null 2>&1; then
  if python3 -c 'import ensurepip' >/dev/null 2>&1; then
    printf '  [ok]      python3 ensurepip\n'
  else
    printf '  [missing] python3 ensurepip (install python3-venv)\n'
    missing=1
  fi
fi

printf '\nOptional or pending-decision tools:\n'
for tool in "${optional_tools[@]}"; do
  if command -v "$tool" >/dev/null 2>&1; then
    printf '  [present] %s\n' "$tool"
  else
    printf '  [not set] %s\n' "$tool"
  fi
done

if [[ "$missing" -ne 0 ]]; then
  printf '\nEnvironment check failed.\n' >&2
  exit 1
fi

printf '\nEnvironment check passed.\n'
