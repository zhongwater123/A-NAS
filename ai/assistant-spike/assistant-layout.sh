#!/usr/bin/env bash
# Stand-in for the Host Agent's typed operations for the AI assistant (ADR 0024), run as
# root. Every subcommand is idempotent. Directories are root:root without setgid and get
# exactly the ACL below, written as internal/hostops/linux/acl.go writes them (setfacl
# --set; defaults dropped first when the ACL has none); a reserved name holding something
# other than a directory is moved aside, and so is a directory at Agent空间's names that root
# does not own (an impostor made after the real one was renamed).
#
# Only for the disposable spike and system-test containers. It chowns and setfacls by path
# in folders members can write (Shared, a Hermes home) and writes the token into the product
# service's own directory, both following links, so whoever controls those folders could
# turn it against root. The trial on the Experimental NAS (nas-trial.sh) therefore runs
# prepare only once, on fresh state, with the session gate (no token copies). The Host Agent opens every directory
# with openat2(RESOLVE_NO_SYMLINKS) and O_NOFOLLOW, acts only on what it opened, and hands
# the token to the product service in its typed response.
#
#   assistant-layout.sh spaces  DATA ACCOUNT...   spaces and recycle bins as the Host Agent
#                                                 makes them (spike only; the installer does
#                                                 this on a real volume)
#   assistant-layout.sh prepare DATA ACCOUNT...   Agent空间 and its member folders, the
#                                                 assistant directories, the files Hermes's
#                                                 home binds, then members
#   assistant-layout.sh members DATA ACCOUNT...   only what lives in root's own /etc: member
#                                                 config, instance secrets, instance sockets
#                                                 (nothing members can write; the trial's update)
#   assistant-layout.sh quota   DATA SIZE ACCOUNT...  simple quota on the volume, SIZE per
#                                                 assistant directory
#   assistant-layout.sh remove  DATA ACCOUNT      what deleting the account removes
#
# With ASSISTANT_GATE=host-agent the Host Agent's File Broker is the session gate (ADR 0024
# decision 10): the instance sockets stay root's and the product service gets no secret, the
# broker hands out a connection and an access token of Hermes's login per session. Otherwise
# (the spike, and the local UI prototype whose product service runs without a File Broker)
# PRODUCT_USER (default a-nas) owns the instance sockets and gets a copy of each instance
# secret in PRODUCT_TOKENS (default /run/anas-spike-tokens, which run.sh shares with the host
# in product mode) to log in with.
set -euo pipefail
here=$(cd "$(dirname "$0")" && pwd)
users=a-nas-users
product_user=${PRODUCT_USER:-a-nas}
product_tokens=${PRODUCT_TOKENS:-/run/anas-spike-tokens}
gate=${ASSISTANT_GATE:-}
runtime_python=/opt/a-nas/assistant-runtimes/spike/venv/bin/python

# acl.go: inheritedACL, containerACL, trashRootACL.
inherited() {
  local access="user::rwx,group::---,other::---,mask::rwx" entry all
  for entry in "$@"; do access+=",$entry"; done
  all=$access
  IFS=, read -ra entries <<<"$access"
  for entry in "${entries[@]}"; do all+=",default:$entry"; done
  echo "$all"
}
container() { echo "user::rwx,group::---,other::---,group:$users:--x,mask::--x"; }
trash_root() { echo "user::rwx,group::---,other::---,$1:--x,mask::--x"; }

displace() { mv -T "$1" "$1.displaced-$(date +%s%N)"; }

# ensure_dir PATH ACL [reserved|owned] [subvolume]: owned also moves aside a directory root
# does not own, with whatever others made inside it, instead of taking it over in place.
ensure_dir() {
  local path=$1 acl=$2 reserved=${3:-} subvolume=${4:-}
  if [[ -L "$path" || ( -e "$path" && ! -d "$path" ) ]]; then
    [[ -n "$reserved" ]] || { echo "$path is not a safe directory" >&2; return 1; }
    displace "$path"
  elif [[ "$reserved" == owned && -d "$path" && "$(stat -c %u "$path")" != 0 ]]; then
    displace "$path"
  fi
  if [[ ! -e "$path" ]]; then
    if [[ "$subvolume" == subvolume && "$(stat -f -c %T "$(dirname "$path")")" == btrfs ]]; then
      btrfs subvolume create "$path" >/dev/null
    else
      mkdir -m 0700 "$path"
    fi
  fi
  chown root:root "$path"
  chmod g-s "$path"
  [[ "$acl" == *default:* ]] || setfacl --remove-default "$path"
  setfacl --set "$acl" "$path"
}

# Hermes's home needs something at each path the unit binds over; anything else there is
# moved aside.
mount_point_file() { [[ -f "$1" && ! -L "$1" ]] || { [[ ! -e "$1" && ! -L "$1" ]] || displace "$1"; install -m 0644 /dev/null "$1"; }; }
mount_point_dir() { [[ -d "$1" && ! -L "$1" ]] || { [[ ! -e "$1" && ! -L "$1" ]] || displace "$1"; install -d "$1"; }; }

cmd=${1:?usage}; data=${2:?data volume}; shift 2
case $cmd in
  spaces)
    for path in "$(dirname "$data")" "$data"; do setfacl --modify "group:$users:--x" "$path"; done
    ensure_dir "$data/spaces" "$(container)"
    ensure_dir "$data/spaces/private" "$(container)"
    ensure_dir "$data/spaces/shared" "$(inherited "group:$users:rwx")" "" subvolume
    ensure_dir "$data/spaces/shared/.a-nas-trash" "$(trash_root "group:$users")" reserved
    for account in "$@"; do
      ensure_dir "$data/spaces/private/$account" "$(inherited "user:$account:rwx")" "" subvolume
      ensure_dir "$data/spaces/private/$account/.a-nas-trash" "$(trash_root "user:$account")" reserved
      ensure_dir "$data/spaces/private/$account/.a-nas-trash/$account" "$(inherited "user:$account:rwx")" reserved
      ensure_dir "$data/spaces/shared/.a-nas-trash/$account" "$(inherited "user:$account:rwx")" reserved
    done
    ;;
  prepare)
    # ADR 0024 decision 5: every member reads Agent空间; each member writes only their own
    # folder there (through the change broker), the pattern of .a-nas-trash/<username>. It sits
    # in Shared, which members can write: the product service refuses to rename, move or delete
    # it. Product mode uses the Host Agent's managed roots; only the isolated
    # stand-in creates them here. Samba hides Agent空间 through its veto list.
    if [[ -z "$gate" ]]; then
      ensure_dir "$data/spaces/shared/Agent空间" "$(inherited "group:$users:r-x")" owned
    else
      [[ -d "$data/spaces/shared/Agent空间" && ! -L "$data/spaces/shared/Agent空间" && "$(stat -c %u "$data/spaces/shared/Agent空间")" == 0 ]] || {
        echo 'Host Agent must reconcile Agent空间 before preparing the assistant' >&2; exit 1;
      }
    fi
    # The change broker walks to the staging area as the member, so members pass through.
    ensure_dir "$data/assistant" "$(container)"
    for account in "$@"; do
      if [[ -z "$gate" ]]; then
        ensure_dir "$data/spaces/shared/Agent空间/$account" "$(inherited "group:$users:r-x" "user:$account:rwx")" owned
      else
        [[ -d "$data/spaces/shared/Agent空间/$account" && ! -L "$data/spaces/shared/Agent空间/$account" && "$(stat -c %u "$data/spaces/shared/Agent空间/$account")" == 0 ]] || {
          echo "Host Agent must reconcile Agent空间/$account before preparing the assistant" >&2; exit 1;
        }
      fi
      home=$data/assistant/$account
      ensure_dir "$home" "user::rwx,group::---,other::---,user:$account:--x,mask::--x" "" subvolume
      ensure_dir "$home/files" "$(inherited "user:$account:rwx")"
      ensure_dir "$home/hermes" "$(inherited "user:$account:rwx")"
      for file in config.yaml .env SOUL.md active_profile; do mount_point_file "$home/hermes/$file"; done
      for folder in hooks plugins profiles; do mount_point_dir "$home/hermes/$folder"; done
    done
    ;&
  members)
    for account in "$@"; do
      # The instance's secret: created once, root-only; the member's password for Hermes's own
      # login (decision 10), which the Host Agent uses for a signed-in session. Without the
      # gate the product service gets its own copy and logs in itself.
      install -d -m 0700 /etc/a-nas-assistant/tokens
      if ! grep -qxE 'HERMES_DASHBOARD_SESSION_TOKEN=[0-9a-f]{64}' "/etc/a-nas-assistant/tokens/$account.env" 2>/dev/null; then
        (umask 077; printf 'HERMES_DASHBOARD_SESSION_TOKEN=%s\n' "$(od -An -tx1 -N32 /dev/urandom | tr -d ' \n')" \
          > "/etc/a-nas-assistant/tokens/$account.env")
      fi
      # Hermes reads skills.external_dirs, mcp_servers (under serve), dashboard.basic_auth and
      # browser.backend from this file only. The login holds the secret's scrypt hash, made
      # with Hermes's own hash_password; without the runtime only the probe runs, and serve,
      # whose login gate would refuse to start without it, never does.
      install -d -m 0755 "/etc/a-nas-assistant/members/$account" "/etc/a-nas-assistant/members/$account/anas-member"
      hash=
      if [[ -x "$runtime_python" ]]; then
        hash=$(sed -n 's/^HERMES_DASHBOARD_SESSION_TOKEN=//p' "/etc/a-nas-assistant/tokens/$account.env" | tr -d '\n' |
          "$runtime_python" -c 'import sys; from plugins.dashboard_auth.basic import hash_password; print(hash_password(sys.stdin.read()))')
      fi
      { printf 'skills:\n  external_dirs: [/opt/a-nas/assistant-skills, /etc/a-nas-assistant/members/%s]\n' "$account"
        sed -n '/^mcp_servers:/,/^[a-z]/{/^mcp_servers:/p;/^[ #]/p}' "$here/managed-config.yaml"
        # Hermes's own browser tools only, served by the member's VNAS window (browser
        # control), never the Browser Use CLI.
        printf 'browser:\n  backend: "off"\n'
        [[ -z "$hash" ]] || printf 'dashboard:\n  basic_auth:\n    username: %s\n    password_hash: "%s"\n' "$account" "$hash"
      } > "/etc/a-nas-assistant/members/$account/config.yaml.new"
      mv "/etc/a-nas-assistant/members/$account/config.yaml.new" "/etc/a-nas-assistant/members/$account/config.yaml"
      sed "s/{{member}}/$account/g" "$here/member-skill.md" > "/etc/a-nas-assistant/members/$account/anas-member/SKILL.md"
      chmod 0644 "/etc/a-nas-assistant/members/$account/config.yaml" "/etc/a-nas-assistant/members/$account/anas-member/SKILL.md"
      if [[ "$gate" != host-agent ]] && getent passwd "$product_user" >/dev/null; then
        install -d -m 0700 -o "$product_user" "$product_tokens"
        (umask 077; sed -n 's/^HERMES_DASHBOARD_SESSION_TOKEN=//p' "/etc/a-nas-assistant/tokens/$account.env" \
          > "$product_tokens/$account.token")
        chown "$product_user" "$product_tokens/$account.token"
      fi
      # Token files and Linux group membership survive account disable. Ask
      # the Host Agent even on the first install, before enabling any entry.
      if [[ "$gate" == host-agent ]]; then
        if python3 -I "$here/cron-waker.py" --active "$account"; then
          :
        else
          status=$?
          if [[ "$status" == 1 ]]; then continue; fi
          echo "cannot query active identity for $account; assistant installation incomplete" >&2
          exit "$status"
        fi
      fi
      for template in anas-assistant-api@.socket anas-assistant-curator@.timer; do
        [[ -f /etc/systemd/system/$template ]] || continue
        unit=${template/@./@$account.}
        # Account disable masks all assistant entry points. An update must
        # preserve that decision instead of re-enabling them or failing here.
        state=$(systemctl is-enabled "$unit" 2>/dev/null || true)
        [[ "$state" == masked || "$state" == masked-runtime ]] && continue
        systemctl enable --now --quiet "$unit"
      done
    done
    ;;
  quota)
    size=${1:?size}; shift
    btrfs qgroup show "$data" >/dev/null 2>&1 || btrfs quota enable --simple "$data"
    for account in "$@"; do btrfs qgroup limit "$size" "$data/assistant/$account"; done
    ;;
  remove)
    account=${1:?account}
    systemctl disable --now --quiet "anas-assistant-api@$account.socket" "anas-assistant-curator@$account.timer" 2>/dev/null || true
    systemctl stop "anas-assistant-api@$account.service" "anas-assistant@$account.service" 2>/dev/null || true
    rm -f "/etc/a-nas-assistant/tokens/$account.env" "$product_tokens/$account.token"
    rm -rf "/etc/a-nas-assistant/members/$account"
    if [[ -d "$data/assistant/$account" ]]; then
      if btrfs subvolume show "$data/assistant/$account" >/dev/null 2>&1; then
        btrfs subvolume delete "$data/assistant/$account" >/dev/null
      else
        rm -rf -- "${data:?}/assistant/$account"
      fi
    fi
    # Agent空间/<account> holds what the member published to the family; what happens to it,
    # like the member's recycle bins, is decided with account deletion (#26).
    ;;
  *)
    sed -n '2,29p' "$0"
    exit 2
    ;;
esac
