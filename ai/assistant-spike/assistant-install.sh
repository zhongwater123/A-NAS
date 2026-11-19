#!/usr/bin/env bash
# The spike's stand-in for the a-nas package and the Host Agent (ADR 0024), as root in the
# disposable systemd container run.sh starts (setup.sh): the assistant's files where the
# package installs them (/usr/lib/a-nas/assistant, from the checkout at /src), its units,
# the model (the scripted fake model, or the gateway with a real key), the spike's change
# broker, then assistant-layout.sh prepare for each account, which writes what the Host Agent
# would.
#   assistant-install.sh ACCOUNT...
# Environment: SPIKE_MODEL, SPIKE_UPSTREAM, SPIKE_CHANNEL, SPIKE_APPROVAL_TIMEOUT as for
# run.sh; PRODUCT_USER, the account of the product service, which alone may connect to the
# broker's product socket and the instance sockets, and gets a copy of each instance secret.
# On an A-NAS the a-nas package and the Host Agent do all of this; the system test checks
# theirs (system-test.sh). Running it again replaces the model and the broker.
set -euo pipefail
here=$(cd "$(dirname "$0")" && pwd)
src=${ANAS_SOURCE:-/src}
lib=/usr/lib/a-nas/assistant
product_user=${PRODUCT_USER:-}
if [[ -n "$product_user" ]] && ! getent passwd "$product_user" >/dev/null; then
  echo "no account $product_user" >&2
  exit 2
fi
[[ -d "$src/ai/assistant" ]] || { echo "no checkout at $src (run.sh mounts it)" >&2; exit 2; }

# The package's files: scripts, the managed policy (with the spike's model and approval
# timeout), the system skills, the files the sandbox binds and the runtime's link.
install -d -m 0755 "$lib/managed" "$lib/empty"
install -m 0755 "$src"/ai/assistant/*.sh "$src"/ai/assistant/*.py "$lib/"
install -m 0644 "$src/ai/assistant/member-skill.md" "$lib/"
rm -rf "$lib/skills" && cp -r "$src/ai/assistant/skills" "$lib/skills" && chmod -R u=rwX,go=rX "$lib/skills"
sed -e "s/^  default: deepseek-flash$/  default: ${SPIKE_MODEL:-spike-model}/" \
  -e "s/^  timeout: 86400$/  timeout: ${SPIKE_APPROVAL_TIMEOUT:-86400}/" \
  "$src/ai/assistant/managed-config.yaml" > "$lib/managed/config.yaml"
chmod 0644 "$lib/managed/config.yaml"
install -m 0644 /dev/null "$lib/empty.env"
# The policy of scheduled tasks, with the montage center's planning server, and the prompt
# templates of a planning round, as the package installs them (scripts/build-packages.sh).
# shellcheck source=../../scripts/lib/assistant-units.sh
. "$src/scripts/lib/assistant-units.sh"
install -d -m 0755 "$lib/managed-cron" "$lib/montage-prompts"
derive_cron_policy "$lib/managed/config.yaml" "$src/ai/assistant/montage-mcp.yaml" "$lib/managed-cron/config.yaml"
install -m 0644 "$src"/ai/anas_montage/prompts/*.md "$lib/montage-prompts/"
ln -sfn /opt/a-nas/assistant-runtimes/spike "$lib/runtime"
[[ -f "$lib/runtime/src/SOUL.md" ]] || { install -d /opt/a-nas/assistant-runtimes/spike/src && : > /opt/a-nas/assistant-runtimes/spike/src/SOUL.md; }

# The mount points of the package's tmpfiles.d.
install -d -m 0755 /run/a-nas-assistant /run/anas-assistant-gw /run/anas-assistant-skills \
  /run/anas-assistant-photos /run/anas-assistant-companion /run/anas-assistant-montage /run/anas-change-agent
for file in /run/anas-assistant-photos/photos.sock /run/anas-assistant-companion/companion.sock /run/anas-assistant-montage/montage.sock; do
  [[ -e "$file" ]] || : > "$file"
done

# The units, as the package installs them, and the probe in the same sandbox.
units=/etc/systemd/system
install -m 0644 "$src"/deploy/systemd/system/anas-assistant{@,-net@,-api@}.service "$src/deploy/systemd/system/anas-assistant-api@.socket" \
  "$src/deploy/systemd/system/anas-assistant-curator@.timer" "$src/deploy/systemd/system/anas-assistant-cron-waker.service" "$units/"
# The spike's change broker binds no Host Agent socket; the empty file stands in.
sed -i 's#^BindReadOnlyPaths=/run/a-nas/assistant-changes.sock:#BindReadOnlyPaths=-/run/a-nas/assistant-changes.sock:#' "$units/anas-assistant@.service"
# The units run while a key is saved; the spike has none.
sed -i '/^ConditionPathExists=/d' "$units/anas-assistant@.service" "$units/anas-assistant-net@.service" \
  "$units/anas-assistant-cron-waker.service"
derive_assistant_units "$units"
sed -e 's#^ExecStart=.*#ExecStart=/bin/bash /spike/probe.sh#' -e '/^ExecStartPost=/d' -e 's/^Type=.*/Type=oneshot/' -e '/^StopWhenUnneeded/d' \
  "$units/anas-assistant@.service" > "$units/anas-assistant-probe@.service"
if [[ "${SPIKE_CHANNEL:-serve}" == api_server ]]; then
  install -d "$units/anas-assistant@.service.d" "$units/anas-assistant-api@.service.d"
  install -m 0644 "$here/api-server-channel/anas-assistant.conf" "$units/anas-assistant@.service.d/channel.conf"
  install -m 0644 "$here/api-server-channel/anas-assistant-api.conf" "$units/anas-assistant-api@.service.d/channel.conf"
fi
rm -f "$units/anas-assistant-api@.socket.d/product.conf"
if [[ -n "$product_user" ]]; then
  # Without the Host Agent's gate only the product service may connect, as its own user.
  install -d "$units/anas-assistant-api@.socket.d"
  printf '[Socket]\nSocketUser=%s\nSocketMode=0600\n' "$product_user" > "$units/anas-assistant-api@.socket.d/product.conf"
fi

# transient UNIT ARGS...: (re)start a transient service, replacing one left by an earlier run.
transient() {
  systemctl stop "$1" 2>/dev/null || true
  systemctl reset-failed "$1" 2>/dev/null || true
  systemd-run --unit "$@"
}
if [[ -n "${SPIKE_MODEL:-}" ]]; then
  # Real model: the gateway holds the key; the sandbox cannot see the key file. The spike's
  # research logs the messages too.
  transient model-gateway --quiet -E ANAS_ASSISTANT_KEY_FILE=/etc/anas-spike/model.key \
    -E ANAS_ASSISTANT_UPSTREAM="https://${SPIKE_UPSTREAM:-api.deepseek.com}" -E GATEWAY_LOG_MESSAGES=1 \
    python3 "$lib/gateway.py" /run/anas-assistant-gw/model.sock /var/log/gateway.jsonl
else
  transient fake-model --quiet python3 "$here/fake_model.py" /run/anas-assistant-gw/model.sock /var/log/fake-model.jsonl
fi
# The spike's change broker executes the assistants' changes, with a product socket for the
# product service and the agent socket for the assistants' MCP tool.
install -d -m 0755 /run/anas-spike-broker
transient change-broker --quiet -E SPIKE_PRODUCT_UID="${product_user:+$(id -u "$product_user")}" \
  python3 "$here/change_broker.py" /run/anas-spike-broker/broker.sock /run/anas-change-agent/agent.sock
systemctl daemon-reload
systemctl enable --now --quiet anas-assistant-cron-waker.service
PRODUCT_USER=${product_user:-root} bash "$here/assistant-layout.sh" prepare /srv/a-nas/data "$@"
