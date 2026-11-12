#!/usr/bin/env bash
# Fetches the model files that deploy/models/*.json pin from the private
# repository zhongwater123/A-NAS-assets, so a development machine needs
# neither Hugging Face nor the conversions that made some of them (D-FINE's
# ONNX export, the text reader's GGUF files). Each file is the release
# sha256-<its SHA-256> there. A file of 2 GiB or more, which GitHub refuses as
# one asset, is split into <file>.part-NN, joined in order. Every file is
# checked against its manifest's size and SHA-256 before it takes its place
# in DIR, in the directories listed below (layout), from which the
# release content store is filled (scripts/build-content-packages.sh). A file already in place that
# passes the check is kept. An interrupted download keeps the parts it
# finished in DIR/.incoming/<sha256>/; a file that fails the check is
# removed, and the next run downloads it again.
#
# --publish uploads the files the given manifests pin from the same layout in
# DIR, after the same check. It skips a model whose release exists already:
# a release is never changed, and a new file is a new release.
#
# Usage: scripts/fetch-models.sh DIR [MANIFEST...]
#        scripts/fetch-models.sh --publish DIR MANIFEST...
# DIR is D:\A-NAS-models (/mnt/d/A-NAS-models in WSL) on Windows and
# ~/A-NAS-models on macOS. Without MANIFEST every manifest in deploy/models
# is fetched. Both forms need jq and gh signed in to an account that can read
# (for --publish, write) the repository; ANAS_ASSETS_REPO names another one.
set -euo pipefail

repo=$(cd "$(dirname "$0")/.." && pwd)
assets=${ANAS_ASSETS_REPO:-zhongwater123/A-NAS-assets}
asset_limit=$((2 * 1024 * 1024 * 1024))
part_bytes=$((1024 * 1024 * 1024))

publish=false
if [[ "${1:-}" == --publish ]]; then
  publish=true
  shift
fi
if [[ $# -lt 1 ]] || { $publish && [[ $# -lt 2 ]]; }; then
  echo "usage: $0 DIR [MANIFEST...] | --publish DIR MANIFEST..." >&2
  exit 2
fi
mkdir -p -- "$1"
root=$(cd -- "$1" && pwd)
shift
if [[ $# -eq 0 ]]; then
  set -- "$repo"/deploy/models/*.json
fi

# layout NAME: the directory under DIR that holds the model NAME.
layout() {
  case $1 in
    embeddinggemma-2-740m) echo embeddinggemma-2-740m ;;
    dfine-m-obj365) echo dfine-obj365/official ;;
    yunet-2026may) echo faces/yunet ;;
    sface-2021dec) echo faces/sface ;;
    auraface-v1) echo faces/auraface ;;
    dinov2-small) echo pets/dinov2-small ;;
    qwen3.5-4b-q4_k_m | qwen3.5-4b-mmproj-f16) echo ocr/qwen3.5-4b ;;
    *) return 1 ;;
  esac
}

# macOS has shasum but not sha256sum.
sha256() {
  if command -v sha256sum >/dev/null; then sha256sum -- "$1"; else shasum -a 256 -- "$1"; fi | cut -d ' ' -f 1
}
# pinned FILE SIZE SHA256: FILE has that size and SHA-256.
pinned() {
  [[ -f "$1" && "$(wc -c <"$1" | tr -d ' ')" == "$2" && "$(sha256 "$1")" == "$3" ]]
}

# fetch NAME FILE SIZE SHA256 DIRECTORY
fetch() {
  local target=$5/$2 incoming=$root/.incoming/$4 listing bytes asset parts
  if pinned "$target" "$3" "$4"; then
    echo "$target"
    return
  fi
  mkdir -p -- "$incoming"
  # gh writes an asset under its own name as it downloads, so a part an
  # interruption cut short goes before --skip-existing would keep it.
  if listing=$(gh release view "sha256-$4" --repo "$assets" --json assets --jq '.assets[] | "\(.size) \(.name)"'); then
    while read -r bytes asset; do
      if [[ -f "$incoming/$asset" && "$(wc -c <"$incoming/$asset" | tr -d ' ')" != "$bytes" ]]; then
        rm -f -- "$incoming/$asset"
      fi
    done <<<"$listing"
  fi
  if ! gh release download "sha256-$4" --repo "$assets" --dir "$incoming" --skip-existing; then
    rmdir -- "$incoming" "$root/.incoming" 2>/dev/null || true
    echo "cannot download $1 (release sha256-$4) from $assets" >&2
    exit 4
  fi
  if [[ ! -f "$incoming/$2" ]]; then
    parts=("$incoming/$2".part-*)
    if [[ ! -f "${parts[0]}" ]]; then
      echo "release sha256-$4 in $assets holds no $2" >&2
      exit 4
    fi
    cat -- "${parts[@]}" >"$incoming/$2"
    rm -f -- "${parts[@]}"
  fi
  if ! pinned "$incoming/$2" "$3" "$4"; then
    rm -rf -- "$incoming"
    echo "the downloaded $2 does not match its manifest; run again to download it again" >&2
    exit 4
  fi
  mkdir -p -- "$5"
  mv -f -- "$incoming/$2" "$target"
  rm -rf -- "$incoming"
  rmdir -- "$root/.incoming" 2>/dev/null || true
  echo "$target"
}

# publish NAME FILE SIZE SHA256 DIRECTORY MANIFEST
publish() {
  local source=$5/$2 tag=sha256-$4 draft outgoing files fence
  if ! pinned "$source" "$3" "$4"; then
    echo "$source does not match $6" >&2
    exit 4
  fi
  draft=$(gh release view "$tag" --repo "$assets" --json isDraft --jq .isDraft 2>/dev/null || echo none)
  if [[ "$draft" == false ]]; then
    echo "$1 is published already: $tag"
    return
  fi
  if [[ "$draft" == true ]]; then
    echo "$tag in $assets is a draft left by an interrupted upload: delete it, then run again" >&2
    exit 3
  fi
  outgoing=$(mktemp -d "$root/.outgoing-XXXXXX")
  files=("$source")
  if (($3 >= asset_limit)); then
    split -b "$part_bytes" -d -a 2 -- "$source" "$outgoing/$2.part-"
    files=("$outgoing/$2".part-*)
  fi
  fence='```'
  {
    printf 'A-NAS deploy/models/%s:\n\n%sjson\n' "${6##*/}" "$fence"
    cat -- "$6"
    printf '%s\n' "$fence"
  } >"$outgoing/notes.md"
  # gh uploads into a draft and publishes it once every asset is in.
  if ! gh release create "$tag" --repo "$assets" --latest=false --title "$1 ($2)" \
    --notes-file "$outgoing/notes.md" "${files[@]}"; then
    rm -rf -- "$outgoing"
    echo "cannot publish $1 to $assets" >&2
    exit 4
  fi
  rm -rf -- "$outgoing"
  echo "$1 published: $tag"
}

for manifest in "$@"; do
  field() { jq -er --arg key "$1" '.[$key]' "$manifest"; }
  name=$(field name)
  file=$(field file)
  size=$(field sizeBytes)
  sha=$(field sha256)
  if [[ ! "$sha" =~ ^[0-9a-f]{64}$ || ! "$size" =~ ^[0-9]+$ || ! "$file" =~ ^[A-Za-z0-9][A-Za-z0-9._-]*$ ]]; then
    echo "invalid model manifest: $manifest" >&2
    exit 2
  fi
  if ! directory=$(layout "$name"); then
    echo "no directory for $name: add it to the layout in $0" >&2
    exit 2
  fi
  if $publish; then
    publish "$name" "$file" "$size" "$sha" "$root/$directory" "$manifest"
  else
    fetch "$name" "$file" "$size" "$sha" "$root/$directory"
  fi
done
