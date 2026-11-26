#!/bin/bash
# Runs after an assistant unit's main process has started (ExecStartPost): the
# unit counts as started once PORT accepts connections. Under PrivatePIDs= it
# runs under tini, as every command of the unit does.
#
# Usage: wait-port.sh PORT
for _ in $(seq 1 600); do
  (exec 3<>/dev/tcp/127.0.0.1/"$1") 2>/dev/null && exit 0
  sleep 0.2
done
echo "port $1 never opened" >&2
exit 1
