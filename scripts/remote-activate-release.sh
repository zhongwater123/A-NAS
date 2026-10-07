#!/usr/bin/env bash
set -Eeuo pipefail

if [[ $# -ne 5 ]]; then
  echo "usage: remote-activate-release.sh VERSION API_SHA AGENT_SHA MODE PRODUCT_VERSION" >&2
  exit 2
fi

version="$1"
api_sha="$2"
agent_sha="$3"
mode="$4"
product_version="$5"

[[ "$version" =~ ^[0-9a-f]{7,40}$ ]] || { echo "invalid version" >&2; exit 2; }
[[ "$api_sha" =~ ^[0-9a-f]{64}$ ]] || { echo "invalid API hash" >&2; exit 2; }
[[ "$agent_sha" =~ ^[0-9a-f]{64}$ ]] || { echo "invalid Host Agent hash" >&2; exit 2; }
[[ "$mode" == "fake" || "$mode" == "agent" ]] || { echo "invalid host state mode" >&2; exit 2; }
[[ "$product_version" =~ ^[0-9a-f]{7,40}$ || "$product_version" =~ ^v[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?(\+[0-9A-Za-z.-]+)?$ ]] || { echo "invalid product version" >&2; exit 2; }

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
install -m 0750 anas-api.incoming anas-api
install -m 0750 anas-host-agent.incoming anas-host-agent
install -m 0750 kiosk-launcher.incoming kiosk-launcher
install -m 0644 anas-kiosk@.service.incoming anas-kiosk@.service
install -m 0644 a-nas-kiosk.pam.incoming a-nas-kiosk.pam
install -m 0644 kiosk.env.incoming kiosk.env
install -m 0644 anas-api-system.service.incoming anas-api-system.service
install -m 0644 anas-host-agent-system.service.incoming anas-host-agent-system.service
install -m 0750 install-v1.0.1-system-services.sh.incoming install-v1.0.1-system-services.sh
install -m 0750 provision-v1.0.1-rc.sh.incoming provision-v1.0.1-rc.sh
rm -f \
  anas-api.incoming \
  anas-host-agent.incoming \
  kiosk-launcher.incoming \
  anas-kiosk@.service.incoming \
  a-nas-kiosk.pam.incoming \
  kiosk.env.incoming \
  anas-api-system.service.incoming \
  anas-host-agent-system.service.incoming \
  install-v1.0.1-system-services.sh.incoming \
  provision-v1.0.1-rc.sh.incoming

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
chmod 0600 "$config_dir/anas-api.env"
install -m 0644 anas-api.service.incoming "$unit_dir/anas-api.service"
install -m 0644 anas-host-agent.service.incoming "$unit_dir/anas-host-agent.service"

ln -sfn "$release" "$HOME/apps/a-nas/current.next"
mv -Tf "$HOME/apps/a-nas/current.next" "$current"

printf 'version=%s\nproduct_version=%s\napi_sha256=%s\nhost_agent_sha256=%s\nmode=%s\nsource=%s\n' \
  "$version" "$product_version" "$api_sha" "$agent_sha" "$mode" \
  "https://github.com/zhongwater123/A-NAS/commit/$version" > RELEASE
chmod 0640 RELEASE

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
