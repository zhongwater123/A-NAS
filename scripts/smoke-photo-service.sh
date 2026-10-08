#!/usr/bin/env bash
# End-to-end smoke test of the photo service (ADR 0011). It runs the real Host
# Agent as root, the photo service as a-nas-photos and the Product Service as
# a-nas on a loop-mounted Btrfs volume, then drives the photo API through the
# Product Service. It creates accounts, mounts and daemons: run it only as root
# in a disposable privileged container with the repository at /src; see
# docs/development/LOCAL_ENVIRONMENT.md. Prints PASS/FAIL lines.
set -euo pipefail
if [[ $EUID -ne 0 || ! -f /.dockerenv ]]; then
  echo "run as root inside a disposable container" >&2
  exit 2
fi
export PATH=/usr/local/go/bin:$PATH
cd /src
if ! command -v curl >/dev/null || ! command -v dmsetup >/dev/null; then
  apt-get update -qq >/dev/null && apt-get install -y -qq curl dmsetup >/dev/null 2>&1
fi
go build -o /tmp/bin/ ./cmd/anas-api ./cmd/anas-host-agent
cat > /tmp/genpng.go <<'GO'
package main

import (
	"image"
	"image/color"
	"image/png"
	"os"
)

func main() {
	img := image.NewRGBA(image.Rect(0, 0, 800, 600))
	for y := range 600 {
		for x := range 800 {
			img.Set(x, y, color.RGBA{uint8(x), uint8(y), 128, 255})
		}
	}
	file, _ := os.Create(os.Args[1])
	_ = png.Encode(file, img)
	_ = file.Close()
}
GO
go run /tmp/genpng.go /tmp/e2e.png

groupadd --system a-nas
useradd --system --gid a-nas --no-create-home --home-dir /var/lib/a-nas --shell /usr/sbin/nologin a-nas
groupadd --system --gid 31000 a-nas-photos
useradd --system --uid 31000 --gid 31000 --no-create-home --home-dir /nonexistent --shell /usr/sbin/nologin a-nas-photos
usermod -aG a-nas-photos a-nas
install -d -o a-nas -g a-nas -m 0700 /var/lib/a-nas
install -d -o root -g a-nas -m 0750 /srv/a-nas /srv/a-nas/data
# Larger than the 10 GiB capacity reserve; sparse, so it costs little disk.
truncate --size=64G /tmp/data.img
# The volume sits on a device-mapper device so that a disk failure can be
# injected later. Device-mapper and loop devices belong to the host kernel,
# not the container: remove them on every exit.
loop=$(losetup --find --show /tmp/data.img)
volume=anas-smoke-$(hostname)
# shellcheck disable=SC2317,SC2329 # invoked by the EXIT trap (SC2317 in older shellcheck)
cleanup() {
  # Stop everything that holds the volume, then release it. A deferred
  # removal finishes on its own once the last holder is gone.
  pkill -KILL -f /tmp/bin/ 2>/dev/null || true
  pkill -KILL smbd 2>/dev/null || true
  sleep 0.5
  umount /srv/a-nas/data 2>/dev/null || umount --lazy /srv/a-nas/data 2>/dev/null || true
  dmsetup remove "$volume" 2>/dev/null || dmsetup remove --deferred "$volume" 2>/dev/null || true
  losetup --detach "$loop" 2>/dev/null || true
}
trap cleanup EXIT
linear_table="0 $(blockdev --getsz "$loop") linear $loop 0"
dmsetup create --noudevsync "$volume" --table "$linear_table"
dmsetup mknodes "$volume"
mkfs.btrfs --quiet "/dev/mapper/$volume"
mount "/dev/mapper/$volume" /srv/a-nas/data
# Like the Experimental NAS, the volume root lets nobody else pass through.
chmod 0750 /srv/a-nas/data
btrfs subvolume create /srv/a-nas/data/spaces >/dev/null
printf '{"filesystemUuid":"e2e","formatVersion":1}\n' > /srv/a-nas/data/.a-nas-volume.json
mkdir -p /etc/samba
printf '[global]\n    server role = standalone server\n    smb ports = 445\n    disable netbios = yes\n' > /etc/samba/smb.conf
smbd --daemon
# systemd's RuntimeDirectory= for anas-photos.service.
install -d -o a-nas-photos -g a-nas-photos -m 0750 /run/a-nas-photos

env ANAS_HOST_AGENT_SOCKET=/run/a-nas/host-agent.sock ANAS_FILE_BROKER_SOCKET=/run/a-nas/file-broker.sock \
  ANAS_STATE_DIR=/var/lib/a-nas ANAS_HOST_AGENT_GROUP=a-nas ANAS_DATA_MOUNT=/srv/a-nas/data \
  ANAS_PHOTO_SESSION_SOCKET=/run/a-nas-sessions/photos.sock ANAS_PHOTO_SESSION_GROUP=a-nas-photos \
  /tmp/bin/anas-host-agent > /tmp/host-agent.log 2>&1 &
for _ in $(seq 1 100); do [ -S /run/a-nas-sessions/photos.sock ] && break; sleep 0.2; done

start_photo_service() {
  setpriv --reuid=a-nas-photos --regid=a-nas-photos --init-groups env \
    ANAS_PHOTOS_ROOT=/srv/a-nas/data/photos ANAS_PHOTOS_SOCKET=/run/a-nas-photos/photos.sock \
    ANAS_PHOTO_SESSION_SOCKET=/run/a-nas-sessions/photos.sock \
    /tmp/bin/anas-api photo-service >> /tmp/photos.log 2>&1 &
  photo_service=$!
}
start_photo_service
setpriv --reuid=a-nas --regid=a-nas --init-groups env \
  ANAS_HTTP_ADDR=127.0.0.1:8080 ANAS_HOSTSTATE_MODE=agent ANAS_HOST_AGENT_SOCKET=/run/a-nas/host-agent.sock \
  ANAS_FILE_BROKER_SOCKET=/run/a-nas/file-broker.sock ANAS_STATE_DIR=/var/lib/a-nas ANAS_DATA_MOUNT=/srv/a-nas/data \
  ANAS_PHOTOS_SOCKET=/run/a-nas-photos/photos.sock \
  /tmp/bin/anas-api > /tmp/api.log 2>&1 &
for _ in $(seq 1 100); do curl -fsS http://127.0.0.1:8080/healthz >/dev/null 2>&1 && break; sleep 0.2; done

failures=0
# check NAME COMMAND...: runs the command and records the outcome.
check() {
  local name=$1
  shift
  if "$@"; then echo "PASS $name"; else echo "FAIL $name"; failures=$((failures + 1)); fi
}
# denied COMMAND...: succeeds when the command fails, quietly.
# shellcheck disable=SC2317,SC2329 # invoked through check (SC2317 in older shellcheck)
denied() { ! "$@" >/dev/null 2>&1; }
# shellcheck disable=SC2317,SC2329 # invoked through check (SC2317 in older shellcheck)
quiet() { "$@" >/dev/null 2>&1; }
as() { local user=$1; shift; setpriv --reuid="$user" --regid="$user" --init-groups "$@"; }
mode_of() { stat -c '%a %U:%G' "$1"; }
status_of() { curl -s -o /dev/null -w '%{http_code}' "$@"; }
api=http://127.0.0.1:8080

check "photos store is a-nas-photos 0700" test "$(mode_of /srv/a-nas/data/photos)" = "700 a-nas-photos:a-nas-photos"
check "photos store is a Btrfs subvolume" quiet btrfs subvolume show /srv/a-nas/data/photos
check "session socket is root:a-nas-photos 0660" test "$(mode_of /run/a-nas-sessions/photos.sock)" = "660 root:a-nas-photos"
check "photo socket is a-nas-photos 0660" test "$(mode_of /run/a-nas-photos/photos.sock)" = "660 a-nas-photos:a-nas-photos"

session=$(curl -fsS -c /tmp/jar -H 'Content-Type: application/json' \
  -d '{"username":"owner","password":"e2e owner password"}' "$api/api/v1/setup/admin")
csrf=$(printf '%s' "$session" | sed -E 's/.*"csrfToken":"([^"]+)".*/\1/')
status=000
for _ in $(seq 1 50); do
  status=$(curl -s -o /tmp/libraries.json -w '%{http_code}' -b /tmp/jar "$api/api/v1/photos/libraries")
  [ "$status" = 200 ] && break
  sleep 0.2
done
check "libraries through the proxy" test "$status" = 200
library=$(sed -E 's/.*"id":"(library:[0-9a-f]+)","kind":"private".*/\1/' /tmp/libraries.json)
uploads="$api/api/v1/photos/libraries/$library/uploads"
code=$(curl -s -o /tmp/upload.json -w '%{http_code}' -b /tmp/jar -H "X-CSRF-Token: $csrf" -F file=@/tmp/e2e.png "$uploads")
check "upload through the proxy" test "$code" = 201
check "upload without CSRF is refused" test "$(status_of -b /tmp/jar -F file=@/tmp/e2e.png "$uploads")" = 403
asset=$(sed -E 's/.*"id":"(photo:[0-9a-f]+)".*/\1/' /tmp/upload.json)
thumbnail_ready() { curl -s -b /tmp/jar "$api/api/v1/photos/assets/$asset" | grep -q '"thumbnail":"ready"'; }
for _ in $(seq 1 50); do thumbnail_ready && break; sleep 0.2; done
check "thumbnail rendered by the photo service" thumbnail_ready
check "thumbnail served" test "$(status_of -b /tmp/jar "$api/api/v1/photos/assets/$asset/thumbnail")" = 200
# shellcheck disable=SC2317,SC2329 # invoked through check (SC2317 in older shellcheck)
original_matches() { curl -s -b /tmp/jar "$api/api/v1/photos/assets/$asset/original" | cmp -s - /tmp/e2e.png; }
check "original bytes round-trip" original_matches
check "objects belong to a-nas-photos" test "$(find /srv/a-nas/data/photos/objects -type f -printf '%U %m\n' | sort -u)" = "31000 400"
check "Product Service cannot list the store" denied as a-nas ls /srv/a-nas/data/photos
check "administrator cannot read the catalog" denied as owner cat /srv/a-nas/data/photos/catalog.db
check "photo service cannot list spaces" denied as a-nas-photos ls /srv/a-nas/data/spaces/shared
check "forged token at the photo socket is refused" test "$(as a-nas curl -s -o /dev/null -w '%{http_code}' \
  --unix-socket /run/a-nas-photos/photos.sock -H 'X-A-NAS-Session: forged' http://photos/api/v1/photos/libraries)" = 401
check "members outside the group cannot reach the photo socket" denied as owner curl -fs \
  --unix-socket /run/a-nas-photos/photos.sock http://photos/api/v1/photos/libraries
check "photo service logged ready" grep -q "photo service ready" /tmp/photos.log

# A member's private library: hidden from the administrator until a
# library viewing grant, read-only during it, hidden again after it ends.
json_field() { sed -E "s/.*\"$1\":\"([^\"]+)\".*/\1/"; }
member=$(curl -fsS -b /tmp/jar -H "X-CSRF-Token: $csrf" -H 'Content-Type: application/json' \
  -d '{"username":"alice","password":"alice e2e password"}' "$api/api/v1/users")
alice_id=$(printf '%s' "$member" | json_field id)
alice_csrf=$(curl -fsS -c /tmp/alice-jar -H 'Content-Type: application/json' \
  -d '{"username":"alice","password":"alice e2e password"}' "$api/api/v1/session" | json_field csrfToken)
curl -fsS -b /tmp/alice-jar "$api/api/v1/photos/libraries" > /tmp/alice-libraries.json
alice_library=$(sed -E 's/.*"id":"(library:[0-9a-f]+)","kind":"private".*/\1/' /tmp/alice-libraries.json)
curl -fsS -o /tmp/alice-upload.json -b /tmp/alice-jar -H "X-CSRF-Token: $alice_csrf" -F file=@/tmp/e2e.png \
  "$api/api/v1/photos/libraries/$alice_library/uploads"
alice_asset=$(json_field id < /tmp/alice-upload.json)
check "duplicate hint names only the other member" grep -q '"alsoKeptBy":\["owner"\]' /tmp/alice-upload.json
alice_timeline="$api/api/v1/photos/libraries/$alice_library/timeline"
check "administrator cannot see a member's library" test "$(status_of -b /tmp/jar "$alice_timeline")" = 404
grant_id=$(curl -fsS -b /tmp/jar -H "X-CSRF-Token: $csrf" -H 'Content-Type: application/json' \
  -d '{"password":"e2e owner password","reason":"e2e smoke","scope":"library"}' "$api/api/v1/users/$alice_id/viewing" | json_field id)
check "library grant opens the member's library" test "$(status_of -b /tmp/jar "$alice_timeline")" = 200
check "library grant is read-only" test "$(status_of -X DELETE -b /tmp/jar -H "X-CSRF-Token: $csrf" "$api/api/v1/photos/assets/$alice_asset")" = 403
shared_library=$(sed -E 's/.*"id":"(library:[0-9a-f]+)","kind":"shared".*/\1/' /tmp/alice-libraries.json)
check "library grant cannot copy photos out" test "$(status_of -b /tmp/jar -H "X-CSRF-Token: $csrf" -H 'Content-Type: application/json' \
  -d "{\"libraryId\":\"$shared_library\"}" "$api/api/v1/photos/assets/$alice_asset/copies")" = 403
check "library grant does not open the private space" denied as owner ls "/srv/a-nas/data/spaces/private/alice"
check "member is told about the viewing" grep -q admin_library_viewing <(curl -fsS -b /tmp/alice-jar "$api/api/v1/notifications")
curl -fsS -o /dev/null -X DELETE -b /tmp/jar -H "X-CSRF-Token: $csrf" "$api/api/v1/viewing/$grant_id"
check "ending the grant hides the library again" test "$(status_of -b /tmp/jar "$alice_timeline")" = 404

# Volume outages: the photo service closes its Catalog while the data volume
# has failed or is gone, writes nothing to the system disk, and reopens and
# reconciles the Catalog when the volume returns.
# wait_status WANT: waits up to 15 s for the photo API to answer WANT.
# shellcheck disable=SC2317,SC2329 # invoked through check (SC2317 in older shellcheck)
wait_status() {
  for _ in $(seq 1 75); do
    [ "$(status_of -b /tmp/jar "$api/api/v1/photos/libraries")" = "$1" ] && return 0
    sleep 0.2
  done
  return 1
}
# shellcheck disable=SC2317,SC2329 # invoked through check (SC2317 in older shellcheck)
original_intact() { curl -fsS -b /tmp/jar "$api/api/v1/photos/assets/$asset/original" | cmp -s - /tmp/e2e.png; }
# remount_volume: mounts the volume again once nothing holds the old one.
# shellcheck disable=SC2317,SC2329 # invoked through check (SC2317 in older shellcheck)
remount_volume() {
  for _ in $(seq 1 50); do
    umount /srv/a-nas/data 2>/dev/null || true
    mount "/dev/mapper/$volume" /srv/a-nas/data 2>/dev/null && return 0
    sleep 0.2
  done
  return 1
}

# The disk starts failing every request; the next write makes Btrfs abort and
# turn the volume read-only.
dmsetup suspend "$volume"
dmsetup load "$volume" --table "0 $(blockdev --getsz "$loop") error"
dmsetup resume "$volume"
{ echo probe > /srv/a-nas/data/.probe && sync --file-system /srv/a-nas/data/.probe; } 2>/dev/null || true
check "failed disk makes photos unavailable" wait_status 503
check "photo service logged the lost store" grep -q "photo store lost" /tmp/photos.log
dmsetup suspend "$volume"
dmsetup load "$volume" --table "$linear_table"
dmsetup resume "$volume"
check "volume mounts again once the disk is back" remount_volume
check "photos return after the disk failure" wait_status 200
check "photo survives the disk failure" original_intact

umount --lazy /srv/a-nas/data
check "unmounted volume makes photos unavailable" wait_status 503
check "nothing is written to the system disk" test -z "$(ls -A /srv/a-nas/data)"
# The detached volume is released once the photo service closes its Catalog.
check "volume mounts again after the unmount" remount_volume
check "photos return once the volume is mounted again" wait_status 200
check "photo survives the unmount" original_intact

# The photo service is killed in the middle of an upload.
{ printf '\211PNG\r\n\032\n'; head -c 8M /dev/urandom; } > /tmp/big.png
curl -s -o /dev/null --limit-rate 1M -b /tmp/jar -H "X-CSRF-Token: $csrf" -F file=@/tmp/big.png "$uploads" &
upload=$!
for _ in $(seq 1 50); do [ -n "$(ls -A /srv/a-nas/data/photos/staging)" ] && break; sleep 0.1; done
check "upload is in progress" test -n "$(ls -A /srv/a-nas/data/photos/staging)"
kill -KILL "$photo_service"
wait "$upload" || true
start_photo_service
check "photos return after the photo service was killed" wait_status 200
check "interrupted upload was cleaned up" test -z "$(ls -A /srv/a-nas/data/photos/staging)"
check "photo service reported the repair" grep -q "photo store reconciled" /tmp/photos.log
check "photo survives the kill" original_intact

if [ "$failures" -ne 0 ]; then
  echo "--- host agent"; tail -20 /tmp/host-agent.log
  echo "--- photo service"; tail -20 /tmp/photos.log
  echo "--- api"; tail -20 /tmp/api.log
fi
echo "failures=$failures"
exit "$failures"
