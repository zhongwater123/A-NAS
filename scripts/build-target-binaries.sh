#!/usr/bin/env bash
# Build real Linux binaries for a supported Debian target; CGO stays enabled.
set -euo pipefail
repo=$(cd "$(dirname "$0")/.." && pwd)
# shellcheck source=lib/target.sh
. "$repo/scripts/lib/target.sh"
arch=$(target_arch)
native=$(dpkg --print-architecture)
out=${1:-$repo/build}
version=${2:-0~dev}
compiler=${CC:-gcc}
if [[ "$arch" != "$native" && -z "${CC:-}" ]]; then
  case "$arch" in
    arm64) compiler=aarch64-linux-gnu-gcc ;;
    amd64) compiler=x86_64-linux-gnu-gcc ;;
  esac
fi
command -v "$compiler" >/dev/null || { echo "missing C compiler for $arch: $compiler" >&2; exit 2; }
mkdir -p "$out"
cd "$repo"
env GOOS=linux GOARCH="$arch" CGO_ENABLED=1 CC="$compiler" \
  "${GO:-go}" build -trimpath -ldflags "-X main.version=$version" -o "$out/" \
  ./cmd/anas-api ./cmd/anas-host-agent ./cmd/anas-container-agent ./cmd/anas-scoring
for binary in anas-api anas-host-agent anas-container-agent anas-scoring; do
  require_target_elf "$out/$binary"
done
