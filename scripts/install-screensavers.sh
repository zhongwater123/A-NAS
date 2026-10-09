#!/usr/bin/env bash
# Installs a screen saver pool and makes it the one the local console plays.
# Screen saver videos are long-lived media on the system disk, not part of a
# release (ADR 0014): each video is stored once under objects/ by its SHA-256,
# a pool is a directory of hard links to them named after its manifest, and
# "current" names the pool the Product Service reads.
#
#   install-screensavers.sh POOL_ID [STAGING]
#       Installs the pool scripts/remote-stage-screensavers.sh staged, or
#       switches back to a pool that is already installed.
#   install-screensavers.sh --import DIRECTORY
#       Adopts the pool an installer before issue #49 left in DIRECTORY.
set -euo pipefail

if [[ $EUID -ne 0 ]]; then
  echo "run as root" >&2
  exit 1
fi
usage() {
  echo "usage: $0 POOL_ID [STAGING] | --import DIRECTORY" >&2
  exit 2
}
[[ $# -ge 1 && $# -le 2 ]] || usage

store=/var/lib/a-nas/screensavers
getent group a-nas >/dev/null || { echo "missing group a-nas: install the system services first" >&2; exit 3; }
install -d -o root -g a-nas -m 0750 "$store" "$store/objects" "$store/pools"
temporary=$(mktemp -d "$store/.install.XXXXXX")
trap 'rm -rf -- "$temporary"' EXIT
manifest=$temporary/SHA256SUMS
hashes=()
sources=()

if [[ $1 == --import ]]; then
  [[ $# -eq 2 && -d $2 ]] || usage
  shopt -s nullglob
  videos=("$2"/*.mp4)
  shopt -u nullglob
  (( ${#videos[@]} >= 1 && ${#videos[@]} <= 32 )) || { echo "no screen saver pool to import in $2" >&2; exit 2; }
  for index in "${!videos[@]}"; do
    hash=$(sha256sum "${videos[$index]}" | cut -d ' ' -f 1)
    printf '%s  screensaver-%03d.mp4\n' "$hash" "$index" >> "$manifest"
  done
  pool=$(sha256sum < "$manifest" | cut -c 1-16)
  link=true # root's own files on the same file system
else
  pool=$1
  staging=${2:-/home/anas-dev/apps/a-nas/screensavers}
  [[ $pool =~ ^[0-9a-f]{16}$ ]] || usage
  if [[ ! -d $store/pools/$pool ]]; then
    [[ -f $staging/pools/$pool.sums ]] || { echo "pool $pool is neither installed nor staged in $staging" >&2; exit 2; }
    # Check a private copy: the staging account can change its own files.
    cp -- "$staging/pools/$pool.sums" "$manifest"
    [[ $(sha256sum < "$manifest" | cut -c 1-16) == "$pool" ]] || { echo "the staged manifest is not pool $pool" >&2; exit 2; }
    videos=()
  fi
  link=false
fi

if [[ ! -d $store/pools/$pool ]]; then
  index=0
  while read -r hash name; do
    [[ $hash =~ ^[0-9a-f]{64}$ && $name == "$(printf 'screensaver-%03d.mp4' "$index")" ]] ||
      { echo "malformed manifest line $((index + 1)) of pool $pool" >&2; exit 2; }
    for seen in "${hashes[@]}"; do
      [[ $seen != "$hash" ]] || { echo "pool $pool lists a video twice: $hash" >&2; exit 2; }
    done
    hashes+=("$hash")
    if [[ $link == true ]]; then sources+=("${videos[$index]}"); else sources+=("$staging/objects/$hash.mp4"); fi
    index=$((index + 1))
  done < "$manifest"
  (( index >= 1 && index <= 32 )) || { echo "pool $pool must list 1 to 32 videos" >&2; exit 2; }

  # Verify every new video before storing any of them.
  added=()
  for index in "${!hashes[@]}"; do
    hash=${hashes[$index]}
    [[ ! -e $store/objects/$hash.mp4 ]] || continue
    if [[ $link == true ]]; then
      ln -- "${sources[$index]}" "$temporary/$hash.mp4"
    else
      [[ -f ${sources[$index]} ]] || { echo "video is not staged: ${sources[$index]}" >&2; exit 2; }
      cp -- "${sources[$index]}" "$temporary/$hash.mp4"
    fi
    chown root:a-nas "$temporary/$hash.mp4"
    chmod 0640 "$temporary/$hash.mp4"
    printf '%s  %s\n' "$hash" "$temporary/$hash.mp4" | sha256sum --check --quiet - ||
      { echo "video does not match its SHA-256: ${sources[$index]}" >&2; exit 2; }
    added+=("$hash")
  done
  for hash in "${added[@]}"; do
    mv -T -- "$temporary/$hash.mp4" "$store/objects/$hash.mp4"
  done

  install -d -o root -g a-nas -m 0750 "$temporary/pool"
  for index in "${!hashes[@]}"; do
    ln -- "$store/objects/${hashes[$index]}.mp4" "$temporary/pool/$(printf 'screensaver-%03d.mp4' "$index")"
  done
  install -o root -g a-nas -m 0640 "$manifest" "$temporary/pool/SHA256SUMS"
  mv -T -- "$temporary/pool" "$store/pools/$pool"
  echo "installed screen saver pool $pool: ${#hashes[@]} videos, ${#added[@]} new"
fi

previous=$(readlink "$store/current" || true)
ln -sfn -- "pools/$pool" "$store/.current.next"
mv -Tf -- "$store/.current.next" "$store/current"
echo "screen saver pool $pool is current (was ${previous:-none})"
# The Product Service reads "current" on every request; restart the local
# console only so its open page reads the new list.
if systemctl is-active --quiet anas-kiosk@tty1.service; then
  systemctl restart anas-kiosk@tty1.service
fi
