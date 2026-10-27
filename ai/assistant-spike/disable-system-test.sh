# shellcheck shell=bash
# shellcheck disable=SC2154 # sourced by scripts/system-test.sh after the assistant checks.
# A real systemd regression for #178, without needing the Hermes runtime.
# Keep a harmless process in the installed cron sandbox, then disable its owner
# through the Product Service and verify every activation path is closed.
host_socket=/run/a-nas/host-agent.sock
bob_uid=$(id -u bob)
check "the Host Agent identifies an active scheduled-task owner" \
  test "$(status_of --unix-socket "$host_socket" -H 'Content-Type: application/json' \
    -d "{\"uid\":$bob_uid}" http://host-agent/v1/accounts/resolve-uid)" = 200

install -d /etc/systemd/system/anas-assistant-cron@bob.service.d
cat > /etc/systemd/system/anas-assistant-cron@bob.service.d/disable-test.conf <<'EOF'
[Service]
ExecStart=
ExecStart=/bin/sleep infinity
TimeoutStartSec=0
EOF
systemctl daemon-reload
systemctl start --no-block anas-assistant-cron@bob.service || true
for _ in $(seq 1 50); do
  [[ "$(systemctl show -p MainPID --value anas-assistant-cron@bob.service)" != 0 ]] && break
  sleep 0.1
done
check "a task is running before its owner is disabled" \
  test "$(systemctl show -p MainPID --value anas-assistant-cron@bob.service)" -gt 0
check "disabling a task owner succeeds through the product API" \
  test "$(status_of -X DELETE -b /tmp/owner.jar -H "X-CSRF-Token: $owner_csrf" "$api/api/v1/users/$bob_user")" = 204
check "the Host Agent rejects the disabled scheduled-task owner" \
  test "$(status_of --unix-socket "$host_socket" -H 'Content-Type: application/json' \
    -d "{\"uid\":$bob_uid}" http://host-agent/v1/accounts/resolve-uid)" = 401
check "disabling stops the running task" \
  test "$(systemctl show -p MainPID --value anas-assistant-cron@bob.service)" = 0

for unit in anas-assistant@bob.service anas-assistant-api@bob.service anas-assistant-api@bob.socket \
  anas-assistant-cron@bob.service anas-assistant-curator@bob.service anas-assistant-curator@bob.timer; do
  check "a disabled account cannot restart $unit" denied systemctl start "$unit"
  check "$unit remains masked" test "$(systemctl is-enabled "$unit" 2>/dev/null || true)" = masked
done
check "updating member configuration preserves account disable" \
  env ASSISTANT_GATE=host-agent bash /spike/assistant-layout.sh members "$data" bob
check "updating does not reactivate the member socket" \
  test "$(systemctl is-enabled anas-assistant-api@bob.socket 2>/dev/null || true)" = masked
check "updating does not reactivate the curator timer" \
  test "$(systemctl is-enabled anas-assistant-curator@bob.timer 2>/dev/null || true)" = masked
