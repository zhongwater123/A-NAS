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
# serve-managed.sh, serve-surface.sh, "serve-background.sh alice 30", measure.sh, crash.sh,
# quota.sh. Those use a scripted fake model; serve_client.py is the JSON-RPC client.
# SPIKE_CHANNEL=api_server with sandbox installs the earlier API Server channel instead
# (api-server-channel/), for api-basic.sh, api-tools.sh, api-after.sh, api-managed.sh,
# boundary.sh, scenarios and the UI prototype.
#
# With a real OpenAI-compatible model (the key stays in gateway.py outside the sandbox):
#   SPIKE_CHANNEL=api_server SPIKE_MODEL=deepseek-flash SPIKE_KEY_FILE=~/model.key ai/assistant-spike/run.sh sandbox
#   ai/assistant-spike/run.sh scenarios [scenarios.txt|scenarios-publish.txt|scenarios-boundary.txt]
#   ai/assistant-spike/run.sh test boundary-after.sh   what persisted in memory and skills
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
    docker run --rm -e HOST_UID="$(id -u)" -e HOST_GID="$(id -g)" \
      -v "$work/src:/src:ro" -v "$work/out:/out" -v "$here:/spike:ro" \
      anas-ai-dev:trixie bash /spike/build-runtime.sh 2>&1 | tee "$work/out/build.log"
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
    docker rm -f "$name" >/dev/null 2>&1 || true
    docker run -d --name "$name" --privileged --cgroupns=host -v /sys/fs/cgroup:/sys/fs/cgroup:rw \
      --tmpfs /run --tmpfs /run/lock -v "$work/out:/spike-out:ro" -v "$here:/spike:ro" "${key_mount[@]}" \
      anas-systemd:trixie >/dev/null
    for _ in $(seq 1 50); do
      docker exec "$name" systemctl is-system-running 2>/dev/null | grep -qE 'running|degraded' && break
      sleep 0.5
    done
    docker exec -e SPIKE_MODEL="${SPIKE_MODEL:-}" -e SPIKE_UPSTREAM="${SPIKE_UPSTREAM:-}" -e SPIKE_CHANNEL="${SPIKE_CHANNEL:-}"       -e SPIKE_PRODUCT_UID="${SPIKE_PRODUCT_UID:-}" "$name" bash /spike/setup.sh 2>&1 | tail -3
    docker exec "$name" systemctl start anas-assistant-probe@alice.service
    docker exec "$name" journalctl -u anas-assistant-probe@alice.service -o cat --no-pager | grep -E '^(PASS|FAIL|INFO)'
    ;;
  scenarios)
    docker exec "$name" python3 /spike/scenarios.py "/spike/${2:-scenarios.txt}"
    ;;
  test)
    shift
    docker exec "$name" bash "/spike/$1" "${@:2}"
    ;;
  clean)
    docker rm -f "$name"
    ;;
  *)
    sed -n '2,27p' "$0"
    exit 2
    ;;
esac
