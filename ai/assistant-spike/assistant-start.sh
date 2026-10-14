#!/bin/bash
# Inside the sandbox: the only way out is the gateway's Unix socket, which the
# model client reaches as http://127.0.0.1:18001 through socat.
socat TCP-LISTEN:18001,bind=127.0.0.1,fork,reuseaddr UNIX-CONNECT:/run/anas-assistant-gw/model.sock &
mkdir -p "$HERMES_HOME"
exec /opt/a-nas/assistant-runtimes/spike/venv/bin/hermes gateway run
