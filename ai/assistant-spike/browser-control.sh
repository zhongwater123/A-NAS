#!/usr/bin/env bash
# Hermes's browser control in the assistant's sandbox (user decision, 2026-10-20), with the
# fake model: the VNAS window, logged in with Hermes's own login, registers as the browser
# of its session and answers the browser commands of a turn (browser_control.py).
set -uo pipefail
user=${1:-alice}
py=/opt/a-nas/assistant-runtimes/spike/venv/bin/python
exec "$py" /spike/browser_control.py "$user"
