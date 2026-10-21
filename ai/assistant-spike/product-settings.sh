#!/usr/bin/env bash
# What the AI assistant (ADR 0024) needs switched on in A-NAS's own services, for the trial
# (nas-trial.sh) and the system test. The assistant is not shipped yet, so each of these is
# off by default; they go in as systemd drop-ins, because a deploy rewrites /etc/a-nas/*.env
# and would drop lines added there.
#
#   product-settings.sh on     the session gate (decision 10) in the Host Agent and the Product
#                              Service, and the photo service's socket for members' assistants
#                              (decision 6, #113) with the Host Agent's lookup of members by
#                              UID behind it; 语音处理's translations and summaries through the
#                              same model gateway (ADR 0027), with the model TRIAL_MODEL names
#                              (default deepseek-flash); restarts the Host Agent, the photo
#                              service and the Product Service
#   product-settings.sh off    undo, and restart them
#   product-settings.sh state  one line saying what is on
#
# Runs as root.
set -euo pipefail
here=$(cd "$(dirname "$0")" && pwd)
gate=ANAS_ASSISTANT_SOCKET_DIR=/run/a-nas-assistant
photos=/run/a-nas-photos-assistant/assistant.sock
units=/etc/systemd/system
dropin=assistant-trial.conf
# 语音处理 translates and summarises with the assistant's model, through the gateway that holds
# its key (ADR 0027).
gateway=/run/anas-assistant-gw/model.sock
model=${TRIAL_MODEL:-deepseek-flash}
[[ $model =~ ^[A-Za-z0-9._:-]+$ ]] || { echo "TRIAL_MODEL is not a model name" >&2; exit 2; }
services=(anas-host-agent anas-api anas-photos)

restart() {
  systemctl restart anas-host-agent
  if [[ $1 == on ]]; then
    for _ in $(seq 1 100); do [[ -S /run/a-nas/assistant-changes.sock ]] && break; sleep 0.2; done
  fi
  # The photo service looks members up in the Host Agent, and the Product Service reaches both.
  # systemd starts a socket only while its service is not running.
  systemctl stop anas-photos
  if [[ $1 == on ]]; then systemctl start anas-photos-assistant.socket; fi
  systemctl start anas-photos
  systemctl restart anas-api
  for _ in $(seq 1 100); do curl -fsS http://127.0.0.1:8080/healthz >/dev/null 2>&1 && break; sleep 0.2; done
}

# Trials before 2026-10-19 put the gate into the env files.
old_lines() {
  local file
  for file in /etc/a-nas/host-agent.env /etc/a-nas/anas-api.env; do
    if [[ -f $file ]]; then sed -i "\#^$gate\$#d" "$file"; fi
  done
}

on() {
  local service
  for service in "${services[@]}"; do install -d -m 0755 "$units/$service.service.d"; done
  printf '[Service]\nEnvironment=%s\n# Members by UID, for the photo service'"'"'s socket for assistants.\nEnvironment=ANAS_PHOTO_ASSISTANT_LOOKUP=1\n' \
    "$gate" > "$units/anas-host-agent.service.d/$dropin"
  printf '[Service]\nEnvironment=%s\n# 语音处理: the model gateway and the model.\nEnvironment=ANAS_TEXT_MODEL_SOCKET=%s\nEnvironment=ANAS_TEXT_MODEL=%s\n' \
    "$gate" "$gateway" "$model" > "$units/anas-api.service.d/$dropin"
  printf '[Unit]\nWants=anas-photos-assistant.socket\nAfter=anas-photos-assistant.socket\n\n[Service]\nSockets=anas-photos-assistant.socket\nEnvironment=ANAS_PHOTOS_ASSISTANT_SOCKET=%s\n' \
    "$photos" > "$units/anas-photos.service.d/$dropin"
  install -m 0644 "$here/anas-photos-assistant.socket" "$units/anas-photos-assistant.socket"
  for service in "${services[@]}"; do chmod 0644 "$units/$service.service.d/$dropin"; done
  old_lines
  systemctl daemon-reload
  systemctl enable --quiet anas-photos-assistant.socket
  restart on
}

off() {
  local service
  for service in "${services[@]}"; do
    rm -f "$units/$service.service.d/$dropin"
    rmdir "$units/$service.service.d" 2>/dev/null || true
  done
  systemctl disable --now --quiet anas-photos-assistant.socket 2>/dev/null || true
  rm -f "$units/anas-photos-assistant.socket"
  old_lines
  systemctl daemon-reload
  restart off
}

state() {
  local gate_on=off photos_on=off
  if [[ -f "$units/anas-host-agent.service.d/$dropin" && -f "$units/anas-api.service.d/$dropin" ]] ||
    grep -qx "$gate" /etc/a-nas/host-agent.env 2>/dev/null; then gate_on=on; fi
  if systemctl is-active --quiet anas-photos-assistant.socket && [[ -f "$units/anas-photos.service.d/$dropin" ]]; then photos_on=on; fi
  printf 'session gate %s, photos for assistants %s\n' "$gate_on" "$photos_on"
}

case ${1:-} in
  on) on ;;
  off) off ;;
  state) state ;;
  *) sed -n '2,15p' "$0"; exit 2 ;;
esac
