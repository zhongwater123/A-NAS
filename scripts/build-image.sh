#!/usr/bin/env bash
# The A-NAS device image (ADR 0034 decision 2, step 5) of a release in the
# A-NAS apt repository: the same packages an apt machine installs
# (docs/runbooks/build-device-image.md).
#
#   scripts/build-image.sh REPO VERSION SEQUENCE UPDATES GNUPGHOME
#
# REPO is the signed repository that make release published the release to,
# VERSION the release's Debian version, and SEQUENCE its number on main: the
# count of first-parent commits, which only grows along main's first-parent
# line (make image builds commits on it alone). systemd-sysupdate refuses the
# "~" of Debian versions and its own comparison sorts every replacement after
# the release, so the device's update files are numbered by SEQUENCE; the
# Debian version stays in the release files. Each SEQUENCE is published once:
# one that is not above the last one UPDATES published is refused.
#
# UPDATES keeps the device's update files from release to release, in a
# directory per architecture (x86-64/), each kind in a directory of its own
# with a SHA256SUMS that the key in GNUPGHOME signs (scripts/apt-repository.sh
# sums). A device's update service reads them (docs/specs/system-update.md),
# and systemd-sysupdate writes the base:
#   os/                 the base image: Debian, a-nas-host and a-nas-console,
#                       as a /usr partition and a UKI, published only when what
#                       it holds changed, numbered by the release that changed it
#   product/            the a-nas package as a system extension, every release,
#                       and the release file a-nas-release_SEQUENCE.json: the
#                       base, product and content of the release, each file
#                       with its size and SHA-256
#   content-ROLE/       each kind of content (scripts/lib/content.sh) as a
#                       system extension, published when the release pins new
#                       content, numbered by that release; a device keeps what
#                       its product and the one before use merged side by side
#   install/            the whole disk of this release, to write to a new
#                       device's system disk
#   index/              what was published, for the next release to compare
#
# A key in GNUPGHOME that has a passphrase is unlocked before the build:
# ANAS_RELEASE_PASSPHRASE_FILE as for make release, else the terminal asks.
#
# mkosi and the tools run in a privileged container of the Debian 13 image
# builder (scripts/image-builder.sh). WORK, by default build/image-work, holds
# mkosi's cache and output, up to about 30 GB; a path ending in .img is an
# ext4 image file that the container mounts, for WSL, whose own disk never
# shrinks. ANAS_IMAGE_EXTRA, a directory, is copied over the base image: for
# tests only; it changes the base but not what the product needs from it.
set -euo pipefail
shopt -s inherit_errexit

# The image's architecture as systemd names it (mkosi.conf's Architecture=):
# it names the directory of update files and the extensions' ARCHITECTURE=.
arch=x86-64

if [[ "${1:-}" != --inside ]]; then
  [[ $# -eq 5 ]] || { sed -n '5,6p' "$0" >&2; exit 2; }
  repo_root=$(cd "$(dirname "$0")/.." && pwd)
  repository=$(cd "$1" && pwd)
  version=$2 sequence=$3
  [[ "$sequence" =~ ^[1-9][0-9]*$ ]] || { echo "SEQUENCE is a number, not $sequence" >&2; exit 2; }
  mkdir -p "$4/$arch"
  updates=$(cd "$4/$arch" && pwd)
  gnupg=$(cd "$5" && pwd)
  last=$(cat "$updates/index/sequence" 2>/dev/null || echo 0)
  ((sequence > last)) || { echo "$updates published image $last already: SEQUENCE $sequence must be above it" >&2; exit 2; }
  work=${WORK:-$repo_root/build/image-work}
  # shellcheck source=lib/content.sh
  . "$repo_root/scripts/lib/content.sh"
  # shellcheck source=lib/signing.sh
  . "$repo_root/scripts/lib/signing.sh"
  # shellcheck source=image-builder.sh
  . "$repo_root/scripts/image-builder.sh"

  # The key that signs the update files: a protected one is unlocked now,
  # not after the build.
  key=$(bash "$repo_root/scripts/apt-repository.sh" signing-key "$gnupg")
  passphrase=
  if ! signs_without_passphrase "$gnupg" "$key"; then
    ask_passphrase
    passphrase_unlocks "$gnupg" "$key" || { echo "the passphrase does not unlock $key in $gnupg" >&2; exit 2; }
  fi

  # deb NAME VERSION: the package's file in the repository, by its control
  # fields: a file name drops the epoch, and upstream's need not follow
  # Debian's pattern.
  deb() {
    local file found=()
    while IFS= read -r -d '' file; do
      [[ "$(dpkg-deb --field "$file" Package Version | paste -sd ' ' -)" == "Package: $1 Version: $2" ]] && found+=("$file")
    done < <(find "$repository/pool" -name "${1}_*.deb" -print0)
    ((${#found[@]} == 1)) || { echo "the repository has ${#found[@]} files of $1 $2" >&2; return 1; }
    echo "${found[0]}"
  }
  inputs=$(mktemp -d)
  trap 'rm -rf -- "$inputs"' EXIT
  mkdir -p "$inputs/base" "$inputs/product" "$inputs/content"
  # The base image: what the release needs from Debian and upstream.
  for package in a-nas-host a-nas-console a-nas-archive-keyring a-nas-docker-policy; do
    cp "$(deb "$package" "$version")" "$inputs/base/"
  done
  while read -r name pinned; do
    cp "$(deb "$name" "$pinned")" "$inputs/base/"
  done < <(jq -r '.packages[] | "\(.name) \(.version)"' "$repo_root/deploy/upstream/packages.json")
  cp "$(deb a-nas "$version")" "$inputs/product/a-nas.deb"
  # The content, by role; the release's checkout names what it pins.
  while read -r role packages; do
    mkdir -p "$inputs/content/$role"
    for package in $packages; do cp "$(deb "$package" 1)" "$inputs/content/$role/"; done
  done < <(content_roles "$repo_root")
  if [[ -n "${ANAS_IMAGE_EXTRA:-}" ]]; then cp -r "$ANAS_IMAGE_EXTRA" "$inputs/extra"; fi

  image_builder_start "$work"
  image_builder_run -v "$repo_root:/src:ro" -v "$inputs:/inputs:ro" -v "$updates:/updates" -- \
    bash /src/scripts/build-image.sh --inside "$version" "$sequence" "$(id -u):$(id -g)"

  # The release file: which base, product and content this release is, each
  # file with its size and SHA-256. The device's update service checks it
  # against the product directory's signed SHA256SUMS, and each file against
  # it (internal/systemupdate/updater/image.go). Those of releases whose
  # product is gone go with it.
  read -r base _ <"$updates/index/base"
  files=("product/a-nas-product_$sequence.raw" "os/a-nas_$base.usr.raw.xz" "os/a-nas_$base.efi")
  content='{}'
  while read -r role pinned _; do
    content=$(jq -c --arg role "$role" --arg pinned "$pinned" '. + {($role): $pinned}' <<<"$content")
    files+=("content-$role/a-nas-content-${role}_$pinned.raw")
  done <"$updates/index/content"
  listed='{}'
  for name in "${files[@]}"; do
    listed=$(jq -c --arg name "$name" --argjson size "$(stat -c %s "$updates/$name")" \
      --arg sum "$(sha256sum "$updates/$name" | cut -d ' ' -f 1)" '. + {($name): {size: $size, sha256: $sum}}' <<<"$listed")
  done
  jq -n --arg sequence "$sequence" --arg version "$version" --arg arch "$arch" --arg host "$(cat "$updates/index/host")" \
    --arg base "$base" --argjson content "$content" --argjson files "$listed" \
    '{sequence: $sequence, version: $version, architecture: $arch, hostId: $host, base: $base, content: $content, files: $files}' \
    >"$updates/product/a-nas-release_$sequence.json"
  for file in "$updates"/product/a-nas-release_*.json; do
    kept=${file##*/a-nas-release_}
    [[ -f "$updates/product/a-nas-product_${kept%.json}.raw" ]] || rm -f -- "$file"
  done

  # Every directory systemd-sysupdate reads gets a signed SHA256SUMS, in one
  # container, so that the passphrase is given once.
  dirs=()
  for dir in "$updates"/os "$updates"/product "$updates"/content-*; do
    if [[ -d "$dir" ]]; then dirs+=("$dir"); fi
  done
  if [[ -n "$passphrase" ]]; then
    ANAS_RELEASE_PASSPHRASE_FILE=<(printf '%s\n' "$passphrase") \
      bash "$repo_root/scripts/apt-repository.sh" sums "$gnupg" "${dirs[@]}"
  else
    bash "$repo_root/scripts/apt-repository.sh" sums "$gnupg" "${dirs[@]}"
  fi
  echo "$sequence" >"$updates/index/sequence"
  echo "image $sequence ($version): $(cat "$updates/index/summary")"
  exit 0
fi

# ---- inside the builder ----------------------------------------------------
version=$2 sequence=$3 owner=$4
W=/work
U=/updates
# The update files belong to the user who builds them, who signs and copies
# them next.
trap 'chown -R "$owner" "$U"' EXIT
mkdir -p "$U/index" "$U/product" "$U/install"
touch "$U/index/base" "$U/index/content"
summary=()

# extension NAME TREE OUT: TREE, keeping /usr and /opt, as the system
# extension NAME in the file OUT. The release file's name is the extension's:
# the name of the file or link systemd-sysext finds in /var/lib/extensions.
extension() {
  local name=$1 tree=$2 out=$3
  shift 3
  find "$tree" -mindepth 1 -maxdepth 1 ! -name usr ! -name opt -exec rm -rf -- {} +
  mkdir -p "$tree/usr/lib/extension-release.d"
  printf '%s\n' "$@" EXTENSION_RELOAD_MANAGER=1 >"$tree/usr/lib/extension-release.d/extension-release.$name"
  rm -f -- "$out"
  # Model weights do not compress; the product does.
  local compress=()
  if [[ $name == a-nas-product ]]; then compress=(-zlz4hc); fi
  mkfs.erofs "${compress[@]}" "$out" "$tree" >"$W/mkfs.erofs.log" 2>&1 || { cat "$W/mkfs.erofs.log"; return 1; }
}

# package_contents DEB: what an A-NAS package installs and runs, without its
# version: each path with its type, mode and link target, each file's SHA-256
# and the maintainer scripts. The packages carry no md5sums.
package_contents() {
  local dir
  dir=$(mktemp -d)
  dpkg-deb --raw-extract "$1" "$dir"
  rm -f -- "$dir/DEBIAN/control"
  (
    cd "$dir"
    find . -mindepth 1 -printf '%y %m %p %l\n' | LC_ALL=C sort
    find . -type f -print0 | LC_ALL=C sort -z | xargs -0 -r sha256sum
  )
  rm -rf -- "$dir"
}

# The image's configuration. A Windows checkout shows every file as
# executable.
config=$W/config
rm -rf -- "$config"
cp -r /src/deploy/image "$config"
find "$config" -type d -exec chmod 0755 {} +
find "$config" -type f -exec chmod 0644 {} +
chmod 0755 "$config/mkosi.postinst" "$config/mkosi.finalize" "$config"/mkosi.extra/usr/lib/a-nas-image/*

# What the product needs from the base: A-NAS's own packages in it, by what
# they install, run and depend on, and the image's configuration. The base's
# os-release and the product's release file both carry it, and a-nas-check
# compares them. Debian's packages may change under a product, and the test's
# ANAS_IMAGE_EXTRA does not count.
host_id=$(
  {
    for package in /inputs/base/a-nas-*.deb; do
      echo "$(dpkg-deb -f "$package" Package): $(dpkg-deb -f "$package" Depends | sed -E 's/ \([^)]*\)//g')"
      package_contents "$package"
    done
    cd "$config" && find . -type f | LC_ALL=C sort | xargs sha256sum
  } | sha256sum | cut -c 1-32
)
echo "$host_id" >"$U/index/host"

# The content: a role whose packages are those of the last image keeps its
# file and number; a role with new content gets this release's. Each version
# is an extension of its own name, so that a device merges the one before
# beside it.
declare -A content_version=() content_packages=()
for role_dir in /inputs/content/*/; do
  role=$(basename "$role_dir")
  name=a-nas-content-$role
  packages=$(cd "$role_dir" && find . -name '*.deb' -printf '%P\n' | LC_ALL=C sort | paste -sd ' ' -)
  content_packages[$role]=$packages
  previous=$(awk -v r="$role" '$1 == r { $1 = ""; print substr($0, 2) }' "$U/index/content")
  had=${previous%% *}
  dir=$U/content-$role
  if [[ -n "$had" && "${previous#* }" == "$packages" && -f "$dir/${name}_$had.raw" ]]; then
    content_version[$role]=$had
    continue
  fi
  tree=$W/content/$role
  rm -rf -- "$tree"
  mkdir -p "$tree" "$dir"
  for package in $packages; do dpkg-deb -x "/inputs/content/$role/$package" "$tree"; done
  extension "${name}_$sequence" "$tree" "$dir/${name}_$sequence.raw" ID=_any "ARCHITECTURE=$arch"
  rm -rf -- "$tree"
  content_version[$role]=$sequence
  summary+=("content $role $sequence")
  # Two versions stay: the one before, for a device that goes back.
  find "$dir" -maxdepth 1 -name "${name}_*.raw" | sort -V | head -n -2 | xargs -r rm -f --
done
for role in "${!content_version[@]}"; do
  echo "$role ${content_version[$role]} ${content_packages[$role]}"
done | LC_ALL=C sort >"$U/index/content.new"
mv "$U/index/content.new" "$U/index/content"

# The product: the a-nas package as an extension. Its release file names its
# Debian version and sequence, and what it needs from the base.
tree=$W/product
rm -rf -- "$tree"
mkdir -p "$tree"
dpkg-deb -x /inputs/product/a-nas.deb "$tree"
extension a-nas-product "$tree" "$U/product/a-nas-product_$sequence.raw" ID=debian VERSION_ID=13 \
  "ARCHITECTURE=$arch" "A_NAS_VERSION=$version" "A_NAS_SEQUENCE=$sequence" "A_NAS_HOST_ID=$host_id"
rm -rf -- "$tree"
summary+=("product $sequence")
find "$U/product" -maxdepth 1 -name 'a-nas-product_*.raw' | sort -V | head -n -5 | xargs -r rm -f --

# What a new device starts with: this release's product and content, where
# systemd-sysext and the update service expect them, and the product linked
# to this base (a-nas-product-select.service).
first=$W/first
rm -rf -- "$first"
mkdir -p "$first/var/lib/a-nas-sysext" "$first/var/lib/extensions" "$first/var/lib/a-nas-image/product"
cp "$U/product/a-nas-product_$sequence.raw" "$first/var/lib/a-nas-sysext/"
ln -s "/var/lib/a-nas-sysext/a-nas-product_$sequence.raw" "$first/var/lib/extensions/a-nas-product.raw"
ln -s "/var/lib/a-nas-sysext/a-nas-product_$sequence.raw" "$first/var/lib/a-nas-image/product/$sequence"
for role in "${!content_version[@]}"; do
  cp "$U/content-$role/a-nas-content-${role}_${content_version[$role]}.raw" "$first/var/lib/extensions/"
done

# The base image.
extra=()
if [[ -d /inputs/extra ]]; then extra=(--extra-tree=/inputs/extra); fi
out=$W/out
rm -rf -- "$out"
mkdir -p "$out" "$W/cache" "$W/pkgcache"
if ! mkosi --directory="$config" --image-version="$sequence" --package-directory=/inputs/base \
  --environment="A_NAS_HOST_ID=$host_id" --extra-tree="$first" "${extra[@]}" --output-directory="$out" \
  --cache-directory="$W/cache" --package-cache-directory="$W/pkgcache" --workspace-directory=/var/tmp \
  --manifest-format=json -f build >"$W/mkosi.log" 2>&1; then
  tail -n 40 "$W/mkosi.log"
  exit 1
fi

# What the base holds: what the product needs from it, Debian's and
# upstream's packages by version, and the test's additions.
fingerprint=$(
  {
    echo "host $host_id"
    jq -r '.packages[] | select(.name | startswith("a-nas") | not) | "\(.name) \(.version)"' "$out/a-nas_$sequence.manifest"
    if [[ -d /inputs/extra ]]; then cd /inputs/extra && find . -type f | LC_ALL=C sort | xargs sha256sum; fi
  } | sha256sum | cut -d ' ' -f 1
)
read -r base had <"$U/index/base" || true
if [[ "${had:-}" == "$fingerprint" && -f "$U/os/a-nas_$base.efi" ]]; then
  summary+=("base $base, unchanged")
else
  mkdir -p "$U/os"
  xz -T0 -6 -c "$out/a-nas_$sequence.usr.raw" >"$U/os/a-nas_$sequence.usr.raw.xz"
  cp "$out/a-nas_$sequence.efi" "$U/os/"
  echo "$sequence $fingerprint" >"$U/index/base"
  summary+=("base $sequence")
  # Three bases stay: a device keeps two, and goes back only to its own.
  for kind in usr.raw.xz efi; do
    find "$U/os" -maxdepth 1 -name "a-nas_*.$kind" | sort -V | head -n -3 | xargs -r rm -f --
  done
fi
rm -f -- "$U"/install/a-nas_*.raw.zst
zstd -q -T0 -3 "$out/a-nas_$sequence.raw" -o "$U/install/a-nas_$sequence.raw.zst"
rm -rf -- "$out" "$first"
printf '%s; ' "${summary[@]}" | sed 's/; $/\n/' >"$U/index/summary"
