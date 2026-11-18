#!/usr/bin/env bash
# The VM half of scripts/image-test.sh, run in the image builder container:
# /test holds what its build step made (updates/x86-64/ with every update
# file, stage-SEQUENCE/ with each directory's signed SHA256SUMS after that
# image and the list of files there), /small the first disk and the test's
# SSH key, and /work is scratch on the same drive as /test. The VM's disks
# stay on the container's own disk and go with it.
set -uo pipefail

T=/test
U=$T/updates/x86-64
VM=/var/tmp/a-nas-vm
# The device's ANAS_UPDATE_STAGING, where a push leaves the update files.
STAGING=/var/tmp/a-nas-push
mkdir -p "$VM" /work/logs
L=/work/logs
: >"$L/results.log"
failures=0
note() { printf '%s %s\n' "$(date +%T)" "$*" | tee -a "$L/results.log"; }
check() { # check NAME COMMAND...
  local name=$1
  shift
  if "$@"; then note "PASS $name"; else
    note "FAIL $name"
    failures=$((failures + 1))
  fi
}

g() {
  ssh -q -i /small/ssh -p 2222 -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null \
    -o ConnectTimeout=5 -o ServerAliveInterval=5 root@127.0.0.1 "$@"
}
boots=0 vm=
boot() { # starts the VM; 0 once SSH answers and the boot settled, 1 if it went down first
  boots=$((boots + 1))
  qemu-system-x86_64 -machine q35,accel=kvm -cpu host -smp 2 -m 3072 -no-reboot -nodefaults \
    -display none -serial "file:$L/boot-$boots.log" \
    -drive if=pflash,format=raw,unit=0,readonly=on,file=/usr/share/OVMF/OVMF_CODE_4M.fd \
    -drive if=pflash,format=raw,unit=1,file="$VM/vars.fd" \
    -drive if=virtio,format=raw,file="$VM/disk.raw" \
    -netdev user,id=n0,hostfwd=tcp:127.0.0.1:2222-:22 -device virtio-net-pci,netdev=n0 \
    -device virtio-rng-pci -device i6300esb &
  vm=$!
  local deadline=$((SECONDS + 420))
  while ((SECONDS < deadline)); do
    if ! kill -0 "$vm" 2>/dev/null; then
      wait "$vm" || true
      return 1
    fi
    if g true 2>/dev/null; then
      # A failing health check reboots after SSH answers.
      g 'systemctl is-system-running --wait' >/dev/null 2>&1 || true
      sleep 5
      if kill -0 "$vm" 2>/dev/null; then return 0; fi
      wait "$vm" || true
      return 1
    fi
    sleep 2
  done
  note "boot $boots: no SSH after 420 s"
  kill -9 "$vm"
  wait "$vm" || true
  return 1
}
boot_until_up() { # boot_until_up MAX
  local n
  for ((n = 0; n < $1; n++)); do
    if boot; then return 0; fi
  done
  return 1
}
# restarted MAX: the update service restarted the VM, as it does into a new
# base: QEMU, started with -no-reboot, exits, and starts again. A VM still
# up after five minutes did not restart: a failure, and the power goes.
restarted() {
  local deadline=$((SECONDS + 300))
  while kill -0 "$vm" 2>/dev/null && ((SECONDS < deadline)); do sleep 2; done
  if kill -0 "$vm" 2>/dev/null; then
    note "FAIL the device restarts when the update service restarts it"
    failures=$((failures + 1))
    power_cut
  else
    wait "$vm" || true
  fi
  boot_until_up "$1"
}
power_cut() {
  kill -9 "$vm"
  wait "$vm" || true
}
# shellcheck disable=SC2016 # the guest expands these
booted() { g '. /etc/os-release && echo "$IMAGE_VERSION"'; }
# shellcheck disable=SC2016
product() { g '. /usr/lib/extension-release.d/extension-release.a-nas-product && echo "$A_NAS_VERSION"'; }
esp() { g 'ls -A /efi/EFI/Linux | tr "\n" " "'; }
extensions() { g 'ls -A /var/lib/extensions | tr "\n" " "'; }
manifest() { g 'find /etc/a-nas/image-test /var/lib/a-nas/image-test -type f -print0 | sort -z | xargs -0 sha256sum'; }
# content_merged: each kind of content the first image holds has its release
# file in the merged /usr.
kinds=$(find "$T/stage-1001" -mindepth 1 -maxdepth 1 -name 'content-*' | wc -l)
content_merged() {
  ((kinds > 0)) &&
    (($(g 'ls /usr/lib/extension-release.d' | sed -n 's/^extension-release\.a-nas-content-\(.*\)_[0-9]*$/\1/p' | sort -u | wc -l) == kinds))
}
# starts BASE PRODUCT: the update service links base BASE to product PRODUCT,
# which a-nas-product-select.service merges at that base's boots.
starts() { [[ "$(g readlink "/var/lib/a-nas-image/product/$1")" == "/var/lib/a-nas-sysext/a-nas-product_$2.raw" ]]; }
root_grew() { (($(g 'df -B1 --output=size / | tail -n 1') > 30 * 1024 ** 3)); }
# shellcheck disable=SC2016 # the guest expands these
same_host() {
  g '. /usr/lib/os-release && host=$A_NAS_HOST_ID && . /usr/lib/extension-release.d/extension-release.a-nas-product &&
    test -n "$host" && test "$host" = "$A_NAS_HOST_ID"'
}
# no_ordering_cycle: systemd dropped no job of this boot to break a cycle.
no_ordering_cycle() { [[ "$(g 'journalctl -b --no-pager -o cat')" != *'ordering cycle'* ]]; }
# on_the_disk COMMAND...: COMMAND, run while the persistent root of the disk a
# new device gets is mounted at /mnt/first-root.
on_the_disk() {
  local offset status
  offset=$(sfdisk --json "$VM/disk.raw" |
    jq -r '.partitiontable as $t | $t.partitions[] | select(.name == "a-nas-state") | .start * ($t.sectorsize // 512)')
  mkdir -p /mnt/first-root
  mount -o ro,loop,offset="$offset" "$VM/disk.raw" /mnt/first-root || return 1
  "$@"
  status=$?
  umount /mnt/first-root
  return "$status"
}
# no_host_keys: the disk holds no SSH host keys, which every device would share.
no_host_keys() { test -d /mnt/first-root/etc/ssh && ! compgen -G '/mnt/first-root/etc/ssh/ssh_host_*' >/dev/null; }
# no_credential_key: nor systemd's credential key, which encrypts the AI
# assistant's model key (ADR 0024) and which anas-site.service makes on the
# device itself.
no_credential_key() { test ! -e /mnt/first-root/var/lib/systemd/credential.secret; }
# The administrator's model key for the AI assistant, made up for this test.
test_key=sk-$(od -An -tx1 -N16 /dev/urandom | tr -d ' \n')
credential=/etc/credstore.encrypted/a-nas-assistant-deepseek
# save_key: the key as the Product Service hands it to the Host Agent once an
# administrator saved it in 设置 → VNAS (PUT /v1/assistant-key); the answer is
# in the logs.
save_key() {
  g python3 - >"$L/key-save.log" 2>&1 <<PY
import http.client, json, socket
class Agent(http.client.HTTPConnection):
    def connect(self):
        self.sock = socket.socket(socket.AF_UNIX)
        self.sock.connect("/run/a-nas/host-agent.sock")
agent = Agent("host-agent", timeout=120)
agent.request("PUT", "/v1/assistant-key", json.dumps({"key": "$test_key"}), {"Content-Type": "application/json"})
answer = agent.getresponse()
body = answer.read().decode()
print(answer.status, body)
raise SystemExit(0 if answer.status == 200 and json.loads(body).get("configured") else 1)
PY
}
# key_kept: the saved key is encrypted where only root reads it.
key_kept() {
  [[ "$(g stat -c "'%a %U'" "$credential")" == "600 root" ]] && g "test -s $credential && ! grep -qF '$test_key' $credential"
}
# key_works NAME: the model gateway starts afresh with the saved key, which
# systemd decrypts with the device's credential key; its journal is in the
# logs as key-NAME.log when it does not.
# shellcheck disable=SC2016 # the guest expands these
key_works() {
  g 'systemctl restart anas-assistant-gateway.service &&
    for _ in $(seq 1 50); do test -S /run/anas-assistant-gw/model.sock && exit 0; sleep 0.2; done; exit 1' && return 0
  g 'systemctl status --no-pager anas-assistant-gateway.service; journalctl -b --no-pager -u anas-assistant-gateway -u anas-site | tail -n 60' \
    >"$L/key-$1.log" 2>&1
  return 1
}
credential_key() { g sha256sum /var/lib/systemd/credential.secret; }
# The image has Python, not curl.
service_answers() { g 'python3 -c "import urllib.request; urllib.request.urlopen(\"http://127.0.0.1:8080/healthz\", timeout=5)"' >/dev/null 2>&1; }
# checked NAME: a-nas-check passes; what it said is in the logs as
# check-NAME.log, followed by the services' journals when it fails.
checked() {
  g a-nas-check >"$L/check-$1.log" 2>&1 && return 0
  g 'systemctl status --no-pager anas-api.service anas-host-agent.service; journalctl -b --no-pager -u anas-api -u anas-host-agent | tail -n 150' \
    >>"$L/check-$1.log" 2>&1
  return 1
}
# blessed SEQUENCE: booted from it, and its UKI lost the try counter.
blessed() { [[ "$(booted)" == "$1" && "$(esp)" == *"a-nas_$1.efi"* && "$(esp)" != *"a-nas_$1+"* ]]; }
probe_table() {
  g python3 - <<'PY'
import sqlite3
db = sqlite3.connect("file:/var/lib/a-nas/control.db?mode=ro", uri=True)
version = db.execute("PRAGMA user_version").fetchone()[0]
probe = db.execute("SELECT COUNT(*) FROM sqlite_master WHERE name = 'rollback_probe'").fetchone()[0]
print(f"{version}:{probe}")
PY
}
# push STAGE: what a development machine's push after image STAGE leaves in
# the device's staging directory: each directory's signed SHA256SUMS and its
# files, with their times, as rsync keeps them.
push() {
  local stage=$T/stage-$1 dir files try
  g "rm -rf $STAGING && mkdir -p $STAGING"
  for dir in "$stage"/*/; do
    dir=$(basename "$dir")
    mapfile -t files < <(grep "^$dir/" "$stage/files" | sed "s|^|$U/|")
    # Reading from a Windows drive may fail now and then: up to three tries.
    for try in 1 2 3; do
      g "rm -rf $STAGING/$dir && mkdir -p $STAGING/$dir"
      scp -q -p -i /small/ssh -P 2222 -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null \
        "$stage/$dir"/SHA256SUMS "$stage/$dir"/SHA256SUMS.gpg "${files[@]}" "root@127.0.0.1:$STAGING/$dir/" && break
      note "push ${stage##*-} $dir: try $try failed"
    done
  done
  # On the disk before anything else happens: the test cuts the power
  # moments later.
  g sync
}
# ask KIND [BY]: a request as the Host Agent leaves it in the update
# service's state directory; the service takes it, and ask returns once the
# run ended. A run the timer started meanwhile leaves it for the next.
ask() {
  g "install -d -m 0700 /var/lib/a-nas-update &&
    printf '{\"kind\":\"%s\",\"by\":\"%s\"}\n' '$1' '${2:-image-test}' >/var/lib/a-nas-update/request.json &&
    for _ in 1 2 3; do systemctl start anas-update.service; [ -e /var/lib/a-nas-update/request.json ] || break; done" \
    >>"$L/ask.log" 2>&1
}
# state PATH: a value from the update service's state, such as phase,
# image.restart or records.0.kind; "" for one it does not have.
state() {
  g python3 - "$1" <<'PY'
import json, sys
value = json.load(open("/var/lib/a-nas-update/state.json"))
for key in sys.argv[1].split("."):
    if isinstance(value, list):
        value = value[int(key)] if int(key) < len(value) else ""
    elif isinstance(value, dict):
        value = value.get(key, "")
print(value if isinstance(value, str) else json.dumps(value))
PY
}
# keep NAME: the update service's state and journal, in the logs.
keep() {
  g 'cat /var/lib/a-nas-update/state.json; journalctl -b --no-pager -u anas-update -u a-nas-product-select | tail -n 60' \
    >"$L/update-$1.log" 2>&1
}

cleanup() {
  [[ -z "$vm" ]] || { kill -9 "$vm" 2>/dev/null; wait "$vm" 2>/dev/null; }
  cp "$L"/* "$T/logs/" 2>/dev/null
  [[ -z "${ANAS_OWNER:-}" ]] || chown -R "$ANAS_OWNER" "$T/logs"
  rm -rf -- "$VM"
}
mkdir -p "$T/logs"
trap cleanup EXIT

# --- 1001: the disk a new device gets, on a disk larger than the image.
zstd -q -d /small/first-disk.raw.zst -o "$VM/disk.raw" || { note "FAIL the first disk decompresses"; exit 1; }
truncate -s 40G "$VM/disk.raw"
cp /usr/share/OVMF/OVMF_VARS_4M.fd "$VM/vars.fd"
check "the disk holds no SSH host keys" on_the_disk no_host_keys
check "the disk holds no credential key, which every device would share" on_the_disk no_credential_key
boot_until_up 1 || { note "FAIL 1001 boots"; exit 1; }
g "systemd-sysext status --no-pager; lsblk -o NAME,PARTLABEL,SIZE; df -h /; journalctl -b --no-pager -u systemd-repart -u systemd-growfs-root -u a-nas-product-select -u systemd-sysext -u a-nas-etc -u a-nas-sysext-ready -u a-nas-health-baseline -u sshd-keygen | tail -n 120" >"$L/first-boot.log" 2>&1
check "1001 boots the base it was written with" test "$(booted)" = 1001
check "the boot has no ordering cycle" no_ordering_cycle
check "the product is merged" test -n "$(product)"
check "the base is the one the product needs" same_host
check "each kind of content is merged" content_merged
check "the base starts the product it was written with" starts 1001 1001
check "the root file system grew to the disk" root_grew
check "the device made its own SSH host keys" g test -s /etc/ssh/ssh_host_ed25519_key
iface=$(g "ip -o -4 route show to default | awk '{print \$5; exit}'")
g "mkdir -p /etc/a-nas /etc/a-nas/image-test /var/lib/a-nas/image-test &&
  printf 'ANAS_SMB_INTERFACE=%s\nANAS_UPDATE_STAGING=%s\n' '$iface' '$STAGING' >/etc/a-nas/site.conf &&
  head -c 1M /dev/urandom >/etc/a-nas/image-test/state && head -c 8M /dev/urandom >/var/lib/a-nas/image-test/state &&
  a-nas-site" >"$L/site.log" 2>&1
for _ in $(seq 1 60); do service_answers && break; sleep 2; done
check "the Product Service answers" service_answers
check "a-nas-check passes on the new device" checked 1001
# The AI assistant's key (ADR 0024): the device made its own credential key, whose
# product is a system extension that no package script set up, and the Host Agent
# saves the administrator's key with it; the model gateway reads it.
check "the device made its own credential key" g test -s /var/lib/systemd/credential.secret
check "the Host Agent saves the assistant's model key" save_key
check "the key is encrypted where only root reads it" key_kept
check "the model gateway starts with the key" key_works 1001
check "a-nas-check passes with the key saved" checked 1001-key
key_before=$(credential_key)
manifest >"$L/manifest-0"
boot_id=$(g cat /proc/sys/kernel/random/boot_id)
first=$(product)
# This release's own database steps, which the next release's step follows.
steps=$(probe_table)
steps=${steps%%:*}

# --- 1002: a new product, without a restart, then back to the one before.
push 1002
esp_before=$(esp) extensions_before=$(extensions)
ask check
keep 1002-check
check "the update service offers the next product, with no restart" \
  test "$(state phase)/$(state candidate)/$(state image.restart)" = "ready/$first+next/"
ask install
keep 1002-install
check "the next product installs" test "$(state phase)/$(state current)" = "up-to-date/$first+next"
check "the next product runs, with its database step" test "$(product)/$(probe_table)" = "$first+next/$((steps + 1)):1"
check "a-nas-check passes on the next product" checked 1002
check "the next product's gateway starts with the key saved before" key_works 1002
check "the product changed without a restart" test "$(g cat /proc/sys/kernel/random/boot_id)" = "$boot_id"
check "unchanged content and an unchanged base are not written again" \
  test "$(esp)/$(extensions)" = "$esp_before/$extensions_before"
check "the base now starts the next product" starts 1001 1002
ask revert
keep 1002-revert
check "the product goes back" test "$(state phase)/$(state records.0.kind)" = "up-to-date/reverted"
check "the product before runs, with its database back" test "$(product)/$(probe_table)" = "$first/$steps:0"
check "a-nas-check passes after going back" checked back

# --- 1003: a new base. The first install is cut while the base is written.
# Before it, the device lacks an account that a package script made, and has
# removed an /etc file that the image put there.
account=$(g "awk '\$1 == \"u\" { print \$2; exit }' /usr/lib/sysusers.d/a-nas-image-accounts.conf")
account_id=
if [[ -n "$account" ]]; then
  account_id=$(g "id -u $account")
  g "sed -i '/^$account:/d' /etc/passwd /etc/shadow"
fi
g rm /etc/a-nas-image-test-factory/removed-by-the-device
# A check that already fails does not hold the new base back: a-nas-kiosk
# gets a shell, which a-nas-check reports; the install records the baseline.
g "sed -i 's|^\(a-nas-kiosk:.*:\)/usr/sbin/nologin\$|\1/bin/sh|' /etc/passwd"
push 1003
ask check
check "a new base is offered, with a restart" test "$(state phase)/$(state image.restart)" = "ready/true"
ask install >/dev/null 2>&1 &
# The power goes while the base's /usr partition is written: systemd-sysupdate
# has systemd-pull unpack it at the partition's offset. (pgrep -x cannot find
# systemd-sysupdate itself: a process name keeps 15 characters. The brackets
# keep the pattern from matching the shell that runs pgrep.)
written=
for _ in $(seq 1 360); do
  if g "pgrep -f 'systemd-pull raw .*-[-]offset'" >/dev/null 2>&1; then
    written=1
    break
  fi
  sleep 0.5
done
[[ -n "$written" ]] || note "the base's write was not seen; the power goes now"
sleep 1
power_cut
boot_until_up 3
check "a power cut while a base is written leaves the device on its version" test "$(booted)" = 1001
if [[ -n "$account" ]]; then
  check "an account that a package script made comes back at the next boot ($account)" \
    test "$(g "id -u $account")" = "$account_id"
else
  note "the image holds no account that a package script made"
fi
ask check
keep 1003-cut
check "the install the power cut stopped is offered again" test "$(state phase)/$(state records.0.kind)" = "ready/failed"
ask install
keep 1003-install
check "the new base waits for a restart" test "$(state phase)/$(state target)" = "restart-pending/$first+next2"
check "the product that runs is unchanged until the restart" test "$(product)" = "$first"
check "the new base starts its own product" starts 1003 1003
check "the base before starts its own product" starts 1001 1001
ask restart
restarted 3
ask tick
keep 1003-restart
note "after 1003: booted $(booted); ESP: $(esp)"
g 'bootctl list --no-pager; systemctl status --no-pager a-nas-health.service a-nas-etc.service systemd-bless-boot.service boot-complete.target; journalctl -b --no-pager -u a-nas-health -u a-nas-etc; ls /sys/firmware/efi/efivars | grep -i loaderboot' >"$L/boot-1003.log" 2>&1
check "1003 boots and is blessed after its check, which failed before it too" blessed 1003
check "the health check ran" test "$(g systemctl is-active a-nas-health.service)" = active
check "the update service records the new base as installed" \
  test "$(state phase)/$(state current)/$(state image.base)" = "up-to-date/$first+next2/1003"
check "the new base runs its own product" test "$(product)" = "$first+next2"
check "the new base's boot has no ordering cycle" no_ordering_cycle
check "an /etc file the new base adds reaches the device" g test -e /etc/a-nas-image-test-factory/added-in-1003
check "an /etc file the device removed stays removed" g test ! -e /etc/a-nas-image-test-factory/removed-by-the-device
check "state stays across the base update" cmp -s <(manifest) "$L/manifest-0"
check "the credential key stays across the base update" test "$(credential_key)" = "$key_before"
check "the new base's gateway starts with the key saved before" key_works 1003
g "sed -i 's|^\(a-nas-kiosk:.*:\)/bin/sh\$|\1/usr/sbin/nologin|' /etc/passwd"

# --- Back to the version before, on the base before: a restart.
ask revert
keep 1003-revert
check "going back to the base before waits for a restart" test "$(state phase)/$(state target)" = "restart-pending/$first"
ask restart
restarted 3
ask tick
keep 1003-back
check "the device is back on the base and product before" \
  test "$(booted)/$(product)/$(state phase)/$(state records.0.kind)" = "1001/$first/up-to-date/reverted"
check "the product before runs with its database back" test "$(probe_table)" = "$steps:0"
check "a-nas-check passes after going back to the base before" checked back-base
check "the base before still starts the gateway with the key" key_works back-base

# --- 1004: a base whose check fails, installed as in the install window: the
# device restarts at once, and systemd-boot goes back after three tries.
push 1004
ask check
check "1004 is offered" test "$(state phase)/$(state candidate)" = "ready/$first+next3"
ask install automatic
restarted 6
note "after 1004: booted $(booted); ESP: $(esp); boots so far $boots"
ask tick
keep 1004
check "a base that fails its check is left for the one before" test "$(booted)" = 1001
check "the base before starts its own product" test "$(product)/$(probe_table)" = "$first/$steps:0"
check "the update service says the new base did not pass its boot check" \
  test "$(state phase)/$(state problem.code)/$(state records.0.kind)" = "rolled-back/boot/rolled-back"
check "a-nas-check passes after the fallback" checked fallback
check "state stays after the fallback" cmp -s <(manifest) "$L/manifest-0"
check "the credential key stays after the fallback" test "$(credential_key)" = "$key_before"
check "the gateway starts with the key after the fallback" key_works fallback

g systemctl poweroff >/dev/null 2>&1 || true
wait "$vm" 2>/dev/null || true
vm=
note "failures=$failures"
((failures == 0))
