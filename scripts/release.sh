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
# any other to dev. ANAS_RELEASE_GNUPGHOME names the GnuPG home of the signing
# key; without it, the test key in ~/.local/share/a-nas/release-test-key signs,
# made on first use. Which key signs releases, and where it is kept, is the
# owner's decision (docs/decisions/2026-11-11-release-signing-key.md).
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

gnupg=${ANAS_RELEASE_GNUPGHOME:-}
if [[ -z "$gnupg" ]]; then
  gnupg=$HOME/.local/share/a-nas/release-test-key
  echo "signing with the test key in $gnupg (ANAS_RELEASE_GNUPGHOME names another)"
  "$repo/scripts/apt-repository.sh" key "$gnupg" >/dev/null
fi

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
"$repo/scripts/apt-repository.sh" keyring "$incoming" "$version" "$gnupg"
debs+=("$incoming/a-nas-archive-keyring_${version}_all.deb")
"$repo/scripts/apt-repository.sh" publish "$repository" dev "$gnupg" "${debs[@]}"
if [[ "$dist" == release ]]; then
  "$repo/scripts/apt-repository.sh" publish "$repository" release "$gnupg" "${debs[@]}"
fi
keyring=$(find "$repository/pool" -name "a-nas-archive-keyring_${version}_all.deb" -print -quit)
echo "A-NAS $version ($commit) is in $repository ($dist). Copy the directory to the deploy machine as it is."
# What a machine checks before it first trusts the repository
# (docs/runbooks/deploy-a-nas-packages.md).
echo "signing key: $("$repo/scripts/apt-repository.sh" fingerprint "$gnupg")"
echo "${keyring#"$repository"/} SHA-256: $(sha256sum "$keyring" | cut -d ' ' -f 1)"
