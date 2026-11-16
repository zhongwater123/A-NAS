# Debian 13 development image for the AI Worker with MediaPipe, built on the
# system test image (scripts/build-system-test-image.sh):
#   docker build -t anas-ai-dev:trixie -f ai/dev.Dockerfile ai
# MediaPipe's C library links libEGL and libGLESv2 even on the CPU; the
# dispatch libraries suffice, without GPU drivers.
FROM anas-systemd:trixie
RUN apt-get update && apt-get install -y --no-install-recommends python3-venv libegl1 libgles2 && rm -rf /var/lib/apt/lists/*
# The GUI build of OpenCV needs X libraries; a server uses the headless one.
RUN python3 -m venv /opt/anas-ai \
 && /opt/anas-ai/bin/pip install --no-cache-dir mediapipe==1.1.0 \
 && /opt/anas-ai/bin/pip uninstall -y opencv-contrib-python \
 && /opt/anas-ai/bin/pip install --no-cache-dir opencv-contrib-python-headless==5.0.0.93
# The object detector (docs/architecture/photo-objects.md).
RUN /opt/anas-ai/bin/pip install --no-cache-dir onnxruntime==1.31.0 protobuf==7.36.2
# The aesthetic scorer (docs/architecture/photo-ai.md#美学打分): PyTorch's CPU
# build, from its own index as ai/requirements.in names it.
RUN /opt/anas-ai/bin/pip install --no-cache-dir --index-url https://download.pytorch.org/whl/cpu \
      torch==2.8.0+cpu torchvision==0.23.0+cpu \
 && /opt/anas-ai/bin/pip install --no-cache-dir transformers==5.20.2 safetensors==0.8.0 pydantic==2.13.4
# Label calibration on the development machine only (ai/eval).
RUN /opt/anas-ai/bin/pip install --no-cache-dir scikit-learn==1.7.2
ENTRYPOINT []
CMD ["/bin/bash"]
