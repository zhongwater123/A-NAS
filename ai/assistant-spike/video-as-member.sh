#!/usr/bin/env bash
# Runs video_client.py as a member, inside that member's running assistant's mount namespace,
# as mcp-as-member.sh runs anas_mcp.py: one request to the video moments service, with no MCP
# server and no model's tools between (eval.video_plan, #350).
#
#   ai/assistant-spike/video-as-member.sh MEMBER PATH < body.json > answer
#
# HERMES_SPIKE_NAME names the container and DOCKER_CONTEXT the Docker host, as there.
set -euo pipefail
member=$1 path=$2
exec docker exec -i "${HERMES_SPIKE_NAME:-hermes-spike-sbx}" bash -c '
  member=$1 path=$2 py=/opt/a-nas/assistant-runtimes/spike/venv/bin/python
  "$py" /spike/serve_client.py "$member" ready >/dev/null
  setsid "$py" /spike/serve_client.py "$member" chat "SLEEP 600" >/dev/null 2>&1 </dev/null &
  pid=$(pgrep -u "$member" -f serve_nondumpable | head -1)
  exec nsenter -t "$pid" -m -- setpriv --reuid "$member" --regid "$member" --groups 20000 -- "$py" /spike/video_client.py "$path"
' bash "$member" "$path"
