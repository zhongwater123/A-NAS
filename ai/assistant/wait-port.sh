#!/bin/bash
# Runs after an assistant unit's main process has started (ExecStartPost): the
# unit counts as started once PORT accepts connections.
#
# Under PrivatePIDs= each of the unit's processes, this one too, is process 1
# of a PID namespace of its own, which gets only the signals it handles:
# without the trap, stopping a unit that was still starting waited out
# TimeoutStopSec and then killed it. From there it cannot see the main process
# either, so a main process that exits early fails the start only when this
# gives up.
#
# Usage: wait-port.sh PORT
trap 'exit 143' TERM
for _ in $(seq 1 600); do
  (exec 3<>/dev/tcp/127.0.0.1/"$1") 2>/dev/null && exit 0
  sleep 0.2
done
echo "port $1 never opened" >&2
exit 1
