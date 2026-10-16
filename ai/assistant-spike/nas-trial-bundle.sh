#!/usr/bin/env bash
# Makes the bundle nas-trial.sh installs from (docs/runbooks/try-ai-assistant-on-experimental-nas.md),
# on the development machine:
#
#   ai/assistant-spike/nas-trial-bundle.sh NAME [PACKAGE...]
#
# NAME.tar.gz holds NAME/spike (this directory), NAME/runtime.tar.gz (the runtime run.sh
# build made in $HERMES_SPIKE_DIR/out), NAME/debs (socat, ripgrep and any extra PACKAGE,
# downloaded from Debian 13 in the local image DEB_IMAGE, default anas-systemd:trixie, so
# the NAS installs nothing over the network) and NAME/SHA256SUMS. Prints the archive's
# SHA-256, which the owner checks on the NAS before unpacking.
set -euo pipefail
here=$(cd "$(dirname "$0")" && pwd)
name=${1:?usage: nas-trial-bundle.sh NAME [PACKAGE...]}
shift
runtime=${HERMES_SPIKE_DIR:-$HOME/hermes-spike}/out/runtime.tar.gz
image=${DEB_IMAGE:-anas-systemd:trixie}
[[ -f "$runtime" ]] || { echo "no runtime at $runtime: run ai/assistant-spike/run.sh build" >&2; exit 2; }
work=$(mktemp -d)
trap 'rm -rf -- "$work"' EXIT
out=$work/$name
mkdir -p "$out/debs"
cp -r "$here" "$out/spike"
rm -rf "$out/spike/__pycache__"
cp "$runtime" "$out/runtime.tar.gz"
docker run --rm -v "$out/debs:/out" -e PACKAGES="socat ripgrep $*" -e OWNER="$(id -u):$(id -g)" "$image" bash -c '
  set -euo pipefail
  cd /out
  apt-get update -qq
  # shellcheck disable=SC2086 # one word per package
  apt-get download -qq $PACKAGES
  chown "$OWNER" ./*.deb'
(cd "$out" && find . -type f -print0 | sort -z | xargs -0 sha256sum) > "$work/SHA256SUMS"
mv "$work/SHA256SUMS" "$out/SHA256SUMS"
tar -C "$work" -czf "$name.tar.gz" "$name"
for deb in "$out"/debs/*.deb; do
  printf '%s depends on: %s\n' "$(basename "$deb")" "$(dpkg-deb -f "$deb" Depends)"
done
sha256sum "$name.tar.gz"
