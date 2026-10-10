#!/usr/bin/env bash
# Builds the Debian 13 image for scripts/system-test.sh: systemd as PID 1 plus
# the host commands the installer and Host Agent need. It is assembled with
# debootstrap from deb.debian.org, so it works where Docker Hub is not
# reachable; BUILDER is any local Debian image that can install debootstrap.
#
# Usage: scripts/build-system-test-image.sh [IMAGE] [BUILDER]
set -euo pipefail

image=${1:-anas-systemd:trixie}
builder=${2:-debian:bookworm}
packages=systemd,systemd-sysv,dbus,udev,acl,btrfs-progs,parted,smartmontools,samba,smbclient,
packages+=curl,ca-certificates,procps,util-linux,passwd,iproute2,jq,python3,caddy,libegl1,libgles2,ffmpeg

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
