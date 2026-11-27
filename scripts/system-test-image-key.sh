#!/usr/bin/env bash
# Canonical key of the shared system-test image, used by CI's cache key, the
# server's builder and the checkout-side environment check, so the rule lives
# in one place.
#
#   scripts/system-test-image-key.sh            <week>-<inputs> (CI cache key)
#   scripts/system-test-image-key.sh --week     ISO week (a weekly rebuild
#                                               brings Debian's updates in)
#   scripts/system-test-image-key.sh --inputs   hash of the image inputs
#
# <inputs> is the SHA-256 of `sha256sum` over the four files: one digest line
# per file, so moving bytes between the files changes the hash. -t keeps the
# lines as Linux writes them where sha256sum defaults to binary mode and marks
# each file with `*` (Git for Windows), which would change the hash.
set -euo pipefail
repo=$(cd "$(dirname "$0")/.." && pwd)
files=(
  scripts/build-system-test-image.sh
  deploy/debian/a-nas-host/DEBIAN/control
  deploy/debian/a-nas-docker-policy/DEBIAN/control
  deploy/upstream/packages.json
)
for f in "${files[@]}"; do
  [[ -f "$repo/$f" ]] || { echo "missing $f" >&2; exit 2; }
done
inputs=$( (cd "$repo" && sha256sum -t "${files[@]}") | sha256sum | awk '{print $1}')
week=$(date -u +%G-W%V)
case "${1:-}" in
  "") echo "$week-$inputs" ;;
  --week) echo "$week" ;;
  --inputs) echo "$inputs" ;;
  -h|--help) sed -n '2,13p' "$0" ;;
  *) echo "unknown argument: $1" >&2; exit 2 ;;
esac
