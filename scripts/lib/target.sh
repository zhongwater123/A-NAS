# shellcheck shell=bash
# Debian target selection shared by build and content scripts. Existing amd64
# pins and runtime identities remain unchanged; arm64 has its own inputs.
target_arch() {
  local arch=${ANAS_TARGET_ARCH:-}
  if [[ -z "$arch" ]]; then arch=$(dpkg --print-architecture); fi
  case "$arch" in
    amd64|arm64) printf '%s\n' "$arch" ;;
    *) echo "unsupported A-NAS target architecture: $arch" >&2; return 2 ;;
  esac
}

# target_file REPO RELATIVE: select a separately pinned arm64 input, never
# silently fall back to a native input when cross-building.
target_file() {
  local repo=$1 relative=$2 arch
  arch=$(target_arch) || return 2
  if [[ "$arch" == arm64 ]]; then relative=${relative%.*}-arm64.${relative##*.}; fi
  [[ -f "$repo/$relative" ]] || { echo "missing target input: $repo/$relative" >&2; return 2; }
  printf '%s\n' "$repo/$relative"
}

# require_target_elf FILE: reject scripts and binaries for another machine
# before giving a Debian package its architecture label.
require_target_elf() {
  local machine expected arch
  arch=$(target_arch) || return 2
  case "$arch" in
    amd64) expected='Advanced Micro Devices X86-64' ;;
    arm64) expected=AArch64 ;;
  esac
  machine=$(LC_ALL=C readelf -h "$1" | sed -n 's/^[[:space:]]*Machine:[[:space:]]*//p') || return 2
  [[ "$machine" == "$expected" ]] || {
    echo "wrong ELF architecture: $1 is $machine; expected $expected" >&2
    return 2
  }
}

require_target_deb() {
  local arch package_arch
  arch=$(target_arch) || return 2
  package_arch=$(dpkg-deb --field "$1" Architecture) || return 2
  [[ "$package_arch" == "$arch" || "$package_arch" == all ]] || {
    echo "wrong Debian package architecture: $1 is $package_arch; expected $arch or all" >&2
    return 2
  }
}
