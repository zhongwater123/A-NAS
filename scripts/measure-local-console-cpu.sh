#!/usr/bin/env bash
# Measures what the local console (Cage + Chromium) costs while nobody uses
# it, for example while the screen saver plays. Read-only: it reads cgroup,
# /proc and DRM counters at the start and end and prints the difference.
#
#   measure-local-console-cpu.sh [SECONDS]
#
# Any account can read the CPU figures. GPU engine time, the loaded VA-API
# driver and the video being served need root (or, for the first two, the
# kiosk's own account); without it those lines say so.
# See docs/investigations/2026-10-28-screensaver-video-cpu.md.
set -euo pipefail
export LC_ALL=C

seconds=${1:-60}
[[ $seconds =~ ^[1-9][0-9]*$ ]] || { echo "usage: $0 [SECONDS]" >&2; exit 2; }
hz=$(getconf CLK_TCK)

cage=$(pgrep -o -x cage || true)
[[ -n $cage ]] || { echo "the local console (cage) is not running" >&2; exit 1; }
cgroup=/sys/fs/cgroup$(sed -n 's/^0:://p' "/proc/$cage/cgroup")
# Cage runs in a logind session scope. The user slice above it also holds
# the account's user manager, and any SSH session of the same account.
slice=${cgroup%/session-*.scope}
gpu=$(pgrep -f '^/usr/lib/chromium/chromium --type=gpu-process' | head -n 1 || true)

pids() { pgrep -x cage; pgrep -f '^/usr/lib/chromium/chromium'; }
usage() { sed -n 's/^usage_usec //p' "$1/cpu.stat"; }

# threads prints "TID TICKS PROCESS-TYPE THREAD-NAME" for every thread of
# Cage and Chromium.
threads() {
  local pid task type stat
  for pid in $(pids); do
    # Chromium rewrites its command line into one space-separated string.
    type=$(tr '\0' ' ' < "/proc/$pid/cmdline" 2>/dev/null | grep -o -- '--type=[a-z-]*' | head -n 1 || true)
    type=${type#--type=}
    [[ $(cat "/proc/$pid/comm" 2>/dev/null) == cage ]] && type=cage
    for task in /proc/"$pid"/task/*; do
      stat=$(cat "$task/stat" 2>/dev/null) || continue
      # After the parenthesised name: $12 is utime, $13 stime (fields 14, 15).
      # shellcheck disable=SC2086 # split the fields on purpose
      set -- ${stat##*) }
      printf '%s %s %s %s\n' "${task##*/}" "$((${12} + ${13}))" "${type:-browser}" "$(tr ' ' _ 2>/dev/null < "$task/comm")"
    done
  done
}

# engines prints "ENGINE NANOSECONDS" busy time summed over the DRM clients
# of Cage and Chromium. A client can sit behind several descriptors, so each
# drm-client-id counts once.
engines() {
  local pid fd
  for pid in $(pids); do
    for fd in /proc/"$pid"/fdinfo/*; do
      grep -s -E '^drm-(client-id|engine-[a-z-]+):' "$fd" || true
    done
  done | awk '
    $1 == "drm-client-id:" { client = $2; next }
    $1 ~ /^drm-engine-capacity-/ { next }
    !seen[client, $1]++ { name = substr($1, 12, length($1) - 12); busy[name] += $2 }
    END { for (name in busy) print name, busy[name] }' | sort
}

# serving prints the screen saver files the Product Service has open.
serving() {
  local api
  api=$(pgrep -o -x anas-api || true)
  [[ -n $api ]] || return 0
  find "/proc/$api/fd" -mindepth 1 -maxdepth 1 -lname '/var/lib/a-nas/screensavers/*' -printf '%l\n' 2>/dev/null || true
}

before_scope=$(usage "$cgroup")
before_slice=$(usage "$slice")
before_threads=$(threads)
before_engines=$(engines)
videos=$(serving)
for _ in $(seq 1 "$seconds"); do
  sleep 1
  videos+=$'\n'$(serving)
done
after_scope=$(usage "$cgroup")
after_slice=$(usage "$slice")
after_threads=$(threads)
after_engines=$(engines)

cores() { awk -v a="$1" -v b="$2" -v s="$seconds" 'BEGIN { printf "%.3f", (b - a) / 1e6 / s }'; }
echo "time:          $(date --iso-8601=seconds), over ${seconds}s"
echo "kiosk account: $(ps -o user= -p "$cage")"
echo "session scope: $(cores "$before_scope" "$after_scope") cores  ${cgroup#/sys/fs/cgroup}"
echo "user slice:    $(cores "$before_slice" "$after_slice") cores  ${slice#/sys/fs/cgroup}"

if [[ -z $before_engines ]]; then
  echo "GPU engines:   unreadable (needs root or the kiosk account)"
else
  join <(echo "$before_engines") <(echo "$after_engines") |
    awk -v s="$seconds" '{ printf "GPU %-14s %5.1f%% busy\n", $1 ":", ($3 - $2) / 1e9 / s * 100 }'
fi
if [[ -n $gpu ]] && maps=$(cat "/proc/$gpu/maps" 2>/dev/null); then
  driver=$(grep -o '[^/]*_drv_video\.so' <<<"$maps" | sort -u | tr '\n' ' ' || true)
  echo "VA-API driver: ${driver:-none loaded}"
else
  echo "VA-API driver: unreadable (needs root or the kiosk account)"
fi
if [[ $EUID -ne 0 ]]; then
  echo "screen saver:  unreadable (needs root)"
else
  playing=$(sed '/^$/d' <<<"$videos" | sort -u)
  echo "screen saver:  ${playing:-no video served}"
  [[ -z $playing ]] || while read -r video; do
    ffprobe -v error -select_streams v:0 -show_entries stream=codec_name,profile,width,height,avg_frame_rate,bit_rate \
      -of compact=p=0 "$video" 2>/dev/null | sed 's/^/               /' || true
  done <<<"$playing"
fi

echo "busiest threads (cores):"
join <(sort <<<"$before_threads") <(sort <<<"$after_threads") |
  awk -v hz="$hz" -v s="$seconds" '{ d = ($5 - $2) / hz / s; if (d >= 0.005) printf "  %.3f  %-13s %s\n", d, $3, $4 }' |
  sort -rn | head -n 15
