#!/bin/sh
# The shell of make check-steps's steps (Makefile). Called as make calls a
# shell, with the file for the target in front: FILE -c LINE. It runs LINE
# with /bin/sh, as make would, and adds its start and end to FILE.
#
# scripts/check-times.sh --report DIR lists the targets that have a file in
# DIR, slowest first: the time from the start of a target's first line to the
# end of its last, in seconds.
set -u

if [ "${1:-}" = --report ]; then
  echo "Seconds per check step:"
  awk 'FNR == 1 { target = FILENAME; sub(/.*\//, "", target) }
    !(target in start) || $1 < start[target] { start[target] = $1 }
    $2 > end[target] { end[target] = $2 }
    NR == 1 || $1 < first { first = $1 }
    $2 > last { last = $2 }
    END {
      for (target in start) printf "%6d  %s\n", end[target] - start[target], target | "sort -rn"
      close("sort -rn")
      if (NR) printf "%6d  all, from the first start to the last end\n", last - first
    }' "$2"/* 2>/dev/null
  exit 0
fi

file=$1
shift
start=$(date +%s)
/bin/sh "$@"
status=$?
echo "$start $(date +%s)" >>"$file"
exit "$status"
