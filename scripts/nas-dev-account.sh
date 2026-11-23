#!/usr/bin/env bash
# Developer SSH accounts on the Experimental NAS. Run as root on the NAS; the
# runbook docs/runbooks/manage-developer-ssh-accounts.md says when and how.
#
#   setup                       groups, sshd rules, key directory and the shared
#                               evaluation directory; safe to run again
#   add <user> <public-key>     create a developer account dev-<name>, or give an
#                               existing one another key; the key is a .pub file
#   list                        developer accounts, their keys and last logins
#   revoke <user>               remove the account's keys, expire it and end its
#                               sessions; the home directory stays
#
# A developer account is an ordinary login without sudo, docker or a password.
# It belongs to anas-devs, whose sshd rules accept only keys from the
# root-owned /etc/ssh/authorized_keys/<user>, so only root grants or revokes
# access; to systemd-journal, to read service logs; and to anas-eval, which
# shares /srv/eval for models, datasets and evaluation runs. UIDs come from
# 1000-1999, below the ranges A-NAS reserves (ADR 0008: 20000-29999; apps and
# services from 30000).
set -euo pipefail

dev_group=anas-devs
eval_group=anas-eval
eval_dir=/srv/eval
key_dir=/etc/ssh/authorized_keys
sshd_dropin=/etc/ssh/sshd_config.d/90-a-nas-devs.conf
slice_dropin=50-a-nas-dev.conf
# A backstop per account, not a budget: a run that grows past MemoryHigh is
# slowed and reclaimed, past MemoryMax it is killed, before the NAS's own
# services start swapping. CPU and I/O weights keep the local console ahead of
# developer sessions in user.slice.
memory_high=6G
memory_max=8G
weight=20
uid_first=1000
uid_last=1999

die() { printf '%s: %s\n' "${0##*/}" "$*" >&2; exit 1; }
say() { printf '== %s\n' "$*"; }

usage() {
  sed -n '2,11p' "$0" | sed 's/^# \{0,1\}//' >&2
  exit 2
}

require_root() { [ "$(id -u)" -eq 0 ] || die "run as root"; }

require_setup() {
  getent group "$dev_group" >/dev/null || die "group $dev_group is missing: run setup first"
  [ -f "$sshd_dropin" ] || die "$sshd_dropin is missing: run setup first"
}

is_developer() {
  [[ " $(id -nG "$1" 2>/dev/null) " == *" $dev_group "* ]]
}

valid_name() {
  [[ $1 =~ ^dev-[a-z][a-z0-9]{1,15}$ ]] || die "account name must be dev-<2-16 lowercase letters or digits>: $1"
}

lock_password() {
  # passwd -S prints L for a locked password, P for a usable one, NP for none.
  [ "$(passwd -S "$1" | awk '{print $2}')" = L ] || usermod --lock "$1"
}

developers() {
  getent group "$dev_group" | cut -d: -f4 | tr ',' '\n' | sed '/^$/d' | sort
}

sshd_rules() {
  cat <<EOF
# Developer accounts (scripts/nas-dev-account.sh). Only root adds or removes
# their keys, in $key_dir/<user>; no passwords, no agent, X11
# or remote forwarding, local port forwarding only.
Match Group $dev_group
    AuthorizedKeysFile $key_dir/%u
    AuthenticationMethods publickey
    PasswordAuthentication no
    KbdInteractiveAuthentication no
    AllowAgentForwarding no
    AllowTcpForwarding local
    AllowStreamLocalForwarding no
    PermitTunnel no
    X11Forwarding no
Match all
EOF
}

# The effective sshd settings for one user, as a login from the LAN would get.
effective_sshd() {
  sshd -T -C "user=$1,host=dev.lan,addr=192.0.2.1" |
    grep -E '^(authorizedkeysfile|authenticationmethods|passwordauthentication|allowagentforwarding|allowtcpforwarding|x11forwarding) '
}

cmd_setup() {
  require_root
  command -v sshd >/dev/null || die "sshd is not installed"
  command -v setfacl >/dev/null || die "setfacl is missing (package acl)"
  grep -qE '^[[:space:]]*Include[[:space:]]+/etc/ssh/sshd_config\.d/\*\.conf' /etc/ssh/sshd_config ||
    die "/etc/ssh/sshd_config does not include sshd_config.d/*.conf"
  getent group systemd-journal >/dev/null || die "group systemd-journal is missing"

  for group in "$dev_group" "$eval_group"; do
    if getent group "$group" >/dev/null; then
      say "group $group exists"
    else
      groupadd --system "$group"
      say "created group $group"
    fi
  done

  install -d -m 0755 -o root -g root "$key_dir"
  say "key directory $key_dir"

  # Shared by anas-eval: setgid keeps the group on new files, and the default
  # ACL makes them group-writable whatever a member's umask is.
  install -d -m 2770 -o root -g "$eval_group" "$eval_dir"
  setfacl -m g::rwx,o::--- "$eval_dir"
  setfacl -d -m u::rwx,g::rwx,o::--- "$eval_dir"
  say "shared directory $eval_dir ($(stat -c '%A %U:%G' "$eval_dir"))"

  local new
  new=$(sshd_rules)
  if [ -f "$sshd_dropin" ] && [ "$(cat "$sshd_dropin")" = "$new" ]; then
    say "sshd rules unchanged ($sshd_dropin)"
  else
    local backup=""
    if [ -f "$sshd_dropin" ]; then
      backup="$sshd_dropin.$(date -u +%Y%m%dT%H%M%SZ).bak"
      cp -p "$sshd_dropin" "$backup"
    fi
    printf '%s\n' "$new" >"$sshd_dropin.tmp"
    chmod 0644 "$sshd_dropin.tmp"
    mv "$sshd_dropin.tmp" "$sshd_dropin"
    if ! sshd -t; then
      if [ -n "$backup" ]; then mv "$backup" "$sshd_dropin"; else rm -f "$sshd_dropin"; fi
      sshd -t || true
      die "sshd rejected the new rules; restored the previous state, ssh was not reloaded"
    fi
    # Reloading keeps open sessions; a socket-activated sshd reads the file
    # on the next connection anyway.
    if systemctl is-active --quiet ssh.service; then systemctl reload ssh.service; fi
    say "sshd rules written to $sshd_dropin and in effect for new logins${backup:+ (previous: $backup)}"
  fi
  say "setup done"
}

# The first UID in the developer range whose number is free as a UID and as a
# GID, so the account's own group gets the same number.
free_uid() {
  local n
  for ((n = uid_first; n <= uid_last; n++)); do
    if ! getent passwd "$n" >/dev/null && ! getent group "$n" >/dev/null; then
      echo "$n"
      return
    fi
  done
  die "no free UID in $uid_first-$uid_last"
}

# Prints the one key line of a .pub file, or stops.
read_key() {
  local file=$1 line info bits type
  [ -f "$file" ] || die "no such file: $file"
  line=$(tr -d '\r' <"$file" | sed -e '/^[[:space:]]*$/d' -e '/^#/d')
  [ -n "$line" ] || die "$file holds no key"
  [ "$(printf '%s\n' "$line" | wc -l)" -eq 1 ] || die "$file holds more than one line; give one key per file"
  case $line in
    ssh-ed25519\ * | sk-ssh-ed25519@openssh.com\ * | ecdsa-sha2-nistp*\ * | sk-ecdsa-sha2-nistp256@openssh.com\ * | ssh-rsa\ *) ;;
    *) die "$file is not a public key line (options before the key are not accepted)" ;;
  esac
  info=$(printf '%s\n' "$line" | ssh-keygen -lf - 2>/dev/null) || die "$file is not a valid public key"
  bits=${info%% *}
  type=${info##*(}
  if [ "$type" = "RSA)" ] && [ "$bits" -lt 3072 ]; then
    die "RSA keys need at least 3072 bits; this one has $bits (prefer ed25519)"
  fi
  printf '%s\n' "$line"
}

write_slice_limits() {
  local uid=$1 dir=/etc/systemd/system/user-$1.slice.d
  install -d -m 0755 "$dir"
  cat >"$dir/$slice_dropin" <<EOF
# Developer account limits (scripts/nas-dev-account.sh).
[Slice]
MemoryHigh=$memory_high
MemoryMax=$memory_max
CPUWeight=$weight
IOWeight=$weight
EOF
  systemctl daemon-reload
}

cmd_add() {
  [ $# -eq 2 ] || usage
  require_root
  require_setup
  local user=$1 file=$2 key fingerprint uid home keys existing
  valid_name "$user"
  key=$(read_key "$file")
  fingerprint=$(printf '%s\n' "$key" | ssh-keygen -lf - | awk '{print $2}')

  if getent passwd "$user" >/dev/null; then
    is_developer "$user" || die "$user exists and is not in $dev_group; this script only manages developer accounts"
    uid=$(id -u "$user")
    say "account $user exists (UID $uid)"
  else
    uid=$(free_uid)
    groupadd --gid "$uid" "$user"
    useradd --uid "$uid" --gid "$uid" --create-home --shell /bin/bash --comment "A-NAS developer" "$user"
    home=$(getent passwd "$user" | cut -d: -f6)
    chmod 0700 "$home"
    ln -s "$eval_dir" "$home/eval"
    chown -h "$user:$user" "$home/eval"
    say "created $user (UID $uid)"
  fi
  # No expiry (revoke sets one) and no usable password.
  usermod --expiredate '' --append --groups "$dev_group,$eval_group,systemd-journal" "$user"
  lock_password "$user"

  keys=$key_dir/$user
  touch "$keys"
  chown root:root "$keys"
  chmod 0644 "$keys"
  # An empty file makes ssh-keygen fail: no keys yet.
  existing=$(ssh-keygen -lf "$keys" 2>/dev/null | awk '{print $2}') || true
  if grep -qxF "$fingerprint" <<<"$existing"; then
    say "key $fingerprint is already there"
  else
    printf '%s\n' "$key" >>"$keys"
    say "added key $fingerprint"
  fi

  write_slice_limits "$uid"
  say "limits: MemoryHigh=$memory_high MemoryMax=$memory_max CPUWeight=$weight IOWeight=$weight"
  say "groups: $(id -nG "$user")"
  say "effective sshd settings for $user:"
  effective_sshd "$user"
}

cmd_list() {
  [ $# -eq 0 ] || usage
  require_root
  require_setup
  local user expires users
  mapfile -t users < <(developers)
  for user in "${users[@]}"; do
    expires=$(LC_ALL=C chage -l "$user" | sed -n 's/^Account expires[[:space:]]*:[[:space:]]*//p')
    printf '\n%s  UID %s  expires: %s\n' "$user" "$(id -u "$user")" "$expires"
    if [ -s "$key_dir/$user" ]; then
      ssh-keygen -lf "$key_dir/$user" | sed 's/^/  key: /'
    else
      echo "  key: none"
    fi
    # awk stops at the first match, so last may die of SIGPIPE: not an error.
    last -w "$user" 2>/dev/null | awk -v u="$user" '$1 == u { print "  last: " $0; exit }' || true
  done
}

cmd_revoke() {
  [ $# -eq 1 ] || usage
  require_root
  require_setup
  local user=$1 stamp
  valid_name "$user"
  getent passwd "$user" >/dev/null || die "no such account: $user"
  is_developer "$user" || die "$user is not in $dev_group; this script only manages developer accounts"
  stamp=$(date -u +%Y%m%dT%H%M%SZ)
  if [ -f "$key_dir/$user" ]; then
    install -d -m 0700 "$key_dir/.revoked"
    mv "$key_dir/$user" "$key_dir/.revoked/$user.$stamp"
    say "keys moved to $key_dir/.revoked/$user.$stamp"
  fi
  # Day 1 of 1970: PAM's account check refuses every later login, key or not.
  usermod --expiredate 1 "$user"
  lock_password "$user"
  say "account expired"
  if loginctl show-user "$user" >/dev/null 2>&1; then
    loginctl terminate-user "$user"
    say "sessions ended"
  fi
  pkill -KILL -u "$user" 2>/dev/null || true
  say "home $(getent passwd "$user" | cut -d: -f6) kept; delete it only as the runbook says"
}

main() {
  [ $# -ge 1 ] || usage
  local command=$1
  shift
  case $command in
    setup) [ $# -eq 0 ] || usage; cmd_setup ;;
    add) cmd_add "$@" ;;
    list) cmd_list "$@" ;;
    revoke) cmd_revoke "$@" ;;
    *) usage ;;
  esac
}

main "$@"
