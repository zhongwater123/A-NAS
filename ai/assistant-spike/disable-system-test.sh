# shellcheck shell=bash
# shellcheck disable=SC2154 # sourced by scripts/system-test.sh after the assistant checks.
# A real systemd regression for #178, without needing the Hermes runtime.
# Keep a harmless process in the installed cron sandbox and the owner's instance
# running, then disable the owner through the Product Service while the instance
# keeps being started, and verify every activation path is closed.
check "bob's assistant is ready to start" test -S /run/a-nas-assistant/bob.sock
host_socket=/run/a-nas/host-agent.sock
bob_uid=$(id -u bob)
check "the Host Agent identifies an active scheduled-task owner" \
  test "$(status_of --unix-socket "$host_socket" -H 'Content-Type: application/json' \
    -d "{\"uid\":$bob_uid}" http://host-agent/v1/accounts/resolve-uid)" = 200

# The stand-ins run under tini, as the units' own programs do: with PrivatePIDs= a
# bare sleep is process 1 of its PID namespace and ignores SIGTERM, so each stop
# waited out TimeoutStopSec (90 s).
install -d /etc/systemd/system/anas-assistant-cron@bob.service.d
cat > /etc/systemd/system/anas-assistant-cron@bob.service.d/disable-test.conf <<'EOF'
[Service]
ExecStart=
ExecStart=/usr/bin/tini -- /bin/sleep infinity
TimeoutStartSec=0
EOF
systemctl daemon-reload
check "a task for bob is queued to start" systemctl start --no-block anas-assistant-cron@bob.service
for _ in $(seq 1 50); do
  [[ "$(systemctl show -p MainPID --value anas-assistant-cron@bob.service)" != 0 ]] && break
  sleep 0.1
done
check "a task is running before its owner is disabled" running anas-assistant-cron@bob.service

# Bob is signed in, so the Product Service keeps his instance running and starts
# it again through his socket. A start queued while the disable stopped the units
# replaced their stop jobs, and the disable failed (docs/investigations/
# 2026-11-03-assistant-disable-start-race.md). His instance stops in two seconds
# here, and starts keep coming during the disable, through the socket as the
# keeper does and directly, so one always lands while its stop job runs.
install -d /etc/systemd/system/anas-assistant@bob.service.d
cat > /etc/systemd/system/anas-assistant@bob.service.d/disable-test.conf <<'EOF'
[Unit]
StopWhenUnneeded=no
[Service]
ExecStart=
ExecStart=/usr/bin/tini -- /bin/sleep infinity
ExecStartPost=
ExecStop=/bin/sleep 2
EOF
systemctl daemon-reload
check "bob's assistant restarts with the test's slow stop" systemctl restart anas-assistant@bob.service
check "bob's assistant is running before he is disabled" systemctl is-active --quiet anas-assistant@bob.service
(
  while :; do
    curl -s -o /dev/null -m 1 --unix-socket /run/a-nas-assistant/bob.sock http://assistant/ || true
    systemctl start --no-block anas-assistant@bob.service 2>/dev/null || true
    sleep 0.1
  done
) &
restarts=$!
check "bob has a planning grant for VideoCut before he is disabled" test -f /var/lib/a-nas-host/assistant/plans/bob
check "disabling a task owner succeeds through the product API" \
  test "$(status_of -X POST -b /tmp/owner.jar -H "X-CSRF-Token: $owner_csrf" "$api/api/v1/users/$bob_user/disable")" = 204
check "disabling him removed his planning grant, and nobody else's" \
  test ! -e /var/lib/a-nas-host/assistant/plans/bob -a -f /var/lib/a-nas-host/assistant/plans/alice
kill "$restarts"
wait "$restarts" 2>/dev/null || true
check "disabling stops his assistant although it kept being started" \
  test "$(systemctl show -p MainPID --value anas-assistant@bob.service)" = 0
check "disabling him again only retries and succeeds" \
  test "$(status_of -X POST -b /tmp/owner.jar -H "X-CSRF-Token: $owner_csrf" "$api/api/v1/users/$bob_user/disable")" = 204
check "the Host Agent rejects the disabled scheduled-task owner" \
  test "$(status_of --unix-socket "$host_socket" -H 'Content-Type: application/json' \
    -d "{\"uid\":$bob_uid}" http://host-agent/v1/accounts/resolve-uid)" = 401
check "disabling stops the running task" \
  test "$(systemctl show -p MainPID --value anas-assistant-cron@bob.service)" = 0

for unit in anas-assistant@bob.service anas-assistant-api@bob.service anas-assistant-api@bob.socket \
  anas-assistant-cron@bob.service anas-assistant-curator@bob.service anas-assistant-curator@bob.timer \
  anas-assistant-plan@bob.service anas-assistant-planapi@bob.service anas-assistant-planapi@bob.socket \
  anas-assistant-net@bob.service; do
  check "a disabled account cannot restart $unit" fails systemctl start "$unit"
  check "$unit remains masked" test "$(systemctl is-enabled "$unit" 2>/dev/null || true)" = masked
done
# Saving the key prepares every active member's assistant, and only those.
check "saving the key again keeps a disabled account's assistant off" \
  test "$(save_key /tmp/owner.jar "$owner_csrf" true)" = 200
check "it does not reactivate the member socket" \
  test "$(systemctl is-enabled anas-assistant-api@bob.socket 2>/dev/null || true)" = masked
check "nor the socket of his planning instance" \
  test "$(systemctl is-enabled anas-assistant-planapi@bob.socket 2>/dev/null || true)" = masked
check "nor the curator timer" \
  test "$(systemctl is-enabled anas-assistant-curator@bob.timer 2>/dev/null || true)" = masked

# --- Deleting the disabled member removes his assistant for good: its folder, secret and
# settings go, and its units lose their masks, so an account given the name later starts clean.
rm -rf /etc/systemd/system/anas-assistant-cron@bob.service.d /etc/systemd/system/anas-assistant@bob.service.d
systemctl daemon-reload
check "the administrator deletes bob" test "$(status_of -X PUT -b /tmp/owner.jar -H "X-CSRF-Token: $owner_csrf" \
  -H 'Content-Type: application/json' -d "{\"password\":\"$owner_password\",\"personalSpace\":\"delete\",\"library\":\"delete\"}" \
  "$api/api/v1/users/$bob_user/deletion")" = 200
for _ in $(seq 1 120); do getent passwd bob >/dev/null || break; sleep 0.5; done
check "bob is removed once his time is up" bash -c '! getent passwd bob'
check "his assistant leaves no secret, settings, socket or folder" \
  test ! -e /var/lib/a-nas-host/assistant/tokens/bob.env -a ! -e /var/lib/a-nas-host/assistant/members/bob \
  -a ! -e /run/a-nas-assistant/bob.sock -a ! -e /run/a-nas-assistant-plan/bob.sock -a ! -e "$data/assistant/bob"
check "and his units are no longer masked" \
  test "$(systemctl is-enabled anas-assistant-curator@bob.timer 2>/dev/null || true)" != masked

# --- Deleting the key turns VNAS off for everyone and keeps what their assistants hold;
# saving it again brings each back.
check "the administrator deletes the key" \
  test "$(status_of -X DELETE -b /tmp/owner.jar -H "X-CSRF-Token: $owner_csrf" "$api/api/v1/assistant-key")" = 200
check "without the key the model gateway, the waker and VideoCut's planner stop" \
  test "$(systemctl is-active anas-assistant-gateway.service anas-assistant-cron-waker.service anas-videocut-planner.service |
    sort -u | paste -sd ' ' || true)" = inactive
check "alice's assistant is off" test ! -S /run/a-nas-assistant/alice.sock
check "and alice is told why" \
  test "$(curl -fsS -b /tmp/alice.jar "$api/api/v1/assistant/status" | jq -r .reason || true)" = no_key
# shellcheck disable=SC2317,SC2329 # invoked through check (SC2317 in older shellcheck)
start_without_key() {
  systemctl start anas-assistant-cron@alice.service &&
    test "$(systemctl show -p ConditionResult --value anas-assistant-cron@alice.service)" = no
}
check "a start that still arrives runs none of her assistant" start_without_key
check "her folder and instance secret stay" \
  test -d "$data/assistant/alice/hermes" -a -f /var/lib/a-nas-host/assistant/tokens/alice.env
check "the administrator saves the key again" test "$(save_key /tmp/owner.jar "$owner_csrf" true)" = 200
for _ in $(seq 1 100); do [[ -S /run/a-nas-assistant/alice.sock ]] && break; sleep 0.2; done
check "and alice has VNAS again" \
  test "$(curl -fsS -b /tmp/alice.jar "$api/api/v1/assistant/status" | jq -r .available || true)" = true
