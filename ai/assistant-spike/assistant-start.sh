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
# `hermes serve`, the headless backend Hermes Desktop runs (REST /api/* and JSON-RPC on
# /api/ws), without HERMES_DESKTOP=1: that would also make it tick cron jobs itself. It uses
# Hermes's own login (dashboard.basic_auth in the member's root-written config.yaml: the
# account and the scrypt hash of the instance's root-only secret, assistant-layout.sh), and
# binds every address of the sandbox's private network, which has only loopback, so Hermes's
# login gate engages: a connection that logged in carries the member's identity, which
# Hermes's browser control requires, and the legacy session token is refused. Sessions are
# signed with a key serve draws at start and keeps in memory; serve_nondumpable.py keeps that
# memory out of reach of the member's own processes in the sandbox.
exec "$runtime/python" -P /spike/serve_nondumpable.py --host 0.0.0.0 --port 9119
