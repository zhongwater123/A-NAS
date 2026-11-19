#!/bin/sh
# Samba's dfree command for the A-NAS shares (the smb.conf the Host Agent
# writes): the data volume's size, and what is free beyond the reserve that
# every A-NAS writer keeps, 5% of the volume and at least 10 GiB
# (internal/capacity/capacity.go). A Windows copy that would eat into the
# reserve is then refused before it starts, as a Web upload is (#181).
# Clients only ask; a client that writes anyway is not stopped.
#
# Samba runs it as the signed-in user, with the share's directory as its
# argument, and reads "total available" in KiB.
set -eu
# Blocks, blocks available to users and their size (statfs f_frsize).
# shellcheck disable=SC2046 # three numbers, split on purpose
set -- $(stat -f -c '%b %a %S' -- "${1:-.}")
exec awk -v blocks="$1" -v avail="$2" -v size="$3" 'BEGIN {
  total = blocks * size / 1024
  free = avail * size / 1024
  reserve = total / 20
  if (reserve < 10 * 1024 * 1024) reserve = 10 * 1024 * 1024
  free = free > reserve ? free - reserve : 0
  printf "%.0f %.0f\n", total, free
}'
