#!/usr/bin/env bash
# The device image's test (ADR 0034 step 5, docs/runbooks/build-device-image.md):
# builds images of the release under test with scripts/build-image.sh from a
# signed repository, as the system test makes one, writes the first to a VM's
# disk and updates it the way a device is updated, with systemd-sysupdate
# reading the pushed update files:
#
#   1001  the release under test: the disk a new device gets
#   1002  the next release (a Product Service with one more database step):
#         a new product only, the base and the content unchanged
#   1003  a base that differs by a file: a new base, which needs a restart
#   1004  a base whose check fails: systemd-boot goes back to 1003
#
# It checks that the disk boots with the product and content merged and the
# root grown to the disk; that the product updates without a restart, its
# database step applied, and goes back; that a new base is blessed after its
# check and a broken one is left after three tries; that a power cut while a
# base is written leaves the device on its version; that unchanged content is
# not copied again; and that /etc, /var/lib/a-nas and the data volume stay.
#
# Usage: make packages && scripts/image-test.sh [STEP...]
# Steps: build vm clean (default: build vm). IMAGE_TEST_DIR (default
# build/image-test) holds the repository, the update files and, in build.img,
# mkosi's work: about 40 GB; on WSL put it on a Windows drive. Needs Docker,
# /dev/kvm and Go. Test keys only.
set -euo pipefail

repo_root=$(cd "$(dirname "$0")/.." && pwd)
T=${IMAGE_TEST_DIR:-$repo_root/build/image-test}
case "$T" in /?*) ;; *) echo "IMAGE_TEST_DIR must be an absolute path" >&2; exit 2 ;; esac
mkdir -p "$T"
T=$(cd "$T" && pwd)
[[ "$T" != / && "$T" != "$HOME" ]] || { echo "IMAGE_TEST_DIR must be a directory of its own" >&2; exit 2; }
# shellcheck source=image-builder.sh
. "$repo_root/scripts/image-builder.sh"
export IMAGE=${IMAGE:-anas-systemd:trixie}
export WORK=$T/build.img
if (($# == 0)); then set -- build vm; fi

step_build() {
  shopt -s nullglob
  local products=("$repo_root"/build/packages/a-nas_*.deb)
  ((${#products[@]} == 1)) || { echo "build/packages needs exactly one a-nas package: run make packages" >&2; return 2; }
  local version next
  version=$(dpkg-deb --field "${products[0]}" Version)
  next=$version+next
  # The key and the repository stay on this machine's own disk: GnuPG's agent
  # needs a socket, and aptly hard links, which a Windows drive may not offer.
  local small=$repo_root/build/image-test
  local repository=$small/repository gnupg=$small/gnupg
  rm -rf -- "$small" "$T/updates" "$T"/stage-* "$T/first-disk.raw.zst"
  mkdir -p "$small"
  bash "$repo_root/scripts/apt-repository.sh" key "$gnupg" >/dev/null
  # The next release: the same packages but a Product Service with one more
  # database step (internal/accounts/rollbacktest.go), as the system test's.
  install -d "$small/next-programs"
  cp "$repo_root"/build/{anas-host-agent,anas-container-agent,anas-scoring} "$small/next-programs/"
  (cd "$repo_root" && go build -tags rollbacktest -trimpath -ldflags "-X main.version=$next" \
    -o "$small/next-programs/anas-api" ./cmd/anas-api)
  bash "$repo_root/scripts/build-packages.sh" "$small/next" "$next" "$small/next-programs" >/dev/null
  for release in "$version" "$next"; do
    bash "$repo_root/scripts/apt-repository.sh" keyring "$small/keyring" "$release" "$gnupg" >/dev/null
  done
  local content=$repo_root/build/stand-in-content upstream
  # The stand-ins come with the real Qdrant and llama.cpp, as the system test's.
  bash "$repo_root/scripts/fetch-qdrant.sh" >/dev/null
  bash "$repo_root/scripts/fetch-llama.sh" >/dev/null
  bash "$repo_root/scripts/build-content-packages.sh" --stand-in "$content" "$repo_root/build" >/dev/null
  upstream=$(bash "$repo_root/scripts/fetch-upstream-packages.sh")
  mapfile -t upstream <<<"$upstream"
  bash "$repo_root/scripts/apt-repository.sh" publish "$repository" dev "$gnupg" \
    "$repo_root"/build/packages/a-nas{,-host,-console,-docker-policy}_*.deb \
    "$small"/next/a-nas{,-host,-console,-docker-policy}_*.deb \
    "$content"/*.deb "$small"/keyring/*.deb "${upstream[@]}" >/dev/null

  # The test's own additions to the base: SSH for the test, and what makes
  # 1003 and 1004 differ. On this disk too: a key or authorized_keys on a
  # Windows drive shows every mode, which ssh and sshd refuse.
  local extra=$small/extra
  ssh-keygen -q -t ed25519 -N '' -f "$small/ssh"
  install -d -m 0700 "$extra/base/root" "$extra/base/root/.ssh"
  install -m 0600 "$small/ssh.pub" "$extra/base/root/.ssh/authorized_keys"
  cp -a "$extra/base" "$extra/1003"
  install -D -m 0644 /dev/null "$extra/1003/usr/lib/a-nas/image-test-1003"
  cp -a "$extra/1003" "$extra/1004"
  # 1004's check fails: anas-site.service, which a-nas-check reports, refuses to start.
  install -d "$extra/1004/usr/lib/systemd/system/anas-site.service.d"
  printf '[Service]\nExecStartPre=/bin/false\n' >"$extra/1004/usr/lib/systemd/system/anas-site.service.d/image-test.conf"

  # build SEQUENCE VERSION EXTRA: one image, then what a device would have
  # seen after it: each directory's signed SHA256SUMS and the files there.
  # Nothing this test publishes is pruned, so the files stay in updates/.
  build() {
    ANAS_IMAGE_EXTRA=$extra/$3 bash "$repo_root/scripts/build-image.sh" "$repository" "$2" "$1" "$T/updates" "$gnupg"
    local stage=$T/stage-$1 dir
    for dir in "$T"/updates/os "$T"/updates/product "$T"/updates/content-*; do
      [[ -d "$dir" ]] || continue
      mkdir -p "$stage/$(basename "$dir")"
      cp "$dir"/SHA256SUMS "$dir"/SHA256SUMS.gpg "$stage/$(basename "$dir")/"
    done
    (cd "$T/updates" && find . -type f ! -name 'SHA256SUMS*' -printf '%P\n' | LC_ALL=C sort) >"$stage/files"
    # install/ keeps the latest disk alone; the VM starts from the first.
    if [[ $1 == 1001 ]]; then mv "$T/updates/install/a-nas_1001.raw.zst" "$T/first-disk.raw.zst"; fi
  }
  build 1001 "$version" base
  build 1002 "$next" base
  build 1003 "$next" 1003
  build 1004 "$next" 1004
}

step_vm() {
  image_builder_start "$WORK"
  image_builder_run -v "$T:/test" -v "$repo_root/build/image-test:/small:ro" \
    -v "$repo_root/scripts/image-test-vm.sh:/image-test-vm.sh:ro" --device /dev/kvm -- bash /image-test-vm.sh
}

step_clean() {
  rm -rf -- "$T" "$repo_root/build/image-test"
  docker rmi "$image_builder" >/dev/null 2>&1 || true
}

for step; do
  case "$step" in
    build) image_builder_start "$WORK" && step_build ;;
    vm) step_vm ;;
    clean) step_clean ;;
    *) echo "image-test.sh: unknown step $step" >&2; exit 2 ;;
  esac
done
