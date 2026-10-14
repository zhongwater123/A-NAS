#!/usr/bin/env bash
# Item 7: can a per-member assistant subvolume be capped by Btrfs quotas?
set -uo pipefail
echo "kernel $(uname -r); $(btrfs --version | head -1)"
truncate -s 2G /var/tmp/vol.img
dev=$(losetup -f --show /var/tmp/vol.img) || { echo "no loop device"; exit 1; }
mkfs.btrfs -q "$dev" && mkdir -p /mnt/vol && mount "$dev" /mnt/vol
if btrfs quota enable --simple /mnt/vol 2>/tmp/q.err; then mode=simple; else cat /tmp/q.err; btrfs quota enable /mnt/vol && mode=full; fi
echo "quota mode: $mode"
btrfs subvolume create /mnt/vol/alice >/dev/null
chown 20100:20100 /mnt/vol/alice
btrfs qgroup limit 64M /mnt/vol/alice
setpriv --reuid 20100 --regid 20100 --clear-groups dd if=/dev/urandom of=/mnt/vol/alice/big bs=1M count=100 status=none 2>&1 | tail -1
sync
echo "written: $(stat -c %s /mnt/vol/alice/big 2>/dev/null) bytes"
btrfs qgroup show -re /mnt/vol | tail -3
umount /mnt/vol; losetup -d "$dev"; rm -f /var/tmp/vol.img
