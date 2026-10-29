#!/usr/bin/env bash
# Runs as root inside the disposable systemd container (anas-systemd:trixie): two members
# and their spaces made the way the Host Agent makes them (assistant-layout.sh spaces), the
# Hermes runtime, then the assistant itself (assistant-install.sh) and the probe's files.
# ai/assistant-spike/system-test.sh does the same on the real installer's accounts.
set -euo pipefail
export DEBIAN_FRONTEND=noninteractive
# scripts/build-system-test-image.sh now includes them; an image built before needs them.
# The image's own package index is tried first: refreshing it from a slow mirror takes minutes.
command -v socat >/dev/null && command -v rg >/dev/null && command -v tini >/dev/null ||
  apt-get install -y -qq socat ripgrep tini >/dev/null 2>&1 ||
  { apt-get update -qq && apt-get install -y -qq socat ripgrep tini >/dev/null; }
mkdir -p /opt/a-nas/assistant-runtimes
tar -C /opt/a-nas/assistant-runtimes -xzf /spike-out/runtime.tar.gz

# Accounts as the Host Agent creates them (ADR 0008): the fixed groups, then each account
# with its own same-named group and a-nas-users.
groupadd -g 20000 a-nas-users
groupadd -g 20001 a-nas-admins
for u in alice:20100 bob:20101; do
  name=${u%%:*} id=${u##*:}
  groupadd -g "$id" "$name"
  useradd -u "$id" -g "$id" -G a-nas-users -M -d /nonexistent -s /usr/sbin/nologin "$name"
done

d=/srv/a-nas/data
install -d -m 0755 /srv/a-nas "$d"
bash /spike/assistant-layout.sh spaces "$d" alice bob
install -d -o root -g root -m 0700 "$d/.a-nas-snapshots/snap-1"
echo snapshot-copy > "$d/.a-nas-snapshots/snap-1/secret.txt"

product=()
if [[ -n "${SPIKE_PRODUCT_UID:-}" ]]; then
  # The UI prototype's product service runs on the host as this UID.
  useradd -u "$SPIKE_PRODUCT_UID" -M -d /nonexistent -s /usr/sbin/nologin anas-product
  product=(PRODUCT_USER=anas-product)
fi
if [[ -x /spike-photos/photos-spike ]]; then
  # The photo service as its own identity (ADR 0011) with its assistant socket (#113),
  # seeded with photos for alice, bob and the shared library. Its socket directories are
  # private to it; the assistant's unit binds the socket itself into the sandbox. systemd
  # holds the assistant socket, so it outlives restarts of the photo service and the
  # copies bound into running sandboxes keep working.
  useradd -r -u 31000 -U -M -d /nonexistent -s /usr/sbin/nologin a-nas-photos
  install -d -o a-nas-photos -g a-nas-photos -m 0700 /var/lib/photos-spike /run/a-nas-photos /run/a-nas-photos-assistant
  env=()
  if [[ -d /spike-models ]]; then
    # The real AI Worker under the photo service's identity, started by its socket as in
    # A-NAS, with the runtime pinned by hash; Apple-silicon VMs are arm64 (ai-runtime-arm64.lock).
    lock=/spike-ai/requirements.lock
    [[ $(uname -m) == aarch64 ]] && lock=/spike/ai-runtime-arm64.lock
    apt-get update -qq && apt-get install -y -qq --no-install-recommends python3-venv libegl1 libgles2 >/dev/null
    python3 -m venv /opt/photos-spike-ai
    /opt/photos-spike-ai/bin/pip install --quiet --resume-retries 20 --timeout 60 --no-deps --require-hashes \
      --only-binary=:all: -r "$lock"
    install -d -o a-nas-photos -g a-nas-photos -m 0700 /var/lib/photos-spike-ai
    cat >/etc/systemd/system/photos-spike-ai.socket <<'UNIT'
[Socket]
ListenStream=/run/photos-spike-ai/ai.sock
SocketUser=a-nas-photos
SocketGroup=a-nas-photos
SocketMode=0600
UNIT
    cat >/etc/systemd/system/photos-spike-ai.service <<'UNIT'
[Service]
User=a-nas-photos
Group=a-nas-photos
WorkingDirectory=/spike-ai
Environment=PYTHONDONTWRITEBYTECODE=1
ExecStart=/opt/photos-spike-ai/bin/python -m anas_ai.worker --cache-dir /var/lib/photos-spike-ai --model /spike-models/embeddinggemma-2-740m/embeddinggemma-2-740m.litertlm --manifest /spike-manifests/embeddinggemma-2-740m.json
UNIT
    systemctl daemon-reload
    systemctl start photos-spike-ai.socket
    env=(Environment=PHOTOS_SPIKE_AI_SOCKET=/run/photos-spike-ai/ai.sock)
    if [[ -d /spike-import ]]; then env+=(Environment=PHOTOS_SPIKE_IMPORT=/spike-import); fi
  fi
  cat >/etc/systemd/system/photos-spike-assistant.socket <<'UNIT'
[Socket]
ListenStream=/run/a-nas-photos-assistant/assistant.sock
SocketMode=0666
DirectoryMode=0700
FileDescriptorName=assistant
Service=photos-spike.service
UNIT
  cat >/etc/systemd/system/photos-spike.service <<'UNIT'
[Unit]
Requires=photos-spike-assistant.socket
After=photos-spike-assistant.socket

[Service]
User=a-nas-photos
Group=a-nas-photos
Sockets=photos-spike-assistant.socket
ExecStart=/spike-photos/photos-spike /var/lib/photos-spike /run/a-nas-photos/photos.sock /run/a-nas-photos-assistant/assistant.sock
UNIT
  printf '%s\n' "${env[@]}" >>/etc/systemd/system/photos-spike.service
  systemctl daemon-reload
  systemctl start photos-spike-assistant.socket photos-spike.service
  for _ in $(seq 1 240); do
    journalctl -u photos-spike -o cat --no-pager | grep -q photos-ready && break
    systemctl is-active -q photos-spike || { journalctl -u photos-spike -o cat --no-pager | tail -5; exit 1; }
    sleep 1
  done
  if [[ -d /spike-models && -d /spike-video-index ]]; then
    # Video moments (#322): videos_spike.py stands in for the photo service's video moments,
    # under its identity so that it reaches the AI Worker, with the set's index and videos
    # (run.sh SPIKE_VIDEOS); FFmpeg cuts the clips.
    command -v ffmpeg >/dev/null || apt-get install -y -qq --no-install-recommends ffmpeg >/dev/null 2>&1 ||
      { apt-get update -qq && apt-get install -y -qq --no-install-recommends ffmpeg >/dev/null; }
    install -d -o a-nas-photos -g a-nas-photos -m 0700 /var/lib/videos-spike /run/a-nas-videos-assistant
    cat >/etc/systemd/system/videos-spike.service <<'UNIT'
[Service]
User=a-nas-photos
Group=a-nas-photos
Environment=PYTHONPATH=/spike-ai PYTHONDONTWRITEBYTECODE=1
ExecStart=/opt/photos-spike-ai/bin/python /spike/videos_spike.py --socket /run/a-nas-videos-assistant/videos.sock --index /spike-video-index --videos /spike-video-files --ai-socket /run/photos-spike-ai/ai.sock --work /var/lib/videos-spike
UNIT
    # Like the photo socket, it reaches a sandbox only bound over an empty file; a drop-in,
    # so the assistant's own unit stays as the trials install it.
    install -d -m 0755 /run/anas-assistant-videos && : > /run/anas-assistant-videos/videos.sock
    mkdir -p /etc/systemd/system/anas-assistant@.service.d
    printf '[Service]\nBindReadOnlyPaths=-/run/a-nas-videos-assistant/videos.sock:/run/anas-assistant-videos/videos.sock\n' \
      >/etc/systemd/system/anas-assistant@.service.d/videos.conf
    systemctl daemon-reload
    systemctl start videos-spike.service
    for _ in $(seq 1 120); do
      journalctl -u videos-spike -o cat --no-pager | grep -q videos-ready && break
      systemctl is-active -q videos-spike || { journalctl -u videos-spike -o cat --no-pager | tail -5; exit 1; }
      sleep 1
    done
  fi
fi
env "${product[@]}" bash /spike/assistant-install.sh alice bob
bash /spike/seed.sh "$d"
echo setup-done
