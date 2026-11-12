#!/usr/bin/env bash
# Builds the Debian 13 image for scripts/system-test.sh: systemd as PID 1 plus
# what the a-nas-host package depends on (ADR 0031). The system test installs
# the release's packages on it with apt from a local repository, which fails
# when a dependency a package lists is missing. GnuPG and aptly in it make
# that repository (scripts/apt-repository.sh). It is assembled with
# debootstrap from deb.debian.org, so it works where Docker Hub is not
# reachable; BUILDER is any local Debian image that can install debootstrap.
#
# Usage: scripts/build-system-test-image.sh [IMAGE] [BUILDER]
set -euo pipefail

image=${1:-anas-systemd:trixie}
builder=${2:-debian:bookworm}
repo=$(cd "$(dirname "$0")/.." && pwd)
packages=systemd,systemd-sysv,dbus,
packages+=$(sed -n 's/^Depends: //p' "$repo/deploy/debian/a-nas-host/DEBIAN/control" | tr -d ' '),
# What a-nas-host suggests and the system test turns on: the internet entry's
# firewall and tunnel tools (Caddy itself comes from its own repository
# below, ADR 0015) and the AI assistant sandbox (ai/assistant-spike): PID 1 in
# its PID namespace, the model socket bridge and the file search Hermes runs.
packages+=nftables,wireguard-tools,tini,socat,ripgrep,
# Only for the tests: SMB and HTTP clients, JSON and YAML edits, HEIC encoding
# for fixtures, and chattr, which scripts/root-integration-test.sh needs.
packages+=smbclient,curl,ca-certificates,procps,util-linux,passwd,iproute2,jq,yq,libheif-examples,libheif-plugin-x265,e2fsprogs,
# The signed apt repository that make release and the system test make
# (scripts/apt-repository.sh).
packages+=aptly,gnupg

work=$(mktemp -d)
trap 'rm -rf -- "$work"' EXIT
docker run --rm --privileged -v "$work:/out" -e PACKAGES="$packages" "$builder" bash -c '
  set -euo pipefail
  apt-get update -qq && apt-get install -y -qq debootstrap curl gnupg ca-certificates >/dev/null
  debootstrap --variant=minbase --include="$PACKAGES" trixie /rootfs http://deb.debian.org/debian >/out/debootstrap.log 2>&1 ||
    { tail -20 /out/debootstrap.log >&2; exit 1; }
  # Caddy as the LAN and internet entries run it: from Caddy'"'"'s repository,
  # as docs/runbooks/enable-lan-web-access.md installs it. The internet entry
  # needs its proxy_protocol listener (2.7 and later).
  curl -1sSLf https://dl.cloudsmith.io/public/caddy/stable/gpg.key |
    gpg --dearmor -o /rootfs/usr/share/keyrings/caddy-stable-archive-keyring.gpg
  curl -1sSLf https://dl.cloudsmith.io/public/caddy/stable/debian.deb.txt -o /rootfs/etc/apt/sources.list.d/caddy-stable.list
  { echo "Package: caddy"; echo "Pin: origin dl.cloudsmith.io"; echo "Pin-Priority: 700"; } > /rootfs/etc/apt/preferences.d/caddy
  cp /etc/resolv.conf /rootfs/etc/resolv.conf
  chroot /rootfs apt-get update -qq
  chroot /rootfs apt-get install -y -qq --no-install-recommends caddy >/out/caddy.log 2>&1 ||
    { tail -20 /out/caddy.log >&2; exit 1; }
  # Not grep -q: stopping at the match would leave caddy writing to a closed
  # pipe, which pipefail reports as a failure (exit 141).
  chroot /rootfs caddy list-modules | grep -x caddy.listeners.proxy_protocol >/dev/null
  rm -rf /rootfs/var/cache/apt/archives/*.deb
  tar -C /rootfs -czf /out/rootfs.tar.gz .
  chmod 0644 /out/rootfs.tar.gz'
docker import --change 'CMD ["/sbin/init"]' --change 'STOPSIGNAL SIGRTMIN+3' "$work/rootfs.tar.gz" "$image" >/dev/null
docker run --rm "$image" systemctl --version | head -1
