#!/usr/bin/env bash
# Builds the runtime of the montage center's music analysis Worker on a
# development machine (docs/architecture/montage-music.md), as
# scripts/build-ai-runtime.sh builds the AI Worker's: a virtual environment
# for Debian 13's Python 3 with the wheels deploy/videocut-music/requirements.txt
# pins by hash, packed as a tarball that a content package unpacks into
# /opt/a-nas/videocut-music-runtimes/<id> (scripts/build-content-packages.sh
# --videocut-music). NATTEN and madmom publish no wheels for that Python:
# deploy/videocut-music/sources.json pins their sources, which this script
# downloads, checks and compiles in the builder image. madmom's source is
# GitHub's archive of a commit, without the git submodule of model files
# (CC BY-NC-SA 4.0); an empty madmom/models/__init__.py takes its place, and
# the build fails if any such file is in the runtime. The ID derives from the
# lock, the sources and the Python version (scripts/lib/content.sh); an
# existing tarball for that ID is reused.
#
# Usage: scripts/build-videocut-music-runtime.sh [IMAGE]
#        scripts/build-videocut-music-runtime.sh --lock [UV-OPTION...]
# IMAGE is a Debian 13 image with python3-venv, by default anas-ai-dev:trixie
# (ai/dev.Dockerfile); the build installs the compilers into its container
# from Debian. The NAS is x86_64, so build where Docker runs x86_64: NATTEN is
# compiled for the builder's processor. ANAS_NATTEN_JOBS (2) is how many
# files compile at once; each takes some 2 GiB. The first form writes
# build/videocut-music-runtime/<id>.tar.gz and build/videocut-music-runtime.json,
# whose manifest also lists the SHA-256 of the NATTEN and madmom wheels the
# build compiled, kept beside the tarball in build/videocut-music-runtime/<id>.wheels/;
# --lock resolves deploy/videocut-music/requirements.txt. Both need uv, the
# version ai/requirements-dev.txt pins; the first also Docker, curl and jq.
set -euo pipefail

repo=$(cd "$(dirname "$0")/.." && pwd)
# shellcheck source=lib/python-runtime.sh
. "$repo/scripts/lib/python-runtime.sh"
require_uv "$repo"
music=$repo/deploy/videocut-music
# The lock's resolution, which its header repeats: PyTorch for the processor
# from PyTorch's index, and antlr4's runtime, pure Python, from its source.
lock_options=(--no-binary antlr4-python3-runtime --extra-index-url https://download.pytorch.org/whl/cpu
  --index-strategy unsafe-best-match)

if [[ "${1:-}" == --lock ]]; then
  shift
  cd "$music"
  lock_runtime requirements.in requirements.txt "${lock_options[@]}" "$@"
  echo "updated deploy/videocut-music/requirements.txt"
  exit
fi

image=${1:-anas-ai-dev:trixie}
python=$(image_python "$image")
# shellcheck source=lib/content.sh
. "$repo/scripts/lib/content.sh"
if [[ "$python" != "$TARGET_PYTHON" ]]; then
  echo "$image has Python $python; the runtime is for Debian 13's $TARGET_PYTHON (scripts/lib/content.sh)" >&2
  exit 2
fi

# The sources, each checked against sources.json.
sources=$repo/build/videocut-music-sources
mkdir -p "$sources"
while IFS=$'\t' read -r -u 3 url file size sha; do
  target=$sources/$file
  if ! { [[ -f "$target" && "$(stat -c %s "$target")" == "$size" ]] &&
    printf '%s  %s\n' "$sha" "$target" | sha256sum --check --quiet - >/dev/null 2>&1; }; then
    curl --fail --location --silent --show-error --proto '=https' --retry 3 --output "$target.partial" "$url"
    if [[ "$(stat -c %s "$target.partial")" != "$size" ]] ||
      ! printf '%s  %s\n' "$sha" "$target.partial" | sha256sum --check --quiet - >/dev/null 2>&1; then
      echo "the download does not match deploy/videocut-music/sources.json: $url" >&2
      exit 4
    fi
    mv -f -- "$target.partial" "$target"
  fi
done 3< <(jq -r '.packages[] | [.source, .file, .sizeBytes, .sha256] | @tsv' "$music/sources.json")
natten=$(jq -er '.packages[] | select(.name == "natten") | .file' "$music/sources.json")
madmom=$(jq -er '.packages[] | select(.name == "madmom") | .file' "$music/sources.json")

id=$(videocut_music_runtime_id "$repo")
out=$repo/build/videocut-music-runtime
tarball=$out/$id.tar.gz
mkdir -p "$out"
if [[ ! -f "$tarball" ]]; then
  # uv installs the lock again with --require-hashes, so a wheel it does not
  # pin fails the build. pip compiles bytecode as it installs and uv does not,
  # so ask for it: the Worker cannot write to its runtime.
  docker run --rm -v "$music:/music:ro" -v "$sources:/sources:ro" -v "$out:/out" \
    -v "$(command -v uv):/usr/local/bin/uv:ro" -e UV_NO_CACHE=1 -e NATTEN_N_WORKERS="${ANAS_NATTEN_JOBS:-2}" \
    -e NATTEN_CUDA_ARCH= "$image" bash -c "
    set -euo pipefail
    apt-get update -qq
    DEBIAN_FRONTEND=noninteractive apt-get install -y -qq --no-install-recommends build-essential cmake python3-dev >/dev/null
    runtime=/opt/a-nas/videocut-music-runtimes/$id
    python3 -m venv \"\$runtime\"
    uv pip install --quiet --python \"\$runtime/bin/python\" --require-hashes --only-binary :all: \
      ${lock_options[*]} --compile-bytecode -r /music/requirements.txt
    mkdir /tmp/madmom
    tar -C /tmp/madmom --strip-components=1 -xzf /sources/$madmom
    if [[ -n \"\$(ls -A /tmp/madmom/madmom/models)\" ]]; then
      echo 'madmom/models is not empty in the archive' >&2
      exit 1
    fi
    printf '%s\n' '# Left empty by scripts/build-videocut-music-runtime.sh: madmom'\"'\"'s model files' \
      '# (CC BY-NC-SA 4.0) are not part of the A-NAS runtime.' > /tmp/madmom/madmom/models/__init__.py
    rm -rf /out/wheels.partial && mkdir /out/wheels.partial
    \"\$runtime/bin/python\" -m pip wheel --quiet --no-deps --no-build-isolation --wheel-dir /out/wheels.partial \
      /sources/$natten /tmp/madmom
    uv pip install --quiet --python \"\$runtime/bin/python\" --no-deps --compile-bytecode /out/wheels.partial/*.whl
    problems=\$(\"\$runtime/bin/python\" -m pip check || true)
    if [[ \"\$problems\" != 'No broken requirements found.' ]]; then
      echo \"pip check: \$problems\" >&2
      exit 1
    fi
    \"\$runtime/bin/python\" -c 'import allin1.models, demucs.hf, madmom.features.downbeats, natten'
    left=\$(find \"\$runtime\" -path '*/madmom/models/*' -type f ! -name '__init__.py' ! -path '*/__pycache__/*')
    if [[ -n \"\$left\" ]]; then
      echo \"madmom's model files are in the runtime: \$left\" >&2
      exit 1
    fi
    \"\$runtime/bin/pip\" uninstall --quiet --yes pip
    tar -C \"\$runtime\" --owner=0 --group=0 --numeric-owner -czf /out/$id.tar.gz.partial .
    chown -R $(id -u):$(id -g) /out/$id.tar.gz.partial /out/wheels.partial"
  rm -rf -- "$out/$id.wheels"
  mv "$out/wheels.partial" "$out/$id.wheels"
  mv "$tarball.partial" "$tarball"
fi
write_runtime_manifest "$tarball" "$id" "$python" "$repo/build/videocut-music-runtime.json" >/dev/null
# The wheels this build compiled, by SHA-256: compiling again need not give
# the same bytes, so the runtime's manifest records the ones it holds.
wheels=$(cd "$out/$id.wheels" && sha256sum -- *.whl | jq -Rn '[inputs | split("  ") | {file: .[1], sha256: .[0]}]')
jq --argjson wheels "$wheels" '. + {builtWheels: $wheels}' "$repo/build/videocut-music-runtime.json" >"$repo/build/videocut-music-runtime.json.partial"
mv -f -- "$repo/build/videocut-music-runtime.json.partial" "$repo/build/videocut-music-runtime.json"
cat "$repo/build/videocut-music-runtime.json"
