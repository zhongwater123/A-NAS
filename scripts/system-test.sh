#!/usr/bin/env bash
# System test of the multi-user permission model. It installs the release's
# Debian packages with apt from a signed repository (ADR 0034) into a
# disposable Debian 13 container where systemd is PID 1, then checks every
# entry point that touches the data volume: Web files (File Broker workers), SMB, the photo API, the FFmpeg
# worker shared by the media center and music, the administrator terminal,
# the LAN Web entry that Caddy serves on port 80, the internet entry's
# HTTPS site, and the App Center's Docker and container agent. Unlike tests that start the daemons by hand, it runs them under
# the production sandboxes, so it catches a unit that takes the Host Agent's
# CAP_SETUID (issue #38).
#
# The repository holds three releases: the one under test, the "previous" one
# (the same programs at a lower version, without the Docker policy dependency,
# as before that policy existed) and a "next" one whose Product
# Service has one more database step (internal/accounts/rollbacktest.go). The
# test installs the previous release, upgrades to this one, and at the end
# upgrades to the next, rolls back to this one and to the previous, and
# upgrades again, as the runbooks' apt commands do. On success it writes
# build/.system-tested, which names the packages make release may publish.
#
# Usage: make packages && scripts/build-system-test-image.sh &&
#        scripts/system-test.sh [IMAGE]
# Needs Docker and Go. Prints PASS/FAIL lines and exits non-zero on any
# failure.
#
# The AI assistant's sandbox (ai/assistant-spike/system-test.sh) runs without
# Hermes; ANAS_SYSTEM_TEST_ASSISTANT_RUNTIME naming the spike's runtime.tar.gz
# adds its policy check and a real assistant against a scripted model.
#
# The content packages (scripts/build-content-packages.sh) are stand-ins:
# local AI runs on stand-in models and runtime with the Worker's fake
# providers, and web search on a stand-in for SearXNG
# (scripts/system-test-search.py). With ANAS_SYSTEM_TEST_CONTENT naming a
# release content store (D:\A-NAS-releases\content, laid out as
# scripts/build-content-packages.sh describes), it installs the real models
# and runtimes instead; SearXNG then starts without reaching any engine.
# Qdrant and the text reader's llama-server are always the real ones that
# scripts/fetch-qdrant.sh and scripts/fetch-llama.sh fetch, and so are Docker
# and Caddy, which the repository carries as A-NAS's does
# (scripts/fetch-upstream-packages.sh, ADR 0015).
set -euo pipefail

if [[ "${1:-}" != --inside ]]; then
  image=${1:-anas-systemd:trixie}
  repo=$(cd "$(dirname "$0")/.." && pwd)
  shopt -s nullglob
  products=("$repo"/build/packages/a-nas_*.deb)
  ((${#products[@]} == 1)) || { echo "build/packages needs exactly one a-nas package: run make packages" >&2; exit 2; }
  # The photo scoring module is not in the packages; its own script installs it (ADR 0037).
  [[ -x "$repo/build/anas-scoring" ]] || { echo "missing build/anas-scoring: run make build-binaries" >&2; exit 2; }
  real=()
  if [[ -n "${ANAS_SYSTEM_TEST_CONTENT:-}" ]]; then
    content=build/content-packages
    bash "$repo/scripts/build-content-packages.sh" "$repo/$content" "$ANAS_SYSTEM_TEST_CONTENT" >/dev/null
    real=(-e ANAS_SYSTEM_TEST_AI=real -e ANAS_SYSTEM_TEST_SEARCH=real)
  else
    content=build/stand-in-content
    bash "$repo/scripts/build-content-packages.sh" --stand-in "$repo/$content" "$repo/build" >/dev/null
  fi
  # The repository, signed with a key made for this run; GnuPG and aptly
  # run in the same image (scripts/apt-repository.sh).
  export IMAGE=$image
  version=$(dpkg-deb --field "${products[0]}" Version)
  work=$repo/build/system-test
  rm -rf -- "$work"
  trap 'rm -rf -- "$work"' EXIT
  bash "$repo/scripts/apt-repository.sh" key "$work/gnupg" >/dev/null
  bash "$repo/scripts/build-packages.sh" "$work/previous" "$version~previous" "$repo/build" >/dev/null
  # The old package boundary predates a-nas-docker-policy. A previous package
  # made entirely by today's builder would otherwise mask its removal on a
  # historical rollback. Keep the same programs for the database tests, but
  # give the previous host package that older dependency contract.
  legacy_host=$work/legacy-host
  dpkg-deb --raw-extract "$work/previous/a-nas-host_${version}~previous_all.deb" "$legacy_host"
  sed -i -E 's/, a-nas-docker-policy \(>= [^)]*\)//' "$legacy_host/DEBIAN/control"
  dpkg-deb --root-owner-group -Zxz --build "$legacy_host" "$work/previous/a-nas-host_${version}~previous_all.deb" >/dev/null
  install -d "$work/next-programs"
  cp "$repo/build/anas-host-agent" "$repo/build/anas-container-agent" "$work/next-programs/"
  (cd "$repo" && go build -tags rollbacktest -trimpath -ldflags "-X main.version=$version+next" \
    -o "$work/next-programs/anas-api" ./cmd/anas-api)
  bash "$repo/scripts/build-packages.sh" "$work/next" "$version+next" "$work/next-programs" >/dev/null
  # a-nas-host depends on the keyring at least at its own version.
  for release in "$version" "$version+next"; do
    bash "$repo/scripts/apt-repository.sh" keyring "$work/keyring" "$release" "$work/gnupg" >/dev/null
  done
  upstream=$(bash "$repo/scripts/fetch-upstream-packages.sh")
  mapfile -t upstream <<<"$upstream"
  bash "$repo/scripts/apt-repository.sh" publish "$work/repository" dev "$work/gnupg" \
    "$work"/previous/a-nas{,-host}_*.deb "$repo"/build/packages/a-nas{,-host,-docker-policy}_*.deb \
    "$work"/next/a-nas{,-host,-docker-policy}_*.deb \
    "$repo/$content"/*.deb "$work"/keyring/*.deb "${upstream[@]}" >/dev/null
  assistant=()
  if [[ -n "${ANAS_SYSTEM_TEST_ASSISTANT_RUNTIME:-}" ]]; then
    [[ -f "$ANAS_SYSTEM_TEST_ASSISTANT_RUNTIME" ]] || { echo "ANAS_SYSTEM_TEST_ASSISTANT_RUNTIME must name runtime.tar.gz" >&2; exit 2; }
    assistant=(-v "$ANAS_SYSTEM_TEST_ASSISTANT_RUNTIME:/assistant-runtime.tar.gz:ro")
  fi
  name="anas-system-test-$$"
  docker run -d --name "$name" --privileged --cgroupns=host -v /sys/fs/cgroup:/sys/fs/cgroup:rw \
    --tmpfs /run --tmpfs /run/lock -v "$repo:/src:ro" -e ANAS_SYSTEM_TEST_REPOSITORY=/src/build/system-test/repository \
    -e "ANAS_SYSTEM_TEST_VERSION=$version" "${real[@]}" "${assistant[@]}" "$image" >/dev/null
  trap 'docker rm -f "$name" >/dev/null 2>&1; rm -rf -- "$work"' EXIT
  for _ in $(seq 1 60); do
    state=$(docker exec "$name" systemctl is-system-running 2>/dev/null || true)
    [[ "$state" == running || "$state" == degraded ]] && break
    sleep 1
  done
  status=0
  rm -f "$repo/build/.system-tested"
  docker exec "$name" bash /src/scripts/system-test.sh --inside || status=$?
  if ((status == 0)); then
    (cd "$repo/build/packages" && sha256sum -- *.deb >"$repo/build/.system-tested")
  fi
  # The container goes with this script, so the logs a failed run gathered
  # come out first; CI uploads them (#318).
  if ((status != 0)); then
    rm -rf "$repo/build/system-test-logs"
    docker cp "$name:/var/tmp/system-test-logs" "$repo/build/system-test-logs" >/dev/null 2>&1 &&
      echo "the failed run's journal and logs are in build/system-test-logs" >&2
  fi
  exit "$status"
fi

if [[ $EUID -ne 0 || "$(cat /proc/1/comm)" != systemd || ! -f /.dockerenv ]]; then
  echo "run through scripts/system-test.sh, inside its disposable systemd container" >&2
  exit 2
fi

# Register before initialization so setup and first-install failures leave logs too.
failures=0
# A failed run's container is removed with it, so this prints what systemd
# reported and keeps the whole journal, the unit list and the installers' and
# services' logs for the outer script to copy out. They go to /var/tmp:
# /tmp is a tmpfs here, which docker cp does not see.
diagnose() {
  local status=$? logs=/var/tmp/system-test-logs
  ((status != 0 || failures != 0)) || return 0
  echo "=== the run failed: exit status $status, $failures failed checks"
  systemctl --failed --no-pager || true
  journalctl -b --no-pager -p warning -n 60 || true
  mkdir -p "$logs"
  journalctl -b --no-pager >"$logs/journal.txt" 2>&1 || true
  systemctl list-units --all --no-pager >"$logs/units.txt" 2>&1 || true
  cp /tmp/*.log "$logs/" 2>/dev/null || true
}
trap diagnose EXIT

# A Debian host's mounts are shared, so mounting or unmounting the data volume
# reaches the services' private mount namespaces; the container starts with
# private mounts, which would hide that.
mount --make-rshared /

# The release under test, the one before it and the next one, in the signed
# repository the outer script made, with the content packages, real or
# stand-ins.
# shellcheck source=lib/content.sh
. /src/scripts/lib/content.sh
repository=$ANAS_SYSTEM_TEST_REPOSITORY
version=$ANAS_SYSTEM_TEST_VERSION
previous=$version~previous
next=$version+next
# Each model role's manifest, as $ai/ROLE.json, for the checks below.
ai=/tmp/ai-manifests
install -d "$ai"
for role in "${ai_roles[@]}"; do
  cp "/src/deploy/models/${role##*:}.json" "$ai/${role%%:*}.json"
done
models_kept=$(for role in "${ai_roles[@]}"; do jq -r .sha256 "/src/deploy/models/${role##*:}.json"; done | sort -u | wc -l)
if [[ "${ANAS_SYSTEM_TEST_AI:-}" == real ]]; then
  detector_model="$(jq -r .name "$ai/detector.json")@$(jq -r .sha256 "$ai/detector.json" | cut -c1-12)+"
  face_model="$(jq -r .name "$ai/face-detector.json")@$(jq -r .sha256 "$ai/face-detector.json" | cut -c1-12)+"
  animal_model="$(jq -r .name "$ai/animal-embedder.json")@$(jq -r .sha256 "$ai/animal-embedder.json" | cut -c1-12)+"
  ocr_model="$(jq -r .name "$ai/ocr-model.json")@$(jq -r .sha256 "$ai/ocr-model.json" | cut -c1-12)+"
else
  # The stand-in models are random bytes: the Worker runs its fake providers.
  detector_model=fake-detector
  face_model=fake-faces
  animal_model=fake-animals
  ocr_model=fake-reader
  install -d /etc/systemd/system/anas-ai.service.d
  printf '[Service]\nExecStart=\nExecStart=/usr/lib/a-nas/ai/runtime/bin/python -m anas_ai.worker --fake --fake-detector --fake-faces --fake-animals --fake-reader\n' \
    > /etc/systemd/system/anas-ai.service.d/system-test.conf
fi
# AI starts once the photo library has gone unused for this long, so that the
# test sees a photo organised in the Worker's sandbox.
install -d /etc/systemd/system/anas-photos.service.d
printf '[Service]\nEnvironment=ANAS_PHOTOS_AI_QUIET_PERIOD=2s\n' > /etc/systemd/system/anas-photos.service.d/system-test.conf

# Web search (ADR 0030) runs a stand-in for SearXNG under the real unit and its
# sandbox (scripts/system-test-search.py), which the stand-in runtime holds.
# With the real runtime, SearXNG starts without reaching any engine.
if [[ "${ANAS_SYSTEM_TEST_SEARCH:-}" != real ]]; then
  install -d /etc/systemd/system/anas-search.service.d
  printf '[Service]\nExecStart=\nExecStart=/usr/lib/a-nas/search/runtime/bin/python /usr/lib/a-nas/search/runtime/stand-in.py /run/a-nas-search/search.sock\n' \
    > /etc/systemd/system/anas-search.service.d/system-test.conf
fi

# A data volume as storage initialization leaves it, mounted before the
# packages install, as on the Experimental NAS. Larger than the 10 GiB
# reserve; sparse, so it costs little disk.
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
# An installation from before the packages, as the Experimental NAS has it
# (scripts/install-v1.0.1-system-services.sh, removed): a release that
# /opt/a-nas/current names, units and links in /etc, and the environment files
# that hold this machine's settings, here the SMB interface. The first install
# takes over from it (deploy/debian/a-nas/DEBIAN/postinst) and writes
# site.conf from those files; its checks follow the definitions below.
install -d /opt/a-nas/releases/system-test-installer /etc/systemd/system/multi-user.target.wants \
  /etc/systemd/system/sockets.target.wants /etc/systemd/system/smbd.service.d
ln -s /opt/a-nas/releases/system-test-installer /opt/a-nas/current
install -d -m 0750 /etc/a-nas
printf '%s\n' 'ANAS_STATE_DIR=/var/lib/a-nas' "ANAS_SMB_INTERFACE=$iface" > /etc/a-nas/host-agent.env
printf '%s\n' 'ANAS_HTTP_ADDR=127.0.0.1:8080' 'ANAS_HOSTSTATE_MODE=agent' > /etc/a-nas/anas-api.env
for unit in anas-host-agent.service anas-api.service anas-search-dns.path anas-vectors-proxy@.socket; do
  sed 's|/usr/lib/a-nas|/opt/a-nas/current|g' "/src/deploy/systemd/system/$unit" > "/etc/systemd/system/$unit"
done
printf '[Service]\nRestart=on-failure\n' > /etc/systemd/system/smbd.service.d/a-nas-lan-address.conf
ln -s /etc/systemd/system/anas-host-agent.service /etc/systemd/system/multi-user.target.wants/
ln -s /etc/systemd/system/anas-api.service /etc/systemd/system/multi-user.target.wants/
ln -s /etc/systemd/system/anas-vectors-proxy@.socket /etc/systemd/system/sockets.target.wants/anas-vectors-proxy@a-nas-photos.socket
# The installer enabled the internet entry's tunnel when it turned the entry on.
ln -s /usr/lib/systemd/system/wg-quick@.service /etc/systemd/system/multi-user.target.wants/wg-quick@wg-anas.service
systemctl daemon-reload

# apt reads the A-NAS repository alone, as the deploy runbook adds it: the
# image's Debian sources would need the network, and every Debian package
# A-NAS depends on is installed already. Docker and Caddy come from the
# repository.
install -d /etc/apt/sources.list.d/disabled
mv /etc/apt/sources.list /etc/apt/sources.list.d/*.list /etc/apt/sources.list.d/disabled/ 2>/dev/null || true
# The package starts the system update timer, whose runs would use apt
# beside the test's own; the test drives the update service through the
# Product Service instead (below), so the timer's first run comes after it.
install -d /etc/systemd/system/anas-update.timer.d
printf '[Timer]\nOnActiveSec=\nOnActiveSec=12h\nOnUnitInactiveSec=\nOnUnitInactiveSec=12h\n' \
  >/etc/systemd/system/anas-update.timer.d/system-test.conf
dpkg -i "$repository/pool/main/a/a-nas-archive-keyring/a-nas-archive-keyring_${version}_all.deb" >/tmp/keyring.log
sed -e "s|^URIs: .*|URIs: file:$repository|" -e 's|^Suites: .*|Suites: dev|' \
  /usr/share/doc/a-nas-archive-keyring/a-nas.sources >/etc/apt/sources.list.d/a-nas.sources
# apt_install VERSION NAME: the runbook's command for the product's packages
# at VERSION, which may be older; apt's output goes to /tmp/NAME.log.
apt_install() {
  apt-get update -qq >"/tmp/$2.log" 2>&1 &&
    apt-get install -y --no-install-recommends --allow-downgrades -o Dpkg::Options::=--force-confold \
      "a-nas=$1" "a-nas-host=$1" a-nas-docker-policy >>"/tmp/$2.log" 2>&1
}
installed_at=$(date '+%Y-%m-%d %H:%M:%S')
for release in "$previous" "$version"; do
  if ! apt_install "$release" "install-$release" || ! a-nas-check >"/tmp/check-$release.log" 2>&1; then
    tail -n 40 "/tmp/install-$release.log" "/tmp/check-$release.log"
    echo "FAIL apt installs A-NAS $release from the signed repository"
    exit 1
  fi
  echo "PASS apt installs A-NAS $release from the signed repository"
done

# Record the volume as storage initialization would have, so the Product
# Service's volume guard accepts it, then restart it to load the record. It
# creates its databases before it answers, so wait for that first. The share
# service restarts after it, as the package orders them: a restarted Product
# Service gives the share bridge a new generation, and until the share
# service's next check notices, every new share fails as withdrawn.
for _ in $(seq 1 100); do curl -fsS http://127.0.0.1:8080/healthz >/dev/null 2>&1 && break; sleep 0.2; done
systemctl stop anas-shares anas-api
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
systemctl start anas-api anas-shares

# What quiet, fails and denied keep out of the output, shown when their check
# fails.
hidden=/tmp/system-test-hidden.log
# check NAME COMMAND...: runs the command and records the outcome. A failure
# also prints the command as it ran, which shows the values a test compared,
# and the end of what quiet or denied hid (#318).
check() {
  local name=$1 ran
  shift
  : >"$hidden"
  if "$@"; then
    echo "PASS $name"
  else
    echo "FAIL $name"
    failures=$((failures + 1))
    ran=$(printf ' %q' "$@")
    echo "  ran:${ran:0:600}"
    if [[ -s "$hidden" ]]; then tail -n 20 "$hidden" | sed 's/^/  | /'; fi
  fi
}
# fails COMMAND...: the command fails, for any reason. A check that an account
# is refused access uses denied or cannot_create instead (#423).
# shellcheck disable=SC2317,SC2329 # invoked through check (SC2317 in older shellcheck)
fails() { ! "$@" >>"$hidden" 2>&1; }
# shellcheck disable=SC2317,SC2329 # invoked through check (SC2317 in older shellcheck)
quiet() { "$@" >>"$hidden" 2>&1; }
# refused USER COMMAND...: USER's COMMAND fails for want of permission
# (EACCES), not for another reason such as a missing account or a crash.
# shellcheck disable=SC2317,SC2329 # invoked through denied (SC2317 in older shellcheck)
refused() {
  local user=$1 out
  shift
  if out=$(LC_ALL=C as "$user" "$@" 2>&1); then
    printf '%s\n(succeeded as %s)\n' "$out" "$user" >>"$hidden"
    return 1
  fi
  printf '%s\n' "$out" >>"$hidden"
  [[ $out == *'Permission denied'* ]]
}
# denied USER COMMAND... PATH: PATH exists, as root sees it, and USER is
# refused COMMAND on it for want of permission. A path that is not there
# fails the check rather than passing as refused (#423).
# shellcheck disable=SC2317,SC2329 # invoked through check (SC2317 in older shellcheck)
denied() {
  if [[ ! -e ${!#} ]]; then
    echo "nothing at ${!#} to refuse" >>"$hidden"
    return 1
  fi
  refused "$@"
}
# cannot_create USER PATH: PATH's directory exists and PATH does not, and USER
# is refused creating PATH for want of permission.
# shellcheck disable=SC2317,SC2329 # invoked through check (SC2317 in older shellcheck)
cannot_create() {
  if [[ ! -d ${2%/*} || -e $2 ]]; then
    echo "no directory ${2%/*}, or $2 is already there" >>"$hidden"
    return 1
  fi
  refused "$1" touch "$2" && [[ ! -e $2 ]]
}
# finds PROBE FINDING...: the sandbox probe PROBE, run here outside its
# sandbox, reports every FINDING. A probe that fails for another reason, such
# as an exception, does not pass (#423).
# shellcheck disable=SC2317,SC2329 # invoked through check (SC2317 in older shellcheck)
finds() {
  local out finding
  out=$(python3 "$1" 2>&1) || true
  printf '%s\n' "$out" >>"$hidden"
  for finding in "${@:2}"; do
    [[ $out == *"$finding"* ]] || return 1
  done
}
# The Python that connects to the Unix socket named by its first argument.
unix_connect='import socket, sys; socket.socket(socket.AF_UNIX).connect(sys.argv[1])'
# running UNIT: the unit has a main process; otherwise prints its status.
# shellcheck disable=SC2317,SC2329 # invoked through check (SC2317 in older shellcheck)
running() {
  local pid
  if pid=$(systemctl show -p MainPID --value "$1") && [[ "$pid" =~ ^[1-9][0-9]*$ ]]; then
    return 0
  fi
  systemctl status --no-pager --lines=10 "$1"
  return 1
}
# shellcheck disable=SC2317,SC2329 # invoked through check (SC2317 in older shellcheck)
journal_has() { journalctl -u "$1" --since "$2" -o cat --no-pager | grep -Fq "$3"; }
# smbd_listens_only_on_its_interfaces: within 30 s, smbd listens on the SMB
# interface's address and on no wildcard address; otherwise prints where it
# listens. smbd tells systemd it is ready before it opens its listening
# sockets (source3/smbd/server.c: daemon_ready, then open_sockets_smbd).
# shellcheck disable=SC2317,SC2329 # invoked through check (SC2317 in older shellcheck)
smbd_listens_only_on_its_interfaces() {
  local listeners
  for _ in $(seq 1 30); do
    listeners=" $(ss -ltnH 'sport = :445' | awk '{print $4}' | paste -sd ' ') "
    if [[ $listeners == *" $ip:445 "* && $listeners != *" 0.0.0.0:445 "* && $listeners != *" [::]:445 "* && $listeners != *" *:445 "* ]]; then
      return 0
    fi
    sleep 1
  done
  echo "smbd listens on:$listeners" >>"$hidden"
  return 1
}
# shellcheck disable=SC2317,SC2329 # invoked through check (SC2317 in older shellcheck)
has_capabilities() {
  local pid cap_eff
  pid=$(systemctl show -p MainPID --value "$1")
  cap_eff=$(awk '/^CapEff:/ {print $2}' "/proc/$pid/status")
  (((16#$cap_eff >> 6 & 3) == 3))
}
# shellcheck disable=SC2317,SC2329 # invoked through refused (SC2317 in older shellcheck)
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

# --- The first install took over from the installation before the packages.
check "the takeover wrote site.conf from the installer's environment files" \
  test "$(grep -cx "ANAS_SMB_INTERFACE=$iface" /etc/a-nas/site.conf):$(grep -c '^ANAS_HTTP_ADDR=' /etc/a-nas/site.conf)" = 1:0
check "the takeover moved the installer's units, drop-ins and environment files aside" \
  test ! -e /etc/systemd/system/anas-host-agent.service -a ! -e /etc/systemd/system/anas-vectors-proxy@.socket \
  -a ! -e /etc/systemd/system/smbd.service.d/a-nas-lan-address.conf -a ! -e /etc/a-nas/host-agent.env \
  -a ! -e /opt/a-nas/current -a -f /var/backups/a-nas/installer-layout/etc/systemd/system/anas-host-agent.service \
  -a -f /var/backups/a-nas/installer-layout/etc/a-nas/anas-api.env
check "no link the installer made is left in /etc" \
  test -z "$(find /etc/systemd/system -type l \( -name 'anas-*' -o -name 'wg-quick@wg-anas.service' \) -print)"
check "the vector index's socket is on after the takeover" \
  test "$(systemctl is-active anas-vectors-proxy@a-nas-photos.socket)" = active

# reinstall NAME [VERSION]: installs the product's packages at VERSION, this
# release's unless given, with apt, the runbook's command, which runs their
# scripts as an upgrade does even over the same version; the content packages
# stay as they are. Then waits until the restarted Product Service answers and
# the photo service is ready again. apt's output goes to /tmp/NAME.log.
# shellcheck disable=SC2317,SC2329 # invoked through check (SC2317 in older shellcheck)
reinstall() {
  local since
  since=$(date '+%Y-%m-%d %H:%M:%S')
  if ! apt-get install -y --no-install-recommends --allow-downgrades --reinstall \
    "a-nas=${2:-$version}" "a-nas-host=${2:-$version}" >"/tmp/$1.log" 2>&1; then
    echo "  apt failed; the end of /tmp/$1.log:"
    tail -n 30 "/tmp/$1.log" | sed 's/^/  | /'
    return 1
  fi
  for _ in $(seq 1 150); do
    curl -fsS "$api/healthz" >/dev/null 2>&1 &&
      journalctl -u anas-photos --since "$since" -o cat --no-pager | grep -Fq 'photo service ready' && return
    sleep 0.2
  done
  echo "  apt finished, but the services were not ready within 30 seconds:"
  curl -sS -o /dev/null -w '  healthz answered %{http_code}\n' "$api/healthz" || true
  systemctl status --no-pager --lines=10 anas-api anas-photos | sed 's/^/  | /'
  return 1
}
# site_apply NAME: a-nas-site applies site.conf and internet.env, as the
# runbooks run it; its output goes to /tmp/NAME.log.
# shellcheck disable=SC2317,SC2329 # invoked through check (SC2317 in older shellcheck)
site_apply() {
  if ! a-nas-site >"/tmp/$1.log" 2>&1; then
    echo "  a-nas-site failed; the end of /tmp/$1.log:"
    tail -n 30 "/tmp/$1.log" | sed 's/^/  | /'
    return 1
  fi
  for _ in $(seq 1 150); do curl -fsS "$api/healthz" >/dev/null 2>&1 && return; sleep 0.2; done
  return 1
}
# product_environment: the Product Service's environment as systemd gives it.
product_environment() { systemctl show -p Environment --value anas-api | tr ' ' '\n'; }

# --- The release as packaged (ADR 0034): every file as dpkg installed it, and
# the product's own health check.
# shellcheck disable=SC2046 # one word per package
check "dpkg finds every file of the release's packages as packaged" \
  test -z "$(dpkg --verify a-nas a-nas-host $(content_packages /src) 2>&1)"
check "a-nas-check finds the installed product working" quiet a-nas-check

# apt takes only what the signed index lists: a package changed on the way and
# an index changed after signing are refused (#171).
tampered=/var/tmp/tampered-repository
this_deb=$(cd "$repository" && find pool -name "a-nas_${version}_amd64.deb")
install -d "$tampered/$(dirname "$this_deb")"
cp -r "$repository/dists" "$tampered/"
cp "$repository/$this_deb" "$tampered/$this_deb"
printf x >>"$tampered/$this_deb"
sed -i "s|^URIs: .*|URIs: file:$tampered|" /etc/apt/sources.list.d/a-nas.sources
apt-get update -qq >/tmp/tampered.log 2>&1 || true
check "apt refuses a package that differs from the signed index" \
  fails apt-get install -y --reinstall --no-install-recommends "a-nas=$version"
sed -i 's/^\(SHA256:\)$/\1\n 0000000000000000000000000000000000000000000000000000000000000000 1 main\/binary-amd64\/Packages/' \
  "$tampered/dists/dev/InRelease"
# apt update keeps the index it had and exits 0, so the refusal shows only in
# its output.
# shellcheck disable=SC2317,SC2329 # invoked through check
index_refused() { apt-get update 2>&1 | tee -a "$hidden" | grep -Fq 'OpenPGP signature verification failed'; }
check "apt refuses an index changed after signing" index_refused
sed -i "s|^URIs: .*|URIs: file:$repository|" /etc/apt/sources.list.d/a-nas.sources
apt-get update -qq >/tmp/apt-update.log 2>&1
rm -rf "$tampered"

# --- The units as installed, with every drop-in, read by Debian 13's systemd
# as on the NAS (#278): each directive and value, and that each command exists.
# The image has no Wayland compositor: Cage, which the local console's unit
# starts, is a stand-in that only has to exist.
[[ -x /usr/bin/cage ]] || install -m 0755 /dev/null /usr/bin/cage
# The container agent's unit is left out: it needs Docker, which its runbook
# installs.
# shellcheck disable=SC2046 # unit files have no spaces
check "systemd accepts every installed A-NAS unit" \
  verified $(find /usr/lib/systemd/system -maxdepth 1 -name '*anas*' ! -name anas-container-agent.service -type f)

# --- smbd opens its listening sockets only when it starts, and a reload keeps
# them: under Debian's configuration, before the Host Agent writes A-NAS's, it
# listens on every address. The Host Agent's first Samba configuration
# restarts it, so that it listens only on the loopback and the SMB interface
# (docs/investigations/2026-11-04-smbd-starts-before-dhcp-address.md). Waiting
# for that restart also keeps it from landing on the checks below.
for _ in $(seq 1 60); do journal_has anas-host-agent "$installed_at" 'data volume reconciled' && break; sleep 1; done
check "the Host Agent writes its Samba configuration once it serves" \
  journal_has anas-host-agent "$installed_at" 'data volume reconciled'
check "smbd then listens only on the loopback and SMB interface's addresses" smbd_listens_only_on_its_interfaces

# --- Every service runs under its production unit (issue #38).
# One unit per check: systemctl is-active A B succeeds when any of them is (#423).
for unit in anas-host-agent anas-photos anas-api anas-shares anas-family smbd; do
  check "services are active: $unit" systemctl is-active --quiet "$unit"
done
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
check "Product Service cannot list the Host Agent's records" denied a-nas ls /var/lib/a-nas-host

# --- The local console (issue #174): an account of its own that cannot log
# in, starting the launcher the a-nas package installs. The container has no
# display, so the unit is installed but neither enabled nor started.
check "the local console runs as its own account" \
  test "$(systemctl show -p User --value anas-kiosk@tty1.service)" = a-nas-kiosk
check "the local console's account has no home and no login shell" \
  test "$(getent passwd a-nas-kiosk | cut -d: -f6-7)" = /nonexistent:/usr/sbin/nologin
check "the local console's account has no password" test "$(passwd -S a-nas-kiosk | cut -d ' ' -f 2)" = L
check "the local console's launcher is the package's" \
  cmp -s /usr/lib/a-nas/kiosk-launcher /src/scripts/run-kiosk.sh
check "the local console's launcher is root's" test "$(stat -c '%U %a' /usr/lib/a-nas/kiosk-launcher)" = 'root 755'
check "the local console cannot write the product's files" cannot_create a-nas-kiosk /usr/lib/a-nas/written-by-kiosk
# The account that copies the release's repository to the NAS, as anas-dev.
id anas-dev >/dev/null 2>&1 || useradd --create-home --shell /bin/bash anas-dev
# lacks_group ACCOUNT GROUP: ACCOUNT's groups can be read, and GROUP is not among them.
# shellcheck disable=SC2317,SC2329 # invoked through check (SC2317 in older shellcheck)
lacks_group() {
  local groups
  groups=$(id -nG "$1") || return 1
  [[ " $groups " != *" $2 "* ]]
}
check "the local console is not in the staging account's group" lacks_group a-nas-kiosk anas-dev
check "the package leaves the local console disabled" \
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

# --- smbd listens only on the addresses present when it starts, so it waits
# for the SMB interface's IPv4 address and tries again until it is there
# (docs/investigations/2026-11-04-smbd-starts-before-dhcp-address.md).
check "systemd accepts smbd with a-nas-generator's drop-in" verified /usr/lib/systemd/system/smbd.service
check "smbd waits for the SMB interface's address" \
  grep -Fq "show dev $iface scope global" <<<"$(systemctl show -p ExecStartPre --value smbd)"
check "smbd starts again after a failed start" test "$(systemctl show -p Restart --value smbd)" = on-failure
# The same wait on an interface whose address comes late, as from DHCP at boot.
late_dropin=/etc/systemd/system/smbd.service.d/zz-system-test-late-address.conf
ip link add anas-late0 type veth peer name anas-late1
install -d /etc/systemd/system/smbd.service.d
ip link set anas-late0 up
sed "s/\b$iface\b/anas-late0/g" /run/systemd/generator/smbd.service.d/a-nas-lan-address.conf >"$late_dropin"
systemctl daemon-reload
systemctl stop smbd
systemctl start --no-block smbd
sleep 3
check "smbd does not start before the interface has an address" test "$(systemctl is-active smbd)" = activating
for _ in $(seq 1 30); do (($(systemctl show -p NRestarts --value smbd) > 0)) && break; sleep 1; done
check "smbd tries again while the address is missing" test "$(systemctl show -p NRestarts --value smbd)" -gt 0
ip address add 192.0.2.10/24 dev anas-late0
for _ in $(seq 1 10); do systemctl is-active --quiet smbd && break; sleep 1; done
check "smbd starts once the interface has an address" systemctl is-active --quiet smbd
rm -f "$late_dropin"
ip link delete anas-late0
systemctl daemon-reload
systemctl restart smbd
check "smbd listens on the SMB interface's address" smbd_listens_only_on_its_interfaces

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
sharing_smoke() {
  python3 /src/scripts/smoke-member-sharing.py "$api" /tmp/owner.jar /tmp/alice.jar \
    "$owner_csrf" "$alice_csrf" "$owner_file" "$alice_private" "${1:-initial}" && return 0
  journalctl -b -u anas-shares -u anas-api --no-pager -n 100 || true
  return 1
}
check "member sharing works through production service sandboxes" sharing_smoke
# --- Docker and Caddy (ADR 0015): apt installed them from A-NAS's repository
# as a-nas-host depends on them, at the versions deploy/upstream/packages.json
# pins. Docker waits for the address pool the App Center's runbook gives it
# (ADR 0013), then the container agent reaches it.
upstream_pinned() {
  local name pinned
  while read -r name pinned; do
    [[ "$(dpkg-query -W -f='${Version}' "$name" 2>>"$hidden")" == "$pinned" ]] || { echo "$name is not $pinned" >>"$hidden"; return 1; }
  done < <(jq -r '.packages[] | "\(.name) \(.version)"' /src/deploy/upstream/packages.json)
}
check "apt installed Docker and Caddy as A-NAS's repository pins them" upstream_pinned
check "Docker waits for its address pool" \
  bash -c '! systemctl is-active --quiet docker.service docker.socket && ! ip link show docker0 >/dev/null 2>&1'
systemctl start docker.socket docker.service >>"$hidden" 2>&1 || true
check "Docker does not start on request before it has its address pool" \
  bash -c '! systemctl is-active --quiet docker.service docker.socket'
install -d /etc/docker
for config in '{}' '{"log-driver":"json-file"}' '{"bip":"10.96.127.1/24"}' \
  '{"default-address-pools":[{"base":"10.96.64.0/19","size":24}]}' \
  '{"bip":"10.96.127.1/24","default-address-pools":[]}' '{'; do
  printf '%s\n' "$config" >/etc/docker/daemon.json
  systemctl reset-failed docker.socket docker.service
  systemctl start docker.socket docker.service >>"$hidden" 2>&1 || true
  check "Docker refuses incomplete or malformed address-pool config: $config" \
    bash -c '! systemctl is-active --quiet docker.service docker.socket && ! ip link show docker0 >/dev/null 2>&1'
done
systemctl reset-failed docker.socket docker.service
printf '{"bip": "10.96.127.1/24", "default-address-pools": [{"base": "10.96.64.0/19", "size": 24}]}\n' >/etc/docker/daemon.json
check "Docker accepts the runbook's address pool" quiet dockerd --validate --config-file /etc/docker/daemon.json
systemctl start docker.socket docker.service >>"$hidden" 2>&1 || true
check "Docker starts with its address pool" systemctl is-active --quiet docker.service
check "Docker allocates networks from the pool alone" \
  test "$(docker info --format '{{json .DefaultAddressPools}}' 2>>"$hidden")" = '[{"Base":"10.96.64.0/19","Size":24}]'
check "Docker's bridge has the runbook's address" \
  test "$(ip -4 -o addr show dev docker0 2>>"$hidden" | awk '{print $4}')" = 10.96.127.1/24
systemctl start anas-container-agent.service >>"$hidden" 2>&1 || true
# The engine's version, as docker-ce's Debian version 5:29.9.3-1~debian.13~trixie carries it.
docker_engine=$(jq -r '.packages[] | select(.name == "docker-ce") | .version' /src/deploy/upstream/packages.json |
  sed -e 's/^[0-9]*://' -e 's/-[^-]*$//')
agent_snapshot() {
  for _ in $(seq 1 50); do
    setpriv --reuid=a-nas --regid=a-nas --init-groups curl -fsS --unix-socket /run/a-nas-container/agent.sock \
      http://agent/v1/snapshot 2>>"$hidden" | jq -e --arg version "$docker_engine" '.engine.version == $version' >/dev/null && return 0
    sleep 0.2
  done
  return 1
}
check "the container agent gives the Product Service the pinned Docker engine" agent_snapshot
# The rest of the test runs as the packages leave a machine whose
# administrator has not turned the App Center on.
systemctl stop anas-container-agent.service docker.socket docker.service >>"$hidden" 2>&1 || true
rm -f /etc/docker/daemon.json
ip link delete docker0 2>/dev/null || true
# --- LAN Web entry (ADR 0012): plain HTTP through Caddy on the LAN address.
lan=http://$ip
lasting_login() { # URL USER PASSWORD JAR -> the session JSON of a login that asks to last
  curl -fsS -c "$4" -H 'Content-Type: application/json' \
    -d "{\"username\":\"$2\",\"password\":\"$3\",\"localConsole\":true}" "$1/api/v1/session"
}
check "Caddy serves the LAN entry" systemctl is-active --quiet caddy
check "anas-site.service replaced Caddy's own configuration" grep -Fqx '# Managed by A-NAS.' /etc/caddy/Caddyfile
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
# terminal_refused URL JAR: the server answers the session's WebSocket handshake with 403;
# a client that crashes or cannot connect is not a refusal (#423).
# shellcheck disable=SC2317,SC2329 # invoked through check (SC2317 in older shellcheck)
terminal_refused() {
  local out
  if out=$(terminal "$1" "$2" 'exit 0' 2>&1); then
    printf '%s\n(a session opened)\n' "$out" >>"$hidden"
    return 1
  fi
  printf '%s\n' "$out" >>"$hidden"
  [[ $out == *'handshake refused: HTTP/1.1 403'* ]]
}
check "the terminal is off on a new device" \
  test "$(curl -fsS -b /tmp/owner.jar "$api/api/v1/terminal" | jq -r .enabled)" = false
check "a disabled terminal refuses sessions" terminal_refused "$api" /tmp/owner.jar
# The runbook step that turns it on.
printf 'ANAS_TERMINAL=enabled\n' >> /etc/a-nas/site.conf
check "a-nas-site applies site.conf" site_apply terminal-on
check "sharing recovers after the Product Service restarts" shares_ready
check "sharing can create, save and revoke after the Product Service restarts" \
  sharing_smoke after-api-restart
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
check "a member's session gets no shell" terminal_refused "$lan" /tmp/lan.jar
# The shell runs in a scope of its own, so what it logs is not the Host
# Agent's, even under its name (issue #166).
forged_at=$(date '+%Y-%m-%d %H:%M:%S')
scope_output=$(terminal "$lan" /tmp/owner-lan.jar $'echo "cgroup:$(cat /proc/self/cgroup)"\nlogger -t anas-host-agent "forged by a terminal"\nunshare --user true 2>/dev/null; echo "unshare:$?"\nexit 0\n' || true)
check "the shell runs in a scope of its own" grep -Eqx 'cgroup:0::/(.*/)?anas-terminal-owner-[0-9]+\.scope' <<<"$scope_output"
check "the shell cannot create namespaces" grep -qx 'unshare:[1-9][0-9]*' <<<"$scope_output"
check "what a shell logs reaches the journal" \
  bash -c "for _ in \$(seq 1 20); do journalctl -t anas-host-agent --since '$forged_at' -o cat --no-pager | grep -Fq 'forged by a terminal' && exit 0; sleep 0.2; done; exit 1"
check "but not as the Host Agent's" fails journal_has anas-host-agent "$forged_at" 'forged by a terminal'
check "nor in the Host Agent's log in Settings" \
  test "$(curl -fsS -b /tmp/owner.jar "$api/api/v1/system-logs?service=host-agent&q=forged" | jq '.items | length')" = 0

# --- A data volume the Host Agent cannot reconcile leaves it serving (issue
# #166). Btrfs turns a volume read-only when it finds errors; a remount stands
# in. The photo, share and family services hold databases open for writing
# on it, which a read-only remount refuses.
entries /tmp/alice.jar "$alice_private" >/dev/null || true
scopes_before=$(systemctl list-units --plain --no-legend 'anas-files-*' 'anas-terminal-*' | awk '{print $1}')
systemctl stop anas-photos anas-shares anas-family
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
systemctl start anas-photos anas-shares anas-family
check "once the volume is writable, the Host Agent reconciles it" \
  bash -c "for _ in \$(seq 1 50); do journalctl -u anas-host-agent --since '$writable_at' -o cat --no-pager | grep -Fq 'data volume reconciled' && exit 0; sleep 0.2; done; exit 1"
check "and reports nothing wrong" \
  bash -c "curl -fsS --unix-socket /run/a-nas/host-agent.sock http://host-agent/v1/state | jq -e '.reconcileFailed != true' >/dev/null"
check "sharing recovers on the writable volume" shares_ready

# --- No service identity reaches a space.
check "Product Service account cannot list a private space" denied a-nas ls "$data/spaces/private/alice"
check "Product Service account cannot list Shared" denied a-nas ls "$data/spaces/shared"
check "photo service cannot list Shared" denied a-nas-photos ls "$data/spaces/shared"

# --- SMB: the same users, the same files, the same ACLs.
smb_auth() { printf 'username = %s\npassword = %s\n' "$1" "$2" > "/tmp/$1.smb"; chmod 0600 "/tmp/$1.smb"; }
smb_auth owner "$owner_password"
smb_auth alice "$alice_password"
smb() { local user=$1 share=$2; shift 2; smbclient "//$ip/$share" -A "/tmp/$user.smb" -m SMB3 --client-protection=encrypt -c "$*"; }
# smb_refused STATUS USER SHARE COMMAND...: Samba refuses with STATUS, such as
# NT_STATUS_ACCESS_DENIED. smbd being down, a missing credentials file or a
# misspelled share (NT_STATUS_BAD_NETWORK_NAME) fail the check (#423).
# shellcheck disable=SC2317,SC2329 # invoked through check (SC2317 in older shellcheck)
smb_refused() {
  local status=$1 out
  shift
  if out=$(smb "$@" 2>&1); then
    printf '%s\n(it succeeded)\n' "$out" >>"$hidden"
    return 1
  fi
  printf '%s\n' "$out" >>"$hidden"
  [[ $out == *"$status"* ]]
}
check "member opens her home over SMB and sees her Web upload" grep -q alice.txt <(smb alice alice ls 2>/dev/null)
printf 'from smb %s\n' "$(secret)" > /tmp/alice-smb.txt
check "member writes over SMB" quiet smb alice alice "put /tmp/alice-smb.txt alice-smb.txt"
check "SMB write is owned by the member's UID" \
  test "$(owner_uid "$data/spaces/private/alice/alice-smb.txt")" = "$alice_id"
check "Web lists the file written over SMB" grep -qx alice-smb.txt <(entries /tmp/alice.jar "$alice_private")
check "administrator cannot open the member's home over SMB" smb_refused NT_STATUS_ACCESS_DENIED owner alice ls
check "member cannot open the administrator's home over SMB" smb_refused NT_STATUS_ACCESS_DENIED alice owner ls
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

# --- Cross-space moves (#180) preserve times through the confined Worker
# and recycle the source instead of deleting it permanently.
move_source="$data/spaces/private/alice/move-times"
move_target="$data/spaces/shared/move-times"
as alice mkdir "$move_source"
as alice cp /tmp/alice.txt "$move_source/dated.txt"
as alice touch -m -d '2020-01-02 03:04:05.123456789 UTC' "$move_source/dated.txt" "$move_source"
move_file_time=$(stat -c %y "$move_source/dated.txt")
move_dir_time=$(stat -c %y "$move_source")
move_source_id=$(curl -fsS -b /tmp/alice.jar "$api/api/v1/spaces/$alice_private/entries" | jq -r '.items[] | select(.name == "move-times") | .id')
move_id=$(curl -fsS -X PATCH -b /tmp/alice.jar -H "X-CSRF-Token: $alice_csrf" \
  -H 'Content-Type: application/json' -d "$(jq -nc --arg space "$shared" '{spaceId:$space}')" \
  "$api/api/v1/files/$move_source_id" | jq -r .id || true)
check "cross-space move publishes a new entry" test -n "$move_id" -a "$move_id" != null -a "$move_id" != "$move_source_id"
check "cross-space move preserves file nanosecond mtime" test "$(stat -c %y "$move_target/dated.txt" 2>/dev/null)" = "$move_file_time"
check "cross-space move preserves directory nanosecond mtime" test "$(stat -c %y "$move_target" 2>/dev/null)" = "$move_dir_time"
check "cross-space move inherits Shared read permissions" as owner cmp -s /tmp/alice.txt "$move_target/dated.txt"
check "cross-space move removes the source from its old location" test ! -e "$move_source"
move_trash_id=$(curl -fsS -b /tmp/alice.jar "$api/api/v1/trash" | jq -r '.items[] | select(.name == "move-times") | .id')
check "cross-space move leaves an operator recycle item" test -n "$move_trash_id" -a "$move_trash_id" != null
check "another user cannot restore the moved source" test "$(status_of -b /tmp/owner.jar -H "X-CSRF-Token: $owner_csrf" \
  -H 'Content-Type: application/json' -d '{}' "$api/api/v1/trash/$move_trash_id/restore")" = 403
check "the operator restores the moved source" test "$(status_of -b /tmp/alice.jar -H "X-CSRF-Token: $alice_csrf" \
  -H 'Content-Type: application/json' -d '{}' "$api/api/v1/trash/$move_trash_id/restore")" = 200
check "the restored source keeps its content" cmp -s /tmp/alice.txt "$move_source/dated.txt"

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
check "its record is gone" fails grep -qx "$gone_snapshot" <(snapshot_ids)
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
check "Product Service account cannot list the photo store" denied a-nas ls "$data/photos"

# --- Family center (ADR 0029): its own identity and store; it asks the photo
# service about a channel's photos with the reader's session.
check "family store belongs to the family service alone" \
  test "$(stat -c '%a %U' "$data/family")" = "700 a-nas-family"
check "Product Service account cannot list the family store" denied a-nas ls "$data/family"
check "member founds the family through the family service" \
  test "$(status_of -b /tmp/alice.jar -H "X-CSRF-Token: $alice_csrf" -H 'Content-Type: application/json' \
    -d '{"role":"妈妈"}' "$api/api/v1/family/join")" = 200

# --- Photo scoring, a removable module (ADR 0037): its own scripts install
# and remove it; it reads only the Shared folder 照片打分, and only read-only,
# and the Product Service forwards its API with the session token alone.
install -d /var/tmp/scoring-module
install -m 0755 /src/build/anas-scoring /src/scripts/modules/scoring/install.sh /src/scripts/modules/scoring/remove.sh /var/tmp/scoring-module/
install -m 0644 /src/deploy/modules/scoring/anas-module-scoring.service /src/deploy/modules/scoring/anas-module-scoring.socket \
  /src/deploy/modules/scoring/scoring.json /var/tmp/scoring-module/
check "photo scoring is not installed before its script runs" \
  test "$(status_of -b /tmp/alice.jar "$api/api/v1/modules/scoring/me")" = 404
check "photo scoring's install script installs it" quiet bash /var/tmp/scoring-module/install.sh /var/tmp/scoring-module
check "photo scoring's installed units pass systemd-analyze verify" \
  verified /etc/systemd/system/anas-module-scoring.service /etc/systemd/system/anas-module-scoring.socket
check "the module store belongs to the module alone" \
  test "$(stat -c '%a %U' "$data/module-scoring")" = "700 a-nas-scoring"
check "Product Service account cannot list the module store" denied a-nas ls "$data/module-scoring"
check "the module's socket is the Product Service's alone" \
  test "$(stat -c '%a %U %G' /run/a-nas-modules/scoring.sock)" = "660 a-nas-scoring a-nas-modules"
check "the desktop lists photo scoring as installed" \
  bash -c "curl -fsS -b /tmp/alice.jar '$api/api/v1/modules' | jq -e '.items == [{\"id\":\"scoring\"}]' >/dev/null"
check "a member nobody chose gets no batches" \
  test "$(status_of -b /tmp/alice.jar "$api/api/v1/modules/scoring/batches")" = 403
check "a member cannot reach the module's administration" \
  test "$(status_of -b /tmp/alice.jar "$api/api/v1/modules/scoring/admin/batches")" = 403
install -d "$data/spaces/shared/照片打分/第一批"
# A photo to score has a longer side of at least 2400 pixels; the small one
# is skipped.
python3 - "$data/spaces/shared/照片打分/第一批/a.png" <<'PY'
import struct, sys, zlib
def chunk(kind, data):
    return struct.pack(">I", len(data)) + kind + data + struct.pack(">I", zlib.crc32(kind + data))
width, height = 2400, 1600
row = bytes(value for x in range(width) for value in (x % 256, (x * 3) % 256, 96))
png = b"\x89PNG\r\n\x1a\n" + chunk(b"IHDR", struct.pack(">IIBBBBB", width, height, 8, 2, 0, 0, 0))
png += chunk(b"IDAT", zlib.compress(b"".join(b"\x00" + row for _ in range(height)))) + chunk(b"IEND", b"")
open(sys.argv[1], "wb").write(png)
PY
cp /tmp/photo.png "$data/spaces/shared/照片打分/第一批/small.png"
scoring_batch=$(curl -fsS -b /tmp/owner.jar -H "X-CSRF-Token: $owner_csrf" -H 'Content-Type: application/json' \
  -d '{"name":"第一批","source":"第一批","required":1,"rubric":"","dimensions":["overall","sharpness"]}' \
  "$api/api/v1/modules/scoring/admin/batches" | jq -r .id || true)
# shellcheck disable=SC2317,SC2329 # invoked through check (SC2317 in older shellcheck)
scoring_open() {
  for _ in $(seq 1 30); do
    curl -fsS -b /tmp/owner.jar "$api/api/v1/modules/scoring/admin/batches" |
      jq -e '.items[0].state == "open" and .items[0].photos == 1 and .items[0].import.skippedBy == [{"reason":"too_small","count":1}]' >/dev/null && return 0
    sleep 1
  done
  return 1
}
check "the module imports a folder of 照片打分 as a batch, skipping a photo too small to score" scoring_open
check "an administrator chooses a member as rater" \
  test "$(status_of -X PUT -b /tmp/owner.jar -H "X-CSRF-Token: $owner_csrf" "$api/api/v1/modules/scoring/admin/raters/$alice_user")" = 204
scoring_claim=$(curl -fsS -b /tmp/alice.jar -X POST -H "X-CSRF-Token: $alice_csrf" \
  "$api/api/v1/modules/scoring/batches/$scoring_batch/claims" | jq -r '.items[0].token' || true)
check "the rater sees the photo handed to her" \
  test "$(status_of -b /tmp/alice.jar "$api/api/v1/modules/scoring/claims/$scoring_claim/image")" = 200
check "the rater sees it at full size, read again from the original" \
  test "$(curl -fsS -o /dev/null -w '%{http_code} %{content_type}' -b /tmp/alice.jar "$api/api/v1/modules/scoring/claims/$scoring_claim/full")" = "200 image/jpeg"
check "the rater scores it on every 打分项" test "$(status_of -X PUT -b /tmp/alice.jar -H "X-CSRF-Token: $alice_csrf" \
  -H 'Content-Type: application/json' -d '{"submission":"system-test-0001","kind":"score","score":3.47,"scores":{"sharpness":2.5},"viewMs":1200}' \
  "$api/api/v1/modules/scoring/claims/$scoring_claim/rating")" = 200
scoring_pid=$(systemctl show -p MainPID --value anas-module-scoring)
# shellcheck disable=SC2317,SC2329 # invoked through check (SC2317 in older shellcheck)
module_sees() { nsenter -t "$scoring_pid" -m ls -A "$1" | tr '\n' ' '; }
check "the module sees nothing of the data volume but its store and 照片打分" \
  test "$(module_sees "$data")/$(module_sees "$data/spaces/shared")" = "module-scoring spaces /照片打分 "
# shellcheck disable=SC2317,SC2329 # invoked through check (SC2317 in older shellcheck)
module_writes() {
  nsenter -t "$scoring_pid" -m setpriv --reuid=a-nas-scoring --regid=a-nas-scoring \
  --groups="$(getent group a-nas-users | cut -d: -f3)" touch "$data/spaces/shared/照片打分/written-by-the-module"
}
check "the module cannot write in 照片打分" fails module_writes
check "photo scoring's remove script removes it" quiet bash /var/tmp/scoring-module/remove.sh
check "removed: units, program and account are gone" \
  bash -c '! systemctl cat anas-module-scoring.service >/dev/null 2>&1 && [[ ! -e /opt/a-nas/modules/scoring && ! -e /etc/a-nas/modules/scoring.json ]] && ! getent passwd a-nas-scoring >/dev/null'
check "removed: the API says photo scoring is not installed" \
  test "$(status_of -b /tmp/alice.jar "$api/api/v1/modules/scoring/me")" = 404
check "removed: the store stays until the runbook deletes it" test -d "$data/module-scoring"
check "removed: the photos stay in the Shared space" test -f "$data/spaces/shared/照片打分/第一批/a.png"
check "the runbook's deletion of the store succeeds" quiet btrfs subvolume delete "$data/module-scoring"

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
# shellcheck disable=SC2317,SC2329 # invoked through check (SC2317 in older shellcheck)
worker_memory_beside_photos() { # where internal/photoservice/gate.go workerCgroup reads it
  local photos root worker held
  photos=$(systemctl show -p ControlGroup --value anas-photos.service)
  root=${photos%/system.slice/*} # this container's systemd root under the host's
  worker=$(systemctl show -p ControlGroup --value anas-ai.service)
  # cgroup files report a size of 0, so read the value.
  held=$(cat "/sys/fs/cgroup$root/anas.slice/anas-ai.slice/anas-ai.service/memory.current" 2>/dev/null)
  [[ "$worker" == "$root/anas.slice/anas-ai.slice/anas-ai.service" && "$held" =~ ^[1-9][0-9]*$ ]] ||
    { echo "the Worker is in '$worker' holding '$held', the photo service in '$photos'" >&2; return 1; }
}
check "the package turns the AI socket on" test -L /usr/lib/systemd/system/sockets.target.wants/anas-ai.socket
check "only the photo service reaches the AI socket" \
  test "$(stat -c '%a %U %G' /run/a-nas-ai/ai.sock)" = "660 root a-nas-photos"
check "the package links the models and runtime of its content packages" \
  test "$(readlink /usr/lib/a-nas/ai/model.litertlm)" = "/opt/a-nas/models/$(jq -r .sha256 "$ai/model.json")/$(jq -r .file "$ai/model.json")" \
  -a "$(readlink /usr/lib/a-nas/ai/detector.onnx)" = "/opt/a-nas/models/$(jq -r .sha256 "$ai/detector.json")/$(jq -r .file "$ai/detector.json")" \
  -a "$(readlink /usr/lib/a-nas/ai/face-detector.onnx)" = "/opt/a-nas/models/$(jq -r .sha256 "$ai/face-detector.json")/$(jq -r .file "$ai/face-detector.json")" \
  -a "$(readlink /usr/lib/a-nas/ai/face-embedder.onnx)" = "/opt/a-nas/models/$(jq -r .sha256 "$ai/face-embedder.json")/$(jq -r .file "$ai/face-embedder.json")" \
  -a "$(readlink /usr/lib/a-nas/ai/animal-embedder.onnx)" = "/opt/a-nas/models/$(jq -r .sha256 "$ai/animal-embedder.json")/$(jq -r .file "$ai/animal-embedder.json")" \
  -a "$(readlink /usr/lib/a-nas/ai/ocr-model.gguf)" = "/opt/a-nas/models/$(jq -r .sha256 "$ai/ocr-model.json")/$(model_link_file "$ai/ocr-model.json")" \
  -a "$(readlink /usr/lib/a-nas/ai/ocr-mmproj.gguf)" = "/opt/a-nas/models/$(jq -r .sha256 "$ai/ocr-mmproj.json")/$(jq -r .file "$ai/ocr-mmproj.json")" \
  -a "$(readlink /usr/lib/a-nas/ai/runtime)" = "/opt/a-nas/ai-runtimes/$(ai_runtime_id /src)"
# The OCR model comes in parts (scripts/split-model.sh), each from its own
# content package, beside the first one the link names.
# shellcheck disable=SC2317,SC2329 # invoked through check (SC2317 in older shellcheck)
model_parts_installed() {
  local directory part
  directory=/opt/a-nas/models/$(jq -r .sha256 "$1")
  while read -r part; do
    [[ -f "$directory/$part" ]] || { echo "no $directory/$part" >>"$hidden"; return 1; }
  done < <(jq -r '(.parts // [])[].file' "$1")
}
check "the OCR model's parts are all beside its first" model_parts_installed "$ai/ocr-model.json"
llama_sha=$(jq -r .sha256 /src/deploy/llama/llama.json)
check "the package links llama.cpp, only the members its manifest names" \
  test "$(readlink /usr/lib/a-nas/ai/llama)" = "/opt/a-nas/llama/$llama_sha" \
  -a -x "/opt/a-nas/llama/$llama_sha/llama-server" -a ! -e "/opt/a-nas/llama/$llama_sha/llama-cli" \
  -a "$(stat -c '%U %a' "/opt/a-nas/llama/$llama_sha")" = "root 755"
ai_cores=$(($(nproc) > 2 ? $(nproc) - 2 : 1))
check "the Worker may use every core but two" \
  test "$(systemctl show -p CPUQuotaPerSecUSec --value anas-ai.service)" = "${ai_cores}s"
memory_kib=$(awk '$1 == "MemTotal:" { print $2 }' /proc/meminfo)
check "the Worker's memory is capped only at all but 2.5 GiB, against a runaway" \
  test "$(systemctl show -p MemoryHigh --value anas-ai.service):$(systemctl show -p MemoryMax --value anas-ai.service)" = \
  "infinity:$(((memory_kib > 3670016 ? memory_kib - 2621440 : 1048576) * 1024))"
check "the Worker's models are never swapped out, and it is the first to end when memory runs out" \
  test "$(systemctl show -p MemorySwapMax --value anas-ai.service):$(systemctl show -p OOMScoreAdjust --value anas-ai.service)" = 0:500
check "the Worker runs in its own slice, apart from the device's other work" \
  test "$(systemctl show -p Slice --value anas-ai.service)" = anas-ai.slice
check "background work gives way to the device's other work where weights compare" \
  test "$(systemctl show -p CPUWeight --value anas.slice):$(systemctl show -p Slice --value anas-ai.slice)" = 10:anas.slice
check "AI work yields to the pressure of the device's other work, not its own" \
  journal_has anas-photos.service "$installed_at" "AI work yields to the pressure of"
check "search starts the Worker through its socket" semantic_search 猫
check "the Worker runs under its own dynamic identity" dynamic_identity anas-ai.service
check "the Worker's memory is where the photo service's gate reads it" worker_memory_beside_photos
check "the Worker records what it holds once started, for its models' memory" \
  journal_has anas-ai.service "$installed_at" "started with"
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
# read-text.py SOCKET IMAGE MODEL [WORD]: the Worker names its text reader,
# releases its other background models and reads IMAGE with the model named
# MODEL..., finding WORD in it.
cat > /usr/local/lib/read-text.py <<'PY'
import array, json, socket, struct, sys

def call(request, image=None):
    with socket.socket(socket.AF_UNIX, socket.SOCK_STREAM) as conn:
        conn.settimeout(600)
        conn.connect(sys.argv[1])
        body = json.dumps(request).encode()
        fds = [(socket.SOL_SOCKET, socket.SCM_RIGHTS, array.array('i', [image.fileno()]))] if image else []
        assert conn.sendmsg([struct.pack('>I', len(body))], fds) == 4
        conn.sendall(body)
        with conn.makefile('rb') as reply:
            size = struct.unpack('>I', reply.read(4))[0]
            return json.loads(reply.read(size))

info = call({'op': 'info'})
assert info['ok'] and info['ocrModel'].startswith(sys.argv[3]), info
# Reading is a phase of its own: the other background models go first.
assert call({'op': 'release', 'what': 'detector'})['ok']
with open(sys.argv[2], 'rb') as image:
    answer = call({'op': 'read_text', 'kind': 'text'}, image)
print(json.dumps(answer, ensure_ascii=False)[:400])
reading = answer['reading']
assert answer['ok'] and answer['model'] == info['ocrModel'] and reading['finish'] == 'stop' and reading['logprobs'], answer
assert len(sys.argv) < 5 or sys.argv[4] in reading['text'], reading['text']
PY
if [[ "${ANAS_SYSTEM_TEST_AI:-}" == real ]]; then
  ffmpeg -hide_banner -loglevel error -f lavfi -i color=c=white:s=640x200 -frames:v 1 \
    -vf "drawtext=text='A-NAS 2026':fontsize=72:fontcolor=black:x=40:y=60" /tmp/text.png
  check "llama-server reads the text in a photo in the Worker's sandbox" \
    python3 /usr/local/lib/read-text.py /run/a-nas-ai/ai.sock /tmp/text.png "$ocr_model" 2026
  echo "INFO the Worker's memory peak after reading: $(systemctl show -p MemoryPeak --value anas-ai.service) bytes"
else
  check "the Worker reads the text in a photo" python3 /usr/local/lib/read-text.py /run/a-nas-ai/ai.sock /tmp/photo.png "$ocr_model"
fi
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
sed -e 's|^ExecStart=.*|ExecStart=/usr/lib/a-nas/ai/runtime/bin/python /usr/local/lib/anas-ai-probe.py|' \
  -e 's|^Type=.*|Type=oneshot|' -e '/^Requires=/d' -e '/^After=/d' \
  /usr/lib/systemd/system/anas-ai.service > /etc/systemd/system/anas-ai-probe.service
systemctl daemon-reload
check "the probe finds the network and data volume outside the sandbox" \
  finds /usr/local/lib/anas-ai-probe.py 'opened a AF_INET socket' 'listed the data volume'
check "the Worker's sandbox opens no network socket and cannot read the data volume" \
  systemctl start anas-ai-probe.service
journalctl -u anas-ai-probe.service -o cat --no-pager | grep -E 'contained|opened|listed' | tail -n 1 || true
# The text reader's llama-server, in the Worker's unit, finds the libraries
# it links; reading with the real model (ANAS_SYSTEM_TEST_AI=real) also loads
# its CPU backend.
sed -e 's|^ExecStart=.*|ExecStart=/usr/lib/a-nas/ai/llama/llama-server --version|' \
  -e 's|^Type=.*|Type=oneshot|' -e '/^Requires=/d' -e '/^After=/d' \
  /usr/lib/systemd/system/anas-ai.service > /etc/systemd/system/anas-ai-llama-probe.service
systemctl daemon-reload
check "llama-server runs in the Worker's sandbox" systemctl start anas-ai-llama-probe.service
journalctl -u anas-ai-llama-probe.service -o cat --no-pager | grep -E '^version|error' | tail -n 1 || true
# dpkg keeps every file's checksum: a content file that changed shows, and its
# package installed again restores it.
detector_file=$(readlink /usr/lib/a-nas/ai/detector.onnx)
detector_package=$(dpkg-query --search "$detector_file" | cut -d: -f1)
printf x >> "$detector_file"
check "dpkg finds a model file that changed" bash -c "dpkg --verify '$detector_package' | grep -Fq '$detector_file'"
apt-get install -y --reinstall "$detector_package" >/tmp/detector-again.log 2>&1 || true
check "installing its package again restores it" test -z "$(dpkg --verify "$detector_package" 2>&1)"

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
# shellcheck disable=SC2317,SC2329 # invoked through check (SC2317 in older shellcheck)
journal_lacks() { ! journal_has "$@"; } # UNIT SINCE TEXT: the unit has not logged TEXT
check "the installer enables the photo service's vector index socket" \
  systemctl is-enabled --quiet anas-vectors-proxy@a-nas-photos.socket
check "the photo service builds its vector index from the Catalog" \
  journal_shows anas-photos.service "$installed_at" "photo vector index created"
check "and brings the organised photo into it" \
  journal_shows anas-photos.service "$installed_at" "photo vector index synced"
journalctl -u anas-photos.service --since "$installed_at" -o cat --no-pager | grep -F 'vector index' | tail -n 3 || true
# A restart clears what the service remembers of earlier searches: the last
# check, which spaces checks ten minutes apart, and which reasons to read the
# Catalog it has already logged.
searched_at=$(date '+%Y-%m-%d %H:%M:%S')
systemctl restart anas-photos
check "the photo service starts again" journal_shows anas-photos.service "$searched_at" "photo service ready"
check "a search reads the vector index" semantic_search 猫
check "and is checked against the Catalog in the background" \
  journal_shows anas-photos.service "$searched_at" "photo vector index check"
check "which agrees with the Catalog" \
  journal_has anas-photos.service "$searched_at" '"overlap10":1,"overlap100":1,"overlap500":1'
check "without falling back to the Catalog's vectors" \
  journal_lacks anas-photos.service "$searched_at" "photo search reads the Catalog's vectors"
check "only the photo service reaches its vector index" \
  test "$(stat -c '%a %U %G' /run/a-nas-vectors/a-nas-photos.sock)" = "600 a-nas-photos a-nas-photos"
check "the Product Service account cannot connect to the photo vector index" \
  denied a-nas python3 -c "$unix_connect" /run/a-nas-vectors/a-nas-photos.sock
check "the package links the Qdrant of its content package" \
  test "$(readlink /usr/lib/a-nas/vectors/qdrant)" = "/opt/a-nas/qdrant/$qdrant_sha/qdrant"
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
  denied a-nas-photos python3 -c "$unix_connect" /run/a-nas-vectors/a-nas-shares.sock
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
  "/usr/lib/systemd/system/anas-vectors@.service" > "/etc/systemd/system/anas-vectors-probe@.service"
systemctl daemon-reload
check "the vector index probe finds the network and data volume outside the sandbox" \
  finds /usr/local/lib/anas-vectors-probe.py 'sees interfaces' 'listed /srv/a-nas/data'
check "the vector index's sandbox has only loopback and cannot read the data volume or product state" \
  systemctl start anas-vectors-probe@a-nas-photos.service
journalctl -u anas-vectors-probe@a-nas-photos.service -o cat --no-pager | grep -E 'contained|sees|reached|opened|listed' | tail -n 1 || true

# --- Web search (ADR 0030): SearXNG under its own account, reached only by the
# Product Service through its socket, and able to reach public addresses only.
search_socket=/run/a-nas-search/search.sock
# The Product Service holds a-nas-search through its unit's SupplementaryGroups,
# not as a login group of a-nas.
as_product() { setpriv --reuid=a-nas --regid=a-nas --groups=a-nas-photos,a-nas-shares,a-nas-family,a-nas-search "$@"; }
check "web search runs under its own account" \
  test "$(systemctl is-active anas-search):$(systemctl show -p User --value anas-search)" = active:a-nas-search
check "web search's runtime is the one the release pins" \
  test "$(readlink /usr/lib/a-nas/search/runtime)" = "/opt/a-nas/search-runtimes/$(search_runtime_id /src)"
check "web search's socket is only its group's" \
  test "$(stat -c '%a %U %G' /run/a-nas-search):$(stat -c '%a %U %G' "$search_socket")" = \
  "750 a-nas-search a-nas-search:660 a-nas-search a-nas-search"
check "the Product Service's groups reach web search" \
  quiet as_product curl -fsS --unix-socket "$search_socket" http://localhost/healthz
check "a member does not reach web search" \
  denied alice python3 -c "$unix_connect" "$search_socket"
# search_denied PATH: PATH exists here, and web search's account is refused it inside web
# search's own mount namespace. One path per check: ls of several fails when any one does.
# shellcheck disable=SC2317,SC2329 # invoked through check (SC2317 in older shellcheck)
search_denied() {
  local pid out
  pid=$(systemctl show -p MainPID --value anas-search)
  if [[ ! -e $1 || ! $pid =~ ^[1-9][0-9]*$ ]]; then
    echo "no $1, or web search is not running (MainPID $pid)" >>"$hidden"
    return 1
  fi
  if out=$(LC_ALL=C nsenter -t "$pid" -m setpriv --reuid=a-nas-search --regid=a-nas-search --init-groups ls "$1" 2>&1); then
    printf '%s\n(listed as a-nas-search)\n' "$out" >>"$hidden"
    return 1
  fi
  printf '%s\n' "$out" >>"$hidden"
  [[ $out == *'Permission denied'* ]]
}
check "web search reads no data volume" search_denied /srv/a-nas/data
check "web search reads no Host Agent record" search_denied /var/lib/a-nas-host
# systemd 257 loses a running unit's IP counts at every daemon-reload, which
# the installs above ran, so the checks that read them restart web search.
restart_web_search() {
  systemctl restart anas-search
  for _ in $(seq 1 60); do
    as_product curl -fsS --unix-socket "$search_socket" http://localhost/healthz >/dev/null 2>&1 && return
    sleep 0.5
  done
}
if [[ "${ANAS_SYSTEM_TEST_SEARCH:-}" == real ]]; then
  # SearXNG itself in its unit's sandbox: the release's engines and plugins
  # loaded behind the WSGI entry, no IP traffic while it idles, and no error
  # logged while it answered. Every engine is loaded but those that need an
  # API key, Tor or a service of one's own, including those SearXNG ships
  # inactive.
  restart_web_search
  # shellcheck disable=SC2317,SC2329 # invoked through check
  searxng_config() {
    as_product curl -fsS --unix-socket "$search_socket" http://localhost/config |
      jq -e '[.engines[].name] as $names | ($names | length) > 300
        and ([("baidu", "bing", "startpage", "mojeek", "piped.music", "z-library")] - $names) == []
        and ([("youtube_api", "braveapi", "ahmia", "elasticsearch")] - $names | length) == 4
        and ([.plugins[].name] | index("tracker_url_remover") == null)' >/dev/null
  }
  check "SearXNG answers in its sandbox with the release's engines and plugins" searxng_config
  # Radio Browser looks its servers up in DNS as SearXNG starts; after that
  # nothing goes out until someone searches.
  sleep 15
  started=$(systemctl show -p IPEgressPackets --value anas-search)
  sleep 15
  check "SearXNG sends nothing while nobody searches" \
    test "$(systemctl show -p IPEgressPackets --value anas-search)" = "$started"
  check "SearXNG logs no error for the Product Service's requests" \
    test "$(journalctl -u anas-search -b -o cat --no-pager | grep -c 'ERROR' || true)" = 0
fi
if [[ "${ANAS_SYSTEM_TEST_SEARCH:-}" != real ]]; then
  # search_probe HOST PORT: what the stand-in, in web search's sandbox, got
  # when it connected there; direct_probe the same from outside any sandbox.
  # systemd drops the filtered packets, so a denied address times out.
  cat > /tmp/connect-probe.py <<'PY'
import errno, socket, sys
try:
    socket.create_connection((sys.argv[1], int(sys.argv[2])), timeout=2).close()
    print("connected")
except TimeoutError:
    print("timeout")
except OSError as error:
    print("denied" if error.errno == errno.EPERM else errno.errorcode.get(error.errno or 0, "error"))
PY
  search_probe() {
    as_product curl -fsS --unix-socket "$search_socket" "http://localhost/probe?host=$1&port=$2" | jq -r .result
  }
  direct_probe() { python3 /tmp/connect-probe.py "$1" "$2"; }
  # An address of each range the service must not reach, on this machine,
  # with something listening there, so that only the filter keeps it out.
  # 93.184.216.34 stands for a public address, which the filter lets through.
  for address in 10.99.0.1 172.16.99.1 192.168.99.1 100.64.99.1 169.254.99.1 192.168.77.53 93.184.216.34; do
    ip address add "$address/32" dev lo
  done
  ip -6 address add fd00::99/128 dev lo
  python3 -c '
import socket, threading
def serve(family, address):
    listener = socket.socket(family, socket.SOCK_STREAM)
    listener.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
    if family == socket.AF_INET6:
        listener.setsockopt(socket.IPPROTO_IPV6, socket.IPV6_V6ONLY, 1)
    listener.bind((address, 7777))
    listener.listen()
    while True:
        listener.accept()[0].close()
threading.Thread(target=serve, args=(socket.AF_INET6, "::"), daemon=True).start()
serve(socket.AF_INET, "0.0.0.0")' &
  search_listener=$!
  container_address=$(hostname -I | cut -d ' ' -f 1)
  # shellcheck disable=SC2317,SC2329 # invoked through check
  kept_out() { # HOST PORT: reachable directly, not from web search's sandbox
    [[ "$(direct_probe "$1" "$2")" == connected ]] && [[ "$(search_probe "$1" "$2")" =~ ^(timeout|denied)$ ]]
  }
  for target in 127.0.0.1:8080 "[::1]:7777" "$container_address:7777" 10.99.0.1:7777 172.16.99.1:7777 \
    192.168.99.1:7777 100.64.99.1:7777 169.254.99.1:7777 "[fd00::99]:7777"; do
    host=${target%:*}
    host=${host//[\[\]]/}
    check "web search cannot reach $target" kept_out "$host" "${target##*:}"
  done
  restart_web_search
  check "web search may reach a public address" test "$(search_probe 93.184.216.34 7777)" = connected
  # The count the real mode expects to stay at zero sees that connection.
  check "web search's IP traffic is counted" test "$(systemctl show -p IPEgressPackets --value anas-search)" -gt 0
  dns_server=$(awk '$1 == "nameserver" { print $2; exit }' /etc/resolv.conf)
  # shellcheck disable=SC2317,SC2329 # invoked through check
  dns_allowed() { systemctl show -p IPAddressAllow --value anas-search | grep -Fqw "$1"; }
  check "web search may reach this machine's DNS server $dns_server" dns_allowed "$dns_server"
  # dhcpcd rewrites /etc/resolv.conf when the lease changes.
  check "web search cannot reach 192.168.77.53 before it is a DNS server" kept_out 192.168.77.53 7777
  cp /etc/resolv.conf /tmp/resolv.conf.before-search
  printf 'nameserver 192.168.77.53\n' >> /etc/resolv.conf
  for _ in $(seq 1 20); do
    systemctl show -p IPAddressAllow --value anas-search | grep -Fq 192.168.77.53 && break
    sleep 0.5
  done
  check "web search follows a new DNS server" test "$(search_probe 192.168.77.53 7777)" = connected
  cat /tmp/resolv.conf.before-search > /etc/resolv.conf
  kill "$search_listener"
  for address in 10.99.0.1 172.16.99.1 192.168.99.1 100.64.99.1 169.254.99.1 192.168.77.53 93.184.216.34; do
    ip address del "$address/32" dev lo
  done
  ip -6 address del fd00::99/128 dev lo
  # The running Product Service, with its own groups and SearXNG's secret.
  # shellcheck disable=SC2317,SC2329 # invoked through check
  product_search() {
    curl -fsS -b /tmp/owner.jar -H "X-CSRF-Token: $owner_csrf" -H 'Content-Type: application/json' \
      -d '{"q":"system test","category":"web"}' "$api/api/v1/web-search/query" |
      jq -se '[.[].results[].title] == ["System test: system test"]
        and [.[].unresponsive[]] == ["百度"] and .[-1].pending == 0' >/dev/null
  }
  check "a search reaches the stand-in through the Product Service" product_search
fi
# The administrator's switch: the Host Agent writes its file and stops the
# service, and the unit's condition keeps it stopped across restarts and
# reinstalls (checked again after the upgrade below).
# shellcheck disable=SC2317,SC2329 # invoked through check
web_search_switch() {
  curl -fsS -o /dev/null -b /tmp/owner.jar -H "X-CSRF-Token: $owner_csrf" -H 'Content-Type: application/json' \
    -X PUT -d "{\"enabled\":$1,\"torrents\":false}" "$api/api/v1/web-search"
}
check "an administrator turns web search off" web_search_switch false
# The Host Agent does not wait for the stop; SearXNG takes a moment.
for _ in $(seq 1 40); do [[ "$(systemctl is-active anas-search || true)" == inactive ]] && break; sleep 0.5; done
check "turning it off stops the service and leaves the Host Agent's file" \
  test "$(systemctl is-active anas-search || true)" = inactive -a -f /var/lib/a-nas-host/web-search-disabled
systemctl restart anas-search
check "web search stays stopped while turned off" test "$(systemctl is-active anas-search || true)" = inactive
check "an administrator turns web search on" web_search_switch true
for _ in $(seq 1 40); do systemctl is-active --quiet anas-search && break; sleep 0.5; done
check "turning it on starts the service" systemctl is-active --quiet anas-search
# Web search that cannot start fails no upgrade: here its DNS helper fails,
# and the product's own services run.
install -d /etc/systemd/system/anas-search-dns.service.d
printf '[Service]\nExecStart=\nExecStart=/bin/false\n' > /etc/systemd/system/anas-search-dns.service.d/system-test-broken.conf
check "an upgrade finishes when web search cannot start" reinstall search-broken
for unit in anas-host-agent anas-photos anas-api anas-shares; do
  check "the product's services run after that upgrade: $unit" systemctl is-active --quiet "$unit"
done
rm /etc/systemd/system/anas-search-dns.service.d/system-test-broken.conf
systemctl daemon-reload
# Started over and over while broken, it has hit systemd's start limit.
systemctl reset-failed anas-search-dns.service anas-search-dns.path
systemctl restart anas-search-dns.path
systemctl start anas-search-dns.service
systemctl restart anas-search.service

# --- Media center (ADR 0017): files are read as the user through the File
# Broker; FFmpeg runs in a per-connection worker without network, data volume
# or Product Service state.
check "the package turns the FFmpeg worker socket on" test -L /usr/lib/systemd/system/sockets.target.wants/anas-ffmpeg.socket
check "the Product and Photo Services reach the FFmpeg worker socket" \
  test "$(stat -c '%a %U %G' /run/a-nas-ffmpeg/ffmpeg.sock)" = "660 root a-nas-photos"
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
systemctl stop anas-ffmpeg.socket
systemctl restart anas-api
for _ in $(seq 1 100); do curl -fsS "$api/healthz" >/dev/null 2>&1 && break; sleep 0.2; done
check "video preview survives restart without the decoder" \
  curl -fsS -b /tmp/alice.jar "$api/api/v1/files/$video_file/preview?size=thumbnail" -o /tmp/video-preview-restarted.jpg
check "persisted video preview is unchanged" cmp /tmp/video-preview.jpg /tmp/video-preview-restarted.jpg
check "unchanged video preview revalidates with 304" \
  test "$(status_of -b /tmp/alice.jar -H "If-None-Match: $preview_etag" "$api/api/v1/files/$video_file/preview?size=thumbnail")" = 304
systemctl start anas-ffmpeg.socket
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
check "Product Service cannot open the HEIC source itself" denied a-nas cat "$data/spaces/shared/preview.HEIC"

# The services take turns from shared slots before they connect (issue #168):
# with every slot held elsewhere, a preview waits for one instead of failing.
check "the FFmpeg worker's slots are open to its callers only" \
  test "$(stat -c '%a %U %G' /run/a-nas-ffmpeg/slots):$(find /run/a-nas-ffmpeg/slots -type f -perm 0440 | wc -l)" = "750 root a-nas-photos:8"
# Each account with the groups its unit gives it.
for account in a-nas:a-nas-photos,a-nas-shares a-nas-photos:a-nas-photos a-nas-shares:a-nas-photos; do
  check "${account%%:*} can take a FFmpeg worker slot" \
    setpriv --reuid="${account%%:*}" --regid="${account%%:*}" --groups="${account#*:}" flock -n /run/a-nas-ffmpeg/slots/7 true
done
check "all FFmpeg workers share one memory limit" test \
  "$(systemctl show -p Slice --value anas-ffmpeg@slot-check.service):$(systemctl show -p MemoryMax --value system-anasffmpeg.slice)" = "system-anasffmpeg.slice:3221225472"
ffmpeg -hide_banner -loglevel error -f lavfi -i color=c=teal:s=320x240 -frames:v 1 /tmp/slots.png
heif-enc -o /tmp/slots.heic /tmp/slots.png >/tmp/heif-encode.log 2>&1
slots_file=$(curl -fsS -b /tmp/owner.jar -H "X-CSRF-Token: $owner_csrf" -F 'parentId=' \
  -F 'file=@/tmp/slots.heic;filename=slots.HEIC' "$api/api/v1/spaces/$shared/uploads" | jq -r .id)
slot_holders=()
for slot in 0 1 2 3 4 5 6 7; do
  flock -n -o "/run/a-nas-ffmpeg/slots/$slot" sleep 60 &
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
check "the FFmpeg worker reads the film's duration in its sandbox" test "${film_duration%.*}" -ge 5
check "the member plays the original with range requests" \
  test "$(status_of -b /tmp/alice.jar -H 'Range: bytes=0-99' "$api/api/v1/media/videos/$film/file")" = 206
check "the FFmpeg worker converts the film to fragmented MP4" bash -c \
  "curl -s --max-time 60 -b /tmp/alice.jar '$api/api/v1/media/videos/$film/stream?mode=transcode&start=1&audio=0&quality=original' | head -c 64 | grep -aq ftyp"
check "the FFmpeg worker takes a frame of the film" bash -c \
  "for _ in \$(seq 1 30); do [[ \$(curl -s -o /dev/null -w '%{http_code}' -b /tmp/alice.jar '$api/api/v1/media/artwork/$film/thumb') == 200 ]] && exit 0; sleep 1; done; exit 1"
check "Product Service account cannot read the film itself" denied a-nas cat "$data/spaces/shared/Movies/Heat (1995).mkv"

# --- Music (ADR 0018) indexes the same files in place and has the shared
# FFmpeg worker probe and decode the formats browsers cannot play.
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
check "the FFmpeg worker reads the song's duration in its sandbox" test "$(jq -r '.durationMs // 0' <<<"${song:-null}" 2>/dev/null)" -ge 4900
check "the FFmpeg worker decodes the song to FLAC from a start position" bash -c \
  "curl -s --max-time 60 -b /tmp/alice.jar '$api/api/v1/music/tracks/$(jq -r .id <<<"${song:-null}" 2>/dev/null)/stream?start=1' | head -c 4 | grep -aq fLaC"
# A probe in the FFmpeg worker's own unit, in place of the worker.
cat > /usr/local/lib/anas-ffmpeg-probe.py <<'PY'
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
sed -e 's|^ExecStart=.*|ExecStart=/usr/bin/python3 /usr/local/lib/anas-ffmpeg-probe.py|' -e 's|^Type=.*|Type=oneshot|' \
  -e '/^StandardInput=/d' /usr/lib/systemd/system/anas-ffmpeg@.service > /etc/systemd/system/anas-ffmpeg-probe.service
systemctl daemon-reload
check "the media probe finds the network and data volume outside the sandbox" \
  finds /usr/local/lib/anas-ffmpeg-probe.py 'opened a AF_INET socket' 'listed /srv/a-nas/data'
check "the FFmpeg worker's sandbox has no network, data volume or Product Service state" \
  systemctl start anas-ffmpeg-probe.service
journalctl -u anas-ffmpeg-probe.service -o cat --no-pager | grep -E 'contained|opened|listed|ran as' | tail -n 1 || true

# Web search turned off before an upgrade stays off after it.
touch /var/lib/a-nas-host/web-search-disabled
systemctl stop anas-search
check "the release's packages install again over themselves" reinstall again
check "web search turned off stays off across an upgrade" test "$(systemctl is-active anas-search || true)" = inactive
rm -f /var/lib/a-nas-host/web-search-disabled
systemctl start anas-search
check "the upgrade copied the databases first" \
  test -n "$(find /var/backups/a-nas -path '*/system-disk/control.db')" -a -n "$(find "$data/.a-nas-backups" -path '*/photos/catalog.db')"
check "the databases' copies are root's alone" \
  test "$(stat -c '%U %a' /var/backups/a-nas):$(stat -c '%U %a' "$data/.a-nas-backups")" = 'root 700:root 700'
# The data volume's copies share the catalog's blocks: reflinked, not
# written again (btrfs).
check "the photo catalog's copy stays on the data volume" \
  test -z "$(find /var/backups/a-nas -name catalog.db)" -a "$(stat -f -c %T "$data/.a-nas-backups")" = btrfs
# A copy holds no -shm: SQLite rebuilds it from the log, and a stale one must
# never be restored.
check "the databases' copies leave out the log's index" \
  test -z "$(find /var/backups/a-nas "$data/.a-nas-backups" -name '*-shm')"
check "an upgrade keeps eight FFmpeg admission slots" test "$(find /run/a-nas-ffmpeg/slots -type f | wc -l)" = 8
check "music decodes through the worker after an upgrade" bash -c \
  "curl -s --max-time 60 -b /tmp/alice.jar '$api/api/v1/music/tracks/$(jq -r .id <<<"${song:-null}" 2>/dev/null)/stream?start=1' | head -c 4 | grep -aq fLaC"

# --- One password for Web and SMB.
new_password=$(secret)
check "member changes her password on the Web" test "$(status_of -b /tmp/alice.jar -H "X-CSRF-Token: $alice_csrf" \
  -H 'Content-Type: application/json' -d "{\"currentPassword\":\"$alice_password\",\"newPassword\":\"$new_password\"}" \
  "$api/api/v1/session/password")" = 204
check "the old password no longer opens SMB" smb_refused NT_STATUS_LOGON_FAILURE alice alice ls
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
check "the disabled member cannot open SMB" smb_refused NT_STATUS_ACCOUNT_DISABLED carol carol ls
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
installer=/usr/lib/a-nas/install-media-pool.sh
stager=/tmp/anas-dev # the staging account's home
staged=$stager/apps/a-nas/screensavers
uploads=0
pool_size() { curl -fsS "$api/local-console/screensavers" | jq '.videos | length'; }
current_pool() { readlink "$pools/current" || true; }
# The local console reads a pool's list only when its page opens, so the media
# installer restarts it after a switch, but only when it runs. It cannot run
# here without a display: the checks see systemd's records of start attempts,
# and a stand-in unit plays the running console.
# kiosk_starts: when systemd last tried to start the local console (0: never).
kiosk_starts() { systemctl show -p ConditionTimestampMonotonic --value anas-kiosk@tty1.service; }
# kiosk_run: the ID of the local console's current run.
kiosk_run() { systemctl show -p InvocationID --value anas-kiosk@tty1.service; }
# kiosk_restarted RUN: the local console runs again, in a run other than RUN.
# shellcheck disable=SC2317,SC2329 # invoked through check (SC2317 in older shellcheck)
kiosk_restarted() { [[ $(systemctl is-active anas-kiosk@tty1.service) == active && $(kiosk_run) != "$1" ]]; }
# kiosk_stand_in start|stop: a stand-in for the local console's instance, which
# takes precedence over the installed template while it exists.
# shellcheck disable=SC2317,SC2329 # invoked through check (SC2317 in older shellcheck)
kiosk_stand_in() {
  local unit=/run/systemd/system/anas-kiosk@tty1.service
  if [[ $1 == start ]]; then
    printf '[Service]\nExecStart=/bin/sleep infinity\n' > "$unit"
    systemctl daemon-reload
    systemctl start anas-kiosk@tty1.service
  else
    systemctl stop anas-kiosk@tty1.service
    rm -f "$unit"
    systemctl daemon-reload
  fi
}
# new_video: a stand-in video at /tmp/HASH.mp4; prints HASH.
new_video() {
  local file hash
  file=$(mktemp /tmp/video.XXXXXX)
  head -c 4096 /dev/urandom > "$file"
  hash=$(sha256sum "$file" | cut -d ' ' -f 1)
  mv "$file" "/tmp/$hash.mp4"
  echo "$hash"
}
# stage_pool HASH...: stages a pool as scripts/deploy-media-pool.ps1 -Kind
# screensavers does, uploading only the videos the staging area lacks; sets
# pool to its ID.
stage_pool() {
  local hash
  for hash in $(HOME=$stager bash /src/scripts/remote-stage-media-pool.sh screensavers missing "$@"); do
    cp "/tmp/$hash.mp4" "$staged/objects/$hash.mp4.incoming"
    uploads=$((uploads + 1))
  done
  pool=$(HOME=$stager bash /src/scripts/remote-stage-media-pool.sh screensavers commit "$@")
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
check "the old staging script's name stages the same pool" \
  test "$(HOME=$stager bash /src/scripts/remote-stage-screensavers.sh commit "$v1" "$v2")" = "$pool_a"
kiosk_before=$(kiosk_starts)
check "a staged pool installs" quiet "$installer" screensavers "$pool_a" "$staged"
check "the install leaves a local console that is not running alone" test "$(kiosk_starts)" = "$kiosk_before"
check "it becomes the current pool" test "$(current_pool):$(pool_size)" = "pools/$pool_a:2"
model_inode=$(stat -c %i "$(readlink /usr/lib/a-nas/ai/model.litertlm)")
check "a release installs" reinstall upgrade
check "the upgrade keeps the operator's terminal opt-in" \
  test "$(grep -cx 'ANAS_TERMINAL=enabled' /etc/a-nas/site.conf):$(curl -fsS -b /tmp/owner.jar "$api/api/v1/terminal" | jq -r .enabled)" = 1:true
check "the upgrade keeps the current pool" test "$(current_pool):$(pool_size)" = "pools/$pool_a:2"
check "the upgrade leaves the models and runtimes as their packages installed them" \
  test "$(find /opt/a-nas/models /opt/a-nas/ai-runtimes -mindepth 1 -maxdepth 1 | wc -l)" = "$((models_kept + 1))" \
  -a "$(stat -c %i "$(readlink /usr/lib/a-nas/ai/model.litertlm)")" = "$model_inode"
login alice "$new_password" /tmp/alice.jar >/dev/null || true
check "search reaches the upgraded release's Worker" semantic_search 猫
stage_pool "$v2" "$v3"
pool_b=$pool
check "a new pool uploads only its new video" test "$uploads" = 2
check "the new pool installs" quiet "$installer" screensavers "$pool_b" "$staged"
check "it replaces the current pool" test "$(current_pool)" = "pools/$pool_b"
check "a video both pools hold is stored once" \
  test "$(find "$pools/objects" -type f | wc -l):$(stat -c %h "$pools/objects/$v2.mp4")" = 3:3
check "installing the same pool again changes nothing" quiet "$installer" screensavers "$pool_b" "$staged"
check "a stand-in local console runs for the screen saver checks" kiosk_stand_in start
run=$(kiosk_run)
check "switching back to an installed pool needs no staging, by the old name too" \
  quiet /usr/lib/a-nas/install-screensavers.sh "$pool_a" /nonexistent
check "the media rollback plays the old pool" test "$(current_pool)" = "pools/$pool_a"
check "the switch restarts the running local console" kiosk_restarted "$run"
v4=$(new_video)
stage_pool "$v4"
printf x >> "$staged/objects/$v4.mp4"
run=$(kiosk_run)
check "a video that does not match its hash is refused" fails "$installer" screensavers "$pool" "$staged"
check "the refused pool changes nothing" \
  test "$(current_pool)" = "pools/$pool_a" -a ! -e "$pools/objects/$v4.mp4" -a ! -e "$pools/pools/$pool"
check "the refused pool leaves the local console running as it was" test "$(kiosk_run)" = "$run"
check "the stand-in local console goes away" kiosk_stand_in stop
cp "$staged/pools/$pool_b.sums" "$staged/pools/0123456789abcdef.sums"
check "a manifest that is not the named pool is refused" fails "$installer" screensavers 0123456789abcdef "$staged"
check "the Product Service reads the current pool" \
  grep -Fqx "ANAS_SCREENSAVER_DIRECTORY=$pools/current" <(product_environment)
video=$(curl -fsS "$api/local-console/screensavers" | jq -r '.videos[0]' || true)
check "a current pool video answers a byte range" test "$(status_of -r 0-1023 "$api$video")" = 206

# --- The companion's animations are long-lived media like the screen saver
# videos (ADR 0014): staged by hash, checked again and switched in by root.
companion=/var/lib/a-nas/companion
companion_staged=$stager/apps/a-nas/companion
animation_count() { curl -fsS "$api/companion/animations" | jq '.animations | length'; }
current_animations() { readlink "$companion/current" || true; }
# stage_animations NAME...: stages stand-in animations under these names as
# scripts/deploy-media-pool.ps1 -Kind companion does; sets pool to its ID.
stage_animations() {
  local name file hash
  install -d "$companion_staged/pools"
  : > "$companion_staged/pools/.incoming.sums"
  for name in "$@"; do
    file=$(mktemp /tmp/animation.XXXXXX)
    head -c 4096 /dev/urandom > "$file"
    hash=$(sha256sum "$file" | cut -d ' ' -f 1)
    printf '%s  %s\n' "$hash" "$name" >> "$companion_staged/pools/.incoming.sums"
    mv "$file" "/tmp/$hash.animation"
  done
  commit_animations
}
# commit_animations: uploads what the staged manifest lists and the staging
# area lacks, from /tmp/HASH.animation; sets pool to its ID.
commit_animations() {
  local hash
  for hash in $(HOME=$stager bash /src/scripts/remote-stage-media-pool.sh companion missing); do
    cp "/tmp/$hash.animation" "$companion_staged/objects/$hash.incoming"
  done
  pool=$(HOME=$stager bash /src/scripts/remote-stage-media-pool.sh companion commit)
}
check "a first install shows no companion animations" test "$(current_animations):$(animation_count)" = :0
# A release plays the pool its code was vendored with; the checks below stage
# stand-ins, which only --unpinned installs.
pinned_pool=$(sed -n 's/^companion_pool=//p' "$installer")
check "the release pins the pool of the repository's animation manifest" \
  test "$pinned_pool" = "$(sha256sum < /src/web/src/companion/dsh-pet/animations.sha256 | cut -c 1-16)"
install -d "$companion_staged/pools"
cp /src/web/src/companion/dsh-pet/animations.sha256 "$companion_staged/pools/$pinned_pool.sums"
# Its animations are not in the repository: the install gets as far as them.
pinned_out=$("$installer" companion "$pinned_pool" "$companion_staged" 2>&1 || true)
check "the pinned pool needs no --unpinned" grep -q 'is not staged as a regular file' <<<"$pinned_out"
rm -f "$companion_staged/pools/$pinned_pool.sums"
stage_animations 待机.webm 东张西望.webm 待机.mov
animations_a=$pool
check "a pool the release does not pin is refused" fails "$installer" companion "$animations_a" "$companion_staged"
check "the refusal installs nothing" test "$(current_animations)" = "" -a ! -e "$companion/pools/$animations_a"
# The uploader names the pool it computed from its own copy of the manifest.
cp "$companion_staged/pools/$animations_a.sums" "$companion_staged/pools/.incoming.sums"
check "staging refuses a manifest that is not the pool the uploader names" \
  fails env "HOME=$stager" bash /src/scripts/remote-stage-media-pool.sh companion commit 0123456789abcdef
check "and files nothing under that name" \
  test -e "$companion_staged/pools/.incoming.sums" -a ! -e "$companion_staged/pools/0123456789abcdef.sums"
check "staging files the pool the uploader names" \
  test "$(HOME=$stager bash /src/scripts/remote-stage-media-pool.sh companion commit "$animations_a")" = "$animations_a"
kiosk_before=$(kiosk_starts)
check "staged companion animations install" quiet "$installer" companion --unpinned "$animations_a" "$companion_staged"
check "installing them leaves a local console that is not running alone" test "$(kiosk_starts)" = "$kiosk_before"
check "they become the current pool" test "$(current_animations):$(animation_count)" = "pools/$animations_a:2"
check "the manifest names the current pool" \
  test "$(curl -fsS "$api/companion/animations" | jq -r .pool || true)" = "$animations_a"
animation=$(curl -fsS "$api/companion/animations" | jq -r '.animations["东张西望"].webm' || true)
check "an animation answers a byte range" test "$(status_of -r 0-1023 "$api$animation")" = 206
check "a pool's animations may be kept for good" \
  bash -c "curl -fsSI '$api$animation' | grep -Fqi 'cache-control: public, max-age=31536000, immutable'"
# MIT asks for its notice with every copy and the bundle keeps no comments, so
# the Web UI serves the licenses of the code copied into web/src (make web-build).
licenses=$(curl -fsS "$api/assets/third-party-licenses.txt" || true)
check "the Web UI ships dsh-pet's license, naming the commit copied" \
  grep -Fq "== web/src/companion/dsh-pet: PC2005-cloud/dsh-pet (https://github.com/PC2005-cloud/dsh-pet) commit $(sed -n 's/^PINNED = \"\([0-9a-f]*\)\"$/\1/p' /src/scripts/vendor-dsh-pet.py) ==" <<<"$licenses"
check "and every MIT notice of the code copied into web/src" \
  test "$(grep -c '^Permission is hereby granted' <<<"$licenses")" = \
  "$(find /src/web/src -name LICENSE -not -path '*/node_modules/*' -exec grep -l '^Permission is hereby granted' {} + | wc -l)"
# Its name has no hash: a browser asks for it again rather than keep an earlier release's notices.
check "the license list is not kept like the bundle's hashed files" \
  bash -c "curl -fsSI '$api/assets/third-party-licenses.txt' | grep -Fqi 'cache-control: no-cache'"
check "a stand-in local console runs for the companion checks" kiosk_stand_in start
# Two animations may share one file.
shared_animation=$(new_video)
cp "/tmp/$shared_animation.mp4" "/tmp/$shared_animation.animation"
printf '%s  %s\n' "$shared_animation" 待机.webm "$shared_animation" 东张西望.webm > "$companion_staged/pools/.incoming.sums"
commit_animations
animations_b=$pool
run=$(kiosk_run)
check "animations that share a file install" quiet "$installer" companion --unpinned "$animations_b" "$companion_staged"
check "the shared file is stored once" \
  test "$(current_animations):$(animation_count):$(stat -c %h "$companion/objects/$shared_animation")" = "pools/$animations_b:2:3"
check "installing animations restarts the running local console" kiosk_restarted "$run"
check "a page opened before the switch keeps its pool's animations" test "$(status_of -r 0-1023 "$api$animation")" = 206
run=$(kiosk_run)
check "switching back to installed animations needs no staging" quiet "$installer" companion --unpinned "$animations_a" /nonexistent
check "switching back restarts the running local console" kiosk_restarted "$run"
stage_animations 待机.webm
printf x >> "$companion_staged/objects/$(head -c 64 "$companion_staged/pools/$pool.sums")"
run=$(kiosk_run)
check "an animation that does not match its hash is refused" fails "$installer" companion --unpinned "$pool" "$companion_staged"
check "the refused animations change nothing" \
  test "$(current_animations)" = "pools/$animations_a" -a ! -e "$companion/pools/$pool"
check "the refused animations leave the local console running as it was" test "$(kiosk_run)" = "$run"
check "the stand-in local console goes away again" kiosk_stand_in stop
# A manifest whose last line has no newline: read would skip that line, which the
# pool ID still counts.
stage_animations 待机.webm 东张西望.webm
unterminated=$(head -c -1 "$companion_staged/pools/$pool.sums" | sha256sum | cut -c 1-16)
head -c -1 "$companion_staged/pools/$pool.sums" > "$companion_staged/pools/.incoming.sums"
check "staging refuses a manifest without its last newline" \
  fails env "HOME=$stager" bash /src/scripts/remote-stage-media-pool.sh companion commit
head -c -1 "$companion_staged/pools/$pool.sums" > "$companion_staged/pools/$unterminated.sums"
check "installing refuses a manifest without its last newline" fails "$installer" companion --unpinned "$unterminated" "$companion_staged"
check "the unterminated manifest installs nothing" test ! -e "$companion/pools/$unterminated"
# The same with a NUL byte where the last newline was: read and command
# substitution drop it unseen, the pool ID counts it.
{ head -c -1 "$companion_staged/pools/$pool.sums"; printf '\0'; } > /tmp/nul-ended.sums
nul_ended=$(sha256sum < /tmp/nul-ended.sums | cut -c 1-16)
cp /tmp/nul-ended.sums "$companion_staged/pools/.incoming.sums"
check "staging refuses a manifest that ends with a NUL byte" \
  fails env "HOME=$stager" bash /src/scripts/remote-stage-media-pool.sh companion commit
cp /tmp/nul-ended.sums "$companion_staged/pools/$nul_ended.sums"
check "installing refuses a manifest that ends with a NUL byte" fails "$installer" companion --unpinned "$nul_ended" "$companion_staged"
check "the NUL-ended manifest installs nothing" test ! -e "$companion/pools/$nul_ended"
# The staging account can swap what it staged for a FIFO or a link between the
# installer's checks and its copy: root reads regular files only, never waits
# for a writer, and reads no more than a pool may hold.
# refused_at_once COMMAND...: COMMAND fails, and well within 20 seconds.
# shellcheck disable=SC2317,SC2329 # invoked through check (SC2317 in older shellcheck)
refused_at_once() {
  local status=0
  timeout 20 "$@" >>"$hidden" 2>&1 || status=$?
  [[ $status -ne 0 && $status -ne 124 ]]
}
stage_animations 待机.webm
staged_object=$companion_staged/objects/$(head -c 64 "$companion_staged/pools/$pool.sums")
rm -f "$staged_object"
mkfifo "$staged_object"
check "an animation staged as a FIFO is refused at once" refused_at_once "$installer" companion --unpinned "$pool" "$companion_staged"
rm -f "$staged_object"
ln -s /dev/zero "$staged_object"
check "an animation staged as a link to a device is refused at once" refused_at_once "$installer" companion --unpinned "$pool" "$companion_staged"
ln -sfn /dev/zero "$companion_staged/pools/0123456789abcdef.sums"
check "a manifest staged as a link is refused at once" refused_at_once "$installer" companion --unpinned 0123456789abcdef "$companion_staged"
rm -f "$companion_staged/pools/0123456789abcdef.sums"
mkfifo "$companion_staged/pools/0123456789abcdef.sums"
check "a manifest staged as a FIFO is refused at once" refused_at_once "$installer" companion --unpinned 0123456789abcdef "$companion_staged"
rm -f "$companion_staged/pools/0123456789abcdef.sums"
head -c $(((1 << 20) + 1)) /dev/zero > "$companion_staged/pools/0123456789abcdef.sums"
check "a manifest larger than a pool's is refused" refused_at_once "$installer" companion --unpinned 0123456789abcdef "$companion_staged"
check "the refused stagings change nothing" test "$(current_animations)" = "pools/$animations_a" -a ! -e "$companion/pools/$pool"
# --- Internet entry (ADR 0032): /etc/a-nas/internet.env turns it on. Caddy
# serves the domain over HTTPS on the NAS's tunnel address, taking the client's
# address from the PROXY header that only the edge relay may send. There is no
# relay here: its tunnel address sits on a dummy interface and curl speaks for
# it, and Caddy's own CA stands in for ACME, which needs the real domain.
internet_host=nas.example.com
internet=https://$internet_host
relay_tunnel=fd78:1295:a3fe:1::1
nas_tunnel=fd78:1295:a3fe:1::2
printf '%s\n' '# The internet entry (docs/runbooks/enable-internet-access.md).' \
  "ANAS_INTERNET_HOST=$internet_host" 'ANAS_INTERNET_RELAY_ENDPOINT=192.0.2.1:51820' \
  "ANAS_INTERNET_RELAY_PUBLIC_KEY=$(wg genkey | wg pubkey)" 'ANAS_INTERNET_TUNNEL_PREFIX=fd78:1295:a3fe:1::/64' \
  > /etc/a-nas/internet.env
check "a-nas-site turns the internet entry on" site_apply internet-on
check "the Product Service has a loopback listener for the internet entry" \
  bash -c "systemctl show -p Environment --value anas-api | grep -Fq ANAS_INTERNET_HTTP_ADDR=127.0.0.1:8081 && ss -Hltn 'sport = :8081' | grep -Fq 127.0.0.1:8081"
check "Caddy serves the domain on the NAS's tunnel address only" \
  bash -c "grep -Fqx '$internet_host {' /etc/caddy/Caddyfile && grep -Fqx '	bind $nas_tunnel' /etc/caddy/Caddyfile"
check "Caddy takes PROXY headers only from the relay" grep -Fqx "				allow $relay_tunnel/128" /etc/caddy/Caddyfile
check "the tunnel's only peer is the relay, at one address" \
  bash -c "grep -Fqx 'AllowedIPs = $relay_tunnel/128' /etc/wireguard/wg-anas.conf && grep -Fqx 'Address = $nas_tunnel/64' /etc/wireguard/wg-anas.conf"
check "the tunnel's key is root's alone" test "$(stat -c '%U %a' /etc/wireguard/wg-anas.key)" = 'root 600'
check "anas-site.service logs this NAS's tunnel public key" \
  grep -Fq "this NAS's tunnel public key: $(wg pubkey < /etc/wireguard/wg-anas.key)" <(journalctl -u anas-site -o cat --no-pager)
check "the tunnel's firewall unit is accepted by systemd" verified /usr/lib/systemd/system/anas-internet-firewall.service
check "the tunnel's firewall table is loaded" quiet nft list table inet anas_internet
check "Caddy may bind the tunnel address before the tunnel is up" test "$(sysctl -n net.ipv6.ip_nonlocal_bind)" = 1
check "the LAN entry still serves plain HTTP" test "$(curl -fsS "$lan/healthz" | jq -r .status)" = ok

ip link add anas-relay type dummy 2>/dev/null || true
ip link set anas-relay up
ip -6 addr replace "$relay_tunnel/128" dev anas-relay nodad
ip -6 addr replace "$nas_tunnel/128" dev anas-relay nodad
python3 - <<'PY'
import pathlib
import re

caddyfile = pathlib.Path("/etc/caddy/Caddyfile")
caddyfile.write_text(re.sub(r"\ttls \{\n.*?\n\t\}\n", "\ttls internal\n", caddyfile.read_text(), flags=re.S))
PY
systemctl reload caddy
caddy_root=/var/lib/caddy/.local/share/caddy/pki/authorities/local/root.crt
# through [FROM] CURL_ARGUMENTS...: a request to the internet entry as the
# relay forwards it for a client at FROM, 2001:db8:7::7 unless given. curl
# writes its PROXY header in the family of its own connection, IPv6 here, so
# the clients it speaks for are IPv6 too; HAProxy writes the IPv4 client of
# the public connection.
through() {
  local from=2001:db8:7::7
  if [[ "$1" != -* ]]; then from=$1; shift; fi
  curl -sS --cacert "$caddy_root" --resolve "$internet_host:443:[$nas_tunnel]" --interface "$relay_tunnel" \
    --haproxy-protocol --haproxy-clientip "$from" "$@"
}
# shellcheck disable=SC2317,SC2329 # invoked through check (SC2317 in older shellcheck)
asks_for_https() {
  through -f -D - -o /dev/null "$internet/healthz" | tr -d '\r' | grep -ix 'strict-transport-security: max-age=31536000' >/dev/null
}
# shellcheck disable=SC2317,SC2329 # invoked through check (SC2317 in older shellcheck)
from_loopback() { # CURL_ARGUMENTS...: a request on the tunnel address that does not come from the relay
  curl -fsS --cacert "$caddy_root" --resolve "$internet_host:443:[$nas_tunnel]" --interface ::1 "$@" "$internet/healthz"
}
for _ in $(seq 1 50); do
  [[ -s "$caddy_root" ]] && through -f -o /dev/null "$internet/healthz" 2>/dev/null && break
  sleep 0.2
done
check "the internet entry reaches the Product Service over HTTPS" test "$(through -f "$internet/healthz" | jq -r .status)" = ok
check "the internet entry asks browsers to keep to HTTPS" asks_for_https
check "a connection without a PROXY header is served as it comes" quiet from_loopback
# proxy_header_refused: the listener closes a connection whose PROXY header does not come
# from the relay before TLS completes, so curl ends with 35 (handshake) or 56 (receive). An
# HTTP answer (22), a missing CA file (77) or no listener (7) is not that refusal (#423).
# shellcheck disable=SC2317,SC2329 # invoked through check (SC2317 in older shellcheck)
proxy_header_refused() {
  local status=0
  from_loopback --haproxy-protocol --haproxy-clientip 2001:db8:7::7 >>"$hidden" 2>&1 || status=$?
  echo "curl exit $status" >>"$hidden"
  ((status == 35 || status == 56))
}
check "a PROXY header from anywhere but the relay is refused" proxy_header_refused
check "the internet listener answers only for its domain" \
  test "$(status_of -H 'Host: 127.0.0.1' http://127.0.0.1:8081/healthz)" = 421

phone_login=$(through -f -D /tmp/phone.headers -c /tmp/phone.jar -H 'Content-Type: application/json' \
  -d "{\"username\":\"owner\",\"password\":\"$owner_password\",\"localConsole\":true}" "$internet/api/v1/session" || true)
phone_expiry=$(jq -r .expiresAt <<<"$phone_login" 2>/dev/null || true)
check "an internet login is an ordinary session even when it asks to last" \
  test -n "$phone_expiry" -a "$phone_expiry" != null -a "$phone_expiry" != 9999-12-31T23:59:59Z
check "an internet session's cookie is Secure" \
  bash -c "tr -d '\r' < /tmp/phone.headers | grep -i '^set-cookie: anas_session=' | grep -qi '; secure'"
cookie_of() { printf 'anas_session=%s' "$(awk '$6 == "anas_session" {print $7}' "$1")"; }
phone_cookie=$(cookie_of /tmp/phone.jar)
check "an internet session keeps working after the phone changes networks" \
  test "$(through 2001:db8:40::40 -o /dev/null -w '%{http_code}' -H "Cookie: $phone_cookie" "$internet/api/v1/session")" = 200
check "an internet session is refused through the LAN entry" \
  test "$(status_of -H "Cookie: $phone_cookie" "$lan/api/v1/session")" = 401
check "a LAN session is refused through the internet entry" \
  test "$(through -o /dev/null -w '%{http_code}' -H "Cookie: $(cookie_of /tmp/owner-lan.jar)" "$internet/api/v1/session")" = 401
check "the activity log records the internet sign-in with the client's address" \
  bash -c "curl -fsS -b /tmp/owner.jar '$api/api/v1/activity?category=session&q=owner' | jq -e 'any(.items[];
    .action == \"session.signed_in\" and (.detail | fromjson | .via == \"internet\" and .address == \"2001:db8:7::7\"))' >/dev/null"
phone_csrf=$(jq -r .csrfToken <<<"$phone_login" 2>/dev/null || true)
internet_upload=$(through -f -D - -o /dev/null -H "Cookie: $phone_cookie" -H "X-CSRF-Token: $phone_csrf" -H 'Tus-Resumable: 1.0.0' \
  -H 'Upload-Length: 1' -H "Upload-Metadata: $(tus_meta filename internet.bin spaceId "$owner_private")" \
  -X POST "$internet/api/v1/uploads" | header_of Location || true)
check "an internet upload address points back at the internet entry over HTTPS" grep -q "^$internet/api/v1/uploads/" <<<"$internet_upload"
[[ -z "$internet_upload" ]] || through -f -o /dev/null -H "Cookie: $phone_cookie" -H "X-CSRF-Token: $phone_csrf" \
  -H 'Tus-Resumable: 1.0.0' -X DELETE "$internet_upload" || true
check "the terminal says it is not offered through the internet entry" \
  test "$(through -f -H "Cookie: $phone_cookie" "$internet/api/v1/terminal" | jq -c .)" = '{"enabled":true,"internet":true}'
check "the internet entry opens no terminal" \
  test "$(through -H "Cookie: $phone_cookie" "$internet/api/v1/terminal/session" | jq -r .error.code)" = terminal_internet

# Removing the file turns the entry off again; the tunnel's key stays.
tunnel_key=$(wg pubkey < /etc/wireguard/wg-anas.key)
rm /etc/a-nas/internet.env
check "a-nas-site turns the internet entry off" site_apply internet-off
check "the Product Service no longer listens for the internet" \
  bash -c "! systemctl show -p Environment --value anas-api | grep -q ANAS_INTERNET_ && ! ss -Hltn 'sport = :8081' | grep -q ."
check "Caddy serves the LAN entry alone again" cmp -s /src/deploy/caddy/Caddyfile /etc/caddy/Caddyfile
check "the tunnel and its firewall are gone" \
  bash -c "! test -e /etc/wireguard/wg-anas.conf && ! nft list table inet anas_internet >/dev/null 2>&1"
check "the tunnel's key stays for the next time" test "$(wg pubkey < /etc/wireguard/wg-anas.key)" = "$tunnel_key"
check "the LAN entry still serves after the internet entry is gone" test "$(curl -fsS "$lan/healthz" | jq -r .status)" = ok
ip link del anas-relay

# --- The AI assistant's sandbox on the package's accounts, spaces and ACLs (#115).
# shellcheck disable=SC1091 # sourced from the mounted checkout
. /src/ai/assistant-spike/system-test.sh
# shellcheck disable=SC1091 # sourced from the mounted checkout
. /src/ai/assistant-spike/disable-system-test.sh

# --- Upgrade, roll back and upgrade again (ADR 0034), with apt from the
# signed repository. The next release adds a step to control.db; rolling back,
# this release takes the database back with the Down that step stored.
# probe_table: control.db's user_version and whether it has the next
# release's table.
probe_table() {
  python3 - <<'PY'
import sqlite3
db = sqlite3.connect("file:/var/lib/a-nas/control.db?mode=ro", uri=True)
version = db.execute("PRAGMA user_version").fetchone()[0]
probe = db.execute("SELECT COUNT(*) FROM sqlite_master WHERE name = 'rollback_probe'").fetchone()[0]
print(f"{version}:{probe}")
PY
}
check "apt upgrades to a release with a newer database step" reinstall upgrade-next "$next"
check "the newer release applied its step" test "$(probe_table)" = 1:1
check "the newer release brought its keyring" test "$(dpkg-query -W -f='${Version}' a-nas-archive-keyring)" = "$next"
check "the upgrade copied the databases first" \
  test -n "$(find /var/backups/a-nas -mindepth 2 -maxdepth 3 -path "*$version*/system-disk/control.db")"
check "apt rolls back to this release" reinstall rollback-this "$version"
check "this release took the database back with the newer release's Down" test "$(probe_table)" = 0:0
check "a-nas-check passes after the rollback" quiet a-nas-check
login alice "$new_password" /tmp/alice.jar >/dev/null || true
check "the rollback keeps the member's files" grep -qx alice.txt <(entries /tmp/alice.jar "$alice_private")
check "apt rolls back to the previous release" reinstall rollback-previous "$previous"
legacy_without_policy() {
  ! dpkg-query -W -f='${Depends}' a-nas-host | grep -Fq a-nas-docker-policy
}
policy_stays() {
  local file
  for file in /usr/lib/systemd/system/docker.{service,socket}.d/a-nas-docker-policy.conf \
    /usr/lib/a-nas-docker-policy/check-address-pool; do
    [[ "$(dpkg-query -S "$file")" == "a-nas-docker-policy: $file" ]] || return 1
  done
}
check "the previous host package has no Docker policy dependency" legacy_without_policy
check "Docker policy remains independently installed after a historical-layout rollback" policy_stays
systemctl start docker.socket docker.service >>"$hidden" 2>&1 || true
check "Docker stays stopped without a pool after rolling back to a pre-policy package" \
  bash -c '! systemctl is-active --quiet docker.service docker.socket && ! ip link show docker0 >/dev/null 2>&1'
printf '{}\n' >/etc/docker/daemon.json
systemctl reset-failed docker.socket docker.service
systemctl start docker.socket docker.service >>"$hidden" 2>&1 || true
check "an empty config cannot bypass the policy after rolling back" \
  bash -c '! systemctl is-active --quiet docker.service docker.socket && ! ip link show docker0 >/dev/null 2>&1'
systemctl reset-failed docker.socket docker.service
rm /etc/docker/daemon.json
check "a-nas-check passes on the previous release" quiet a-nas-check
check "a rollback keeps the newer keyring" test "$(dpkg-query -W -f='${Version}' a-nas-archive-keyring)" = "$next"
check "apt upgrades to this release again" reinstall upgrade-again "$version"
check "a-nas-check passes after upgrading again" quiet a-nas-check
login alice "$new_password" /tmp/alice.jar >/dev/null || true
check "the files are there after upgrading again" grep -qx alice.txt <(entries /tmp/alice.jar "$alice_private")
check "two copies of the databases stay, under the same names on both disks" \
  test "$(find /var/backups/a-nas -mindepth 1 -maxdepth 1 -name '2*' -printf '%f\n' | sort | paste -sd ' ')" = \
  "$(find "$data/.a-nas-backups" -mindepth 1 -maxdepth 1 -name '2*' -printf '%f\n' | sort | paste -sd ' ')" \
  -a "$(find /var/backups/a-nas -mindepth 1 -maxdepth 1 -name '2*' | wc -l)" = 2

# --- The system update service (docs/specs/system-update.md), driven through
# the Product Service as 设置 → 系统更新 drives it. A development machine's
# push lands in the staging directory site.conf names, here the signed
# repository itself; the service takes it into the local repository apt now
# reads. It installs the next release and goes back, rolls back a release
# whose check fails, and finishes an install a killed run left.
check "the package started the system update timer" systemctl is-active --quiet anas-update.timer
staging=/home/system-test-staging
install -d "$staging"
mount --bind "$repository" "$staging"
printf 'ANAS_UPDATE_STAGING=%s\n' "$staging" >>/etc/a-nas/site.conf
sed -i 's|^URIs: .*|URIs: file:/var/local/a-nas-repository|' /etc/apt/sources.list.d/a-nas.sources
update_jar=/tmp/owner-update.jar
update_csrf=$(login owner "$owner_password" "$update_jar" || true)
alice_update_csrf=$(login alice "$new_password" /tmp/alice-update.jar || true)
update_state() { curl -fsS -b "$update_jar" "$api/api/v1/system/update"; }
# update_ask ACTION: asks for ACTION as the page does; prints the HTTP status.
update_ask() {
  status_of -b "$update_jar" -H "X-CSRF-Token: $update_csrf" -H 'Content-Type: application/json' \
    -d "{\"action\":\"$1\"}" "$api/api/v1/system/update/actions"
}
# update_settled: waits until no run is in progress, reading on through the
# Product Service's restart an install brings, and prints the state.
update_settled() {
  local state=""
  for _ in $(seq 1 900); do
    if state=$(update_state 2>/dev/null) &&
      jq -e '(.running or (.phase | IN("downloading", "installing", "rolling-back"))) | not' <<<"$state" >/dev/null; then
      printf '%s\n' "$state"
      return
    fi
    sleep 0.4
  done
  echo "  the system update did not settle; the last state read: $state" >&2
  return 1
}
# update_logged ACTION...: the activity log's 系统 category has each action.
# shellcheck disable=SC2317,SC2329 # invoked through check (SC2317 in older shellcheck)
update_logged() {
  local actions action
  actions=$(curl -fsS -b "$update_jar" "$api/api/v1/activity?category=system" | jq -r '.items[].action') || return 1
  for action in "$@"; do
    grep -qx "$action" <<<"$actions" || { echo "no $action in: $actions" >>"$hidden"; return 1; }
  done
}
a_nas_version() { dpkg-query -W -f='${Version}' a-nas; }

check "members cannot read the system update" \
  test "$(status_of -b /tmp/alice-update.jar "$api/api/v1/system/update")" = 403
check "members cannot ask for an install" \
  test "$(status_of -b /tmp/alice-update.jar -H "X-CSRF-Token: $alice_update_csrf" -H 'Content-Type: application/json' \
    -d '{"action":"install"}' "$api/api/v1/system/update/actions")" = 403

check "an administrator asks for a check" test "$(update_ask check)" = 202
state=$(update_settled || true)
check "the check took the push into the local repository" test -s /var/local/a-nas-repository/dists/dev/InRelease
check "the check offers the next release, ready to install from the push" \
  test "$(jq -r '"\(.phase)|\(.candidate)|\(.current)|\(.downloadBytes)"' <<<"$state")" = "ready|$next|$version|0"
check "an administrator asks for the install" test "$(update_ask install)" = 202
check "a second request waits for the run in progress" test "$(update_ask check)" = 409
state=$(update_settled || true)
check "the update service installed the next release, which passed its check" \
  test "$(jq -r '"\(.phase)|\(.current)|\(.previous)|\(.records[0].kind)|\(.records[0].by)"' <<<"$state")" = \
  "up-to-date|$next|$version|updated|owner"
check "dpkg has the next release, with its database step" test "$(a_nas_version):$(probe_table)" = "$next:1:1"
check "a-nas-check passes after the update" quiet a-nas-check
check "an administrator goes back to the version before" test "$(update_ask revert)" = 202
state=$(update_settled || true)
check "the update service went back, and the database with it" \
  test "$(jq -r '"\(.phase)|\(.current)|\(.previous // "")|\(.records[0].kind)"' <<<"$state"):$(a_nas_version):$(probe_table)" = \
  "up-to-date|$version||reverted:$version:0:0"
check "a-nas-check passes after going back" quiet a-nas-check
update_ask check >/dev/null
state=$(update_settled || true)
check "a check does not offer the release left behind again" \
  test "$(jq -r '"\(.phase)|\(.candidate // "")"' <<<"$state")" = "up-to-date|"

# A release whose check fails: on the next release anas-site.service refuses
# to start, which its postinst lets pass and a-nas-check reports. The state
# starts anew, as on a device that never ran an update, since the release
# just left is not offered again.
rm /var/lib/a-nas-update/state.json
cat >/usr/local/sbin/system-test-refuse-next <<EOF
#!/bin/sh
[ "\$(dpkg-query -W -f='\${Version}' a-nas)" != "$next" ]
EOF
chmod 0755 /usr/local/sbin/system-test-refuse-next
install -d /etc/systemd/system/anas-site.service.d
printf '[Service]\nExecStartPre=+/usr/local/sbin/system-test-refuse-next\n' >/etc/systemd/system/anas-site.service.d/system-test.conf
systemctl daemon-reload
update_ask check >/dev/null
update_settled >/dev/null || true
check "an administrator installs a release whose check fails" test "$(update_ask install)" = 202
state=$(update_settled || true)
check "the update service rolled back when the check failed" \
  test "$(jq -r '"\(.phase)|\(.current)|\(.problem.code)|\(.records[0].kind)|\(.records[0].version)"' <<<"$state")" = \
  "rolled-back|$version|check|rolled-back|$next"
check "the rollback says which check failed" grep -q anas-site.service <<<"$(jq -r '.problem.detail' <<<"$state")"
check "dpkg is back on this release, and the database with it" test "$(a_nas_version):$(probe_table)" = "$version:0:0"
check "a-nas-check passes after the rollback" quiet a-nas-check
update_ask check >/dev/null
state=$(update_settled || true)
check "a check does not offer the release rolled back again" \
  test "$(jq -r '"\(.phase)|\(.candidate // "")"' <<<"$state")" = "up-to-date|"
rm -r /etc/systemd/system/anas-site.service.d /usr/local/sbin/system-test-refuse-next
systemctl daemon-reload

# An install whose run is killed while dpkg works, as a power cut stops it.
# The next run, here the one the timer starts after boot, finishes it or goes
# back; either way dpkg is whole and the check passes.
rm /var/lib/a-nas-update/state.json
update_ask check >/dev/null
update_settled >/dev/null || true
check "an administrator asks for an install that will be cut short" test "$(update_ask install)" = 202
caught=false
for _ in $(seq 1 1200); do
  if pgrep -x dpkg >/dev/null; then
    systemctl kill --signal=KILL anas-update.service
    caught=true
    break
  fi
  sleep 0.05
done
check "the test stopped the install while dpkg worked" test "$caught" = true
for _ in $(seq 1 100); do systemctl is-active --quiet anas-update.service || break; sleep 0.1; done
check "the run cut short left the install in progress" \
  test "$(jq -r .phase /var/lib/a-nas-update/state.json 2>/dev/null)" = installing
systemctl start --no-block anas-update.service
state=$(update_settled || true)
# shellcheck disable=SC2317,SC2329 # invoked through check (SC2317 in older shellcheck)
finished_or_back() {
  local got
  got=$(jq -r '"\(.phase)|\(.current)|\(.records[0].kind)"' <<<"$state")
  echo "settled as $got" >>"$hidden"
  [[ "$got" == "up-to-date|$next|updated" || "$got" == "rolled-back|$version|rolled-back" ]]
}
check "the next run finished the install or went back" finished_or_back
check "dpkg has nothing left half done" test -z "$(dpkg --audit 2>&1)"
check "a-nas-check passes after the run that finished the install" quiet a-nas-check
check "the activity log has the administrator's requests and the update's outcomes" \
  update_logged system.update.install system.update.revert system.update.result.updated \
  system.update.result.reverted system.update.result.rolled-back
check "members see none of it in their activity" \
  test "$(curl -fsS -b /tmp/alice-update.jar "$api/api/v1/activity?category=system" | jq '.items | length')" = 0
# The rest of the run is on this release, from the repository it began with.
umount "$staging"
if [[ "$(a_nas_version)" != "$version" ]]; then
  check "apt returns to this release after the update service's runs" reinstall update-end "$version"
fi

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
