#!/usr/bin/env bash
# Build the optional radio metapackage for ANAS_TARGET_ARCH (default: native).
# Usage: scripts/build-hotspot-radio-package.sh OUT VERSION
set -euo pipefail

if [[ $# -ne 2 || ! "$2" =~ ^[0-9][A-Za-z0-9.+~]*$ ]]; then
  echo "usage: $0 OUT VERSION (a product Debian version)" >&2
  exit 2
fi
repo=$(cd "$(dirname "$0")/.." && pwd)
arch=${ANAS_TARGET_ARCH:-$(dpkg --print-architecture)}
case "$arch" in
  amd64|arm64) ;;
  *) echo "unsupported hotspot radio architecture: $arch" >&2; exit 2 ;;
esac
lock=$repo/deploy/upstream/hotspot-radio-$arch.json
depends=$(jq -er --arg arch "$arch" 'select(.architecture == $arch) |
  .packages | select(length > 0) | map("\(.name) (>= \(.version))") | join(", ")' "$lock")

mkdir -p "$1"
work=$(mktemp -d)
trap 'rm -rf -- "$work"' EXIT
install -d -m 0755 "$work/DEBIAN"
chmod 0755 "$work"
sed -e "s/@VERSION@/$2/g" -e "s/@ARCH@/$arch/g" -e "s/@DEPENDS@/$depends/g" \
  "$repo/deploy/debian/a-nas-hotspot-radio/DEBIAN/control" > "$work/DEBIAN/control"
chmod 0644 "$work/DEBIAN/control"
SOURCE_DATE_EPOCH=${SOURCE_DATE_EPOCH:-$(git -C "$repo" log -1 --format=%ct 2>/dev/null || echo 0)}
export SOURCE_DATE_EPOCH
dpkg-deb --root-owner-group -Zxz --build "$work" "$1/a-nas-hotspot-radio_${2}_$arch.deb"
