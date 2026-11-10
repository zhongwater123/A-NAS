#!/usr/bin/env bash
# Installs a pool of long-lived media and makes it the one the Product Service
# serves: the local console's screen saver videos, or the companion's
# animations. Neither is part of a release (ADR 0014): each file is stored once
# under objects/ by its SHA-256, a pool is a directory of hard links to them
# named as its manifest lists, and "current" names the pool the Product Service
# reads. The companion pool dsh-pet's pinned commit ships is named in
# web/src/companion/dsh-pet/README.md.
#
#   install-media-pool.sh screensavers|companion POOL_ID [STAGING]
#       Installs the pool scripts/remote-stage-media-pool.sh staged, or
#       switches back to a pool that is already installed.
#   install-media-pool.sh screensavers --import DIRECTORY
#       Adopts the screen saver pool an installer before issue #49 left in
#       DIRECTORY.
set -euo pipefail

if [[ $EUID -ne 0 ]]; then
  echo "run as root" >&2
  exit 1
fi
usage() {
  echo "usage: $0 screensavers|companion POOL_ID [STAGING] | screensavers --import DIRECTORY" >&2
  exit 2
}
[[ $# -ge 2 && $# -le 3 ]] || usage
kind=$1
shift
# What sets the kinds apart: where the pool lives, how its objects and names
# look, how many files it may list, and how large one may be.
case $kind in
  screensavers) label='screen saver' item=video items=videos suffix=.mp4 limit=32 largest=$((4 << 30)) ;;
  companion) label='companion animation' item=animation items=animations suffix='' limit=512 largest=$((256 << 20)) ;;
  *) usage ;;
esac
# A manifest lists at most 512 lines of a hash and a file name.
largest_manifest=$((1 << 20))
# named INDEX NAME: whether the manifest may name its line INDEX (from 0) NAME.
named() {
  case $kind in
    screensavers) [[ $2 == "$(printf 'screensaver-%03d.mp4' "$1")" ]] ;;
    companion) [[ $2 =~ ^[^./][^/]*\.(webm|mov)$ && $2 != *[[:cntrl:]\\]* ]] ;;
  esac
}

# take SOURCE TARGET LIMIT: copies a file from the staging area, which the
# staging account can still change under us: a regular file only, never
# through a symbolic link at its end, never waiting for a writer as a FIFO
# would, and refused over LIMIT bytes, of which it reads at most one more.
take() {
  [[ -f $1 && ! -L $1 ]] || return 1
  dd if="$1" of="$2" iflag=nofollow,nonblock,count_bytes count=$(($3 + 1)) bs=64K status=none 2>/dev/null || return 1
  [[ -f $2 && $(stat -c %s -- "$2") -le $3 ]]
}

store=/var/lib/a-nas/$kind
getent group a-nas >/dev/null || { echo "missing group a-nas: install the system services first" >&2; exit 3; }
install -d -o root -g a-nas -m 0750 "$store" "$store/objects" "$store/pools"
temporary=$(mktemp -d "$store/.install.XXXXXX")
trap 'rm -rf -- "$temporary"' EXIT
manifest=$temporary/SHA256SUMS
hashes=()
names=()
declare -A sources=()

if [[ $1 == --import ]]; then
  [[ $kind == screensavers && $# -eq 2 && -d $2 ]] || usage
  shopt -s nullglob
  videos=("$2"/*.mp4)
  shopt -u nullglob
  (( ${#videos[@]} >= 1 && ${#videos[@]} <= 32 )) || { echo "no screen saver pool to import in $2" >&2; exit 2; }
  for index in "${!videos[@]}"; do
    hash=$(sha256sum "${videos[$index]}" | cut -d ' ' -f 1)
    printf '%s  screensaver-%03d.mp4\n' "$hash" "$index" >> "$manifest"
    sources[$hash]=${videos[$index]}
  done
  pool=$(sha256sum < "$manifest" | cut -c 1-16)
  link=true # root's own files on the same file system
else
  pool=$1
  staging=${2:-/home/anas-dev/apps/a-nas/$kind}
  [[ $pool =~ ^[0-9a-f]{16}$ ]] || usage
  if [[ ! -d $store/pools/$pool ]]; then
    [[ -e $staging/pools/$pool.sums ]] || { echo "pool $pool is neither installed nor staged in $staging" >&2; exit 2; }
    # Check a private copy: the staging account can change its own files.
    take "$staging/pools/$pool.sums" "$manifest" "$largest_manifest" ||
      { echo "the staged manifest of pool $pool is not a regular file of at most $largest_manifest bytes" >&2; exit 2; }
    [[ $(sha256sum < "$manifest" | cut -c 1-16) == "$pool" ]] || { echo "the staged manifest is not pool $pool" >&2; exit 2; }
    # The pool ID counts every byte, read does not: it skips a last line
    # without a newline and drops NUL bytes unseen. Command substitution drops
    # them too, so the last byte is compared as od prints it.
    [[ $(tr -cd '\000' < "$manifest" | wc -c) -eq 0 && $(tail -c 1 -- "$manifest" | od -An -tx1) == *0a ]] ||
      { echo "the manifest of pool $pool has a NUL byte or does not end with a newline" >&2; exit 2; }
  fi
  link=false
fi

if [[ ! -d $store/pools/$pool ]]; then
  while read -r hash name; do
    if ! [[ $hash =~ ^[0-9a-f]{64}$ ]] || ! named "${#names[@]}" "$name"; then
      echo "malformed manifest line $((${#names[@]} + 1)) of pool $pool" >&2
      exit 2
    fi
    # A screen saver plays each video in turn; two animations may share a file.
    if [[ $kind == screensavers ]]; then
      for seen in "${hashes[@]}"; do
        [[ $seen != "$hash" ]] || { echo "pool $pool lists a video twice: $hash" >&2; exit 2; }
      done
    fi
    hashes+=("$hash")
    names+=("$name")
    [[ $link == true ]] || sources[$hash]=$staging/objects/$hash$suffix
  done < "$manifest"
  (( ${#names[@]} >= 1 && ${#names[@]} <= limit )) || { echo "pool $pool must list 1 to $limit $items" >&2; exit 2; }
  [[ $(printf '%s\n' "${names[@]}" | sort -u | wc -l) -eq ${#names[@]} ]] || { echo "pool $pool lists a name twice" >&2; exit 2; }

  # Verify every new file before storing any of them.
  added=()
  for hash in "${hashes[@]}"; do
    object=$hash$suffix
    [[ ! -e $store/objects/$object && ! -e $temporary/$object ]] || continue
    if [[ $link == true ]]; then
      ln -- "${sources[$hash]}" "$temporary/$object"
    else
      take "${sources[$hash]}" "$temporary/$object" "$largest" ||
        { echo "$item is not staged as a regular file of at most $largest bytes: ${sources[$hash]}" >&2; exit 2; }
    fi
    chown root:a-nas "$temporary/$object"
    chmod 0640 "$temporary/$object"
    printf '%s  %s\n' "$hash" "$temporary/$object" | sha256sum --check --quiet - ||
      { echo "$item does not match its SHA-256: ${sources[$hash]}" >&2; exit 2; }
    added+=("$object")
  done
  for object in "${added[@]}"; do
    mv -T -- "$temporary/$object" "$store/objects/$object"
  done

  install -d -o root -g a-nas -m 0750 "$temporary/pool"
  for index in "${!hashes[@]}"; do
    ln -- "$store/objects/${hashes[$index]}$suffix" "$temporary/pool/${names[$index]}"
  done
  install -o root -g a-nas -m 0640 "$manifest" "$temporary/pool/SHA256SUMS"
  mv -T -- "$temporary/pool" "$store/pools/$pool"
  echo "installed $label pool $pool: ${#names[@]} $items, ${#added[@]} new"
fi

previous=$(readlink "$store/current" || true)
ln -sfn -- "pools/$pool" "$store/.current.next"
mv -Tf -- "$store/.current.next" "$store/current"
echo "$label pool $pool is current (was ${previous:-none})"
# The Product Service reads "current" on every request, but the local
# console's page reads the list only when it opens: restart the console, if it
# runs, so it shows the new pool. Other browsers keep the pool they were given
# until they reload.
if systemctl is-active --quiet anas-kiosk@tty1.service; then
  systemctl restart anas-kiosk@tty1.service
fi
