#!/usr/bin/env bash
# Builds the Debian 13 image every video edit plan is measured in
# (docs/specs/video-edit-evaluation.md). debootstrap takes every package from
# one snapshot.debian.org time point, so two machines that build it get the
# same versions; the harness's own Python packages go on top by hash
# (requirements.lock). BUILDER is any local Debian image that can install
# debootstrap, so it works where Docker Hub is not reachable, as
# scripts/build-system-test-image.sh does.
#
# Usage: ai/eval/video_edit/image/build.sh [IMAGE] [BUILDER]
set -euo pipefail

here=$(dirname "$(readlink -f "$0")")
image=${1:-anas-video-edit-eval:v1}
builder=${2:-debian:bookworm}
# Changing the time point changes the environment fingerprint: both plans must
# then be measured again (口径 v2).
snapshot=20261102T000000Z
packages=ffmpeg,python3,python3-venv,ca-certificates,curl

work=$(mktemp -d)
trap 'rm -rf -- "$work"' EXIT
docker run --rm --privileged -v "$work:/out" -e PACKAGES="$packages" -e SNAPSHOT="$snapshot" "$builder" bash -c '
  set -euo pipefail
  apt-get update -qq && apt-get install -y -qq debootstrap ca-certificates >/dev/null
  debootstrap --variant=minbase --include="$PACKAGES" trixie /rootfs "https://snapshot.debian.org/archive/debian/$SNAPSHOT/" \
    >/out/debootstrap.log 2>&1 || { tail -20 /out/debootstrap.log >&2; exit 1; }
  rm -rf /rootfs/var/cache/apt/archives/*.deb /rootfs/var/lib/apt/lists/*
  tar -C /rootfs -czf /out/rootfs.tar.gz .
  chmod 0644 /out/rootfs.tar.gz'
docker import "$work/rootfs.tar.gz" "$image-base" >/dev/null
docker build --quiet --build-arg BASE="$image-base" --label "anas.snapshot=$snapshot" -t "$image" "$here" >/dev/null
docker run --rm "$image" ffmpeg -hide_banner -version | head -1
