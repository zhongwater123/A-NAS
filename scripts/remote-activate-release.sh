#!/usr/bin/env bash
set -Eeuo pipefail

if [[ $# -lt 5 || $# -gt 7 ]]; then
  echo "usage: remote-activate-release.sh VERSION API_SHA AGENT_SHA MODE PRODUCT_VERSION [activate|stage-only] [SCREENSAVER_SHA_LIST]" >&2
  exit 2
fi

version="$1"
api_sha="$2"
agent_sha="$3"
mode="$4"
product_version="$5"
action="${6:-activate}"
screensaver_hashes="${7:-}"

[[ "$version" =~ ^[0-9a-f]{7,40}$ ]] || { echo "invalid version" >&2; exit 2; }
[[ "$api_sha" =~ ^[0-9a-f]{64}$ ]] || { echo "invalid API hash" >&2; exit 2; }
[[ "$agent_sha" =~ ^[0-9a-f]{64}$ ]] || { echo "invalid Host Agent hash" >&2; exit 2; }
[[ "$mode" == "fake" || "$mode" == "agent" ]] || { echo "invalid host state mode" >&2; exit 2; }
[[ "$product_version" =~ ^[0-9a-f]{7,40}$ || "$product_version" =~ ^v[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?(\+[0-9A-Za-z.-]+)?$ ]] || { echo "invalid product version" >&2; exit 2; }
[[ "$action" == "activate" || "$action" == "stage-only" ]] || { echo "invalid release action" >&2; exit 2; }
screensaver_hash_list=()
if [[ -n "$screensaver_hashes" ]]; then
  IFS=',' read -r -a screensaver_hash_list <<< "$screensaver_hashes"
  (( ${#screensaver_hash_list[@]} <= 32 )) || { echo "too many screen saver hashes" >&2; exit 2; }
  for hash in "${screensaver_hash_list[@]}"; do
    [[ "$hash" =~ ^[0-9a-f]{64}$ ]] || { echo "invalid screen saver hash" >&2; exit 2; }
  done
fi

release="$HOME/apps/a-nas/releases/$version"
current="$HOME/apps/a-nas/current"
config_dir="$HOME/.config/a-nas"
unit_dir="$HOME/.config/systemd/user"
previous=""
rollback_dir=""
activated=0

rollback() {
  status=$?
  trap - ERR
  if [[ $activated -eq 1 ]]; then
    echo "activation failed; restoring previous release" >&2
    if [[ -n "$previous" ]]; then
      ln -sfn "$previous" "$HOME/apps/a-nas/current.rollback"
      mv -Tf "$HOME/apps/a-nas/current.rollback" "$current"
    else
      rm -f "$current"
    fi
    if [[ -f "$rollback_dir/anas-api.env" ]]; then
      install -m 0600 "$rollback_dir/anas-api.env" "$config_dir/anas-api.env"
    else
      rm -f "$config_dir/anas-api.env"
    fi
    if [[ -f "$rollback_dir/anas-api.service" ]]; then
      install -m 0644 "$rollback_dir/anas-api.service" "$unit_dir/anas-api.service"
    else
      rm -f "$unit_dir/anas-api.service"
    fi
    if [[ -f "$rollback_dir/anas-host-agent.service" ]]; then
      install -m 0644 "$rollback_dir/anas-host-agent.service" "$unit_dir/anas-host-agent.service"
    else
      rm -f "$unit_dir/anas-host-agent.service"
    fi
    systemctl --user daemon-reload || true
    if [[ -n "$previous" ]]; then
      if grep -qx 'ANAS_HOSTSTATE_MODE=agent' "$config_dir/anas-api.env" 2>/dev/null; then
        systemctl --user enable --now anas-host-agent.service || true
      else
        systemctl --user disable --now anas-host-agent.service >/dev/null 2>&1 || true
      fi
      systemctl --user restart anas-api.service || true
    else
      systemctl --user disable --now anas-api.service anas-host-agent.service >/dev/null 2>&1 || true
    fi
  fi
  [[ -z "$rollback_dir" ]] || rm -rf -- "$rollback_dir"
  exit "$status"
}
trap rollback ERR

cd "$release"
printf '%s  %s\n' "$api_sha" anas-api.incoming | sha256sum -c -
printf '%s  %s\n' "$agent_sha" anas-host-agent.incoming | sha256sum -c -
shopt -s nullglob
screensaver_incoming=(screensaver-*.mp4.incoming)
shopt -u nullglob
if (( ${#screensaver_incoming[@]} != ${#screensaver_hash_list[@]} )); then
  echo "screen saver upload count does not match the verified hash list" >&2
  exit 2
fi
for index in "${!screensaver_hash_list[@]}"; do
  filename=$(printf 'screensaver-%03d.mp4' "$index")
  printf '%s  %s\n' "${screensaver_hash_list[$index]}" "$filename.incoming" | sha256sum -c -
done
if (( ${#screensaver_hash_list[@]} == 0 )) && [[ -e screensavers ]]; then
  echo "screen saver pool exists without a verified hash list" >&2
  exit 2
fi
install -m 0750 anas-api.incoming anas-api
install -m 0750 anas-host-agent.incoming anas-host-agent
install -m 0644 anas-api.service.incoming anas-api.service
install -m 0644 anas-host-agent.service.incoming anas-host-agent.service
install -m 0750 kiosk-launcher.incoming kiosk-launcher
install -m 0644 anas-kiosk@.service.incoming anas-kiosk@.service
install -m 0644 a-nas-kiosk.pam.incoming a-nas-kiosk.pam
install -m 0644 kiosk.env.incoming kiosk.env
install -m 0644 a-nas-chromium-policy.json.incoming a-nas-chromium-policy.json
install -m 0644 anas-api-system.service.incoming anas-api-system.service
install -m 0644 anas-host-agent-system.service.incoming anas-host-agent-system.service
install -m 0644 anas-photos-system.service.incoming anas-photos-system.service
install -m 0750 install-v1.0.1-system-services.sh.incoming install-v1.0.1-system-services.sh
install -m 0750 provision-v1.0.1-rc.sh.incoming provision-v1.0.1-rc.sh
if (( ${#screensaver_hash_list[@]} > 0 )); then
  [[ ! -e screensavers ]] || { echo "screen saver pool is already staged" >&2; exit 2; }
  install -d -m 0750 screensavers.incoming
  for index in "${!screensaver_hash_list[@]}"; do
    filename=$(printf 'screensaver-%03d.mp4' "$index")
    install -m 0640 "$filename.incoming" "screensavers.incoming/$filename"
  done
  mv -- screensavers.incoming screensavers
fi
rm -f \
  anas-api.incoming \
  anas-host-agent.incoming \
  kiosk-launcher.incoming \
  anas-kiosk@.service.incoming \
  a-nas-kiosk.pam.incoming \
  kiosk.env.incoming \
  a-nas-chromium-policy.json.incoming \
  anas-api-system.service.incoming \
  anas-host-agent-system.service.incoming \
  anas-photos-system.service.incoming \
  install-v1.0.1-system-services.sh.incoming \
  provision-v1.0.1-rc.sh.incoming \
  "${screensaver_incoming[@]}"

{
  printf 'version=%s\nproduct_version=%s\napi_sha256=%s\nhost_agent_sha256=%s\nmode=%s\nsource=%s\n' \
    "$version" "$product_version" "$api_sha" "$agent_sha" "$mode" \
    "https://github.com/zhongwater123/A-NAS/commit/$version"
  for index in "${!screensaver_hash_list[@]}"; do
    printf 'screensaver_%03d_sha256=%s\n' "$index" "${screensaver_hash_list[$index]}"
  done
} > RELEASE
chmod 0640 RELEASE

if [[ "$action" == "stage-only" ]]; then
  trap - ERR
  echo "staged A-NAS $version without changing running services"
  exit 0
fi

install -d -m 0700 "$config_dir"
install -d -m 0700 "$config_dir/state"
install -d -m 0750 "$unit_dir"
previous="$(readlink "$current" 2>/dev/null || true)"
rollback_dir="$(mktemp -d "$config_dir/activation-rollback.XXXXXX")"
[[ ! -f "$config_dir/anas-api.env" ]] || cp "$config_dir/anas-api.env" "$rollback_dir/anas-api.env"
[[ ! -f "$unit_dir/anas-api.service" ]] || cp "$unit_dir/anas-api.service" "$rollback_dir/anas-api.service"
[[ ! -f "$unit_dir/anas-host-agent.service" ]] || cp "$unit_dir/anas-host-agent.service" "$rollback_dir/anas-host-agent.service"
activated=1

printf 'ANAS_HTTP_ADDR=127.0.0.1:8080\nANAS_HOSTSTATE_MODE=%s\nANAS_STATE_DIR=%s/state\n' \
  "$mode" "$config_dir" > "$config_dir/anas-api.env"
# Container management stays disabled until an administrator has installed the
# root-owned container agent; its socket is the signal that it is available.
if [[ "$mode" == "agent" && -S /run/a-nas-container/agent.sock ]]; then
  printf 'ANAS_CONTAINERS_MODE=agent\n' >> "$config_dir/anas-api.env"
fi
if [[ -d "$release/screensavers" ]]; then
  printf 'ANAS_SCREENSAVER_DIRECTORY=%s/screensavers\n' "$release" >> "$config_dir/anas-api.env"
fi
chmod 0600 "$config_dir/anas-api.env"
install -m 0644 anas-api.service "$unit_dir/anas-api.service"
install -m 0644 anas-host-agent.service "$unit_dir/anas-host-agent.service"

ln -sfn "$release" "$HOME/apps/a-nas/current.next"
mv -Tf "$HOME/apps/a-nas/current.next" "$current"

systemctl --user daemon-reload
if [[ "$mode" == "agent" ]]; then
  systemctl --user enable --now anas-host-agent.service
else
  systemctl --user disable --now anas-host-agent.service >/dev/null 2>&1 || true
fi
systemctl --user enable anas-api.service
systemctl --user restart anas-api.service

http_get() {
  local path="$1"
  timeout 3 bash -c "exec 3<>/dev/tcp/127.0.0.1/8080; printf 'GET ${path} HTTP/1.1\\r\\nHost: 127.0.0.1\\r\\nConnection: close\\r\\n\\r\\n' >&3; cat <&3"
}

healthy=0
for _ in $(seq 1 20); do
  health="$(http_get /healthz 2>/dev/null || true)"
  setup="$(http_get /api/v1/setup/status 2>/dev/null || true)"
  if [[ "$health" == *"200 OK"* && "$health" == *'"status":"ok"'* && \
        "$setup" == *"200 OK"* && "$setup" == *'"setupRequired"'* ]]; then
    healthy=1
    break
  fi
  sleep 1
done
[[ $healthy -eq 1 ]] || { echo "release health verification failed" >&2; false; }

systemctl --user is-active --quiet anas-api.service
if [[ "$mode" == "agent" ]]; then
  systemctl --user is-active --quiet anas-host-agent.service
fi

activated=0
trap - ERR
[[ -z "$rollback_dir" ]] || rm -rf -- "$rollback_dir"
echo "activated A-NAS $version in $mode mode"
