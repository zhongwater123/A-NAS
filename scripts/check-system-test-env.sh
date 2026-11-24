#!/usr/bin/env bash
# Verify the system-test image (and the assistant runtime, when given) match
# this checkout: the image records the hash of the files it was built from, and
# a stale or hand-built image would make the test's result meaningless.
#
#   scripts/check-system-test-env.sh [--image TAG] [--runtime FILE]
#
# The image inputs must match, or the check fails. A different ISO week only
# warns: the weekly rebuild brings Debian's updates in, but it must not block
# everyone between Monday 00:00 UTC and the rebuild.
#
# Exit: 0 match, 1 mismatch, 2 cannot check (missing image, jq, ...).
# ANAS_SYSTEM_TEST_ALLOW_ENV_MISMATCH=1 downgrades the mismatch to a warning.
set -euo pipefail
repo=$(cd "$(dirname "$0")/.." && pwd)
image=anas-systemd:trixie
runtime=
while (($#)); do
  case "$1" in
    --image) image=${2:?--image needs a tag}; shift 2 ;;
    --runtime) runtime=${2:?--runtime needs a file}; shift 2 ;;
    -h|--help) sed -n '2,14p' "$0"; exit 0 ;;
    *) echo "unknown argument: $1" >&2; exit 2 ;;
  esac
done

expected_inputs=$(bash "$repo/scripts/system-test-image-key.sh" --inputs)
expected_week=$(bash "$repo/scripts/system-test-image-key.sh" --week)
fail=0

if ! docker image inspect "$image" >/dev/null 2>&1; then
  echo "image not found: $image" >&2
  echo "build it: scripts/build-system-test-image.sh $image" >&2
  exit 2
fi
label_inputs=$(docker image inspect --format '{{ index .Config.Labels "anas.image.inputs" }}' "$image" 2>/dev/null || true)
label_week=$(docker image inspect --format '{{ index .Config.Labels "anas.image.week" }}' "$image" 2>/dev/null || true)
label_commit=$(docker image inspect --format '{{ index .Config.Labels "anas.image.commit" }}' "$image" 2>/dev/null || true)
if [[ -z "$label_inputs" ]]; then
  echo "image $image has no anas.image.inputs label" >&2
  echo "it predates the labels or was built by hand; rebuild it with scripts/build-system-test-image.sh" >&2
  fail=1
elif [[ "$label_inputs" != "$expected_inputs" ]]; then
  echo "image $image was built for inputs $label_inputs; this checkout expects $expected_inputs" >&2
  echo "use the shared image built for main, or build your own tag for this branch" >&2
  fail=1
else
  echo "image matches: ${label_commit:-unknown commit}"
  if [[ -n "$label_week" && "$label_week" != "$expected_week" ]]; then
    echo "warning: image is from week $label_week, this checkout is in $expected_week; rebuild to pick up Debian's updates (not blocking)" >&2
  fi
fi

if [[ -n "$runtime" ]]; then
  if ! command -v jq >/dev/null 2>&1; then
    echo "jq is needed to read ai/assistant/runtime.json" >&2
    exit 2
  fi
  expected=$(jq -r .sha256 "$repo/ai/assistant/runtime.json")
  actual=$(sha256sum "$runtime" | awk '{print $1}')
  if [[ "$actual" != "$expected" ]]; then
    echo "runtime $runtime sha256 $actual; ai/assistant/runtime.json pins $expected" >&2
    fail=1
  else
    echo "assistant runtime matches"
  fi
fi

if ((fail)); then
  if [[ "${ANAS_SYSTEM_TEST_ALLOW_ENV_MISMATCH:-}" == 1 ]]; then
    echo "continuing because ANAS_SYSTEM_TEST_ALLOW_ENV_MISMATCH=1" >&2
    exit 0
  fi
  exit 1
fi
echo "environment matches"
