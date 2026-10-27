#!/usr/bin/env bash
# Runs after a vector index instance's Qdrant has started
# (anas-vectors@.service), in its network namespace: the unit counts as
# started, and its proxy may forward, only once the gRPC port accepts
# connections; Qdrant does not tell systemd itself. A start that gets there
# is no longer counted as failed (start.sh).
#
# Usage: wait-ready.sh PORT STATE_DIRECTORY
set -euo pipefail

if [[ $# -ne 2 || ! "$1" =~ ^[0-9]+$ ]]; then
  echo "usage: $0 PORT STATE_DIRECTORY" >&2
  exit 2
fi
for _ in $(seq 1 600); do
  if (exec 3<>"/dev/tcp/127.0.0.1/$1") 2>/dev/null; then
    rm -f -- "$2/failed-starts"
    exit 0
  fi
  sleep 0.1
done
echo "Qdrant did not accept connections on port $1 within 60 seconds" >&2
exit 1
