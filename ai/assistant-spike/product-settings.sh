#!/usr/bin/env bash
# What the AI assistant (ADR 0024) needs switched on in A-NAS's own services, for the trial
# (nas-trial.sh) and the system test. The assistant is not shipped yet, so each of these is
# off by default; they go in as systemd drop-ins, because a deploy rewrites /etc/a-nas/*.env
# and would drop lines added there.
#
#   product-settings.sh on     the session gate (decision 10) in the Host Agent and the Product
#                              Service, the photo service's socket for members' assistants
#                              (decision 6, #113) with the Host Agent's lookup of members by
#                              UID behind it, and the Product Service's socket for their
#                              companion tools (#412); restarts the Host Agent, the photo
#                              service and the Product Service
#   product-settings.sh off    undo, and restart them
#   product-settings.sh state  one line saying what is on
#
# The companion is on only when the running Product Service answers on its socket: one built
# before #412 leaves the socket systemd hands it unread, and every companion call would then
# wait out the companion server's 20 s. Deploy the Product Service before updating the trial.
#
# Runs as root.
set -euo pipefail
here=$(cd "$(dirname "$0")" && pwd)
gate=ANAS_ASSISTANT_SOCKET_DIR=/run/a-nas-assistant
photos=/run/a-nas-photos-assistant/assistant.sock
companion=anas-companion-assistant.socket
companion_socket=/run/a-nas-companion-assistant/assistant.sock
units=/etc/systemd/system
dropin=assistant-trial.conf
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
  systemctl stop anas-api
  if [[ $1 == on ]]; then systemctl start "$companion"; fi
  systemctl start anas-api
  for _ in $(seq 1 100); do curl -fsS http://127.0.0.1:8080/healthz >/dev/null 2>&1 && break; sleep 0.2; done
}

# serves_companion: the running Product Service answers HTTP on the companion socket (a 404 for
# this path; the calls themselves come only from members' UIDs). socat is the trial's own
# dependency (nas-trial.sh).
serves_companion() {
  local answer
  answer=$(printf 'GET /probe HTTP/1.0\r\n\r\n' | timeout 5 socat -t 3 -T 3 - "UNIX-CONNECT:$companion_socket" 2>/dev/null |
    head -c 5 || true)
  [[ $answer == HTTP/ ]]
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
  # The companion socket comes from systemd, named in the socket unit (FileDescriptorName).
  printf '[Unit]\nWants=%s\nAfter=%s\n\n[Service]\nEnvironment=%s\nSockets=%s\n' \
    "$companion" "$companion" "$gate" "$companion" > "$units/anas-api.service.d/$dropin"
  printf '[Unit]\nWants=anas-photos-assistant.socket\nAfter=anas-photos-assistant.socket\n\n[Service]\nSockets=anas-photos-assistant.socket\nEnvironment=ANAS_PHOTOS_ASSISTANT_SOCKET=%s\n' \
    "$photos" > "$units/anas-photos.service.d/$dropin"
  install -m 0644 "$here/anas-photos-assistant.socket" "$units/anas-photos-assistant.socket"
  install -m 0644 "$here/$companion" "$units/$companion"
  for service in "${services[@]}"; do chmod 0644 "$units/$service.service.d/$dropin"; done
  old_lines
  systemctl daemon-reload
  systemctl enable --quiet anas-photos-assistant.socket "$companion"
  restart on
  if ! serves_companion; then
    echo "the Product Service does not answer on $companion_socket: deploy one with the companion socket (#412)" >&2
  fi
}

off() {
  local service
  for service in "${services[@]}"; do
    rm -f "$units/$service.service.d/$dropin"
    rmdir "$units/$service.service.d" 2>/dev/null || true
  done
  systemctl disable --now --quiet anas-photos-assistant.socket "$companion" 2>/dev/null || true
  rm -f "$units/anas-photos-assistant.socket" "$units/$companion"
  old_lines
  systemctl daemon-reload
  restart off
}

state() {
  local gate_on=off photos_on=off companion_on=off
  if [[ -f "$units/anas-host-agent.service.d/$dropin" && -f "$units/anas-api.service.d/$dropin" ]] ||
    grep -qx "$gate" /etc/a-nas/host-agent.env 2>/dev/null; then gate_on=on; fi
  if systemctl is-active --quiet anas-photos-assistant.socket && [[ -f "$units/anas-photos.service.d/$dropin" ]]; then photos_on=on; fi
  if systemctl is-active --quiet "$companion" && grep -qx "Sockets=$companion" "$units/anas-api.service.d/$dropin" 2>/dev/null; then
    companion_on=unanswered
    if serves_companion; then companion_on=on; fi
  fi
  printf 'session gate %s, photos for assistants %s, companion for assistants %s\n' "$gate_on" "$photos_on" "$companion_on"
}

case ${1:-} in
  on) on ;;
  off) off ;;
  state) state ;;
  *) sed -n '2,20p' "$0"; exit 2 ;;
esac
