#!/usr/bin/env bash
# Checks that neither the repository nor the packages built into PACKAGES
# hold any music (docs/specs/videocut-center.md, 配乐): the songs VideoCut
# is tested with are commercial recordings that stay on the
# development machine and on the Experimental NAS. file(1) identifies media
# containers by content; ffprobe distinguishes songs (possibly with cover
# art) from video. make packages runs this check on the build host.
#
# Usage: scripts/check-no-songs.sh PACKAGES
set -euo pipefail

if [[ $# -ne 1 || ! -d "$1" ]]; then
  echo "usage: $0 PACKAGES" >&2
  exit 2
fi
repo=$(cd "$(dirname "$0")/.." && pwd)
packages=$(cd "$1" && pwd)
# music LIST: print the audio files in a NUL-separated list of regular files.
music() {
  python3 -I - "$1" <<'PY'
import json
import os
import subprocess
import sys
from pathlib import Path

song_extensions = set(".mp3 .flac .wav .ogg .oga .opus .m4a .aac .ape .wma .aif .aiff .wv .mka .dsf .dff .alac".split())
with open(sys.argv[1], "rb") as source:
    paths = [os.fsdecode(path) for path in source.read().split(b"\0") if path]

# Batch libmagic reads: even a runtime package with thousands of source and
# library files only needs FFmpeg for plausible media.
for offset in range(0, len(paths), 128):
    batch = paths[offset:offset + 128]
    types = subprocess.check_output(["file", "-Eb", "--mime-type", "--", *batch]).splitlines()
    for path, mime in zip(batch, types, strict=True):
        named = Path(path).suffix.lower() in song_extensions
        if not named and not mime.startswith((b"audio/", b"video/")) and mime not in {
            b"application/ogg", b"application/mp4", b"application/x-matroska", b"application/octet-stream",
        }:
            continue
        probe = subprocess.run(
            ["ffprobe", "-v", "error", "-protocol_whitelist", "file",
             "-show_entries", "stream=codec_type:stream_disposition=attached_pic:format=probe_score",
             "-of", "json", "-i", path], stdin=subprocess.DEVNULL, stdout=subprocess.PIPE,
            stderr=subprocess.DEVNULL, check=False,
        )
        if probe.returncode:
            if named or mime.startswith(b"audio/"):
                print(path)
            continue
        media = json.loads(probe.stdout)
        # A low-confidence guess about arbitrary binary data is not a media
        # identification. Genuine ADTS streams and other renamed recordings
        # can be identified by either libmagic or FFmpeg.
        if not named and mime == b"application/octet-stream" and media["format"]["probe_score"] <= 25:
            continue
        streams = media.get("streams", [])
        audio = any(stream["codec_type"] == "audio" for stream in streams)
        video = any(stream["codec_type"] == "video" and not stream["disposition"]["attached_pic"]
                    for stream in streams)
        if audio and not video:
            print(path)
PY
}

work=$(mktemp -d)
trap 'rm -rf -- "$work"' EXIT
found=0
# The repository's files; a copy without .git, as the development
# machine's virtual machine checks, lists its tree instead.
if [[ -e "$repo/.git" ]] && git -C "$repo" rev-parse --git-dir >/dev/null 2>&1; then
  git -C "$repo" ls-files -z | while IFS= read -r -d '' path; do
    if [[ -f "$repo/$path" && ! -L "$repo/$path" ]]; then printf '%s\0' "$repo/$path"; fi
  done
else
  find "$repo" \( -name .git -o -name node_modules -o -path "$repo/build" -o -path "$repo/.claude/worktrees" \) -prune \
    -o -type f -print0
fi >"$work/repository"
hits=$(music "$work/repository")
if [[ -n "$hits" ]]; then
  echo "the repository holds music:" >&2
  printf '  %s\n' "$hits" >&2
  found=1
fi
shopt -s nullglob
for deb in "$packages"/*.deb; do
  rm -rf "$work/root" && mkdir "$work/root"
  dpkg-deb -x "$deb" "$work/root"
  find "$work/root" -type f -print0 >"$work/files"
  hits=$(music "$work/files")
  if [[ -n "$hits" ]]; then
    echo "$(basename "$deb") holds music:" >&2
    printf '  %s\n' "${hits//$work\/root/}" >&2
    found=1
  fi
done
exit "$found"
