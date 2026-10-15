# shellcheck shell=bash
# shellcheck disable=SC2154 # api, data, the accounts and the helpers come from scripts/system-test.sh
# Sourced by scripts/system-test.sh after its own checks: the AI assistant's sandbox
# (ai/assistant-spike, ADR 0022, #115) on the accounts, spaces and ACLs the real installer
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

# --- Installed beside the real accounts; only the Product Service account reaches the
# instance sockets, the broker's product socket and the copies of the session tokens.
if PRODUCT_USER=a-nas bash /spike/assistant-install.sh owner alice bob >/tmp/assistant-install.log 2>&1; then
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
check "only the Product Service account owns an instance socket" \
  test "$(stat -c '%a %U' /run/a-nas-assistant/alice.sock 2>/dev/null)" = "600 a-nas"
check "a member cannot connect to an instance socket" denied as alice python3 -c \
  'import socket; socket.socket(socket.AF_UNIX).connect("/run/a-nas-assistant/alice.sock")'
check "session tokens are root's alone" test "$(stat -c '%a %U' /etc/a-nas-assistant/tokens/alice.env 2>/dev/null)" = "600 root"
check "the Product Service holds its own copy of each token" \
  test "$(stat -c '%a %U' /run/anas-spike-tokens/alice.token 2>/dev/null)" = "600 a-nas"

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
# shellcheck disable=SC2329 # invoked through check
in_web_trash() { # JQ_CONDITION: alice's Web recycle bin holds an item matching it
  curl -fsS -b /tmp/alice.jar "$api/api/v1/trash" | jq -e "any(.items[]; $1)" >/dev/null
}
check "a file the broker replaced is in the Web recycle bin under its own name and folder" \
  in_web_trash '.name == "计划.md" and .originalPath == ""'
# A file in a folder replaced twice: both earlier copies keep the folder, the second one
# named as Samba's recycle names it.
as alice mkdir -p "$data/spaces/private/alice/笔记" "$data/assistant/alice/files/待确认/笔记" || true
as alice sh -c "printf 'v1\n' > '$data/spaces/private/alice/笔记/a.md'" || true
replace_note() { # TEXT
  local note='{"summary":"改笔记","operations":[{"op":"write","path":"/nas/个人空间/笔记/a.md","source":"/nas/助手/待确认/笔记/a.md"}]}' change
  as alice sh -c "printf '%s\n' '$1' > '$data/assistant/alice/files/待确认/笔记/a.md'" || true
  change=$(python3 /spike/broker_client.py product POST '/changes/prepare?account=alice' "$note" | cut -d' ' -f2- | jq -r .id || true)
  python3 /spike/broker_client.py product POST "/changes/$change/approve?account=alice" >/dev/null || true
  as alice python3 /spike/broker_client.py agent POST /apply "$note" >/dev/null || true
}
replace_note v2
replace_note v3
check "the replaced note is the newest" grep -qx v3 "$data/spaces/private/alice/笔记/a.md"
check "its first copy is in the recycle bin in its folder" in_web_trash '.name == "a.md" and (.originalPath | test("笔记"))'
check "the second copy is named as Samba names it" in_web_trash '.name == "Copy #1 of a.md" and (.originalPath | test("笔记"))'
echo "INFO assistant broker: alice's Web recycle bin: $(curl -fsS -b /tmp/alice.jar "$api/api/v1/trash" |
  jq -c '[.items[] | {name, originalPath}]' 2>/dev/null || true)"

# --- Agent空间 (ADR 0022 decision 5): everyone reads it, each member writes only their own
# folder, and the assistant only through the broker.
agent_space=$data/spaces/shared/Agent空间
as alice sh -c "printf '周日大扫除\n' > '$data/assistant/alice/files/待确认/公告.md'" || true
publish='{"summary":"发布公告","operations":[{"op":"write","path":"/nas/共享空间/Agent空间/alice/公告.md","source":"/nas/助手/待确认/公告.md"}]}'
change=$(python3 /spike/broker_client.py product POST '/changes/prepare?account=alice' "$publish" | cut -d' ' -f2- | jq -r .id || true)
python3 /spike/broker_client.py product POST "/changes/$change/approve?account=alice" >/dev/null || true
as alice python3 /spike/broker_client.py agent POST /apply "$publish" >/dev/null || true
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
# Shared is writable for every member, so its entries can be renamed and deleted (moved to
# the member's recycle bin), Agent空间 included. Recorded here; see the research notes.
renamed=$(status_of -b /tmp/bob.jar -H "X-CSRF-Token: $bob_csrf" -H 'Content-Type: application/json' -X PATCH \
  -d '{"name":"Agent空间-bob"}' "$api/api/v1/files/$agent_space_id" || true)
echo "INFO assistant Agent空间: another member renames it on the Web: HTTP $renamed; now there: $(find "$data/spaces/shared" -mindepth 1 -maxdepth 1 -printf '%f ')"
if [[ -d "$data/spaces/shared/Agent空间-bob" ]]; then mv -T "$data/spaces/shared/Agent空间-bob" "$agent_space"; fi

# --- Assistant directories: one Btrfs subvolume per account, capped by simple quota.
check "each assistant directory is its own Btrfs subvolume" quiet btrfs subvolume show "$data/assistant/alice"
if quota_error=$(bash /spike/assistant-layout.sh quota "$data" 64M owner alice bob 2>&1); then
  as alice dd if=/dev/zero of="$data/assistant/alice/files/big" bs=1M count=100 status=none 2>/dev/null || true
  sync
  check "an assistant directory stops at its quota" \
    test "$(stat -c %s "$data/assistant/alice/files/big" 2>/dev/null || echo 0)" -lt $((100 * 1024 * 1024))
  echo "INFO assistant quota: $(btrfs qgroup show -re "$data" 2>/dev/null | grep -c '^0/') qgroups; alice wrote $(stat -c %s "$data/assistant/alice/files/big" 2>/dev/null || echo 0) bytes"
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
fi

# --- The layout, for comparison with the spike's (layout-acl.sh in both).
while IFS= read -r line; do echo "INFO assistant layout: $line"; done < <(bash /spike/layout-acl.sh "$data" alice bob || true)

# --- Deleting an account removes its assistant (Agent空间 and recycle bins stay for #26).
PRODUCT_USER=a-nas bash /spike/assistant-layout.sh remove "$data" bob || true
check "removing bob's assistant leaves no token, config, socket or directory" \
  test ! -e /etc/a-nas-assistant/tokens/bob.env -a ! -e /run/anas-spike-tokens/bob.token -a ! -e /etc/a-nas-assistant/members/bob \
  -a ! -e /run/a-nas-assistant/bob.sock -a ! -e "$data/assistant/bob"
