#!/usr/bin/env bash
# Starts a vector index instance (anas-vectors@.service), as the instance's
# own user (docs/architecture/vector-index.md). It resolves the release once,
# so the storage check and the exec name the same Qdrant even while an
# installer switches releases, and empties the storage, for its owner to
# rebuild from its Catalog, when that Qdrant cannot use it:
#
# - another minor release wrote it, or a newer one: a single node can neither
#   skip a minor release nor read what a newer release wrote, while patch
#   upgrades within a minor release keep their storage;
# - the last two starts failed, as when a damaged segment makes Qdrant panic
#   while loading: wait-ready.sh clears the count once a start succeeds.
#
# Usage: start.sh STATE_DIRECTORY RELEASE_LINK
set -euo pipefail

if [[ $# -ne 2 ]]; then
  echo "usage: $0 STATE_DIRECTORY RELEASE_LINK" >&2
  exit 2
fi
state=$1
release=$(readlink -f -- "$2")
binary=$(readlink -f -- "$release/vectors/qdrant")
config=$release/vectors/config.yaml
version=$("$binary" --version | awk '{print $2}')
if [[ ! "$version" =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]]; then
  echo "cannot tell the Qdrant release of $binary: $version" >&2
  exit 1
fi

stamp=$state/qdrant-version
failures=$state/failed-starts
written=$(cat -- "$stamp" 2>/dev/null || true)
failed=$(cat -- "$failures" 2>/dev/null || echo 0)
[[ "$failed" =~ ^[0-9]+$ ]] || failed=0
why=
if [[ -e "$state/storage" && "$written" != "$version" ]]; then
  if [[ "${written%.*}" != "${version%.*}" ]]; then
    why="Qdrant $written wrote it, this is $version"
  elif [[ "$(printf '%s\n%s\n' "$written" "$version" | sort -V | tail -n 1)" != "$version" ]]; then
    why="the newer Qdrant $written wrote it, this is $version"
  fi
fi
if ((failed >= 2)); then
  why="the last $failed starts failed"
fi
if [[ -n "$why" ]]; then
  echo "starting the index empty for its owner to rebuild: $why" >&2
  rm -rf -- "$state/storage" "$state/snapshots" "$state/tmp" "$state/.qdrant-initialized"
  failed=0
fi
printf '%s\n' "$version" > "$stamp.incoming"
mv -f -- "$stamp.incoming" "$stamp"
# Counted as failed until wait-ready.sh sees the instance accept connections.
printf '%s\n' "$((failed + 1))" > "$failures.incoming"
mv -f -- "$failures.incoming" "$failures"
exec "$binary" --config-path "$config" --disable-telemetry
