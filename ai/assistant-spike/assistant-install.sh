#!/usr/bin/env bash
# Installs the assistant beside existing accounts and spaces, as root: the managed Hermes
# policy, system skills, the units, the model gateway (or the scripted fake model), the
# change broker, then assistant-layout.sh prepare for each account. setup.sh runs it on the
# spike's hand-made layout, ai/assistant-spike/system-test.sh on the real installer's.
#   assistant-install.sh ACCOUNT...
# Environment: SPIKE_MODEL, SPIKE_UPSTREAM, SPIKE_CHANNEL, SPIKE_APPROVAL_TIMEOUT as for
# run.sh; PRODUCT_USER, the account of the product service, which alone may connect to the
# instance sockets and the broker's product socket and gets a copy of each session token.
# Like assistant-layout.sh, only for the disposable spike and system-test containers, never
# on the Experimental NAS (#116). Running it again replaces the model and the broker.
set -euo pipefail
here=$(cd "$(dirname "$0")" && pwd)
runtime=/opt/a-nas/assistant-runtimes/spike
product_user=${PRODUCT_USER:-}
if [[ -n "$product_user" ]] && ! getent passwd "$product_user" >/dev/null; then
  echo "no account $product_user" >&2
  exit 2
fi

# Managed Hermes policy: root-owned, read-only inside the sandbox.
install -d -m 0755 /etc/hermes /run/anas-assistant-gw /run/a-nas-assistant
# System skills live in a root-owned directory, like the runtime.
install -d -m 0755 /opt/a-nas/assistant-skills /etc/a-nas-assistant /etc/a-nas-assistant/empty
: > /etc/a-nas-assistant/empty.env && chmod 0644 /etc/a-nas-assistant/empty.env
if [[ -f "$runtime/src/SOUL.md" ]]; then
  install -m 0644 "$runtime/src/SOUL.md" /etc/a-nas-assistant/SOUL.md
else
  install -m 0644 /dev/null /etc/a-nas-assistant/SOUL.md  # no runtime: only the probe runs
fi
cp -r "$here/skills/." /opt/a-nas/assistant-skills/ && chown -R root:root /opt/a-nas/assistant-skills && chmod -R u=rwX,go=rX /opt/a-nas/assistant-skills
sed -e "s/spike-model/${SPIKE_MODEL:-spike-model}/" -e "s/^  timeout: 300$/  timeout: ${SPIKE_APPROVAL_TIMEOUT:-300}/" \
  "$here/managed-config.yaml" > /etc/hermes/config.yaml
chmod 0644 /etc/hermes/config.yaml

# Mount point for the photo service's assistant socket (#113): an empty file on the host,
# the socket itself inside each sandbox (anas-assistant@.service).
install -d -m 0755 /run/anas-assistant-photos && : > /run/anas-assistant-photos/photos.sock
install -m 0644 "$here/anas-assistant@.service" "$here/anas-assistant-api@.service" "$here/anas-assistant-api@.socket" /etc/systemd/system/
if [[ "${SPIKE_CHANNEL:-serve}" == api_server ]]; then
  install -d /etc/systemd/system/anas-assistant@.service.d /etc/systemd/system/anas-assistant-api@.service.d
  install -m 0644 "$here/api-server-channel/anas-assistant.conf" /etc/systemd/system/anas-assistant@.service.d/channel.conf
  install -m 0644 "$here/api-server-channel/anas-assistant-api.conf" /etc/systemd/system/anas-assistant-api@.service.d/channel.conf
fi
# The same sandbox with probe.sh in place of Hermes.
sed -e 's#^ExecStart=.*#ExecStart=/bin/bash /spike/probe.sh#' -e '/^ExecStartPost=/d' -e 's/^Type=.*/Type=oneshot/' -e '/^StopWhenUnneeded/d' \
  "$here/anas-assistant@.service" > /etc/systemd/system/anas-assistant-probe@.service
if [[ -n "$product_user" ]]; then
  # Only the product service may connect, as in A-NAS where it is the a-nas user.
  install -d /etc/systemd/system/anas-assistant-api@.socket.d
  printf '[Socket]\nSocketUser=%s\nSocketMode=0600\n' "$product_user" > /etc/systemd/system/anas-assistant-api@.socket.d/product.conf
fi

# transient UNIT ARGS...: (re)start a transient service, replacing one left by an earlier run.
transient() {
  systemctl stop "$1" 2>/dev/null || true
  systemctl reset-failed "$1" 2>/dev/null || true
  systemd-run --unit "$@"
}
if [[ -n "${SPIKE_MODEL:-}" ]]; then
  # Real model: the gateway holds the key; the sandbox cannot see the key file.
  transient model-gateway --quiet python3 "$here/gateway.py" /run/anas-assistant-gw/model.sock \
    /var/log/gateway.jsonl "${SPIKE_UPSTREAM:-api.deepseek.com}" /etc/anas-spike/model.key
else
  transient fake-model --quiet python3 "$here/fake_model.py" /run/anas-assistant-gw/model.sock /var/log/fake-model.jsonl
fi
# The change broker (in A-NAS the product service and the File Broker): the product
# socket for the product service, the agent socket for the assistants' MCP tool.
install -d -m 0755 /run/anas-spike-broker /run/anas-change-agent
transient change-broker --quiet -E SPIKE_PRODUCT_UID="${product_user:+$(id -u "$product_user")}" \
  python3 "$here/change_broker.py" /run/anas-spike-broker/broker.sock /run/anas-change-agent/agent.sock
systemctl daemon-reload
PRODUCT_USER=${product_user:-root} bash "$here/assistant-layout.sh" prepare /srv/a-nas/data "$@"
