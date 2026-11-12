#!/usr/bin/env bash
# Tests the A-NAS archive key's procedures (scripts/release-key.sh,
# docs/runbooks/manage-release-key.md) end to end with a throwaway key, in
# the Debian 13 image IMAGE, where apt checks signatures as a device does:
#
# - the key is made with a subkey for two machines, alpha and beta; each
#   machine's signing home holds its own subkey and no primary secret;
# - a repository alpha signed is accepted by apt; beta copies it and carries
#   it on, taking the published packages into its own aptly state, and apt
#   accepts that too; a test key may not sign it;
# - alpha's subkey is revoked: alpha's machine may no longer sign, a device
#   still on the old keyring takes beta's repository and with it the new
#   keyring, and with the new keyring apt refuses what alpha signs;
# - a subkey added for a third machine signs, and the revocation stays.
#
# Usage: scripts/test-release-key.sh [IMAGE]
set -uo pipefail

export IMAGE=${1:-anas-systemd:trixie}
repo=$(cd "$(dirname "$0")/.." && pwd)
# shellcheck source=lib/signing.sh
. "$repo/scripts/lib/signing.sh"
key=$repo/scripts/release-key.sh
apt_repository=$repo/scripts/apt-repository.sh
work=$(mktemp -d "${TMPDIR:-/tmp}/a-nas-release-key-test.XXXXXX")
trap 'rm -rf -- "$work"' EXIT
printf 'a throwaway passphrase\n' >"$work/passphrase"
export ANAS_RELEASE_PASSPHRASE_FILE=$work/passphrase
export ANAS_RELEASE_PUBLIC_KEY=$work/public/a-nas-archive-key.asc

failures=0
check() {
  local name=$1
  shift
  if "$@" >>"$work/log" 2>&1; then
    echo "PASS $name"
  else
    echo "FAIL $name"
    tail -n 15 "$work/log" | sed 's/^/  | /'
    failures=$((failures + 1))
  fi
}
fails() { ! "$@"; }
as() { # MACHINE COMMAND...: COMMAND with MACHINE's signing home.
  local machine=$1
  shift
  ANAS_RELEASE_GNUPGHOME=$work/$machine "$@"
}
# keyring VERSION: the keyring package of the public key file at VERSION.
keyring() { "$apt_repository" keyring "$work/debs" "$1" "$ANAS_RELEASE_PUBLIC_KEY" >/dev/null; }
# publish MACHINE REPO DEB...: what make release does with MACHINE's subkey.
publish() { as "$1" "$apt_repository" publish "$work/$2" dev "$work/$1" "${@:3}"; }
# accepts REPO KEYRING: apt in a fresh container, trusting the public key in
# the file KEYRING for REPO alone, takes REPO's index.
accepts() {
  local out
  out=$(docker run --rm -v "$work/$1:/repo:ro" -v "$2:/keyring.pgp:ro" "$IMAGE" bash -c '
    rm -f /etc/apt/sources.list /etc/apt/sources.list.d/*
    install -D -m 0644 /keyring.pgp /etc/apt/keyrings/a-nas.pgp
    printf "Types: deb\nURIs: file:/repo\nSuites: dev\nComponents: main\nSigned-By: /etc/apt/keyrings/a-nas.pgp\n" \
      >/etc/apt/sources.list.d/a-nas.sources
    apt-get update 2>&1' 2>&1)
  echo "$out"
  grep -q 'file:/repo dev InRelease' <<<"$out" && ! grep -qiE 'verification failed|NO_PUBKEY|not signed|revoked' <<<"$out"
}
# refuses REPO KEYRING: apt refuses REPO's index because its signing key is revoked.
refuses() {
  local out
  if out=$(accepts "$@"); then
    echo "$out"
    return 1
  fi
  echo "$out"
  grep -qi 'revoked' <<<"$out"
}
# packages REPO: the versions of the keyring package in REPO's index.
packages() { sed -n 's/^Version: //p' "$work/$1/dists/dev/main/binary-amd64/Packages" | sort | paste -sd ' ' -; }
subkey() { awk -v m="$1" '$1 == m { print $2 }' "$work/ceremony/subkeys.txt"; }
copy() { mkdir -p "$work/$2" && cp -a "$work/$1/dists" "$work/$1/pool" "$work/$2/"; }

echo "== the key"
check "the key is made with a subkey for alpha and beta" "$key" create "$work/ceremony" alpha beta
check "its secrets and revocation certificate are written" test -s "$work/ceremony/primary-secret.asc" \
  -a -s "$work/ceremony/revocation.asc" -a -s "$work/ceremony/signing-alpha.asc" -a -s "$work/ceremony/signing-beta.asc"
check "the secrets are under the passphrase" bash -c "! grep -q 'BEGIN PGP PUBLIC' '$work/ceremony/signing-alpha.asc' &&
  docker run --rm -v '$work/ceremony:/c:ro' '$IMAGE' gpg --batch --list-packets /c/signing-alpha.asc 2>/dev/null | grep -q 'protected'"
check "the key is made once" fails "$key" create "$work/again" alpha
check "alpha installs its subkey" as alpha "$key" install "$work/ceremony/signing-alpha.asc"
check "beta installs its subkey" as beta "$key" install "$work/ceremony/signing-beta.asc"
check "alpha signs with its own subkey" test "$(as alpha "$key" check)" = "$(subkey alpha)"
check "alpha's signing home holds no primary secret" bash -c "docker run --rm --user $(id -u) -e HOME=/tmp \
  -v '$work/alpha:/g' -e GNUPGHOME=/g '$IMAGE' gpg --batch --with-colons --list-secret-keys | grep -q '^sec:.*:#:'"
check "a wrong passphrase does not install a subkey" bash -c "printf 'wrong\n' >'$work/wrong' &&
  ! ANAS_RELEASE_PASSPHRASE_FILE='$work/wrong' ANAS_RELEASE_GNUPGHOME='$work/gamma-wrong' '$key' install '$work/ceremony/signing-alpha.asc'"
public_key "$ANAS_RELEASE_PUBLIC_KEY" "$work/keyring-1.pgp"

echo "== one repository, two machines"
keyring 1
check "alpha publishes the repository" publish alpha one "$work/debs/a-nas-archive-keyring_1_all.deb"
check "apt accepts what alpha signed" accepts one "$work/keyring-1.pgp"
copy one two
keyring 2
check "beta carries on the repository alpha published" publish beta two "$work/debs/a-nas-archive-keyring_2_all.deb"
check "beta took alpha's packages into its state" grep -q 'took 1 package versions from the published repository' "$work/log"
check "the repository keeps both versions" test "$(packages two)" = "1 2"
check "apt accepts what beta signed" accepts two "$work/keyring-1.pgp"
check "the repository is signed by the archive key" "$apt_repository" signed-by "$work/two" "$ANAS_RELEASE_PUBLIC_KEY"
"$apt_repository" key "$work/test-key" >/dev/null
check "a test key is told the repository is not its own" fails "$apt_repository" signed-by "$work/two" "$work/test-key"

echo "== alpha's machine is lost"
check "the primary key revokes alpha's subkey" "$key" revoke-subkey "$work/ceremony/primary-secret.asc" "$(subkey alpha)"
check "the public key shows alpha's subkey revoked" bash -c "'$key' show | grep -qx 'sub $(subkey alpha) revoked s'"
check "alpha's machine may no longer sign" fails as alpha "$key" check
check "beta still signs" test "$(as beta "$key" check)" = "$(subkey beta)"
keyring 3
public_key "$ANAS_RELEASE_PUBLIC_KEY" "$work/keyring-3.pgp"
check "beta publishes the keyring without alpha" publish beta two "$work/debs/a-nas-archive-keyring_3_all.deb"
check "a device on the old keyring takes it" accepts two "$work/keyring-1.pgp"
check "with the new keyring apt accepts beta" accepts two "$work/keyring-3.pgp"
copy two stolen
check "the lost subkey can still sign a copy" publish alpha stolen "$work/debs/a-nas-archive-keyring_3_all.deb"
check "apt with the new keyring refuses what alpha signs" refuses stolen "$work/keyring-3.pgp"

echo "== a third machine"
check "the primary key adds a subkey for gamma" "$key" add-subkey "$work/ceremony/primary-secret.asc" "$work/gamma-out" gamma
check "gamma installs it" as gamma "$key" install "$work/gamma-out/signing-gamma.asc"
check "alpha's subkey stays revoked" bash -c "'$key' show | grep -qx 'sub $(subkey alpha) revoked s'"
public_key "$ANAS_RELEASE_PUBLIC_KEY" "$work/keyring-4.pgp"
keyring 4
copy two three
check "gamma publishes" publish gamma three "$work/debs/a-nas-archive-keyring_4_all.deb"
check "apt with the newest keyring accepts gamma" accepts three "$work/keyring-4.pgp"

echo "release key test failures=$failures"
((failures == 0))
