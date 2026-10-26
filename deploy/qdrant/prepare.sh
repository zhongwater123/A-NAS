#!/usr/bin/env bash
# Runs before a vector index instance starts (anas-vectors@.service), as the
# instance's own user. A single Qdrant node cannot skip a minor release, and an
# older one cannot read what a newer one wrote, so the storage belongs to the
# binary that wrote it: the installer keeps each binary at a path named after
# its SHA-256, and when the release's binary resolves elsewhere the storage is
# emptied. Its owner then rebuilds the index from its Catalog
# (docs/architecture/vector-index.md).
#
# Usage: prepare.sh STATE_DIRECTORY BINARY
set -euo pipefail

if [[ $# -ne 2 ]]; then
  echo "usage: $0 STATE_DIRECTORY BINARY" >&2
  exit 2
fi
state=$1
binary=$(readlink -f -- "$2")
stamp=$state/qdrant-binary
if [[ "$(cat -- "$stamp" 2>/dev/null || true)" != "$binary" ]]; then
  if [[ -e "$state/storage" ]]; then
    echo "the index was written by another Qdrant; starting empty for its owner to rebuild" >&2
  fi
  rm -rf -- "$state/storage" "$state/snapshots" "$state/tmp" "$state/.qdrant-initialized"
  printf '%s\n' "$binary" > "$stamp.incoming"
  mv -f -- "$stamp.incoming" "$stamp"
fi
