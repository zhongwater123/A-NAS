#!/usr/bin/env bash
# Builds the AI Worker's Python runtime on the development machine (ADR 0016):
# a virtual environment for Debian 13's Python 3, installed from the wheels
# ai/requirements.txt pins by hash and packed as a tarball. The root installer
# unpacks it into /opt/a-nas/ai-runtimes/<id>, so the NAS never runs pip or
# reaches the network. The ID derives from the lock and the Python version; an
# existing tarball for that ID is reused, so its SHA-256 stays the one the NAS
# has seen.
#
# Usage: scripts/build-ai-runtime.sh [IMAGE]
#        scripts/build-ai-runtime.sh --lock [UV-OPTION...]
# IMAGE is a Debian 13 image with python3-venv, by default anas-ai-dev:trixie
# (ai/dev.Dockerfile). The first form writes build/ai-runtime/<id>.tar.gz and
# build/ai-runtime.json. --lock resolves ai/requirements.in and
# ai/requirements-dev.in into their .txt locks; options such as
# `--upgrade-package pillow` go to `uv pip compile`. Both need uv, the version
# ai/requirements-dev.txt pins, and download from PyPI; the first form also
# needs Docker and jq.
set -euo pipefail

repo=$(cd "$(dirname "$0")/.." && pwd)
# shellcheck source=lib/python-runtime.sh
. "$repo/scripts/lib/python-runtime.sh"
require_uv "$repo"

if [[ "${1:-}" == --lock ]]; then
  shift
  cd "$repo/ai"
  # The runtime for the NAS alone; the development tools for every platform.
  lock_runtime requirements.in requirements.txt --excludes requirements.excludes "$@"
  uv pip compile requirements-dev.in --quiet --universal --generate-hashes --python-version 3.13 \
    --only-binary :all: --output-file requirements-dev.txt "$@"
  echo "updated ai/requirements.txt and ai/requirements-dev.txt"
  exit
fi

image=${1:-anas-ai-dev:trixie}
lock=$repo/ai/requirements.txt
# pip check knows nothing of requirements.excludes, so it reports the
# dependencies left out there as missing; it may report nothing else.
excluded=$(sed -e 's/#.*//' -e '/^[[:space:]]*$/d' "$repo/ai/requirements.excludes" | paste -sd '|')
python=$(image_python "$image")
id=$( { cat "$lock"; printf 'python=%s\n' "$python"; } | sha256sum | cut -c1-16)
out=$repo/build/ai-runtime
tarball=$out/$id.tar.gz
mkdir -p "$out"
if [[ ! -f "$tarball" ]]; then
  # uv resolves the lock again with the same excludes, so a dependency missing
  # from it fails the build, and --require-hashes refuses any wheel it does not
  # pin. pip compiles bytecode as it installs and uv does not, so ask for it:
  # the Worker cannot write to its runtime.
  docker run --rm -v "$repo/ai:/ai:ro" -v "$out:/out" -v "$(command -v uv):/usr/local/bin/uv:ro" \
    -e UV_NO_CACHE=1 -e EXCLUDED="$excluded" "$image" bash -c "
    set -euo pipefail
    runtime=/opt/a-nas/ai-runtimes/$id
    python3 -m venv \"\$runtime\"
    uv pip install --quiet --python \"\$runtime/bin/python\" --require-hashes --only-binary :all: \
      --compile-bytecode --excludes /ai/requirements.excludes -r /ai/requirements.txt
    problems=\$(\"\$runtime/bin/python\" -m pip check | grep -Ev \"requires (\$EXCLUDED), which is not installed\\.\$\" || true)
    if [[ \"\$problems\" != 'No broken requirements found.' && -n \"\$problems\" ]]; then
      echo \"pip check: \$problems\" >&2
      exit 1
    fi
    \"\$runtime/bin/pip\" uninstall --quiet --yes pip
    tar -C \"\$runtime\" --owner=0 --group=0 --numeric-owner -czf /out/$id.tar.gz.partial .
    chown $(id -u):$(id -g) /out/$id.tar.gz.partial"
  mv "$tarball.partial" "$tarball"
fi
write_runtime_manifest "$tarball" "$id" "$python" "$repo/build/ai-runtime.json"
