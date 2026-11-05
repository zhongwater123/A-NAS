#!/usr/bin/env bash
# Converts the official Qwen3.5-4B weights into the text reader's two GGUF
# files (docs/architecture/photo-ocr.md#模型): the language part quantized to
# Q4_K_M and the vision encoder in F16, which
# deploy/models/qwen3.5-4b-q4_k_m.json and qwen3.5-4b-mmproj-f16.json pin.
#
# Before converting it checks the inputs against the manifests: the llama.cpp
# checkout is at their conversionCommit, and every weight file has its
# weightsSha256. It converts twice and stops unless both results are
# byte-identical, prints each file's size and SHA-256, and moves the files to
# the output directory only when both match the manifests. Without either
# manifest it does not start.
#
# Runs in anas-ocr-eval:trixie (ai/ocr-eval.Dockerfile) with a llama.cpp
# checkout built at the commit the manifests name at /src, the download at the
# manifests' revision at /hf (read-only), the manifests at /manifests and the
# output directory at /out (torch needs a user name, which uid 1000 lacks in
# the image, hence USER):
#
#   docker run --rm --network none --user 1000:1000 -e USER=ocr -v ~/ocr-eval/llama.cpp:/src:ro \
#     -v "$PWD/scripts:/scripts:ro" -v "$PWD/deploy/models:/manifests:ro" \
#     -v /mnt/d/A-NAS-models/ocr/qwen3.5-4b/hf:/hf:ro -v ~/ocr-eval/models/qwen3.5-4b:/out \
#     anas-ocr-eval:trixie bash /scripts/convert-ocr-model.sh
set -euo pipefail

src=${SRC:-/src}
hf=${HF:-/hf}
manifests=${MANIFESTS:-/manifests}
out=${OUT:-/out}
model='Qwen3.5-4B-Q4_K_M.gguf'
mmproj='mmproj-Qwen3.5-4B-f16.gguf'
pairs=("$model:qwen3.5-4b-q4_k_m.json" "$mmproj:qwen3.5-4b-mmproj-f16.json")

manifest_paths=()
for pair in "${pairs[@]}"; do
  manifest=$manifests/${pair#*:}
  if [[ ! -f "$manifest" ]]; then
    echo "no manifest $manifest: nothing to check the conversion against" >&2
    exit 4
  fi
  manifest_paths+=("$manifest")
done

# The checkout every manifest names.
commit=$(git -c safe.directory="$src" -C "$src" rev-parse HEAD)
for manifest in "${manifest_paths[@]}"; do
  wanted=$(python3 -c 'import json, sys; print(json.load(open(sys.argv[1]))["conversionCommit"])' "$manifest")
  if [[ "$commit" != "$wanted" ]]; then
    echo "$src is at $commit; $manifest converts with llama.cpp $wanted" >&2
    exit 4
  fi
done

# The weights the manifests pin.
if ! python3 -c '
import json, sys
hf = sys.argv[1]
for path in sys.argv[2:]:
    for name, sha in json.load(open(path))["weightsSha256"].items():
        print(f"{sha}  {hf}/{name}")
' "$hf" "${manifest_paths[@]}" | sort -u | sha256sum --check --quiet --strict -; then
  echo "the weights in $hf are not the ones the manifests pin" >&2
  exit 4
fi

work=$(mktemp -d "$out/.convert-XXXXXX")
trap 'rm -rf -- "$work"' EXIT
convert() { # DIR: both files into DIR
  mkdir "$1"
  python3 "$src/convert_hf_to_gguf.py" --mmproj --outtype f16 --outfile "$1/$mmproj" "$hf" >/dev/null
  python3 "$src/convert_hf_to_gguf.py" --outtype f16 --outfile "$1/f16.gguf" "$hf" >/dev/null
  "$src/build/bin/llama-quantize" "$1/f16.gguf" "$1/$model" Q4_K_M >/dev/null
  rm -f -- "$1/f16.gguf"
}
convert "$work/first"
convert "$work/second"
for file in "$model" "$mmproj"; do
  if ! cmp -s "$work/first/$file" "$work/second/$file"; then
    echo "two conversions of $file differ" >&2
    exit 4
  fi
done

status=0
for pair in "${pairs[@]}"; do
  file=${pair%%:*} manifest=$manifests/${pair#*:}
  size=$(stat -c %s "$work/first/$file")
  sha=$(sha256sum "$work/first/$file" | cut -d ' ' -f 1)
  echo "$file $size $sha"
  if [[ "$(python3 -c 'import json, sys; m = json.load(open(sys.argv[1])); print(m["sizeBytes"], m["sha256"])' \
    "$manifest")" != "$size $sha" ]]; then
    echo "$file does not match $manifest" >&2
    status=4
  fi
done
if ((status != 0)); then
  exit "$status"
fi
for file in "$model" "$mmproj"; do
  mv -f -- "$work/first/$file" "$out/$file"
done
