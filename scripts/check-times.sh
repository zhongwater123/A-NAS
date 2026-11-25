#!/bin/sh
# The shell of make check-steps's steps (Makefile). Called as make calls a
# shell, with the file for the target in front: FILE -c LINE. It runs LINE
# with /bin/sh, as make would, and adds its start and end to FILE. A $(shell)
# call has no target, and its FILE ends in a slash: it only runs.
#
# scripts/check-times.sh --report DIR lists the targets that have a file in
# DIR, slowest first: the time from the start of a target's first line to the
# end of its last, in seconds.
set -u

if [ "${1:-}" = --report ]; then
  echo "Seconds per check step:"
  for file in "$2"/*; do
    [ -f "$file" ] || continue
    awk -v target="${file##*/}" 'NR == 1 || $1 < start { start = $1 } $2 > end { end = $2 }
      END { printf "%6d  %s\n", end - start, target }' "$file"
  done | sort -rn
  cat "$2"/* 2>/dev/null | awk 'NR == 1 || $1 < start { start = $1 } $2 > end { end = $2 }
    END { if (NR) printf "%6d  all, from the first start to the last end\n", end - start }'
  exit 0
fi

file=$1
shift
case $file in
  */) exec /bin/sh "$@" ;;
esac
start=$(date +%s)
/bin/sh "$@"
status=$?
echo "$start $(date +%s)" >>"$file"
exit "$status"
