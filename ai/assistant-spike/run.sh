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
# The suggested order is build, sandbox, then: api-basic.sh, "api-tools.sh alice calls.txt",
# api-after.sh, api-managed.sh, crash.sh, quota.sh. Those use a scripted fake model.
#
# With a real OpenAI-compatible model (the key stays in gateway.py outside the sandbox):
#   SPIKE_MODEL=deepseek-flash SPIKE_KEY_FILE=~/model.key ai/assistant-spike/run.sh sandbox
#   ai/assistant-spike/run.sh scenarios [scenarios.txt|scenarios-publish.txt]
set -euo pipefail
here=$(cd "$(dirname "$0")" && pwd)
work=${HERMES_SPIKE_DIR:-$HOME/hermes-spike}
commit=0e427d788d253630d38a3a51cd76849e73cd6925
name=hermes-spike-sbx

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
    docker rm -f "$name" >/dev/null 2>&1 || true
    docker run -d --name "$name" --privileged --cgroupns=host -v /sys/fs/cgroup:/sys/fs/cgroup:rw \
      --tmpfs /run --tmpfs /run/lock -v "$work/out:/spike-out:ro" -v "$here:/spike:ro" "${key_mount[@]}" \
      anas-systemd:trixie >/dev/null
    for _ in $(seq 1 50); do
      docker exec "$name" systemctl is-system-running 2>/dev/null | grep -qE 'running|degraded' && break
      sleep 0.5
    done
    docker exec -e SPIKE_MODEL="${SPIKE_MODEL:-}" -e SPIKE_UPSTREAM="${SPIKE_UPSTREAM:-}" "$name" bash /spike/setup.sh 2>&1 | tail -3
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
    sed -n '2,17p' "$0"
    exit 2
    ;;
esac
