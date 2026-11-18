#!/bin/bash
for _ in $(seq 1 600); do
  (exec 3<>/dev/tcp/127.0.0.1/"$1") 2>/dev/null && exit 0
  sleep 0.2
done
echo "port $1 never opened" >&2
exit 1
