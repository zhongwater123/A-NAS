#!/usr/bin/env bash
# Hermes Agent spike for the AI assistant (docs/research/hermes-agent-runtime.md).
# Runs on a Linux development machine with Docker and the local images
# anas-ai-dev:trixie (ai/dev.Dockerfile) and anas-systemd:trixie
# (scripts/build-system-test-image.sh). Nothing here touches a NAS.
#
#   ai/assistant-spike/run.sh build        clone the pinned commit, build the runtime tarball
#   ai/assistant-spike/run.sh sandbox      start the disposable systemd container and probe the sandbox
#   ai/assistant-spike/run.sh test SCRIPT [ARGS]  run one test script inside it, e.g. api-basic.sh
#   ai/assistant-spike/run.sh clean        remove the container
#
# The assistant runs `hermes serve` (Hermes Desktop's backend). The suggested order is build,
# sandbox, then: serve-basic.sh, "serve-tools.sh alice calls.txt", serve-after.sh,
# serve-managed.sh, serve-surface.sh, "serve-background.sh alice 30", serve-environ.sh,
# measure.sh, crash.sh, quota.sh. Those use a scripted fake model; serve_client.py is the
# JSON-RPC client. The probe also runs toolsets-check.py against the managed policy.
# In-turn approvals (#111), with SPIKE_APPROVAL_TIMEOUT=20 given to sandbox: approvals-broker.sh,
# "scenarios calls-approval.txt", approvals-managed.sh. Memory, skills and the background review
# (#114): memory-skills.sh, review-whitelist.sh, review-defaults.sh; review-scenarios.sh needs a real model.
# Read-only photos (#113), with SPIKE_PHOTOS=1 given to sandbox: photos-socket.sh,
# "scenarios calls-photos.txt", photos-vision.sh; with a real model, "scenarios
# scenarios-photos.txt" and photos-vision.sh.
# SPIKE_CHANNEL=api_server with sandbox installs the earlier API Server channel instead
# (api-server-channel/), for api-basic.sh, api-tools.sh, api-after.sh, api-managed.sh,
# boundary.sh and the UI prototype; scenarios works on either channel.
#
# With a real OpenAI-compatible model (the key stays in gateway.py outside the sandbox):
#   SPIKE_MODEL=deepseek-flash SPIKE_KEY_FILE=~/model.key ai/assistant-spike/run.sh sandbox
#   ai/assistant-spike/run.sh scenarios FILE [--answer ANSWER]   how the member answers: scenarios.py
#     FILE: scenarios-approval.txt, scenarios.txt, scenarios-publish.txt or scenarios-boundary.txt
#   ai/assistant-spike/run.sh test boundary-after.sh   what persisted in memory and skills
#
# setup.sh makes accounts and spaces by hand (assistant-layout.sh spaces), then installs the
# assistant (assistant-install.sh) and the probe's files (seed.sh). scripts/system-test.sh
# does the same on the real installer's accounts and spaces (system-test.sh here), with
# ANAS_SYSTEM_TEST_ASSISTANT_RUNTIME=$HERMES_SPIKE_DIR/out/runtime.tar.gz for Hermes itself.
#
# Several sessions on one machine share the built runtime in $HERMES_SPIKE_DIR (mounted
# read-only) but each needs its own container: HERMES_SPIKE_NAME=<name>, and with
# SPIKE_PRODUCT_UID also HERMES_SPIKE_RUN=<dir> for the sockets.
set -euo pipefail
here=$(cd "$(dirname "$0")" && pwd)
work=${HERMES_SPIKE_DIR:-$HOME/hermes-spike}
commit=0e427d788d253630d38a3a51cd76849e73cd6925
name=${HERMES_SPIKE_NAME:-hermes-spike-sbx}
run=${HERMES_SPIKE_RUN:-$work/run}

case "${1:-}" in
  build)
    mkdir -p "$work/src/hermes-agent" "$work/out"
    if [[ ! -d "$work/src/hermes-agent/.git" ]]; then
      git -C "$work/src/hermes-agent" init -q
      git -C "$work/src/hermes-agent" remote add origin https://github.com/NousResearch/hermes-agent
    fi
    git -C "$work/src/hermes-agent" fetch -q --depth 1 origin "$commit"
    git -C "$work/src/hermes-agent" checkout -q FETCH_HEAD
    # The pinned commit with the fixes A-NAS carries until upstream has them (hermes-patches/,
    # each says why), in a tree of its own so the checkout stays upstream's.
    patched=$(mktemp -d)
    mkdir -p "$patched/hermes-agent"
    git -C "$work/src/hermes-agent" archive FETCH_HEAD | tar -C "$patched/hermes-agent" -xf -
    for p in "$here"/hermes-patches/*.patch; do
      [[ -e $p ]] || continue
      (cd "$patched/hermes-agent" && git apply "$p") || { echo "cannot apply $(basename "$p")" >&2; exit 1; }
      basename "$p" >> "$patched/hermes-agent/A-NAS-PATCHES"
    done
    docker run --rm -e HOST_UID="$(id -u)" -e HOST_GID="$(id -g)" \
      -v "$patched:/src:ro" -v "$work/out:/out" -v "$here:/spike:ro" \
      anas-ai-dev:trixie bash /spike/build-runtime.sh 2>&1 | tee "$work/out/build.log"
    rm -rf -- "$patched"
    ;;
  sandbox)
    # SPIKE_MODEL=<name> (with SPIKE_KEY_FILE and optionally SPIKE_UPSTREAM) uses a real
    # OpenAI-compatible model through gateway.py instead of the scripted fake model.
    key_mount=()
    if [[ -n "${SPIKE_MODEL:-}" ]]; then
      key_mount=(-v "${SPIKE_KEY_FILE:?set SPIKE_KEY_FILE}:/etc/anas-spike/model.key:ro")
    fi
    # SPIKE_PRODUCT_UID=<uid> puts each member's assistant socket and the change
    # broker's socket in $run on the host, owned by that UID, so a product
    # service running on the host (the UI prototype) reaches them.
    if [[ -n "${SPIKE_PRODUCT_UID:-}" ]]; then
      mkdir -p "$run/assistant" "$run/broker" "$run/tokens"
      chmod 0700 "$run/tokens"
      key_mount+=(-v "$run/assistant:/run/a-nas-assistant" -v "$run/broker:/run/anas-spike-broker" -v "$run/tokens:/run/anas-spike-tokens")
    fi
    # SPIKE_PHOTOS=1 also runs the photo service with its assistant socket and a seeded
    # library (#113), from photos-spike/ in this checkout; it needs Go with cgo.
    if [[ -n "${SPIKE_PHOTOS:-}" ]]; then
      bin="$work/photos-spike/$name"
      mkdir -p "$bin"
      (cd "$here/../.." && CGO_ENABLED=1 go build -trimpath -tags osusergo,netgo,sqlite_omit_load_extension \
        -ldflags '-linkmode external -extldflags -static' -o "$bin/photos-spike" ./ai/assistant-spike/photos-spike)
      key_mount+=(-v "$bin:/spike-photos:ro")
    fi
    docker rm -f "$name" >/dev/null 2>&1 || true
    docker run -d --name "$name" --privileged --cgroupns=host -v /sys/fs/cgroup:/sys/fs/cgroup:rw \
      --tmpfs /run --tmpfs /run/lock -v "$work/out:/spike-out:ro" -v "$here:/spike:ro" "${key_mount[@]}" \
      anas-systemd:trixie >/dev/null
    for _ in $(seq 1 50); do
      docker exec "$name" systemctl is-system-running 2>/dev/null | grep -qE 'running|degraded' && break
      sleep 0.5
    done
    # SPIKE_APPROVAL_TIMEOUT=<seconds> shortens Hermes's wait for the member's approval.
    docker exec -e SPIKE_MODEL="${SPIKE_MODEL:-}" -e SPIKE_UPSTREAM="${SPIKE_UPSTREAM:-}" -e SPIKE_CHANNEL="${SPIKE_CHANNEL:-}" \
      -e SPIKE_PRODUCT_UID="${SPIKE_PRODUCT_UID:-}" -e SPIKE_APPROVAL_TIMEOUT="${SPIKE_APPROVAL_TIMEOUT:-}" \
      "$name" bash /spike/setup.sh 2>&1 | tail -3
    docker exec "$name" systemctl start anas-assistant-probe@alice.service
    docker exec "$name" journalctl -u anas-assistant-probe@alice.service -o cat --no-pager | grep -E '^(PASS|FAIL|INFO)'
    ;;
  scenarios)
    docker exec "$name" /opt/a-nas/assistant-runtimes/spike/venv/bin/python -u /spike/scenarios.py "/spike/${2:-scenarios.txt}" "${@:3}"
    ;;
  test)
    shift
    docker exec "$name" bash "/spike/$1" "${@:2}"
    ;;
  clean)
    docker rm -f "$name"
    ;;
  *)
    sed -n '2,40p' "$0"
    exit 2
    ;;
esac
