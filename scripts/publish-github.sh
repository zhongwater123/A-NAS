#!/usr/bin/env bash
# Publishes a distribution of the A-NAS apt repository on GitHub
# (docs/decisions/2026-11-11-update-hosting.md): devices take it from
#   https://github.com/<GITHUB_REPO>/releases/download/apt-<DIST>/
# as an apt flat repository. A release's files cannot sit in directories, so
# the flat form has the signed index (InRelease, Release, Packages) and every
# package side by side, each package named by its SHA-256, since GitHub
# renames files with ~ or + in their names. The index is made with
# apt-ftparchive from the packages themselves and signed with the A-NAS
# archive key like the repository; apt checks every file against it, so
# GitHub only stores them.
#
#   publish-github.sh flat OUT REPO DIST GNUPGHOME [KEEP]
#       builds the flat form of distribution DIST of the repository REPO in
#       OUT, a new directory beside REPO. With KEEP, only the newest KEEP
#       versions of each A-NAS package stay; content and upstream packages
#       stay whole. The packages are hard links into REPO.
#   publish-github.sh upload OUT GITHUB_REPO TAG
#       makes the release TAG of GITHUB_REPO match OUT with gh: new packages
#       first, the index last, then the packages OUT no longer has go.
#   publish-github.sh REPO DIST
#       both, as make publish runs them: DIST release keeps every version,
#       dev the newest 20; signed with this machine's subkey of the archive
#       key, whose passphrase it asks for (docs/runbooks/manage-release-key.md).
#       GITHUB_REPO names the repository (zhongwater123/A-NAS-dist by default),
#       TAG the release (apt-DIST by default). A test key
#       (ANAS_RELEASE_TEST_KEY=1) may only publish under another TAG.
#
# GnuPG and apt-ftparchive run in the Debian 13 image (scripts/lib/signing.sh).
set -euo pipefail

repo_root=$(cd "$(dirname "$0")/.." && pwd)
# shellcheck source=lib/signing.sh
. "$repo_root/scripts/lib/signing.sh"
# The packages the A-NAS releases version, which KEEP prunes.
product='^(a-nas|a-nas-host|a-nas-console|a-nas-assistant-trial|a-nas-archive-keyring)$'

flat() {
  local out=$1 repo dist=$3 gnupg=$4 keep=${5:-} index key entries list count oldest other package version file sha
  repo=$(cd "$2" && pwd)
  gnupg=$(cd "$gnupg" && pwd)
  index=$repo/dists/$dist/main/binary-amd64/Packages
  [[ -f "$index" ]] || { echo "no distribution $dist in $repo" >&2; return 2; }
  [[ ! -e "$out" ]] || { echo "$out exists: name a new directory" >&2; return 2; }
  mkdir -p "$out"
  out=$(cd "$out" && pwd)
  # PACKAGE VERSION FILENAME for each package of the distribution.
  entries=$(awk '/^Package: / { p = $2 } /^Version: / { v = $2 } /^Filename: / { f = $2 }
    /^$/ { if (p != "") print p, v, f; p = "" } END { if (p != "") print p, v, f }' "$index")
  # With KEEP, the newest KEEP versions of each A-NAS package, by Debian's
  # order: each version replaces the oldest kept one it is newer than.
  declare -A top=()
  if [[ -n "$keep" ]]; then
    while read -r package version _; do
      [[ "$package" =~ $product ]] || continue
      list=${top[$package]:-}
      count=$(grep -c . <<<"$list" || true)
      if ((count < keep)); then
        top[$package]+="$version"$'\n'
        continue
      fi
      oldest=
      for other in $list; do
        if [[ -z "$oldest" ]] || dpkg --compare-versions "$other" lt "$oldest"; then oldest=$other; fi
      done
      if dpkg --compare-versions "$version" gt "$oldest"; then
        top[$package]="$(grep -vxF -- "$oldest" <<<"$list")"$'\n'"$version"$'\n'
      fi
    done <<<"$entries"
  fi
  while read -r package version file; do
    if [[ -n "$keep" && "$package" =~ $product ]] && ! grep -qxF -- "$version" <<<"${top[$package]:-}"; then
      continue
    fi
    sha=$(sha256sum "$repo/$file" | cut -d ' ' -f 1)
    ln -f -- "$repo/$file" "$out/$sha.deb" 2>/dev/null || cp -- "$repo/$file" "$out/$sha.deb"
  done <<<"$entries"
  key=$("$repo_root/scripts/apt-repository.sh" signing-key "$gnupg")
  secret=()
  [[ -z "${passphrase:-}" ]] || secret=(--passphrase)
  # shellcheck disable=SC2016 # the container's shell expands these
  in_image "${secret[@]}" "$gnupg:/gnupg" "$out:/flat" -- bash -c '
    set -euo pipefail
    cd /flat
    apt-ftparchive packages . >Packages
    apt-ftparchive -o APT::FTPArchive::Release::Origin=A-NAS -o APT::FTPArchive::Release::Label=A-NAS \
      -o APT::FTPArchive::Release::Suite="$1" -o APT::FTPArchive::Release::Architectures=amd64 release . >Release.new
    mv Release.new Release
    sign=(gpg --batch --quiet --pinentry-mode loopback --local-user "$2!")
    [[ ! -f /secret/passphrase ]] || sign+=(--passphrase-file /secret/passphrase)
    "${sign[@]}" --clearsign --output InRelease Release' bash "$dist" "$key"
  echo "$dist: $(grep -c '^Package: ' "$out/Packages") package versions in $out"
}

upload() {
  local out github=$2 tag=$3 existing
  out=$(cd "$1" && pwd)
  [[ -f "$out/InRelease" ]] || { echo "no signed index in $out" >&2; return 2; }
  gh release view "$tag" --repo "$github" >/dev/null 2>&1 ||
    gh release create "$tag" --repo "$github" --title "A-NAS apt repository: $tag" --latest=false \
      --notes "The A-NAS apt repository ($tag) in apt's flat form, signed with the A-NAS archive key. Add it to a device as docs/runbooks/deploy-a-nas-packages.md says; apt checks every file against the signature." >/dev/null
  existing=$(gh release view "$tag" --repo "$github" --json assets -q '.assets[].name')
  for deb in "$out"/*.deb; do
    grep -qxF "$(basename "$deb")" <<<"$existing" || gh release upload "$tag" "$deb" --repo "$github"
  done
  # The index after the packages it names, InRelease last: a device that reads
  # it in between finds no file missing, at worst an index that is older.
  gh release upload "$tag" "$out/Packages" "$out/Release" --repo "$github" --clobber
  gh release upload "$tag" "$out/InRelease" --repo "$github" --clobber
  while read -r asset; do
    [[ -z "$asset" || -e "$out/$asset" ]] || gh release delete-asset "$tag" "$asset" --repo "$github" --yes
  done <<<"$existing"
  echo "$tag: https://github.com/$github/releases/download/$tag/"
}

case "${1:-}" in
  flat)
    [[ $# -eq 5 || $# -eq 6 ]] || { echo "usage: $0 flat OUT REPO DIST GNUPGHOME [KEEP]" >&2; exit 2; }
    if [[ -n "${ANAS_RELEASE_PASSPHRASE_FILE:-}" ]]; then ask_passphrase; fi
    flat "${@:2}"
    ;;
  upload)
    [[ $# -eq 4 ]] || { echo "usage: $0 upload OUT GITHUB_REPO TAG" >&2; exit 2; }
    upload "${@:2}"
    ;;
  -* | '' | help)
    sed -n '2,32p' "$0"
    exit 2
    ;;
  *)
    [[ $# -eq 2 ]] || { echo "usage: $0 REPO DIST" >&2; exit 2; }
    repo=$(cd "$1" && pwd)
    dist=$2
    [[ "$dist" == release || "$dist" == dev ]] || { echo "DIST is release or dev, not $dist" >&2; exit 2; }
    github=${GITHUB_REPO:-zhongwater123/A-NAS-dist}
    tag=${TAG:-apt-$dist}
    keep=
    [[ "$dist" == release ]] || keep=20
    if [[ -n "${ANAS_RELEASE_TEST_KEY:-}" ]]; then
      [[ "$tag" != apt-release && "$tag" != apt-dev ]] ||
        { echo "a test key does not publish $tag: devices trust the archive key there" >&2; exit 2; }
      gnupg=$HOME/.local/share/a-nas/release-test-key
    else
      gnupg=${ANAS_RELEASE_GNUPGHOME:-$HOME/.local/share/a-nas/release-key}
      "$repo_root/scripts/release-key.sh" check >/dev/null || exit 2
      ask_passphrase
    fi
    out=$(mktemp -d "$(dirname "$repo")/.github-$dist.XXXXXX")
    rmdir "$out"
    trap 'rm -rf -- "$out"' EXIT
    flat "$out" "$repo" "$dist" "$gnupg" "$keep"
    if ! command -v gh >/dev/null; then
      # On a Windows development machine gh is signed in on Windows, not in
      # WSL: the flat form stays for its upload from Git Bash.
      trap - EXIT
      echo "no gh here: upload from where gh is signed in, such as Git Bash, then remove the directory:"
      # Git Bash names /mnt/d/... as D:/...
      where=$(sed -E 's|^/mnt/([a-z])/|\U\1:/|' <<<"$out")
      echo "  scripts/publish-github.sh upload $where $github $tag && rm -rf $where"
      exit 0
    fi
    upload "$out" "$github" "$tag"
    ;;
esac
