# OCR models through their authors' own transformers pipeline on the
# development machine's CPU, next to the llama.cpp evaluation
# (docs/research/photo-ocr-model-evaluation.md), built on its image:
#   docker build -t anas-ocr-hf:trixie -f ai/ocr-transformers.Dockerfile ai
# The pins are the "verified combination" on Xiaomi-OCR-0's model card.
FROM anas-ocr-eval:trixie
RUN python3 -m venv /opt/hf \
 && /opt/hf/bin/pip install --no-cache-dir --extra-index-url https://download.pytorch.org/whl/cpu \
      torch==2.14.0+cpu torchvision==0.29.0+cpu transformers==5.17.0 pillow==12.0.0
ENV PATH=/opt/hf/bin:$PATH
