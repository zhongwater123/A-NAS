#!/usr/bin/env bash
# The VM half of scripts/image-test.sh, run in the image builder container:
# /test holds what its build step made (stage-SEQUENCE/ with the update files
# after each image, the test's SSH key), /work is scratch on the same drive.
# The VM's disks stay on the container's own disk and go with it.
set -uo pipefail

T=/test
VM=/var/tmp/a-nas-vm
SU=/usr/lib/systemd/systemd-sysupdate
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
reboot_vm() {
  g systemctl reboot || true
  wait "$vm" || true
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
manifest() { g 'find /etc/a-nas/image-test /var/lib/a-nas/image-test -type f -print0 | sort -z | xargs -0 sha256sum'; }
content_merged() { (($(g 'systemd-sysext list --no-legend' | grep -c a-nas-content-) >= 5)); }
content_defined() { g "$SU components" | grep -q content-model-detector; }
root_grew() { (($(g 'lsblk -bno SIZE /dev/disk/by-partlabel/a-nas-state') > 30 * 1024 ** 3)); }
not_taken_again() { g "$SU $* update" 2>&1 | grep -q 'No update needed'; }
# The image has Python, not curl.
service_answers() { g 'python3 -c "import urllib.request; urllib.request.urlopen(\"http://127.0.0.1:8080/healthz\", timeout=5)"' >/dev/null 2>&1; }
# checked NAME: a-nas-check passes; what it said is in the logs as check-NAME.log.
checked() { g a-nas-check >"$L/check-$1.log" 2>&1; }
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
# push STAGE DIR...: the update files a device would have seen after image
# STAGE was published, as a development machine's push leaves them in
# /var/lib/a-nas-updates: the stage's signed SHA256SUMS and its files.
push() {
  local stage=$T/stage-$1 dir files
  shift
  for dir; do
    mapfile -t files < <(grep "^$dir/" "$stage/files" | sed "s|^|$T/updates/|")
    g "rm -rf /var/lib/a-nas-updates/$dir && mkdir -p /var/lib/a-nas-updates/$dir"
    scp -q -i /small/ssh -P 2222 -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null \
      "$stage/$dir"/SHA256SUMS "$stage/$dir"/SHA256SUMS.gpg "${files[@]}" "root@127.0.0.1:/var/lib/a-nas-updates/$dir/"
  done
  # On the disk before anything else happens: the test cuts the power
  # moments later. A push that a power cut leaves half written is refused by
  # its checksum, and the device keeps its version until it is pushed again.
  g sync
}
# apply_product: what the update service does to switch the product: copy the
# databases with the running product's preinst, switch, then the new
# product's postinst and a-nas-check.
apply_product() {
  g 'set -e; old=$(. /usr/lib/extension-release.d/extension-release.a-nas-product && echo "$A_NAS_VERSION")
    /usr/lib/a-nas/release/preinst upgrade "$old"
    '"$1"'
    systemd-sysext refresh
    /usr/lib/a-nas/release/postinst configure "$old"' >"$L/apply-$2.log" 2>&1
}

cleanup() {
  [[ -z "$vm" ]] || { kill -9 "$vm" 2>/dev/null; wait "$vm" 2>/dev/null; }
  cp "$L"/* "$T/logs/" 2>/dev/null
  rm -rf -- "$VM"
}
mkdir -p "$T/logs"
trap cleanup EXIT

# --- 1001: the disk a new device gets, on a disk larger than the image.
zstd -q -d "$T/first-disk.raw.zst" -o "$VM/disk.raw"
truncate -s 40G "$VM/disk.raw"
cp /usr/share/OVMF/OVMF_VARS_4M.fd "$VM/vars.fd"
boot_until_up 1 || { note "FAIL 1001 boots"; exit 1; }
g "$SU components; systemd-sysext list --no-pager; lsblk -o NAME,PARTLABEL,SIZE; journalctl -b --no-pager -u systemd-repart -u systemd-sysext -u a-nas-sysext-ready | tail -n 80" >"$L/first-boot.log" 2>&1
check "1001 boots the base it was written with" test "$(booted)" = 1001
check "the product is merged" test -n "$(product)"
check "each kind of content is merged" content_merged
check "the product's update definitions name the content" content_defined
check "the root partition grew to the disk" root_grew
iface=$(g "ip -o -4 route show to default | awk '{print \$5; exit}'")
g "mkdir -p /etc/a-nas /etc/a-nas/image-test /var/lib/a-nas/image-test &&
  printf 'ANAS_SMB_INTERFACE=%s\n' '$iface' >/etc/a-nas/site.conf &&
  head -c 1M /dev/urandom >/etc/a-nas/image-test/state && head -c 8M /dev/urandom >/var/lib/a-nas/image-test/state &&
  a-nas-site" >"$L/site.log" 2>&1
for _ in $(seq 1 60); do service_answers && break; sleep 2; done
check "the Product Service answers" service_answers
check "a-nas-check passes on the new device" checked 1001
manifest >"$L/manifest-0"
boot_id=$(g cat /proc/sys/kernel/random/boot_id)

# --- 1002: a new product, applied without a restart, then left again.
push 1002 product os
for role in $(cd "$T/stage-1002" && ls -d content-* | sed 's/^content-//'); do push 1002 "content-$role"; done
check "unchanged content is not taken again" not_taken_again --component=content-model-detector
check "an unchanged base is not taken again" not_taken_again
first_product=$(product)
check "the next product installs" apply_product "$SU --component=product update" 1002
check "the next product runs, with its database step" test "$(product)/$(probe_table)" = "$first_product+next/1:1"
check "a-nas-check passes on the next product" checked 1002
check "the product changed without a restart" test "$(g cat /proc/sys/kernel/random/boot_id)" = "$boot_id"
check "the product goes back" apply_product \
  'ln -sfn /var/lib/a-nas-sysext/a-nas-product_1001.raw /var/lib/extensions/a-nas-product.raw' 1001-again
check "the product before runs, with its database back" test "$(product)/$(probe_table)" = "$first_product/0:0"
check "a-nas-check passes after going back" checked back

# --- 1003: a new base. The first try is cut while it is written.
push 1003 os
g "$SU update" >"$L/update-cut.log" 2>&1 &
sleep 3
power_cut
boot_until_up 3
check "a power cut while a base is written leaves the device on its version" test "$(booted)" = 1001
g "$SU update" >"$L/update-1003.log" 2>&1
note "ESP after writing 1003: $(esp)"
reboot_vm
boot_until_up 3
note "after 1003: booted $(booted); ESP: $(esp)"
g 'bootctl list --no-pager; systemctl status --no-pager a-nas-health.service systemd-bless-boot.service boot-complete.target; ls /sys/firmware/efi/efivars | grep -i loaderboot' >"$L/boot-1003.log" 2>&1
check "1003 boots and is blessed after its check" blessed 1003
check "the health check ran" test "$(g systemctl is-active a-nas-health.service)" = active
check "the product stays merged on the new base" test -n "$(product)"
check "state stays across the base update" cmp -s <(manifest) "$L/manifest-0"

# --- 1004: a base whose check fails; systemd-boot goes back after three tries.
push 1004 os
g "$SU update" >"$L/update-1004.log" 2>&1
reboot_vm
boot_until_up 6
note "after 1004: booted $(booted); ESP: $(esp); boots so far $boots"
check "a base that fails its check is left for the one before" test "$(booted)" = 1003
check "state stays after the fallback" cmp -s <(manifest) "$L/manifest-0"

g systemctl poweroff >/dev/null 2>&1 || true
wait "$vm" 2>/dev/null || true
vm=
note "failures=$failures"
((failures == 0))
