# shellcheck shell=bash
# What scripts/apt-repository.sh, scripts/release-key.sh and scripts/release.sh
# share: GnuPG and aptly run in IMAGE, the Debian 13 image that
# scripts/build-system-test-image.sh makes (anas-systemd:trixie by default),
# as the calling user, so nothing needs installing on the machine that signs;
# and the passphrase of the A-NAS archive key reaches them on standard input,
# never in a file on this machine's disk (docs/runbooks/manage-release-key.md).
# This file defines functions only.

# in_image [--passphrase] MOUNT... -- COMMAND...: COMMAND in IMAGE with the
# mounts, as this user, with GNUPGHOME=/gnupg. With --passphrase, the
# passphrase that ask_passphrase read is in /secret/passphrase, a tmpfs of the
# container that goes with it.
in_image() {
  local mounts=() secret=false
  if [[ "$1" == --passphrase ]]; then
    secret=true
    shift
  fi
  while [[ "$1" != -- ]]; do
    mounts+=(-v "$1")
    shift
  done
  shift
  local run=(docker run --rm --user "$(id -u):$(id -g)" -e HOME=/tmp -e GNUPGHOME=/gnupg "${mounts[@]}")
  if "$secret"; then
    [[ -n "${passphrase:-}" ]] || { echo "no passphrase: call ask_passphrase first" >&2; return 2; }
    # shellcheck disable=SC2016 # the container's shell expands these
    printf '%s\n' "$passphrase" | "${run[@]}" -i --tmpfs /secret:mode=1777 "${IMAGE:-anas-systemd:trixie}" \
      bash -c 'umask 077 && head -n 1 >/secret/passphrase && exec "$@"' bash "$@"
  else
    "${run[@]}" "${IMAGE:-anas-systemd:trixie}" "$@"
  fi
}

# ask_passphrase [--new]: sets passphrase to the A-NAS archive key's
# passphrase: the first line of ANAS_RELEASE_PASSPHRASE_FILE when that is set
# (tests, and runs without a terminal), else what the terminal gives. --new
# asks twice, for a key being made, and refuses an empty one.
ask_passphrase() {
  local again
  if [[ -n "${ANAS_RELEASE_PASSPHRASE_FILE:-}" ]]; then
    IFS= read -r passphrase <"$ANAS_RELEASE_PASSPHRASE_FILE" || [[ -n "${passphrase:-}" ]] ||
      { echo "no passphrase in $ANAS_RELEASE_PASSPHRASE_FILE" >&2; return 2; }
  elif { : </dev/tty; } 2>/dev/null; then
    IFS= read -rs -p "Passphrase of the A-NAS archive key: " passphrase </dev/tty
    echo >/dev/tty
    if [[ "${1:-}" == --new ]]; then
      IFS= read -rs -p "The same passphrase again: " again </dev/tty
      echo >/dev/tty
      [[ "$passphrase" == "$again" ]] || { echo "the two passphrases differ" >&2; return 2; }
    fi
  else
    echo "no terminal to ask for the passphrase on: set ANAS_RELEASE_PASSPHRASE_FILE" >&2
    return 2
  fi
  if [[ "${1:-}" == --new && ${#passphrase} -lt 12 ]]; then
    echo "the archive key's passphrase needs at least 12 characters" >&2
    return 2
  fi
}

# public_key KEY OUT: KEY's public key in OpenPGP's binary form into the file
# OUT. KEY is a GnuPG home, whose primary key is taken, or a key file, armored
# or binary, such as deploy/release/a-nas-archive-key.asc.
public_key() {
  local key out
  key=$(cd "$(dirname "$1")" && pwd)/$(basename "$1")
  out=$(cd "$(dirname "$2")" && pwd)
  if [[ -d "$key" ]]; then
    # shellcheck disable=SC2016 # the container's shell expands these
    in_image "$key:/gnupg" "$out:/out" -- bash -c \
      'gpg --batch --export "$(gpg --batch --with-colons --list-secret-keys | awk -F: '\''$1 == "fpr" { print $10; exit }'\'')" >"/out/$1"' \
      bash "$(basename "$2")"
  else
    # shellcheck disable=SC2016 # the container's shell expands these
    in_image "$key:/key.asc:ro" "$out:/out" -- bash -c \
      'export GNUPGHOME=$(mktemp -d) && gpg --batch --quiet --import /key.asc && gpg --batch --export >"/out/$1"' \
      bash "$(basename "$2")"
  fi
  [[ -s "$2" ]] || { echo "no public key in $1" >&2; return 2; }
}
