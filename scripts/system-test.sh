#!/usr/bin/env bash
# System test of the multi-user permission model. It installs the built
# release with the real installer and systemd units into a disposable Debian
# 13 container where systemd is PID 1, then checks every entry point that
# touches the data volume: Web files (File Broker workers), SMB, the photo API,
# the FFmpeg media worker shared by the media center and music, the
# administrator terminal, and the LAN Web entry that Caddy serves on
# port 80. Unlike tests that start
# the daemons by hand, it runs them under the production sandboxes, so it
# catches a unit that takes the Host Agent's CAP_SETUID (issue #38).
#
# Usage: make build-binaries && scripts/build-system-test-image.sh &&
#        scripts/system-test.sh [IMAGE]
# Prints PASS/FAIL lines and exits non-zero on any failure.
#
# Local AI runs on a stand-in model, detector and runtime with the Worker's
# fake providers. With ANAS_SYSTEM_TEST_AI=real, after
# scripts/build-ai-runtime.sh and with ANAS_AI_MODEL and ANAS_AI_DETECTOR
# naming the model and detector files, it installs the real runtime and models
# instead.
set -euo pipefail

if [[ "${1:-}" != --inside ]]; then
  image=${1:-anas-systemd:trixie}
  repo=$(cd "$(dirname "$0")/.." && pwd)
  for binary in anas-api anas-host-agent; do
    [[ -x "$repo/build/$binary" ]] || { echo "missing build/$binary: run make build-binaries" >&2; exit 2; }
  done
  real_ai=()
  if [[ "${ANAS_SYSTEM_TEST_AI:-}" == real ]]; then
    [[ -f "$repo/build/ai-runtime.json" ]] || { echo "missing build/ai-runtime.json: run scripts/build-ai-runtime.sh" >&2; exit 2; }
    [[ -f "${ANAS_AI_MODEL:-}" ]] || { echo "ANAS_AI_MODEL must name the model file" >&2; exit 2; }
    [[ -f "${ANAS_AI_DETECTOR:-}" ]] || { echo "ANAS_AI_DETECTOR must name the detector file" >&2; exit 2; }
    real_ai=(-e ANAS_SYSTEM_TEST_AI=real -v "$(dirname "$ANAS_AI_MODEL"):/ai-model:ro" -v "$(dirname "$ANAS_AI_DETECTOR"):/ai-detector:ro")
  fi
  name="anas-system-test-$$"
  docker run -d --name "$name" --privileged --cgroupns=host -v /sys/fs/cgroup:/sys/fs/cgroup:rw \
    --tmpfs /run --tmpfs /run/lock -v "$repo:/src:ro" "${real_ai[@]}" "$image" >/dev/null
  trap 'docker rm -f "$name" >/dev/null 2>&1' EXIT
  for _ in $(seq 1 60); do
    state=$(docker exec "$name" systemctl is-system-running 2>/dev/null || true)
    [[ "$state" == running || "$state" == degraded ]] && break
    sleep 1
  done
  docker exec "$name" bash /src/scripts/system-test.sh --inside
  exit
fi

if [[ $EUID -ne 0 || "$(cat /proc/1/comm)" != systemd || ! -f /.dockerenv ]]; then
  echo "run through scripts/system-test.sh, inside its disposable systemd container" >&2
  exit 2
fi

# A Debian host's mounts are shared, so mounting or unmounting the data volume
# reaches the services' private mount namespaces; the container starts with
# private mounts, which would hide that.
mount --make-rshared /

# The models and runtime the releases pin (ADR 0016), staged once as
# deploy-dev.ps1 stages them. The stand-in runtime is a virtual environment
# around Debian's Python alone, which the fake providers need.
ai=/tmp/ai-staged
install -d "$ai"
if [[ "${ANAS_SYSTEM_TEST_AI:-}" == real ]]; then
  cp /src/deploy/models/embeddinggemma-2-740m.json "$ai/model.json"
  ln -s "/ai-model/$(jq -r .file "$ai/model.json")" "$ai/model.litertlm"
  cp /src/deploy/models/dfine-m-obj365.json "$ai/detector.json"
  ln -s "/ai-detector/$(jq -r .file "$ai/detector.json")" "$ai/detector.onnx"
  cp /src/build/ai-runtime.json "$ai/runtime.json"
  ln -s "/src/build/ai-runtime/$(jq -r .file "$ai/runtime.json")" "$ai/runtime.tar.gz"
  detector_model="$(jq -r .name "$ai/detector.json")@$(jq -r .sha256 "$ai/detector.json" | cut -c1-12)+"
else
  head -c 65536 /dev/urandom > "$ai/model.litertlm"
  printf '{"name":"system-test","file":"system-test.litertlm","sizeBytes":%s,"sha256":"%s"}\n' \
    "$(stat -c %s "$ai/model.litertlm")" "$(sha256sum "$ai/model.litertlm" | cut -d ' ' -f 1)" > "$ai/model.json"
  head -c 4096 /dev/urandom > "$ai/detector.onnx"
  printf '{"name":"system-test-detector","file":"system-test.onnx","sizeBytes":%s,"sha256":"%s"}\n' \
    "$(stat -c %s "$ai/detector.onnx")" "$(sha256sum "$ai/detector.onnx" | cut -d ' ' -f 1)" > "$ai/detector.json"
  detector_model=fake-detector
  install -d "$ai/venv/bin"
  ln -s /usr/bin/python3 "$ai/venv/bin/python"
  printf 'home = /usr/bin\ninclude-system-site-packages = false\nversion = %s\n' \
    "$(python3 -c 'import platform; print(platform.python_version())')" > "$ai/venv/pyvenv.cfg"
  tar -C "$ai/venv" -czf "$ai/runtime.tar.gz" .
  runtime_sha=$(sha256sum "$ai/runtime.tar.gz" | cut -d ' ' -f 1)
  printf '{"id":"%s","python":"%s","file":"runtime.tar.gz","sizeBytes":%s,"sha256":"%s"}\n' "${runtime_sha:0:16}" \
    "$(python3 -c 'import sys; print("%d.%d" % sys.version_info[:2])')" "$(stat -c %s "$ai/runtime.tar.gz")" \
    "$runtime_sha" > "$ai/runtime.json"
  install -d /etc/systemd/system/anas-ai.service.d
  printf '[Service]\nExecStart=\nExecStart=/opt/a-nas/current/ai/runtime/bin/python -m anas_ai.worker --fake --fake-detector\n' \
    > /etc/systemd/system/anas-ai.service.d/system-test.conf
fi
# AI starts once the photo library has gone unused for this long, so that the
# test sees a photo organised in the Worker's sandbox.
install -d /etc/systemd/system/anas-photos.service.d
printf '[Service]\nEnvironment=ANAS_PHOTOS_AI_QUIET_PERIOD=2s\n' > /etc/systemd/system/anas-photos.service.d/system-test.conf

# stage DIR: a release as scripts/deploy-dev.ps1 stages it.
stage() {
  install -d "$1"
  install -m 0755 /src/build/anas-api /src/build/anas-host-agent /src/scripts/install-screensavers.sh "$1/"
  for unit in anas-api anas-host-agent anas-photos; do
    install -m 0644 "/src/deploy/systemd/system/$unit.service" "$1/$unit-system.service"
  done
  install -m 0644 /src/deploy/systemd/system/anas.slice "$1/anas-system.slice"
  install -m 0644 /src/deploy/systemd/system/anas-ai.socket "$1/anas-ai-system.socket"
  install -m 0644 /src/deploy/systemd/system/anas-ai.service "$1/anas-ai-system.service"
  install -m 0644 /src/deploy/systemd/system/anas-media.socket "$1/anas-media-system.socket"
  install -m 0644 "/src/deploy/systemd/system/anas-media@.service" "$1/anas-media-system@.service"
  install -d "$1/ai/anas_ai"
  install -m 0644 /src/ai/anas_ai/*.py "$1/ai/anas_ai/"
  install -m 0644 "$ai/model.json" "$ai/detector.json" "$ai/runtime.json" "$1/ai/"
  ln -s "$ai/model.litertlm" "$1/ai/model.litertlm"
  ln -s "$ai/detector.onnx" "$1/ai/detector.onnx"
  ln -s "$ai/runtime.tar.gz" "$1/ai/runtime.tar.gz"
  install -m 0644 /src/deploy/chromium/policies/managed/a-nas.json "$1/a-nas-chromium-policy.json"
  install -m 0644 /src/deploy/caddy/Caddyfile "$1/Caddyfile"
}
release=/tmp/release
stage "$release"

# A data volume as storage initialization leaves it, mounted before the
# installer runs, as on the Experimental NAS. Larger than the 10 GiB reserve;
# sparse, so it costs little disk.
truncate --size=64G /var/tmp/data.img
mkfs.btrfs --quiet /var/tmp/data.img
install -d /srv/a-nas/data
mount -o loop /var/tmp/data.img /srv/a-nas/data
btrfs subvolume create /srv/a-nas/data/spaces >/dev/null
volume_uuid=11111111-2222-4333-8444-555555555555
printf '{"filesystemUuid":"%s","formatVersion":1}\n' "$volume_uuid" > /srv/a-nas/data/.a-nas-volume.json

# The container has no disks of its own. Present a system disk and the
# loop-backed data volume as SATA disks at the lsblk boundary, the way the
# Experimental NAS shows them; everything above lsblk runs unchanged.
cat > /usr/local/sbin/lsblk <<'SHIM'
#!/bin/sh
cat <<'JSON'
{"blockdevices":[
  {"name":"sda","type":"disk","size":256060514304,"model":"System Test SSD","tran":"sata","rota":false,"rm":false,
   "fstype":null,"mountpoints":[null],"wwn":"0x5000000000000001","serial":"SYSTEM0001","children":[
    {"name":"sda1","type":"part","size":256059465728,"model":null,"tran":null,"rota":false,"rm":false,
     "fstype":"ext4","mountpoints":["/"],"wwn":"0x5000000000000001","serial":null}]},
  {"name":"sdb","type":"disk","size":68719476736,"model":"System Test HDD","tran":"sata","rota":true,"rm":false,
   "fstype":null,"mountpoints":[null],"wwn":"0x5000000000000002","serial":"DATA0001","children":[
    {"name":"sdb1","type":"part","size":68718428160,"model":null,"tran":null,"rota":true,"rm":false,
     "fstype":"btrfs","mountpoints":["/srv/a-nas/data"],"wwn":"0x5000000000000002","serial":null}]}
]}
JSON
SHIM
chmod 0755 /usr/local/sbin/lsblk
# The Host Agent derives disk IDs from the WWN (internal/hoststate/linux).
data_disk="disk:$(printf 'a-nas:disk:v1\0wwn:0x5000000000000002' | sha256sum | cut -c1-32)"

iface=$(ip -o -4 route show to default | awk '{print $5; exit}')
ip=$(ip -o -4 addr show dev "$iface" | awk '{split($4, a, "/"); print a[1]; exit}')
installed_at=$(date '+%Y-%m-%d %H:%M:%S')
if ! bash /src/scripts/install-v1.0.1-system-services.sh "$release" system-test "$iface" >/tmp/install.log 2>&1; then
  tail -30 /tmp/install.log
  echo "FAIL installer"
  exit 1
fi

# Record the volume as storage initialization would have, so the Product
# Service's volume guard accepts it, then restart it to load the record. It
# creates its databases before it answers, so wait for that first.
for _ in $(seq 1 100); do curl -fsS http://127.0.0.1:8080/healthz >/dev/null 2>&1 && break; sleep 0.2; done
systemctl stop anas-api
setpriv --reuid=a-nas --regid=a-nas --init-groups python3 - "$data_disk" "$volume_uuid" <<'PY'
import json, sqlite3, sys
disk, uuid = sys.argv[1], sys.argv[2]
now = "2026-10-08T00:00:00Z"
plan = {"id": "plan:system-test", "diskId": disk, "diskModel": "System Test HDD", "capacityBytes": 68719476736,
        "fingerprint": "", "signatures": [], "confirmationPhrase": "", "actions": [], "state": "succeeded",
        "createdAt": now, "expiresAt": now,
        "volume": {"id": "volume:data", "diskId": disk, "filesystemUuid": uuid, "state": "available"}}
db = sqlite3.connect("/var/lib/a-nas/storage.db")
db.execute("INSERT INTO storage_plans(id, document_json, updated_at) VALUES(?, ?, ?)", (plan["id"], json.dumps(plan), now))
db.commit()
PY
systemctl start anas-api

failures=0
# check NAME COMMAND...: runs the command and records the outcome.
check() {
  local name=$1
  shift
  if "$@"; then echo "PASS $name"; else echo "FAIL $name"; failures=$((failures + 1)); fi
}
# shellcheck disable=SC2317,SC2329 # invoked through check (SC2317 in older shellcheck)
denied() { ! "$@" >/dev/null 2>&1; }
# shellcheck disable=SC2317,SC2329 # invoked through check (SC2317 in older shellcheck)
quiet() { "$@" >/dev/null 2>&1; }
# shellcheck disable=SC2317,SC2329 # invoked through check (SC2317 in older shellcheck)
journal_has() { journalctl -u "$1" --since "$2" -o cat --no-pager | grep -Fq "$3"; }
# shellcheck disable=SC2317,SC2329 # invoked through check (SC2317 in older shellcheck)
has_capabilities() {
  local pid cap_eff
  pid=$(systemctl show -p MainPID --value "$1")
  cap_eff=$(awk '/^CapEff:/ {print $2}' "/proc/$pid/status")
  (((16#$cap_eff >> 6 & 3) == 3))
}
# shellcheck disable=SC2317,SC2329 # invoked through denied (SC2317 in older shellcheck)
as() { local user=$1; shift; setpriv --reuid="$user" --regid="$user" --init-groups "$@"; }
owner_uid() { stat -c '%u' "$1"; }
# shellcheck disable=SC2317,SC2329 # invoked through check (SC2317 in older shellcheck)
in_trash() { # USER SPACE_DIR FILE: FILE's bytes are in USER's recycle bin
  find "$2/.a-nas-trash/$1" -type f -exec cmp -s "$3" {} \; -print | grep -q .
}
status_of() { curl -s -o /dev/null -w '%{http_code}' "$@"; }
secret() { head -c 24 /dev/urandom | base64 | tr -dc 'A-Za-z0-9' | head -c 20; }
api=http://127.0.0.1:8080
data=/srv/a-nas/data

# --- Every service runs under its production unit (issue #38).
check "services are active" systemctl is-active --quiet anas-host-agent anas-photos anas-api smbd
check "Host Agent keeps CAP_SETUID and CAP_SETGID" has_capabilities anas-host-agent
check "Host Agent proved it can start workers as users" \
  journal_has anas-host-agent "$installed_at" 'file broker identity switch verified'
for _ in $(seq 1 50); do curl -fsS "$api/healthz" >/dev/null 2>&1 && break; sleep 0.2; done

# --- Accounts: an administrator and a member, each with one Linux identity.
owner_password=$(secret)
alice_password=$(secret)
login() { # USER PASSWORD JAR -> CSRF token
  curl -fsS -c "$3" -H 'Content-Type: application/json' \
    -d "{\"username\":\"$1\",\"password\":\"$2\"}" "$api/api/v1/session" | jq -r .csrfToken
}
owner_csrf=$(curl -fsS -c /tmp/owner.jar -H 'Content-Type: application/json' \
  -d "{\"username\":\"owner\",\"password\":\"$owner_password\"}" "$api/api/v1/setup/admin" | jq -r .csrfToken)
curl -fsS -o /dev/null -b /tmp/owner.jar -H "X-CSRF-Token: $owner_csrf" -H 'Content-Type: application/json' \
  -d "{\"username\":\"alice\",\"password\":\"$alice_password\"}" "$api/api/v1/users"
alice_csrf=$(login alice "$alice_password" /tmp/alice.jar)
owner_id=$(id -u owner)
alice_id=$(id -u alice)
check "accounts have A-NAS UIDs" test "$owner_id" -ge 20100 -a "$alice_id" -ge 20100 -a "$owner_id" -ne "$alice_id"

space_of() { curl -fsS -b "$1" "$api/api/v1/spaces" | jq -r --arg kind "$2" '[.items[] | select(.kind == $kind)][0].id'; }
upload() { # JAR CSRF SPACE FILE NAME -> file ID
  curl -fsS -b "$1" -H "X-CSRF-Token: $2" -F "file=@$4;filename=$5" "$api/api/v1/spaces/$3/uploads" | jq -r .id
}
entries() { curl -fsS -b "$1" "$api/api/v1/spaces/$2/entries" | jq -r '.items[].name'; }
owner_private=$(space_of /tmp/owner.jar private)
alice_private=$(space_of /tmp/alice.jar private)
shared=$(space_of /tmp/alice.jar shared)

# --- Web: every operation runs as the signed-in user.
printf 'owner %s\n' "$(secret)" > /tmp/owner.txt
printf 'alice %s\n' "$(secret)" > /tmp/alice.txt
printf 'shared %s\n' "$(secret)" > /tmp/shared.txt
owner_file=$(upload /tmp/owner.jar "$owner_csrf" "$owner_private" /tmp/owner.txt owner.txt || true)
check "Web upload works for the administrator" test -n "$owner_file" -a "$owner_file" != null
check "Web upload is owned by the administrator's UID" \
  test "$(owner_uid "$data/spaces/private/owner/owner.txt")" = "$owner_id"
alice_file=$(upload /tmp/alice.jar "$alice_csrf" "$alice_private" /tmp/alice.txt alice.txt || true)
check "Web upload is owned by the member's UID" \
  test "$(owner_uid "$data/spaces/private/alice/alice.txt")" = "$alice_id"
check "Web download returns the uploaded bytes" \
  cmp -s /tmp/alice.txt <(curl -fsS -b /tmp/alice.jar "$api/api/v1/files/$alice_file/content")
check "member cannot list the administrator's private space" \
  test "$(status_of -b /tmp/alice.jar "$api/api/v1/spaces/$owner_private/entries")" != 200
check "member cannot download the administrator's file" \
  test "$(status_of -b /tmp/alice.jar "$api/api/v1/files/$owner_file/content")" != 200
check "administrator cannot list the member's private space" \
  test "$(status_of -b /tmp/owner.jar "$api/api/v1/spaces/$alice_private/entries")" != 200
shared_file=$(upload /tmp/alice.jar "$alice_csrf" "$shared" /tmp/shared.txt shared.txt || true)
check "Shared upload is owned by the member's UID" test "$(owner_uid "$data/spaces/shared/shared.txt")" = "$alice_id"
check "administrator reads the member's Shared file" \
  cmp -s /tmp/shared.txt <(curl -fsS -b /tmp/owner.jar "$api/api/v1/files/$shared_file/content")

# --- LAN Web entry (ADR 0012): plain HTTP through Caddy on the LAN address.
lan=http://$ip
lasting_login() { # URL USER PASSWORD JAR -> the session JSON of a login that asks to last
  curl -fsS -c "$4" -H 'Content-Type: application/json' \
    -d "{\"username\":\"$2\",\"password\":\"$3\",\"localConsole\":true}" "$1/api/v1/session"
}
check "Caddy serves the LAN entry" systemctl is-active --quiet caddy
check "installer replaced Caddy's own configuration" grep -Fqx '# Managed by A-NAS.' /etc/caddy/Caddyfile
check "LAN entry reaches the Product Service" test "$(curl -fsS "$lan/healthz" | jq -r .status)" = ok
check "local console login lasts until sign-out" \
  test "$(lasting_login "$api" alice "$alice_password" /tmp/console.jar | jq -r .expiresAt)" = 9999-12-31T23:59:59Z
lan_session=$(lasting_login "$lan" alice "$alice_password" /tmp/lan.jar || true)
lan_expiry=$(jq -r .expiresAt <<<"$lan_session" 2>/dev/null || true)
lan_csrf=$(jq -r .csrfToken <<<"$lan_session" 2>/dev/null || true)
check "LAN login is an ordinary session even when it asks to last" \
  test -n "$lan_expiry" -a "$lan_expiry" != null -a "$lan_expiry" != 9999-12-31T23:59:59Z
printf 'lan %s\n' "$(secret)" > /tmp/lan.txt
curl -fsS -o /dev/null -b /tmp/lan.jar -H "X-CSRF-Token: $lan_csrf" \
  -F "file=@/tmp/lan.txt;filename=lan.txt" "$lan/api/v1/spaces/$alice_private/uploads" || true
check "LAN upload is owned by the member's UID" test "$(owner_uid "$data/spaces/private/alice/lan.txt" 2>/dev/null)" = "$alice_id"

# --- Resumable uploads (ADR 0019): tus parts land in the member's own temporary.
tus_meta() { # KEY VALUE ... -> Upload-Metadata
  local pairs=()
  while [ $# -gt 1 ]; do pairs+=("$1 $(printf '%s' "$2" | base64 -w0)"); shift 2; done
  local IFS=,
  printf '%s' "${pairs[*]}"
}
header_of() { tr -d '\r' | sed -n "s/^$1: //Ip"; }
head -c 3000000 /dev/urandom > /tmp/resumable.bin
tus_location=$(curl -fsS -D - -o /dev/null -b /tmp/lan.jar -H "X-CSRF-Token: $lan_csrf" -H 'Tus-Resumable: 1.0.0' \
  -H 'Upload-Length: 3000000' -H "Upload-Metadata: $(tus_meta filename resumable.bin spaceId "$alice_private" relativePath 续传/2026)" \
  -X POST "$lan/api/v1/uploads" | header_of Location || true)
check "LAN upload address points back at the LAN entry" grep -q "^$lan/api/v1/uploads/" <<<"$tus_location"
tus_part() { # OFFSET COUNT
  head -c "$(($1 + $2))" /tmp/resumable.bin | tail -c "$2" | curl -fsS -o /dev/null -b /tmp/lan.jar -H "X-CSRF-Token: $lan_csrf" \
    -H 'Tus-Resumable: 1.0.0' -H "Upload-Offset: $1" -H 'Content-Type: application/offset+octet-stream' --data-binary @- -X PATCH "$tus_location"
}
tus_part 0 1000000 || true
resumed_dir="$data/spaces/private/alice/续传/2026"
temporary=$(find "$resumed_dir" -maxdepth 1 -name '.a-nas-upload-*' 2>/dev/null | head -n 1)
check "an unfinished upload is a hidden temporary owned by the member" \
  test -n "$temporary" -a "$(owner_uid "$temporary" 2>/dev/null)" = "$alice_id"
check "the NAS reports where the upload stopped" \
  test "$(curl -fsS -I -b /tmp/lan.jar -H 'Tus-Resumable: 1.0.0' "$tus_location" | header_of Upload-Offset)" = 1000000
tus_part 1000000 2000000 || true
check "the resumed upload saves the whole file" cmp -s /tmp/resumable.bin "$resumed_dir/resumable.bin"
check "the saved upload is owned by the member's UID" test "$(owner_uid "$resumed_dir/resumable.bin" 2>/dev/null)" = "$alice_id"
check "a saved upload leaves no temporary" test -z "$(find "$resumed_dir" -name '.a-nas-upload-*' 2>/dev/null)"
check "member cannot start an upload into the administrator's private space" \
  test "$(status_of -b /tmp/alice.jar -H "X-CSRF-Token: $alice_csrf" -H 'Tus-Resumable: 1.0.0' -H 'Upload-Length: 1' \
    -H "Upload-Metadata: $(tus_meta filename x.bin spaceId "$owner_private")" -X POST "$api/api/v1/uploads")" = 403
download_url=$(curl -fsS -b /tmp/alice.jar -H "X-CSRF-Token: $alice_csrf" -H 'Content-Type: application/json' \
  -d "{\"entryIds\":[\"$alice_file\"]}" "$api/api/v1/transfers/downloads" | jq -r .contentUrl || true)
check "a followed download returns the uploaded bytes" cmp -s /tmp/alice.txt <(curl -fsS -b /tmp/alice.jar "$api$download_url")

# --- Logs (ADR 0020): sign-ins are recorded with how the client connected,
# and the Host Agent reads A-NAS service logs from the journal under its
# production unit.
# shellcheck disable=SC2317,SC2329 # invoked through check (SC2317 in older shellcheck)
signed_in_from() { # VIA ADDRESS: the administrator sees alice's sign-in from there
  curl -fsS -b /tmp/owner.jar "$api/api/v1/activity?category=session&q=alice" | jq -e --arg via "$1" --arg address "$2" \
    'any(.items[]; .action == "session.signed_in" and .actor.username == "alice"
      and (.detail | fromjson | .via == $via and (.address // "") == $address))' >/dev/null
}
check "the activity log records the LAN sign-in with the client's address" signed_in_from lan "$ip"
check "the activity log records the local console sign-in" signed_in_from local-console ""
check "a member's activity holds only the member's own events" \
  test "$(curl -fsS -b /tmp/alice.jar "$api/api/v1/activity?limit=200" | jq '[.items[] | select(.actor.username == "owner" and .subject.username != "alice")] | length')" = 0
check "administrators read the Product Service's log through the Host Agent" \
  bash -c "curl -fsS -b /tmp/owner.jar '$api/api/v1/system-logs?service=api&q=A-NAS%20API%20listening' |
    jq -e '.dataSource == \"live\" and any(.items[]; .message == \"A-NAS API listening\" and .level == \"info\")' >/dev/null"
check "systemd's own lines about a unit are included" \
  bash -c "curl -fsS -b /tmp/owner.jar '$api/api/v1/system-logs?service=host-agent&q=Started' | jq -e '.items | length > 0' >/dev/null"
check "members cannot read system logs" test "$(status_of -b /tmp/alice.jar "$api/api/v1/system-logs")" = 403
check "a unit outside the allowlist is refused" test "$(status_of -b /tmp/owner.jar "$api/api/v1/system-logs?service=ssh")" = 400

# --- Settings → 终端: off until the device's operator turns it on, then a
# shell that the File Broker starts as the administrator inside the Host
# Agent's sandbox, reached through the LAN entry.
cat > /tmp/terminal-session.py <<'PY'
# URL JAR INPUT: types INPUT into a terminal session, prints what the shell
# wrote without escape sequences, then "close CODE REASON". A minimal RFC 6455
# client: Debian's Python has none in its standard library.
import base64, os, re, socket, struct, sys, urllib.parse
url, jar, typed = sys.argv[1], sys.argv[2], sys.argv[3]
target = urllib.parse.urlsplit(url)
cookies = []
for line in open(jar):
    line = line.removeprefix("#HttpOnly_")
    fields = line.rstrip("\n").split("\t")
    if not line.startswith("#") and len(fields) == 7:
        cookies.append(f"{fields[5]}={fields[6]}")
conn = socket.create_connection((target.hostname, target.port or 80), timeout=30)
conn.sendall((f"GET {target.path} HTTP/1.1\r\nHost: {target.netloc}\r\nOrigin: http://{target.netloc}\r\n"
              "Upgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Version: 13\r\n"
              f"Sec-WebSocket-Key: {base64.b64encode(os.urandom(16)).decode()}\r\n"
              f"Cookie: {'; '.join(cookies)}\r\n\r\n").encode())
reader = conn.makefile("rb")
status = reader.readline().decode().strip()
if " 101 " not in f"{status} ":
    sys.exit(f"handshake refused: {status}")
while reader.readline() not in (b"\r\n", b""):
    pass
def send(opcode, payload):
    mask = os.urandom(4)
    size = len(payload)
    length = bytes([0x80 | size]) if size < 126 else bytes([0x80 | 126]) + struct.pack(">H", size)
    conn.sendall(bytes([0x80 | opcode]) + length + mask + bytes(b ^ mask[i % 4] for i, b in enumerate(payload)))
send(1, b'{"type":"resize","cols":120,"rows":30}')
send(2, typed.encode())
output = bytearray()
while len(head := reader.read(2)) == 2:
    opcode, size = head[0] & 0x0F, head[1] & 0x7F
    if size == 126:
        size = struct.unpack(">H", reader.read(2))[0]
    elif size == 127:
        size = struct.unpack(">Q", reader.read(8))[0]
    payload = reader.read(size)
    if opcode in (0, 2):
        output += payload
    elif opcode == 9:
        send(10, payload)
    elif opcode == 8:
        code = struct.unpack(">H", payload[:2])[0] if len(payload) >= 2 else 1005
        # Bash marks each command it runs with bracketed-paste switches.
        text = re.sub(r"\x1b\[[0-9;?]*[ -/]*[@-~]|\x1b\][^\x07]*\x07", "", output.decode(errors="replace"))
        sys.stdout.write(text.replace("\r", ""))
        print(f"\nclose {code} {payload[2:].decode()}")
        sys.exit(0)
sys.exit("connection ended without a close frame")
PY
# terminal URL JAR INPUT
terminal() { timeout 60 python3 /tmp/terminal-session.py "$1/api/v1/terminal/session" "$2" "$3"; }
check "the terminal is off on a new device" \
  test "$(curl -fsS -b /tmp/owner.jar "$api/api/v1/terminal" | jq -r .enabled)" = false
check "a disabled terminal refuses sessions" denied terminal "$api" /tmp/owner.jar 'exit 0'
# The runbook step that turns it on.
printf 'ANAS_TERMINAL=enabled\n' >> /etc/a-nas/anas-api.env
systemctl restart anas-api
for _ in $(seq 1 100); do curl -fsS "$api/healthz" >/dev/null 2>&1 && break; sleep 0.2; done
check "the operator turns the terminal on" \
  test "$(curl -fsS -b /tmp/owner.jar "$api/api/v1/terminal" | jq -r .enabled)" = true
check "members cannot use the terminal" test "$(status_of -b /tmp/alice.jar "$api/api/v1/terminal")" = 403
curl -fsS -o /dev/null -c /tmp/owner-lan.jar -H 'Content-Type: application/json' \
  -d "{\"username\":\"owner\",\"password\":\"$owner_password\"}" "$lan/api/v1/session" || true
shell_output=$(terminal "$lan" /tmp/owner-lan.jar $'echo "uid:$(id -u)"\necho "cwd:$PWD"\nls '"$data"$'/spaces/private/alice >/dev/null 2>&1; echo "other:$?"\nexit 7\n' || true)
check "a LAN shell runs as the administrator's own UID" grep -qx "uid:$owner_id" <<<"$shell_output"
check "the shell starts in the administrator's private space" grep -qx "cwd:$data/spaces/private/owner" <<<"$shell_output"
check "the shell cannot list a member's private space" grep -qx 'other:[1-9][0-9]*' <<<"$shell_output"
check "the shell's exit code reaches the browser" grep -qx 'close 1000 exit 7' <<<"$shell_output"
check "the local console reaches the terminal too" \
  grep -qx 'close 1000 exit 0' <<<"$(terminal "$api" /tmp/owner.jar $'exit 0\n' || true)"
check "a member's session gets no shell" denied terminal "$lan" /tmp/lan.jar 'exit 0'

# --- No service identity reaches a space.
check "Product Service account cannot list a private space" denied as a-nas ls "$data/spaces/private/alice"
check "Product Service account cannot list Shared" denied as a-nas ls "$data/spaces/shared"
check "photo service cannot list Shared" denied as a-nas-photos ls "$data/spaces/shared"

# --- SMB: the same users, the same files, the same ACLs.
smb_auth() { printf 'username = %s\npassword = %s\n' "$1" "$2" > "/tmp/$1.smb"; chmod 0600 "/tmp/$1.smb"; }
smb_auth owner "$owner_password"
smb_auth alice "$alice_password"
smb() { local user=$1 share=$2; shift 2; smbclient "//$ip/$share" -A "/tmp/$user.smb" -m SMB3 --client-protection=encrypt -c "$*"; }
check "member opens her home over SMB and sees her Web upload" grep -q alice.txt <(smb alice alice ls 2>/dev/null)
printf 'from smb %s\n' "$(secret)" > /tmp/alice-smb.txt
check "member writes over SMB" quiet smb alice alice "put /tmp/alice-smb.txt alice-smb.txt"
check "SMB write is owned by the member's UID" \
  test "$(owner_uid "$data/spaces/private/alice/alice-smb.txt")" = "$alice_id"
check "Web lists the file written over SMB" grep -qx alice-smb.txt <(entries /tmp/alice.jar "$alice_private")
check "administrator cannot open the member's home over SMB" denied smb owner alice ls
check "member cannot open the administrator's home over SMB" denied smb alice owner ls
check "administrator reads Shared over SMB" quiet smb owner Shared "get shared.txt /tmp/shared-smb.txt"
check "SMB read of Shared returns the Web upload" cmp -s /tmp/shared.txt /tmp/shared-smb.txt

# --- One recycle bin per user for Web and SMB deletes.
trash_id=$(curl -fsS -X DELETE -b /tmp/alice.jar -H "X-CSRF-Token: $alice_csrf" "$api/api/v1/files/$alice_file" | jq -r .id || true)
check "Web delete removes the file from the space" \
  test ! -e "$data/spaces/private/alice/alice.txt" -a -n "$trash_id"
check "Web-deleted bytes are in the member's recycle bin" in_trash alice "$data/spaces/private/alice" /tmp/alice.txt
check "Web restore brings the file back" test "$(status_of -b /tmp/alice.jar -H "X-CSRF-Token: $alice_csrf" \
  -H 'Content-Type: application/json' -d '{"name":"alice.txt"}' "$api/api/v1/trash/$trash_id/restore")" = 200
check "SMB delete" quiet smb alice alice "del alice-smb.txt"
check "SMB delete appears in the member's Web recycle bin" \
  grep -qx alice-smb.txt <(curl -fsS -b /tmp/alice.jar "$api/api/v1/trash" | jq -r '.items[].name')
check "SMB-deleted bytes are in the same recycle bin" in_trash alice "$data/spaces/private/alice" /tmp/alice-smb.txt

# --- Photos: the photo service's own identity, the member's own library.
python3 - /tmp/photo.png <<'PY'
import struct, sys, zlib
def chunk(kind, data):
    return struct.pack(">I", len(data)) + kind + data + struct.pack(">I", zlib.crc32(kind + data))
rows = b"".join(b"\x00" + b"".join(bytes((x * 8, y * 8, 128)) for x in range(32)) for y in range(32))
png = b"\x89PNG\r\n\x1a\n" + chunk(b"IHDR", struct.pack(">IIBBBBB", 32, 32, 8, 2, 0, 0, 0))
png += chunk(b"IDAT", zlib.compress(rows)) + chunk(b"IEND", b"")
open(sys.argv[1], "wb").write(png)
PY
check "photo service becomes ready on its own" \
  bash -c "for _ in \$(seq 1 30); do journalctl -u anas-photos -o cat --no-pager | grep -Fq 'photo service ready' && exit 0; sleep 1; done; exit 1"
library_of() { curl -fsS -b "$1" "$api/api/v1/photos/libraries" | jq -r --arg user "$2" '[.items[] | select(.kind == "private" and .ownerUserId == $user)][0].id'; }
alice_user=$(curl -fsS -b /tmp/alice.jar "$api/api/v1/session" | jq -r .user.id || true)
alice_library=$(library_of /tmp/alice.jar "$alice_user" || true)
check "member uploads a photo" test "$(status_of -b /tmp/alice.jar -H "X-CSRF-Token: $alice_csrf" \
  -F file=@/tmp/photo.png "$api/api/v1/photos/libraries/$alice_library/uploads")" = 201
check "administrator does not see the member's photo library" \
  test "$(library_of /tmp/owner.jar "$alice_user")" = null
check "photo store belongs to the photo service alone" \
  test "$(stat -c '%a %U' "$data/photos")" = "700 a-nas-photos"
check "Product Service account cannot list the photo store" denied as a-nas ls "$data/photos"

# --- Local AI is built in (ADR 0016): started on demand, without network or
# data volume.
# shellcheck disable=SC2317,SC2329 # invoked through check (SC2317 in older shellcheck)
semantic_search() { curl -fsS -b /tmp/alice.jar --get --data-urlencode "q=$1" "$api/api/v1/photos/search" | jq -e '.semantic == true' >/dev/null; }
# shellcheck disable=SC2317,SC2329 # invoked through check (SC2317 in older shellcheck)
dynamic_identity() { # UNIT: runs under a UID systemd allocates for it, not root
  local uid
  uid=$(awk '/^Uid:/ {print $2}' "/proc/$(systemctl show -p MainPID --value "$1")/status")
  ((uid >= 61184 && uid <= 65519))
}
check "the installer enables the AI socket" systemctl is-enabled --quiet anas-ai.socket
check "only the photo service reaches the AI socket" \
  test "$(stat -c '%a %U %G' /run/a-nas-ai/ai.sock)" = "660 root a-nas-photos"
check "the release links the models and runtime kept once by content" \
  test "$(readlink /opt/a-nas/current/ai/model.litertlm)" = "/opt/a-nas/models/$(jq -r .sha256 "$ai/model.json")/$(jq -r .file "$ai/model.json")" \
  -a "$(readlink /opt/a-nas/current/ai/detector.onnx)" = "/opt/a-nas/models/$(jq -r .sha256 "$ai/detector.json")/$(jq -r .file "$ai/detector.json")" \
  -a "$(readlink /opt/a-nas/current/ai/runtime)" = "/opt/a-nas/ai-runtimes/$(jq -r .id "$ai/runtime.json")"
ai_cores=$(($(nproc) > 2 ? $(nproc) - 2 : 1))
check "the Worker may use every core but two" \
  test "$(systemctl show -p CPUQuotaPerSecUSec --value anas-ai.service)" = "${ai_cores}s"
check "the kernel reclaims the Worker's memory before its hard limit" \
  test "$(systemctl show -p MemoryHigh --value anas-ai.service):$(systemctl show -p MemoryMax --value anas-ai.service)" = 1879048192:2147483648
check "the Worker runs in its own slice, apart from the device's other work" \
  test "$(systemctl show -p Slice --value anas-ai.service)" = anas-ai.slice
check "background work gives way to the device's other work where weights compare" \
  test "$(systemctl show -p CPUWeight --value anas.slice):$(systemctl show -p Slice --value anas-ai.slice)" = 10:anas.slice
check "AI work yields to the pressure of the device's other work, not its own" \
  journal_has anas-photos.service "$installed_at" "AI work yields to the pressure of"
check "search starts the Worker through its socket" semantic_search 猫
check "the Worker runs under its own dynamic identity" dynamic_identity anas-ai.service
if [[ "${ANAS_SYSTEM_TEST_AI:-}" == real ]]; then
  check "the Worker serves the real model in its sandbox" \
    journal_has anas-ai.service "$installed_at" "serving $(jq -r .name "$ai/model.json")@$(jq -r .sha256 "$ai/model.json" | cut -c1-12)"
fi
# shellcheck disable=SC2317,SC2329 # invoked through check (SC2317 in older shellcheck)
organised() { # the member's photo has its vector and its detection
  local status
  for _ in $(seq 1 180); do
    status=$(curl -fsS -b /tmp/alice.jar "$api/api/v1/photos/ai" || true)
    jq -e --arg model "$detector_model" '.pending == 0 and .ready >= 1 and (.objectModel // "" | startswith($model))' <<<"$status" >/dev/null && return
    sleep 1
  done
  echo "$status"
  return 1
}
check "the Worker organises the photo with its vector and detector in its sandbox" organised
echo "INFO the Worker's memory peak: $(systemctl show -p MemoryPeak --value anas-ai.service) bytes"
# A probe in the Worker's own unit, in place of the Worker.
cat > /usr/local/lib/anas-ai-probe.py <<'PY'
import os, socket, sys
leaks = []
for family, kind in ((socket.AF_INET, socket.SOCK_STREAM), (socket.AF_INET6, socket.SOCK_STREAM), (socket.AF_NETLINK, socket.SOCK_DGRAM)):
    try:
        socket.socket(family, kind).close()
        leaks.append(f"opened a {family.name} socket")
    except OSError:
        pass
try:
    os.listdir("/srv/a-nas/data")
    leaks.append("listed the data volume")
except OSError:
    pass
print("; ".join(leaks) or "contained")
sys.exit(1 if leaks else 0)
PY
sed -e 's|^ExecStart=.*|ExecStart=/opt/a-nas/current/ai/runtime/bin/python /usr/local/lib/anas-ai-probe.py|' \
  -e 's|^Type=.*|Type=oneshot|' -e '/^Requires=/d' -e '/^After=/d' \
  /etc/systemd/system/anas-ai.service > /etc/systemd/system/anas-ai-probe.service
systemctl daemon-reload
check "the probe finds the network and data volume outside the sandbox" denied python3 /usr/local/lib/anas-ai-probe.py
check "the Worker's sandbox opens no network socket and cannot read the data volume" \
  systemctl start anas-ai-probe.service
journalctl -u anas-ai-probe.service -o cat --no-pager | grep -E 'contained|opened|listed' | tail -n 1 || true
staged_tampered=/tmp/tampered
stage "$staged_tampered"
rm "$staged_tampered/ai/model.litertlm"
head -c 1024 /dev/urandom > "$staged_tampered/ai/model.litertlm"
jq --arg sha "$(head -c 1024 /dev/urandom | sha256sum | cut -d ' ' -f 1)" '.sha256 = $sha | .sizeBytes = 1024' \
  "$ai/model.json" > "$staged_tampered/ai/model.json"
check "a staged model that does not match its manifest is refused" \
  denied bash /src/scripts/install-v1.0.1-system-services.sh "$staged_tampered" tampered "$iface"
check "the refused release changes nothing" \
  test "$(readlink /opt/a-nas/current)" = /opt/a-nas/releases/system-test -a ! -e /opt/a-nas/releases/tampered \
  -a "$(find /opt/a-nas/models -mindepth 1 -maxdepth 1 | wc -l)" = 2
staged_tampered=/tmp/tampered-detector
stage "$staged_tampered"
rm "$staged_tampered/ai/detector.onnx"
head -c 1024 /dev/urandom > "$staged_tampered/ai/detector.onnx"
jq --arg sha "$(head -c 1024 /dev/urandom | sha256sum | cut -d ' ' -f 1)" '.sha256 = $sha | .sizeBytes = 1024' \
  "$ai/detector.json" > "$staged_tampered/ai/detector.json"
check "a staged detector that does not match its manifest is refused" \
  denied bash /src/scripts/install-v1.0.1-system-services.sh "$staged_tampered" tampered "$iface"
check "the refused detector changes nothing" \
  test "$(readlink /opt/a-nas/current)" = /opt/a-nas/releases/system-test -a ! -e /opt/a-nas/releases/tampered \
  -a "$(find /opt/a-nas/models -mindepth 1 -maxdepth 1 | wc -l)" = 2

# --- Media center (ADR 0017): files are read as the user through the File
# Broker; FFmpeg runs in a per-connection worker without network, data volume
# or Product Service state.
check "the installer enables the media worker socket" systemctl is-enabled --quiet anas-media.socket
check "only the Product Service reaches the media worker socket" \
  test "$(stat -c '%a %U %G' /run/a-nas-media/media.sock)" = "660 root a-nas"
movies_dir=$(curl -fsS -b /tmp/owner.jar -H "X-CSRF-Token: $owner_csrf" -H 'Content-Type: application/json' \
  -d '{"parentId":"","name":"Movies"}' "$api/api/v1/spaces/$shared/directories" | jq -r .id || true)
ffmpeg -hide_banner -loglevel error -f lavfi -i testsrc2=size=640x360:rate=25:duration=6 -f lavfi -i sine=duration=6 \
  -c:v libx264 -pix_fmt yuv420p -c:a ac3 /tmp/heat.mkv
curl -fsS -o /dev/null -b /tmp/owner.jar -H "X-CSRF-Token: $owner_csrf" -F "parentId=$movies_dir" \
  -F "file=@/tmp/heat.mkv;filename=Heat (1995).mkv" "$api/api/v1/spaces/$shared/uploads" || true
# Ordinary file previews use the same sandbox without creating a media library.
video_file=$(curl -fsS -b /tmp/owner.jar "$api/api/v1/spaces/$shared/entries?parentId=$movies_dir" | jq -r '.items[0].id')
check "file management renders a video thumbnail" test "$(status_of -b /tmp/alice.jar "$api/api/v1/files/$video_file/preview?size=thumbnail")" = 200
ffmpeg -hide_banner -loglevel error -f lavfi -i color=c=orange:s=640x480 -frames:v 1 /tmp/preview.png
heif-enc -o /tmp/preview.heic /tmp/preview.png >/tmp/heif-encode.log 2>&1
heif_file=$(curl -fsS -b /tmp/owner.jar -H "X-CSRF-Token: $owner_csrf" -F 'parentId=' \
  -F 'file=@/tmp/preview.heic;filename=preview.HEIC' "$api/api/v1/spaces/$shared/uploads" | jq -r .id)
for preview_size in thumbnail preview; do
  check "file management renders HEIC $preview_size under DynamicUser" \
    curl -fsS -b /tmp/alice.jar "$api/api/v1/files/$heif_file/preview?size=$preview_size" -o /tmp/heif-preview.png
  check "the HEIC $preview_size is a PNG" test "$(od -An -tx1 -N8 /tmp/heif-preview.png | tr -d ' \n')" = 89504e470d0a1a0a
done
curl -fsS -b /tmp/alice.jar "$api/api/v1/files/$heif_file/content" -o /tmp/heif-original.heic
check "HEIC download remains the original" cmp /tmp/preview.heic /tmp/heif-original.heic
check "Product Service cannot open the HEIC source itself" denied as a-nas cat "$data/spaces/shared/preview.HEIC"

media_library=$(curl -fsS -b /tmp/owner.jar -H "X-CSRF-Token: $owner_csrf" -H 'Content-Type: application/json' \
  -d "{\"name\":\"Movies\",\"kind\":\"movies\",\"spaceId\":\"$shared\",\"folderIds\":[\"$movies_dir\"]}" \
  "$api/api/v1/media/libraries" | jq -r .id || true)
check "the administrator creates a shared media library" test -n "$media_library" -a "$media_library" != null
check "a member cannot create a shared media library" test "$(status_of -b /tmp/alice.jar -H "X-CSRF-Token: $alice_csrf" \
  -H 'Content-Type: application/json' -d "{\"name\":\"Mine\",\"kind\":\"movies\",\"spaceId\":\"$shared\",\"folderIds\":[\"$movies_dir\"]}" \
  "$api/api/v1/media/libraries")" = 403
film_duration=0
for _ in $(seq 1 60); do
  film_duration=$(curl -fsS -b /tmp/alice.jar "$api/api/v1/media/titles?category=movie" | jq -r '.items[0].duration // 0' || echo 0)
  [[ "$film_duration" != 0 ]] && break
  sleep 1
done
film=$(curl -fsS -b /tmp/alice.jar "$api/api/v1/media/titles?category=movie" | jq -r '.items[0].id' || true)
check "the member sees the scanned film" test "$(curl -fsS -b /tmp/alice.jar "$api/api/v1/media/videos/$film" | jq -r .title)" = Heat
check "the media worker reads the film's duration in its sandbox" test "${film_duration%.*}" -ge 5
check "the member plays the original with range requests" \
  test "$(status_of -b /tmp/alice.jar -H 'Range: bytes=0-99' "$api/api/v1/media/videos/$film/file")" = 206
check "the media worker converts the film to fragmented MP4" bash -c \
  "curl -s --max-time 60 -b /tmp/alice.jar '$api/api/v1/media/videos/$film/stream?mode=transcode&start=1&audio=0&quality=original' | head -c 64 | grep -aq ftyp"
check "the media worker takes a frame of the film" bash -c \
  "for _ in \$(seq 1 30); do [[ \$(curl -s -o /dev/null -w '%{http_code}' -b /tmp/alice.jar '$api/api/v1/media/artwork/$film/thumb') == 200 ]] && exit 0; sleep 1; done; exit 1"
check "Product Service account cannot read the film itself" denied as a-nas cat "$data/spaces/shared/Movies/Heat (1995).mkv"

# --- Music (ADR 0018) indexes the same files in place and has the shared
# media worker probe and decode the formats browsers cannot play.
ffmpeg -hide_banner -loglevel error -f lavfi -i sine=frequency=440:duration=5 -c:a wavpack \
  -metadata title=Aria -metadata artist=Quartet /tmp/aria.wv
curl -fsS -o /dev/null -b /tmp/owner.jar -H "X-CSRF-Token: $owner_csrf" -F "parentId=" \
  -F "file=@/tmp/aria.wv;filename=aria.wv" "$api/api/v1/spaces/$shared/uploads" || true
curl -fsS -o /dev/null -b /tmp/alice.jar -H "X-CSRF-Token: $alice_csrf" -X POST "$api/api/v1/music/library/scan" || true
for _ in $(seq 1 60); do
  [[ "$(curl -fsS -b /tmp/alice.jar "$api/api/v1/music/library" | jq -r .scan.state || true)" == idle ]] && break
  sleep 1
done
song=$(curl -fsS -b /tmp/alice.jar "$api/api/v1/music/tracks" | jq -c '.items[] | select(.title == "Aria")' || true)
check "the member's music library holds the WavPack song with its tags" \
  test "$(jq -r '.artist + " " + (.transcode | tostring)' <<<"${song:-null}" 2>/dev/null)" = "Quartet true"
check "the media worker reads the song's duration in its sandbox" test "$(jq -r '.durationMs // 0' <<<"${song:-null}" 2>/dev/null)" -ge 4900
check "the media worker decodes the song to FLAC from a start position" bash -c \
  "curl -s --max-time 60 -b /tmp/alice.jar '$api/api/v1/music/tracks/$(jq -r .id <<<"${song:-null}" 2>/dev/null)/stream?start=1' | head -c 4 | grep -aq fLaC"
# A probe in the media worker's own unit, in place of the worker.
cat > /usr/local/lib/anas-media-probe.py <<'PY'
import os, socket, sys
leaks = []
for family, kind in ((socket.AF_INET, socket.SOCK_STREAM), (socket.AF_INET6, socket.SOCK_STREAM), (socket.AF_NETLINK, socket.SOCK_DGRAM)):
    try:
        socket.socket(family, kind).close()
        leaks.append(f"opened a {family.name} socket")
    except OSError:
        pass
for path in ("/srv/a-nas/data", "/var/lib/a-nas", "/run/a-nas"):
    try:
        os.listdir(path)
        leaks.append(f"listed {path}")
    except OSError:
        pass
if not 61184 <= os.getuid() <= 65519:
    leaks.append(f"ran as UID {os.getuid()}")
print("; ".join(leaks) or "contained")
sys.exit(1 if leaks else 0)
PY
sed -e 's|^ExecStart=.*|ExecStart=/usr/bin/python3 /usr/local/lib/anas-media-probe.py|' -e 's|^Type=.*|Type=oneshot|' \
  -e '/^StandardInput=/d' /etc/systemd/system/anas-media@.service > /etc/systemd/system/anas-media-probe.service
systemctl daemon-reload
check "the media probe finds the network and data volume outside the sandbox" denied python3 /usr/local/lib/anas-media-probe.py
check "the media worker's sandbox has no network, data volume or Product Service state" \
  systemctl start anas-media-probe.service
journalctl -u anas-media-probe.service -o cat --no-pager | grep -E 'contained|opened|listed|ran as' | tail -n 1 || true

# --- One password for Web and SMB.
new_password=$(secret)
check "member changes her password on the Web" test "$(status_of -b /tmp/alice.jar -H "X-CSRF-Token: $alice_csrf" \
  -H 'Content-Type: application/json' -d "{\"currentPassword\":\"$alice_password\",\"newPassword\":\"$new_password\"}" \
  "$api/api/v1/session/password")" = 204
check "the old password no longer opens SMB" denied smb alice alice ls
smb_auth alice "$new_password"
check "the new password opens SMB" quiet smb alice alice ls

# --- Restarting the Host Agent keeps all of the above.
restarted_at=$(date '+%Y-%m-%d %H:%M:%S')
systemctl restart anas-host-agent
check "Host Agent proves the identity switch again after a restart" \
  bash -c "for _ in \$(seq 1 30); do journalctl -u anas-host-agent --since '$restarted_at' -o cat --no-pager |
    grep -Fq 'file broker identity switch verified' && exit 0; sleep 1; done; exit 1"
login alice "$new_password" /tmp/alice.jar >/dev/null || true
check "Web files work after the restart" grep -qx alice.txt <(entries /tmp/alice.jar "$alice_private")

# --- Screen saver videos are long-lived media, not part of a release (ADR 0014).
pools=/var/lib/a-nas/screensavers
media=/opt/a-nas/current/install-screensavers.sh
stager=/tmp/anas-dev # the staging account's home
staged=$stager/apps/a-nas/screensavers
uploads=0
# install_release NAME: the real installer on the release staged in /tmp/NAME,
# then wait until the restarted Product Service answers and the photo service
# is ready again.
# shellcheck disable=SC2317,SC2329 # invoked through check (SC2317 in older shellcheck)
install_release() {
  local since
  since=$(date '+%Y-%m-%d %H:%M:%S')
  bash /src/scripts/install-v1.0.1-system-services.sh "/tmp/$1" "$1" "$iface" >"/tmp/$1.log" 2>&1 || return
  for _ in $(seq 1 150); do
    curl -fsS "$api/healthz" >/dev/null 2>&1 &&
      journalctl -u anas-photos --since "$since" -o cat --no-pager | grep -Fq 'photo service ready' && return
    sleep 0.2
  done
  return 1
}
pool_size() { curl -fsS "$api/local-console/screensavers" | jq '.videos | length'; }
current_pool() { readlink "$pools/current" || true; }
# new_video: a stand-in video at /tmp/HASH.mp4; prints HASH.
new_video() {
  local file hash
  file=$(mktemp /tmp/video.XXXXXX)
  head -c 4096 /dev/urandom > "$file"
  hash=$(sha256sum "$file" | cut -d ' ' -f 1)
  mv "$file" "/tmp/$hash.mp4"
  echo "$hash"
}
# stage_pool HASH...: stages a pool as scripts/deploy-screensavers.ps1 does,
# uploading only the videos the staging area lacks; sets pool to its ID.
stage_pool() {
  local hash
  for hash in $(HOME=$stager bash /src/scripts/remote-stage-screensavers.sh missing "$@"); do
    cp "/tmp/$hash.mp4" "$staged/objects/$hash.mp4.incoming"
    uploads=$((uploads + 1))
  done
  pool=$(HOME=$stager bash /src/scripts/remote-stage-screensavers.sh commit "$@")
}
check "a first install plays no screen saver" test "$(current_pool):$(pool_size)" = :0
v1=$(new_video)
v2=$(new_video)
v3=$(new_video)
# A release staged v1 the way deploy-dev.ps1 did before issue #49.
install -d "$stager/apps/a-nas/releases/old/screensavers"
cp "/tmp/$v1.mp4" "$stager/apps/a-nas/releases/old/screensavers/screensaver-000.mp4"
printf 'screensaver_000_sha256=%s\n' "$v1" > "$stager/apps/a-nas/releases/old/RELEASE"
stage_pool "$v1" "$v2"
pool_a=$pool
check "staging uploads only the videos the NAS lacks" test "$uploads" = 1
check "a staged pool installs" quiet "$media" "$pool_a" "$staged"
check "it becomes the current pool" test "$(current_pool):$(pool_size)" = "pools/$pool_a:2"
stage /tmp/upgrade
check "a release installs" install_release upgrade
check "the upgrade keeps the operator's terminal opt-in" \
  test "$(grep -cx 'ANAS_TERMINAL=enabled' /etc/a-nas/anas-api.env):$(curl -fsS -b /tmp/owner.jar "$api/api/v1/terminal" | jq -r .enabled)" = 1:true
check "the upgrade keeps the current pool" test "$(current_pool):$(pool_size)" = "pools/$pool_a:2"
check "the upgrade copies neither the models nor the runtime again" \
  test "$(find /opt/a-nas/models /opt/a-nas/ai-runtimes -mindepth 1 -maxdepth 1 | wc -l)" = 3 \
  -a "$(readlink /opt/a-nas/current/ai/model.litertlm)" = "$(readlink /opt/a-nas/releases/system-test/ai/model.litertlm)" \
  -a "$(readlink /opt/a-nas/current/ai/detector.onnx)" = "$(readlink /opt/a-nas/releases/system-test/ai/detector.onnx)"
login alice "$new_password" /tmp/alice.jar >/dev/null || true
check "search reaches the upgraded release's Worker" semantic_search 猫
stage_pool "$v2" "$v3"
pool_b=$pool
check "a new pool uploads only its new video" test "$uploads" = 2
check "the new pool installs" quiet "$media" "$pool_b" "$staged"
check "it replaces the current pool" test "$(current_pool)" = "pools/$pool_b"
check "a video both pools hold is stored once" \
  test "$(find "$pools/objects" -type f | wc -l):$(stat -c %h "$pools/objects/$v2.mp4")" = 3:3
check "installing the same pool again changes nothing" quiet "$media" "$pool_b" "$staged"
check "switching back to an installed pool needs no staging" quiet "$media" "$pool_a" /nonexistent
check "the media rollback plays the old pool" test "$(current_pool)" = "pools/$pool_a"
v4=$(new_video)
stage_pool "$v4"
printf x >> "$staged/objects/$v4.mp4"
check "a video that does not match its hash is refused" denied "$media" "$pool" "$staged"
check "the refused pool changes nothing" \
  test "$(current_pool)" = "pools/$pool_a" -a ! -e "$pools/objects/$v4.mp4" -a ! -e "$pools/pools/$pool"
cp "$staged/pools/$pool_b.sums" "$staged/pools/0123456789abcdef.sums"
check "a manifest that is not the named pool is refused" denied "$media" 0123456789abcdef "$staged"
# An installer before issue #49 left a pool named after its release, and no
# "current".
legacy=$pools/legacy-release
install -d -o root -g a-nas -m 0750 "$legacy"
v5=$(new_video)
v6=$(new_video)
install -o root -g a-nas -m 0640 "/tmp/$v5.mp4" "$legacy/screensaver-000.mp4"
install -o root -g a-nas -m 0640 "/tmp/$v6.mp4" "$legacy/screensaver-001.mp4"
rm "$pools/current"
stage /tmp/first-upgrade
check "the first upgrade from an older installer installs" install_release first-upgrade
check "it adopts the pool that installer left, by linking" \
  test "$(pool_size):$(stat -c %h "$legacy/screensaver-000.mp4")" = 2:3
check "the adopted pool is the current one" grep -q "$v6" "$pools/current/SHA256SUMS"
check "the Product Service reads the current pool" \
  grep -Fqx "ANAS_SCREENSAVER_DIRECTORY=$pools/current" /etc/a-nas/anas-api.env
video=$(curl -fsS "$api/local-console/screensavers" | jq -r '.videos[0]' || true)
check "a current pool video answers a byte range" test "$(status_of -r 0-1023 "$api$video")" = 206

# --- Last, because other services keep the detached volume busy: the
# unmount reaches the photo service's private mount namespace, and it stops
# serving from the lost store instead of following the path.
lost_at=$(date '+%Y-%m-%d %H:%M:%S')
umount --lazy "$data"
check "photo service notices the data volume is gone" \
  bash -c "for _ in \$(seq 1 15); do journalctl -u anas-photos --since '$lost_at' -o cat --no-pager |
    grep -Fq 'photo store lost' && exit 0; sleep 1; done; exit 1"
check "photos answer unavailable while the volume is gone" \
  test "$(status_of -b /tmp/alice.jar "$api/api/v1/photos/libraries")" = 503

echo "failures=$failures"
[[ $failures -eq 0 ]]
