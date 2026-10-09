#!/usr/bin/env bash
# System test of the multi-user permission model. It installs the built
# release with the real installer and systemd units into a disposable Debian
# 13 container where systemd is PID 1, then checks every entry point that
# touches the data volume: Web files (File Broker workers), SMB, the photo API
# and the LAN Web entry that Caddy serves on port 80. Unlike tests that start
# the daemons by hand, it runs them under the production sandboxes, so it
# catches a unit that takes the Host Agent's CAP_SETUID (issue #38).
#
# Usage: make build-binaries && scripts/build-system-test-image.sh &&
#        scripts/system-test.sh [IMAGE]
# Prints PASS/FAIL lines and exits non-zero on any failure.
set -euo pipefail

if [[ "${1:-}" != --inside ]]; then
  image=${1:-anas-systemd:trixie}
  repo=$(cd "$(dirname "$0")/.." && pwd)
  for binary in anas-api anas-host-agent; do
    [[ -x "$repo/build/$binary" ]] || { echo "missing build/$binary: run make build-binaries" >&2; exit 2; }
  done
  name="anas-system-test-$$"
  docker run -d --name "$name" --privileged --cgroupns=host -v /sys/fs/cgroup:/sys/fs/cgroup:rw \
    --tmpfs /run --tmpfs /run/lock -v "$repo:/src:ro" "$image" >/dev/null
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

# The release as scripts/deploy-dev.ps1 stages it.
release=/tmp/release
install -d "$release"
install -m 0755 /src/build/anas-api /src/build/anas-host-agent "$release/"
for unit in anas-api anas-host-agent anas-photos; do
  install -m 0644 "/src/deploy/systemd/system/$unit.service" "$release/$unit-system.service"
done
install -m 0644 /src/deploy/chromium/policies/managed/a-nas.json "$release/a-nas-chromium-policy.json"
install -m 0644 /src/deploy/caddy/Caddyfile "$release/Caddyfile"

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
# Service's volume guard accepts it, then restart it to load the record.
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

echo "failures=$failures"
[[ $failures -eq 0 ]]
