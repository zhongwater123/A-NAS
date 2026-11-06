#!/usr/bin/env bash
# Installs a pool of the companion's animations and makes it the one the
# Product Service serves. The animations are long-lived media on the system
# disk, not part of a release, like the screen saver videos (ADR 0014): each
# file is stored once under objects/ by its SHA-256, a pool is a directory of
# hard links to them named as its manifest lists, and "current" names the pool
# the Product Service reads. The pool dsh-pet's pinned commit ships is named in
# web/src/companion/dsh-pet/README.md.
#
#   install-companion-animations.sh POOL_ID [STAGING]
#       Installs the pool scripts/remote-stage-companion-animations.sh staged,
#       or switches back to a pool that is already installed.
set -euo pipefail

if [[ $EUID -ne 0 ]]; then
  echo "run as root" >&2
  exit 1
fi
usage() {
  echo "usage: $0 POOL_ID [STAGING]" >&2
  exit 2
}
[[ $# -ge 1 && $# -le 2 ]] || usage
pool=$1
staging=${2:-/home/anas-dev/apps/a-nas/companion}
[[ $pool =~ ^[0-9a-f]{16}$ ]] || usage

store=/var/lib/a-nas/companion
getent group a-nas >/dev/null || { echo "missing group a-nas: install the system services first" >&2; exit 3; }
install -d -o root -g a-nas -m 0750 "$store" "$store/objects" "$store/pools"
temporary=$(mktemp -d "$store/.install.XXXXXX")
trap 'rm -rf -- "$temporary"' EXIT

if [[ ! -d $store/pools/$pool ]]; then
  [[ -f $staging/pools/$pool.sums ]] || { echo "pool $pool is neither installed nor staged in $staging" >&2; exit 2; }
  # Check a private copy: the staging account can change its own files.
  manifest=$temporary/SHA256SUMS
  cp -- "$staging/pools/$pool.sums" "$manifest"
  [[ $(sha256sum < "$manifest" | cut -c 1-16) == "$pool" ]] || { echo "the staged manifest is not pool $pool" >&2; exit 2; }
  hashes=()
  names=()
  while read -r hash name; do
    [[ $hash =~ ^[0-9a-f]{64}$ && $name =~ ^[^./][^/]*\.(webm|mov)$ && $name != *[[:cntrl:]\\]* ]] ||
      { echo "malformed manifest line $((${#hashes[@]} + 1)) of pool $pool" >&2; exit 2; }
    hashes+=("$hash")
    names+=("$name")
  done < "$manifest"
  (( ${#hashes[@]} >= 1 && ${#hashes[@]} <= 512 )) || { echo "pool $pool must list 1 to 512 animations" >&2; exit 2; }
  [[ $(printf '%s\n' "${names[@]}" | sort -u | wc -l) -eq ${#names[@]} ]] || { echo "pool $pool lists a name twice" >&2; exit 2; }

  # Verify every new animation before storing any of them.
  added=()
  for hash in $(printf '%s\n' "${hashes[@]}" | sort -u); do
    [[ ! -e $store/objects/$hash ]] || continue
    [[ -f $staging/objects/$hash ]] || { echo "animation is not staged: $staging/objects/$hash" >&2; exit 2; }
    cp -- "$staging/objects/$hash" "$temporary/$hash"
    chown root:a-nas "$temporary/$hash"
    chmod 0640 "$temporary/$hash"
    printf '%s  %s\n' "$hash" "$temporary/$hash" | sha256sum --check --quiet - ||
      { echo "animation does not match its SHA-256: $hash" >&2; exit 2; }
    added+=("$hash")
  done
  for hash in "${added[@]}"; do
    mv -T -- "$temporary/$hash" "$store/objects/$hash"
  done

  install -d -o root -g a-nas -m 0750 "$temporary/pool"
  for index in "${!hashes[@]}"; do
    ln -- "$store/objects/${hashes[$index]}" "$temporary/pool/${names[$index]}"
  done
  install -o root -g a-nas -m 0640 "$manifest" "$temporary/pool/SHA256SUMS"
  mv -T -- "$temporary/pool" "$store/pools/$pool"
  echo "installed companion animation pool $pool: ${#hashes[@]} animations, ${#added[@]} new files"
fi

previous=$(readlink "$store/current" || true)
ln -sfn -- "pools/$pool" "$store/.current.next"
mv -Tf -- "$store/.current.next" "$store/current"
echo "companion animation pool $pool is current (was ${previous:-none})"