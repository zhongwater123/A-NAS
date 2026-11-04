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
# ANAS_SPIKE_CARD_PICTURES=1 and ANAS_SPIKE_CARD_SCREEN=1 pass through: video cards come with
# their frames, or screened by the vision model; ANAS_SPIKE_RETRIEVED_ONLY=1 and
# ANAS_SPIKE_SPAN=probe|frame too: a plan of retrieved cards only, its seconds set by the service;
# ANAS_SPIKE_LOCATE=probe|frame: the seconds set from each segment's shows, no retrieval needed;
# ANAS_SPIKE_FILL=1 too: to add up to the seconds the plan asks for; ANAS_SPIKE_EDGES=1: segments
# trimmed to a cut near either end; ANAS_SPIKE_NO_LOOK=1: no review by the vision model;
# ANAS_SPIKE_FLOW=1: a catalog without seconds and a plan kept to one scene at a time (anas_mcp.py).
set -euo pipefail
member=${1:-alice}
exec docker exec -i -e ANAS_SPIKE_CARD_PICTURES -e ANAS_SPIKE_CARD_SCREEN -e ANAS_SPIKE_RETRIEVED_ONLY -e ANAS_SPIKE_SPAN -e ANAS_SPIKE_LOCATE -e ANAS_SPIKE_FILL -e ANAS_SPIKE_EDGES -e ANAS_SPIKE_NO_LOOK -e ANAS_SPIKE_FLOW "${HERMES_SPIKE_NAME:-hermes-spike-sbx}" bash -c '
  member=$1 py=/opt/a-nas/assistant-runtimes/spike/venv/bin/python
  "$py" /spike/serve_client.py "$member" ready >/dev/null
  setsid "$py" /spike/serve_client.py "$member" chat "SLEEP 600" >/dev/null 2>&1 </dev/null &
  pid=$(pgrep -u "$member" -f serve_nondumpable | head -1)
  exec nsenter -t "$pid" -m -- setpriv --reuid "$member" --regid "$member" --groups 20000 -- "$py" /spike/anas_mcp.py
' bash "$member"
