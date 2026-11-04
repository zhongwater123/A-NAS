#!/usr/bin/env bash
# Builds the host packages of ADR 0031 from deploy/debian: OUT/a-nas-host.deb
# and OUT/a-nas-console.deb, at the Debian form of VERSION, the product version
# (the Makefile's VERSION). A release tag v1.0.1-rc.7 becomes 1.0.1~rc.7, which
# Debian sorts before 1.0.1; anything else, such as a commit or "dev", becomes
# 0~<it>. The installer only compares versions for equality, so a version
# names a release and does not order them: commits sort by their hashes, and
# every commit after a tag sorts before it. Hosts install the packages with
# apt's --allow-downgrades.
#
# Usage: scripts/build-host-packages.sh OUT VERSION
set -euo pipefail

if [[ $# -ne 2 ]]; then
  echo "usage: $0 OUT VERSION" >&2
  exit 2
fi
out=$1
version=$2
repo=$(cd "$(dirname "$0")/.." && pwd)

if [[ "$version" =~ ^v?([0-9]+\.[0-9]+\.[0-9]+)(-([0-9A-Za-z.-]+))?(\+([0-9A-Za-z.-]+))?$ ]]; then
  debian_version=${BASH_REMATCH[1]}
  if [[ -n "${BASH_REMATCH[3]}" ]]; then debian_version+="~${BASH_REMATCH[3]//-/.}"; fi
  if [[ -n "${BASH_REMATCH[5]}" ]]; then debian_version+="+${BASH_REMATCH[5]//-/.}"; fi
else
  debian_version="0~$(printf '%s' "$version" | tr -c 'A-Za-z0-9.+~' '.')"
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
  if [[ -f "$source/DEBIAN/postinst" ]]; then
    install -m 0755 "$source/DEBIAN/postinst" "$work/$package/DEBIAN/postinst"
  fi
  dpkg-deb --root-owner-group -Zxz --build "$work/$package" "$out/$package.deb" >/dev/null
  printf '%s %s\n' "$(dpkg-deb --field "$out/$package.deb" Package)" "$(dpkg-deb --field "$out/$package.deb" Version)"
done
