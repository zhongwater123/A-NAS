#!/usr/bin/env bash
# The A-NAS archive key, which signs the A-NAS apt repository
# (docs/decisions/2026-11-11-release-signing-key.md). Its primary key only
# certifies: it makes and revokes signing subkeys, one for each machine that
# signs releases, and is kept apart from them. Each signing machine holds its
# own subkey alone, under a passphrase, in its signing home
# (~/.local/share/a-nas/release-key unless ANAS_RELEASE_GNUPGHOME names
# another). Devices trust the public key in deploy/release/a-nas-archive-key.asc
# (ANAS_RELEASE_PUBLIC_KEY names another, for tests), which the
# a-nas-archive-keyring package carries; a change to it is a commit to review.
# Steps and the reasons for them: docs/runbooks/manage-release-key.md.
#
#   release-key.sh create OUT MACHINE...
#       makes the key, with a signing subkey for each MACHINE, in a container
#       that keeps nothing. OUT, a new directory, then holds the primary
#       key's secret (primary-secret.asc), its revocation certificate
#       (revocation.asc), each machine's subkey (signing-MACHINE.asc) and
#       subkeys.txt; the public key goes to the public key file. Every secret
#       is under the passphrase it asks for.
#   release-key.sh install SIGNING_FILE
#       puts a machine's signing-MACHINE.asc into this machine's signing
#       home, then checks it.
#   release-key.sh check
#       prints the fingerprint of the subkey this machine signs with. It fails
#       unless the signing home holds a subkey of the public key that the
#       public key file does not show as revoked.
#   release-key.sh add-subkey PRIMARY_SECRET OUT MACHINE
#       adds a signing subkey for MACHINE: OUT/signing-MACHINE.asc, and the
#       public key file with it.
#   release-key.sh revoke-subkey PRIMARY_SECRET SUBKEY [compromised|superseded|retired]
#       revokes a subkey in the public key file. compromised (the default,
#       for a lost or leaked machine) also voids what it signed before.
#   release-key.sh show
#       prints the public key file's primary key and subkeys.
set -euo pipefail

repo_root=$(cd "$(dirname "$0")/.." && pwd)
# shellcheck source=lib/signing.sh
. "$repo_root/scripts/lib/signing.sh"
public=${ANAS_RELEASE_PUBLIC_KEY:-$repo_root/deploy/release/a-nas-archive-key.asc}
home=${ANAS_RELEASE_GNUPGHOME:-$HOME/.local/share/a-nas/release-key}
uid='A-NAS archive key <zhongwater123@users.noreply.github.com>'

machine_name() {
  [[ "$1" =~ ^[a-z0-9][a-z0-9-]*$ ]] || { echo "a machine name is lower-case letters, digits and -, not $1" >&2; exit 2; }
}
new_directory() {
  [[ ! -e "$1" ]] || { echo "$1 exists: name a new directory" >&2; exit 2; }
  (umask 077 && mkdir -p "$1")
  (cd "$1" && pwd)
}
absolute_file() {
  [[ -f "$1" ]] || { echo "no file $1" >&2; exit 2; }
  echo "$(cd "$(dirname "$1")" && pwd)/$(basename "$1")"
}
public_dir() {
  mkdir -p "$(dirname "$public")"
  cd "$(dirname "$public")" && pwd
}

# What every script below starts with in the image: a throwaway GnuPG home,
# the gpg that reads the passphrase, and helpers on `gpg --with-colons`
# listings, where a key's line is followed by its fingerprint's. A listing is
# read whole before awk looks at it, so awk stopping early cannot cut gpg off.
# shellcheck disable=SC2016 # the container's shell expands these
prelude='
  set -euo pipefail
  export GNUPGHOME=$(mktemp -d)
  gpg_secret=(gpg --batch --quiet --pinentry-mode loopback --passphrase-file /secret/passphrase)
  # primary LISTING: the first fingerprint.
  primary() { awk -F: '\''$1 == "fpr" { print $10; exit }'\'' <<<"$1"; }
  # last_subkey LISTING: the fingerprint of the last subkey.
  last_subkey() { awk -F: '\''$1 == "sub" { s = 1; next } s && $1 == "fpr" { last = $10; s = 0 } END { print last }'\'' <<<"$1"; }
  # validity LISTING SUBKEY: the subkey'\''s validity field, r once revoked.
  validity() { awk -F: -v want="$2" '\''$1 == "sub" { v = $2; next } $1 == "fpr" && $10 == want { print v }'\'' <<<"$1"; }
'
# The primary key's secret from /primary.asc and the public key file from
# /public/$PUBLIC, merged, so revocations and subkeys added since the secret
# was exported stay.
# shellcheck disable=SC2016 # the container's shell expands these
with_primary='
  "${gpg_secret[@]}" --import /primary.asc 2>/dev/null
  gpg --batch --quiet --import "/public/$PUBLIC" 2>/dev/null
  fpr=$(primary "$(gpg --batch --with-colons --list-secret-keys)")
'

case "${1:-}" in
  create)
    [[ $# -ge 3 ]] || { echo "usage: $0 create OUT MACHINE..." >&2; exit 2; }
    [[ ! -e "$public" ]] || { echo "$public exists: the archive key is made once; see docs/runbooks/manage-release-key.md" >&2; exit 2; }
    for machine in "${@:3}"; do machine_name "$machine"; done
    ask_passphrase --new
    out=$(new_directory "$2")
    pubdir=$(public_dir)
    # shellcheck disable=SC2016 # the container's shell expands these
    in_image --passphrase "$out:/out" "$pubdir:/public" -- env PUBLIC="$(basename "$public")" bash -c "$prelude"'
      uid=$1
      shift
      "${gpg_secret[@]}" --quick-generate-key "$uid" ed25519 cert never 2>/dev/null
      fpr=$(primary "$(gpg --batch --with-colons --list-secret-keys)")
      for machine in "$@"; do
        "${gpg_secret[@]}" --quick-add-key "$fpr" ed25519 sign never 2>/dev/null
        sub=$(last_subkey "$(gpg --batch --with-colons --list-keys "$fpr")")
        "${gpg_secret[@]}" --armor --export-secret-subkeys "$sub!" >"/out/signing-$machine.asc"
        echo "$machine $sub" >>/out/subkeys.txt
      done
      "${gpg_secret[@]}" --armor --export-secret-keys "$fpr" >/out/primary-secret.asc
      cp "$GNUPGHOME/openpgp-revocs.d/$fpr.rev" /out/revocation.asc
      gpg --batch --armor --export "$fpr" >"/public/$PUBLIC"
      echo "primary $fpr"
      cat /out/subkeys.txt' \
      bash "$uid" "${@:3}"
    echo "public key: $public"
    echo "secrets: $out (move them as docs/runbooks/manage-release-key.md says)"
    ;;
  install)
    [[ $# -eq 2 ]] || { echo "usage: $0 install SIGNING_FILE" >&2; exit 2; }
    file=$(absolute_file "$2")
    (umask 077 && mkdir -p "$home")
    chmod 0700 "$home"
    existing=$(in_image "$home:/gnupg" -- gpg --batch --with-colons --list-secret-keys 2>/dev/null || true)
    if grep -q '^ssb' <<<"$existing"; then
      echo "$home holds a signing subkey already; remove it first (docs/runbooks/manage-release-key.md)" >&2
      exit 2
    fi
    ask_passphrase
    # gpg imports a secret key without its passphrase, so the passphrase is
    # proven first, by signing with the subkey in a throwaway home.
    # shellcheck disable=SC2016 # the container's shell expands these
    in_image --passphrase "$file:/signing.asc:ro" -- bash -c "$prelude"'
      "${gpg_secret[@]}" --import /signing.asc 2>/dev/null
      secret=$(gpg --batch --with-colons --list-secret-keys)
      sub=$(awk -F: '\''$1 == "ssb" { s = ($12 ~ /s/ && $15 != "#"); next } s && $1 == "fpr" { print $10; exit }'\'' <<<"$secret")
      [[ -n "$sub" ]] || { echo "no signing subkey in the file" >&2; exit 3; }
      printf x | "${gpg_secret[@]}" --local-user "$sub!" --detach-sign >/dev/null 2>&1 ||
        { echo "the passphrase does not unlock subkey $sub" >&2; exit 3; }'
    in_image --passphrase "$home:/gnupg" "$file:/signing.asc:ro" -- \
      gpg --batch --quiet --pinentry-mode loopback --passphrase-file /secret/passphrase --import /signing.asc
    "$0" check
    ;;
  check)
    [[ $# -eq 1 ]] || { echo "usage: $0 check" >&2; exit 2; }
    [[ -f "$public" ]] || { echo "no A-NAS archive key in $public yet (docs/runbooks/manage-release-key.md)" >&2; exit 2; }
    [[ -d "$home" ]] || { echo "no signing home $home: install this machine's subkey (docs/runbooks/manage-release-key.md)" >&2; exit 2; }
    # shellcheck disable=SC2016 # the container's shell expands these
    in_image "$home:/home" "$(absolute_file "$public"):/public.asc:ro" -- bash -c "$prelude"'
      # The subkeys the signing home can sign with: secret there, not a stub.
      secret=$(GNUPGHOME=/home gpg --batch --with-colons --list-secret-keys 2>/dev/null || true)
      mine=$(awk -F: '\''$1 == "ssb" { s = ($12 ~ /s/ && $15 != "#"); next } s && $1 == "fpr" { print $10; s = 0 }'\'' <<<"$secret")
      # The public key file decides which subkeys are still good.
      gpg --batch --quiet --import /public.asc 2>/dev/null
      keys=$(gpg --batch --with-colons --list-keys)
      archive=$(primary "$keys")
      [[ -n "$secret" && "$(primary "$secret")" == "$archive" ]] ||
        { echo "the signing home holds no subkey of the A-NAS archive key $archive" >&2; exit 3; }
      good=$(awk -F: '\''$1 == "sub" { s = ($2 !~ /[rei]/ && $12 ~ /s/); next } s && $1 == "fpr" { print $10; s = 0 }'\'' <<<"$keys")
      for sub in $mine; do
        if grep -qx "$sub" <<<"$good"; then echo "$sub"; exit 0; fi
      done
      echo "the signing home has no good subkey of the A-NAS archive key: ${mine:-none} is revoked or unknown in the public key file" >&2
      exit 3'
    ;;
  add-subkey)
    [[ $# -eq 4 ]] || { echo "usage: $0 add-subkey PRIMARY_SECRET OUT MACHINE" >&2; exit 2; }
    primary=$(absolute_file "$2")
    machine_name "$4"
    [[ -f "$public" ]] || { echo "no A-NAS archive key in $public" >&2; exit 2; }
    ask_passphrase
    out=$(new_directory "$3")
    pubdir=$(public_dir)
    # shellcheck disable=SC2016 # the container's shell expands these
    in_image --passphrase "$primary:/primary.asc:ro" "$out:/out" "$pubdir:/public" -- \
      env PUBLIC="$(basename "$public")" bash -c "$prelude$with_primary"'
      machine=$1
      "${gpg_secret[@]}" --quick-add-key "$fpr" ed25519 sign never 2>/dev/null
      sub=$(last_subkey "$(gpg --batch --with-colons --list-keys "$fpr")")
      "${gpg_secret[@]}" --armor --export-secret-subkeys "$sub!" >"/out/signing-$machine.asc"
      echo "$machine $sub" >/out/subkeys.txt
      gpg --batch --armor --export "$fpr" >"/public/$PUBLIC"
      echo "$machine $sub"' \
      bash "$4"
    ;;
  revoke-subkey)
    [[ $# -eq 3 || $# -eq 4 ]] || { echo "usage: $0 revoke-subkey PRIMARY_SECRET SUBKEY [compromised|superseded|retired]" >&2; exit 2; }
    primary=$(absolute_file "$2")
    [[ "$3" =~ ^[0-9A-F]{40}$ ]] || { echo "SUBKEY is a fingerprint: 40 upper-case hex digits" >&2; exit 2; }
    case "${4:-compromised}" in
      compromised) reason=1 ;;
      superseded) reason=2 ;;
      retired) reason=3 ;;
      *) echo "the reason is compromised, superseded or retired" >&2; exit 2 ;;
    esac
    [[ -f "$public" ]] || { echo "no A-NAS archive key in $public" >&2; exit 2; }
    ask_passphrase
    pubdir=$(public_dir)
    # shellcheck disable=SC2016 # the container's shell expands these
    in_image --passphrase "$primary:/primary.asc:ro" "$pubdir:/public" -- \
      env PUBLIC="$(basename "$public")" bash -c "$prelude$with_primary"'
      sub=$1 reason=$2
      [[ -n "$(validity "$(gpg --batch --with-colons --list-keys "$fpr")" "$sub")" ]] ||
        { echo "$sub is not a subkey of $fpr" >&2; exit 3; }
      printf "key %s\nrevkey\ny\n%s\n\ny\nsave\n" "$sub" "$reason" |
        "${gpg_secret[@]}" --command-fd 0 --edit-key "$fpr" >/dev/null 2>&1 || true
      [[ "$(validity "$(gpg --batch --with-colons --list-keys "$fpr")" "$sub")" == r ]] ||
        { echo "gpg did not revoke $sub" >&2; exit 3; }
      gpg --batch --armor --export "$fpr" >"/public/$PUBLIC"
      echo "revoked $sub"' \
      bash "$3" "$reason"
    ;;
  show)
    [[ -f "$public" ]] || { echo "no A-NAS archive key in $public yet" >&2; exit 2; }
    # shellcheck disable=SC2016 # the container's shell expands these
    in_image "$(absolute_file "$public"):/public.asc:ro" -- bash -c "$prelude"'
      gpg --batch --quiet --import /public.asc 2>/dev/null
      keys=$(gpg --batch --with-colons --list-keys)
      awk -F: '\''
        $1 == "pub" || $1 == "sub" { kind = $1; v = $2; usage = $12; next }
        $1 == "fpr" && kind != "" { print kind, $10, (v == "r" ? "revoked" : "good"), usage; kind = "" }'\'' <<<"$keys"'
    ;;
  *)
    sed -n '2,36p' "$0"
    exit 2
    ;;
esac
