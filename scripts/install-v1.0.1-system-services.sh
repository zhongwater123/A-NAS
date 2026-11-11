#!/usr/bin/env bash
set -euo pipefail

if [[ $EUID -ne 0 ]]; then
  echo "run as root" >&2
  exit 1
fi
if [[ $# -ne 3 ]]; then
  echo "usage: $0 SOURCE_RELEASE RELEASE_ID SMB_INTERFACE" >&2
  exit 2
fi

source_release=$(readlink -f -- "$1")
release_id=$2
smb_interface=$3
case "$release_id" in
  *[!A-Za-z0-9._-]*|'') echo "RELEASE_ID contains unsupported characters" >&2; exit 2 ;;
esac
case "$smb_interface" in
  *[!A-Za-z0-9_.:-]*|'') echo "SMB_INTERFACE contains unsupported characters" >&2; exit 2 ;;
esac
if [[ ! -d "$source_release" ]]; then
  echo "source release is not a directory: $source_release" >&2
  exit 2
fi
for binary in anas-api anas-host-agent install-media-pool.sh install-screensavers.sh install-ffmpeg-units.sh; do
  if [[ ! -f "$source_release/$binary" || ! -x "$source_release/$binary" ]]; then
    echo "missing executable: $source_release/$binary" >&2
    exit 2
  fi
done
for unit in anas-api-system.service anas-host-agent-system.service anas-photos-system.service anas-shares-system.service anas-family-system.service anas-ffmpeg-system.socket anas-ffmpeg-system@.service anas-ffmpeg-system.slice; do
  if [[ ! -f "$source_release/$unit" ]]; then
    echo "missing system unit: $source_release/$unit" >&2
    exit 2
  fi
done
if [[ ! -f "$source_release/a-nas-chromium-policy.json" ]]; then
  echo "missing Chromium policy: $source_release/a-nas-chromium-policy.json" >&2
  exit 2
fi
if [[ ! -f "$source_release/Caddyfile" ]]; then
  echo "missing LAN Web entry configuration: $source_release/Caddyfile" >&2
  exit 2
fi
for file in Caddyfile.internet anas-internet.nft anas-internet-firewall-system.service; do
  if [[ ! -f "$source_release/$file" ]]; then
    echo "missing internet entry file: $source_release/$file" >&2
    exit 2
  fi
done
# The local console runs as an account of its own from the installed release
# (issue #174); enabling it stays a runbook step (operate-local-kiosk.md).
for file in kiosk-launcher anas-kiosk@.service a-nas-kiosk.pam a-nas-kiosk.sysusers; do
  if [[ ! -f "$source_release/$file" ]]; then
    echo "missing local console file: $source_release/$file" >&2
    exit 2
  fi
done
# Local AI is part of every release (ADR 0016): the Worker's code, its units,
# and manifests that pin its models and Python runtime by content. Each model
# has a role: its manifest is ai/ROLE.json and the release links its file as
# ai/ROLE.EXTENSION, where the Worker's unit looks for it.
ai_roles=(model:litertlm detector:onnx face-detector:onnx face-embedder:onnx animal-embedder:onnx ocr-model:gguf ocr-mmproj:gguf)
# The text reader's llama-server (docs/architecture/photo-ocr.md) is pinned
# by ai/llama.json and kept like Qdrant below.
for file in anas-system.slice anas-ai-system.socket anas-ai-system.service ai/anas_ai/worker.py ai/runtime.json ai/llama.json; do
  if [[ ! -f "$source_release/$file" ]]; then
    echo "missing local AI file: $source_release/$file" >&2
    exit 2
  fi
done
for role in "${ai_roles[@]}"; do
  if [[ ! -f "$source_release/ai/${role%%:*}.json" ]]; then
    echo "missing local AI file: $source_release/ai/${role%%:*}.json" >&2
    exit 2
  fi
done
# The vector index (ADR 0028): the Qdrant release its manifest pins by
# content, the settings every instance shares, and the scripts its unit runs.
for file in anas-vectors-system@.service anas-vectors-proxy-system@.socket anas-vectors-proxy-system@.service \
  anas-vectors-system.slice vectors/qdrant.json vectors/config.yaml vectors/start.sh vectors/wait-ready.sh; do
  if [[ ! -f "$source_release/$file" ]]; then
    echo "missing vector index file: $source_release/$file" >&2
    exit 2
  fi
done
# Web search (ADR 0030): SearXNG's settings and pin, its runtime's manifest,
# the DNS helper its units run, the units and the service's account.
for file in anas-search-system.service anas-search-dns-system.service anas-search-dns-system.path a-nas-search.sysusers \
  search/settings.yml search/searxng.json search/runtime.json search/allow-dns.sh search/anas_search.py; do
  if [[ ! -f "$source_release/$file" ]]; then
    echo "missing web search file: $source_release/$file" >&2
    exit 2
  fi
done
# The Debian packages the release runs on come with its a-nas-host (ADR 0031),
# which dpkg keeps whole: the release only checks that this one is installed,
# and that a local console's a-nas-console, when installed, is this release's.
for package in a-nas-host a-nas-console; do
  if [[ ! -f "$source_release/$package.deb" ]]; then
    echo "missing host package: $source_release/$package.deb" >&2
    exit 2
  fi
done
host_package_version=$(dpkg-deb --field "$source_release/a-nas-host.deb" Version)
for package in a-nas-host a-nas-console; do
  installed=$(dpkg-query --show --showformat='${db:Status-Status} ${Version}' "$package" 2>/dev/null || true)
  # A device without a screen has no a-nas-console, or only its configuration.
  if [[ "$package" == a-nas-console ]]; then
    case "${installed%% *}" in '' | not-installed | config-files) continue ;; esac
  fi
  if [[ "$installed" != "installed $host_package_version" ]]; then
    echo "this release needs $package $host_package_version; install it first: apt install --no-install-recommends --allow-downgrades $source_release/$package.deb" >&2
    exit 3
  fi
done

# manifest_field FILE KEY: one field of a JSON manifest.
manifest_field() { python3 -c 'import json, sys; print(json.load(open(sys.argv[1], encoding="utf-8"))[sys.argv[2]])' "$1" "$2"; }
declare -A model_sha model_size model_file model_path
for role in "${ai_roles[@]}"; do
  name=${role%%:*}
  model_sha[$name]=$(manifest_field "$source_release/ai/$name.json" sha256)
  model_size[$name]=$(manifest_field "$source_release/ai/$name.json" sizeBytes)
  model_file[$name]=$(manifest_field "$source_release/ai/$name.json" file)
  if [[ ! "${model_sha[$name]}" =~ ^[0-9a-f]{64}$ || ! "${model_size[$name]}" =~ ^[0-9]+$ ||
    ! "${model_file[$name]}" =~ ^[A-Za-z0-9][A-Za-z0-9._-]*$ ]]; then
    echo "invalid $name manifest: $source_release/ai/$name.json" >&2
    exit 2
  fi
done
# Each Python runtime (ADR 0016, ADR 0030) is the release's NAME/runtime.json
# and NAME/runtime.tar.gz, kept once by content in /opt/a-nas/NAME-runtimes/ID.
system_python=$(python3 -c 'import sys; print("%d.%d" % sys.version_info[:2])')
declare -A runtime_sha runtime_path
# check_runtime NAME LABEL: the manifest is valid, the runtime fits this
# system's Python, and it is installed or staged.
check_runtime() {
  local name=$1 label=$2 manifest=$source_release/$1/runtime.json id python
  id=$(manifest_field "$manifest" id)
  runtime_sha[$name]=$(manifest_field "$manifest" sha256)
  python=$(manifest_field "$manifest" python)
  if [[ ! "$id" =~ ^[0-9a-f]{16}$ || ! "${runtime_sha[$name]}" =~ ^[0-9a-f]{64}$ ]]; then
    echo "invalid $label runtime manifest: $manifest" >&2
    exit 2
  fi
  if [[ "$system_python" != "$python" ]]; then
    echo "the $label runtime needs Python $python; this system has $system_python" >&2
    exit 3
  fi
  runtime_path[$name]=/opt/a-nas/$name-runtimes/$id
  if [[ ! -d "${runtime_path[$name]}" && ! -f "$source_release/$name/runtime.tar.gz" ]]; then
    echo "missing $label runtime: neither ${runtime_path[$name]} nor $source_release/$name/runtime.tar.gz" >&2
    exit 2
  fi
}
check_runtime ai AI
check_runtime search 'web search'
# Each model and runtime is kept once by content and linked from every release
# that pins it, so an upgrade or rollback copies neither again.
for role in "${ai_roles[@]}"; do
  name=${role%%:*}
  model_path[$name]=/opt/a-nas/models/${model_sha[$name]}/${model_file[$name]}
  if [[ ! -f "${model_path[$name]}" && ! -f "$source_release/ai/$name.${role#*:}" ]]; then
    echo "missing $name: neither ${model_path[$name]} nor $source_release/ai/$name.${role#*:}" >&2
    exit 2
  fi
done
# Qdrant, like the models, is kept once by content: the official tarball is
# checked, and only its binary is kept, under the tarball's SHA-256.
qdrant_sha=$(manifest_field "$source_release/vectors/qdrant.json" sha256)
qdrant_size=$(manifest_field "$source_release/vectors/qdrant.json" sizeBytes)
qdrant_binary=$(manifest_field "$source_release/vectors/qdrant.json" binary)
if [[ ! "$qdrant_sha" =~ ^[0-9a-f]{64}$ || ! "$qdrant_size" =~ ^[0-9]+$ ||
  ! "$qdrant_binary" =~ ^[A-Za-z0-9][A-Za-z0-9._-]*$ ]]; then
  echo "invalid Qdrant manifest: $source_release/vectors/qdrant.json" >&2
  exit 2
fi
qdrant_path=/opt/a-nas/qdrant/$qdrant_sha/qdrant
if [[ ! -f "$qdrant_path" && ! -f "$source_release/vectors/qdrant.tar.gz" ]]; then
  echo "missing Qdrant: neither $qdrant_path nor $source_release/vectors/qdrant.tar.gz" >&2
  exit 2
fi
# So is llama.cpp: only the members its manifest names, llama-server with
# its libraries and licence, are kept, together under the tarball's SHA-256.
llama_sha=$(manifest_field "$source_release/ai/llama.json" sha256)
llama_size=$(manifest_field "$source_release/ai/llama.json" sizeBytes)
llama_directory=$(manifest_field "$source_release/ai/llama.json" directory)
llama_binary=$(manifest_field "$source_release/ai/llama.json" binary)
mapfile -t llama_members < <(python3 -c 'import json, sys; print("\n".join(json.load(open(sys.argv[1], encoding="utf-8"))["members"]))' \
  "$source_release/ai/llama.json")
if [[ ! "$llama_sha" =~ ^[0-9a-f]{64}$ || ! "$llama_size" =~ ^[0-9]+$ ||
  ! "$llama_directory" =~ ^[A-Za-z0-9][A-Za-z0-9._-]*$ || ! "$llama_binary" =~ ^[A-Za-z0-9][A-Za-z0-9._-]*$ ||
  ${#llama_members[@]} -eq 0 ]]; then
  echo "invalid llama.cpp manifest: $source_release/ai/llama.json" >&2
  exit 2
fi
for member in "${llama_members[@]}"; do
  if [[ ! "$member" =~ ^[A-Za-z0-9][A-Za-z0-9._*-]*$ ]]; then
    echo "invalid llama.cpp manifest: $source_release/ai/llama.json" >&2
    exit 2
  fi
done
llama_path=/opt/a-nas/llama/$llama_sha
if [[ ! -f "$llama_path/$llama_binary" && ! -f "$source_release/ai/llama.tar.gz" ]]; then
  echo "missing llama.cpp: neither $llama_path nor $source_release/ai/llama.tar.gz" >&2
  exit 2
fi
# The LAN Web entry is optional: installing Caddy enables it (ADR 0012).
# Validate before changing anything, so a bad configuration stops the upgrade.
lan_entry=false
if command -v caddy >/dev/null; then
  caddy validate --adapter caddyfile --config "$source_release/Caddyfile" >/dev/null 2>&1 ||
    { echo "LAN Web entry configuration does not validate: $source_release/Caddyfile" >&2; exit 2; }
  lan_entry=true
fi
# The internet entry is optional too (ADR 0032): /etc/a-nas/internet.env turns
# it on. Read the file without running it, check each value and what the entry
# needs from the host, and validate Caddy's configuration, all before
# changing anything. Removing the file turns the entry off again.
internet_env=/etc/a-nas/internet.env
internet_entry=false
if [[ -e "$internet_env" ]]; then
  declare -A internet=()
  while IFS= read -r line || [[ -n "$line" ]]; do
    case "$line" in '' | '#'*) continue ;; esac
    case "${line%%=*}" in
      ANAS_INTERNET_HOST | ANAS_INTERNET_RELAY_ENDPOINT | ANAS_INTERNET_RELAY_PUBLIC_KEY | ANAS_INTERNET_TUNNEL_PREFIX)
        internet[${line%%=*}]=${line#*=} ;;
      *) echo "$internet_env: unknown line: ${line%%=*}" >&2; exit 2 ;;
    esac
  done < "$internet_env"
  internet_host=${internet[ANAS_INTERNET_HOST]:-}
  relay_endpoint=${internet[ANAS_INTERNET_RELAY_ENDPOINT]:-}
  relay_key=${internet[ANAS_INTERNET_RELAY_PUBLIC_KEY]:-}
  # A domain in lower-case ASCII, as browsers send it; an internationalized
  # name is written in its xn-- form.
  if [[ ! "$internet_host" =~ ^([a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?\.)+[a-z]([a-z0-9-]{0,61}[a-z0-9])?$ ]]; then
    echo "$internet_env: ANAS_INTERNET_HOST must be the entry's domain in lower case, such as nas.example.com" >&2
    exit 2
  fi
  if [[ ! "$relay_key" =~ ^[A-Za-z0-9+/]{42}[AEIMQUYcgkosw048]=$ ]]; then
    echo "$internet_env: ANAS_INTERNET_RELAY_PUBLIC_KEY must be the relay's WireGuard public key" >&2
    exit 2
  fi
  # The tunnel's addresses come from a unique local /64: the relay is ::1,
  # this NAS ::2.
  if ! tunnel=$(python3 -I - "${internet[ANAS_INTERNET_TUNNEL_PREFIX]:-}" "$relay_endpoint" <<'PY'
import ipaddress
import sys

try:
    prefix = ipaddress.IPv6Network(sys.argv[1])
    host, colon, port = sys.argv[2].rpartition(":")
    ipaddress.ip_address(host.removeprefix("[").removesuffix("]"))
except ValueError:
    sys.exit(1)
if prefix.prefixlen != 64 or not prefix.subnet_of(ipaddress.IPv6Network("fd00::/8")):
    sys.exit(1)
if not colon or not port.isdigit() or not 1 <= int(port) <= 65535:
    sys.exit(1)
print(prefix[1], prefix[2])
PY
  ); then
    echo "$internet_env: ANAS_INTERNET_TUNNEL_PREFIX must be a unique local /64 (fdXX:XXXX:XXXX:XXXX::/64) and ANAS_INTERNET_RELAY_ENDPOINT the relay's address:port" >&2
    exit 2
  fi
  read -r relay_address tunnel_address <<<"$tunnel"
  # grep reads the whole list: stopping at the match would leave caddy
  # writing to a closed pipe, which pipefail reports as a failure.
  if [[ "$lan_entry" != true ]] || ! caddy list-modules 2>/dev/null | grep -x 'caddy.listeners.proxy_protocol' >/dev/null; then
    echo "the internet entry needs Caddy 2.7 or newer from Caddy's own repository; install it first (docs/runbooks/enable-lan-web-access.md)" >&2
    exit 3
  fi
  for command in wg wg-quick nft; do
    if ! command -v "$command" >/dev/null; then
      echo "the internet entry needs $command; install it first: apt install --no-install-recommends wireguard-tools nftables" >&2
      exit 3
    fi
  done
  internet_caddyfile() {
    sed -e "s|@INTERNET_HOST@|$internet_host|g" -e "s|@TUNNEL_ADDRESS@|$tunnel_address|g" \
      -e "s|@RELAY_ADDRESS@|$relay_address|g" "$source_release/Caddyfile.internet"
  }
  caddy validate --adapter caddyfile --config <(internet_caddyfile) >/dev/null 2>&1 ||
    { echo "internet entry configuration does not validate: $source_release/Caddyfile.internet with $internet_env" >&2; exit 2; }
  nft --check --file "$source_release/anas-internet.nft" ||
    { echo "internet entry firewall table does not load: $source_release/anas-internet.nft" >&2; exit 2; }
  internet_entry=true
fi
# The Host Agent moves each of its records out of /var/lib/a-nas when it
# starts, but only a regular file with one link that root owns (issue #176):
# any other record would be unusable after the restart. Stop before changing
# anything; the provisioning runbook says how to check such a file.
for record in space-registry identity-registry viewing-grants app-identities volume-operations; do
  legacy=/var/lib/a-nas/$record.json
  [[ -e "$legacy" || -L "$legacy" ]] || continue
  [[ -e "/var/lib/a-nas-host/$record.json" || -L "/var/lib/a-nas-host/$record.json" ]] && continue
  details=$(stat -c '%u %h %F' -- "$legacy")
  case "$details" in
    '0 1 regular file' | '0 1 regular empty file') ;;
    *)
      echo "$legacy is not a regular file with one link owned by root ($details), so the Host Agent would not move it; check it as the provisioning runbook describes, then run the installer again" >&2
      exit 3
      ;;
  esac
done

getent group a-nas >/dev/null || groupadd --system a-nas
if ! id a-nas >/dev/null 2>&1; then
  useradd --system --gid a-nas --home-dir /var/lib/a-nas --shell /usr/sbin/nologin a-nas
fi
# The photo service owns the photos subvolume under a fixed UID/GID, so a
# reinstalled system disk owns the same photos again (ADR 0011). A-NAS never
# adopts an account or ID it did not create.
photos_id=31000
if getent group a-nas-photos >/dev/null; then
  [[ "$(getent group a-nas-photos | cut -d: -f3)" == "$photos_id" ]] || { echo "group a-nas-photos exists with another GID" >&2; exit 3; }
elif getent group "$photos_id" >/dev/null; then
  echo "GID $photos_id is taken by another group" >&2; exit 3
else
  groupadd --system --gid "$photos_id" a-nas-photos
fi
if getent passwd a-nas-photos >/dev/null; then
  [[ "$(id -u a-nas-photos):$(id -g a-nas-photos)" == "$photos_id:$photos_id" ]] || { echo "user a-nas-photos exists with another UID or GID" >&2; exit 3; }
elif getent passwd "$photos_id" >/dev/null; then
  echo "UID $photos_id is taken by another user" >&2; exit 3
else
  useradd --system --uid "$photos_id" --gid "$photos_id" --no-create-home --home-dir /nonexistent --shell /usr/sbin/nologin a-nas-photos
fi
shares_id=31001
if getent group a-nas-shares >/dev/null; then
  [[ "$(getent group a-nas-shares | cut -d: -f3)" == "$shares_id" ]] || { echo "group a-nas-shares exists with another GID" >&2; exit 3; }
elif getent group "$shares_id" >/dev/null; then
  echo "GID $shares_id is taken by another group" >&2; exit 3
else
  groupadd --system --gid "$shares_id" a-nas-shares
fi
if getent passwd a-nas-shares >/dev/null; then
  [[ "$(id -u a-nas-shares):$(id -g a-nas-shares)" == "$shares_id:$shares_id" ]] || { echo "user a-nas-shares exists with another UID or GID" >&2; exit 3; }
elif getent passwd "$shares_id" >/dev/null; then
  echo "UID $shares_id is taken by another user" >&2; exit 3
else
  useradd --system --uid "$shares_id" --gid "$shares_id" --no-create-home --home-dir /nonexistent --shell /usr/sbin/nologin a-nas-shares
fi
# The family service owns the family store, the family center's records and
# recordings, under a fixed UID/GID like the photo service (ADR 0029).
family_id=31002
if getent group a-nas-family >/dev/null; then
  [[ "$(getent group a-nas-family | cut -d: -f3)" == "$family_id" ]] || { echo "group a-nas-family exists with another GID" >&2; exit 3; }
elif getent group "$family_id" >/dev/null; then
  echo "GID $family_id is taken by another group" >&2; exit 3
else
  groupadd --system --gid "$family_id" a-nas-family
fi
if getent passwd a-nas-family >/dev/null; then
  [[ "$(id -u a-nas-family):$(id -g a-nas-family)" == "$family_id:$family_id" ]] || { echo "user a-nas-family exists with another UID or GID" >&2; exit 3; }
elif getent passwd "$family_id" >/dev/null; then
  echo "UID $family_id is taken by another user" >&2; exit 3
else
  useradd --system --uid "$family_id" --gid "$family_id" --no-create-home --home-dir /nonexistent --shell /usr/sbin/nologin a-nas-family
fi
# The local console's account owns no data, so its ID may differ between
# installs; it must not be able to log in.
install -d -o root -g root -m 0755 /etc/sysusers.d
install -o root -g root -m 0644 "$source_release/a-nas-kiosk.sysusers" /etc/sysusers.d/a-nas-kiosk.conf
systemd-sysusers /etc/sysusers.d/a-nas-kiosk.conf
if [[ "$(getent passwd a-nas-kiosk | cut -d: -f6-7)" != /nonexistent:/usr/sbin/nologin ]]; then
  echo "user a-nas-kiosk exists with a home or shell A-NAS did not give it" >&2
  exit 3
fi
# The web search service's account owns no data either; the Product Service
# joins its group to reach the service's socket.
install -o root -g root -m 0644 "$source_release/a-nas-search.sysusers" /etc/sysusers.d/a-nas-search.conf
systemd-sysusers /etc/sysusers.d/a-nas-search.conf
if [[ "$(getent passwd a-nas-search | cut -d: -f6-7)" != /nonexistent:/usr/sbin/nologin ]]; then
  echo "user a-nas-search exists with a home or shell A-NAS did not give it" >&2
  exit 3
fi
# setgid makes the Product-owned bridge socket inherit the shares-only group.
install -d -o root -g root -m 0755 /etc/tmpfiles.d
printf 'd /run/a-nas-share-files 2750 a-nas a-nas-shares -\n' > /etc/tmpfiles.d/a-nas-shares.conf
chmod 0644 /etc/tmpfiles.d/a-nas-shares.conf
systemd-tmpfiles --create /etc/tmpfiles.d/a-nas-shares.conf
install -d -o root -g root -m 0755 /opt/a-nas/releases
install -d -o a-nas -g a-nas -m 0700 /var/lib/a-nas
install -d -o root -g a-nas -m 0750 /var/lib/a-nas/screensavers
# The Host Agent's records (its StateDirectory); it moves those of older
# releases out of /var/lib/a-nas when it starts (issue #176).
install -d -o root -g root -m 0700 /var/lib/a-nas-host
install -d -o root -g a-nas -m 0750 /srv/a-nas /srv/a-nas/data
install -d -o root -g a-nas -m 0750 /etc/a-nas
install -d -o root -g root -m 0755 /etc/samba
install -d -o root -g root -m 0755 /etc/chromium /etc/chromium/policies /etc/chromium/policies/managed
install -d -o root -g root -m 0755 /var/lib/samba /run/samba
install -d -o root -g root -m 0755 /opt/a-nas/models /opt/a-nas/ai-runtimes /opt/a-nas/search-runtimes /opt/a-nas/qdrant /opt/a-nas/llama

# Staged content is copied before it is checked, so the staging account cannot
# change it after the check.
incoming=
trap '[[ -z "$incoming" ]] || rm -rf -- "$incoming" "$incoming.tar.gz"' EXIT
for role in "${ai_roles[@]}"; do
  name=${role%%:*}
  [[ -f "${model_path[$name]}" ]] && continue
  staged="$source_release/ai/$name.${role#*:}"
  incoming=$(mktemp -d /opt/a-nas/models/.incoming-XXXXXX)
  cp -- "$staged" "$incoming/${model_file[$name]}"
  if [[ "$(stat -c %s "$incoming/${model_file[$name]}")" != "${model_size[$name]}" ]] ||
    ! printf '%s  %s\n' "${model_sha[$name]}" "$incoming/${model_file[$name]}" | sha256sum --check --quiet - >/dev/null 2>&1; then
    echo "the staged $name does not match its manifest: $staged" >&2
    exit 4
  fi
  chmod 0644 "$incoming/${model_file[$name]}"
  chmod 0755 "$incoming"
  mv -T -- "$incoming" "/opt/a-nas/models/${model_sha[$name]}"
  incoming=
done
# install_runtime NAME LABEL: unpack the staged runtime unless it is installed.
install_runtime() {
  local name=$1 label=$2
  [[ -d "${runtime_path[$name]}" ]] && return
  incoming=$(mktemp -d "/opt/a-nas/$name-runtimes/.incoming-XXXXXX")
  cp -- "$source_release/$name/runtime.tar.gz" "$incoming.tar.gz"
  if ! printf '%s  %s\n' "${runtime_sha[$name]}" "$incoming.tar.gz" | sha256sum --check --quiet - >/dev/null 2>&1; then
    echo "the staged $label runtime does not match its manifest: $source_release/$name/runtime.tar.gz" >&2
    exit 4
  fi
  tar -C "$incoming" --no-same-owner -xzf "$incoming.tar.gz"
  rm -f -- "$incoming.tar.gz"
  chmod 0755 "$incoming"
  mv -T -- "$incoming" "${runtime_path[$name]}"
  incoming=
}
install_runtime ai AI
install_runtime search 'web search'
if [[ ! -f "$qdrant_path" ]]; then
  incoming=$(mktemp -d /opt/a-nas/qdrant/.incoming-XXXXXX)
  cp -- "$source_release/vectors/qdrant.tar.gz" "$incoming.tar.gz"
  if [[ "$(stat -c %s "$incoming.tar.gz")" != "$qdrant_size" ]] ||
    ! printf '%s  %s\n' "$qdrant_sha" "$incoming.tar.gz" | sha256sum --check --quiet - >/dev/null 2>&1; then
    echo "the staged Qdrant does not match its manifest: $source_release/vectors/qdrant.tar.gz" >&2
    exit 4
  fi
  install -d -m 0700 "$incoming/unpacked"
  tar -C "$incoming/unpacked" --no-same-owner -xzf "$incoming.tar.gz" "$qdrant_binary"
  rm -f -- "$incoming.tar.gz"
  install -o root -g root -m 0755 "$incoming/unpacked/$qdrant_binary" "$incoming/qdrant"
  rm -rf -- "$incoming/unpacked"
  chmod 0755 "$incoming"
  mv -T -- "$incoming" "/opt/a-nas/qdrant/$qdrant_sha"
  incoming=
fi
if [[ ! -f "$llama_path/$llama_binary" ]]; then
  incoming=$(mktemp -d /opt/a-nas/llama/.incoming-XXXXXX)
  cp -- "$source_release/ai/llama.tar.gz" "$incoming.tar.gz"
  if [[ "$(stat -c %s "$incoming.tar.gz")" != "$llama_size" ]] ||
    ! printf '%s  %s\n' "$llama_sha" "$incoming.tar.gz" | sha256sum --check --quiet - >/dev/null 2>&1; then
    echo "the staged llama.cpp does not match its manifest: $source_release/ai/llama.tar.gz" >&2
    exit 4
  fi
  tar -C "$incoming" --no-same-owner --strip-components=1 -xzf "$incoming.tar.gz" --wildcards \
    "${llama_members[@]/#/$llama_directory/}"
  rm -f -- "$incoming.tar.gz"
  chmod -R u=rwX,go=rX "$incoming"
  chmod 0755 "$incoming/$llama_binary"
  mv -T -- "$incoming" "$llama_path"
  incoming=
fi
trap - EXIT

target="/opt/a-nas/releases/$release_id"
if [[ -e "$target" ]]; then
  echo "target release already exists: $target" >&2
  exit 4
fi
temporary="/opt/a-nas/releases/.install-$release_id-$$"
trap 'rm -rf -- "$temporary"' EXIT
install -d -o root -g root -m 0755 "$temporary"
install -o root -g root -m 0755 "$source_release/anas-api" "$temporary/anas-api"
install -o root -g root -m 0755 "$source_release/anas-host-agent" "$temporary/anas-host-agent"
install -o root -g root -m 0755 "$source_release/install-screensavers.sh" "$temporary/install-screensavers.sh"
install -o root -g root -m 0755 "$source_release/install-media-pool.sh" "$temporary/install-media-pool.sh"
install -o root -g root -m 0755 "$source_release/install-ffmpeg-units.sh" "$temporary/install-ffmpeg-units.sh"
install -o root -g root -m 0755 "$source_release/kiosk-launcher" "$temporary/kiosk-launcher"
# A rollback to this release installs its host packages again.
install -o root -g root -m 0644 "$source_release/a-nas-host.deb" "$source_release/a-nas-console.deb" "$temporary/"
install -d -o root -g root -m 0755 "$temporary/ai" "$temporary/ai/anas_ai"
install -o root -g root -m 0644 "$source_release"/ai/anas_ai/*.py "$temporary/ai/anas_ai/"
install -o root -g root -m 0644 "$source_release/ai/runtime.json" "$temporary/ai/"
for role in "${ai_roles[@]}"; do
  name=${role%%:*}
  install -o root -g root -m 0644 "$source_release/ai/$name.json" "$temporary/ai/"
  ln -s -- "${model_path[$name]}" "$temporary/ai/$name.${role#*:}"
done
ln -s -- "${runtime_path[ai]}" "$temporary/ai/runtime"
install -o root -g root -m 0644 "$source_release/ai/llama.json" "$temporary/ai/"
ln -s -- "$llama_path" "$temporary/ai/llama"
# Web search (ADR 0030): SearXNG's settings and pin, the DNS helper its units
# run, and its runtime.
install -d -o root -g root -m 0755 "$temporary/search"
install -o root -g root -m 0644 "$source_release/search/settings.yml" "$source_release/search/searxng.json" \
  "$source_release/search/runtime.json" "$source_release/search/anas_search.py" "$temporary/search/"
install -o root -g root -m 0755 "$source_release/search/allow-dns.sh" "$temporary/search/"
ln -s -- "${runtime_path[search]}" "$temporary/search/runtime"
install -d -o root -g root -m 0755 "$temporary/vectors"
install -o root -g root -m 0644 "$source_release/vectors/qdrant.json" "$source_release/vectors/config.yaml" "$temporary/vectors/"
install -o root -g root -m 0755 "$source_release/vectors/start.sh" "$source_release/vectors/wait-ready.sh" "$temporary/vectors/"
ln -s -- "$qdrant_path" "$temporary/vectors/qdrant"
mv -- "$temporary" "$target"
trap - EXIT

# Screen saver videos are long-lived media, not part of a release (ADR 0014):
# install-media-pool.sh keeps them under /var/lib/a-nas/screensavers, where
# "current" names the pool the Product Service plays whatever the release.
if [[ -d "$source_release/screensavers" ]]; then
  echo "ignoring the release's screen saver videos: install them with install-media-pool.sh screensavers" >&2
fi
# Installers before issue #49 left each pool in a directory named after its
# release and no "current". Adopt the pool installed last, which they played
# unless a release without videos followed.
if [[ ! -e /var/lib/a-nas/screensavers/current && ! -L /var/lib/a-nas/screensavers/current ]]; then
  legacy_pool=$(find /var/lib/a-nas/screensavers -mindepth 1 -maxdepth 1 -type d -name '[A-Za-z0-9_-]*' \
    ! -name objects ! -name pools -printf '%T@ %p\n' | sort -n | tail -n 1 | cut -d ' ' -f 2-)
  if [[ -n "$legacy_pool" ]]; then
    "$target/install-media-pool.sh" screensavers --import "$legacy_pool" ||
      echo "could not adopt the screen saver pool in $legacy_pool; the local console keeps its wallpaper" >&2
  fi
fi

# The administrator terminal stays off until the device's operator turns it on
# (docs/specs/web-terminal.md). This file is rewritten on every release, which
# used to drop that choice, so carry it over.
terminal_enabled=
if grep -qx 'ANAS_TERMINAL=enabled' /etc/a-nas/anas-api.env 2>/dev/null; then
  terminal_enabled=1
fi
# SearXNG's secret signs its thumbnail addresses, and the Product Service, in
# group a-nas, reads it to sign them as well (ADR 0030). It is generated once,
# so upgrades keep it.
if ! grep -Eqx 'SEARXNG_SECRET=[0-9a-f]{64}' /etc/a-nas/search.env 2>/dev/null; then
  printf 'SEARXNG_SECRET=%s\n' "$(od -An -tx1 -N32 /dev/urandom | tr -d ' \n')" > /etc/a-nas/search.env.incoming
  chmod 0600 /etc/a-nas/search.env.incoming
  mv -f -- /etc/a-nas/search.env.incoming /etc/a-nas/search.env
fi
chown root:a-nas /etc/a-nas/search.env
chmod 0640 /etc/a-nas/search.env
printf '%s\n' \
  'ANAS_HTTP_ADDR=127.0.0.1:8080' \
  'ANAS_HOSTSTATE_MODE=agent' \
  'ANAS_HOST_AGENT_SOCKET=/run/a-nas/host-agent.sock' \
  'ANAS_FILE_BROKER_SOCKET=/run/a-nas/file-broker.sock' \
  'ANAS_PHOTOS_SOCKET=/run/a-nas-photos/photos.sock' \
  'ANAS_FFMPEG_SOCKET=/run/a-nas-ffmpeg/ffmpeg.sock' \
  'ANAS_STATE_DIR=/var/lib/a-nas' \
  'ANAS_SCREENSAVER_DIRECTORY=/var/lib/a-nas/screensavers/current' \
  'ANAS_DATA_MOUNT=/srv/a-nas/data' \
  'ANAS_WEB_SEARCH_SOCKET=/run/a-nas-search/search.sock' \
  'ANAS_WEB_SEARCH_RELEASE=/opt/a-nas/current/search' \
  'ANAS_WEB_SEARCH_SECRET_FILE=/etc/a-nas/search.env' > /etc/a-nas/anas-api.env
# Container management is an optional root-installed capability. Preserve it
# across system-service upgrades only when the typed agent socket is present;
# never grant the Product Service direct access to docker.sock.
if [[ -S /run/a-nas-container/agent.sock ]]; then
  printf 'ANAS_CONTAINERS_MODE=agent\n' >> /etc/a-nas/anas-api.env
fi
if [[ -n "$terminal_enabled" ]]; then
  printf 'ANAS_TERMINAL=enabled\n' >> /etc/a-nas/anas-api.env
fi
# The internet entry's own listener, which Caddy's HTTPS site proxies to.
if [[ "$internet_entry" == true ]]; then
  printf 'ANAS_INTERNET_HTTP_ADDR=127.0.0.1:8081\nANAS_INTERNET_HOST=%s\n' "$internet_host" >> /etc/a-nas/anas-api.env
fi
chown root:a-nas /etc/a-nas/anas-api.env
chmod 0640 /etc/a-nas/anas-api.env

printf '%s\n' \
  'ANAS_HOST_AGENT_SOCKET=/run/a-nas/host-agent.sock' \
  'ANAS_FILE_BROKER_SOCKET=/run/a-nas/file-broker.sock' \
  'ANAS_STATE_DIR=/var/lib/a-nas' \
  'ANAS_HOST_AGENT_GROUP=a-nas' \
  'ANAS_PHOTO_SESSION_SOCKET=/run/a-nas-sessions/photos.sock' \
  'ANAS_PHOTO_SESSION_GROUP=a-nas-photos' \
  'ANAS_SHARE_SESSION_SOCKET=/run/a-nas-share-sessions/shares.sock' \
  'ANAS_SHARE_SESSION_GROUP=a-nas-shares' \
  'ANAS_FAMILY_SESSION_SOCKET=/run/a-nas-family-sessions/family.sock' \
  'ANAS_FAMILY_SESSION_GROUP=a-nas-family' \
  'ANAS_DATA_MOUNT=/srv/a-nas/data' \
  "ANAS_SMB_INTERFACE=$smb_interface" > /etc/a-nas/host-agent.env
chown root:root /etc/a-nas/host-agent.env
chmod 0600 /etc/a-nas/host-agent.env
# smbd listens only on the addresses its interfaces have when it starts (bind
# interfaces only), and at boot a DHCP address can arrive after smbd's turn.
# So smbd waits up to 20 s for the SMB interface's IPv4 address; without one,
# the start fails and systemd tries again 5 s later, until it has one. Units
# ordered after smbd, the Host Agent among them, start after the first try.
# Restart= also brings smbd back after a crash
# (docs/investigations/2026-11-04-smbd-starts-before-dhcp-address.md).
install -d -o root -g root -m 0755 /etc/systemd/system/smbd.service.d
cat > /etc/systemd/system/smbd.service.d/a-nas-lan-address.conf <<EOF
# Written by the A-NAS installer for the SMB interface $smb_interface.
[Service]
ExecStartPre=/bin/sh -c 'timeout 20 sh -c "until ip -4 -o address show dev $smb_interface scope global 2>/dev/null | grep -q inet; do sleep 1; done" || { echo "$smb_interface has no IPv4 address after 20 s; smbd waits for one because it listens only on the addresses present when it starts" >&2; exit 1; }'
Restart=on-failure
RestartSec=5s
EOF
chmod 0644 /etc/systemd/system/smbd.service.d/a-nas-lan-address.conf

install -o root -g root -m 0644 "$source_release/anas-host-agent-system.service" /etc/systemd/system/anas-host-agent.service
install -o root -g root -m 0644 "$source_release/anas-api-system.service" /etc/systemd/system/anas-api.service
install -o root -g root -m 0644 "$source_release/anas-photos-system.service" /etc/systemd/system/anas-photos.service
install -o root -g root -m 0644 "$source_release/anas-shares-system.service" /etc/systemd/system/anas-shares.service
install -o root -g root -m 0644 "$source_release/anas-family-system.service" /etc/systemd/system/anas-family.service
# Background work gives way to the device's other work (anas.slice).
install -o root -g root -m 0644 "$source_release/anas-system.slice" /etc/systemd/system/anas.slice
install -o root -g root -m 0644 "$source_release/anas-ai-system.socket" /etc/systemd/system/anas-ai.socket
install -o root -g root -m 0644 "$source_release/anas-ai-system.service" /etc/systemd/system/anas-ai.service
# The AI Worker may use every core but two, which stay free for the user's next
# request (anas-ai.service); the cap follows this machine's cores.
cores=$(nproc)
install -d -o root -g root -m 0755 /etc/systemd/system/anas-ai.service.d
printf '# Written by the A-NAS installer for %s cores: all but two.\n[Service]\nCPUQuota=%s%%\n' \
  "$cores" "$((cores > 2 ? (cores - 2) * 100 : 100))" > /etc/systemd/system/anas-ai.service.d/cpu-reserve.conf
chmod 0644 /etc/systemd/system/anas-ai.service.d/cpu-reserve.conf
# Its memory has no budget: background work loads a model only while 1 GiB
# stays free (anas-ai.service). The cap, all of this machine's memory but
# 2.5 GiB and at least 1 GiB, only stops a runaway.
memory_kib=$(awk '$1 == "MemTotal:" { print $2 }' /proc/meminfo)
printf '# Written by the A-NAS installer for %s KiB of memory: all but 2.5 GiB.\n[Service]\nMemoryMax=%sK\n' \
  "$memory_kib" "$((memory_kib > 3670016 ? memory_kib - 2621440 : 1048576))" \
  > /etc/systemd/system/anas-ai.service.d/memory-reserve.conf
chmod 0644 /etc/systemd/system/anas-ai.service.d/memory-reserve.conf
# One vector index instance per owning service, reached only through its
# proxy socket (ADR 0028).
install -o root -g root -m 0644 "$source_release/anas-vectors-system.slice" /etc/systemd/system/anas-vectors.slice
install -o root -g root -m 0644 "$source_release/anas-vectors-system@.service" /etc/systemd/system/anas-vectors@.service
install -o root -g root -m 0644 "$source_release/anas-vectors-proxy-system@.socket" /etc/systemd/system/anas-vectors-proxy@.socket
install -o root -g root -m 0644 "$source_release/anas-vectors-proxy-system@.service" /etc/systemd/system/anas-vectors-proxy@.service
# Web search, and what lets it reach the DNS servers (ADR 0030).
install -o root -g root -m 0644 "$source_release/anas-search-system.service" /etc/systemd/system/anas-search.service
install -o root -g root -m 0644 "$source_release/anas-search-dns-system.service" /etc/systemd/system/anas-search-dns.service
install -o root -g root -m 0644 "$source_release/anas-search-dns-system.path" /etc/systemd/system/anas-search-dns.path
# A renamed worker must not coexist with old clients using a separate slot
# directory. Stop clients before replacing units; restart after the switch.
for service in anas-api anas-photos anas-shares anas-family; do
  if [[ $(systemctl show -p LoadState --value "$service.service") != not-found ]]; then
    systemctl stop "$service.service"
  fi
done
"$target/install-ffmpeg-units.sh" "$source_release"
install -o root -g root -m 0644 "$source_release/a-nas-chromium-policy.json" /etc/chromium/policies/managed/a-nas.json
# The local console's unit, enabled or not, starts this release's launcher.
install -o root -g root -m 0644 "$source_release/anas-kiosk@.service" /etc/systemd/system/anas-kiosk@.service
install -o root -g root -m 0644 "$source_release/a-nas-kiosk.pam" /etc/pam.d/a-nas-kiosk
if [[ "$lan_entry" == true ]]; then
  # Keep the administrator's own configuration the first time A-NAS replaces it.
  if [[ -f /etc/caddy/Caddyfile && ! -e /etc/caddy/Caddyfile.before-a-nas ]] &&
    ! grep -Fqx '# Managed by A-NAS.' /etc/caddy/Caddyfile; then
    cp -p /etc/caddy/Caddyfile /etc/caddy/Caddyfile.before-a-nas
  fi
  install -d -o root -g root -m 0755 /etc/caddy
  if [[ "$internet_entry" == true ]]; then
    internet_caddyfile > /etc/caddy/Caddyfile.incoming
    chmod 0644 /etc/caddy/Caddyfile.incoming
    mv -f -- /etc/caddy/Caddyfile.incoming /etc/caddy/Caddyfile
  else
    install -o root -g root -m 0644 "$source_release/Caddyfile" /etc/caddy/Caddyfile
  fi
fi
tunnel_changed=
if [[ "$internet_entry" == true ]]; then
  # Caddy binds the tunnel's address even while the tunnel is down, so it
  # starts, LAN entry included, whatever the tunnel does.
  printf '%s\n' '# Written by the A-NAS installer: the internet entry binds its tunnel address' \
    '# before the tunnel is up (ADR 0032).' 'net.ipv6.ip_nonlocal_bind = 1' > /etc/sysctl.d/60-anas-internet.conf
  chmod 0644 /etc/sysctl.d/60-anas-internet.conf
  sysctl --quiet --load=/etc/sysctl.d/60-anas-internet.conf
  install -o root -g root -m 0644 "$source_release/anas-internet.nft" /etc/a-nas/anas-internet.nft
  install -o root -g root -m 0644 "$source_release/anas-internet-firewall-system.service" /etc/systemd/system/anas-internet-firewall.service
  # The tunnel's key is made once and kept, also when the entry is turned off.
  install -d -o root -g root -m 0700 /etc/wireguard
  if [[ ! -s /etc/wireguard/wg-anas.key ]]; then
    (umask 077 && wg genkey > /etc/wireguard/wg-anas.key.incoming)
    mv -f -- /etc/wireguard/wg-anas.key.incoming /etc/wireguard/wg-anas.key
  fi
  printf '%s\n' \
    "# Managed by A-NAS: the internet entry's tunnel to the edge relay (ADR 0032)," \
    "# written from $internet_env. This NAS dials the relay." \
    '[Interface]' \
    "Address = $tunnel_address/64" \
    'PostUp = wg set %i private-key /etc/wireguard/%i.key' \
    '' \
    '[Peer]' \
    "PublicKey = $relay_key" \
    "Endpoint = $relay_endpoint" \
    "AllowedIPs = $relay_address/128" \
    'PersistentKeepalive = 25' > /etc/wireguard/wg-anas.conf.incoming
  chmod 0600 /etc/wireguard/wg-anas.conf.incoming
  if cmp -s /etc/wireguard/wg-anas.conf.incoming /etc/wireguard/wg-anas.conf; then
    rm -f -- /etc/wireguard/wg-anas.conf.incoming
  else
    mv -f -- /etc/wireguard/wg-anas.conf.incoming /etc/wireguard/wg-anas.conf
    tunnel_changed=1
  fi
elif [[ -e /etc/systemd/system/anas-internet-firewall.service || -e /etc/wireguard/wg-anas.conf ]]; then
  # Turned off: undo what an earlier installation set up, keeping the key.
  systemctl disable --now wg-quick@wg-anas.service anas-internet-firewall.service 2>/dev/null || true
  rm -f -- /etc/wireguard/wg-anas.conf /etc/systemd/system/anas-internet-firewall.service \
    /etc/a-nas/anas-internet.nft /etc/sysctl.d/60-anas-internet.conf
  echo "internet entry turned off; the tunnel key stays in /etc/wireguard/wg-anas.key"
fi
# Fan speeds come from the board's sensor chip, whose driver Linux does not
# load by itself (docs/specs/device-temperatures.md). The in-kernel drivers of
# the common chips find no device on a board without one; those that find a
# chip are loaded again at every boot. Loading only reads the chip and changes
# no fan setting, and no driver is forced onto a chip it does not know.
sensor_modules=()
for module in nct6775 it87; do
  if modprobe "$module" 2>/dev/null; then
    sensor_modules+=("$module")
  fi
done
install -d -o root -g root -m 0755 /etc/modules-load.d
if ((${#sensor_modules[@]} > 0)); then
  {
    echo '# Written by the A-NAS installer: the drivers that found a sensor chip.'
    printf '%s\n' "${sensor_modules[@]}"
  } > /etc/modules-load.d/a-nas-sensors.conf
  chmod 0644 /etc/modules-load.d/a-nas-sensors.conf
else
  rm -f /etc/modules-load.d/a-nas-sensors.conf
fi
ln -sfn -- "$target" /opt/a-nas/current
systemctl daemon-reload
# A running smbd keeps its listeners here; the Host Agent restarts it when the
# Samba configuration it writes listens elsewhere, as on a new install whose
# smbd runs under Debian's configuration. One that is not running waits for the
# SMB interface's address and keeps trying without it, which must not stop
# the installation halfway; the end of the output says so.
systemctl enable smbd.service
smb_started=true
systemctl start smbd.service || smb_started=false
# A running Worker belongs to the previous release; the next connection starts
# this release's.
systemctl stop anas-ai.service
systemctl enable anas-ai.socket
systemctl restart anas-ai.socket
# Clients reconnect to this release's worker and its shared admission slots.
systemctl enable anas-ffmpeg.socket
systemctl restart anas-ffmpeg.socket
# A running vector index belongs to the previous release; the photo service's
# next connection starts this release's.
systemctl stop 'anas-vectors-proxy@*.service' 'anas-vectors@*.service'
systemctl enable anas-vectors-proxy@a-nas-photos.socket
systemctl restart anas-vectors-proxy@a-nas-photos.socket
systemctl enable anas-host-agent.service anas-photos.service anas-api.service anas-shares.service anas-family.service
restarted_at=$(date '+%Y-%m-%d %H:%M:%S')
systemctl restart anas-host-agent.service anas-photos.service anas-api.service anas-shares.service anas-family.service
if systemctl is-active --quiet anas-kiosk@tty1.service; then
  systemctl restart anas-kiosk@tty1.service
fi
if [[ "$internet_entry" == true ]]; then
  systemctl enable anas-internet-firewall.service wg-quick@wg-anas.service
  systemctl restart anas-internet-firewall.service
  # A tunnel that does not come up leaves only the internet entry unreachable.
  if [[ -n "$tunnel_changed" ]] || ! systemctl is-active --quiet wg-quick@wg-anas.service; then
    systemctl restart wg-quick@wg-anas.service ||
      echo "the internet entry's tunnel did not start; see journalctl -u wg-quick@wg-anas (docs/runbooks/enable-internet-access.md)" >&2
  fi
fi
if [[ "$lan_entry" == true ]]; then
  systemctl enable caddy.service
  systemctl reload-or-restart caddy.service
fi
if [[ "$internet_entry" == true ]]; then
  echo "internet entry: https://$internet_host through $relay_endpoint; this NAS's tunnel public key: $(wg pubkey < /etc/wireguard/wg-anas.key)"
fi
# Web search follows this release's runtime; its unit's condition keeps it
# stopped while the administrator has turned it off. It comes after the
# product's own services and cannot fail the installation: the desktop
# answers web_search_unavailable while it is down.
web_search_failed=
systemctl enable anas-search-dns.path anas-search.service || web_search_failed=1
systemctl restart anas-search-dns.path || web_search_failed=1
systemctl start anas-search-dns.service || web_search_failed=1
systemctl restart anas-search.service || web_search_failed=1

systemctl --no-pager --full status anas-host-agent.service anas-photos.service anas-api.service anas-shares.service anas-family.service anas-ai.socket anas-ffmpeg.socket anas-vectors-proxy@a-nas-photos.socket
# Stopped is a valid state for web search, which status would report as a failure.
echo "web search: $(systemctl is-active anas-search.service || true)"
if [[ -n "$web_search_failed" ]]; then
  echo "web search did not start; see journalctl -u anas-search-dns -u anas-search -b (docs/runbooks/enable-web-search.md)" >&2
fi
if [[ "$smb_started" == false ]]; then
  echo "smbd has not started; systemd tries again every 5 s. If $smb_interface has no IPv4 address, SMB starts once it has one; otherwise see journalctl -u smbd -b" >&2
fi

# Web files and the terminal run as the signed-in user, so the Host Agent must
# keep CAP_SETUID and CAP_SETGID in its sandbox and prove the switch at startup
# (issue #38). A release that cannot is installed but must be rolled back.
identity_switch=
for _ in $(seq 1 30); do
  identity_switch=$(journalctl -u anas-host-agent.service --since "$restarted_at" -o cat --no-pager |
    grep -Eo 'file broker (identity switch verified|cannot switch to user identities)' | tail -n 1 || true)
  [[ -n "$identity_switch" ]] && break
  sleep 1
done
host_agent_pid=$(systemctl show -p MainPID --value anas-host-agent.service)
cap_eff=$(awk '/^CapEff:/ {print $2}' "/proc/$host_agent_pid/status" 2>/dev/null || true)
if [[ "$identity_switch" != "file broker identity switch verified" || -z "$cap_eff" ]] || (((16#$cap_eff >> 6 & 3) != 3)); then
  echo "anas-host-agent cannot start file workers as users (CapEff=${cap_eff:-unknown}; ${identity_switch:-no identity probe result}); roll back this release" >&2
  exit 5
fi
echo "Open the local A-NAS console to create the first administrator with an account and password."
