#!/usr/bin/env bash
# Stages a media pool on the NAS for install-media-pool.sh, as the staging
# account (scripts/deploy-media-pool.ps1 runs it). Each file is kept once, by
# its SHA-256, under ~/apps/a-nas/KIND/objects, so only files the NAS does not
# have yet are uploaded (ADR 0014).
#
#   remote-stage-media-pool.sh screensavers missing HASH...
#       Prints the hashes still to upload as objects/HASH.mp4.incoming.
#   remote-stage-media-pool.sh screensavers commit HASH...
#       Checks the uploads, writes the pool manifest and prints the pool ID.
#   remote-stage-media-pool.sh companion missing
#       Prints the hashes in pools/.incoming.sums still to upload as
#       objects/HASH.incoming.
#   remote-stage-media-pool.sh companion commit
#       Checks the uploads, files the manifest under its pool ID and prints it.
set -euo pipefail

usage() {
  echo "usage: $0 screensavers missing|commit HASH... | companion missing|commit" >&2
  exit 2
}
[[ $# -ge 2 ]] || usage
kind=$1
action=$2
shift 2
apps=$HOME/apps/a-nas
staging=$apps/$kind

case $kind in
  screensavers)
    # The pool is the videos named here, in this order.
    item=video suffix=.mp4
    [[ $# -ge 1 && $# -le 32 ]] || usage
    for hash in "$@"; do
      [[ $hash =~ ^[0-9a-f]{64}$ ]] || { echo "invalid video hash: $hash" >&2; exit 2; }
    done
    [[ $(printf '%s\n' "$@" | sort -u | wc -l) -eq $# ]] || { echo "a video is listed twice" >&2; exit 2; }
    install -d -m 0750 "$staging" "$staging/objects" "$staging/pools"
    hashes=("$@")
    ;;
  companion)
    # The pool is the manifest uploaded to pools/.incoming.sums.
    item=animation suffix=''
    [[ $# -eq 0 ]] || usage
    manifest=$staging/pools/.incoming.sums
    install -d -m 0750 "$staging" "$staging/objects" "$staging/pools"
    [[ -f $manifest ]] || { echo "no manifest uploaded to $manifest" >&2; exit 2; }
    # The pool ID counts every byte, read does not: it skips a last line
    # without a newline and drops NUL bytes unseen. Command substitution drops
    # them too, so the last byte is compared as od prints it.
    [[ $(tr -cd '\000' < "$manifest" | wc -c) -eq 0 && $(tail -c 1 -- "$manifest" | od -An -tx1) == *0a ]] ||
      { echo "the manifest has a NUL byte or does not end with a newline" >&2; exit 2; }
    # The hashes of a well-formed manifest: one "HASH  NAME" per animation, a
    # plain file name, each name once, at most 512 lines.
    listed() {
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
    unique=$(listed | sort -u)
    mapfile -t hashes <<<"$unique"
    ;;
  *) usage ;;
esac

# seed HASH: links the screen saver video an earlier release staged with this
# hash, before videos were staged on their own.
seed() {
  local object=$staging/objects/$1.mp4 record index
  [[ $kind == screensavers ]] || return 1
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
    for hash in "${hashes[@]}"; do
      [[ -f $staging/objects/$hash$suffix ]] || seed "$hash" || echo "$hash"
    done
    ;;
  commit)
    for hash in "${hashes[@]}"; do
      object=$staging/objects/$hash$suffix
      if [[ -f $object.incoming ]]; then
        printf '%s  %s\n' "$hash" "$object.incoming" | sha256sum --check --quiet - >&2 ||
          { echo "upload does not match its SHA-256: $hash" >&2; exit 2; }
        chmod 0640 "$object.incoming"
        mv -T -- "$object.incoming" "$object"
      fi
      [[ -f $object ]] || { echo "$item was not uploaded: $hash" >&2; exit 2; }
    done
    if [[ $kind == screensavers ]]; then
      manifest=$(mktemp "$staging/pools/.manifest.XXXXXX")
      index=0
      for hash in "$@"; do
        printf '%s  screensaver-%03d.mp4\n' "$hash" "$index" >> "$manifest"
        index=$((index + 1))
      done
    fi
    pool=$(sha256sum < "$manifest" | cut -c 1-16)
    chmod 0640 "$manifest"
    mv -T -- "$manifest" "$staging/pools/$pool.sums"
    echo "$pool"
    ;;
  *) usage ;;
esac
