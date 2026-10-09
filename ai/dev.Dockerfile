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
# Label calibration on the development machine only (ai/eval).
RUN /opt/anas-ai/bin/pip install --no-cache-dir scikit-learn==1.7.2
ENTRYPOINT []
CMD ["/bin/bash"]
