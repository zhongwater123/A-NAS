# shellcheck shell=bash
# The content a release pins: the AI Worker's models and Python runtime
# (ADR 0016), web search's runtime (ADR 0030), Qdrant (ADR 0028) and the text
# reader's llama.cpp. Each is a Debian package of its own whose name carries
# the content's identity, at version 1 (ADR 0034): a release depends on the
# names it pins, so apt downloads only content that changed, and the content
# of an older release stays installed beside a newer one until apt autoremove.
# The files keep the places the installer gave them under /opt/a-nas.
#
# scripts/build-packages.sh and scripts/build-content-packages.sh source this
# file; it defines variables and functions only. Needs jq.

# The Python of Debian 13, which the runtimes are built for.
TARGET_PYTHON=3.13

# Each model role is ROLE:EXTENSION:MANIFEST, the manifest under
# deploy/models. The AI Worker's unit finds the role's file as
# /usr/lib/a-nas/ai/ROLE.EXTENSION and its manifest as ROLE.json.
ai_roles=(
  model:litertlm:embeddinggemma-2-740m
  detector:onnx:dfine-m-obj365
  face-detector:onnx:yunet-2026may
  face-embedder:onnx:auraface-v1
  animal-embedder:onnx:dinov2-small
  ocr-model:gguf:qwen3.5-4b-q4_k_m
  ocr-mmproj:gguf:qwen3.5-4b-mmproj-f16
)

# pin_field FILE KEY: a field of a pin, failing when it is missing.
pin_field() { jq -er --arg key "$2" '.[$key]' "$1"; }

# package_name TEXT: TEXT as a Debian package name.
package_name() { tr 'A-Z_' 'a-z-' <<<"$1"; }

# model_package MANIFEST: the package of a model.
model_package() {
  echo "a-nas-model-$(package_name "$(pin_field "$1" name)")-$(pin_field "$1" sha256 | cut -c1-12)"
}

# ai_runtime_id REPO: the AI Worker's runtime, named by its lock and Python.
ai_runtime_id() {
  { cat "$1/ai/requirements.txt"; printf 'python=%s\n' "$TARGET_PYTHON"; } | sha256sum | cut -c1-16
}

# search_runtime_id REPO: web search's runtime, named by its lock, Python and
# the SearXNG archive it is built from.
search_runtime_id() {
  { cat "$1/deploy/search/requirements.txt"
    printf 'python=%s\nsearxng=%s\n' "$TARGET_PYTHON" "$(pin_field "$1/deploy/search/searxng.json" sha256)"; } |
    sha256sum | cut -c1-16
}

# pinned_package REPO KIND MANIFEST: the package of Qdrant or llama.cpp.
pinned_package() { echo "a-nas-$2-$(pin_field "$1/$3" sha256 | cut -c1-12)"; }
qdrant_package() { pinned_package "$1" qdrant deploy/qdrant/qdrant.json; }
llama_package() { pinned_package "$1" llama deploy/llama/llama.json; }

# assistant_runtime_package REPO: the Hermes runtime of the assistant trial,
# which a-nas-assistant-trial depends on (ai/assistant-spike/runtime.json).
assistant_runtime_package() { pinned_package "$1" assistant-runtime ai/assistant-spike/runtime.json; }

# content_packages REPO: every content package a-nas depends on.
content_packages() {
  local role
  for role in "${ai_roles[@]}"; do
    model_package "$1/deploy/models/${role##*:}.json"
  done
  echo "a-nas-ai-runtime-$(ai_runtime_id "$1")"
  echo "a-nas-search-runtime-$(search_runtime_id "$1")"
  qdrant_package "$1"
  llama_package "$1"
}
