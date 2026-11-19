# shellcheck shell=bash
# shellcheck disable=SC2154 # api, data, the accounts and the helpers come from scripts/system-test.sh
# Sourced by scripts/system-test.sh after its own checks: the AI assistant (ADR 0024) as the
# a-nas package installs it and the Host Agent prepares it, on the accounts, spaces and ACLs
# the real installer made. The administrator saves a key in 设置 → VNAS, and the model gateway
# sends what would go to DeepSeek to the scripted fake model here. The probe, the clients and
# the fake model are the spike's (this directory).
#
# The package's Hermes runtime is a stand-in here, so without more only the sandbox, the
# executor and the layout are checked. With ANAS_SYSTEM_TEST_ASSISTANT_RUNTIME naming the
# runtime.tar.gz ai/assistant/runtime.json pins (ai/assistant-spike/run.sh build), the real
# runtime is unpacked over the stand-in: the probe also runs toolsets-check.py and alice's
# assistant runs `hermes serve` against the scripted model.
#
# scripts/system-test.sh runs with set -e: every command that may fail here is a check or
# ends in || true.

ln -sfn /src/ai/assistant-spike /spike # the spike's probe, clients and scripted model
real_runtime=false
if [[ -f /assistant-runtime.tar.gz ]]; then
  tar -C /opt/a-nas/assistant-runtimes -xzf /assistant-runtime.tar.gz
  real_runtime=true
fi
assistant_python=/usr/lib/a-nas/assistant/runtime/venv/bin/python

# --- A second member, created as every account is: through the Product Service.
bob_password=$(secret)
# shellcheck disable=SC2034 # consumed by disable-system-test.sh in the same sourced shell.
bob_user=$(curl -fsS -b /tmp/owner.jar -H "X-CSRF-Token: $owner_csrf" -H 'Content-Type: application/json' \
  -d "{\"username\":\"bob\",\"password\":\"$bob_password\"}" "$api/api/v1/users" | jq -r .id || true)
bob_csrf=$(login bob "$bob_password" /tmp/bob.jar || true)
alice_csrf=$(login alice "$new_password" /tmp/alice.jar || true)
for _ in $(seq 1 100); do [[ -d "$data/spaces/private/bob/.a-nas-trash/bob" ]] && break; sleep 0.2; done
check "the Host Agent made bob's space and recycle bins" \
  test -d "$data/spaces/private/bob/.a-nas-trash/bob" -a -d "$data/spaces/shared/.a-nas-trash/bob"

# --- Without the administrator's key nobody has VNAS, and the desktop is told why.
check "without a key no member has an assistant" \
  test "$(curl -fsS -b /tmp/alice.jar "$api/api/v1/assistant/status" | jq -c . || true)" = '{"available":false,"reason":"no_key"}'
check "without a key the model gateway is not running" \
  test "$(systemctl is-active anas-assistant-gateway.service || true)" != active
check "a member cannot open the key's page" test "$(status_of -b /tmp/alice.jar "$api/api/v1/assistant-key")" = 403
# The requests that may lead to planning in the montage center register a planning grant for
# the member behind them first (docs/architecture/montage-center.md, 规划授权): the File Broker
# writes it into the Host Agent's own state, for root's planner alone. Here the job asked for does
# not exist, so the montage center answers 404 once the Product Service has passed the request
# on; without VNAS it passes it on with no grant.
replan() { # JAR CSRF: the status of asking to plan a job again
  status_of -b "$1" -H "X-CSRF-Token: $2" -X POST "$api/api/v1/montage/jobs/job:AAAAAAAAAAAAAAAAAAAAAAAA/replan" || true
}
plans=/var/lib/a-nas-host/assistant/plans
check "without VNAS a planning request reaches the montage center, and leaves no planning grant" \
  test "$(replan /tmp/alice.jar "$alice_csrf")" = 404 -a ! -e "$plans/alice"

# The scripted model stands where the gateway would reach DeepSeek. Planning the montage
# center's job below, its first call takes longer than two minutes.
systemd-run --unit fake-model --quiet --setenv=FAKE_MODEL_PLAN_SLEEP=130 \
  python3 /spike/fake_model.py tcp:127.0.0.1:18090 /var/log/fake-model.jsonl || true
install -d /etc/systemd/system/anas-assistant-gateway.service.d
printf '[Service]\nEnvironment=ANAS_ASSISTANT_UPSTREAM=http://127.0.0.1:18090\n' \
  >/etc/systemd/system/anas-assistant-gateway.service.d/system-test.conf
systemctl daemon-reload

# --- The administrator saves the household's DeepSeek key (设置 → VNAS): the Host Agent
# encrypts it for the gateway and prepares every active member's assistant, with its File
# Broker as the session gate (ADR 0024 decision 10) and the executor of the changes: only root
# reaches the instance sockets and the session tokens; the Product Service reaches a member's
# instance, and prepares or grants that member's changes, only with the member's live session.
test_key=sk-$(od -An -tx1 -N16 /dev/urandom | tr -d ' \n')
save_key() { # JAR CSRF ACKNOWLEDGED: the status of saving the test key
  status_of -b "$1" -H "X-CSRF-Token: $2" -H 'Content-Type: application/json' -X PUT \
    -d "{\"key\":\"$test_key\",\"acknowledged\":$3}" "$api/api/v1/assistant-key" || true
}
check "a member cannot save the key" test "$(save_key /tmp/alice.jar "$alice_csrf" true)" = 403
check "saving it needs the administrator's confirmation of what goes to DeepSeek" \
  test "$(save_key /tmp/owner.jar "$owner_csrf" false)" = 400
check "the administrator saves the key" test "$(save_key /tmp/owner.jar "$owner_csrf" true)" = 200
key_page=$(curl -fsS -b /tmp/owner.jar "$api/api/v1/assistant-key" || true)
check "the key's page says it is saved" test "$(jq -r .configured <<<"$key_page")" = true
check "and never what it is" test -z "$(grep -F "$test_key" <<<"$key_page" || true)"
credential=/etc/credstore.encrypted/a-nas-assistant-deepseek
check "the key is encrypted where only root reads it" \
  test "$(stat -c '%a %U' "$credential" 2>/dev/null)" = "600 root" -a -z "$(grep -lF "$test_key" "$credential" 2>/dev/null || true)"
check "no service logged the key" \
  test -z "$(journalctl -o cat --no-pager -u anas-api.service -u anas-host-agent.service 2>/dev/null | grep -F "$test_key" || true)"
for _ in $(seq 1 100); do
  systemctl is-active --quiet anas-assistant-gateway.service && [[ -S /run/anas-assistant-gw/model.sock ]] && break
  sleep 0.2
done
check "the model gateway runs with the key" systemctl is-active --quiet anas-assistant-gateway.service
check "only members' bridges may connect to the gateway" \
  test "$(stat -c '%a %G' /run/anas-assistant-gw/model.sock 2>/dev/null)" = "660 a-nas-users"
check "the gateway refuses what Hermes never asks of the model" test "$(curl -s -o /dev/null -w '%{http_code}' \
  --unix-socket /run/anas-assistant-gw/model.sock -X POST -d '{}' http://gateway/v1/files || true)" = 404
check "scheduled tasks are started when due" systemctl is-active --quiet anas-assistant-cron-waker.service
for member in owner alice bob; do
  for _ in $(seq 1 100); do [[ -S /run/a-nas-assistant/$member.sock ]] && break; sleep 0.2; done
  check "the Host Agent prepared $member's assistant" \
    test -S "/run/a-nas-assistant/$member.sock" -a -d "$data/assistant/$member/files" -a -d "$data/assistant/$member/hermes"
done
check "alice's Hermes home is hers alone" test "$(stat -c '%a %U %G' "$data/assistant/alice/hermes" 2>/dev/null)" = "700 alice alice"
check "the members' settings are in the Host Agent's own state" \
  test "$(stat -c '%a %U' /var/lib/a-nas-host 2>/dev/null)" = "700 root" -a -f /var/lib/a-nas-host/assistant/members/alice/config.yaml
check "alice now has VNAS" test "$(curl -fsS -b /tmp/alice.jar "$api/api/v1/assistant/status" | jq -r .available || true)" = true
check "with VNAS a planning request registers her grant on its way to the montage center" \
  test "$(replan /tmp/alice.jar "$alice_csrf"):$(replan /tmp/bob.jar "$bob_csrf")" = 404:404
check "the grants are root's alone, in the Host Agent's own state" \
  test "$(stat -c '%a %U' "$plans" 2>/dev/null):$(stat -c '%a %U %F' "$plans/alice" 2>/dev/null):$(stat -c '%a %U %F' "$plans/bob" 2>/dev/null)" = \
  "700 root:600 root regular file:600 root regular file"
check "a grant says when it was issued and how many rounds it allows" \
  jq -e '.rounds == 5 and (.issuedAt | fromdateiso8601) > (now - 300)' "$plans/alice"
check "no grant is kept where the Product Service could write one" test ! -e /var/lib/a-nas/assistant-plans
check "the Product Service's account cannot read the grants" denied a-nas ls "$plans"
check "nor can the montage service's" denied a-nas-montage ls "$plans"
check "the members write the probe's files into their spaces" quiet bash /spike/seed.sh "$data"
# A real snapshot of alice's space, which the sandbox must not show.
curl -fsS -o /dev/null -b /tmp/alice.jar -H "X-CSRF-Token: $alice_csrf" -H 'Content-Type: application/json' \
  -d '{"name":"assistant-probe"}' "$api/api/v1/spaces/$alice_private/snapshots" || true
check "a snapshot of alice's space exists on the volume" test -n "$(ls -A "$data/.a-nas-snapshots" 2>/dev/null)"
check "only root may connect to an instance socket" \
  test "$(stat -c '%a %U' /run/a-nas-assistant/alice.sock 2>/dev/null)" = "600 root"
check "a member cannot connect to an instance socket" denied alice python3 -c "$unix_connect" /run/a-nas-assistant/alice.sock
check "the Product Service cannot connect to one directly" denied a-nas python3 -c "$unix_connect" /run/a-nas-assistant/alice.sock
check "session tokens are root's alone" test "$(stat -c '%a %U' /var/lib/a-nas-host/assistant/tokens/alice.env 2>/dev/null)" = "600 root"
check "the Product Service holds no copy of the tokens" test ! -e /run/anas-spike-tokens/alice.token
# One unit per check: systemctl is-enabled A B succeeds when any of them is (#423).
for member in alice bob; do
  check "each member's weekly curator is enabled: $member" systemctl is-enabled --quiet "anas-assistant-curator@$member.timer"
done
# The photo service and the Product Service take their sockets for assistants from systemd.
check "the photo service holds its socket for assistants" systemctl is-active --quiet anas-photos-assistant.socket
check "the Product Service holds the socket of the companion tools" systemctl is-active --quiet anas-companion-assistant.socket
for _ in $(seq 1 100); do curl -fsS "$api/healthz" >/dev/null 2>&1 && [[ -S /run/a-nas/assistant-changes.sock ]] && break; sleep 0.2; done
check "the executor's assistant socket is out of a member's reach outside the sandbox" \
  denied alice python3 -c "$unix_connect" /run/a-nas/assistant-changes.sock
# The companion's socket (#412) reaches the sandbox of a member's conversations only: a
# scheduled task and the curator run as the same UID but have no window, so their units
# do not bind it, and outside a bind it is root's alone.
companion_socket=/run/a-nas-companion-assistant/assistant.sock
binds() { systemctl show -p BindPaths -p BindReadOnlyPaths --value "$1" || true; }
check "the companion's socket is out of a member's reach outside the sandbox" \
  denied alice python3 -c "$unix_connect" "$companion_socket"
check "a member's assistant binds the companion's socket into its sandbox" \
  grep -Fq "$companion_socket:/run/anas-assistant-companion/companion.sock" <<<"$(binds anas-assistant@alice.service)"
for unit in anas-assistant-cron@alice.service anas-assistant-curator@alice.service; do
  check "$unit does not bind the companion's socket" \
    test -n "$(binds "$unit")" -a "$(grep -cF "$companion_socket" <<<"$(binds "$unit")")" = 0
done
check "without the bind, the sandbox's path is the empty mount point" \
  test -f /run/anas-assistant-companion/companion.sock -a ! -s /run/anas-assistant-companion/companion.sock
# The montage center's socket (docs/architecture/montage-planning-session.md) is the reverse:
# only a member's planning instance binds it, a second `hermes serve` in her sandbox with the
# policy and settings that add the montage server and its tools pinned to montage, memory and
# skills; her ordinary instance, scheduled tasks and curator have none of it. Outside a bind
# the socket is root's alone, and root is answered only for the account it names: that is the
# planner, which hands the planning to her planning instance.
montage_socket=/run/a-nas-montage-assistant/assistant.sock
check "the montage service holds its socket for planning" systemctl is-active --quiet anas-montage-assistant.socket
check "the montage center's socket is out of a member's reach outside the sandbox" \
  denied alice python3 -c "$unix_connect" "$montage_socket"
montage_claim() { # [HEADER]: the status of a claim of a planning round on it, as root
  local header=()
  [[ -z "${1:-}" ]] || header=(-H "$1")
  curl -s -o /dev/null -w '%{http_code}' --unix-socket "$montage_socket" "${header[@]}" -X POST -d '{}' http://montage/v1/rounds/claim || true
}
check "the montage center refuses root unless it names the account" test "$(montage_claim)" = 403
check "and answers root for the account it names: alice has nothing to plan yet" \
  test "$(montage_claim 'X-A-NAS-Account: alice')" = 409
check "an account that is not in use is refused" test "$(montage_claim 'X-A-NAS-Account: nobody')" = 401
check "a member's planning instance binds the montage center's socket" \
  grep -Fq "$montage_socket:/run/anas-assistant-montage/montage.sock" <<<"$(binds anas-assistant-plan@alice.service)"
for unit in anas-assistant@alice.service anas-assistant-cron@alice.service anas-assistant-curator@alice.service; do
  check "$unit does not bind the montage center's socket" \
    test -n "$(binds "$unit")" -a "$(grep -cF "$montage_socket" <<<"$(binds "$unit")")" = 0
done
check "her planning instance does not bind the companion's socket either" \
  test -n "$(binds anas-assistant-plan@alice.service)" -a "$(grep -cF "$companion_socket" <<<"$(binds anas-assistant-plan@alice.service)")" = 0
plan_environment=$(systemctl show -p Environment --value anas-assistant-plan@alice.service || true)
check "her planning instance gives the model the montage, memory and skill tools alone" \
  grep -q 'HERMES_TUI_TOOLSETS=montage,memory,skills' <<<"$plan_environment"
check "and reads the policy with the montage server" grep -q 'HERMES_MANAGED_DIR=/usr/lib/a-nas/assistant/managed-plan' <<<"$plan_environment"
# Counted, not "no match": a policy that is missing or cannot be read has no line either.
members=/var/lib/a-nas-host/assistant/members
check "only that policy has the montage server" \
  test "$(grep -c '^  montage:' /usr/lib/a-nas/assistant/managed-plan/config.yaml):$(grep -c '^  montage:' /usr/lib/a-nas/assistant/managed/config.yaml):$(grep -c '^  anas:' /usr/lib/a-nas/assistant/managed/config.yaml)" = 1:0:1
check "and has every planning reviewed for what VNAS learned" \
  quiet yq -e '.skills.creation_nudge_interval == 1' /usr/lib/a-nas/assistant/managed-plan/config.yaml
check "the member's settings for planning carry it, the instance's do not" \
  test "$(grep -c 'anas_montage_mcp.py' "$members/alice/config-plan.yaml"):$(grep -c 'anas_montage_mcp.py' "$members/alice/config.yaml"):$(grep -c 'anas_mcp.py' "$members/alice/config.yaml")" = 1:0:1
check "her scheduled tasks are back on the ordinary policy" \
  test ! -e /usr/lib/a-nas/assistant/managed-cron -a ! -e "$members/alice/config-cron.yaml"
check "her planning instance has a socket of its own, root's alone" \
  test "$(stat -c '%a %U' /run/a-nas-assistant-plan/alice.sock 2>/dev/null)" = "600 root"
check "root's planner runs while the key is saved" systemctl is-active --quiet anas-montage-planner.service
check "the prompt templates of planning are installed" test -s /usr/lib/a-nas/assistant/montage-prompts/plan-v2.md
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
# The probe runs in the sandbox of anas-assistant@.service, in place of Hermes.
sed -e 's#^ExecStart=.*#ExecStart=/bin/bash /spike/probe.sh#' -e '/^ExecStartPost=/d' -e 's/^Type=.*/Type=oneshot/' \
  -e '/^StopWhenUnneeded/d' /usr/lib/systemd/system/anas-assistant@.service >/etc/systemd/system/anas-assistant-probe@.service
systemctl daemon-reload
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
# on_host ACCOUNT PATH: where PATH in ACCOUNT's assistant sandbox is outside it, by the probe
# unit's own bind mounts (systemctl show prints each as [-]SOURCE:DESTINATION:OPTIONS): the
# longest destination that holds PATH. A path under no bind is the host's own.
on_host() {
  local entry source destination host=$2 longest=0
  for entry in $(systemctl show -p BindPaths -p BindReadOnlyPaths --value "anas-assistant-probe@$1.service"); do
    entry=${entry#-}
    source=${entry%%:*}
    destination=${entry#*:}
    destination=${destination%:*}
    if [[ ($2 == "$destination" || $2 == "$destination"/*) && ${#destination} -gt $longest ]]; then
      host=$source${2#"$destination"}
      longest=${#destination}
    fi
  done
  echo "$host"
}
assistant_probe() { # ACCOUNT LEAST_PASSES
  local log hidden_path
  systemctl start "anas-assistant-probe@$1.service" || true
  # The oneshot has exited, but journald reads its stdout stream on its own time: a short
  # probe's lines could still be on their way (CI once read none of owner's). Have journald
  # take in everything written so far before reading it.
  journalctl --sync || true
  log=$(journalctl -u "anas-assistant-probe@$1.service" -o cat --no-pager || true)
  fold "assistant sandbox ($1)" < <(grep -E '^(PASS|FAIL|INFO)' <<<"$log" || true)
  check "the probe ran in $1's sandbox" test "$folded" -ge "$2"
  # What the probe was refused without seeing it is there outside the sandbox: a refusal of
  # something missing would prove nothing (#423).
  while read -r _ hidden_path; do
    check "assistant sandbox ($1): what it cannot see is there outside it: $hidden_path" \
      test -e "$(on_host "$1" "$hidden_path")"
  done < <(grep '^HIDDEN ' <<<"$log" || true)
}
# Every line the probe runs in this layout; lines that do not apply here print INFO instead.
assistant_probe alice 39
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
check "another member cannot write into her folder" cannot_create bob "$agent_space/alice/x.md"
check "no member writes Agent空间 itself" cannot_create alice "$agent_space/x.md"
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
# Both Web and SMB refuse to rename the managed publication roots (#177).
renamed=$(status_of -b /tmp/bob.jar -H "X-CSRF-Token: $bob_csrf" -H 'Content-Type: application/json' -X PATCH \
  -d '{"name":"Agent空间-bob"}' "$api/api/v1/files/$agent_space_id" || true)
check "another member cannot rename Agent空间 on the Web" test "$renamed" = 403
# Samba vetoes the name in Shared, so it answers as if Agent空间 were not there; it is.
check "SMB cannot rename Agent空间" smb_refused NT_STATUS_OBJECT_NAME_NOT_FOUND alice Shared 'rename Agent空间 Agent空间-smb'
check "Agent空间 keeps its name on the volume" test -d "$agent_space" -a ! -e "$data/spaces/shared/Agent空间-smb"
# A local process can still write Shared. Reconciliation repairs an impostor
# through descriptors; it must not rely on the old spike's path-based setup.
if [[ -d "$agent_space" ]]; then as bob mv -T "$agent_space" "$data/spaces/shared/Agent空间-bob" || true; fi
as bob mkdir -p "$agent_space/alice" || true
check "after a rename, another member can make an Agent空间 with a folder for alice" \
  test "$(stat -c %U "$agent_space/alice" 2>/dev/null)" = bob
systemctl restart anas-host-agent
# The restart recreates /run/a-nas, so the change socket bound at the sandbox's
# path above is now a deleted file, and a unit started later cannot mount its
# own over it (226/NAMESPACE). Bind the new socket there again.
for _ in $(seq 1 50); do [[ -S /run/a-nas/assistant-changes.sock ]] && break; sleep 0.2; done
# The old bind may be gone already; a new one would sit on top of it anyway.
umount /run/anas-change-agent/agent.sock 2>/dev/null || true
check "the restarted Host Agent's change socket is bound into the sandbox again" \
  mount --bind /run/a-nas/assistant-changes.sock /run/anas-change-agent/agent.sock
for _ in $(seq 1 50); do
  [[ "$(stat -c %U "$agent_space/alice" 2>/dev/null)" == root ]] && break
  sleep 0.2
done
check "the repair moves the impostor aside with what was made inside it" \
  test -n "$(find "$data/spaces/shared" -mindepth 1 -maxdepth 1 -name 'Agent空间.displaced-*' -user bob)"
check "and puts Agent空间 and her folder back as root's" \
  test "$(stat -c %U "$agent_space" 2>/dev/null):$(stat -c %U "$agent_space/alice" 2>/dev/null)" = root:root
check "her folder is hers alone again" cannot_create bob "$agent_space/alice/y.md"
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
if "$real_runtime"; then
  # The companion's MCP server under the runtime's own MCP SDK, which make ai-test has not
  # got: a missing argument, or a string that looks like JSON, reaches the Product Service
  # instead of coming back as the SDK's error, which Hermes's circuit breaker counts.
  sdk_out=$(cd /src/ai && "$assistant_python" -B -m unittest tests.test_companion_mcp 2>&1 || true)
  check "the companion's MCP server passes every call on under the runtime's MCP SDK" grep -qx OK <<<"$sdk_out"
  # The managed policy trusts the companion's server and not the A-NAS one, so a companion
  # tool runs without a card. approvals-managed.sh checks the same against a hostile member
  # config, but only in the spike: its unanswered approval waits out the managed
  # approvals.timeout, a day as installed here (SPIKE_APPROVAL_TIMEOUT sets 20 s there).
  check "the managed policy has the A-NAS tools ask and the companion's not" \
    quiet yq -e '.mcp_servers.anas.trust == "untrusted" and .mcp_servers.companion.trust == "full"' /usr/lib/a-nas/assistant/managed/config.yaml
  companion_out=$(SERVE_CLIENT_APPROVALS=deny "$assistant_python" /spike/serve_client.py alice chat 'CALL mcp__companion__wake {}' 2>&1 || true)
  check "a companion tool runs without asking for approval" \
    test "$(grep -c 'server request:' <<<"$companion_out")" = 0 -a "$(grep -c "mcp__companion__wake" <<<"$companion_out")" -ge 1
  check "and with no VNAS window it answers that no desktop is in use" grep -qE 'no_desktop|没有在用的桌面' <<<"$companion_out"
  # The desktop's server likewise, and a picture of the page reaches its client as an image.
  sdk_out=$(cd /src/ai && "$assistant_python" -B -m unittest tests.test_desktop_mcp 2>&1 || true)
  check "the desktop's MCP server does too, and returns a picture as an image" grep -qx OK <<<"$sdk_out"
  serve_out=$("$assistant_python" /spike/serve_client.py alice chat hello 'CALL read_file {"path": "/nas/个人空间/计划.md"}' 2>&1 || true)
  check "alice's assistant answers over its socket" grep -qx '< ok' <<<"$serve_out"
  check "it reads her own file" grep -q 买菜 <<<"$serve_out"
  # Her photos (decision 6, #113): the photo service's socket, held by systemd and bound into
  # her sandbox, answers her UID with her own library and the shared one, read-only.
  # The unit's main process (tini) shares the sandbox's mount namespace.
  alice_pid=$(systemctl show -p MainPID --value anas-assistant@alice.service)
  in_alice_sandbox() {
    nsenter -t "$alice_pid" -m -- setpriv --reuid alice --regid alice --init-groups -- \
      "$assistant_python" /spike/photos_peer.py /run/anas-assistant-photos/photos.sock "$@"
  }
  sandbox_libraries=$(in_alice_sandbox libraries 2>&1 || true)
  check "her assistant reaches her photo library and the shared one" \
    grep -qE 'private:.*shared:|shared:.*private:' <<<"$sandbox_libraries"
  check "and finds her photo" grep -q photo.png <<<"$(in_alice_sandbox search photo 2>&1 || true)"
  alice_photo=$(in_alice_sandbox ids photo 2>/dev/null | grep -o 'photo.png=[^,'"'"' ]*' | cut -d= -f2 || true)
  renamed=0
  in_alice_sandbox rename "$alice_photo" >/dev/null 2>&1 || renamed=$?
  check "but cannot rename it" test "$renamed" = 1
  photo_out=$("$assistant_python" /spike/serve_client.py alice chat 'CALL mcp__anas__search_photos {"query": "photo"}' 2>&1 || true)
  check "Hermes's photo search tool returns it" grep -q photo.png <<<"$photo_out"
  echo "INFO assistant photos: $sandbox_libraries"
  # Hermes's gateway, which runs her scheduled tasks, serves only a home that is its user's own.
  check "her Hermes home is her own, closed to everyone else, without an ACL" \
    test "$(stat -c '%u %a' "$data/assistant/alice/hermes"):$(getfacl -cp "$data/assistant/alice/hermes" 2>/dev/null | grep -cE '^(user|group):[^:]|^mask|^default')" = "$alice_id 700:0"
  check "it runs under alice's own UID" \
    test "$(awk '/^Uid:/ {print $2}' "/proc/$(systemctl show -p MainPID --value anas-assistant@alice.service)/status" 2>/dev/null)" = "$alice_id"
  # Hermes's own login and the desktop tools (decisions 8 to 10): the old session token is
  # refused, the model is offered the desktop server's tools and none of Hermes's own
  # browser tools, and a step with no VNAS window behind it says that no desktop is in use.
  desktop_out=$("$assistant_python" /spike/desktop_control.py alice 2>&1 || true)
  check "her instance refuses the old session token" grep -q '^PASS the old session token is refused' <<<"$desktop_out"
  check "her assistant has the desktop tools, and none of Hermes's browser tools" grep -q '^desktop control: 0 failed' <<<"$desktop_out"
  grep '^FAIL' <<<"$desktop_out" | sed 's/^/INFO assistant desktop tools: /' || true
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
  # Renaming and deleting a conversation: Hermes commits such a write only behind its fence,
  # which the Product Service names (internal/assistantapi/sessionwrite.go).
  renamed_id=$(jq -r '.sessions[0].id // empty' /tmp/gate-sessions.json 2>/dev/null || true)
  deleted_id=$(jq -r '.sessions[1].id // empty' /tmp/gate-sessions.json 2>/dev/null || true)
  check "alice renames a conversation" \
    test "$(status_of -b /tmp/alice.jar -H "X-CSRF-Token: $alice_csrf" -H 'Content-Type: application/json' -X PATCH \
      -d '{"title":"系统测试改名"}' "$api/api/v1/assistant/sessions/$renamed_id")" = 200
  check "and deletes another" \
    test "$(status_of -b /tmp/alice.jar -H "X-CSRF-Token: $alice_csrf" -X DELETE "$api/api/v1/assistant/sessions/$deleted_id")" = 204
  # shellcheck disable=SC2016 # $kept and $gone are jq's
  check "her list has the new title and no longer the deleted conversation" \
    jq -e --arg kept "$renamed_id" --arg gone "$deleted_id" \
      '(.sessions | map(select(.id == $kept)) | .[0].title == "系统测试改名") and (.sessions | map(.id) | index($gone) | not)' \
      <<<"$(curl -fsS -b /tmp/alice.jar "$api/api/v1/assistant/sessions" || true)"
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
  # VNAS operates the desktop (decision 8): a step the model takes leaves her sandbox by the
  # desktop's MCP server and the Product Service's socket for assistants, reaches the window
  # that attached as the conversation's browser and sent its prompt, and what the window
  # answers returns into the turn; a picture of the page returns as an image file.
  "$assistant_python" -u /spike/gate_client.py "ws://127.0.0.1:8080/api/v1/assistant/ws" "$(session_of /tmp/alice.jar)" \
    desktop >/tmp/gate-desktop.out 2>&1 || true
  check "her window attaches as its conversation's browser" grep -qx 'attached True' /tmp/gate-desktop.out
  check "a snapshot the model asks for reaches her window" grep -qx 'command snapshot {}' /tmp/gate-desktop.out
  check "and the page her window read returns into the turn" grep -q '^answer: .*button .*相册.*ref=e2' /tmp/gate-desktop.out
  check "a click carries the control's ref to her window" grep -qx 'command click {"ref": "e2"}' /tmp/gate-desktop.out
  check "a picture of the page returns as an image for vision_analyze" \
    bash -c "grep -q '^command look' /tmp/gate-desktop.out && grep -q '^answer: .*vision_analyze.*MEDIA:' /tmp/gate-desktop.out"
  echo "INFO assistant desktop: $(tr '\n' ';' </tmp/gate-desktop.out | cut -c1-600)"
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
  # Scheduled tasks (decision 8): alice makes one in a conversation with Hermes's own tool;
  # when it is due, the waker runs it in her sandbox with her assistant stopped, and the
  # Product Service lists it and tells her of the run once she is signed in.
  check "the waker that starts due tasks runs" systemctl is-active --quiet anas-assistant-cron-waker.service
  "$assistant_python" /spike/serve_client.py alice chat \
    'CALL cronjob_manage {"action": "create", "name": "系统测试任务", "schedule": "every 1m", "prompt": "SAY 共享空间今天没有变化 END"}' >/dev/null 2>&1 || true
  systemctl stop anas-assistant-api@alice.service anas-assistant@alice.service 2>/dev/null || true
  # From here on only a round of her scheduled tasks asks the model.
  round_from=$(date +%s.%N)
  jobs_file="$data/assistant/alice/hermes/cron/jobs.json"
  for _ in $(seq 1 60); do jq -e '.jobs[0].last_run_at' "$jobs_file" >/dev/null 2>&1 && break; sleep 5; done
  check "her task ran when due, in her sandbox, with her assistant stopped" \
    jq -e '.jobs[0].last_status == "ok"' "$jobs_file"
  check "her assistant itself stayed stopped" test "$(systemctl is-active anas-assistant@alice.service || true)" != active
  # A scheduled run is not followed by Hermes's background review of memory and skills. The
  # scripted model notes each request that is a review; this run asked the model and none of
  # its requests was one.
  # shellcheck disable=SC2016 # $from is jq's
  check "the scheduled run asked the model, and Hermes followed it with no background review" \
    jq -es --argjson from "$round_from" '[.[] | select(.t >= $from)] | length >= 1 and all(.[]; .review == false)' /var/log/fake-model.jsonl
  echo "INFO assistant tasks: $(jq -c '.jobs[0] | {name, last_status, last_error}' "$jobs_file" 2>/dev/null || true)"
  # The round's gateway is gone, and with it the last unit that needed her private network.
  for _ in $(seq 1 30); do [[ "$(systemctl is-active anas-assistant-net@alice.service || true)" == active ]] || break; sleep 2; done
  check "her assistant's private network stops cleanly once nothing needs it" \
    test "$(systemctl is-active anas-assistant-net@alice.service || true)" = inactive
  # The system test's last check uses /tmp/alice.jar.
  alice_csrf=$(login alice "$new_password" /tmp/alice.jar || true)
  tasks=$(curl -fsS -b /tmp/alice.jar "$api/api/v1/assistant/tasks" || true)
  check "the Product Service lists her task without Hermes's internals" \
    jq -e '.tasks[0].name == "系统测试任务" and .tasks[0].lastStatus == "ok" and (tostring | contains("hermes_home") | not)' <<<"$tasks"
  task_id=$(jq -r '.tasks[0].id // empty' <<<"$tasks" || true)
  notices() { curl -fsS -b /tmp/alice.jar "$api/api/v1/notifications" || true; }
  for _ in $(seq 1 60); do jq -e 'any(.items[]; .kind == "assistant_task")' <<<"$(notices)" >/dev/null 2>&1 && break; sleep 2; done
  check "signed in, she is told of the run with what it answered" \
    jq -e 'any(.items[]; .kind == "assistant_task" and .task.name == "系统测试任务" and .task.summary == "共享空间今天没有变化")' <<<"$(notices)"
  # "立即运行" (#469): the task runs in a round of her cron unit, like one that is due, and
  # never in her instance, where Hermes would start a gateway of its own and leave it. The
  # task is paused first, so that only her asking runs it; running it resumes it.
  check "she pauses the task" \
    test "$(status_of -b /tmp/alice.jar -H "X-CSRF-Token: $alice_csrf" -X POST "$api/api/v1/assistant/tasks/$task_id/pause")" = 200
  # A round is a oneshot unit: activating while it runs, never active.
  round_over() { [[ ! "$(systemctl is-active anas-assistant-cron@alice.service 2>/dev/null || true)" =~ ^(activating|active|deactivating)$ ]]; }
  for _ in $(seq 1 60); do round_over && break; sleep 2; done
  for _ in $(seq 1 120); do systemctl is-active --quiet anas-assistant@alice.service && break; sleep 0.5; done
  ran_before=$(jq -r '.jobs[0].last_run_at // empty' "$jobs_file" 2>/dev/null || true)
  check "she asks for the task to run now, and is told it started" \
    test "$(status_of -b /tmp/alice.jar -H "X-CSRF-Token: $alice_csrf" -X POST "$api/api/v1/assistant/tasks/$task_id/run")" = 202
  for _ in $(seq 1 60); do
    [[ "$(jq -r '.jobs[0].last_run_at // empty' "$jobs_file" 2>/dev/null || true)" != "$ran_before" ]] && break
    sleep 2
  done
  # shellcheck disable=SC2016 # $before is jq's
  check "the task ran, in a round of her scheduled tasks, and is no longer paused" \
    jq -e --arg before "$ran_before" '.jobs[0] | .last_run_at != $before and .last_status == "ok" and .state == "scheduled"' "$jobs_file"
  check "her instance is running, and no gateway runs in it" \
    bash -c "systemctl is-active --quiet anas-assistant@alice.service && ! COLUMNS=400 systemd-cgls --no-pager -l -u anas-assistant@alice.service | grep -q 'gateway run'"
  for _ in $(seq 1 60); do round_over && break; sleep 2; done
  check "the round took her request and left none behind" \
    test -z "$(ls -A /run/a-nas/assistant-runs/alice /run/anas-assistant-run-now/alice 2>/dev/null)"
  echo "INFO assistant run now: $(journalctl -u anas-assistant-cron@alice.service -o cat --no-pager | grep 'queued to run now' | tail -n 1 || true)"
  check "she deletes the task" \
    test "$(status_of -b /tmp/alice.jar -H "X-CSRF-Token: $alice_csrf" -X DELETE "$api/api/v1/assistant/tasks/$task_id")" = 204
  # --- The montage center's planning (docs/architecture/montage-planning-session.md): a job of
  # hers comes to its planning step; root's planner claims a round for her and hands it to her
  # planning instance as a conversation; the scripted model plays VNAS and plans both 方案 with
  # the four montage tools, one of its calls taking longer than two minutes; the job goes on
  # with what it handed in, the conversation stays in her VNAS history, and Hermes's background
  # review writes what it learned as a skill.
  #
  # What comes before planning needs models this test does not install: the photo library's
  # analysis of her video and allin1's analysis of her song. With the montage service stopped,
  # the test gives the job what those steps would have kept, a catalog of four cards, and her
  # song an analysis (montage-plan-seed.py); from there the job works out its keys and asks for
  # its briefs itself, whose data and schema the compute Worker's stand-in writes
  # (scripts/system-test.sh). Nothing in the product is opened for the test. The real compute
  # Worker would take the four cards for real ones, so with the real content this is left out.
  for _ in $(seq 1 60); do round_over && break; sleep 2; done
  if [[ "${ANAS_SYSTEM_TEST_AI:-}" == real ]]; then
    echo "INFO montage planning: left out with the real compute Worker, which the seeded cards are not for"
  else
    montage_job() { curl -fsS -b /tmp/alice.jar "$api/api/v1/montage/jobs/$plan_job" || true; }
    plan_project=$(curl -fsS -b /tmp/alice.jar -H "X-CSRF-Token: $alice_csrf" -H 'Content-Type: application/json' \
      -d "{\"requestKey\":\"system-test-plan\",\"assetIds\":[\"$clip\"]}" "$api/api/v1/montage/projects" | jq -r .id || true)
    check "she makes a montage project of her video and her song" \
      test "$(status_of -X PUT -b /tmp/alice.jar -H "X-CSRF-Token: $alice_csrf" -H 'Content-Type: application/json' \
        -d "{\"trackId\":\"$alice_track\"}" "$api/api/v1/montage/projects/$plan_project/song")" = 200
    plan_name=$(curl -fsS -b /tmp/alice.jar "$api/api/v1/montage/projects/$plan_project" | jq -r .name || true)
    plan_job=$(curl -fsS -b /tmp/alice.jar -H "X-CSRF-Token: $alice_csrf" -H 'Content-Type: application/json' \
      -d '{"requestKey":"system-test-plan"}' "$api/api/v1/montage/projects/$plan_project/generate" | jq -r .id || true)
    check "she starts it, which registers her planning grant on the way" \
      bash -c "[[ -n '$plan_job' && '$plan_job' != null ]] && jq -e '(.issuedAt | fromdateiso8601) > (now - 120)' '$plans/alice' >/dev/null"
    systemctl stop anas-montage-assistant.socket anas-montage.service || true
    seeded=$(setpriv --reuid=a-nas-montage --regid=a-nas-montage --clear-groups \
      python3 /spike/montage-plan-seed.py "$data/montage/montage.db" "$plan_job" "$clip" 2>&1 || true)
    systemctl start anas-montage.service anas-montage-assistant.socket || true
    check "the test gives her job a catalog and her song an analysis" grep -q '^seeded ' <<<"$seeded"
    echo "INFO montage planning: $seeded"
    waits_for_vnas() { [[ "$(montage_job | jq -r '.state + " " + (.waitReason // "")')" == "waiting vnas" ]]; }
    for _ in $(seq 1 60); do waits_for_vnas && break; sleep 2; done
    check "her job comes to planning by itself and waits for VNAS" waits_for_vnas
    echo "INFO montage planning: the job before its round: $(montage_job | jq -c '{state, step, waitReason, error}' || true)"
    planner_said() { journalctl -u anas-montage-planner.service -o cat --no-pager | grep -q "$1"; }
    for _ in $(seq 1 30); do planner_said "alice: round .* handed to VNAS" && break; sleep 2; done
    check "root's planner claims a round for her and hands it to her planning instance" planner_said "alice: round .* handed to VNAS"
    check "her planning instance runs apart from her ordinary one" \
      test "$(systemctl show -p ActiveEnterTimestampMonotonic --value anas-assistant-plan@alice.service)" != 0
    for _ in $(seq 1 240); do planner_said "alice: round .* ended" && break; sleep 2; done
    check "the round ends with both 方案 handed in" planner_said "alice: round .* ended: complete"
    check "the montage center took both steps of both 方案" test "$(journalctl -u anas-montage.service -o cat --no-pager |
      grep 'planning step taken' | grep -cF "$plan_job")" = 4
    past_planning() {
      montage_job | jq -e '([.steps[] | select(.name == "pick" or .name == "shots") | .state] == ["done", "done"])
        and (.step | IN("pick", "shots") | not)' >/dev/null
    }
    for _ in $(seq 1 60); do past_planning && break; sleep 2; done
    check "her job goes past planning with what VNAS handed in" past_planning
    echo "INFO montage planning: the job after its round: $(montage_job | jq -c '{state, step, waitReason, error}' || true)"
    plan_requests=$(jq -sc '[.[] | select(.plan and (.review | not) and (.tools | length) > 0)]' /var/log/fake-model.jsonl || true)
    plan_tools=$(jq -c '[.[].tools[]] | unique' <<<"$plan_requests" || true)
    check "the model was offered the montage, memory and skill tools alone" test "$plan_tools" = \
      '["mcp__montage__montage_brief","mcp__montage__montage_shots_brief","mcp__montage__montage_submit_outline","mcp__montage__montage_submit_shots","memory","skill_manage","skill_view","skills_list"]'
    echo "INFO montage planning: tools offered: $plan_tools"
    check "a model call of more than two minutes in the round was waited out" \
      jq -e 'any(.[]; (.decision.sleep // 0) >= 120)' <<<"$plan_requests"
    # shellcheck disable=SC2016 # $title is jq's
    check "the planning stays in her VNAS history, titled for the project" jq -e --arg title "剪辑规划 · $plan_name" \
      'any(.sessions[]; .title == $title)' <<<"$(curl -fsS -b /tmp/alice.jar "$api/api/v1/assistant/sessions" || true)"
    check "Hermes's background review of the planning ran" \
      jq -es 'any(.[]; .plan and .review and .decision.tool == "skill_manage")' /var/log/fake-model.jsonl
    check "and wrote what it learned as a skill of hers" \
      test -n "$(find "$data/assistant/alice/hermes/skills" -path '*/montage-planning-notes/SKILL.md' 2>/dev/null)"
    check "the planner counted the round on her grant" \
      jq -e '[.counts | to_entries[] | select(.key | startswith("alice|")) | .value] == [1]' /var/lib/anas-montage-planner/state.json
    check "no marker is left for her" test ! -e /run/a-nas-montage/plans/alice
    for _ in $(seq 1 90); do systemctl is-active --quiet anas-assistant-plan@alice.service || break; sleep 2; done
    check "her planning instance stops once the planner has left it" \
      test "$(systemctl is-active anas-assistant-plan@alice.service || true)" != active
    echo "INFO montage planning: $(journalctl -u anas-montage-planner.service -o cat --no-pager | tail -n 6 | tr '\n' ';' || true)"
  fi
fi

# --- The layout, for comparison with the spike's (layout-acl.sh in both).
while IFS= read -r line; do echo "INFO assistant layout: $line"; done < <(bash /spike/layout-acl.sh "$data" alice bob || true)


