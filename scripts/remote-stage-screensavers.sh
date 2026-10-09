#!/usr/bin/env bash
# Stages a screen saver pool on the NAS for install-screensavers.sh, as the
# staging account (scripts/deploy-screensavers.ps1 runs it). Each video is
# kept once, by its SHA-256, under ~/apps/a-nas/screensavers/objects, so only
# videos the NAS does not have yet are uploaded (ADR 0014).
#
#   remote-stage-screensavers.sh missing HASH...
#       Prints the hashes still to upload as objects/HASH.mp4.incoming.
#   remote-stage-screensavers.sh commit HASH...
#       Checks the uploads, writes the pool manifest and prints the pool ID.
set -euo pipefail

usage() {
  echo "usage: $0 missing|commit HASH..." >&2
  exit 2
}
[[ $# -ge 2 && $# -le 33 ]] || usage
action=$1
shift
for hash in "$@"; do
  [[ $hash =~ ^[0-9a-f]{64}$ ]] || { echo "invalid video hash: $hash" >&2; exit 2; }
done
[[ $(printf '%s\n' "$@" | sort -u | wc -l) -eq $# ]] || { echo "a video is listed twice" >&2; exit 2; }

apps=$HOME/apps/a-nas
staging=$apps/screensavers
install -d -m 0750 "$staging" "$staging/objects" "$staging/pools"

# seed HASH: links the video an earlier release staged with this hash, before
# videos were staged on their own.
seed() {
  local object=$staging/objects/$1.mp4 record index
  for record in "$apps"/releases/*/RELEASE; do
    index=$(sed -n "s/^screensaver_\([0-9]\{3\}\)_sha256=$1\$/\1/p" "$record" 2>/dev/null | head -n 1 || true)
    [[ -n $index && -f ${record%/RELEASE}/screensavers/screensaver-$index.mp4 ]] || continue
    ln -f -- "${record%/RELEASE}/screensavers/screensaver-$index.mp4" "$object.incoming"
    if printf '%s  %s\n' "$1" "$object.incoming" | sha256sum --check --quiet - >/dev/null 2>&1; then
      mv -T -- "$object.incoming" "$object"
      return 0
    fi
    rm -f -- "$object.incoming"
  done
  return 1
}

case $action in
  missing)
    for hash in "$@"; do
      [[ -f $staging/objects/$hash.mp4 ]] || seed "$hash" || echo "$hash"
    done
    ;;
  commit)
    for hash in "$@"; do
      object=$staging/objects/$hash.mp4
      if [[ -f $object.incoming ]]; then
        printf '%s  %s\n' "$hash" "$object.incoming" | sha256sum --check --quiet - >&2 ||
          { echo "upload does not match its SHA-256: $hash" >&2; exit 2; }
        chmod 0640 "$object.incoming"
        mv -T -- "$object.incoming" "$object"
      fi
      [[ -f $object ]] || { echo "video was not uploaded: $hash" >&2; exit 2; }
    done
    manifest=$(mktemp "$staging/pools/.manifest.XXXXXX")
    index=0
    for hash in "$@"; do
      printf '%s  screensaver-%03d.mp4\n' "$hash" "$index" >> "$manifest"
      index=$((index + 1))
    done
    pool=$(sha256sum < "$manifest" | cut -c 1-16)
    chmod 0640 "$manifest"
    mv -T -- "$manifest" "$staging/pools/$pool.sums"
    echo "$pool"
    ;;
  *) usage ;;
esac
