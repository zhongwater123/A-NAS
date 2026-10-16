#!/usr/bin/env bash
# Trial of the AI assistant (ADR 0024) on the Experimental NAS with a cloud model, for
# development testing only: docs/runbooks/try-ai-assistant-on-experimental-nas.md. Run as
# root from the unpacked bundle that nas-trial-bundle.sh makes (spike/, runtime.tar.gz,
# debs/, SHA256SUMS).
#
#   nas-trial.sh check        what is in place and what install would do; changes nothing
#   nas-trial.sh install      install for every member of a-nas-users, once, on fresh state
#   nas-trial.sh update       this bundle's scripts, units, policy and runtime over an install,
#                             keeping each member's assistant folder, conversations and memory
#   nas-trial.sh smoke USER   start USER's assistant and wait for it (no model call)
#   nas-trial.sh remove [--purge-key]
#                             undo install and delete the gateway's log; the key stays unless
#                             --purge-key
#
# The key is /etc/anas-spike/model.key (root, 0600), which the owner writes by hand once;
# install, update and remove leave it. ANAS_TRIAL_MODEL (default deepseek-flash) and ANAS_TRIAL_UPSTREAM (default
# api.deepseek.com) pick the OpenAI-compatible service.
#
# install runs assistant-layout.sh prepare, which works by path in folders members can
# write; it therefore refuses to run over anything an earlier run or a member left. update
# touches only root's own files (assistant-layout.sh members).
set -euo pipefail
here=$(cd "$(dirname "$0")" && pwd)
bundle=$(dirname "$here")
data=/srv/a-nas/data
trial=/opt/a-nas/assistant-trial
runtimes=/opt/a-nas/assistant-runtimes
key=/etc/anas-spike/model.key
model=${ANAS_TRIAL_MODEL:-deepseek-flash}
upstream=${ANAS_TRIAL_UPSTREAM:-api.deepseek.com}
env_line=ANAS_ASSISTANT_SOCKET_DIR=/run/a-nas-assistant
units=(anas-assistant@.service anas-assistant-token@.service anas-assistant-curator@.service anas-assistant-curator@.timer anas-assistant-api@.service anas-assistant-api@.socket anas-assistant-probe@.service
  anas-assistant-trial-gateway.service)
python=$runtimes/spike/venv/bin/python

say() { printf '%s\n' "$*"; }
fail() { printf 'STOP: %s\n' "$*" >&2; exit 1; }

# accounts: members of a-nas-users whose spaces and recycle bins the Host Agent made.
accounts() {
  local account
  for account in $(getent group a-nas-users | cut -d: -f4 | tr , ' '); do
    [[ "$account" =~ ^[a-z_][a-z0-9_-]{0,31}$ ]] || continue
    getent passwd "$account" >/dev/null || continue
    [[ -d "$data/spaces/private/$account/.a-nas-trash/$account" && -d "$data/spaces/shared/.a-nas-trash/$account" ]] || continue
    printf '%s\n' "$account"
  done
}

# leftovers: what install makes, if any of it is there.
leftovers() {
  local path
  for path in "$data/assistant" "$data/spaces/shared/Agent空间" /etc/hermes /etc/a-nas-assistant /spike "$trial" \
    "$runtimes/spike" /opt/a-nas/assistant-skills /etc/systemd/system/anas-assistant@.service; do
    if [[ -e "$path" || -L "$path" ]]; then printf '%s\n' "$path"; fi
  done
}

preflight() {
  [[ $EUID -eq 0 ]] || fail "run as root"
  [[ "$(findmnt -n -o FSTYPE --target "$data" 2>/dev/null)" == btrfs ]] || fail "$data is not the mounted Btrfs data volume"
  grep -q ANAS_ASSISTANT_SOCKET_DIR /opt/a-nas/current/anas-host-agent ||
    fail "the installed release ($(readlink -f /opt/a-nas/current)) has no assistant support"
  (cd "$bundle" && sha256sum --quiet -c SHA256SUMS) || fail "the bundle does not match its SHA256SUMS"
  local api_user
  api_user=$(systemctl show -p User --value anas-api)
  if [[ -z "$api_user" ]] || ! getent passwd "$api_user" >/dev/null; then fail "no Product Service account"; fi
  echo "$api_user"
}

check() {
  local api_user list
  api_user=$(preflight)
  list=$(accounts)
  say "host:         $(hostname) ($(uname -r))"
  say "release:      $(readlink -f /opt/a-nas/current)"
  say "data volume:  $(findmnt -n -o SOURCE,FSTYPE,UUID --target "$data")"
  say "memory:       $(free -m | awk '/^Mem|^内存/ {print $7 " MiB available of " $2}')"
  say "product user: $api_user"
  say "model:        $model at $upstream"
  if [[ -s "$key" && "$(stat -c '%a %U' "$key")" == "600 root" ]]; then say "key:          $key (600 root)"
  else say "key:          MISSING — write it to $key (root, 0600) before install"; fi
  say "packages:     $(for p in socat ripgrep tini; do dpkg-query -W -f='${Package}=${Status} ' "$p" 2>/dev/null | sed 's/install ok installed/ok/' || printf '%s=missing ' "$p"; done)"
  say "assistant on: $(grep -lx "$env_line" /etc/a-nas/host-agent.env /etc/a-nas/anas-api.env 2>/dev/null | tr '\n' ' ' || true)"
  say "accounts ($(wc -l <<<"$list")): $(tr '\n' ' ' <<<"$list")"
  local left
  left=$(leftovers)
  if [[ -n "$left" ]]; then say "already there (install refuses; remove first):"; sed 's/^/  /' <<<"$left"
  else say "fresh:        nothing from an earlier install"; fi
}

install_packages() {
  local deb todo=()
  say "--- packages"
  for deb in "$bundle"/debs/*.deb; do
    if ! dpkg-query -W -f='${Status}' "$(dpkg-deb -f "$deb" Package)" 2>/dev/null | grep -q 'install ok installed'; then
      todo+=("$deb")
    fi
  done
  if ((${#todo[@]})); then dpkg -i "${todo[@]}"; fi
  for tool in socat rg tini; do command -v "$tool" >/dev/null || fail "$tool is missing"; done
}

# install_files puts this bundle's scripts and runtime in place, replacing earlier ones.
install_files() {
  say "--- files"
  install -d -m 0755 "$trial" "$runtimes"
  rm -rf -- "$trial/spike" "$runtimes/spike"
  cp -r "$bundle/spike" "$trial/spike"
  chown -R root:root "$trial" && chmod -R go-w "$trial"
  tar -C "$runtimes" -xzf "$bundle/runtime.tar.gz"
  chown -R root:root "$runtimes/spike"
  install -m 0644 "$trial/spike/anas-assistant-trial.tmpfiles" /etc/tmpfiles.d/anas-assistant-trial.conf
  systemd-tmpfiles --create /etc/tmpfiles.d/anas-assistant-trial.conf
  sed "s/@UPSTREAM@/$upstream/" "$trial/spike/anas-assistant-trial-gateway.service" > /etc/systemd/system/anas-assistant-trial-gateway.service
}

install_trial() {
  local api_user list left
  api_user=$(preflight)
  [[ -s "$key" && "$(stat -c '%a %U' "$key")" == "600 root" ]] || fail "write the key to $key (root, 0600) first"
  left=$(leftovers)
  [[ -z "$left" ]] || fail "not fresh, remove first: $(tr '\n' ' ' <<<"$left")"
  list=$(accounts)
  [[ -n "$list" ]] || fail "no member of a-nas-users has a space"

  install_packages
  install_files
  ln -s "$trial/spike" /spike

  say "--- the assistant for $(wc -l <<<"$list") accounts"
  # shellcheck disable=SC2046 # one argument per account
  ASSISTANT_GATE=host-agent PRODUCT_USER=$api_user SPIKE_MODEL=$model SPIKE_MODEL_GATEWAY=external \
    bash /spike/assistant-install.sh $(tr '\n' ' ' <<<"$list")
  systemctl enable --now anas-assistant-trial-gateway.service

  say "--- the session gate in the Host Agent and the Product Service"
  local file
  for file in /etc/a-nas/host-agent.env /etc/a-nas/anas-api.env; do
    grep -qx "$env_line" "$file" || printf '%s\n' "$env_line" >> "$file"
  done
  systemctl restart anas-host-agent
  for _ in $(seq 1 100); do [[ -S /run/a-nas/assistant-changes.sock ]] && break; sleep 0.2; done
  systemctl restart anas-api
  for _ in $(seq 1 100); do curl -fsS http://127.0.0.1:8080/healthz >/dev/null 2>&1 && break; sleep 0.2; done
  verify
}

verify() {
  local ok=1 first
  first=$(accounts)
  first=${first%%$'\n'*}
  check_one() { if "${@:2}" >/dev/null 2>&1; then say "PASS $1"; else say "FAIL $1"; ok=0; fi; }
  check_one "model gateway running" systemctl is-active anas-assistant-trial-gateway.service
  check_one "model gateway socket" test -S /run/anas-assistant-gw/model.sock
  check_one "executor socket in the Host Agent" test -S /run/a-nas/assistant-changes.sock
  check_one "Product Service healthy" curl -fsS http://127.0.0.1:8080/healthz
  check_one "instance socket for $first" systemctl is-active "anas-assistant-api@$first.socket"
  check_one "instance sockets are root's alone" test "$(stat -c '%a %U' "/run/a-nas-assistant/$first.sock")" = "600 root"
  check_one "gateway log is root's alone" test "$(stat -c '%a' /var/log/anas-assistant-gateway)" = 700
  check_one "weekly curator for $first" systemctl is-enabled "anas-assistant-curator@$first.timer"
  ((ok)) || fail "see the FAIL lines; remove undoes the install"
  say "installed: open AI 助手 on the desktop (members of a-nas-users)"
}

update_trial() {
  local api_user list account ready=() missing=()
  api_user=$(preflight)
  [[ -s "$key" && "$(stat -c '%a %U' "$key")" == "600 root" ]] || fail "write the key to $key (root, 0600) first"
  [[ "$(readlink /spike)" == "$trial/spike" && -d /etc/a-nas-assistant ]] || fail "no trial installed here; use install"
  list=$(accounts)
  for account in $list; do
    if [[ -d "$data/assistant/$account" ]]; then ready+=("$account"); else missing+=("$account"); fi
  done
  ((${#ready[@]})) || fail "no member has an assistant folder; use install"
  say "--- stopping the running assistants"
  systemctl stop 'anas-assistant@*.service' 'anas-assistant-api@*.service' 2>/dev/null || true
  install_packages
  install_files
  say "--- the assistant for ${#ready[@]} accounts"
  ASSISTANT_GATE=host-agent PRODUCT_USER=$api_user SPIKE_MODEL=$model SPIKE_MODEL_GATEWAY=external ASSISTANT_LAYOUT=members \
    bash /spike/assistant-install.sh "${ready[@]}"
  systemctl restart anas-assistant-trial-gateway.service
  ((${#missing[@]} == 0)) || say "not set up (new since install; remove and install again to include them): ${missing[*]}"
  verify
}

smoke() {
  local account=${1:?account}
  [[ -f "/etc/a-nas-assistant/tokens/$account.env" ]] || fail "$account has no assistant"
  say "starting $account's assistant (the first start writes Hermes's own skills, up to a minute)"
  local started=$SECONDS
  "$python" /spike/serve_client.py "$account" ready
  say "ready after $((SECONDS - started)) s; memory $(systemctl show -p MemoryCurrent --value "anas-assistant@$account.service") bytes"
}

# drop PATH...: remove what it can, say what stays, and carry on.
drop() {
  local path
  for path in "$@"; do
    if [[ -e "$path" || -L "$path" ]]; then rm -rf -- "$path" 2>/dev/null || say "could not remove $path"; fi
  done
}

remove_trial() {
  local purge=${1:-}
  [[ $EUID -eq 0 ]] || fail "run as root"
  [[ -z "$purge" || "$purge" == --purge-key ]] || fail "remove takes only --purge-key"
  # Without the volume the assistant directories cannot be deleted, and once the tokens
  # are gone nothing would name them.
  [[ "$(findmnt -n -o FSTYPE --target "$data" 2>/dev/null)" == btrfs ]] || fail "$data is not mounted; mount it first"
  local file account path accounts=()
  say "--- the session gate off"
  for file in /etc/a-nas/host-agent.env /etc/a-nas/anas-api.env; do
    if [[ -f "$file" ]]; then sed -i "\#^$env_line\$#d" "$file"; fi
  done
  systemctl restart anas-host-agent anas-api
  say "--- assistants"
  for file in /etc/a-nas-assistant/tokens/*.env "$data"/assistant/*/; do
    [[ -e "$file" ]] || continue
    account=$(basename "$file" .env)
    [[ " ${accounts[*]} " == *" $account "* ]] || accounts+=("$account")
  done
  for account in "${accounts[@]}"; do
    if bash "$here/assistant-layout.sh" remove "$data" "$account"; then say "removed $account's assistant"
    else say "could not remove $account's assistant"; fi
  done
  # Agent空间 keeps whatever members published; only empty folders go.
  if [[ -d "$data/spaces/shared/Agent空间" && ! -L "$data/spaces/shared/Agent空间" ]]; then
    for path in "$data/spaces/shared/Agent空间"/*/; do
      if [[ -d "$path" ]]; then rmdir "$path" 2>/dev/null || say "kept $path (not empty)"; fi
    done
    rmdir "$data/spaces/shared/Agent空间" 2>/dev/null || say "kept $data/spaces/shared/Agent空间 (not empty)"
  fi
  if [[ -d "$data/assistant" && ! -L "$data/assistant" ]]; then
    drop "$data/assistant/.changes"
    rmdir "$data/assistant" 2>/dev/null || say "kept $data/assistant: $(find "$data/assistant" -mindepth 1 -maxdepth 1 -printf '%f ')"
  fi
  say "--- units and files"
  systemctl disable --now anas-assistant-trial-gateway.service 2>/dev/null || true
  for file in "${units[@]}"; do drop "/etc/systemd/system/$file"; done
  drop /etc/systemd/system/anas-assistant@.service.d /etc/systemd/system/anas-assistant-api@.service.d \
    /etc/systemd/system/anas-assistant-api@.socket.d /etc/tmpfiles.d/anas-assistant-trial.conf
  systemctl daemon-reload
  drop /run/a-nas-assistant /run/a-nas-assistant-start /run/anas-assistant-start /run/anas-assistant-gw /run/anas-assistant-photos \
    /run/anas-change-agent /run/anas-spike-broker \
    /etc/hermes /etc/a-nas-assistant /opt/a-nas/assistant-skills "$runtimes/spike" "$trial"
  rmdir "$runtimes" 2>/dev/null || true
  if [[ -L /spike ]]; then rm -f /spike; fi
  say "--- the gateway's log"
  drop /var/log/anas-assistant-gateway
  if [[ -n "$purge" ]]; then
    drop /etc/anas-spike
    say "the key is deleted"
  else
    say "the key stays at $key (remove --purge-key deletes it)"
  fi
  say "removed; socat and ripgrep stay installed (dpkg -r socat ripgrep removes them)"
}

case ${1:-} in
  check) check ;;
  install) install_trial ;;
  update) update_trial ;;
  smoke) smoke "${2:-}" ;;
  remove) remove_trial "${2:-}" ;;
  *) sed -n '2,24p' "$0"; exit 2 ;;
esac
