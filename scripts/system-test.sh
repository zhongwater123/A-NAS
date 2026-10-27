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
# The AI assistant's sandbox (ai/assistant-spike/system-test.sh) runs without
# Hermes; ANAS_SYSTEM_TEST_ASSISTANT_RUNTIME naming the spike's runtime.tar.gz
# adds its policy check and a real assistant against a scripted model.
#
# Local AI runs on a stand-in model, detector and runtime with the Worker's
# fake providers. With ANAS_SYSTEM_TEST_AI=real, after
# scripts/build-ai-runtime.sh and with ANAS_AI_MODEL and ANAS_AI_DETECTOR
# naming the model and detector files, ANAS_AI_FACES the directory holding
# the face models (yunet/, sface/ and auraface/, as D:\A-NAS-models\faces) and
# ANAS_AI_PETS the one holding the animal model (dinov2-small/, as
# D:\A-NAS-models\pets), it installs the real runtime and models instead.
set -euo pipefail

if [[ "${1:-}" != --inside ]]; then
  image=${1:-anas-systemd:trixie}
  repo=$(cd "$(dirname "$0")/.." && pwd)
  for binary in anas-api anas-host-agent; do
    [[ -x "$repo/build/$binary" ]] || { echo "missing build/$binary: run make build-binaries" >&2; exit 2; }
  done
  qdrant_tarball="$repo/build/qdrant/$(jq -r .sha256 "$repo/deploy/qdrant/qdrant.json")/$(jq -r .file "$repo/deploy/qdrant/qdrant.json")"
  [[ -f "$qdrant_tarball" ]] || { echo "missing $qdrant_tarball: run scripts/fetch-qdrant.sh" >&2; exit 2; }
  real_ai=()
  if [[ "${ANAS_SYSTEM_TEST_AI:-}" == real ]]; then
    [[ -f "$repo/build/ai-runtime.json" ]] || { echo "missing build/ai-runtime.json: run scripts/build-ai-runtime.sh" >&2; exit 2; }
    [[ -f "${ANAS_AI_MODEL:-}" ]] || { echo "ANAS_AI_MODEL must name the model file" >&2; exit 2; }
    [[ -f "${ANAS_AI_DETECTOR:-}" ]] || { echo "ANAS_AI_DETECTOR must name the detector file" >&2; exit 2; }
    [[ -d "${ANAS_AI_FACES:-}" ]] || { echo "ANAS_AI_FACES must name the face models' directory" >&2; exit 2; }
    [[ -d "${ANAS_AI_PETS:-}" ]] || { echo "ANAS_AI_PETS must name the animal model's directory" >&2; exit 2; }
    real_ai=(-e ANAS_SYSTEM_TEST_AI=real -v "$(dirname "$ANAS_AI_MODEL"):/ai-model:ro" -v "$(dirname "$ANAS_AI_DETECTOR"):/ai-detector:ro"
      -v "$ANAS_AI_FACES:/ai-faces:ro" -v "$ANAS_AI_PETS:/ai-pets:ro")
  fi
  assistant=()
  if [[ -n "${ANAS_SYSTEM_TEST_ASSISTANT_RUNTIME:-}" ]]; then
    [[ -f "$ANAS_SYSTEM_TEST_ASSISTANT_RUNTIME" ]] || { echo "ANAS_SYSTEM_TEST_ASSISTANT_RUNTIME must name runtime.tar.gz" >&2; exit 2; }
    assistant=(-v "$ANAS_SYSTEM_TEST_ASSISTANT_RUNTIME:/assistant-runtime.tar.gz:ro")
  fi
  name="anas-system-test-$$"
  docker run -d --name "$name" --privileged --cgroupns=host -v /sys/fs/cgroup:/sys/fs/cgroup:rw \
    --tmpfs /run --tmpfs /run/lock -v "$repo:/src:ro" "${real_ai[@]}" "${assistant[@]}" "$image" >/dev/null
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
  cp /src/deploy/models/yunet-2026may.json "$ai/face-detector.json"
  ln -s "/ai-faces/yunet/$(jq -r .file "$ai/face-detector.json")" "$ai/face-detector.onnx"
  cp "/src/deploy/models/${ANAS_SYSTEM_TEST_FACE_EMBEDDER:-auraface-v1}.json" "$ai/face-embedder.json"
  ln -s "/ai-faces/$(jq -r .name "$ai/face-embedder.json" | cut -d - -f 1)/$(jq -r .file "$ai/face-embedder.json")" "$ai/face-embedder.onnx"
  cp /src/deploy/models/dinov2-small.json "$ai/animal-embedder.json"
  ln -s "/ai-pets/dinov2-small/$(jq -r .file "$ai/animal-embedder.json")" "$ai/animal-embedder.onnx"
  cp /src/build/ai-runtime.json "$ai/runtime.json"
  ln -s "/src/build/ai-runtime/$(jq -r .file "$ai/runtime.json")" "$ai/runtime.tar.gz"
  detector_model="$(jq -r .name "$ai/detector.json")@$(jq -r .sha256 "$ai/detector.json" | cut -c1-12)+"
  face_model="$(jq -r .name "$ai/face-detector.json")@$(jq -r .sha256 "$ai/face-detector.json" | cut -c1-12)+"
  animal_model="$(jq -r .name "$ai/animal-embedder.json")@$(jq -r .sha256 "$ai/animal-embedder.json" | cut -c1-12)+"
else
  head -c 65536 /dev/urandom > "$ai/model.litertlm"
  printf '{"name":"system-test","file":"system-test.litertlm","sizeBytes":%s,"sha256":"%s"}\n' \
    "$(stat -c %s "$ai/model.litertlm")" "$(sha256sum "$ai/model.litertlm" | cut -d ' ' -f 1)" > "$ai/model.json"
  head -c 4096 /dev/urandom > "$ai/detector.onnx"
  printf '{"name":"system-test-detector","file":"system-test.onnx","sizeBytes":%s,"sha256":"%s"}\n' \
    "$(stat -c %s "$ai/detector.onnx")" "$(sha256sum "$ai/detector.onnx" | cut -d ' ' -f 1)" > "$ai/detector.json"
  detector_model=fake-detector
  for role in face-detector face-embedder animal-embedder; do
    head -c 2048 /dev/urandom > "$ai/$role.onnx"
    printf '{"name":"system-test-%s","kind":"sface","file":"system-test.onnx","sizeBytes":%s,"sha256":"%s"}\n' "$role" \
      "$(stat -c %s "$ai/$role.onnx")" "$(sha256sum "$ai/$role.onnx" | cut -d ' ' -f 1)" > "$ai/$role.json"
  done
  face_model=fake-faces
  animal_model=fake-animals
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
  printf '[Service]\nExecStart=\nExecStart=/opt/a-nas/current/ai/runtime/bin/python -m anas_ai.worker --fake --fake-detector --fake-faces --fake-animals\n' \
    > /etc/systemd/system/anas-ai.service.d/system-test.conf
fi
# Each model role's file is kept once by content: as many as distinct pins.
models_kept=$(for role in model detector face-detector face-embedder animal-embedder; do jq -r .sha256 "$ai/$role.json"; done | sort -u | wc -l)
# AI starts once the photo library has gone unused for this long, so that the
# test sees a photo organised in the Worker's sandbox.
install -d /etc/systemd/system/anas-photos.service.d
printf '[Service]\nEnvironment=ANAS_PHOTOS_AI_QUIET_PERIOD=2s\n' > /etc/systemd/system/anas-photos.service.d/system-test.conf

# stage DIR: a release as scripts/deploy-dev.ps1 stages it.
stage() {
  install -d "$1"
  install -m 0755 /src/build/anas-api /src/build/anas-host-agent /src/scripts/install-screensavers.sh "$1/"
  for unit in anas-api anas-host-agent anas-photos anas-shares; do
    install -m 0644 "/src/deploy/systemd/system/$unit.service" "$1/$unit-system.service"
  done
  install -m 0644 /src/deploy/systemd/system/anas.slice "$1/anas-system.slice"
  install -m 0644 /src/deploy/systemd/system/anas-ai.socket "$1/anas-ai-system.socket"
  install -m 0644 /src/deploy/systemd/system/anas-ai.service "$1/anas-ai-system.service"
  install -m 0644 /src/deploy/systemd/system/anas-media.socket "$1/anas-media-system.socket"
  install -m 0644 "/src/deploy/systemd/system/anas-media@.service" "$1/anas-media-system@.service"
  install -m 0644 /src/deploy/systemd/system/system-anasmedia.slice "$1/anas-media-system.slice"
  install -m 0644 /src/deploy/systemd/system/anas-vectors.slice "$1/anas-vectors-system.slice"
  install -m 0644 "/src/deploy/systemd/system/anas-vectors@.service" "$1/anas-vectors-system@.service"
  install -m 0644 "/src/deploy/systemd/system/anas-vectors-proxy@.socket" "$1/anas-vectors-proxy-system@.socket"
  install -m 0644 "/src/deploy/systemd/system/anas-vectors-proxy@.service" "$1/anas-vectors-proxy-system@.service"
  install -d "$1/vectors"
  install -m 0644 /src/deploy/qdrant/qdrant.json /src/deploy/qdrant/config.yaml "$1/vectors/"
  install -m 0755 /src/deploy/qdrant/start.sh /src/deploy/qdrant/wait-ready.sh "$1/vectors/"
  ln -s "/src/build/qdrant/$(jq -r .sha256 /src/deploy/qdrant/qdrant.json)/$(jq -r .file /src/deploy/qdrant/qdrant.json)" \
    "$1/vectors/qdrant.tar.gz"
  install -d "$1/ai/anas_ai"
  install -m 0644 /src/ai/anas_ai/*.py "$1/ai/anas_ai/"
  install -m 0644 "$ai/model.json" "$ai/detector.json" "$ai/face-detector.json" "$ai/face-embedder.json" \
    "$ai/animal-embedder.json" "$ai/runtime.json" "$1/ai/"
  ln -s "$ai/model.litertlm" "$1/ai/model.litertlm"
  ln -s "$ai/detector.onnx" "$1/ai/detector.onnx"
  ln -s "$ai/face-detector.onnx" "$1/ai/face-detector.onnx"
  ln -s "$ai/face-embedder.onnx" "$1/ai/face-embedder.onnx"
  ln -s "$ai/animal-embedder.onnx" "$1/ai/animal-embedder.onnx"
  ln -s "$ai/runtime.tar.gz" "$1/ai/runtime.tar.gz"
  install -m 0644 /src/deploy/chromium/policies/managed/a-nas.json "$1/a-nas-chromium-policy.json"
  install -m 0644 /src/deploy/caddy/Caddyfile "$1/Caddyfile"
  install -m 0750 /src/scripts/run-kiosk.sh "$1/kiosk-launcher"
  install -m 0644 "/src/deploy/systemd/system/anas-kiosk@.service" "$1/anas-kiosk@.service"
  install -m 0644 /src/deploy/pam/a-nas-kiosk "$1/a-nas-kiosk.pam"
  install -m 0644 /src/deploy/sysusers.d/a-nas-kiosk.conf "$1/a-nas-kiosk.sysusers"
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

# A record of the Host Agent's as a release before issue #176 left it, in the
# Product Service's StateDirectory; the Host Agent moves it to its own.
install -d -m 0700 /var/lib/a-nas
printf '{\n  "legacy-app": 30000\n}\n' > /var/lib/a-nas/app-identities.json
chmod 0600 /var/lib/a-nas/app-identities.json
cp -p /var/lib/a-nas/app-identities.json /tmp/legacy-app-identities.json

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
# shellcheck disable=SC2317,SC2329 # invoked through check (SC2317 in older shellcheck)
verified() { # UNIT_FILE...: systemd-analyze verify reports nothing, not even a warning
  local report
  report=$(systemd-analyze verify --man=no "$@" 2>&1) && [[ -z "$report" ]] && return
  printf '%s\n' "$report" >&2
  return 1
}
api=http://127.0.0.1:8080
data=/srv/a-nas/data

# --- The units as installed, with every drop-in, read by Debian 13's systemd
# as on the NAS (#278): each directive and value, and that each command exists.
# The image has no Wayland compositor: Cage, which the local console's unit
# starts, is a stand-in that only has to exist.
[[ -x /usr/bin/cage ]] || install -m 0755 /dev/null /usr/bin/cage
check "systemd accepts every installed A-NAS unit" \
  verified /etc/systemd/system/anas*.service /etc/systemd/system/anas*.socket /etc/systemd/system/*anas*.slice

# --- Every service runs under its production unit (issue #38).
check "services are active" systemctl is-active --quiet anas-host-agent anas-photos anas-api anas-shares smbd
check "Host Agent keeps CAP_SETUID and CAP_SETGID" has_capabilities anas-host-agent
check "Host Agent proved it can start workers as users" \
  journal_has anas-host-agent "$installed_at" 'file broker identity switch verified'
# Hardening that everything below runs under (issue #166).
check "Host Agent holds only the capabilities its unit names" \
  test "$(awk '/^CapBnd:/ {print $2}' "/proc/$(systemctl show -p MainPID --value anas-host-agent)/status")" = 00000000012a00ff
check "Host Agent's memory and tasks are limited" \
  test "$(systemctl show -p MemoryMax --value anas-host-agent):$(systemctl show -p TasksMax --value anas-host-agent)" = 1073741824:512
# Its records live where only root can open them (issue #176).
check "Host Agent's state directory is root's alone" test "$(stat -c '%U:%G %a' /var/lib/a-nas-host)" = 'root:root 700'
check "Host Agent moved an older release's record into it" \
  cmp -s /tmp/legacy-app-identities.json /var/lib/a-nas-host/app-identities.json
check "the moved record is root's alone" \
  test "$(stat -c '%U:%G %a' /var/lib/a-nas-host/app-identities.json)" = 'root:root 600'
check "the record left the Product Service's directory" test ! -e /var/lib/a-nas/app-identities.json
check "Product Service cannot list the Host Agent's records" denied as a-nas ls /var/lib/a-nas-host

# --- The local console (issue #174): an account of its own that cannot log
# in, starting the launcher of the installed release. The container has no
# display, so the unit is installed but neither enabled nor started.
check "the local console runs as its own account" \
  test "$(systemctl show -p User --value anas-kiosk@tty1.service)" = a-nas-kiosk
check "the local console's account has no home and no login shell" \
  test "$(getent passwd a-nas-kiosk | cut -d: -f6-7)" = /nonexistent:/usr/sbin/nologin
check "the local console's account has no password" test "$(passwd -S a-nas-kiosk | cut -d ' ' -f 2)" = L
check "the local console's launcher is the installed release's" \
  cmp -s /opt/a-nas/current/kiosk-launcher /src/scripts/run-kiosk.sh
check "the local console's launcher is root's" test "$(stat -c '%U %a' /opt/a-nas/current/kiosk-launcher)" = 'root 755'
check "the local console cannot write the staged release" denied as a-nas-kiosk touch "$release/kiosk-launcher"
check "the installer leaves the local console disabled" \
  test "$(systemctl is-enabled anas-kiosk@tty1.service 2>/dev/null)" = disabled
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
# The unit a process runs in: its cgroup's last part. The container's systemd
# may sit below the host's cgroup root, so only the end is fixed.
scope_of() { sed -n 's|^0::.*/||p' "/proc/$1/cgroup"; }
alice_worker=$(pgrep -u "$alice_id" -f 'anas-host-agent file-worker' | head -n 1 || true)
check "the member's file worker runs in a scope of its own, not the Host Agent's" \
  grep -Eqx 'anas-files-alice-[0-9]+\.scope' <<<"$(scope_of "$alice_worker" 2>/dev/null)"
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

# --- Member sharing crosses the product bridge, independent service and broker.
shares_ready() {
  for _ in $(seq 1 100); do
    curl -fsS -b /tmp/owner.jar "$api/api/v1/shares" >/dev/null 2>&1 && return 0
    sleep 0.2
  done
  return 1
}
check "member sharing works through production service sandboxes" \
  python3 /src/scripts/smoke-member-sharing.py "$api" /tmp/owner.jar /tmp/alice.jar \
    "$owner_csrf" "$alice_csrf" "$owner_file" "$alice_private"
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
# Installed apps get the session cookie too (issue #161): it works only from
# the client that signed in, and only the desktop may frame its pages.
check "a LAN session is refused from another client" test "$(status_of -b /tmp/lan.jar "$api/api/v1/session")" = 401
check "the local console session is refused through the LAN entry" test "$(status_of -b /tmp/console.jar "$lan/api/v1/session")" = 401
check "the desktop cannot be framed through the LAN entry" \
  bash -c "curl -fsS -D - -o /dev/null '$lan/' | tr -d '\r' | grep -qix 'x-frame-options: deny'"

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
check "sharing recovers after the Product Service restarts" shares_ready
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
# The shell runs in a scope of its own, so what it logs is not the Host
# Agent's, even under its name (issue #166).
forged_at=$(date '+%Y-%m-%d %H:%M:%S')
scope_output=$(terminal "$lan" /tmp/owner-lan.jar $'echo "cgroup:$(cat /proc/self/cgroup)"\nlogger -t anas-host-agent "forged by a terminal"\nunshare --user true 2>/dev/null; echo "unshare:$?"\nexit 0\n' || true)
check "the shell runs in a scope of its own" grep -Eqx 'cgroup:0::/(.*/)?anas-terminal-owner-[0-9]+\.scope' <<<"$scope_output"
check "the shell cannot create namespaces" grep -qx 'unshare:[1-9][0-9]*' <<<"$scope_output"
check "what a shell logs reaches the journal" \
  bash -c "for _ in \$(seq 1 20); do journalctl -t anas-host-agent --since '$forged_at' -o cat --no-pager | grep -Fq 'forged by a terminal' && exit 0; sleep 0.2; done; exit 1"
check "but not as the Host Agent's" denied journal_has anas-host-agent "$forged_at" 'forged by a terminal'
check "nor in the Host Agent's log in Settings" \
  test "$(curl -fsS -b /tmp/owner.jar "$api/api/v1/system-logs?service=host-agent&q=forged" | jq '.items | length')" = 0

# --- A data volume the Host Agent cannot reconcile leaves it serving (issue
# #166). Btrfs turns a volume read-only when it finds errors; a remount stands
# in. The photo and share services hold databases open for writing on it,
# which a read-only remount refuses.
entries /tmp/alice.jar "$alice_private" >/dev/null || true
scopes_before=$(systemctl list-units --plain --no-legend 'anas-files-*' 'anas-terminal-*' | awk '{print $1}')
systemctl stop anas-photos anas-shares
check "the data volume remounts read-only" mount -o remount,ro "$data"
readonly_at=$(date '+%Y-%m-%d %H:%M:%S')
systemctl restart anas-host-agent
for _ in $(seq 1 50); do journal_has anas-host-agent "$readonly_at" 'data-volume reconciliation failed' && break; sleep 0.2; done
check "restarting the Host Agent ends the scopes it started" \
  test -n "$scopes_before" -a -z "$(for scope in $scopes_before; do systemctl is-active --quiet "$scope" && echo "$scope"; done)"
check "the Host Agent serves on a read-only data volume" systemctl is-active --quiet anas-host-agent
check "it logs that it cannot reconcile the volume" journal_has anas-host-agent "$readonly_at" 'data-volume reconciliation failed'
check "and reports that in the host state" \
  bash -c "curl -fsS --unix-socket /run/a-nas/host-agent.sock http://host-agent/v1/state | jq -e '.reconcileFailed == true' >/dev/null"
check "Web files still list" grep -qx alice.txt <(entries /tmp/alice.jar "$alice_private")
check "the terminal still opens" grep -qx 'close 1000 exit 0' <<<"$(terminal "$api" /tmp/owner.jar $'exit 0\n' || true)"
check "the Host Agent's log still reads in Settings" \
  bash -c "curl -fsS -b /tmp/owner.jar '$api/api/v1/system-logs?service=host-agent&q=reconciliation' | jq -e '.items | length > 0' >/dev/null"
check "the health check names the read-only data volume" \
  bash -c "curl -fsS -b /tmp/owner.jar '$api/api/v1/system' | jq -e '[.healthFindings[].code] | index(\"volume_read_only\")' >/dev/null"
mount -o remount,rw "$data"
writable_at=$(date '+%Y-%m-%d %H:%M:%S')
systemctl restart anas-host-agent
systemctl start anas-photos anas-shares
check "once the volume is writable, the Host Agent reconciles it" \
  bash -c "for _ in \$(seq 1 50); do journalctl -u anas-host-agent --since '$writable_at' -o cat --no-pager | grep -Fq 'data volume reconciled' && exit 0; sleep 0.2; done; exit 1"
check "and reports nothing wrong" \
  bash -c "curl -fsS --unix-socket /run/a-nas/host-agent.sock http://host-agent/v1/state | jq -e '.reconcileFailed != true' >/dev/null"
check "sharing recovers on the writable volume" shares_ready

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

# --- Snapshots (#162): a space of 100,000 files, far beyond the 1 MiB the
# Host Agent's reply used to be cut at, and the sweep that removes what a
# failed or interrupted snapshot leaves on the volume or in the catalog.
alice_root="$data/spaces/private/alice"
subvolume_of() { # SNAPSHOT_ID -> the Host Agent's subvolume path for it
  printf '%s/.a-nas-snapshots/%s' "$data" "$(printf 'a-nas:snapshot:v1\0%s' "$1" | sha256sum | cut -c1-32)"
}
snapshot_ids() { curl -fsS -b /tmp/alice.jar "$api/api/v1/spaces/$alice_private/snapshots" | jq -r '.items[].id'; }
take_snapshot() { # NAME -> the snapshot JSON
  curl -fsS -b /tmp/alice.jar -H "X-CSRF-Token: $alice_csrf" -H 'Content-Type: application/json' \
    -d "{\"name\":\"$1\"}" "$api/api/v1/spaces/$alice_private/snapshots"
}
as alice python3 - "$alice_root/快照压力" <<'PY'
import os, sys
for folder in range(100):
    path = os.path.join(sys.argv[1], f"文件夹 {folder:03d}")
    os.makedirs(path)
    for index in range(1000):
        open(os.path.join(path, f"照片_{index:04d}.jpg"), "wb").close()
PY
large_snapshot=$(take_snapshot 十万个文件 || true)
large_snapshot_id=$(jq -r '.id // empty' <<<"$large_snapshot" 2>/dev/null || true)
check "a snapshot of a space with 100,000 files succeeds" \
  test "$(jq -r '.fileCount // 0' <<<"$large_snapshot" 2>/dev/null || echo 0)" -ge 100000
check "the backup center lists it" grep -qx "$large_snapshot_id" <(snapshot_ids)
check "its folders can be browsed" grep -qx 快照压力 \
  <(curl -fsS -b /tmp/alice.jar "$api/api/v1/snapshots/$large_snapshot_id/entries" | jq -r '.items[].name')
# Later sections list this space; keep them quick.
rm -rf "$alice_root/快照压力"
gone_snapshot=$(take_snapshot 卷上已删除 | jq -r '.id // empty' || true)
btrfs subvolume delete "$(subvolume_of "$gone_snapshot")" >/dev/null 2>&1 || true
check "a snapshot already gone from the volume can still be deleted" \
  test "$(status_of -X DELETE -b /tmp/alice.jar -H "X-CSRF-Token: $alice_csrf" "$api/api/v1/snapshots/$gone_snapshot")" = 204
check "its record is gone" denied grep -qx "$gone_snapshot" <(snapshot_ids)
# What a crash leaves: a subvolume noted as pending and a record without a
# subvolume. The Product Service sweeps both when it starts, and leaves a
# subvolume the catalog never noted, as one of a lost catalog (#269).
systemctl stop anas-api
orphan=$(subvolume_of snapshot:crashed)
foreign="$data/.a-nas-snapshots/ffffffffffffffffffffffffffffffff"
btrfs subvolume snapshot -r "$alice_root" "$orphan" >/dev/null
btrfs subvolume snapshot -r "$alice_root" "$foreign" >/dev/null
setpriv --reuid=a-nas --regid=a-nas --init-groups python3 - "$alice_private" <<'PY'
import sqlite3, sys
db = sqlite3.connect("/var/lib/a-nas/files.db")
db.execute("INSERT INTO snapshots(id, space_id, name, origin, created_by, created_at, file_count, size_bytes)"
           " VALUES(?,?,?,?,?,?,?,?)", ("snapshot:lost", sys.argv[1], "卷上已没有", "manual", "system", "2026-10-20T00:00:00Z", 0, 0))
db.execute("INSERT INTO pending_snapshots(id, space_id, created_at) VALUES(?,?,?)",
           ("snapshot:crashed", sys.argv[1], "2026-10-20T00:00:00Z"))
db.commit()
PY
systemctl start anas-api
for _ in $(seq 1 100); do curl -fsS "$api/healthz" >/dev/null 2>&1 && break; sleep 0.2; done
# shellcheck disable=SC2317,SC2329 # invoked through check (SC2317 in older shellcheck)
lost_record_gone() { snapshot_ids >/tmp/snapshot-ids.txt && ! grep -qx snapshot:lost /tmp/snapshot-ids.txt; }
for _ in $(seq 1 100); do test ! -e "$orphan" && lost_record_gone && break; sleep 0.2; done
check "on start the Product Service deletes a snapshot whose taking never finished" test ! -e "$orphan"
check "and the record of a snapshot gone from the volume" lost_record_gone
check "recorded snapshots survive the sweep" test -d "$(subvolume_of "$large_snapshot_id")"
check "a snapshot the catalog never noted survives too" test -d "$foreign"
curl -fsS -b /tmp/owner.jar "$api/api/v1/activity?category=file" | jq -r '.items[].action' >/tmp/sweep-actions.txt || true
check "the sweep is in the activity log" grep -qx snapshot.missing /tmp/sweep-actions.txt
check "so is the snapshot it left alone" grep -qx snapshot.unrecorded /tmp/sweep-actions.txt
btrfs subvolume delete "$foreign" >/dev/null 2>&1 || true
# A copy that a restart cut short leaves a hidden duplicate; listing the
# space removes it.
as alice mkdir -p "$alice_root/.a-nas-copy-0123456789abcdef/副本"
as alice touch "$alice_root/.a-nas-copy-0123456789abcdef/副本/a.jpg"
curl -fsS -o /dev/null -b /tmp/alice.jar "$api/api/v1/spaces/$alice_private/entries" || true
check "listing a space removes a copy that a restart cut short" test ! -e "$alice_root/.a-nas-copy-0123456789abcdef"

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
  bash -c "for _ in \$(seq 1 30); do journalctl -u anas-photos --since '$writable_at' -o cat --no-pager | grep -Fq 'photo service ready' && exit 0; sleep 1; done; exit 1"
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
  -a "$(readlink /opt/a-nas/current/ai/face-detector.onnx)" = "/opt/a-nas/models/$(jq -r .sha256 "$ai/face-detector.json")/$(jq -r .file "$ai/face-detector.json")" \
  -a "$(readlink /opt/a-nas/current/ai/face-embedder.onnx)" = "/opt/a-nas/models/$(jq -r .sha256 "$ai/face-embedder.json")/$(jq -r .file "$ai/face-embedder.json")" \
  -a "$(readlink /opt/a-nas/current/ai/animal-embedder.onnx)" = "/opt/a-nas/models/$(jq -r .sha256 "$ai/animal-embedder.json")/$(jq -r .file "$ai/animal-embedder.json")" \
  -a "$(readlink /opt/a-nas/current/ai/runtime)" = "/opt/a-nas/ai-runtimes/$(jq -r .id "$ai/runtime.json")"
ai_cores=$(($(nproc) > 2 ? $(nproc) - 2 : 1))
check "the Worker may use every core but two" \
  test "$(systemctl show -p CPUQuotaPerSecUSec --value anas-ai.service)" = "${ai_cores}s"
check "the kernel reclaims the Worker's memory before its hard limit" \
  test "$(systemctl show -p MemoryHigh --value anas-ai.service):$(systemctl show -p MemoryMax --value anas-ai.service)" = 2684354560:3221225472
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
organised() { # the member's photo has its vector, its detection and its face scan
  local status
  for _ in $(seq 1 180); do
    status=$(curl -fsS -b /tmp/alice.jar "$api/api/v1/photos/ai" || true)
    jq -e --arg model "$detector_model" --arg faces "$face_model" --arg animals "$animal_model" \
      '.pending == 0 and .ready >= 1 and (.objectModel // "" | startswith($model)) and (.faceModel // "" | startswith($faces))
        and (.animalModel // "" | startswith($animals))' <<<"$status" >/dev/null && return
    sleep 1
  done
  echo "$status"
  return 1
}
check "the Worker organises the photo with its vector, detector, face and animal models in its sandbox" organised
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
  -a "$(find /opt/a-nas/models -mindepth 1 -maxdepth 1 | wc -l)" = "$models_kept"
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
  -a "$(find /opt/a-nas/models -mindepth 1 -maxdepth 1 | wc -l)" = "$models_kept"

# --- The vector index (ADR 0028): one Qdrant instance per owning service,
# reached only through its owner's socket, started on demand in a network
# namespace of its own and stopped when idle.
vectors_state=/var/lib/private/a-nas-vectors/a-nas-photos
qdrant_sha=$(jq -r .sha256 /src/deploy/qdrant/qdrant.json)
# grpc-answers.py SOCKET: an HTTP/2 server answers the client preface through it.
cat > /usr/local/lib/grpc-answers.py <<'PY'
import socket, sys
s = socket.socket(socket.AF_UNIX)
s.settimeout(60)
s.connect(sys.argv[1])
s.sendall(b"PRI * HTTP/2.0\r\n\r\nSM\r\n\r\n" + bytes([0, 0, 0, 4, 0, 0, 0, 0, 0]))
frame = b""
while len(frame) < 9:
    chunk = s.recv(9 - len(frame))
    if not chunk:
        sys.exit(1)
    frame += chunk
sys.exit(0 if frame[3] == 4 else 1)
PY
# shellcheck disable=SC2317,SC2329 # invoked through check (SC2317 in older shellcheck)
unit_stops() { # UNIT: becomes inactive within 90 seconds, the owner's idle minute included
  for _ in $(seq 1 180); do
    systemctl is-active --quiet "$1" || return 0
    sleep 0.5
  done
  return 1
}
# shellcheck disable=SC2317,SC2329 # invoked through check (SC2317 in older shellcheck)
same_network_namespace() {
  local index proxy
  index=$(systemctl show -p MainPID --value anas-vectors@a-nas-photos.service)
  proxy=$(systemctl show -p MainPID --value anas-vectors-proxy@a-nas-photos.service)
  [[ "$index" != 0 && "$proxy" != 0 && "$(readlink "/proc/$index/ns/net")" == "$(readlink "/proc/$proxy/ns/net")" &&
    "$(readlink "/proc/$index/ns/net")" != "$(readlink /proc/1/ns/net)" ]]
}
# shellcheck disable=SC2317,SC2329 # invoked through check (SC2317 in older shellcheck)
journal_shows() { # UNIT SINCE TEXT: the unit logs TEXT within three minutes; the vector sync waits,
  # as AI work does, while the device's other work is under pressure
  for _ in $(seq 1 360); do
    journal_has "$@" && return 0
    sleep 0.5
  done
  return 1
}
check "the installer enables the photo service's vector index socket" \
  systemctl is-enabled --quiet anas-vectors-proxy@a-nas-photos.socket
check "the photo service builds its vector index from the Catalog" \
  journal_shows anas-photos.service "$installed_at" "photo vector index created"
check "and brings the organised photo into it" \
  journal_shows anas-photos.service "$installed_at" "photo vector index synced"
journalctl -u anas-photos.service --since "$installed_at" -o cat --no-pager | grep -F 'vector index' | tail -n 3 || true
check "a search is compared with the vector index in the background" semantic_search 猫
check "and the comparison is logged" \
  journal_shows anas-photos.service "$installed_at" "photo vector index shadow search"
check "only the photo service reaches its vector index" \
  test "$(stat -c '%a %U %G' /run/a-nas-vectors/a-nas-photos.sock)" = "600 a-nas-photos a-nas-photos"
check "the Product Service account cannot connect to the photo vector index" \
  denied as a-nas python3 -c 'import socket; socket.socket(socket.AF_UNIX).connect("/run/a-nas-vectors/a-nas-photos.sock")'
check "the release links Qdrant kept once by content" \
  test "$(readlink /opt/a-nas/current/vectors/qdrant)" = "/opt/a-nas/qdrant/$qdrant_sha/qdrant"
check "all vector index instances share one memory limit" \
  test "$(systemctl show -p MemoryMax --value anas-vectors.slice)" = 1610612736
check "connecting starts the vector index and reaches its gRPC server" \
  as a-nas-photos python3 /usr/local/lib/grpc-answers.py /run/a-nas-vectors/a-nas-photos.sock
# hold SOCKET SECONDS: keeps a connection open while the test looks at the
# running instance.
hold() { python3 -c 'import socket, sys, time; s = socket.socket(socket.AF_UNIX); s.connect(sys.argv[1]); time.sleep(float(sys.argv[2]))' "$1" "$2"; }
# index_rest PATH [CURL OPTION...]: a REST answer of the photo vector index,
# from inside its network namespace.
index_rest() {
  nsenter -t "$(systemctl show -p MainPID --value anas-vectors@a-nas-photos.service)" -n curl -fsS "http://127.0.0.1:6333$1" "${@:2}"
}
# shellcheck disable=SC2317,SC2329 # invoked through check (SC2317 in older shellcheck)
index_holds_photos() { # the photo service's collection holds at least one point
  local name
  name=$(index_rest /collections | jq -r '.result.collections[].name | select(startswith("images_v1_"))')
  [[ -n "$name" ]] && (($(index_rest "/collections/$name" | jq -r '.result.points_count') >= 1))
}
systemctl start anas-vectors-proxy@a-nas-shares.socket
hold /run/a-nas-vectors/a-nas-photos.sock 8 &
holder=$!
hold /run/a-nas-vectors/a-nas-shares.sock 8 &
other_holder=$!
sleep 3
check "the vector index runs under a dynamic identity of its own" \
  test "$(systemctl show -p User --value anas-vectors@a-nas-photos.service)" = a-nas-vec-a-nas-photos
check "the vector index's dynamic identity is not root" dynamic_identity anas-vectors@a-nas-photos.service
check "another owner's instance runs under another identity" \
  test "$(awk '/^Uid:/ {print $2}' "/proc/$(systemctl show -p MainPID --value anas-vectors@a-nas-photos.service)/status")" != \
  "$(awk '/^Uid:/ {print $2}' "/proc/$(systemctl show -p MainPID --value anas-vectors@a-nas-shares.service)/status")"
check "another owner's socket is its own" test "$(stat -c '%a %U' /run/a-nas-vectors/a-nas-shares.sock)" = "600 a-nas-shares"
check "the photo service cannot connect to another owner's vector index" \
  denied as a-nas-photos python3 -c 'import socket; socket.socket(socket.AF_UNIX).connect("/run/a-nas-vectors/a-nas-shares.sock")'
check "the proxy shares the vector index's own network namespace" same_network_namespace
check "the vector index's namespace has only its loopback" \
  test "$(nsenter -t "$(systemctl show -p MainPID --value anas-vectors@a-nas-photos.service)" -n ip -o link | awk -F': ' '{print $2}')" = lo
check "the photo service's collection holds the organised photo" index_holds_photos
check "the vector index keeps its storage in its own state directory" test -d "$vectors_state/storage"
check "the vector index runs in its slice under background work's" \
  test "$(systemctl show -p Slice --value anas-vectors@a-nas-photos.service):$(systemctl show -p Slice --value anas-vectors.slice)" = anas-vectors.slice:anas.slice
check "the vector index may keep many files open" \
  test "$(systemctl show -p LimitNOFILE --value anas-vectors@a-nas-photos.service)" = 65536
echo "INFO the vector index's memory: $(systemctl show -p MemoryCurrent --value anas-vectors@a-nas-photos.service) bytes"
wait "$holder" "$other_holder"
systemctl stop anas-vectors-proxy@a-nas-shares.socket anas-vectors-proxy@a-nas-shares.service anas-vectors@a-nas-shares.service
check "the vector index stops once its proxy has been idle" unit_stops anas-vectors@a-nas-photos.service
echo "INFO the vector index's memory peak: $(systemctl show -p MemoryPeak --value anas-vectors@a-nas-photos.service) bytes"
# mark NAME: a file in the index's storage, as its dynamic user would leave it.
mark() {
  touch "$vectors_state/storage/$1"
  chown --reference="$vectors_state" "$vectors_state/storage/$1"
}
mark kept-by-the-same-release
check "the same Qdrant release starts" \
  as a-nas-photos python3 /usr/local/lib/grpc-answers.py /run/a-nas-vectors/a-nas-photos.sock
check "and keeps its index" test -e "$vectors_state/storage/kept-by-the-same-release"
check "the vector index stops again once idle" unit_stops anas-vectors@a-nas-photos.service
printf '1.19.9\n' > "$vectors_state/qdrant-version"
chown --reference="$vectors_state" "$vectors_state/qdrant-version"
mark written-by-another-minor-release
check "a vector index another minor release wrote starts" \
  as a-nas-photos python3 /usr/local/lib/grpc-answers.py /run/a-nas-vectors/a-nas-photos.sock
check "and starts empty" test ! -e "$vectors_state/storage/written-by-another-minor-release"
# A collection whose stored configuration is damaged makes Qdrant panic while
# loading: the instance cannot start until its storage is emptied.
hold /run/a-nas-vectors/a-nas-photos.sock 4 &
holder=$!
sleep 2
index_rest /collections/damaged -X PUT -H 'Content-Type: application/json' -d '{"vectors":{"size":4,"distance":"Dot"}}' >/dev/null
wait "$holder"
check "the vector index stops before the damage" unit_stops anas-vectors@a-nas-photos.service
printf 'damaged\n' > "$vectors_state/storage/collections/damaged/config.json"
failed_starts=0
until as a-nas-photos python3 /usr/local/lib/grpc-answers.py /run/a-nas-vectors/a-nas-photos.sock; do
  failed_starts=$((failed_starts + 1))
  ((failed_starts < 5)) || break
  sleep 1
done
echo "INFO starts that failed on the damaged index: $failed_starts"
check "a damaged vector index fails to start at first" test "$failed_starts" -ge 1
check "and starts empty after two failures in a row" \
  test "$failed_starts" -le 3 -a ! -e "$vectors_state/storage/collections/damaged"
check "failed starts leave the vector index socket listening" systemctl is-active --quiet anas-vectors-proxy@a-nas-photos.socket
# A probe in the vector index's own unit, in place of Qdrant.
cat > /usr/local/lib/anas-vectors-probe.py <<'PY'
import os, socket, sys
leaks = []
with open("/proc/net/dev") as devices:
    interfaces = {line.split(":")[0].strip() for line in devices.readlines()[2:]}
if interfaces != {"lo"}:
    leaks.append(f"sees interfaces {sorted(interfaces)}")
try:
    socket.create_connection(("1.1.1.1", 53), timeout=3).close()
    leaks.append("reached the network")
except OSError:
    pass
try:
    socket.socket(socket.AF_NETLINK, socket.SOCK_DGRAM).close()
    leaks.append("opened a netlink socket")
except OSError:
    pass
for path in ("/srv/a-nas/data", "/var/lib/a-nas"):
    try:
        os.listdir(path)
        leaks.append(f"listed {path}")
    except OSError:
        pass
print("; ".join(leaks) or "contained")
sys.exit(1 if leaks else 0)
PY
sed -e 's|^ExecStart=.*|ExecStart=/usr/bin/python3 /usr/local/lib/anas-vectors-probe.py|' \
  -e 's|^Type=.*|Type=oneshot|' -e '/^ExecStartPre=/d' -e '/^ExecStartPost=/d' -e '/^StopWhenUnneeded=/d' \
  "/etc/systemd/system/anas-vectors@.service" > "/etc/systemd/system/anas-vectors-probe@.service"
systemctl daemon-reload
check "the vector index probe finds the network and data volume outside the sandbox" \
  denied python3 /usr/local/lib/anas-vectors-probe.py
check "the vector index's sandbox has only loopback and cannot read the data volume or product state" \
  systemctl start anas-vectors-probe@a-nas-photos.service
journalctl -u anas-vectors-probe@a-nas-photos.service -o cat --no-pager | grep -E 'contained|sees|reached|opened|listed' | tail -n 1 || true
qdrant_kept=$(find /opt/a-nas/qdrant -mindepth 1 -maxdepth 1 | wc -l)
staged_tampered=/tmp/tampered-qdrant
stage "$staged_tampered"
rm "$staged_tampered/vectors/qdrant.tar.gz"
head -c 1024 /dev/urandom > "$staged_tampered/vectors/qdrant.tar.gz"
jq --arg sha "$(head -c 1024 /dev/urandom | sha256sum | cut -d ' ' -f 1)" '.sha256 = $sha | .sizeBytes = 1024' \
  /src/deploy/qdrant/qdrant.json > "$staged_tampered/vectors/qdrant.json"
check "a staged Qdrant that does not match its manifest is refused" \
  denied bash /src/scripts/install-v1.0.1-system-services.sh "$staged_tampered" tampered "$iface"
check "the refused Qdrant changes nothing" \
  test "$(readlink /opt/a-nas/current)" = /opt/a-nas/releases/system-test -a ! -e /opt/a-nas/releases/tampered \
  -a "$(find /opt/a-nas/qdrant -mindepth 1 -maxdepth 1 | wc -l)" = "$qdrant_kept"

# --- Media center (ADR 0017): files are read as the user through the File
# Broker; FFmpeg runs in a per-connection worker without network, data volume
# or Product Service state.
check "the installer enables the media worker socket" systemctl is-enabled --quiet anas-media.socket
check "the Product and Photo Services reach the media worker socket" \
  test "$(stat -c '%a %U %G' /run/a-nas-media/media.sock)" = "660 root a-nas-photos"
# Photo Service also uses the same descriptor-only worker socket.
ffmpeg -hide_banner -loglevel error -f lavfi -i testsrc2=size=160x120:rate=12:duration=1 -c:v libx264 /tmp/photo.mov
live_photo=$(curl -fsS -b /tmp/alice.jar -H "X-CSRF-Token: $alice_csrf" \
  -F file=@/tmp/photo.png -F motion=@/tmp/photo.mov "$api/api/v1/photos/libraries/$alice_library/live-photos" | jq -r '.id' || true)
check "photo service imports both Live Photo originals through the isolated worker" test -n "$live_photo"
check "photo service streams browser-compatible motion" \
  test "$(status_of -b /tmp/alice.jar "$api/api/v1/photos/assets/$live_photo/motion/play")" = 200
check "administrator cannot read private motion without a viewing grant" \
  test "$(status_of -b /tmp/owner.jar "$api/api/v1/photos/assets/$live_photo/motion")" = 404
movies_dir=$(curl -fsS -b /tmp/owner.jar -H "X-CSRF-Token: $owner_csrf" -H 'Content-Type: application/json' \
  -d '{"parentId":"","name":"Movies"}' "$api/api/v1/spaces/$shared/directories" | jq -r .id || true)
ffmpeg -hide_banner -loglevel error -f lavfi -i testsrc2=size=640x360:rate=25:duration=6 -f lavfi -i sine=duration=6 \
  -c:v libx264 -pix_fmt yuv420p -c:a ac3 /tmp/heat.mkv
curl -fsS -o /dev/null -b /tmp/owner.jar -H "X-CSRF-Token: $owner_csrf" -F "parentId=$movies_dir" \
  -F "file=@/tmp/heat.mkv;filename=Heat (1995).mkv" "$api/api/v1/spaces/$shared/uploads" || true
# Ordinary file previews use the same sandbox without creating a media library.
video_file=$(curl -fsS -b /tmp/owner.jar "$api/api/v1/spaces/$shared/entries?parentId=$movies_dir" | jq -r '.items[0].id')
check "file management renders a video thumbnail" test "$(status_of -b /tmp/alice.jar "$api/api/v1/files/$video_file/preview?size=thumbnail")" = 200
# The preview must survive a Product Service restart even when decoding is
# unavailable. This also exercises the production state-directory permissions.
curl -fsS -b /tmp/alice.jar -D /tmp/video-preview.headers "$api/api/v1/files/$video_file/preview?size=thumbnail" -o /tmp/video-preview.jpg
preview_etag=$(sed -n 's/^[Ee][Tt][Aa][Gg]: //p' /tmp/video-preview.headers | tr -d '\r')
check "file previews return an ETag" test -n "$preview_etag"
check "file preview cache is private to Product Service" test "$(stat -c '%a %U' /var/lib/a-nas/file-previews)" = "700 a-nas"
systemctl stop anas-media.socket
systemctl restart anas-api
for _ in $(seq 1 100); do curl -fsS "$api/healthz" >/dev/null 2>&1 && break; sleep 0.2; done
check "video preview survives restart without the decoder" \
  curl -fsS -b /tmp/alice.jar "$api/api/v1/files/$video_file/preview?size=thumbnail" -o /tmp/video-preview-restarted.jpg
check "persisted video preview is unchanged" cmp /tmp/video-preview.jpg /tmp/video-preview-restarted.jpg
check "unchanged video preview revalidates with 304" \
  test "$(status_of -b /tmp/alice.jar -H "If-None-Match: $preview_etag" "$api/api/v1/files/$video_file/preview?size=thumbnail")" = 304
systemctl start anas-media.socket
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

# The services take turns from shared slots before they connect (issue #168):
# with every slot held elsewhere, a preview waits for one instead of failing.
check "the media worker's slots are open to its callers only" \
  test "$(stat -c '%a %U %G' /run/a-nas-media/slots):$(find /run/a-nas-media/slots -type f -perm 0440 | wc -l)" = "750 root a-nas-photos:8"
# Each account with the groups its unit gives it.
for account in a-nas:a-nas-photos,a-nas-shares a-nas-photos:a-nas-photos a-nas-shares:a-nas-photos; do
  check "${account%%:*} can take a media worker slot" \
    setpriv --reuid="${account%%:*}" --regid="${account%%:*}" --groups="${account#*:}" flock -n /run/a-nas-media/slots/7 true
done
check "all media workers share one memory limit" test \
  "$(systemctl show -p Slice --value anas-media@slot-check.service):$(systemctl show -p MemoryMax --value system-anasmedia.slice)" = "system-anasmedia.slice:3221225472"
ffmpeg -hide_banner -loglevel error -f lavfi -i color=c=teal:s=320x240 -frames:v 1 /tmp/slots.png
heif-enc -o /tmp/slots.heic /tmp/slots.png >/tmp/heif-encode.log 2>&1
slots_file=$(curl -fsS -b /tmp/owner.jar -H "X-CSRF-Token: $owner_csrf" -F 'parentId=' \
  -F 'file=@/tmp/slots.heic;filename=slots.HEIC' "$api/api/v1/spaces/$shared/uploads" | jq -r .id)
slot_holders=()
for slot in 0 1 2 3 4 5 6 7; do
  flock -n -o "/run/a-nas-media/slots/$slot" sleep 60 &
  slot_holders+=("$!")
done
sleep 0.5
# Only someone waiting can take the top slot: neither streams nor background
# work reach it.
(sleep 2; kill "${slot_holders[7]}") &
slot_releaser=$!
waited_from=$(date +%s%N)
slot_status=$(status_of -b /tmp/alice.jar "$api/api/v1/files/$slots_file/preview?size=thumbnail")
waited_ms=$(( ($(date +%s%N) - waited_from) / 1000000 ))
kill "${slot_holders[@]:0:7}" 2>/dev/null || true
wait "${slot_holders[@]}" "$slot_releaser" 2>/dev/null || true
check "a preview waits for a slot other processes hold, then renders" test "$slot_status:$((waited_ms >= 1500))" = "200:1"

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

# --- Disabling an account ends its SMB sessions. Under the Host Agent's
# RestrictAddressFamilies=AF_UNIX, smbstatus warns on standard error that it
# found no network interfaces; read together with its JSON, the warning
# failed every disable and every start's identity synchronization
# (docs/investigations/2026-10-26-smbstatus-warning-fails-identity-sync.md).
# shellcheck disable=SC2317,SC2329 # invoked through check (SC2317 in older shellcheck)
smb_sessions_of() { # USER -> how many SMB sessions USER has
  smbstatus --processes --user="$1" --json 2>/dev/null | jq --arg user "$1" '[.sessions[] | select(.username == $user)] | length'
}
# shellcheck disable=SC2317,SC2329 # invoked through check (SC2317 in older shellcheck)
smb_sessions_end() { for _ in $(seq 1 50); do [[ "$(smb_sessions_of "$1")" == 0 ]] && return; sleep 0.2; done; return 1; }
carol_password=$(secret)
carol_user=$(curl -fsS -b /tmp/owner.jar -H "X-CSRF-Token: $owner_csrf" -H 'Content-Type: application/json' \
  -d "{\"username\":\"carol\",\"password\":\"$carol_password\"}" "$api/api/v1/users" | jq -r .id || true)
smb_auth carol "$carol_password"
# A client that stays connected, as a mapped drive does.
smbclient "//$ip/carol" -A /tmp/carol.smb -m SMB3 --client-protection=encrypt < <(sleep 60) >/dev/null 2>&1 &
carol_client=$!
for _ in $(seq 1 50); do [[ "$(smb_sessions_of carol)" -gt 0 ]] && break; sleep 0.2; done
check "a member stays connected over SMB" test "$(smb_sessions_of carol)" -gt 0
check "the administrator disables the member" test "$(status_of -X DELETE -b /tmp/owner.jar \
  -H "X-CSRF-Token: $owner_csrf" "$api/api/v1/users/$carol_user")" = 204
check "disabling ends the member's SMB session" smb_sessions_end carol
check "the disabled member cannot open SMB" denied smb carol carol ls
kill "$carol_client" 2>/dev/null || true
# Every start synchronizes identities, the disabled member's SMB entry too.
synced_at=$(date '+%Y-%m-%d %H:%M:%S')
systemctl restart anas-api
for _ in $(seq 1 100); do curl -fsS "$api/healthz" >/dev/null 2>&1 && break; sleep 0.2; done
check "the Product Service's start synchronizes identities with a disabled SMB account" \
  bash -c "for _ in \$(seq 1 30); do journalctl -u anas-api --since '$synced_at' -o cat --no-pager |
    grep -Fq 'identity synchronization succeeded' && exit 0; sleep 1; done; exit 1"

# --- Restarting the Host Agent keeps all of the above.
restarted_at=$(date '+%Y-%m-%d %H:%M:%S')
systemctl restart anas-host-agent
check "Host Agent proves the identity switch again after a restart" \
  bash -c "for _ in \$(seq 1 30); do journalctl -u anas-host-agent --since '$restarted_at' -o cat --no-pager |
    grep -Fq 'file broker identity switch verified' && exit 0; sleep 1; done; exit 1"
login alice "$new_password" /tmp/alice.jar >/dev/null || true
check "sharing recovers after the Host Agent restarts" shares_ready
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
  test "$(find /opt/a-nas/models /opt/a-nas/ai-runtimes -mindepth 1 -maxdepth 1 | wc -l)" = "$((models_kept + 1))" \
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

# --- The AI assistant's sandbox on this installer's accounts, spaces and ACLs (#115).
# shellcheck disable=SC1091 # sourced from the mounted checkout
. /src/ai/assistant-spike/system-test.sh

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
