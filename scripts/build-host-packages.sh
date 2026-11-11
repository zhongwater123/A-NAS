#!/usr/bin/env bash
# Builds the host packages of ADR 0031 from deploy/debian: OUT/a-nas-host.deb
# and OUT/a-nas-console.deb, at VERSION, the product's Debian version that
# scripts/release-version.sh gives (the Makefile's VERSION).
#
# Usage: scripts/build-host-packages.sh OUT VERSION
set -euo pipefail

if [[ $# -ne 2 ]]; then
  echo "usage: $0 OUT VERSION" >&2
  exit 2
fi
out=$1
debian_version=$2
repo=$(cd "$(dirname "$0")/.." && pwd)
# A Debian upstream version without a revision (scripts/release-version.sh).
if [[ ! "$debian_version" =~ ^[0-9][A-Za-z0-9.+~]*$ ]]; then
  echo "not a Debian version: $debian_version (see scripts/release-version.sh)" >&2
  exit 2
fi

# The same commit builds the same packages.
SOURCE_DATE_EPOCH=${SOURCE_DATE_EPOCH:-$(git -C "$repo" log -1 --format=%ct 2>/dev/null || echo 0)}
export SOURCE_DATE_EPOCH

work=$(mktemp -d)
trap 'rm -rf -- "$work"' EXIT
mkdir -p "$out"
for package in a-nas-host a-nas-console; do
  source=$repo/deploy/debian/$package
  install -d -m 0755 "$work/$package/DEBIAN"
  sed "s/@VERSION@/$debian_version/g" "$source/DEBIAN/control" >"$work/$package/DEBIAN/control"
  chmod 0644 "$work/$package/DEBIAN/control"
  dpkg-deb --root-owner-group -Zxz --build "$work/$package" "$out/$package.deb" >/dev/null
  printf '%s %s\n' "$(dpkg-deb --field "$out/$package.deb" Package)" "$(dpkg-deb --field "$out/$package.deb" Version)"
done
