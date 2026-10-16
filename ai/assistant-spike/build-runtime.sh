#!/usr/bin/env bash
# Runs inside anas-ai-dev:trixie with network: builds the Hermes runtime the way
# ADR 0016 builds the AI runtime, plus a standalone CPython 3.14 because Hermes
# only installs its dependencies on 3.14. Hermes refuses wheel builds, so the
# pinned source lives inside the runtime and is installed editable.
set -euo pipefail
rt=/opt/a-nas/assistant-runtimes/spike
python3 -m venv /tmp/uvenv
/tmp/uvenv/bin/pip install --quiet uv
uv=/tmp/uvenv/bin/uv
$uv --version
export UV_PYTHON_INSTALL_DIR=$rt/python UV_PYTHON_PREFERENCE=only-managed UV_NO_CACHE=1
$uv python install --quiet 3.14.8
mkdir -p $rt/src
tar -C /src/hermes-agent --exclude=.git --exclude=website --exclude=tests --exclude=tests-js \
  --exclude=ui-tui --exclude=web --exclude=node_modules -cf - . | tar -C $rt/src -xf -
cd $rt/src
$uv export --quiet --frozen --no-dev --extra mcp --extra sms --no-emit-project --format requirements-txt -o /out/requirements.txt
grep -c '^[a-z0-9]' /out/requirements.txt | sed 's/^/pinned packages: /'
$uv venv --quiet --python 3.14 $rt/venv
VIRTUAL_ENV=$rt/venv $uv pip install --quiet --require-hashes -r /out/requirements.txt
VIRTUAL_ENV=$rt/venv $uv pip install --quiet --no-deps --editable $rt/src
$rt/venv/bin/python --version
$rt/venv/bin/hermes --version 2>&1 | head -5 || true
du -sh $rt/python $rt/venv $rt/src $rt | sed 's/^/size /'
tar -C /opt/a-nas/assistant-runtimes --owner=0 --group=0 --numeric-owner -czf /out/runtime.tar.gz spike
ls -l /out/runtime.tar.gz | awk '{print "tarball bytes", $5}'
sha256sum /out/runtime.tar.gz
chown -R "$HOST_UID:$HOST_GID" /out
