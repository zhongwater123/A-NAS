#!/usr/bin/env bash
# Builds the AI Worker's Python runtime on the development machine (ADR 0016):
# a virtual environment for Debian 13's Python 3, installed from the wheels
# ai/requirements.lock pins by hash and packed as a tarball. The root installer
# unpacks it into /opt/a-nas/ai-runtimes/<id>, so the NAS never runs pip or
# reaches the network. The ID derives from the lock and the Python version; an
# existing tarball for that ID is reused, so its SHA-256 stays the one the NAS
# has seen.
#
# Usage: scripts/build-ai-runtime.sh [IMAGE]
#        scripts/build-ai-runtime.sh --lock [IMAGE]
# IMAGE is a Debian 13 image with python3-venv, by default anas-ai-dev:trixie
# (ai/dev.Dockerfile). The first form writes build/ai-runtime/<id>.tar.gz and
# build/ai-runtime.json; --lock rewrites the lock's hashes for the versions it
# pins. Both download wheels from PyPI.
set -euo pipefail

mode=build
if [[ "${1:-}" == --lock ]]; then
  mode=lock
  shift
fi
image=${1:-anas-ai-dev:trixie}
repo=$(cd "$(dirname "$0")/.." && pwd)
lock=$repo/ai/requirements.lock

if [[ "$mode" == lock ]]; then
  docker run --rm -v "$repo/ai:/ai" "$image" bash -c '
    set -euo pipefail
    python3 -m venv /tmp/venv
    grep -Eo "^[A-Za-z0-9_.-]+==[^ ]+" /ai/requirements.lock > /tmp/pins
    /tmp/venv/bin/pip download --quiet --no-deps --only-binary=:all: -d /tmp/wheels -r /tmp/pins
    /tmp/venv/bin/python - /ai/requirements.lock /tmp/wheels <<"PY"
import hashlib, pathlib, re, sys
lock, wheels = pathlib.Path(sys.argv[1]), pathlib.Path(sys.argv[2])
def key(name):
    return re.sub(r"[-_.]+", "-", name).lower()
digests = {}
for wheel in wheels.glob("*.whl"):
    name, version = wheel.name.split("-")[:2]
    digests[(key(name), version)] = hashlib.sha256(wheel.read_bytes()).hexdigest()
lines = []
for line in lock.read_text().splitlines():
    match = re.match(r"([A-Za-z0-9_.-]+)==(\S+)", line)
    if match:
        line = f"{match[0]} --hash=sha256:{digests[(key(match[1]), match[2])]}"
    lines.append(line)
lock.write_text("\n".join(lines) + "\n")
PY'
  echo "updated $lock"
  exit
fi

python=$(docker run --rm "$image" python3 -c 'import sys; print("%d.%d" % sys.version_info[:2])')
id=$( { cat "$lock"; printf 'python=%s\n' "$python"; } | sha256sum | cut -c1-16)
out=$repo/build/ai-runtime
tarball=$out/$id.tar.gz
mkdir -p "$out"
if [[ ! -f "$tarball" ]]; then
  docker run --rm -v "$repo/ai:/ai:ro" -v "$out:/out" "$image" bash -c "
    set -euo pipefail
    runtime=/opt/a-nas/ai-runtimes/$id
    python3 -m venv \"\$runtime\"
    \"\$runtime/bin/pip\" install --quiet --no-deps --require-hashes --only-binary=:all: -r /ai/requirements.lock
    \"\$runtime/bin/pip\" uninstall --quiet --yes pip
    tar -C \"\$runtime\" --owner=0 --group=0 --numeric-owner -czf /out/$id.tar.gz.partial .
    chown $(id -u):$(id -g) /out/$id.tar.gz.partial"
  mv "$tarball.partial" "$tarball"
fi
read -r sha _ < <(sha256sum "$tarball")
size=$(stat -c %s "$tarball")
printf '{"id":"%s","python":"%s","file":"%s.tar.gz","sizeBytes":%s,"sha256":"%s"}\n' \
  "$id" "$python" "$id" "$size" "$sha" > "$repo/build/ai-runtime.json"
cat "$repo/build/ai-runtime.json"
