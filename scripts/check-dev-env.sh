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
warnings=0
# Free space below which the script warns: the Web and AI dependencies, the
# Go caches, build/ with its packages and the pinned Qdrant take about this.
disk_warn_gib=20

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

# The pinned versions of ShellCheck, lychee, zizmor and Ruff are checked where
# the Makefile reads the pins, in the first steps of make check (docs-check,
# ops-check, ai-lint). pip-audit has no such check, and vuln-check, which runs
# it, comes late in the run; PIP_AUDIT names it as for make.
printf '\nAudit tool:\n'
if command -v "${PIP_AUDIT:-pip-audit}" >/dev/null 2>&1; then
  printf '  [ok]      pip-audit\n'
else
  printf '  [missing] pip-audit (ai/requirements-dev.txt; make vuln-check runs it)\n'
  missing=1
fi

# The vector index tests run the Qdrant that deploy/qdrant/qdrant.json pins
# (docs/development/testing.md#qdrant-测试依赖). Without it they skip, which
# make check accepts only while ANAS_QDRANT_REQUIRED is unset: then a gap is
# a warning, and with it set (as in CI) an error.
printf '\nQdrant for the vector index tests:\n'
qdrant_gap() {
  if [[ "${ANAS_QDRANT_REQUIRED:-}" == 1 ]]; then
    printf '  [missing] %s\n' "$1"
    missing=1
  else
    printf '  [warning] %s\n' "$1"
    warnings=$((warnings + 1))
  fi
}
qdrant_sha=$(jq -r .sha256 "$project_root/deploy/qdrant/qdrant.json" 2>/dev/null || true)
qdrant=${ANAS_QDRANT_BINARY:-$project_root/build/qdrant/$qdrant_sha/qdrant}
if [[ -x "$qdrant" ]]; then
  printf '  [ok]      %s\n' "$qdrant"
elif [[ -n "${ANAS_QDRANT_BINARY:-}" ]]; then
  qdrant_gap "ANAS_QDRANT_BINARY is not an executable: $qdrant"
else
  qdrant_gap "the pinned Qdrant is not fetched; run scripts/fetch-qdrant.sh"
fi
# The pin is an x86_64 build: an arm64 Linux machine runs it through a
# registered x86_64 binfmt handler, QEMU's (qemu-user-static) or Rosetta's.
if [[ "$(uname -s)" == Linux && "$(uname -m)" =~ ^(aarch64|arm64)$ ]]; then
  binfmt=
  for handler in /proc/sys/fs/binfmt_misc/qemu-x86_64 /proc/sys/fs/binfmt_misc/rosetta; do
    if [[ "$(head -n 1 "$handler" 2>/dev/null)" == enabled ]]; then binfmt=$handler; fi
  done
  if [[ -n "$binfmt" ]]; then
    printf '  [ok]      x86_64 binfmt: %s\n' "$binfmt"
  else
    qdrant_gap "no x86_64 binfmt handler is registered (install qemu-user-static)"
  fi
fi

free_kib=$(df -Pk "$project_root" 2>/dev/null | awk 'NR == 2 { print $4 }')
printf '\nDisk:\n'
if [[ "$free_kib" =~ ^[0-9]+$ ]] && ((free_kib < disk_warn_gib * 1024 * 1024)); then
  printf '  [warning] %s GiB free under the workspace; make check needs about %s GiB\n' \
    "$((free_kib / 1024 / 1024))" "$disk_warn_gib"
  warnings=$((warnings + 1))
elif [[ "$free_kib" =~ ^[0-9]+$ ]]; then
  printf '  [ok]      %s GiB free under the workspace\n' "$((free_kib / 1024 / 1024))"
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

if [[ "$warnings" -ne 0 ]]; then
  printf '\nEnvironment check passed with %s warning(s).\n' "$warnings"
else
  printf '\nEnvironment check passed.\n'
fi
