#!/usr/bin/env bash
# Checks that neither the repository nor the packages built into PACKAGES
# hold any music (docs/specs/montage-center.md, 配乐): the songs the montage
# center is tested with are commercial recordings that stay on the
# development machine and on the Experimental NAS. A file counts as music by
# its name, or by its content as libmagic reads it, so a renamed copy is
# found too. make packages runs it after building the packages.
#
# Usage: scripts/check-no-songs.sh PACKAGES
set -euo pipefail

if [[ $# -ne 1 || ! -d "$1" ]]; then
  echo "usage: $0 PACKAGES" >&2
  exit 2
fi
command -v file >/dev/null || { echo "$0 needs file(1)" >&2; exit 2; }
repo=$(cd "$(dirname "$0")/.." && pwd)
packages=$(cd "$1" && pwd)
extensions='\.(mp3|flac|wav|ogg|oga|opus|m4a|aac|ape|wma|aiff?|wv|mka|dsf|dff|alac)$'

# music LIST: the files of a NUL-separated LIST that are music, one per line.
music() {
  local list=$1
  { tr '\0' '\n' <"$list" | grep -iE "$extensions" || true
    xargs -0 -r file -N -F $'\t' --mime-type -- <"$list" | awk -F '\t' '$2 ~ /^ *audio\// { print $1 }'
  } | sort -u
}

work=$(mktemp -d)
trap 'rm -rf -- "$work"' EXIT
found=0
git -C "$repo" ls-files -z | while IFS= read -r -d '' path; do
  [[ -f "$repo/$path" ]] && printf '%s\0' "$repo/$path"
done >"$work/repository"
if hits=$(music "$work/repository") && [[ -n "$hits" ]]; then
  echo "the repository holds music:" >&2
  printf '  %s\n' "$hits" >&2
  found=1
fi
shopt -s nullglob
for deb in "$packages"/*.deb; do
  rm -rf "$work/root" && mkdir "$work/root"
  dpkg-deb -x "$deb" "$work/root"
  find "$work/root" -type f -print0 >"$work/files"
  if hits=$(music "$work/files") && [[ -n "$hits" ]]; then
    echo "$(basename "$deb") holds music:" >&2
    printf '  %s\n' "${hits//$work\/root/}" >&2
    found=1
  fi
done
exit "$found"
