#!/bin/sh
# Run only inside a disposable privileged container; never format a host disk.
set -eu
test "$(id -u)" = 0
test -f /.dockerenv
base=$(mktemp -d /var/tmp/anas-share-btrfs.XXXXXX)
chmod 0755 "$base"
image="$base/data.img"
volume="$base/data"
mkdir "$volume"
truncate -s 2G "$image"
test -f "$image"
mkfs.btrfs --quiet "$image"
mount -o loop "$image" "$volume"
trap 'umount "$volume"' EXIT HUP INT TERM
chmod 1777 "$volume"
ANAS_SHARE_BTRFS_ROOT="$volume" ANAS_SHARE_TEST_GID=1001 \
 setpriv --reuid=1000 --regid=1000 --groups=1001 \
 /tests/sharefiles.test -test.v -test.run 'Test(BtrfsCapture|RemoteCapture)'

# Real Linux identities, only inside this disposable container.
for record in a-nas-users:20000 a-nas-admins:20001 alice:20100 bob:20101 a-nas-shares:31001; do
 name=${record%:*}
 gid=${record#*:}
 groupadd -g "$gid" "$name"
done
useradd -M -u 20100 -g 20100 -G 20000,20001 -s /usr/sbin/nologin alice
useradd -M -u 20101 -g 20101 -G 20000 -s /usr/sbin/nologin bob
useradd -M -u 31001 -g 31001 -s /usr/sbin/nologin a-nas-shares
ANAS_SHARE_BTRFS_ROOT="$volume" ANAS_SHARE_ACL=1 /tests/sharefiles.test -test.v -test.run TestShareACLMatrix
