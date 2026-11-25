#!/usr/bin/env bash
# The desktop tools in the assistant's sandbox (ADR 0024 decision 8), with the fake model:
# what the model is offered, and what a step answers with no VNAS window behind it
# (desktop_control.py).
set -uo pipefail
user=${1:-alice}
py=/opt/a-nas/assistant-runtimes/spike/venv/bin/python
exec "$py" /spike/desktop_control.py "$user"
