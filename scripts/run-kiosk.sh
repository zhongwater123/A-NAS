#!/usr/bin/env bash
set -Eeuo pipefail

url="${ANAS_KIOSK_URL:-http://127.0.0.1:8080/?local-console=1}"
profile_root="${XDG_STATE_HOME:-$HOME/.local/state}/a-nas/chromium"
output="${ANAS_KIOSK_OUTPUT:-}"
transform="${ANAS_KIOSK_TRANSFORM:-normal}"
scale="${ANAS_KIOSK_SCALE:-1}"

[[ "$url" == "http://127.0.0.1:8080/?local-console=1" ]] || {
  echo "ANAS_KIOSK_URL must remain http://127.0.0.1:8080/?local-console=1" >&2
  exit 2
}

if [[ -z "$output" && ( -n "${ANAS_KIOSK_TRANSFORM:-}" || -n "${ANAS_KIOSK_SCALE:-}" ) ]]; then
  echo "ANAS_KIOSK_OUTPUT is required when transform or scale is configured" >&2
  exit 2
fi
if [[ -n "$output" ]]; then
  [[ "$output" =~ ^[A-Za-z0-9][A-Za-z0-9._-]*$ ]] || {
    echo "ANAS_KIOSK_OUTPUT contains unsupported characters" >&2
    exit 2
  }
  case "$transform" in
    normal|90|180|270|flipped|flipped-90|flipped-180|flipped-270) ;;
    *) echo "ANAS_KIOSK_TRANSFORM is invalid" >&2; exit 2 ;;
  esac
  [[ "$scale" =~ ^(0\.[0-9]*[1-9][0-9]*|[1-9][0-9]*(\.[0-9]+)?)$ ]] || {
    echo "ANAS_KIOSK_SCALE must be a positive number" >&2
    exit 2
  }
fi

if [[ "${1:-}" == "--check-output-config" ]]; then
  exit 0
fi
[[ $# -eq 0 ]] || { echo "usage: kiosk-launcher [--check-output-config]" >&2; exit 2; }

install -d -m 0700 "$profile_root"

if [[ -n "$output" ]]; then
  /usr/bin/wlr-randr \
    --output "$output" \
    --transform "$transform" \
    --scale "$scale"
fi

api_ready=0
for _ in $(seq 1 30); do
  response="$(timeout 2 bash -c "exec 3<>/dev/tcp/127.0.0.1/8080; printf 'GET /healthz HTTP/1.1\\r\\nHost: 127.0.0.1\\r\\nConnection: close\\r\\n\\r\\n' >&3; cat <&3" 2>/dev/null || true)"
  if [[ "$response" == *"200 OK"* && "$response" == *'"status":"ok"'* ]]; then
    api_ready=1
    break
  fi
  sleep 1
done

[[ $api_ready -eq 1 ]] || {
  echo "A-NAS product service did not become ready" >&2
  exit 1
}

exec /usr/bin/chromium \
  --ozone-platform=wayland \
  --kiosk \
  --app="$url" \
  --user-data-dir="$profile_root" \
  --no-first-run \
  --no-default-browser-check \
  --noerrdialogs \
  --disable-session-crashed-bubble \
  --disable-restore-session-state \
  --disable-translate \
  --password-store=basic \
  --overscroll-history-navigation=0
