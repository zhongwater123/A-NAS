# shellcheck shell=bash
# The AI assistant's units that run in the sandbox of anas-assistant@.service
# (ADR 0024), made from it so that they stay the same: a round of the member's
# scheduled tasks and the weekly curator, each a oneshot, and the member's
# planning instance for the montage center with the socket that starts it
# (docs/architecture/montage-planning-session.md). None of them has the
# companion's socket, which only a conversation's window answers (#412). The
# planning instance alone binds the montage center's socket, reads the policy
# and the member settings that add its planning server (managed-plan,
# config-plan.yaml) and gives the model the montage, memory and skill tools
# alone. The curator also prunes Hermes's image cache, which serve never does
# (ai/assistant/media-cache.py). scripts/build-packages.sh and the spike's
# installer (ai/assistant-spike/assistant-install.sh) source this file.

# derive_plan_policy POLICY SERVER OUT: the managed policy of a member's planning instance in
# OUT: POLICY, the policy of every other unit, with the montage center's planning server added
# (the fragment of mcp_servers in SERVER) and a background review after every planning
# (skills.creation_nudge_interval: 1), which writes what VNAS learned as skills. The text is
# spliced, which keeps the policy's comments; internal/hostops/linux reads the result as YAML in
# its tests, where a key written twice or a fragment out of place fails.
derive_plan_policy() {
  local policy=$1 server=$2 out=$3
  if [[ "$(grep -c '^mcp_servers:$' "$policy")" != 1 || "$(grep -c '^skills:$' "$policy")" != 1 ]] ||
    grep -q '^  montage:' "$policy" || grep -q 'creation_nudge_interval' "$policy" ||
    [[ "$(grep -c '^  montage:$' "$server")" != 1 ]]; then
    echo "the assistant's policy and the montage server do not fit together" >&2
    return 1
  fi
  sed -e "/^mcp_servers:\$/r $server" \
    -e '/^skills:$/a\  # Every planning is reviewed for what VNAS learned (docs/architecture/montage-planning-session.md).\n  creation_nudge_interval: 1' \
    "$policy" >"$out"
  chmod 0644 "$out"
}

# derive_assistant_units DIR: anas-assistant-cron@.service, anas-assistant-curator@.service,
# anas-assistant-plan@.service and anas-assistant-planapi@.socket/.service in DIR, from
# DIR/anas-assistant@.service and DIR/anas-assistant-api@.socket/.service.
derive_assistant_units() {
  local units=$1 lib=/usr/lib/a-nas/assistant
  sed -e 's#^Description=.*#Description=Scheduled tasks of the A-NAS assistant of %i#' \
    -e '\#a-nas-companion-assistant#d' \
    -e '/^ExecStart=/i Environment=ANAS_ASSISTANT_CHANNEL=cron ANAS_ASSISTANT_RUNS=/run/anas-assistant-run-now/%i' \
    -e "/^ExecStart=/i ExecStartPre=+/bin/bash $lib/run-requests.sh take %i" \
    -e "/^ExecStart=/a ExecStopPost=+/bin/bash $lib/run-requests.sh done %i" \
    -e '/^ExecStartPost=/d' -e '/^StopWhenUnneeded/d' -e 's/^Type=.*/Type=oneshot/' \
    -e 's/^TimeoutStartSec=.*/TimeoutStartSec=1h/' \
    "$units/anas-assistant@.service" >"$units/anas-assistant-cron@.service"
  sed -e 's#^Description=.*#Description=Hermes curator for the A-NAS assistant of %i#' \
    -e '\#a-nas-companion-assistant#d' \
    -e "s#^ExecStart=.*#ExecStart=/usr/bin/tini -- $lib/runtime/venv/bin/hermes curator run#" \
    -e "/^ExecStart=/a ExecStartPost=/usr/bin/tini -- $lib/runtime/venv/bin/python -P $lib/media-cache.py" \
    -e '/^ExecStartPost=/d' -e '/^StopWhenUnneeded/d' -e 's/^Type=.*/Type=oneshot/' \
    -e "/^\[Service\]/a ExecCondition=+/bin/sh -c '! systemctl is-active --quiet anas-assistant@%i.service'" \
    "$units/anas-assistant@.service" >"$units/anas-assistant-curator@.service"
  # The planning instance: a second `hermes serve` on port 9120 of the same sandbox, Hermes
  # home and network, started by its own socket for root's planner and stopped once that
  # bridge exits idle, as the member's instance is by anas-assistant-api@.
  sed -e 's#^Description=.*#Description=A-NAS assistant (VNAS) of %i, planning for the montage center#' \
    -e '\#a-nas-companion-assistant#d' \
    -e '/^Environment=HOME=/a Environment=ANAS_ASSISTANT_CHANNEL=plan HERMES_TUI_TOOLSETS=montage,memory,skills' \
    -e "s#HERMES_MANAGED_DIR=$lib/managed\\b#HERMES_MANAGED_DIR=$lib/managed-plan#" \
    -e 's#members/%i/config.yaml:#members/%i/config-plan.yaml:#' \
    -e 's#wait-port.sh 9119#wait-port.sh 9120#' \
    -e '\#anas-assistant-photos/photos.sock#a BindReadOnlyPaths=-/run/a-nas-videocut-assistant/assistant.sock:/run/anas-assistant-videocut/videocut.sock' \
    "$units/anas-assistant@.service" >"$units/anas-assistant-plan@.service"
  sed -e 's#^Description=.*#Description=A-NAS assistant planning socket for %i#' \
    -e 's#/run/a-nas-assistant/#/run/a-nas-assistant-plan/#' \
    "$units/anas-assistant-api@.socket" >"$units/anas-assistant-planapi@.socket"
  sed -e 's#^Description=.*#Description=A-NAS assistant planning bridge for %i#' \
    -e 's#anas-assistant@%i\.service#anas-assistant-plan@%i.service#g' \
    -e 's#anas-assistant-api@%i\.socket#anas-assistant-planapi@%i.socket#g' -e 's#9119#9120#g' \
    "$units/anas-assistant-api@.service" >"$units/anas-assistant-planapi@.service"
  # A template that changed under these edits fails here, not on the device.
  local unit line
  for unit in plan@.service:HERMES_TUI_TOOLSETS=montage,memory,skills plan@.service:HERMES_MANAGED_DIR=$lib/managed-plan \
    plan@.service:members/%i/config-plan.yaml: plan@.service:'wait-port.sh 9120' plan@.service:a-nas-videocut-assistant/assistant.sock \
    planapi@.socket:ListenStream=/run/a-nas-assistant-plan/%i.sock planapi@.service:anas-assistant-plan@%i.service \
    planapi@.service:127.0.0.1:9120; do
    line=${unit#*:}
    grep -Fq -- "$line" "$units/anas-assistant-${unit%%:*}" || { echo "anas-assistant-${unit%%:*} lacks $line" >&2; return 1; }
  done
  # Under PrivatePIDs= a command not run under tini ignores the stop's SIGTERM (the unit's
  # comment); commands run as root (+) are outside its namespace.
  for unit in "$units"/anas-assistant{,-cron,-curator,-plan}@.service; do
    if grep -E '^Exec[A-Za-z]+=[^+]' "$unit" | grep -Ev '^Exec[A-Za-z]+=/usr/bin/tini -- ' | grep -q .; then
      echo "${unit##*/} runs a command without tini under PrivatePIDs=" >&2
      return 1
    fi
  done
  if grep -q 'a-nas-companion-assistant\|members/%i/config.yaml:' "$units/anas-assistant-plan@.service" ||
    grep -q 'a-nas-videocut-assistant' "$units/anas-assistant-cron@.service" "$units/anas-assistant-curator@.service"; then
    echo "a unit made from anas-assistant@.service binds what it must not" >&2
    return 1
  fi
  chmod 0644 "$units"/anas-assistant-{cron,curator,plan}@.service "$units"/anas-assistant-planapi@.{socket,service}
}
