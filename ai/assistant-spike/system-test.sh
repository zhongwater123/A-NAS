# shellcheck shell=bash
# shellcheck disable=SC2154 # api, data, the accounts and the helpers come from scripts/system-test.sh
# Sourced by scripts/system-test.sh after its own checks: the AI assistant's sandbox
# (ai/assistant-spike, ADR 0024, #115) on the accounts, spaces and ACLs the real installer
# and Host Agent made, instead of the spike's hand-made layout. assistant-install.sh and
# assistant-layout.sh play the Host Agent's part for what it does not do yet.
#
# Without the Hermes runtime only the sandbox, the change broker and the layout are
# checked. With ANAS_SYSTEM_TEST_ASSISTANT_RUNTIME naming the spike's runtime.tar.gz
# (ai/assistant-spike/run.sh build), the probe also runs toolsets-check.py and alice's
# assistant runs `hermes serve` against the scripted fake model.
#
# scripts/system-test.sh runs with set -e: every command that may fail here is a check or
# ends in || true.

ln -sfn /src/ai/assistant-spike /spike # the units and the managed policy name /spike
if [[ -f /assistant-runtime.tar.gz ]]; then
  install -d /opt/a-nas/assistant-runtimes
  tar -C /opt/a-nas/assistant-runtimes -xzf /assistant-runtime.tar.gz
fi
assistant_python=/opt/a-nas/assistant-runtimes/spike/venv/bin/python

# --- A second member, created as every account is: through the Product Service.
bob_password=$(secret)
curl -fsS -o /dev/null -b /tmp/owner.jar -H "X-CSRF-Token: $owner_csrf" -H 'Content-Type: application/json' \
  -d "{\"username\":\"bob\",\"password\":\"$bob_password\"}" "$api/api/v1/users" || true
bob_csrf=$(login bob "$bob_password" /tmp/bob.jar || true)
alice_csrf=$(login alice "$new_password" /tmp/alice.jar || true)
for _ in $(seq 1 100); do [[ -d "$data/spaces/private/bob/.a-nas-trash/bob" ]] && break; sleep 0.2; done
check "the Host Agent made bob's space and recycle bins" \
  test -d "$data/spaces/private/bob/.a-nas-trash/bob" -a -d "$data/spaces/shared/.a-nas-trash/bob"

# --- Installed beside the real accounts, with the Host Agent's File Broker as the session
# gate (ADR 0024 decision 10) and the executor of the assistant's changes: only root reaches
# the instance sockets and the session tokens; the Product Service reaches a member's instance,
# and prepares or grants that member's changes, only with the member's live session.
if ASSISTANT_GATE=host-agent PRODUCT_USER=a-nas bash /spike/assistant-install.sh owner alice bob >/tmp/assistant-install.log 2>&1; then
  check "the assistant installs beside the real accounts" true
else
  check "the assistant installs beside the real accounts" false
  tail -n 20 /tmp/assistant-install.log
fi
check "the members write the probe's files into their spaces" quiet bash /spike/seed.sh "$data"
# A real snapshot of alice's space, which the sandbox must not show.
curl -fsS -o /dev/null -b /tmp/alice.jar -H "X-CSRF-Token: $alice_csrf" -H 'Content-Type: application/json' \
  -d '{"name":"assistant-probe"}' "$api/api/v1/spaces/$alice_private/snapshots" || true
check "a snapshot of alice's space exists on the volume" test -n "$(ls -A "$data/.a-nas-snapshots" 2>/dev/null)"
check "only root may connect to an instance socket" \
  test "$(stat -c '%a %U' /run/a-nas-assistant/alice.sock 2>/dev/null)" = "600 root"
connect_alice='import socket; socket.socket(socket.AF_UNIX).connect("/run/a-nas-assistant/alice.sock")'
check "a member cannot connect to an instance socket" denied as alice python3 -c "$connect_alice"
check "the Product Service cannot connect to one directly" denied as a-nas python3 -c "$connect_alice"
check "session tokens are root's alone" test "$(stat -c '%a %U' /etc/a-nas-assistant/tokens/alice.env 2>/dev/null)" = "600 root"
check "the Product Service holds no copy of the tokens" test ! -e /run/anas-spike-tokens/alice.token
check "each member's weekly curator is enabled" systemctl is-enabled --quiet anas-assistant-curator@alice.timer anas-assistant-curator@bob.timer
# The gate on, and the assistant's API in the Product Service (both off by default).
printf 'ANAS_ASSISTANT_SOCKET_DIR=/run/a-nas-assistant\n' >> /etc/a-nas/host-agent.env
printf 'ANAS_ASSISTANT_SOCKET_DIR=/run/a-nas-assistant\n' >> /etc/a-nas/anas-api.env
{ systemctl restart anas-host-agent && systemctl restart anas-api; } || true
for _ in $(seq 1 100); do curl -fsS "$api/healthz" >/dev/null 2>&1 && [[ -S /run/a-nas/assistant-changes.sock ]] && break; sleep 0.2; done
changes_socket='import socket; socket.socket(socket.AF_UNIX).connect("/run/a-nas/assistant-changes.sock")'
check "the executor's assistant socket is out of a member's reach outside the sandbox" denied as alice python3 -c "$changes_socket"
# The units bind it into each sandbox. To drive it as alice from outside one, the test binds
# it at the sandbox's path on the host too.
mount --bind /run/a-nas/assistant-changes.sock /run/anas-change-agent/agent.sock || true
export EXECUTOR=host-agent
alice_csrf=$(login alice "$new_password" /tmp/alice.jar || true)
bob_csrf=$(login bob "$bob_password" /tmp/bob.jar || true)
session_of() { awk '$6 == "anas_session" {print $7}' "$1"; }
check "alice's assistant is available through the Product Service" \
  test "$(curl -fsS -b /tmp/alice.jar "$api/api/v1/assistant/status" | jq -r .available)" = true
# The executor prepares, grants and shows changes only for the member whose live session the
# Product Service carries; no request names the account.
executor() { # SESSION METHOD PATH [JSON]: status and body
  BROKER_SESSION=$1 python3 /spike/broker_client.py product "$2" "$3" "${4:-}"
}
check "the executor refuses the Product Service without a session" \
  test "$(executor '' POST /changes/prepare '{"summary":"x","operations":[]}' | cut -d' ' -f1)" = 401
probe_change=$(executor "$(session_of /tmp/alice.jar)" POST /changes/prepare \
  '{"summary":"x","operations":[{"op":"mkdir","path":"/nas/个人空间/闸门"}]}' | cut -d' ' -f2- | jq -r .id || true)
check "it prepares alice's change with her session" test -n "$probe_change" -a "$probe_change" != null
check "another member's session does not see it" \
  test "$(executor "$(session_of /tmp/bob.jar)" GET "/changes/$probe_change" | cut -d' ' -f1)" = 404
check "nor grant it" test "$(executor "$(session_of /tmp/bob.jar)" POST "/changes/$probe_change/approve" | cut -d' ' -f1)" = 404
check "it keeps its copies where only root can read them" \
  test "$(stat -c '%a %U' "$data/assistant/.changes" 2>/dev/null)" = "700 root"
export BROKER_SESSION
BROKER_SESSION=$(session_of /tmp/alice.jar)

# --- probe.sh in the members' own sandboxes; its lines count as this test's.
# fold LABEL: the PASS/FAIL/INFO lines on stdin become this test's; folded counts the passes.
# Fed by redirection, never in a pipeline, so that failures is this shell's.
fold() {
  local line
  folded=0
  while IFS= read -r line; do
    case $line in
      PASS*) echo "PASS $1: ${line#PASS }"; folded=$((folded + 1)) ;;
      FAIL*) echo "FAIL $1: ${line#FAIL }"; failures=$((failures + 1)) ;;
      INFO*) echo "INFO $1: ${line#INFO }" ;;
    esac
  done
}
assistant_probe() { # ACCOUNT LEAST_PASSES
  systemctl start "anas-assistant-probe@$1.service" || true
  fold "assistant sandbox ($1)" < <(journalctl -u "anas-assistant-probe@$1.service" -o cat --no-pager | grep -E '^(PASS|FAIL|INFO)' || true)
  check "the probe ran in $1's sandbox" test "$folded" -ge "$2"
}
assistant_probe alice 33
# The administrator's assistant: the groups it carries, and that none of them is granted
# anything it can reach.
install -d /etc/systemd/system/anas-assistant-probe@owner.service.d
printf '[Service]\nEnvironment=PROBE_IDENTITY_ONLY=1\n' > /etc/systemd/system/anas-assistant-probe@owner.service.d/identity.conf
systemctl daemon-reload
assistant_probe owner 1

# --- What the member approves is applied as the member, with the broker's own checks.
fold "assistant broker" < <(bash /spike/approvals-broker.sh 2>&1 || true)
check "the broker's execution tests ran" test "$folded" -ge 15
# shellcheck disable=SC2329 # invoked through check
in_web_trash() { # JQ_CONDITION: alice's Web recycle bin holds an item matching it
  curl -fsS -b /tmp/alice.jar "$api/api/v1/trash" | jq -e "any(.items[]; $1)" >/dev/null
}
web_restore() { # JQ_CONDITION NAME: restores the first matching item under NAME; prints the status
  local id
  id=$(curl -fsS -b /tmp/alice.jar "$api/api/v1/trash" | jq -r "[.items[] | select($1)][0].id // empty" || true)
  status_of -b /tmp/alice.jar -H "X-CSRF-Token: $alice_csrf" -H 'Content-Type: application/json' \
    -d "{\"name\":\"$2\"}" "$api/api/v1/trash/$id/restore" || true
}
broker_apply() { # SUBMISSION: alice approves it and her assistant applies it
  local change
  change=$(python3 /spike/broker_client.py product POST '/changes/prepare?account=alice' "$1" | cut -d' ' -f2- | jq -r .id || true)
  python3 /spike/broker_client.py product POST "/changes/$change/approve?account=alice" >/dev/null || true
  as alice python3 /spike/broker_client.py agent POST /apply "$1" >/dev/null || true
}
stage() { # PATH TEXT: alice's assistant stages TEXT at PATH below /nas/助手/待确认
  local file=$data/assistant/alice/files/待确认/$1
  as alice mkdir -p "$(dirname "$file")" || true
  printf '%s\n' "$2" | as alice tee "$file" >/dev/null || true
}
check "a file the broker replaced is in the Web recycle bin under its own name, at the root" \
  in_web_trash '.name == "计划.md" and .originalPath == "" and .originalGone == false'
# A file in a folder alice made on the Web, replaced twice: both earlier copies keep the folder,
# the second named as Samba's recycle names it, and a restore puts it back there.
notes_id=$(curl -fsS -b /tmp/alice.jar -H "X-CSRF-Token: $alice_csrf" -H 'Content-Type: application/json' \
  -d '{"parentId":"","name":"笔记"}' "$api/api/v1/spaces/$alice_private/directories" | jq -r .id || true)
printf 'v1\n' > /tmp/a.md
curl -fsS -o /dev/null -b /tmp/alice.jar -H "X-CSRF-Token: $alice_csrf" -F "parentId=$notes_id" \
  -F 'file=@/tmp/a.md;filename=a.md' "$api/api/v1/spaces/$alice_private/uploads" || true
note='{"summary":"改笔记","operations":[{"op":"write","path":"/nas/个人空间/笔记/a.md","source":"/nas/助手/待确认/笔记/a.md"}]}'
stage 笔记/a.md v2
broker_apply "$note"
stage 笔记/a.md v3
broker_apply "$note"
check "the replaced note is the newest" grep -qx v3 "$data/spaces/private/alice/笔记/a.md"
check "its first copy is in the recycle bin, its folder still there" \
  in_web_trash '.name == "a.md" and .originalPath == "笔记" and .originalGone == false'
check "the second copy is named as Samba names it" \
  in_web_trash '.name == "Copy #1 of a.md" and .originalPath == "笔记" and .originalGone == false'
check "restoring the first copy on the Web puts it back in its folder" \
  test "$(web_restore '.name == "a.md"' a-v1.md)" = 200 -a "$(cat "$data/spaces/private/alice/笔记/a-v1.md" 2>/dev/null)" = v1
# A folder the broker made itself is not in the Web index until something lists it, so the
# recycle bin records it as gone and a restore lands at the space root. Recorded only: in
# A-NAS the change runs through the file service, which indexes what it creates.
stage 草稿/b.md v1
broker_apply '{"summary":"新草稿","operations":[{"op":"mkdir","path":"/nas/个人空间/草稿"},{"op":"write","path":"/nas/个人空间/草稿/b.md","source":"/nas/助手/待确认/草稿/b.md"}]}'
stage 草稿/b.md v2
broker_apply '{"summary":"改草稿","operations":[{"op":"write","path":"/nas/个人空间/草稿/b.md","source":"/nas/助手/待确认/草稿/b.md"}]}'
draft_item=$(curl -fsS -b /tmp/alice.jar "$api/api/v1/trash" | jq -c '[.items[] | select(.name == "b.md") | {originalPath, originalGone}][0]' || true)
draft_restore=$(web_restore '.name == "b.md"' b-v1.md)
echo "INFO assistant broker: replaced in a folder the broker made: $draft_item; restored (HTTP $draft_restore) to" \
  "$(cd "$data/spaces/private/alice" && find . -name b-v1.md -printf '%h ' 2>/dev/null)"
echo "INFO assistant broker: alice's Web recycle bin: $(curl -fsS -b /tmp/alice.jar "$api/api/v1/trash" |
  jq -c '[.items[] | {name, originalPath, originalGone}]' 2>/dev/null || true)"

# --- Agent空间 (ADR 0024 decision 5): everyone reads it, each member writes only their own
# folder, and the assistant only through the broker.
agent_space=$data/spaces/shared/Agent空间
stage 公告.md 周日大扫除
broker_apply '{"summary":"发布公告","operations":[{"op":"write","path":"/nas/共享空间/Agent空间/alice/公告.md","source":"/nas/助手/待确认/公告.md"}]}'
check "alice's assistant publishes into her Agent空间 folder through the broker" \
  test "$(owner_uid "$agent_space/alice/公告.md" 2>/dev/null)" = "$alice_id"
check "every member reads what she published" quiet as bob cat "$agent_space/alice/公告.md"
check "another member cannot write into her folder" denied as bob touch "$agent_space/alice/x.md"
check "no member writes Agent空间 itself" denied as alice touch "$agent_space/x.md"
agent_space_id=$(curl -fsS -b /tmp/bob.jar "$api/api/v1/spaces/$shared/entries" | jq -r '.items[] | select(.name == "Agent空间") | .id' || true)
alice_folder_id=$(curl -fsS -b /tmp/bob.jar "$api/api/v1/spaces/$shared/entries?parentId=$agent_space_id" |
  jq -r '.items[] | select(.name == "alice") | .id' || true)
check "the Web lists her publication to other members" grep -qx 公告.md \
  <(curl -fsS -b /tmp/bob.jar "$api/api/v1/spaces/$shared/entries?parentId=$alice_folder_id" | jq -r '.items[].name' || true)
printf 'bob\n' > /tmp/bob.txt
check "another member cannot upload into her folder on the Web" test "$(status_of -b /tmp/bob.jar -H "X-CSRF-Token: $bob_csrf" \
  -F "parentId=$alice_folder_id" -F 'file=@/tmp/bob.txt;filename=x.txt' "$api/api/v1/spaces/$shared/uploads")" != 201
# Deleting moves it into bob's recycle bin, another folder: that needs write access to it.
check "another member cannot delete Agent空间 on the Web" \
  test "$(status_of -b /tmp/bob.jar -H "X-CSRF-Token: $bob_csrf" -X DELETE "$api/api/v1/files/$agent_space_id" || true)" != 200 -a -d "$agent_space"
# Renaming in place needs only write access to Shared. The product service is to refuse it on
# the Web (decided with #115); what the Web answers today is recorded.
renamed=$(status_of -b /tmp/bob.jar -H "X-CSRF-Token: $bob_csrf" -H 'Content-Type: application/json' -X PATCH \
  -d '{"name":"Agent空间-bob"}' "$api/api/v1/files/$agent_space_id" || true)
echo "INFO assistant Agent空间: another member renames it on the Web: HTTP $renamed"
# Over SMB the rename cannot be refused, and bob can then make his own Agent空间 with a folder
# for alice. The Host Agent's repair moves that impostor aside, with what he made inside it.
if [[ -d "$agent_space" ]]; then as bob mv -T "$agent_space" "$data/spaces/shared/Agent空间-bob" || true; fi
as bob mkdir -p "$agent_space/alice" || true
check "after a rename, another member can make an Agent空间 with a folder for alice" \
  test "$(stat -c %U "$agent_space/alice" 2>/dev/null)" = bob
ASSISTANT_GATE=host-agent PRODUCT_USER=a-nas bash /spike/assistant-layout.sh prepare "$data" owner alice bob >/dev/null 2>&1 || true
check "the repair moves the impostor aside with what was made inside it" \
  test -n "$(find "$data/spaces/shared" -mindepth 1 -maxdepth 1 -name 'Agent空间.displaced-*' -user bob)"
check "and puts Agent空间 and her folder back as root's" \
  test "$(stat -c %U "$agent_space" 2>/dev/null):$(stat -c %U "$agent_space/alice" 2>/dev/null)" = root:root
check "her folder is hers alone again" denied as bob touch "$agent_space/alice/y.md"
echo "INFO assistant Agent空间: Shared now holds $(find "$data/spaces/shared" -mindepth 1 -maxdepth 1 -name 'Agent空间*' -printf '%f ')"

# --- Assistant directories: one Btrfs subvolume per account, capped by simple quota.
check "each assistant directory is its own Btrfs subvolume" quiet btrfs subvolume show "$data/assistant/alice"
if quota_error=$(bash /spike/assistant-layout.sh quota "$data" 64M owner alice bob 2>&1); then
  as alice dd if=/dev/zero of="$data/assistant/alice/files/big" bs=1M count=100 status=none 2>/dev/null || true
  sync
  written=$(stat -c %s "$data/assistant/alice/files/big" 2>/dev/null || echo 0)
  check "an assistant directory stops at its quota" test "$written" -gt 0 -a "$written" -lt $((100 * 1024 * 1024))
  echo "INFO assistant quota: $(btrfs qgroup show -re "$data" 2>/dev/null | grep -c '^0/') qgroups; alice wrote $written bytes"
  rm -f "$data/assistant/alice/files/big"
  check "snapshots still work with quotas on the volume" test "$(status_of -b /tmp/alice.jar -H "X-CSRF-Token: $alice_csrf" \
    -H 'Content-Type: application/json' -d '{"name":"after-quota"}' "$api/api/v1/spaces/$alice_private/snapshots")" = 201
  printf 'after quota %s\n' "$(secret)" > /tmp/after-quota.txt
  check "Web uploads still work with quotas on the volume" \
    test -n "$(upload /tmp/alice.jar "$alice_csrf" "$alice_private" /tmp/after-quota.txt after-quota.txt || true)"
else
  echo "INFO assistant quota: simple quota unavailable on kernel $(uname -r): ${quota_error##*$'\n'}"
fi

# --- With the runtime: alice's assistant itself, as alice, through the Product Service's socket.
if [[ -x "$assistant_python" ]]; then
  serve_out=$("$assistant_python" /spike/serve_client.py alice chat hello 'CALL read_file {"path": "/nas/个人空间/计划.md"}' 2>&1 || true)
  check "alice's assistant answers over its socket" grep -qx '< ok' <<<"$serve_out"
  check "it reads her own file" grep -q 买菜 <<<"$serve_out"
  check "it runs under alice's own UID" \
    test "$(awk '/^Uid:/ {print $2}' "/proc/$(systemctl show -p MainPID --value anas-assistant@alice.service)/status" 2>/dev/null)" = "$alice_id"
  check "the first model request offers the A-NAS MCP tool" \
    bash -c "grep -m 1 -F '\"tools\": [\"' /var/log/fake-model.jsonl | grep -q mcp__anas__submit_changes"
  # The window's way in: the Product Service, then the gate, then alice's instance.
  listed_at=$(date '+%Y-%m-%d %H:%M:%S')
  listed=$(curl -s -o /tmp/gate-sessions.json -w '%{http_code}' -b /tmp/alice.jar "$api/api/v1/assistant/sessions" || true)
  check "alice lists her conversations through the gate" test "$listed" = 200
  if [[ "$listed" != 200 ]]; then
    echo "INFO assistant gate: sessions answered $listed $(head -c 300 /tmp/gate-sessions.json)"
    journalctl -u anas-api -u anas-host-agent --since "$listed_at" -o cat --no-pager | grep -i assistant | tail -n 5 |
      sed 's/^/INFO assistant gate: /' || true
  fi
  # The whole chain: Hermes asks to run 提交更改, the Product Service shows the card, alice
  # confirms it from her session, and the Host Agent's executor writes as alice through her
  # confined worker while the assistant waits.
  stage 计划.md "$(printf '# 计划\n- 买菜\n- 经网关确认\n')"
  submission='{"summary":"经网关加一项","operations":[{"op":"write","path":"/nas/个人空间/计划.md","source":"/nas/助手/待确认/计划.md"}]}'
  "$assistant_python" -u /spike/gate_client.py "ws://127.0.0.1:8080/api/v1/assistant/ws" "$(session_of /tmp/alice.jar)" \
    submit "$alice_csrf" "$submission" >/tmp/gate-submit.out 2>&1 || true
  check "a turn's call of 提交更改 becomes a card in the window" grep -q '^card awaiting: 经网关加一项' /tmp/gate-submit.out
  check "alice's confirmation is accepted" grep -qx 'confirm answered 200' /tmp/gate-submit.out
  check "the executor applies it" grep -qx 'card applied: 经网关加一项' /tmp/gate-submit.out
  check "and the assistant hears so" bash -c "sed -n '/^answer:/,\$p' /tmp/gate-submit.out | grep -q '1 项更改已执行'"
  check "her plan has the confirmed line, written as her" \
    test "$(grep -c 经网关确认 "$data/spaces/private/alice/计划.md" 2>/dev/null):$(owner_uid "$data/spaces/private/alice/计划.md")" = "1:$alice_id"
  echo "INFO assistant gate: $(tr '\n' ';' </tmp/gate-submit.out | cut -c1-400)"
  gate_session=$(curl -fsS -c /tmp/alice-gate.jar -H 'Content-Type: application/json' \
    -d "{\"username\":\"alice\",\"password\":\"$new_password\"}" "$api/api/v1/session" | jq -r .csrfToken || true)
  "$assistant_python" -u /spike/gate_client.py "ws://127.0.0.1:8080/api/v1/assistant/ws" "$(session_of /tmp/alice-gate.jar)" 90 \
    >/tmp/gate.out 2>&1 &
  gate_pid=$!
  for _ in $(seq 1 300); do grep -q 'session created' /tmp/gate.out 2>/dev/null && break; sleep 0.5; done
  check "the window's gateway reaches alice's instance" grep -q '^ready after' /tmp/gate.out
  check "it refuses a method outside the allowlist" grep -qx 'shell.exec error -32601' /tmp/gate.out
  check "and a profile parameter" grep -qx 'profile error -32602' /tmp/gate.out
  check "a new conversation comes back without the system prompt" grep -qx 'session created True; system prompt shown False' /tmp/gate.out
  # Signing out ends the session; the gate cuts the connection at its next check.
  check "she signs out" test "$(status_of -b /tmp/alice-gate.jar -H "X-CSRF-Token: $gate_session" -X DELETE "$api/api/v1/session")" = 204
  wait "$gate_pid" || true
  # The File Broker checks the session every 30 seconds.
  check "signing out cuts the window's connection to the instance" \
    test "$(sed -n 's/^closed after \([0-9]*\) s.*/\1/p' /tmp/gate.out)" -le 40
  echo "INFO assistant gate: $(tr '\n' ';' </tmp/gate.out)"
  # The Product Service keeps a member's assistant running while they are signed in: alice is
  # still signed in with /tmp/alice.jar (above). Once she signs out everywhere it exits idle;
  # signing in starts it in the background, with no window open, and keeps it running.
  check "she signs out of her last session" \
    test "$(status_of -b /tmp/alice.jar -H "X-CSRF-Token: $alice_csrf" -X DELETE "$api/api/v1/session")" = 204
  for _ in $(seq 1 180); do systemctl is-active --quiet anas-assistant@alice.service || break; sleep 0.5; done
  check "once she is signed out everywhere her assistant exits idle" test "$(systemctl is-active anas-assistant@alice.service || true)" != active
  keep_csrf=$(login alice "$new_password" /tmp/alice-keep.jar || true)
  for _ in $(seq 1 120); do systemctl is-active --quiet anas-assistant@alice.service && break; sleep 0.5; done
  check "signing in starts her assistant with no window open" systemctl is-active --quiet anas-assistant@alice.service
  sleep 50
  check "it keeps running while she is signed in, past the idle exit" systemctl is-active --quiet anas-assistant@alice.service
  check "she signs out again" \
    test "$(status_of -b /tmp/alice-keep.jar -H "X-CSRF-Token: $keep_csrf" -X DELETE "$api/api/v1/session")" = 204
  # Hermes's curator as the weekly timer starts it: in alice's sandbox, only while her
  # assistant is not running.
  systemctl stop anas-assistant-api@alice.service anas-assistant@alice.service 2>/dev/null || true
  systemctl start anas-assistant-curator@alice.service || true
  check "Hermes's curator runs in her sandbox while her assistant is stopped" \
    test "$(systemctl show -p Result --value anas-assistant-curator@alice.service)" = success -a -s "$data/assistant/alice/hermes/skills/.curator_state"
  echo "INFO assistant curator: $(journalctl -u anas-assistant-curator@alice.service -o cat --no-pager | grep -m 1 '^auto:' || true)"
  # The system test's last check uses /tmp/alice.jar.
  alice_csrf=$(login alice "$new_password" /tmp/alice.jar || true)
fi

# --- The layout, for comparison with the spike's (layout-acl.sh in both).
while IFS= read -r line; do echo "INFO assistant layout: $line"; done < <(bash /spike/layout-acl.sh "$data" alice bob || true)

# --- Deleting an account removes its assistant (Agent空间 and recycle bins stay for #26).
PRODUCT_USER=a-nas bash /spike/assistant-layout.sh remove "$data" bob || true
check "removing bob's assistant leaves no token, config, socket or directory" \
  test ! -e /etc/a-nas-assistant/tokens/bob.env -a ! -e /run/anas-spike-tokens/bob.token -a ! -e /etc/a-nas-assistant/members/bob \
  -a ! -e /run/a-nas-assistant/bob.sock -a ! -e "$data/assistant/bob"
check "and his curator timer is off" test "$(systemctl is-enabled anas-assistant-curator@bob.timer 2>/dev/null || true)" != enabled
