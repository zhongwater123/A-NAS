#!/usr/bin/env bash
# Runs the tests of the real codecs (FFmpeg, libheif) in a disposable
# container of the system test's Debian 13 image, which has the codecs the
# device has (#425): transcoding and frames, audio decoding through the
# worker, HEIC, motion photos and the video codecs. Elsewhere these tests are
# skipped for want of the tools, and go test prints no skips, so nothing
# showed that CI never ran them.
#
# The test binaries are built here, so the image needs no Go toolchain. They
# run as an ordinary user, with ANAS_CODECS_REQUIRED=1: a missing codec fails
# the test instead of skipping it. Each test named below must be seen to
# pass; -test.run exits 0 when a renamed test no longer matches.
#
# Usage: scripts/build-system-test-image.sh && scripts/codec-test.sh [IMAGE]
set -euo pipefail

image=${1:-anas-systemd:trixie}
repo=$(cd "$(dirname "$0")/.." && pwd)
tests=$(mktemp -d)
trap 'rm -rf "$tests"' EXIT
cd "$repo"

# package: the tests of the real codecs in it.
declare -A real=(
  [ffmpegworker]="TestRealFFmpeg TestRealFFmpegAudio TestRealHEIF"
  [music]="TestRealFFmpegDecodesThroughTheWorker"
  [photos]="TestRealMotionPhotoCodecs TestRealVideoCodecs"
)
for package in "${!real[@]}"; do
  CGO_ENABLED=1 go test -c -o "$tests/$package.test" "./internal/$package"
done
chmod 0755 "$tests"

failed=0
for package in "${!real[@]}"; do
  log="$tests/$package.log"
  pattern="^($(tr ' ' '|' <<<"${real[$package]}"))\$"
  # Without systemd as PID 1; the tests write only their own temporary files.
  if ! docker run --rm --entrypoint /bin/bash --user 1000:1000 -e HOME=/tmp -e ANAS_CODECS_REQUIRED=1 \
    -v "$tests:/tests:ro" -w /tmp "$image" -euc "/tests/$package.test -test.v -test.count=1 -test.run '$pattern'" >"$log" 2>&1; then
    failed=1
  fi
  cat "$log"
  for name in ${real[$package]}; do
    if ! grep -q "^--- PASS: $name " "$log"; then
      echo "codec-test: $name of internal/$package did not run and pass" >&2
      failed=1
    fi
  done
done
exit "$failed"
