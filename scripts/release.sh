#!/usr/bin/env bash
# make release (ADR 0034): puts this commit's packages into the signed apt
# repository REPO, from which a deploy machine installs them
# (docs/runbooks/deploy-a-nas-packages.md).
#
# - The product's packages are the ones make packages built at this commit's
#   version (scripts/release-version.sh), which make check and the system test
#   passed: build/.validated-build and build/.system-tested name them.
# - The content packages come from the release content store CONTENT
#   (scripts/build-content-packages.sh); one the repository has is not built
#   again.
# - The Docker and Caddy packages that a-nas-host depends on are the upstream
#   files deploy/upstream/packages.json pins (ADR 0015), fetched into
#   CONTENT/upstream once.
# - a-nas-archive-keyring carries the public key that signs the repository.
#
# A commit with a release tag goes to the release distribution and to dev,
# any other to dev. The A-NAS archive key signs (scripts/release-key.sh,
# docs/runbooks/manage-release-key.md): this machine's subkey of it, in
# ~/.local/share/a-nas/release-key unless ANAS_RELEASE_GNUPGHOME names another,
# which must be good in deploy/release/a-nas-archive-key.asc. Its passphrase
# comes from the terminal, or from ANAS_RELEASE_PASSPHRASE_FILE, and is
# checked before anything is built. ANAS_RELEASE_TEST_KEY=1 signs with this
# machine's test key in ~/.local/share/a-nas/release-test-key instead, made on
# first use, for a repository of its own: a repository signed by one key is
# never signed by another.
#
# Usage: scripts/release.sh REPO CONTENT
set -euo pipefail

if [[ $# -ne 2 ]]; then
  echo "usage: $0 REPO CONTENT" >&2
  exit 2
fi
repo=$(cd "$(dirname "$0")/.." && pwd)
mkdir -p "$1"
repository=$(cd "$1" && pwd)
content=$(cd "$2" && pwd)
build=$repo/build
# shellcheck source=lib/content.sh
. "$repo/scripts/lib/content.sh"
# shellcheck source=lib/signing.sh
. "$repo/scripts/lib/signing.sh"

if [[ -n "$(git -C "$repo" status --porcelain)" ]]; then
  echo "commit everything first: a release is built from a commit" >&2
  exit 2
fi
version=$("$repo/scripts/release-version.sh")
dist=release
[[ "$version" == *dev* ]] && dist=dev
commit=$(git -C "$repo" rev-parse --short=12 HEAD)

# The packages the gates passed, at this version.
products=()
for package in a-nas a-nas-host a-nas-console a-nas-assistant-trial a-nas-docker-policy; do
  deb=$(find "$build/packages" -maxdepth 1 -name "${package}_${version}_*.deb" -print -quit 2>/dev/null)
  [[ -n "$deb" ]] || { echo "missing $package $version: run make packages" >&2; exit 2; }
  products+=("$deb")
done
validated=$(sed -n 's/^commit=//p' "$build/.validated-build" 2>/dev/null || true)
[[ "$validated" == "$commit" ]] || { echo "make check has not passed at $commit (build/.validated-build)" >&2; exit 2; }
for binary in api:anas-api host_agent:anas-host-agent; do
  grep -qx "${binary%%:*}_sha256=$(sha256sum "$build/${binary#*:}" | cut -d ' ' -f 1)" "$build/.validated-build" ||
    { echo "build/${binary#*:} is not the one make check passed" >&2; exit 2; }
done
(cd "$build/packages" && sha256sum --check --quiet --strict "$build/.system-tested" 2>/dev/null) ||
  { echo "the system test has not passed with these packages: make system-test (build/.system-tested)" >&2; exit 2; }

# The key that signs, and the public key the keyring carries.
if [[ -n "${ANAS_RELEASE_TEST_KEY:-}" ]]; then
  gnupg=$HOME/.local/share/a-nas/release-test-key
  echo "signing with the test key in $gnupg"
  "$repo/scripts/apt-repository.sh" key "$gnupg" >/dev/null
  public=$gnupg
  signer=$("$repo/scripts/apt-repository.sh" fingerprint "$gnupg")
else
  gnupg=${ANAS_RELEASE_GNUPGHOME:-$HOME/.local/share/a-nas/release-key}
  public=${ANAS_RELEASE_PUBLIC_KEY:-$repo/deploy/release/a-nas-archive-key.asc}
  subkey=$(ANAS_RELEASE_GNUPGHOME=$gnupg ANAS_RELEASE_PUBLIC_KEY=$public "$repo/scripts/release-key.sh" check) || exit 2
  ask_passphrase
  # shellcheck disable=SC2016 # the container's shell expands these
  in_image --passphrase "$gnupg:/gnupg" -- bash -c 'printf x | gpg --batch --quiet --pinentry-mode loopback \
    --passphrase-file /secret/passphrase --local-user "$1!" --detach-sign >/dev/null 2>&1' bash "$subkey" ||
    { echo "the passphrase does not unlock subkey $subkey in $gnupg" >&2; exit 2; }
  signer="$("$repo/scripts/apt-repository.sh" fingerprint "$gnupg"), subkey $subkey"
fi
"$repo/scripts/apt-repository.sh" signed-by "$repository" "$public" ||
  { echo "$repository is signed by another key: publish into a new repository (docs/runbooks/manage-release-key.md)" >&2; exit 2; }
# publish DIST: the packages into DIST, signed; the passphrase goes to the
# repository script through a pipe, not a file.
publish() {
  if [[ -n "${passphrase:-}" ]]; then
    ANAS_RELEASE_PASSPHRASE_FILE=<(printf '%s\n' "$passphrase") \
      "$repo/scripts/apt-repository.sh" publish "$repository" "$1" "$gnupg" "${debs[@]}"
  else
    "$repo/scripts/apt-repository.sh" publish "$repository" "$1" "$gnupg" "${debs[@]}"
  fi
}

# The content packages the repository lacks, built next to it and removed
# once published.
incoming=$(mktemp -d "$repository/.incoming.XXXXXX")
trap 'rm -rf -- "$incoming"' EXIT
ANAS_CONTENT_REPOSITORY=$repository "$repo/scripts/build-content-packages.sh" "$incoming" "$content"
# Every content package this release depends on, new or already published,
# goes into its distributions.
debs=("${products[@]}")
for name in $(content_packages "$repo") "$(assistant_runtime_package "$repo")"; do
  deb=$(find "$incoming" "$repository/pool" -name "${name}_1_*.deb" -print -quit 2>/dev/null)
  [[ -n "$deb" ]] || { echo "missing $name: its content in $content (scripts/build-content-packages.sh)" >&2; exit 2; }
  debs+=("$deb")
done
upstream=$("$repo/scripts/fetch-upstream-packages.sh" "$content/upstream")
mapfile -t upstream <<<"$upstream"
debs+=("${upstream[@]}")
"$repo/scripts/apt-repository.sh" keyring "$incoming" "$version" "$public"
debs+=("$incoming/a-nas-archive-keyring_${version}_all.deb")
publish dev
if [[ "$dist" == release ]]; then
  publish release
fi
keyring=$(find "$repository/pool" -name "a-nas-archive-keyring_${version}_all.deb" -print -quit)
echo "A-NAS $version ($commit) is in $repository ($dist). Copy the directory to the deploy machine as it is."
# What a machine checks before it first trusts the repository
# (docs/runbooks/deploy-a-nas-packages.md).
echo "signing key: $signer"
echo "${keyring#"$repository"/} SHA-256: $(sha256sum "$keyring" | cut -d ' ' -f 1)"
