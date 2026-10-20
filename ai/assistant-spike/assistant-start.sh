#!/bin/bash
# Inside the sandbox: the only way out is the gateway's Unix socket, which the
# model client reaches as http://127.0.0.1:18001 through socat.
socat TCP-LISTEN:18001,bind=127.0.0.1,fork,reuseaddr UNIX-CONNECT:/run/anas-assistant-gw/model.sock &
mkdir -p "$HERMES_HOME"
runtime=/opt/a-nas/assistant-runtimes/spike/venv/bin
if [ "${ANAS_ASSISTANT_CHANNEL:-serve}" = api_server ]; then
  exec "$runtime/hermes" gateway run
fi
# A round of the member's scheduled tasks (anas-assistant-cron@.service): Hermes's own
# `hermes cron tick` runs the jobs that are due once, behind the same bridge to the model.
if [ "${ANAS_ASSISTANT_CHANNEL:-serve}" = cron ]; then
  exec "$runtime/hermes" cron tick
fi
# The session token reaches `hermes serve` alone, the way Hermes Desktop hands it to a
# backend over SSH: in a 0600 file under $HOME/.hermes/desktop-ssh, which serve checks
# without following links and deletes once read (--ssh-session-token-file). No process
# has it in its environment (/proc/<pid>/environ), which the member's own processes in
# the sandbox can read. The unit stages it in a file only this instance reads and
# empties that once serve listens; serve_nondumpable.py keeps serve's memory out of reach.
token=$(cat /run/anas-assistant-start/token)
[[ $token =~ ^[0-9a-f]{64}$ ]] || { echo "no session token for hermes serve" >&2; exit 1; }
hex() { od -An -tx1 -N"$1" /dev/urandom | tr -d ' \n'; }
handoff=$HOME/.hermes/desktop-ssh
rm -rf -- "$handoff"
umask 077
dir=$handoff/$(hex 16)
mkdir -p "$dir"
file=$dir/$(hex 8).token
printf '%s' "$token" > "$file"
# The folder's default ACL widens new entries; serve takes only 0700 and 0600.
chmod 0700 "$dir" && chmod 0600 "$file"
unset token
umask 022
# The headless backend Hermes Desktop runs (REST /api/* and JSON-RPC on /api/ws),
# without HERMES_DESKTOP=1: that would also make it tick cron jobs itself.
exec "$runtime/python" -P /spike/serve_nondumpable.py --host 127.0.0.1 --port 9119 --ssh-session-token-file "$file"
