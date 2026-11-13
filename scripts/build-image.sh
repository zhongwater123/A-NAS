#!/usr/bin/env bash
# The A-NAS device image (ADR 0034 decision 2, step 5) of a release in the
# A-NAS apt repository: the same packages an apt machine installs
# (docs/runbooks/build-device-image.md).
#
#   scripts/build-image.sh REPO VERSION SEQUENCE UPDATES GNUPGHOME
#
# REPO is the signed repository that make release published the release to,
# VERSION the release's Debian version, and SEQUENCE its number on main: the
# count of first-parent commits, which only grows along main. systemd-sysupdate
# refuses the "~" of Debian versions and its own comparison sorts every
# replacement after the release, so the device's update files are numbered by
# SEQUENCE; the Debian version stays in the release files. Images are built
# from commits on main alone.
#
# UPDATES keeps the device's update files from release to release, each kind
# in a directory of its own with a SHA256SUMS that the key in GNUPGHOME signs
# (scripts/apt-repository.sh sums), as systemd-sysupdate reads them:
#   os/                 the base image: Debian, a-nas-host and a-nas-console,
#                       as a /usr partition and a UKI, published only when what
#                       it holds changed, numbered by the release that changed it
#   product/            the a-nas package as a system extension, every release
#   content-ROLE/       each kind of content (scripts/lib/content.sh) as a
#                       system extension, published when the release pins new
#                       content, numbered by that release
#   install/            the whole disk of this release, to write to a new
#                       device's system disk
#   index/              what was published, for the next release to compare
#
# mkosi and the tools run in a privileged container of the Debian 13 image
# builder (scripts/image-builder.sh). WORK, by default build/image-work, holds
# mkosi's cache and output, up to about 30 GB; a path ending in .img is an
# ext4 image file that the container mounts, for WSL, whose own disk never
# shrinks. ANAS_RELEASE_PASSPHRASE_FILE as for make release. ANAS_IMAGE_EXTRA,
# a directory, is copied over the base image: for tests only.
set -euo pipefail

if [[ "${1:-}" != --inside ]]; then
  [[ $# -eq 5 ]] || { sed -n '5,9p' "$0" >&2; exit 2; }
  repo_root=$(cd "$(dirname "$0")/.." && pwd)
  repository=$(cd "$1" && pwd)
  version=$2 sequence=$3
  mkdir -p "$4"
  updates=$(cd "$4" && pwd)
  gnupg=$(cd "$5" && pwd)
  [[ "$sequence" =~ ^[1-9][0-9]*$ ]] || { echo "SEQUENCE is a number, not $sequence" >&2; exit 2; }
  work=${WORK:-$repo_root/build/image-work}
  # shellcheck source=lib/content.sh
  . "$repo_root/scripts/lib/content.sh"
  # shellcheck source=image-builder.sh
  . "$repo_root/scripts/image-builder.sh"

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
  cp "$(deb a-nas "$version")" "$inputs/product/"
  # The content, by role; the release's checkout names what it pins.
  while read -r role packages; do
    mkdir -p "$inputs/content/$role"
    for package in $packages; do cp "$(deb "$package" 1)" "$inputs/content/$role/"; done
  done < <(content_roles "$repo_root")
  if [[ -n "${ANAS_IMAGE_EXTRA:-}" ]]; then cp -r "$ANAS_IMAGE_EXTRA" "$inputs/extra"; fi

  image_builder_start "$work"
  image_builder_run -v "$repo_root:/src:ro" -v "$inputs:/inputs:ro" -v "$updates:/updates" -- \
    bash /src/scripts/build-image.sh --inside "$version" "$sequence"
  # Every directory systemd-sysupdate reads gets a signed SHA256SUMS.
  for dir in "$updates"/os "$updates"/product "$updates"/content-*; do
    [[ -d "$dir" ]] || continue
    bash "$repo_root/scripts/apt-repository.sh" sums "$dir" "$gnupg"
  done
  echo "image $sequence ($version): $(cat "$updates/index/summary")"
  exit 0
fi

# ---- inside the builder ----------------------------------------------------
version=$2 sequence=$3
W=/work
U=/updates
mkdir -p "$U/index" "$U/product" "$U/install"
touch "$U/index/base" "$U/index/content"
summary=()

# extension NAME TREE OUT: TREE, keeping /usr and /opt, as the system
# extension NAME in the file OUT. The release file's name is the extension's:
# the name of the link in /var/lib/extensions, not of the versioned file.
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

# The content: a role whose packages are those of the last image keeps its
# file and number; a role with new content gets this release's.
declare -A content_version=()
for role_dir in /inputs/content/*/; do
  role=$(basename "$role_dir")
  packages=$(cd "$role_dir" && find . -name '*.deb' -printf '%P\n' | LC_ALL=C sort | paste -sd ' ' -)
  previous=$(awk -v r="$role" '$1 == r { $1 = ""; print substr($0, 2) }' "$U/index/content")
  had=${previous%% *}
  dir=$U/content-$role
  if [[ -n "$had" && "${previous#* }" == "$packages" && -f "$dir/a-nas-content-${role}_$had.raw" ]]; then
    content_version[$role]=$had
    continue
  fi
  tree=$W/content/$role
  rm -rf -- "$tree"
  mkdir -p "$tree" "$dir"
  for package in $packages; do dpkg-deb -x "/inputs/content/$role/$package" "$tree"; done
  extension "a-nas-content-$role" "$tree" "$dir/a-nas-content-${role}_$sequence.raw" ID=_any
  rm -rf -- "$tree"
  content_version[$role]=$sequence
  summary+=("content $role $sequence")
  # Two versions stay: the one before, for a device that goes back.
  find "$dir" -maxdepth 1 -name "a-nas-content-${role}_*.raw" | sort -V | head -n -2 | xargs -r rm -f --
done
{
  for role in "${!content_version[@]}"; do
    echo "$role ${content_version[$role]} $(cd "/inputs/content/$role" && find . -name '*.deb' -printf '%P\n' | LC_ALL=C sort | paste -sd ' ' -)"
  done | LC_ALL=C sort
} >"$U/index/content.new"
mv "$U/index/content.new" "$U/index/content"

# The product: the a-nas package as an extension, with the update definitions
# of its content, so that a release that pins a new kind of content brings
# its definition along.
tree=$W/product
rm -rf -- "$tree"
mkdir -p "$tree"
dpkg-deb -x /inputs/product/a-nas_"$version"_*.deb "$tree"
for role in "${!content_version[@]}"; do
  defs=$tree/usr/lib/sysupdate.content-$role.d
  mkdir -p "$defs"
  cat >"$defs/50-content.transfer" <<EOF
# $role, a system extension of its own (scripts/build-image.sh).
[Source]
Type=url-file
Path=file:///var/lib/a-nas-updates/content-$role/
MatchPattern=a-nas-content-${role}_@v.raw

[Target]
Type=regular-file
Path=/var/lib/a-nas-content/$role
MatchPattern=a-nas-content-${role}_@v.raw
CurrentSymlink=/var/lib/extensions/a-nas-content-$role.raw
InstancesMax=2
EOF
done
extension a-nas-product "$tree" "$U/product/a-nas-product_$sequence.raw" ID=debian VERSION_ID=13 "A_NAS_VERSION=$version"
rm -rf -- "$tree"
summary+=("product $sequence")
find "$U/product" -maxdepth 1 -name 'a-nas-product_*.raw' | sort -V | head -n -5 | xargs -r rm -f --

# What a new device starts with: this release's product and content, linked
# where systemd-sysext and the update definitions expect them.
first=$W/first
rm -rf -- "$first"
mkdir -p "$first/var/lib/a-nas-sysext" "$first/var/lib/extensions"
cp "$U/product/a-nas-product_$sequence.raw" "$first/var/lib/a-nas-sysext/"
ln -s "/var/lib/a-nas-sysext/a-nas-product_$sequence.raw" "$first/var/lib/extensions/a-nas-product.raw"
for role in "${!content_version[@]}"; do
  file=a-nas-content-${role}_${content_version[$role]}.raw
  mkdir -p "$first/var/lib/a-nas-content/$role"
  cp "$U/content-$role/$file" "$first/var/lib/a-nas-content/$role/"
  ln -s "/var/lib/a-nas-content/$role/$file" "$first/var/lib/extensions/a-nas-content-$role.raw"
done

# The base image.
config=$W/config
rm -rf -- "$config"
cp -r /src/deploy/image "$config"
# A Windows checkout shows every file as executable.
find "$config" -type d -exec chmod 0755 {} +
find "$config" -type f -exec chmod 0644 {} +
chmod 0755 "$config/mkosi.postinst"
extra=()
if [[ -d /inputs/extra ]]; then extra=(--extra-tree=/inputs/extra); fi
out=$W/out
rm -rf -- "$out"
mkdir -p "$out" "$W/cache" "$W/pkgcache"
if ! mkosi --directory="$config" --image-version="$sequence" --package-directory=/inputs/base \
  --extra-tree="$first" "${extra[@]}" --output-directory="$out" --cache-directory="$W/cache" \
  --package-cache-directory="$W/pkgcache" --workspace-directory=/var/tmp --manifest-format=json \
  -f build >"$W/mkosi.log" 2>&1; then
  tail -n 40 "$W/mkosi.log"
  exit 1
fi

# What the base holds: Debian's and upstream's packages by version, A-NAS's
# own by what they install and need, which a release changes only now and
# then, and the image's configuration.
fingerprint=$(
  {
    jq -r '.packages[] | select(.name | startswith("a-nas") | not) | "\(.name) \(.version)"' "$out/a-nas_$sequence.manifest"
    for package in /inputs/base/a-nas-*.deb; do
      echo "$(dpkg-deb -f "$package" Package): $(dpkg-deb -f "$package" Depends | sed -E 's/ \([^)]*\)//g')"
      dpkg-deb -I "$package" md5sums 2>/dev/null || true
    done
    cd "$config" && find . -type f | LC_ALL=C sort | xargs sha256sum
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
