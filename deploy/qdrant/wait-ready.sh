#!/usr/bin/env bash
# Runs after a vector index instance's Qdrant has started
# (anas-vectors@.service), in its network namespace: the unit counts as
# started, and its proxy may forward, only once the gRPC port accepts
# connections; Qdrant does not tell systemd itself. A start that gets there
# is no longer counted as failed (start.sh).
#
# systemd waits for this script even after Qdrant has exited, as when it
# panics loading a damaged segment, so the start fails once the main process
# ($MAINPID) is gone; this used to take the full 60 seconds. Not at once: the
# connection that started the instance waits in the socket's queue and starts
# it again as soon as the start fails. Failing at once, that was some twenty
# starts in a second, which hit the instance's start limit and then the
# socket's trigger limit, and the socket failed for good (2026-11-25). Five
# seconds keep it to a start every few seconds, under both.
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
  if [[ -n "${MAINPID:-}" ]] && ! kill -0 "$MAINPID" 2>/dev/null; then
    echo "Qdrant exited before it accepted connections on port $1" >&2
    sleep 5
    exit 1
  fi
  sleep 0.1
done
echo "Qdrant did not accept connections on port $1 within 60 seconds" >&2
exit 1
