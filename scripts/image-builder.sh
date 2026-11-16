# shellcheck shell=bash
# The Debian 13 tools that build and test the A-NAS device image (ADR 0034
# step 5): mkosi, systemd-repart, systemd-boot, systemd-sysupdate, erofs-utils
# and QEMU, from Debian 13 alone, in an image on top of the system test's
# (scripts/build-system-test-image.sh). Each command runs in a privileged
# container of its own: mkosi and QEMU need loop devices and /dev/kvm.
# scripts/build-image.sh and scripts/image-test.sh source this file; it
# defines functions only.

# What the builder installs; reprepro makes mkosi's local repository of the
# release's packages. The image's tag follows the list, so a change to it
# builds a new image.
image_builder_packages="mkosi reprepro systemd-boot systemd-boot-efi systemd-ukify systemd-repart
  systemd-container qemu-system-x86 ovmf erofs-utils dosfstools mtools gnupg openssl python3 jq xz-utils
  zstd cpio kmod debian-archive-keyring openssh-client e2fsprogs fdisk apt dpkg"
image_builder=anas-image-builder:trixie-$(printf '%s' "$image_builder_packages" | sha256sum | cut -c1-8)

# image_builder_start WORK: makes the builder image unless it is there, and
# WORK: a directory, or an ext4 image file of 40 GB when WORK ends in .img.
# On WSL, put it on a Windows drive, since WSL's own disk never shrinks; the
# Windows drive does not keep it sparse, so it takes its 40 GB until removed.
image_builder_start() {
  image_builder_work=$1
  if ! docker image inspect "$image_builder" >/dev/null 2>&1; then
    printf 'FROM anas-systemd:trixie\nRUN apt-get update -qq && DEBIAN_FRONTEND=noninteractive apt-get install -y -qq --no-install-recommends %s >/dev/null && rm -rf /var/cache/apt/archives/*.deb\n' \
      "$(tr -s ' \n' ' ' <<<"$image_builder_packages")" | docker build -q -t "$image_builder" - >/dev/null
  fi
  if [[ "$image_builder_work" == *.img ]]; then
    if [[ ! -f "$image_builder_work" ]]; then
      mkdir -p "$(dirname "$image_builder_work")"
      docker run --rm -v "$(dirname "$image_builder_work"):/store" "$image_builder" \
        bash -c 'truncate -s 40G "/store/$1" && mkfs.ext4 -q "/store/$1"' _ "$(basename "$image_builder_work")"
    fi
  else
    mkdir -p "$image_builder_work"
  fi
}

# image_builder_run [DOCKER-OPTION...] -- COMMAND...: COMMAND as root in a
# privileged builder container, with WORK at /work.
image_builder_run() {
  local options=()
  while [[ "$1" != -- ]]; do
    options+=("$1")
    shift
  done
  shift
  if [[ "$image_builder_work" == *.img ]]; then
    docker run --rm --privileged -v "$(dirname "$image_builder_work"):/store" "${options[@]}" "$image_builder" \
      bash -c 'mkdir -p /work && mount -o loop "/store/$1" /work && shift && "$@"; status=$?; umount /work; exit $status' \
      _ "$(basename "$image_builder_work")" "$@"
  else
    docker run --rm --privileged -v "$image_builder_work:/work" "${options[@]}" "$image_builder" "$@"
  fi
}
