#!/usr/bin/env bash
# Builds the web search service's runtime on the development machine
# (ADR 0030), as scripts/build-ai-runtime.sh builds the AI Worker's: a virtual
# environment for Debian 13's Python 3 with the wheels
# deploy/search/requirements.txt pins by hash, and SearXNG itself from the
# commit deploy/search/searxng.json pins. SearXNG publishes no package, so its
# searx package goes under lib/searxng, on the path the service's unit sets.
# The runtime also carries SearXNG's source archive and license and a list of
# every installed package with its license, which the release offers
# (AGPL-3.0). The ID derives from the lock, the Python version and the
# archive; an existing tarball for that ID is reused.
#
# Usage: scripts/build-search-runtime.sh [IMAGE]
#        scripts/build-search-runtime.sh --lock [UV-OPTION...]
# IMAGE is a Debian 13 image with python3, by default anas-systemd:trixie
# (scripts/build-system-test-image.sh). The first form writes
# build/search-runtime/<id>.tar.gz and build/search-runtime.json. --lock takes
# SearXNG's own requirements at the pinned commit into
# deploy/search/requirements.in and resolves deploy/search/requirements.txt.
# Both download the archive once into build/search-source/ and check it; both
# need uv, the version ai/requirements-dev.txt pins, curl and jq, and the
# first form Docker.
set -euo pipefail

repo=$(cd "$(dirname "$0")/.." && pwd)
# shellcheck source=lib/python-runtime.sh
. "$repo/scripts/lib/python-runtime.sh"
require_uv "$repo"

search=$repo/deploy/search
pin=$search/searxng.json
field() { jq -er --arg key "$1" '.[$key]' "$pin"; }
commit=$(field commit)
version=$(field version)
source_url=$(field source)
source_size=$(field sizeBytes)
source_sha=$(field sha256)
if [[ ! "$commit" =~ ^[0-9a-f]{40}$ || ! "$source_sha" =~ ^[0-9a-f]{64}$ || ! "$source_size" =~ ^[0-9]+$ ||
  "$source_url" != https://* || ! "$version" =~ ^[0-9]{4}\.[0-9]{1,2}\.[0-9]{1,2}\+[0-9a-f]{7,40}$ ]]; then
  echo "invalid SearXNG manifest: $pin" >&2
  exit 2
fi
# pinned FILE: FILE is the archive the manifest pins.
pinned() {
  [[ -f "$1" && "$(stat -c %s "$1")" == "$source_size" ]] &&
    printf '%s  %s\n' "$source_sha" "$1" | sha256sum --check --quiet - >/dev/null 2>&1
}
archive=$repo/build/search-source/$source_sha/searxng.tar.gz
if ! pinned "$archive"; then
  mkdir -p "$(dirname "$archive")"
  curl --fail --location --silent --show-error --proto '=https' --retry 3 --output "$archive.partial" "$source_url"
  if ! pinned "$archive.partial"; then
    echo "the downloaded SearXNG archive does not match its manifest: $source_url" >&2
    exit 4
  fi
  mv -f -- "$archive.partial" "$archive"
fi
top=searxng-$commit

if [[ "${1:-}" == --lock ]]; then
  shift
  {
    echo "# SearXNG's requirements.txt and requirements-server.txt at the commit"
    echo "# searxng.json pins, written by scripts/build-search-runtime.sh --lock."
    tar -xzOf "$archive" "$top/requirements.txt" "$top/requirements-server.txt" | grep -Ev '^[[:space:]]*(#|-r|$)'
  } > "$search/requirements.in"
  cd "$search"
  lock_runtime requirements.in requirements.txt "$@"
  echo "updated deploy/search/requirements.in and deploy/search/requirements.txt"
  exit
fi

image=${1:-anas-systemd:trixie}
lock=$search/requirements.txt
python=$(image_python "$image")
id=$( { cat "$lock"; printf 'python=%s\nsearxng=%s\n' "$python" "$source_sha"; } | sha256sum | cut -c1-16)
out=$repo/build/search-runtime
tarball=$out/$id.tar.gz
mkdir -p "$out"
if [[ ! -f "$tarball" ]]; then
  # Without a Git checkout SearXNG reads its version from this module.
  frozen=$(printf '%s = "%s"\n' VERSION_STRING "$version" VERSION_TAG "$version" DOCKER_TAG "${version/+/-}" \
    GIT_URL https://github.com/searxng/searxng GIT_BRANCH master)
  # uv installs no pip, and the service cannot write to its runtime, so
  # bytecode is compiled here, for SearXNG as well.
  docker run --rm -v "$search:/search:ro" -v "$archive:/source.tar.gz:ro" -v "$out:/out" \
    -v "$(command -v uv):/usr/local/bin/uv:ro" -e UV_NO_CACHE=1 \
    -e TOP="$top" -e COMMIT="$commit" -e FROZEN="$frozen" "$image" bash -c "
    set -euo pipefail
    runtime=/opt/a-nas/search-runtimes/$id
    python3 -m venv --without-pip \"\$runtime\"
    uv pip install --quiet --python \"\$runtime/bin/python\" --require-hashes --only-binary :all: \
      --compile-bytecode -r /search/requirements.txt
    uv pip check --quiet --python \"\$runtime/bin/python\"
    install -d \"\$runtime/lib/searxng\" \"\$runtime/share/searxng\"
    tar -C \"\$runtime/lib/searxng\" --strip-components=1 --no-same-owner -xzf /source.tar.gz \"\$TOP/searx\"
    printf '%s\n' \"\$FROZEN\" > \"\$runtime/lib/searxng/searx/version_frozen.py\"
    \"\$runtime/bin/python\" -m compileall -q \"\$runtime/lib/searxng\"
    install -m 0644 /source.tar.gz \"\$runtime/share/searxng/searxng-\$COMMIT.tar.gz\"
    tar -xzOf /source.tar.gz \"\$TOP/LICENSE\" > \"\$runtime/share/searxng/LICENSE\"
    \"\$runtime/bin/python\" - > \"\$runtime/share/packages.json\" <<'PY'
import json
from importlib.metadata import distributions

def license_of(meta):
    if meta.get('License-Expression'):
        return meta['License-Expression']
    classifiers = [c.rsplit(' :: ', 1)[-1] for c in meta.get_all('Classifier') or [] if c.startswith('License ::')]
    if classifiers:
        return '; '.join(classifiers)
    text = (meta.get('License') or '').strip()
    return text.splitlines()[0] if text else ''

packages = [{'name': d.metadata['Name'], 'version': d.version, 'license': license_of(d.metadata)} for d in distributions()]
packages.sort(key=lambda p: p['name'].lower())
print(json.dumps(packages, ensure_ascii=False, indent=1))
PY
    tar -C \"\$runtime\" --owner=0 --group=0 --numeric-owner -czf /out/$id.tar.gz.partial .
    chown $(id -u):$(id -g) /out/$id.tar.gz.partial"
  mv "$tarball.partial" "$tarball"
fi
write_runtime_manifest "$tarball" "$id" "$python" "$repo/build/search-runtime.json"
