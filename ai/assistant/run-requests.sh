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
#
# The waker's planning token for the montage center (/run/anas-assistant-montage-plans/ACCOUNT,
# cron-waker.py) is taken the same way, as montage-plan in the round's directory: only a round
# the waker started for planning claims a round of planning (montage_round.py).
set -euo pipefail
waiting=/run/a-nas/assistant-runs
taken=/run/anas-assistant-run-now
plans=/run/anas-assistant-montage-plans
account=${2:-}
[[ "$account" =~ ^[a-z_][a-z0-9_-]{0,31}$ ]] || { echo "run-requests: bad account" >&2; exit 2; }
case ${1:-} in
  take)
    rm -rf -- "${taken:?}/$account"
    install -d -m 0711 "$taken"
    if [[ -d "$waiting/$account" && ! -L "$waiting/$account" ]]; then
      mv -- "$waiting/$account" "$taken/$account"
    fi
    if [[ -f "$plans/$account" && ! -L "$plans/$account" ]]; then
      install -d -m 0750 "$taken/$account"
      mv -- "$plans/$account" "$taken/$account/montage-plan"
    fi
    [[ -d "$taken/$account" ]] || exit 0
    chgrp -R -- "$account" "$taken/$account"
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
