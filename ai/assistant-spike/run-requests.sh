#!/bin/bash
# The tasks a member asked to run now ("立即运行"), handed to a round of their scheduled tasks.
# Root's part, outside the sandbox (anas-assistant-cron@.service runs it with "+"):
#
#   run-requests.sh take ACCOUNT   before the round: the requests waiting become the round's
#   run-requests.sh done ACCOUNT   after it: the round's requests are forgotten
#
# The File Broker notes a request as an empty file named after the job in
# /run/a-nas/assistant-runs/ACCOUNT, in the Host Agent's own runtime directory, once it found
# the member's live session behind it (internal/filebroker/rootbroker/assistant_run.go). A
# round takes what waits there as /run/anas-assistant-run-now/ACCOUNT, which the member may
# list and nobody but root may change; cron_round.py queues those jobs with Hermes's own
# trigger_job before it hands the due jobs over. A request that comes while a round runs
# waits for the next one, which the waker starts (cron-waker.py).
set -euo pipefail
waiting=/run/a-nas/assistant-runs
taken=/run/anas-assistant-run-now
account=${2:-}
[[ "$account" =~ ^[a-z_][a-z0-9_-]{0,31}$ ]] || { echo "run-requests: bad account" >&2; exit 2; }
case ${1:-} in
  take)
    rm -rf -- "${taken:?}/$account"
    [[ -d "$waiting/$account" && ! -L "$waiting/$account" ]] || exit 0
    install -d -m 0711 "$taken"
    mv -- "$waiting/$account" "$taken/$account"
    chgrp -- "$account" "$taken/$account"
    chmod 0750 "$taken/$account"
    ;;
  done)
    rm -rf -- "${taken:?}/$account"
    ;;
  *)
    echo "usage: run-requests.sh take|done ACCOUNT" >&2
    exit 2
    ;;
esac
