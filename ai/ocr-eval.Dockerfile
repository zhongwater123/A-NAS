# Debian 13 image for the OCR model evaluation on the development machine
# (docs/research/photo-ocr-model-evaluation.md), built on the system test
# image (scripts/build-system-test-image.sh):
#   docker build -t anas-ocr-eval:trixie -f ai/ocr-eval.Dockerfile ai
# It builds llama.cpp from a mounted checkout, converts official weights to
# GGUF and drives llama-server. The Python pins follow llama.cpp's own
# requirements/requirements-convert_hf_to_gguf.txt at the evaluated commit.
FROM anas-systemd:trixie
RUN apt-get update && apt-get install -y --no-install-recommends \
      build-essential cmake git python3-venv python3-dev libgomp1 ca-certificates curl \
 && rm -rf /var/lib/apt/lists/*
RUN python3 -m venv /opt/ocr \
 && /opt/ocr/bin/pip install --no-cache-dir --extra-index-url https://download.pytorch.org/whl/cpu \
      torch==2.11.0+cpu transformers==4.57.6 numpy==2.2.6 "sentencepiece>=0.1.98,<0.3.0" "protobuf>=4.21.0,<5.0.0" \
      tqdm pyyaml requests \
 && /opt/ocr/bin/pip install --no-cache-dir pillow==12.0.0 rapidfuzz==3.14.3
# CC-OCR's own KIE and OCR evaluators (MIT), fetched with the dataset.
RUN /opt/ocr/bin/pip install --no-cache-dir zss==1.2.0 nltk==3.10.3 tabulate==0.9.0
ENV PATH=/opt/ocr/bin:$PATH
ENTRYPOINT []
CMD ["/bin/bash"]
