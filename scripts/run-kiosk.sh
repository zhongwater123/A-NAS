#!/usr/bin/env bash
set -Eeuo pipefail

url="${ANAS_KIOSK_URL:-http://127.0.0.1:8080/}"
profile_root="${XDG_STATE_HOME:-$HOME/.local/state}/a-nas/chromium"

[[ "$url" == "http://127.0.0.1:8080/" ]] || {
  echo "ANAS_KIOSK_URL must remain http://127.0.0.1:8080/" >&2
  exit 2
}

install -d -m 0700 "$profile_root"

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
