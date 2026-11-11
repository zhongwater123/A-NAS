#!/usr/bin/env bash
# The A-NAS apt repository (ADR 0034): a directory that aptly keeps and
# signs, which a deploy machine copies to a NAS or a web server as it is. apt
# checks every file it takes from it against the signed Release file, so
# whoever copies it is not trusted with the release.
#
#   apt-repository.sh key GNUPGHOME
#       makes a test signing key in GNUPGHOME unless one is there, and prints
#       its fingerprint. A test key is for development and test machines; the
#       release key and where it is kept are the owner's decision
#       (docs/decisions/2026-11-11-release-signing-key.md).
#   apt-repository.sh fingerprint GNUPGHOME
#       prints the fingerprint of GNUPGHOME's signing key.
#   apt-repository.sh keyring OUT VERSION GNUPGHOME
#       builds a-nas-archive-keyring at VERSION into OUT: the public key of
#       GNUPGHOME's signing key, which the repository's .sources file names in
#       Signed-By.
#   apt-repository.sh publish REPO DIST GNUPGHOME DEB...
#       adds the packages to the distribution DIST (release or dev) of the
#       repository in REPO, made on first use, and signs it with GNUPGHOME's
#       key. Every version stays, so apt can roll back to an older one. A
#       package that is there already at the same version is kept if it is
#       the same file and refused if it is not. aptly's own state is in
#       .REPO.aptly beside REPO.
#
# GnuPG and aptly run in IMAGE, the Debian 13 image
# scripts/build-system-test-image.sh makes (anas-systemd:trixie by default),
# as the calling user, so nothing needs installing here.
set -euo pipefail

image=${IMAGE:-anas-systemd:trixie}
repo_root=$(cd "$(dirname "$0")/.." && pwd)

# in_image MOUNT... -- COMMAND...: COMMAND in IMAGE with the mounts, as this user.
in_image() {
  local mounts=()
  while [[ "$1" != -- ]]; do
    mounts+=(-v "$1")
    shift
  done
  shift
  docker run --rm --user "$(id -u):$(id -g)" -e HOME=/tmp -e GNUPGHOME=/gnupg "${mounts[@]}" "$image" "$@"
}
# fingerprint GNUPGHOME: the signing key's fingerprint; fails without one.
fingerprint() {
  in_image "$1:/gnupg" -- gpg --batch --with-colons --list-secret-keys |
    awk -F: '$1 == "fpr" { print $10; exit }' | grep .
}
absolute() { mkdir -p "$1" && (cd "$1" && pwd); }

case "${1:-}" in
  key)
    [[ $# -eq 2 ]] || { echo "usage: $0 key GNUPGHOME" >&2; exit 2; }
    gnupg=$(absolute "$2")
    chmod 0700 "$gnupg"
    if ! fingerprint "$gnupg" 2>/dev/null; then
      in_image "$gnupg:/gnupg" -- gpg --batch --quiet --pinentry-mode loopback --passphrase '' \
        --quick-generate-key 'A-NAS test key, not for releases <a-nas-test-key@invalid>' ed25519 sign never
      fingerprint "$gnupg"
    fi
    ;;
  fingerprint)
    [[ $# -eq 2 ]] || { echo "usage: $0 fingerprint GNUPGHOME" >&2; exit 2; }
    fingerprint "$(absolute "$2")"
    ;;
  keyring)
    [[ $# -eq 4 ]] || { echo "usage: $0 keyring OUT VERSION GNUPGHOME" >&2; exit 2; }
    out=$(absolute "$2")
    version=$3
    gnupg=$(absolute "$4")
    work=$(mktemp -d)
    trap 'rm -rf -- "$work"' EXIT
    root=$work/a-nas-archive-keyring
    install -d -m 0755 "$root/DEBIAN" "$root/usr/share/keyrings" "$root/usr/share/doc/a-nas-archive-keyring"
    sed "s/@VERSION@/$version/g" "$repo_root/deploy/debian/a-nas-archive-keyring/DEBIAN/control" >"$root/DEBIAN/control"
    in_image "$gnupg:/gnupg" -- gpg --batch --export "$(fingerprint "$gnupg")" \
      >"$root/usr/share/keyrings/a-nas-archive-keyring.pgp"
    [[ -s "$root/usr/share/keyrings/a-nas-archive-keyring.pgp" ]] || { echo "no public key in $gnupg" >&2; exit 2; }
    cp "$repo_root/deploy/apt/a-nas.sources" "$root/usr/share/doc/a-nas-archive-keyring/"
    printf 'The public key of the A-NAS repository; see https://github.com/zhongwater123/A-NAS\n' \
      >"$root/usr/share/doc/a-nas-archive-keyring/copyright"
    chmod 0644 "$root/DEBIAN/control" "$root/usr/share/keyrings/a-nas-archive-keyring.pgp" \
      "$root/usr/share/doc/a-nas-archive-keyring"/*
    SOURCE_DATE_EPOCH=${SOURCE_DATE_EPOCH:-$(git -C "$repo_root" log -1 --format=%ct 2>/dev/null || echo 0)} \
      dpkg-deb --root-owner-group -Zxz --build "$root" "$out/a-nas-archive-keyring_${version}_all.deb" >/dev/null
    echo "a-nas-archive-keyring $version"
    ;;
  publish)
    [[ $# -ge 5 ]] || { echo "usage: $0 publish REPO DIST GNUPGHOME DEB..." >&2; exit 2; }
    repo=$(absolute "$2")
    dist=$3
    gnupg=$(absolute "$4")
    shift 4
    [[ "$dist" == release || "$dist" == dev ]] || { echo "DIST is release or dev, not $dist" >&2; exit 2; }
    key=$(fingerprint "$gnupg") || { echo "no signing key in $gnupg (apt-repository.sh key)" >&2; exit 2; }
    # aptly keeps its database and package pool beside the repository, in
    # .NAME.aptly, and links the published files to them: the directory to
    # copy holds only dists/ and pool/. Both are under one mount, so the links
    # are hard links.
    base=$(dirname "$repo")
    name=$(basename "$repo")
    state=.$name.aptly
    install -d "$base/$state"
    printf '{"rootDir": "/base/%s", "architectures": ["amd64"], "gpgProvider": "gpg",
 "FileSystemPublishEndpoints": {"repository": {"rootDir": "/base/%s", "linkMethod": "hardlink", "verifyMethod": "md5"}}}\n' \
      "$state" "$name" >"$base/$state/aptly.conf"
    # The packages, one mount for their directory each.
    mounts=() debs=() index=0
    declare -A seen=()
    for deb in "$@"; do
      directory=$(cd "$(dirname "$deb")" && pwd)
      if [[ -z "${seen[$directory]:-}" ]]; then
        seen[$directory]=/debs/$index
        mounts+=("$directory:/debs/$index:ro")
        index=$((index + 1))
      fi
      debs+=("${seen[$directory]}/$(basename "$deb")")
    done
    # shellcheck disable=SC2016 # the script expands its variables in the image
    in_image "$gnupg:/gnupg" "$base:/base" "${mounts[@]}" -- env APTLY_CONFIG="/base/$state/aptly.conf" bash -c '
      set -euo pipefail
      dist=$1 key=$2
      shift 2
      aptly=(aptly -config="$APTLY_CONFIG")
      "${aptly[@]}" repo show "$dist" >/dev/null 2>&1 ||
        "${aptly[@]}" repo create -distribution="$dist" -component=main "$dist" >/dev/null
      # A package already there at its version is kept if it is the same file;
      # aptly refuses one that differs.
      "${aptly[@]}" repo add "$dist" "$@" >/dev/null
      if "${aptly[@]}" publish show "$dist" filesystem:repository: >/dev/null 2>&1; then
        "${aptly[@]}" publish update -batch -gpg-key="$key" "$dist" filesystem:repository: >/dev/null
      else
        "${aptly[@]}" publish repo -batch -gpg-key="$key" -origin=A-NAS -label=A-NAS "$dist" filesystem:repository: >/dev/null
      fi
      echo "$dist: $("${aptly[@]}" repo show "$dist" | sed -n "s/^Number of packages: //p") package versions"' \
      publish "$dist" "$key" "${debs[@]}"
    ;;
  *)
    sed -n '2,26p' "$0"
    exit 2
    ;;
esac
