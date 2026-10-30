#!/usr/bin/env bash
# Fetches the llama.cpp build that deploy/llama/llama.json pins, for the
# development machine and CI: the AI Worker reads the text in photos through
# its llama-server (docs/architecture/photo-ocr.md), and deploy-dev.ps1 stages
# the same tarball for the NAS installer. The tarball is checked against the
# manifest's size and SHA-256 before anything is unpacked, and only the
# members it names, llama-server with its libraries and licence, are taken.
# Both stay in build/llama/<sha256>/, with the server and its libraries in
# bin/, and the server's path is printed. A later run reuses the tarball only
# once it passes the same check, and unpacks it again.
#
# Usage: scripts/fetch-llama.sh
set -euo pipefail

repo=$(cd "$(dirname "$0")/.." && pwd)
manifest=$repo/deploy/llama/llama.json
field() { jq -er --arg key "$1" '.[$key]' "$manifest"; }
source=$(field source)
file=$(field file)
size=$(field sizeBytes)
sha=$(field sha256)
directory=$(field directory)
binary=$(field binary)
name='^[A-Za-z0-9][A-Za-z0-9._-]*$'
if [[ ! "$sha" =~ ^[0-9a-f]{64}$ || ! "$size" =~ ^[0-9]+$ || "$source" != https://* ||
  ! "$file" =~ $name || ! "$directory" =~ $name || ! "$binary" =~ $name ]]; then
  echo "invalid llama.cpp manifest: $manifest" >&2
  exit 2
fi
# pinned FILE: FILE is the tarball the manifest pins.
pinned() {
  [[ -f "$1" && "$(stat -c %s "$1")" == "$size" ]] &&
    printf '%s  %s\n' "$sha" "$1" | sha256sum --check --quiet - >/dev/null 2>&1
}

out=$repo/build/llama/$sha
mkdir -p "$repo/build/llama"
incoming=$(mktemp -d "$repo/build/llama/.incoming-XXXXXX")
trap 'rm -rf -- "$incoming"' EXIT
if pinned "$out/$file"; then
  cp -- "$out/$file" "$incoming/$file"
else
  curl --fail --location --silent --show-error --proto '=https' --retry 3 --output "$incoming/$file" "$source"
  if ! pinned "$incoming/$file"; then
    echo "the downloaded llama.cpp does not match its manifest: $source" >&2
    exit 4
  fi
fi
# The manifest's members: the server, the ggml backends it loads at run time
# (one per CPU variant), the libraries it links and the licence; not the
# other tools' libraries.
mapfile -t members < <(jq -er '.members[]' "$manifest")
for member in "${members[@]}"; do
  [[ "$member" =~ ^[A-Za-z0-9][A-Za-z0-9._*-]*$ ]] || { echo "invalid llama.cpp member: $member" >&2; exit 2; }
done
mkdir "$incoming/bin"
tar -C "$incoming/bin" --no-same-owner --strip-components=1 -xzf "$incoming/$file" --wildcards \
  "${members[@]/#/$directory/}"
chmod 0755 "$incoming/bin/$binary"
rm -rf -- "$out"
mv -T -- "$incoming" "$out"
trap - EXIT
echo "$out/bin/$binary"
