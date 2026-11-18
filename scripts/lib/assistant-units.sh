# shellcheck shell=bash
# The AI assistant's units that run in the sandbox of anas-assistant@.service
# (ADR 0024), made from it so that they stay the same: a round of the member's
# scheduled tasks and the weekly curator, each a oneshot, both without the
# companion's socket, which only a conversation's window answers (#412). The
# round alone binds the montage center's socket and reads the policy and the
# member settings that add its planning server (managed-cron, config-cron.yaml;
# docs/architecture/montage-center.md, MCP 服务器). The
# curator also prunes Hermes's image cache, which serve never does
# (ai/assistant/media-cache.py). scripts/build-packages.sh and the spike's
# installer (ai/assistant-spike/assistant-install.sh) source this file.

# derive_cron_policy POLICY SERVER OUT: the managed policy of a member's scheduled tasks in
# OUT: POLICY, the policy of every other unit, with the montage center's planning server
# added, the fragment of mcp_servers in SERVER. A conversation's instance never starts that
# server (docs/architecture/montage-center.md, MCP 服务器). The text is spliced, which keeps
# the policy's comments; internal/hostops/linux reads the result as YAML in its tests, where a
# key written twice or a fragment out of place fails.
derive_cron_policy() {
  local policy=$1 server=$2 out=$3
  if [[ "$(grep -c '^mcp_servers:$' "$policy")" != 1 ]] || grep -q '^  montage:' "$policy" ||
    [[ "$(grep -c '^  montage:$' "$server")" != 1 ]]; then
    echo "the assistant's policy and the montage server do not fit together" >&2
    return 1
  fi
  sed "/^mcp_servers:\$/r $server" "$policy" >"$out"
  chmod 0644 "$out"
}

# derive_assistant_units DIR: anas-assistant-cron@.service and
# anas-assistant-curator@.service in DIR, from DIR/anas-assistant@.service.
derive_assistant_units() {
  local units=$1 lib=/usr/lib/a-nas/assistant
  sed -e 's#^Description=.*#Description=Scheduled tasks of the A-NAS assistant of %i#' \
    -e '\#a-nas-companion-assistant#d' \
    -e '/^ExecStart=/i Environment=ANAS_ASSISTANT_CHANNEL=cron ANAS_ASSISTANT_RUNS=/run/anas-assistant-run-now/%i' \
    -e "/^ExecStart=/i ExecStartPre=+/bin/bash $lib/run-requests.sh take %i" \
    -e "/^ExecStart=/a ExecStopPost=+/bin/bash $lib/run-requests.sh done %i" \
    -e '/^ExecStartPost=/d' -e '/^StopWhenUnneeded/d' -e 's/^Type=.*/Type=oneshot/' \
    -e 's/^TimeoutStartSec=.*/TimeoutStartSec=1h/' \
    -e "s#HERMES_MANAGED_DIR=$lib/managed\\b#HERMES_MANAGED_DIR=$lib/managed-cron#" \
    -e 's#members/%i/config.yaml:#members/%i/config-cron.yaml:#' \
    -e '\#anas-assistant-photos/photos.sock#a BindReadOnlyPaths=-/run/a-nas-montage-assistant/assistant.sock:/run/anas-assistant-montage/montage.sock' \
    "$units/anas-assistant@.service" >"$units/anas-assistant-cron@.service"
  sed -e 's#^Description=.*#Description=Hermes curator for the A-NAS assistant of %i#' \
    -e '\#a-nas-companion-assistant#d' \
    -e "s#^ExecStart=.*#ExecStart=/usr/bin/tini -- $lib/runtime/venv/bin/hermes curator run#" \
    -e "/^ExecStart=/a ExecStartPost=$lib/runtime/venv/bin/python -P $lib/media-cache.py" \
    -e '/^ExecStartPost=/d' -e '/^StopWhenUnneeded/d' -e 's/^Type=.*/Type=oneshot/' \
    -e "/^\[Service\]/a ExecCondition=+/bin/sh -c '! systemctl is-active --quiet anas-assistant@%i.service'" \
    "$units/anas-assistant@.service" >"$units/anas-assistant-curator@.service"
  chmod 0644 "$units/anas-assistant-cron@.service" "$units/anas-assistant-curator@.service"
}
