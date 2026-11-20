#!/usr/bin/env bash
# Stand-in for the Host Agent's typed operations for the AI assistant (ADR 0024), run as
# root. Every subcommand is idempotent. Directories are root:root without setgid and get
# exactly the ACL below, written as internal/hostops/linux/acl.go writes them (setfacl
# --set; defaults dropped first when the ACL has none); a reserved name holding something
# other than a directory is moved aside, and so is a directory at Agent空间's names that root
# does not own (an impostor made after the real one was renamed). The one exception is
# Hermes's home, which is the member's own (member_home).
#
# Only for the disposable spike container (run.sh): on an A-NAS the Host Agent does this
# (internal/hostops/linux/assistant.go) and writes the same files at the same places. This
# chowns and setfacls by path in folders members can write (Shared, a Hermes home) and writes
# the token into the product service's own directory, both following links, so whoever
# controls those folders could turn it against root; the Host Agent opens every directory
# with openat2(RESOLVE_NO_SYMLINKS) and O_NOFOLLOW and acts only on what it opened.
#
#   assistant-layout.sh spaces  DATA ACCOUNT...   spaces and recycle bins as the Host Agent
#                                                 makes them
#   assistant-layout.sh prepare DATA ACCOUNT...   Agent空间 and its member folders, the
#                                                 assistant directories, the files Hermes's
#                                                 home binds, then members
#   assistant-layout.sh members DATA ACCOUNT...   the member's settings and instance secret in
#                                                 /var/lib/a-nas-host/assistant, the instance
#                                                 sockets, and the owner of an existing Hermes
#                                                 home (member_home)
#   assistant-layout.sh quota   DATA SIZE ACCOUNT...  simple quota on the volume, SIZE per
#                                                 assistant directory (the system test uses it
#                                                 too: the Host Agent sets none yet)
#   assistant-layout.sh remove  DATA ACCOUNT      what deleting the account removes
#
# Without the Host Agent's session gate, PRODUCT_USER (default a-nas) owns the instance
# sockets and gets a copy of each instance secret in PRODUCT_TOKENS (default
# /run/anas-spike-tokens, which run.sh shares with the host in product mode) to log in with.
set -euo pipefail
users=a-nas-users
product_user=${PRODUCT_USER:-a-nas}
product_tokens=${PRODUCT_TOKENS:-/run/anas-spike-tokens}
lib=/usr/lib/a-nas/assistant
state=/var/lib/a-nas-host/assistant
runtime_python=$lib/runtime/venv/bin/python

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

# member_home PATH ACCOUNT: Hermes's home is the member's own directory, 0700, without an ACL
# (ADR 0024 decision 3). Hermes's gateway, which runs the scheduled tasks, serves only a home
# that the user it runs as owns and that no ACL lets anyone else write. Only this one
# directory is touched, never what is in it: its parent is root's and the member can only
# pass through it, so the name cannot be turned into a link to somewhere else.
member_home() {
  local path=$1 account=$2
  if [[ -L "$path" || ( -e "$path" && ! -d "$path" ) ]]; then echo "$path is not a safe directory" >&2; return 1; fi
  [[ -e "$path" ]] || mkdir -m 0700 "$path"
  chown --no-dereference "$account:" "$path"
  chmod u=rwx,go=,g-s "$path"
  setfacl --remove-all --remove-default "$path"
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
    ensure_dir "$data/spaces/shared/Agent空间" "$(inherited "group:$users:r-x")" owned
    # The change broker walks to the staging area as the member, so members pass through.
    ensure_dir "$data/assistant" "$(container)"
    for account in "$@"; do
      ensure_dir "$data/spaces/shared/Agent空间/$account" "$(inherited "group:$users:r-x" "user:$account:rwx")" owned
      home=$data/assistant/$account
      ensure_dir "$home" "user::rwx,group::---,other::---,user:$account:--x,mask::--x" "" subvolume
      ensure_dir "$home/files" "$(inherited "user:$account:rwx")"
      member_home "$home/hermes" "$account"
      for file in config.yaml .env SOUL.md active_profile; do mount_point_file "$home/hermes/$file"; done
      for folder in hooks plugins profiles; do mount_point_dir "$home/hermes/$folder"; done
    done
    ;&
  members)
    for account in "$@"; do
      # An install from before the Hermes home was the member's own left it root's with an ACL.
      [[ ! -d "$data/assistant/$account/hermes" ]] || member_home "$data/assistant/$account/hermes" "$account"
      # The instance's secret: created once, root-only; the member's password for Hermes's own
      # login (decision 10). The spike's product service gets its own copy and logs in itself.
      install -d -m 0700 "$state" "$state/tokens"
      if ! grep -qxE 'HERMES_DASHBOARD_SESSION_TOKEN=[0-9a-f]{64}' "$state/tokens/$account.env" 2>/dev/null; then
        (umask 077; printf 'HERMES_DASHBOARD_SESSION_TOKEN=%s
' "$(od -An -tx1 -N32 /dev/urandom | tr -d ' 
')"           > "$state/tokens/$account.env")
      fi
      # Hermes reads skills.external_dirs, mcp_servers (under serve) and dashboard.basic_auth
      # from this file only, as the Host Agent writes it (internal/hostops/linux/assistant.go).
      # The login holds the secret's scrypt hash, made with Hermes's own hash_password here;
      # without the runtime only the probe runs, and serve, whose login gate would refuse to
      # start without it, never does.
      member=$state/members/$account
      install -d -m 0755 "$state/members" "$member" "$member/skills" "$member/skills/anas-member"
      hash=
      if [[ -x "$runtime_python" ]]; then
        hash=$(sed -n 's/^HERMES_DASHBOARD_SESSION_TOKEN=//p' "$state/tokens/$account.env" | tr -d '
' |
          "$runtime_python" -c 'import sys; from plugins.dashboard_auth.basic import hash_password; print(hash_password(sys.stdin.read()))')
      fi
      { printf 'skills:
  external_dirs: [%s/skills, /run/anas-assistant-skills]
' "$lib"
        sed -n '/^mcp_servers:/,/^[a-z]/{/^mcp_servers:/p;/^[ #]/p}' "$lib/managed/config.yaml"
        [[ -z "$hash" ]] || printf 'dashboard:
  basic_auth:
    username: %s
    password_hash: "%s"
' "$account" "$hash"
      } > "$member/config.yaml.new"
      mv "$member/config.yaml.new" "$member/config.yaml"
      # The settings of the planning instance, which its unit binds instead: the same with the
      # servers of its own policy, which adds the montage center's planning server (the Host
      # Agent writes config-plan.yaml beside config.yaml the same way).
      plan_policy=$lib/managed-plan/config.yaml
      [[ -f "$plan_policy" ]] || plan_policy=$lib/managed/config.yaml
      { sed '/^mcp_servers:/,$d' "$member/config.yaml"
        sed -n '/^mcp_servers:/,/^[a-z]/{/^mcp_servers:/p;/^[ #]/p}' "$plan_policy"
        sed -n '/^dashboard:/,$p' "$member/config.yaml"
      } > "$member/config-plan.yaml.new"
      mv "$member/config-plan.yaml.new" "$member/config-plan.yaml"
      sed "s/{{member}}/$account/g" "$lib/member-skill.md" > "$member/skills/anas-member/SKILL.md"
      chmod 0644 "$member/config.yaml" "$member/config-plan.yaml" "$member/skills/anas-member/SKILL.md"
      if getent passwd "$product_user" >/dev/null; then
        install -d -m 0700 -o "$product_user" "$product_tokens"
        (umask 077; sed -n 's/^HERMES_DASHBOARD_SESSION_TOKEN=//p' "$state/tokens/$account.env"           > "$product_tokens/$account.token")
        chown "$product_user" "$product_tokens/$account.token"
      fi
      for template in anas-assistant-api@.socket anas-assistant-curator@.timer; do
        [[ -f /etc/systemd/system/$template ]] || continue
        unit=${template/@./@$account.}
        state_of=$(systemctl is-enabled "$unit" 2>/dev/null || true)
        [[ "$state_of" == masked || "$state_of" == masked-runtime ]] && continue
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
    rm -f "$state/tokens/$account.env" "$product_tokens/$account.token"
    rm -rf "${state:?}/members/$account"
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
    sed -n '2,32p' "$0"
    exit 2
    ;;
esac
