#!/usr/bin/env bash
# Runs the assistant's MCP server (anas_mcp.py) as a member, inside that member's running
# assistant's mount namespace, so an MCP client on the development machine (Claude Code)
# drives the same tools Hermes does: the photo and video sockets as the sandbox binds
# them, the member known by UID. Stdio passes through docker exec.
#
#   ai/assistant-spike/mcp-as-member.sh [MEMBER]     default alice
#
# HERMES_SPIKE_NAME names the container (run.sh's default otherwise) and DOCKER_CONTEXT
# the Docker host, colima-anas on the Mac. A long scripted turn keeps the assistant, and
# with it the namespace, up meanwhile, as photos-socket.sh does; it stops when idle.
set -euo pipefail
member=${1:-alice}
exec docker exec -i "${HERMES_SPIKE_NAME:-hermes-spike-sbx}" bash -c '
  member=$1 py=/opt/a-nas/assistant-runtimes/spike/venv/bin/python
  "$py" /spike/serve_client.py "$member" ready >/dev/null
  setsid "$py" /spike/serve_client.py "$member" chat "SLEEP 600" >/dev/null 2>&1 </dev/null &
  pid=$(pgrep -u "$member" -f serve_nondumpable | head -1)
  exec nsenter -t "$pid" -m -- setpriv --reuid "$member" --regid "$member" --groups 20000 -- "$py" /spike/anas_mcp.py
' bash "$member"
