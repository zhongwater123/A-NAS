#!/usr/bin/env bash
# Cuts a model in the release content store into parts with llama.cpp's
# gguf-split, so that each content package of it stays under GitHub's 2 GiB
# limit for a release file (docs/decisions/2026-11-11-update-hosting.md).
# llama-server loads a model in parts from its first part. The parts go
# beside the model in CONTENT/models/<sha256>/, and the manifest lists them by
# size and SHA-256 (scripts/lib/content.sh model_files). For a manifest that
# lists them already, the parts are made again and must come out the same.
#
# The tool is llama-gguf-split from the llama.cpp build that
# deploy/llama/llama.json pins, taken from CONTENT/llama (scripts/fetch-llama.sh).
#
# Usage: scripts/split-model.sh MANIFEST CONTENT
set -euo pipefail

if [[ $# -ne 2 ]]; then
  echo "usage: $0 MANIFEST CONTENT" >&2
  exit 2
fi
repo=$(cd "$(dirname "$0")/.." && pwd)
manifest=$(cd "$(dirname "$1")" && pwd)/$(basename "$1")
content=$(cd "$2" && pwd)
# Each part well under 2 GiB, with room for the package around it.
max_size=1800M
limit=$((2 * 1024 * 1024 * 1024 - 64 * 1024 * 1024))

field() { jq -er --arg key "$2" '.[$key]' "$1"; }
pinned() {
  [[ -f "$1" && "$(stat -c %s "$1")" == "$2" ]] &&
    printf '%s  %s\n' "$3" "$1" | sha256sum --check --quiet - >/dev/null 2>&1
}
sha=$(field "$manifest" sha256)
file=$(field "$manifest" file)
[[ "$file" == *.gguf ]] || { echo "$manifest: gguf-split cuts .gguf files, not $file" >&2; exit 2; }
source=$content/models/$sha/$file
pinned "$source" "$(field "$manifest" sizeBytes)" "$sha" || { echo "missing or not as pinned: $source" >&2; exit 2; }
llama=$repo/deploy/llama/llama.json
tarball=$content/llama/$(field "$llama" sha256)/$(field "$llama" file)
pinned "$tarball" "$(field "$llama" sizeBytes)" "$(field "$llama" sha256)" ||
  { echo "missing or not as pinned: $tarball (scripts/fetch-llama.sh)" >&2; exit 2; }

# The parts are as large as the model: they are made in /var/tmp, not in a
# /tmp that may be a small tmpfs.
work=$(mktemp -d "${ANAS_CONTENT_WORK:-/var/tmp}/a-nas-split.XXXXXX")
trap 'rm -rf -- "$work"' EXIT
directory=$(field "$llama" directory)
tar -C "$work" --no-same-owner -xzf "$tarball" "$directory"
mkdir "$work/parts"
"$work/$directory/llama-gguf-split" --split --split-max-size "$max_size" "$source" "$work/parts/${file%.gguf}" >"$work/split.log" 2>&1 ||
  { tail -n 20 "$work/split.log" >&2; exit 1; }

parts='[]'
for part in "$work"/parts/*.gguf; do
  size=$(stat -c %s "$part")
  ((size < limit)) || { echo "$(basename "$part") has $size bytes: more than a release file may" >&2; exit 1; }
  parts=$(jq -c --arg file "$(basename "$part")" --argjson size "$size" --arg sha "$(sha256sum "$part" | cut -d ' ' -f 1)" \
    '. + [{file: $file, sizeBytes: $size, sha256: $sha}]' <<<"$parts")
done
(($(jq length <<<"$parts") > 1)) || { echo "$file fits one part: it needs no split" >&2; exit 2; }

if jq -e 'has("parts")' "$manifest" >/dev/null; then
  if [[ "$(jq -c .parts "$manifest")" != "$parts" ]]; then
    echo "the parts differ from the ones $manifest lists: gguf-split did not repeat itself" >&2
    exit 1
  fi
else
  note="llama-gguf-split --split --split-max-size $max_size from llama.cpp $(field "$llama" version) (deploy/llama/llama.json); scripts/split-model.sh"
  jq --argjson parts "$parts" --arg note "$note" '. + {split: $note, parts: $parts}' "$manifest" >"$work/manifest.json"
  mv -f -- "$work/manifest.json" "$manifest"
fi
for part in "$work"/parts/*.gguf; do
  target=$content/models/$sha/$(basename "$part")
  pinned "$target" "$(stat -c %s "$part")" "$(sha256sum "$part" | cut -d ' ' -f 1)" || cp -- "$part" "$target"
done
jq -r '.parts[] | "\(.file) \(.sizeBytes) \(.sha256)"' "$manifest"
