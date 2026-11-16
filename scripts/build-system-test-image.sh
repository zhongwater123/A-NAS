#!/usr/bin/env bash
# Builds the Debian 13 image for scripts/system-test.sh: systemd as PID 1 plus
# what the a-nas-host package depends on from Debian (ADR 0031), with the
# Debian packages that Docker and Caddy depend on. The system test installs
# the release's packages on it with apt from a local repository alone, which
# fails when a dependency a package lists is missing. Docker and Caddy
# themselves come from that repository, as deploy/upstream/packages.json pins
# them (ADR 0015). GnuPG and aptly in the image make that repository
# (scripts/apt-repository.sh). It is assembled with debootstrap from
# deb.debian.org, so it works where Docker Hub is not reachable; BUILDER is
# any local Debian image that can install debootstrap. Needs jq.
#
# Usage: scripts/build-system-test-image.sh [IMAGE] [BUILDER]
set -euo pipefail

image=${1:-anas-systemd:trixie}
builder=${2:-debian:bookworm}
repo=$(cd "$(dirname "$0")/.." && pwd)
lock=$repo/deploy/upstream/packages.json
packages=systemd,systemd-sysv,dbus,
# The packages that a-nas-host, a-nas-docker-policy and the upstream packages
# depend on: the first of each alternative, without its version, and not the
# upstream packages, which a-nas-host's control file names as @UPSTREAM@, nor
# A-NAS's own packages, such as its keyring.
packages+=$(
  { sed -n 's/^Depends: //p' "$repo"/deploy/debian/{a-nas-host,a-nas-docker-policy}/DEBIAN/control; jq -r '.packages[].depends' "$lock"; } |
    sed -e 's/([^)]*)//g' -e 's/|[^,]*//g' | tr ',' '\n' | tr -d ' ' | grep -v -e '^$' -e '^@' -e '^a-nas-' |
    grep -vxF -f <(jq -r '.packages[].name' "$lock") | sort -u | paste -sd, -
),
# What the AI assistant's trial depends on (ai/assistant-spike): PID 1 in its
# PID namespace, the model socket bridge and the file search Hermes runs.
packages+=tini,socat,ripgrep,
# Only for the tests: SMB and HTTP clients, JSON and YAML edits, HEIC encoding
# for fixtures, and chattr, which scripts/root-integration-test.sh needs.
packages+=smbclient,curl,ca-certificates,procps,util-linux,passwd,iproute2,jq,yq,libheif-examples,libheif-plugin-x265,e2fsprogs,
# The signed apt repository that make release and the system test make
# (scripts/apt-repository.sh), and its flat form on GitHub, whose index
# apt-ftparchive makes (scripts/publish-github.sh).
packages+=aptly,gnupg,apt-utils

work=$(mktemp -d)
trap 'rm -rf -- "$work"' EXIT
docker run --rm --privileged -v "$work:/out" -e PACKAGES="$packages" "$builder" bash -c '
  set -euo pipefail
  apt-get update -qq && apt-get install -y -qq debootstrap >/dev/null
  debootstrap --variant=minbase --include="$PACKAGES" trixie /rootfs http://deb.debian.org/debian >/out/debootstrap.log 2>&1 ||
    { tail -20 /out/debootstrap.log >&2; exit 1; }
  rm -rf /rootfs/var/cache/apt/archives/*.deb
  tar -C /rootfs -czf /out/rootfs.tar.gz .
  chmod 0644 /out/rootfs.tar.gz'
docker import --change 'CMD ["/sbin/init"]' --change 'STOPSIGNAL SIGRTMIN+3' "$work/rootfs.tar.gz" "$image" >/dev/null
docker run --rm "$image" systemctl --version | head -1
