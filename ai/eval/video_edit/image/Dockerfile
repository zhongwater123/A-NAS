# The harness's own Python packages over the Debian snapshot base that
# build.sh assembles. Plans install theirs into /env with their own setup.
ARG BASE
FROM ${BASE}
COPY requirements.lock /opt/harness-requirements.lock
RUN python3 -m venv /opt/harness \
 && /opt/harness/bin/pip install --no-cache-dir --no-deps --require-hashes --only-binary=:all: -r /opt/harness-requirements.lock
ENV LANG=C.UTF-8 PYTHONDONTWRITEBYTECODE=1
CMD ["/bin/bash"]
