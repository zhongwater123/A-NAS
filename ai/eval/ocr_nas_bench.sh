#!/usr/bin/env bash
# Times OCR models on the Experimental NAS with eval.ocr --bench, one model at a
# time, each in its own user scope (docs/research/photo-ocr-model-evaluation.md).
# Development only: it runs as an unprivileged account and changes nothing on
# the device but its own directory.
#
# Every second it records the NAS's MemAvailable, the scope's memory and peak
# with its anonymous and file-backed parts, the load and the hottest thermal
# zone. Below 700 MiB available it stops the
# model, and the kernel ends the bench before any service (oom_score_adj 1000).
#
# The directory beside this script holds what eval.ocr reads: llama/ (the
# llama-server build and its libraries), models/ (the GGUF files, as
# eval/ocr_models.sh lays them out), data/ (the CC-OCR rows the bench picks,
# same paths as CC-OCR) and ai/eval/ (__init__.py and ocr.py). The runtime of
# the installed release supplies Python with Pillow. Run it detached, so that it
# outlives the SSH session:
#
#   systemd-run --user --unit ocr-bench --working-directory="$HOME/ocr-bench/ai" \
#     -E THREADS=6 bash "$HOME/ocr-bench/ocr_nas_bench.sh"
#
# THREADS (default 6, every core but two), COUNT (items per model, default 12)
# and MODELS (default "glm-ocr qwen3.5-2b") select the run, and
# ANAS_OCR_SERVER_ARGS adds llama-server flags such as --no-mmap; results go to
# runs/t<THREADS><flags>/ beside the script.
set -uo pipefail
root=$(dirname "$(readlink -f "$0")")
threads=${THREADS:-6}
count=${COUNT:-12}
models=${MODELS:-glm-ocr qwen3.5-2b}
python=/opt/a-nas/current/ai/runtime/bin/python
flags=$(printf '%s' "${ANAS_OCR_SERVER_ARGS:-}" | tr -s ' -' '-')
out="$root/runs/t$threads$flags"
mkdir -p "$out"
export ANAS_OCR_MODELS="$root/models" ANAS_OCR_DATA="$root/data" ANAS_OCR_RUNS="$out" ANAS_OCR_LLAMA="$root/llama"
export LD_LIBRARY_PATH="$root/llama" PYTHONDONTWRITEBYTECODE=1
echo 1000 > /proc/self/oom_score_adj
cd "$root/ai" || exit 1

available() { awk '$1 == "MemAvailable:" { print int($2 / 1024) }' /proc/meminfo; }
hottest() { cat /sys/class/thermal/thermal_zone*/temp 2>/dev/null | sort -n | tail -1; }
mib() { local value; value=$(cat "$1" 2>/dev/null) || value=0; echo $((value >> 20)); }
stat_mib() { awk -v key="$2" '$1 == key { print int($2 / 1048576) }' "$1/memory.stat" 2>/dev/null || echo 0; }

sample() { # SCOPE LOG: one line a second while the scope exists
  local scope=$1 log=$2 free
  echo "time,mem_available_mib,scope_current_mib,scope_peak_mib,scope_anon_mib,scope_file_mib,load1,temp_mc" > "$log"
  for _ in $(seq 1 100); do [ -d "$scope" ] && break; sleep 0.1; done
  while [ -d "$scope" ]; do
    free=$(available)
    echo "$(date +%s),$free,$(mib "$scope/memory.current"),$(mib "$scope/memory.peak"),$(stat_mib "$scope" anon),$(stat_mib "$scope" file),$(cut -d' ' -f1 /proc/loadavg),$(hottest)" >> "$log"
    if [ "$free" -lt 700 ]; then
      echo "$(date +%s),watchdog: MemAvailable $free MiB, stopping $scope" >> "$log"
      xargs -r kill < "$scope/cgroup.procs"
      break
    fi
    sleep 1
  done
}

uid=$(id -u)
for model in $models; do
  scope="/sys/fs/cgroup/user.slice/user-$uid.slice/user@$uid.service/app.slice/ocr-$model.scope"
  echo "$(date -Is) start $model: $threads threads, $count items, MemAvailable $(available) MiB, load $(cut -d' ' -f1-3 /proc/loadavg)" >> "$out/bench.log"
  sample "$scope" "$out/$model.memory.csv" &
  sampler=$!
  systemd-run --user --scope --quiet --unit "ocr-$model" -p MemorySwapMax=0 \
    "$python" -m eval.ocr --bench "$count" --threads "$threads" --models "$model" >> "$out/bench.log" 2>&1
  status=$?
  wait "$sampler"
  echo "$(date -Is) end $model: status $status, MemAvailable $(available) MiB" >> "$out/bench.log"
done
echo "$(date -Is) done" >> "$out/bench.log"
