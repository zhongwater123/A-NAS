# shellcheck shell=bash
# Helpers for the scripts that build a Python runtime for the NAS on the
# development machine: scripts/build-ai-runtime.sh (ADR 0016) and
# scripts/build-search-runtime.sh (ADR 0030). Each runtime is a virtual
# environment for Debian 13's Python 3, installed from wheels a lock pins by
# hash and packed as a tarball named by its content; the root installer checks
# it against the manifest written here and unpacks it under /opt/a-nas.
# Source this file; it defines functions only.

# require_uv REPO: stop unless uv is the version ai/requirements-dev.txt pins.
require_uv() {
  local want have
  want=$(sed -n 's/^uv==\([0-9.]*\).*/\1/p' "$1/ai/requirements-dev.txt")
  have=$(uv --version 2>/dev/null | cut -d ' ' -f 2 || true)
  if [[ "$have" != "$want" ]]; then
    echo "ai/requirements-dev.txt pins uv $want; found: ${have:-none}" >&2
    exit 2
  fi
}

# lock_runtime IN OUT [UV-OPTION...]: resolve IN into the hash lock OUT for the
# NAS alone. Debian 13 has glibc 2.41, and manylinux_2_40 is the newest tag uv
# names. Run it from the directory of IN, so the lock's header names relative
# paths. The header repeats the command, and a runtime's ID hashes the lock,
# so the options keep the order the AI Worker's lock was first written with.
lock_runtime() {
  local in=$1 out=$2
  shift 2
  uv pip compile "$in" --quiet --generate-hashes --python-version 3.13 \
    --python-platform x86_64-manylinux_2_40 --only-binary :all: "$@" --output-file "$out"
}

# image_python IMAGE: the MAJOR.MINOR version of IMAGE's python3, which the
# installer requires the NAS to have as well.
image_python() {
  docker run --rm "$1" python3 -c 'import sys; print("%d.%d" % sys.version_info[:2])'
}

# write_runtime_manifest TARBALL ID PYTHON MANIFEST: the manifest the installer
# checks the tarball against, printed as well.
write_runtime_manifest() {
  local tarball=$1 id=$2 python=$3 manifest=$4 sha
  read -r sha _ < <(sha256sum "$tarball")
  jq -cn --arg id "$id" --arg python "$python" --arg file "$(basename "$tarball")" \
    --argjson sizeBytes "$(stat -c %s "$tarball")" --arg sha256 "$sha" \
    '{$id, $python, $file, $sizeBytes, $sha256}' > "$manifest"
  cat "$manifest"
}
