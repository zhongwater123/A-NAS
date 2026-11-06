#!/usr/bin/env bash
# Stages the companion's animations on the NAS for install-companion-animations.sh,
# as the staging account (scripts/deploy-companion-animations.ps1 runs it). Like
# the screen saver videos (ADR 0014), each file is kept once by its SHA-256 under
# ~/apps/a-nas/companion/objects, so only files the NAS lacks are uploaded.
#
#   remote-stage-companion-animations.sh missing
#       Prints the hashes in pools/.incoming.sums still to upload as
#       objects/HASH.incoming.
#   remote-stage-companion-animations.sh commit
#       Checks the uploads, files the manifest under its pool ID and prints it.
set -euo pipefail

usage() {
  echo "usage: $0 missing|commit" >&2
  exit 2
}
[[ $# -eq 1 ]] || usage
staging=$HOME/apps/a-nas/companion
manifest=$staging/pools/.incoming.sums
install -d -m 0750 "$staging" "$staging/objects" "$staging/pools"
[[ -f $manifest ]] || { echo "no manifest uploaded to $manifest" >&2; exit 2; }

# The hashes of a well-formed manifest: one "HASH  NAME" per animation, a plain
# file name, each name once, at most 512 lines.
hashes() {
  local hash name count=0 names=()
  while read -r hash name; do
    [[ $hash =~ ^[0-9a-f]{64}$ && $name =~ ^[^./][^/]*\.(webm|mov)$ && $name != *[[:cntrl:]\\]* ]] ||
      { echo "malformed manifest line $((count + 1))" >&2; exit 2; }
    names+=("$name")
    echo "$hash"
    count=$((count + 1))
  done < "$manifest"
  (( count >= 1 && count <= 512 )) || { echo "the manifest must list 1 to 512 animations" >&2; exit 2; }
  [[ $(printf '%s\n' "${names[@]}" | sort -u | wc -l) -eq $count ]] || { echo "the manifest lists a name twice" >&2; exit 2; }
}
unique=$(hashes | sort -u)

case $1 in
  missing)
    for hash in $unique; do
      [[ -f $staging/objects/$hash ]] || echo "$hash"
    done
    ;;
  commit)
    for hash in $unique; do
      object=$staging/objects/$hash
      if [[ -f $object.incoming ]]; then
        printf '%s  %s\n' "$hash" "$object.incoming" | sha256sum --check --quiet - >&2 ||
          { echo "upload does not match its SHA-256: $hash" >&2; exit 2; }
        chmod 0640 "$object.incoming"
        mv -T -- "$object.incoming" "$object"
      fi
      [[ -f $object ]] || { echo "animation was not uploaded: $hash" >&2; exit 2; }
    done
    pool=$(sha256sum < "$manifest" | cut -c 1-16)
    chmod 0640 "$manifest"
    mv -T -- "$manifest" "$staging/pools/$pool.sums"
    echo "$pool"
    ;;
  *) usage ;;
esac