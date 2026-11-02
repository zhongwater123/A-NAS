#!/usr/bin/env bash
# Installs the baseline plan into /env (docs/specs/video-edit-evaluation.md#方案目录):
# a venv on the image's Python 3.13 with the packages pinned by hash.
set -euo pipefail
python3 -m venv /env/venv
/env/venv/bin/pip install --no-cache-dir --no-deps --require-hashes --only-binary=:all: -r /plan/requirements.lock
