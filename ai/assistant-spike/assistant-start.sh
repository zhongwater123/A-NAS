#!/bin/bash
# Inside the sandbox: the only way out is the gateway's Unix socket, which the
# model client reaches as http://127.0.0.1:18001 through socat.
socat TCP-LISTEN:18001,bind=127.0.0.1,fork,reuseaddr UNIX-CONNECT:/run/anas-assistant-gw/model.sock &
mkdir -p "$HERMES_HOME"
hermes=/opt/a-nas/assistant-runtimes/spike/venv/bin/hermes
if [ "${ANAS_ASSISTANT_CHANNEL:-serve}" = api_server ]; then
  exec "$hermes" gateway run
fi
# The headless backend Hermes Desktop runs (REST /api/* and JSON-RPC on /api/ws),
# without HERMES_DESKTOP=1: that would also make it tick cron jobs itself.
exec "$hermes" serve --host 127.0.0.1 --port 9119
