#!/bin/bash
# Prepares the GGUF files that eval.ocr loads (docs/research/photo-ocr-model-evaluation.md).
# Runs inside anas-ocr-eval:trixie with the built llama.cpp checkout at /src,
# the downloads at /hf (read-only) and the output directory at /out:
# (torch needs a user name, which uid 1000 lacks in the image, hence USER):
#   docker run --rm --user 1000:1000 -e USER=ocr -v ~/ocr-eval/llama.cpp:/src:ro -v "$PWD/ai:/ai:ro" \
#     -v /mnt/d/A-NAS-models/ocr:/hf:ro -v ~/ocr-eval/models:/out \
#     anas-ocr-eval:trixie bash /ai/eval/ocr_models.sh
# Every language model ends up as Q8_0; vision encoders stay as published (GLM-OCR's
# Q8_0) or are converted to F16. A model whose download is not complete is skipped.
# Qwen3.5-4B, the model the product reads text with, is converted by
# scripts/convert-ocr-model.sh into ~/ocr-eval/models/qwen3.5-4b, where eval.ocr finds it.
set -euo pipefail
convert=/src/convert_hf_to_gguf.py
quantize=/src/build/bin/llama-quantize
out=${OUT:-/out}

file_type() {
  PYTHONPATH=/src/gguf-py python3 -c 'import sys, gguf
r = gguf.GGUFReader(sys.argv[1])
print(gguf.LlamaFileType(int(r.fields["general.file_type"].parts[-1][0])).name)' "$1"
}

# Writes through a temporary name, so an interrupted step is redone next time.
make() {  # make <target> <command...>
  local target=$1; shift
  [ -f "$target" ] && return 0
  "$@" "$target.tmp" && mv "$target.tmp" "$target"
}
q8() { "$quantize" "$1" "$2" Q8_0 >/dev/null; }
hf16() { python3 "$convert" --outtype f16 --outfile "$2" "$1" >/dev/null; }
mmproj16() { python3 "$convert" --mmproj --outtype f16 --outfile "$2" "$1" >/dev/null; }
copy() { cp "$1" "$2"; }

mkdir -p "$out/paddleocr-vl-1.6" "$out/glm-ocr" "$out/hunyuanocr-1.5" "$out/qwen3.5-2b"

# PaddleOCR-VL-1.6: the official GGUF, language part quantized to Q8_0 unless it already is.
src=/hf/paddleocr-vl-1.6
if [ -f "$src/PaddleOCR-VL-1.6-GGUF.gguf" ] && [ -f "$src/PaddleOCR-VL-1.6-GGUF-mmproj.gguf" ]; then
  if [ "$(file_type "$src/PaddleOCR-VL-1.6-GGUF.gguf")" = MOSTLY_Q8_0 ]; then step=copy; else step=q8; fi
  make "$out/paddleocr-vl-1.6/PaddleOCR-VL-1.6-Q8_0.gguf" "$step" "$src/PaddleOCR-VL-1.6-GGUF.gguf"
  make "$out/paddleocr-vl-1.6/PaddleOCR-VL-1.6-GGUF-mmproj.gguf" copy "$src/PaddleOCR-VL-1.6-GGUF-mmproj.gguf"
fi

# GLM-OCR: llama.cpp's own Q8_0 conversion, as published.
for f in GLM-OCR-Q8_0.gguf mmproj-GLM-OCR-Q8_0.gguf; do
  [ -f "/hf/glm-ocr/$f" ] && make "$out/glm-ocr/$f" copy "/hf/glm-ocr/$f"
done

# HunyuanOCR-1.5 and Qwen3.5-2B: converted from the official weights.
for pair in hunyuanocr-1.5:hyocr qwen3.5-2b:Qwen3.5-2B; do
  dir=${pair%%:*} name=${pair##*:}
  ls "/hf/$dir/hf/"*.safetensors >/dev/null 2>&1 || { echo "skip $dir: weights not downloaded"; continue; }
  make "$out/$dir/mmproj-$name-f16.gguf" mmproj16 "/hf/$dir/hf"
  if [ ! -f "$out/$dir/$name-Q8_0.gguf" ]; then
    make "$out/$dir/$name-f16.gguf" hf16 "/hf/$dir/hf"
    make "$out/$dir/$name-Q8_0.gguf" q8 "$out/$dir/$name-f16.gguf"
  fi
  rm -f "$out/$dir/$name-f16.gguf"
done

for f in "$out"/*/*.gguf; do printf '%-50s %s\n' "${f#"$out"/}" "$(file_type "$f")"; done
(cd "$out" && sha256sum ./*/*.gguf) > "$out/SHA256SUMS"
