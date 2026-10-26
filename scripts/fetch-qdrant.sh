#!/usr/bin/env bash
# Fetches the Qdrant server that deploy/qdrant/qdrant.json pins, for the
# development machine and CI: the vector index tests and evaluations run this
# binary, and deploy-dev.ps1 stages the same tarball for the NAS installer
# (docs/architecture/vector-index.md). The tarball is checked against the
# manifest's size and SHA-256 before anything is unpacked, and only the binary
# is taken from it. Both stay in build/qdrant/<sha256>/, and the binary's path
# is printed; a later run reuses them.
#
# Usage: scripts/fetch-qdrant.sh
set -euo pipefail

repo=$(cd "$(dirname "$0")/.." && pwd)
manifest=$repo/deploy/qdrant/qdrant.json
field() { jq -er --arg key "$1" '.[$key]' "$manifest"; }
source=$(field source)
file=$(field file)
size=$(field sizeBytes)
sha=$(field sha256)
binary=$(field binary)
if [[ ! "$sha" =~ ^[0-9a-f]{64}$ || ! "$size" =~ ^[0-9]+$ || "$source" != https://* ||
  ! "$file" =~ ^[A-Za-z0-9][A-Za-z0-9._-]*$ || ! "$binary" =~ ^[A-Za-z0-9][A-Za-z0-9._-]*$ ]]; then
  echo "invalid Qdrant manifest: $manifest" >&2
  exit 2
fi

out=$repo/build/qdrant/$sha
if [[ -x "$out/qdrant" && -f "$out/$file" ]]; then
  echo "$out/qdrant"
  exit
fi
mkdir -p "$repo/build/qdrant"
incoming=$(mktemp -d "$repo/build/qdrant/.incoming-XXXXXX")
trap 'rm -rf -- "$incoming"' EXIT
curl --fail --location --silent --show-error --proto '=https' --output "$incoming/$file" "$source"
if [[ "$(stat -c %s "$incoming/$file")" != "$size" ]] ||
  ! printf '%s  %s\n' "$sha" "$incoming/$file" | sha256sum --check --quiet - >/dev/null 2>&1; then
  echo "the downloaded Qdrant does not match its manifest: $source" >&2
  exit 4
fi
mkdir "$incoming/unpacked"
tar -C "$incoming/unpacked" --no-same-owner -xzf "$incoming/$file" "$binary"
install -m 0755 "$incoming/unpacked/$binary" "$incoming/qdrant"
rm -rf -- "$incoming/unpacked"
rm -rf -- "$out"
mv -T -- "$incoming" "$out"
trap - EXIT
echo "$out/qdrant"
