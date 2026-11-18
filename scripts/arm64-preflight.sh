#!/usr/bin/env bash
# Read-only checks on the target before installing the ARM64 candidate.
set -euo pipefail
[[ "$(uname -m)" == aarch64 && "$(dpkg --print-architecture)" == arm64 ]] || {
  echo 'FAIL: run on the target Debian arm64 system' >&2
  exit 2
}
# shellcheck source=/dev/null
. /etc/os-release
[[ "${ID:-}" == debian && "${VERSION_ID:-}" == 13 ]] || {
  echo "FAIL: expected Debian 13; observed ${PRETTY_NAME:-unknown}" >&2
  exit 2
}
printf 'OS: %s\nKernel: %s\n' "$PRETTY_NAME" "$(uname -r)"
getconf GNU_LIBC_VERSION
printf 'Page size: %s\n' "$(getconf PAGESIZE)"
grep -E '^(MemTotal|MemAvailable|SwapTotal):' /proc/meminfo
python3 - <<'PY'
import ctypes
import platform
import sys

assert platform.machine() == "aarch64"
assert sys.version_info[:2] == (3, 13), sys.version
libc = ctypes.CDLL(None, use_errno=True)
# Linux aarch64 landlock_create_ruleset, with VERSION and no attributes.
abi = libc.syscall(444, ctypes.c_void_p(), 0, 1)
print(f"Landlock ABI: {abi}, errno: {ctypes.get_errno()}")
if abi < 3:
    raise SystemExit("FAIL: file-worker confinement needs Landlock ABI >= 3; check kernel CONFIG_SECURITY_LANDLOCK and active LSMs")
print("PASS: ARM64 user-space and Landlock prerequisites")
PY
printf '\nHost identity and storage (read-only):\n'
cat /etc/machine-id
ip -br link
findmnt -no SOURCE,FSTYPE,OPTIONS /
lsblk -o NAME,PATH,SIZE,TYPE,FSTYPE,MOUNTPOINTS,MODEL,SERIAL,TRAN
printf '\nNo storage was initialized. Continue with the RK3588 runbook.\n'
