#!/bin/bash
# 剪法25: one take each of 上海 (02) and 侦探片 (01) with the 4B served at $1 (default 127.0.0.1:8094).
# A take's pick/shots/cut files are reused when they exist: move them away to cut again.
set -u
cd "$(dirname "$0")"
url=${1:-http://127.0.0.1:8094}
for case in 02 01; do
  CUT_TAG="-1" CUT_SEED="1" python3 cut.py "$url" "$case" || echo "失败 $case"
done
python3 page.py
