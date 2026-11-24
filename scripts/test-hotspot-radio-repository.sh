#!/usr/bin/env bash
# Test the actual signed and flat repositories on the runner's architecture.
set -euo pipefail
repo=$(cd "$(dirname "$0")/.." && pwd)
cd "$repo"
arch=$(dpkg --print-architecture)
work=$(mktemp -d)
trap 'rm -rf -- "$work"' EXIT
image=anas-hotspot-signing:$arch
docker build -t "$image" - <<'DOCKERFILE'
FROM debian:trixie-slim
RUN apt-get update && apt-get install -y --no-install-recommends aptly apt-utils gnupg ca-certificates \
    libdbus-1-3 libgmp10 libhogweed6t64 libidn2-0 libnetfilter-conntrack3 libnettle8t64 libnftables1 libssl3t64
DOCKERFILE
export IMAGE=$image ANAS_TARGET_ARCH=$arch ANAS_RELEASE_TEST_KEY=1 ANAS_RELEASE_TEST_GNUPGHOME=$work/key
mkdir "$work/content"
bash scripts/release.sh --hotspot-radio-only "$work/repository" "$work/content"
index=$work/repository/dists/dev/main/binary-$arch/Packages
grep -qx 'Package: a-nas-hotspot-radio' "$index"
grep -qx "Architecture: $arch" "$index"
bash scripts/publish-github.sh flat "$work/flat" "$work/repository" dev "$ANAS_RELEASE_TEST_GNUPGHOME"
grep -qx "Architectures: $arch" "$work/flat/Release"
keyring=$(find "$work/repository/pool" -name 'a-nas-archive-keyring_*_all.deb' -print -quit)
dpkg-deb -x "$keyring" "$work/keyring"
key=$work/keyring/usr/share/keyrings/a-nas-archive-keyring.pgp
version=$(bash scripts/release-version.sh)
for layout in repository flat; do
  suite='dev main'
  [[ "$layout" != flat ]] || suite=./
  docker run --rm --network none -v "$work/$layout:/anas:ro" -v "$key:/anas-archive.pgp:ro" \
    "$image" bash -c '
      set -euo pipefail
      test "$(dpkg --print-architecture)" = "$1"
      printf "deb [signed-by=/anas-archive.pgp] file:/anas %s\n" "$3" >/tmp/anas.list
      apt=(apt-get -o Dir::Etc::sourcelist=/tmp/anas.list -o Dir::Etc::sourceparts=-)
      "${apt[@]}" update
      "${apt[@]}" install -y --no-install-recommends "a-nas-hotspot-radio=$2"
      dpkg-query -W -f="\${Architecture} \${Version}\n" a-nas-hotspot-radio | grep -Fx "$1 $2"
      test -x /usr/sbin/hostapd -a -x /usr/sbin/dnsmasq -a -x /usr/sbin/iw
    ' bash "$arch" "$version" "$suite"
done
