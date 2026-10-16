#!/usr/bin/env bash
# One line per directory the assistant depends on: kind (Btrfs subvolume or plain
# directory), owner, setgid and the ACL as sorted entries. Run as root in the spike
# container and in the system test, then diff the two to compare the hand-made layout
# with the real installer's.
#   layout-acl.sh DATA ACCOUNT...
set -uo pipefail
d=${1:?data volume}; shift
paths=(.. . spaces spaces/private spaces/shared spaces/shared/.a-nas-trash spaces/shared/Agent空间 assistant)
for account in "$@"; do
  paths+=("spaces/private/$account" "spaces/private/$account/.a-nas-trash" "spaces/private/$account/.a-nas-trash/$account"
    "spaces/shared/.a-nas-trash/$account" "spaces/shared/Agent空间/$account"
    "assistant/$account" "assistant/$account/files" "assistant/$account/hermes")
done
for rel in "${paths[@]}"; do
  path=$d/$rel
  [[ "$rel" == .. ]] && path=$(dirname "$d") rel="(mount point parent)"
  [[ "$rel" == . ]] && rel="(mount point)"
  if [[ ! -e "$path" ]]; then printf '%-40s missing\n' "$rel"; continue; fi
  kind=dir
  [[ "$(stat -c %i "$path")" == 256 ]] && kind=subvolume  # a Btrfs subvolume root is inode 256
  setgid=$([[ -g "$path" ]] && echo setgid || echo -)
  acl=$(getfacl --omit-header --absolute-names "$path" 2>/dev/null | sed 's/[[:space:]]*#.*//' | grep -v '^$' | sort | paste -sd' ')
  printf '%-40s %-9s %-10s %-6s %s\n' "$rel" "$kind" "$(stat -c %U:%G "$path")" "$setgid" "$acl"
done
