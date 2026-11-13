#!/usr/bin/env bash
# The A-NAS apt repository (ADR 0034): a directory that aptly keeps and
# signs, which a deploy machine copies to a NAS or a web server as it is. apt
# checks every file it takes from it against the signed Release file, so
# whoever copies it is not trusted with the release.
#
#   apt-repository.sh key GNUPGHOME
#       makes a test signing key in GNUPGHOME unless one is there, and prints
#       its fingerprint. A test key is for development and test machines;
#       releases are signed with the A-NAS archive key (scripts/release-key.sh,
#       docs/runbooks/manage-release-key.md).
#   apt-repository.sh fingerprint GNUPGHOME
#       prints the fingerprint of GNUPGHOME's primary key.
#   apt-repository.sh signing-key GNUPGHOME
#       prints the fingerprint of the key in GNUPGHOME that signs.
#   apt-repository.sh keyring OUT VERSION KEY
#       builds a-nas-archive-keyring at VERSION into OUT: the public key KEY,
#       a GnuPG home or a key file such as deploy/release/a-nas-archive-key.asc,
#       which the repository's .sources file names in Signed-By.
#   apt-repository.sh publish REPO DIST GNUPGHOME DEB...
#       adds the packages to the distribution DIST (release or dev) of the
#       repository in REPO, made on first use, and signs it with the key in
#       GNUPGHOME that can sign: the test key, or a machine's subkey of the
#       archive key, whose passphrase ANAS_RELEASE_PASSPHRASE_FILE gives. Every
#       version stays, so apt can roll back to an older one. A package that is
#       there already at the same version is kept if it is the same file and
#       refused if it is not. aptly's own state is in .REPO.aptly beside
#       REPO; what the published repository has and that state lacks is taken
#       from the repository first, so a machine that copied a repository
#       another machine published carries it on.
#   apt-repository.sh signed-by REPO KEY
#       succeeds when REPO has no distribution yet or each one is signed by a
#       key of KEY, a GnuPG home or a key file.
#   apt-repository.sh sums DIR GNUPGHOME
#       writes DIR/SHA256SUMS for the files in DIR and signs it with the key
#       that publish signs with, as SHA256SUMS.gpg beside it: what
#       systemd-sysupdate checks a device image's update files against
#       (scripts/build-image.sh).
#
# GnuPG and aptly run in the Debian 13 image (scripts/lib/signing.sh), so
# nothing needs installing here.
set -euo pipefail

repo_root=$(cd "$(dirname "$0")/.." && pwd)
# shellcheck source=lib/signing.sh
. "$repo_root/scripts/lib/signing.sh"

# fingerprint GNUPGHOME: the primary key's fingerprint; fails without one.
fingerprint() {
  local listing
  listing=$(in_image "$1:/gnupg" -- gpg --batch --with-colons --list-secret-keys)
  awk -F: '$1 == "fpr" { print $10; exit }' <<<"$listing" | grep .
}
# signing_key GNUPGHOME: the key there that can sign and whose secret is
# there: the test key itself, or a machine's subkey of the archive key.
signing_key() {
  local listing
  listing=$(in_image "$1:/gnupg" -- gpg --batch --with-colons --list-secret-keys)
  awk -F: '$1 == "sec" || $1 == "ssb" { s = ($12 ~ /s/ && $15 != "#" && $2 !~ /[rei]/); next }
    s && $1 == "fpr" { print $10; exit }' <<<"$listing" | grep .
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
  signing-key)
    [[ $# -eq 2 ]] || { echo "usage: $0 signing-key GNUPGHOME" >&2; exit 2; }
    signing_key "$(absolute "$2")"
    ;;
  keyring)
    [[ $# -eq 4 ]] || { echo "usage: $0 keyring OUT VERSION KEY" >&2; exit 2; }
    out=$(absolute "$2")
    version=$3
    work=$(mktemp -d)
    trap 'rm -rf -- "$work"' EXIT
    root=$work/a-nas-archive-keyring
    install -d -m 0755 "$root/DEBIAN" "$root/usr/share/keyrings" "$root/usr/share/doc/a-nas-archive-keyring"
    sed "s/@VERSION@/$version/g" "$repo_root/deploy/debian/a-nas-archive-keyring/DEBIAN/control" >"$root/DEBIAN/control"
    public_key "$4" "$root/usr/share/keyrings/a-nas-archive-keyring.pgp"
    cp "$repo_root/deploy/apt/a-nas.sources" "$repo_root/deploy/apt/a-nas-github.sources" \
      "$root/usr/share/doc/a-nas-archive-keyring/"
    printf 'The public key of the A-NAS repository; see https://github.com/zhongwater123/A-NAS\n' \
      >"$root/usr/share/doc/a-nas-archive-keyring/copyright"
    chmod 0644 "$root/DEBIAN/control" "$root/usr/share/keyrings/a-nas-archive-keyring.pgp" \
      "$root/usr/share/doc/a-nas-archive-keyring"/*
    SOURCE_DATE_EPOCH=${SOURCE_DATE_EPOCH:-$(git -C "$repo_root" log -1 --format=%ct 2>/dev/null || echo 0)} \
      dpkg-deb --root-owner-group -Zxz --build "$root" "$out/a-nas-archive-keyring_${version}_all.deb" >/dev/null
    echo "a-nas-archive-keyring $version"
    ;;
  signed-by)
    [[ $# -eq 3 ]] || { echo "usage: $0 signed-by REPO KEY" >&2; exit 2; }
    [[ -d "$2/dists" ]] || exit 0
    repo=$(cd "$2" && pwd)
    work=$(mktemp -d)
    trap 'rm -rf -- "$work"' EXIT
    public_key "$3" "$work/key.pgp"
    # shellcheck disable=SC2016 # the container's shell expands these
    in_image "$repo/dists:/dists:ro" "$work/key.pgp:/key.pgp:ro" -- bash -c '
      set -euo pipefail
      export GNUPGHOME=$(mktemp -d)
      gpg --batch --quiet --import /key.pgp 2>/dev/null
      keys=$(gpg --batch --with-colons --list-keys)
      primary=$(awk -F: '\''$1 == "fpr" { print $10; exit }'\'' <<<"$keys")
      # VALIDSIG ends with the primary key of the key that signed, also for
      # a subkey revoked since: a key of this family may sign over it.
      result=0
      for release in /dists/*/InRelease; do
        status=$(gpg --batch --status-fd 1 --verify "$release" 2>/dev/null || true)
        signer=$(awk '\''$2 == "VALIDSIG" { print $NF }'\'' <<<"$status")
        if [[ "$signer" != "$primary" ]]; then
          echo "${release#/dists/} is signed by ${signer:-a key that is not this one}" >&2
          result=1
        fi
      done
      exit "$result"'
    ;;
  publish)
    [[ $# -ge 5 ]] || { echo "usage: $0 publish REPO DIST GNUPGHOME DEB..." >&2; exit 2; }
    repo=$(absolute "$2")
    dist=$3
    gnupg=$(absolute "$4")
    shift 4
    [[ "$dist" == release || "$dist" == dev ]] || { echo "DIST is release or dev, not $dist" >&2; exit 2; }
    key=$(signing_key "$gnupg") || { echo "no key in $gnupg can sign (apt-repository.sh key, release-key.sh install)" >&2; exit 2; }
    # A protected key needs its passphrase, which reaches aptly in a tmpfs.
    secret=() sign=(-gpg-key="$key!")
    if [[ -n "${ANAS_RELEASE_PASSPHRASE_FILE:-}" ]]; then
      ask_passphrase
      secret=(--passphrase)
      sign+=(-passphrase-file=/secret/passphrase)
    fi
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
    in_image "${secret[@]}" "$gnupg:/gnupg" "$base:/base" "${mounts[@]}" -- \
      env APTLY_CONFIG="/base/$state/aptly.conf" PUBLISHED="/base/$name" bash -c '
      set -euo pipefail
      dist=$1 sign=$2
      shift 2
      IFS=, read -ra sign <<<"$sign"
      aptly=(aptly -config="$APTLY_CONFIG")
      "${aptly[@]}" repo show "$dist" >/dev/null 2>&1 ||
        "${aptly[@]}" repo create -distribution="$dist" -component=main "$dist" >/dev/null
      # What the published distribution has and the state lacks: everything,
      # when this machine carries on a repository that another machine
      # published and copied here (docs/runbooks/manage-release-key.md).
      index=$PUBLISHED/dists/$dist/main/binary-amd64/Packages
      if [[ -f "$index" ]]; then
        have=$("${aptly[@]}" repo show -with-packages "$dist" | sed -n "s/^  //p")
        missing=()
        while IFS=" " read -r key file; do
          grep -qxF "$key" <<<"$have" || missing+=("$PUBLISHED/$file")
        done < <(awk '\''/^Package: / { p = $2 } /^Version: / { v = $2 } /^Architecture: / { a = $2 }
          /^Filename: / { f = $2 } /^$/ { if (p != "") print p "_" v "_" a, f; p = "" } END { if (p != "") print p "_" v "_" a, f }'\'' "$index")
        if ((${#missing[@]} > 0)); then
          "${aptly[@]}" repo add "$dist" "${missing[@]}" >/dev/null
          echo "$dist: took ${#missing[@]} package versions from the published repository"
        fi
      fi
      # A package already there at its version is kept if it is the same file;
      # aptly refuses one that differs.
      "${aptly[@]}" repo add "$dist" "$@" >/dev/null
      if "${aptly[@]}" publish show "$dist" filesystem:repository: >/dev/null 2>&1; then
        "${aptly[@]}" publish update -batch "${sign[@]}" "$dist" filesystem:repository: >/dev/null
      else
        "${aptly[@]}" publish repo -batch "${sign[@]}" -origin=A-NAS -label=A-NAS "$dist" filesystem:repository: >/dev/null
      fi
      echo "$dist: $("${aptly[@]}" repo show "$dist" | sed -n "s/^Number of packages: //p") package versions"' \
      publish "$dist" "$(IFS=,; echo "${sign[*]}")" "${debs[@]}"
    ;;
  sums)
    [[ $# -eq 3 ]] || { echo "usage: $0 sums DIR GNUPGHOME" >&2; exit 2; }
    dir=$(absolute "$2")
    gnupg=$(absolute "$3")
    key=$(signing_key "$gnupg") || { echo "no key in $gnupg can sign (apt-repository.sh key, release-key.sh install)" >&2; exit 2; }
    secret=() passphrase_option=()
    if [[ -n "${ANAS_RELEASE_PASSPHRASE_FILE:-}" ]]; then
      ask_passphrase
      secret=(--passphrase)
      passphrase_option=(--pinentry-mode loopback --passphrase-file /secret/passphrase)
    fi
    # shellcheck disable=SC2016 # the script expands its variables in the image
    in_image "${secret[@]}" "$gnupg:/gnupg" "$dir:/dir" -- bash -c '
      set -euo pipefail
      key=$1
      shift
      cd /dir
      find . -maxdepth 1 -type f ! -name "SHA256SUMS*" -printf "%P\n" | LC_ALL=C sort | xargs -r sha256sum >SHA256SUMS
      gpg --batch --yes "$@" --local-user "$key!" --detach-sign --output SHA256SUMS.gpg SHA256SUMS' \
      sums "$key" "${passphrase_option[@]}"
    ;;
  *)
    sed -n '2,41p' "$0"
    exit 2
    ;;
esac
