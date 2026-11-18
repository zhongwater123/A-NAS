#!/usr/bin/env bash
# Checks that neither the repository nor the packages built into PACKAGES
# hold any music (docs/specs/montage-center.md, 配乐): the songs the montage
# center is tested with are commercial recordings that stay on the
# development machine and on the Experimental NAS. A file counts as music by
# its name, or by how its content starts (the formats' magic numbers), so a
# renamed copy is found too. make packages runs it after building the
# packages.
#
# Usage: scripts/check-no-songs.sh PACKAGES
set -euo pipefail

if [[ $# -ne 1 || ! -d "$1" ]]; then
  echo "usage: $0 PACKAGES" >&2
  exit 2
fi
repo=$(cd "$(dirname "$0")/.." && pwd)
packages=$(cd "$1" && pwd)
extensions='\.(mp3|flac|wav|ogg|oga|opus|m4a|aac|ape|wma|aiff?|wv|mka|dsf|dff|alac)$'

# music LIST: the files of a NUL-separated LIST that are music, one per line.
music() {
  local list=$1
  { tr '\0' '\n' <"$list" | grep -iE "$extensions" || true
    python3 -I - "$list" <<'PY'
import sys

# How audio files start: ID3 tags or an MPEG audio frame, FLAC, WAV, Ogg,
# Monkey's Audio, WavPack, AIFF, ASF (WMA) and an MP4 of audio brands.
starts = (b"ID3", b"\xff\xfb", b"\xff\xf3", b"\xff\xf2", b"\xff\xfa", b"fLaC", b"OggS", b"MAC ", b"wvpk",
          b"\x30\x26\xb2\x75\x8e\x66\xcf\x11")
with open(sys.argv[1], "rb") as names:
    for name in filter(None, names.read().split(b"\0")):
        try:
            with open(name, "rb") as f:
                head = f.read(12)
        except OSError:
            continue
        if (head.startswith(starts) or head[:4] == b"RIFF" and head[8:12] == b"WAVE"
                or head[:4] == b"FORM" and head[8:12] in (b"AIFF", b"AIFC")
                or head[4:8] == b"ftyp" and head[8:11] in (b"M4A", b"M4B")):
            print(name.decode(errors="replace"))
PY
  } | sort -u
}

work=$(mktemp -d)
trap 'rm -rf -- "$work"' EXIT
found=0
# The repository's files; a copy without .git, as the development
# machine's virtual machine checks, lists its tree instead.
if git -C "$repo" rev-parse --git-dir >/dev/null 2>&1; then
  git -C "$repo" ls-files -z | while IFS= read -r -d '' path; do
    [[ -f "$repo/$path" ]] && printf '%s\0' "$repo/$path"
  done
else
  find "$repo" \( -name .git -o -name node_modules -o -path "$repo/build" -o -path "$repo/.claude/worktrees" \) -prune \
    -o -type f -print0
fi >"$work/repository"
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
