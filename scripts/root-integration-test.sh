#!/usr/bin/env bash
# Runs the tests that need real root tools as root, in a disposable
# privileged container of the system test's Debian 13 image (#190):
# internal/hostops/linux's rootintegration tests (accounts, ACLs, the
# permission matrix and mirror conversion on loop-mounted Btrfs, Samba, the
# photo store, app and terminal identities), then internal/sharefiles' Btrfs
# capture and share ACL matrix (internal/sharefiles/testdata/btrfs.sh). The
# test binaries are built here, so the image needs no Go toolchain; the host
# needs the Btrfs module loaded.
#
# Usage: scripts/build-system-test-image.sh && scripts/root-integration-test.sh [IMAGE]
set -euo pipefail

image=${1:-anas-systemd:trixie}
repo=$(cd "$(dirname "$0")/.." && pwd)
tests=$(mktemp -d)
trap 'rm -rf "$tests"' EXIT
cd "$repo"
CGO_ENABLED=1 go test -c -tags rootintegration -o "$tests/hostops-linux.test" ./internal/hostops/linux
CGO_ENABLED=1 go test -c -o "$tests/sharefiles.test" ./internal/sharefiles
cp internal/sharefiles/testdata/btrfs.sh "$tests/"
# The File Broker runs the test binary again as each member.
chmod 0755 "$tests"
# A container each, as both make the same accounts. Without systemd as PID 1:
# the tests start their own smbd.
in_container() {
  docker run --rm --privileged --entrypoint /bin/bash -v "$tests:/tests:ro" -w /tests "$image" -euc "$1"
}
in_container 'ANAS_ROOT_INTEGRATION=1 ./hostops-linux.test -test.count=1'
in_container 'sh ./btrfs.sh'
