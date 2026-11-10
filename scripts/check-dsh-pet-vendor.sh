#!/usr/bin/env bash
# Checks that web/src/companion/dsh-pet is what scripts/vendor-dsh-pet.py makes
# of dsh-pet's pinned commit with the patches in
# web/src/companion/dsh-pet-patches (make vendor-check, part of make
# check-steps).
#
#   check-dsh-pet-vendor.sh [DSH_PET [MOV_ZIP]]
#
# Always, offline: the pool animations.sha256 makes is the one the README names
# and scripts/install-media-pool.sh pins, the README lists the patches there
# are, and every copied file names the pinned commit (a file a patch adds
# names none).
# With DSH_PET, a dsh-pet checkout at the pinned commit: vendors again into a
# scratch directory and requires no difference, and sees the script refuse a
# checkout with a change. Without MOV_ZIP, the pinned assets-mov zip, the HEVC
# animations' hashes come from the repository's manifest unchecked. CI has
# neither (upstream replaced the zip), so it runs the offline part only.
# PYTHON names a Python with json5 (ai/requirements-dev.txt); default python3.
set -euo pipefail

repo=$(cd "$(dirname "$0")/.." && pwd)
vendored=$repo/web/src/companion/dsh-pet
patches=$repo/web/src/companion/dsh-pet-patches
python=${PYTHON:-python3}
fail() {
  echo "vendor-check: $*" >&2
  exit 1
}
[[ $# -le 2 && ! (-z ${1:-} && -n ${2:-}) ]] || fail "usage: $0 [DSH_PET [MOV_ZIP]]"

pinned=$(sed -n 's/^PINNED = "\([0-9a-f]\{40\}\)"$/\1/p' "$repo/scripts/vendor-dsh-pet.py")
[[ -n $pinned ]] || fail "scripts/vendor-dsh-pet.py pins no commit"
pool=$(sha256sum < "$vendored/animations.sha256" | cut -c 1-16)
installer_pool=$(sed -n 's/^companion_pool=\([0-9a-f]\{16\}\)$/\1/p' "$repo/scripts/install-media-pool.sh")
[[ $pool == "$installer_pool" ]] ||
  fail "animations.sha256 makes pool $pool, but scripts/install-media-pool.sh pins ${installer_pool:-none}"
grep -Fq "companion animation pool \`$pool\`" "$vendored/README.md" || fail "dsh-pet/README.md does not name pool $pool"
# shellcheck disable=SC2016 # backquotes, not an expansion
listed=$(sed -n 's/^Patches applied: //p' "$vendored/README.md" | grep -o '`[^`]*`' | tr -d '`' || true)
present=$(find "$patches" -maxdepth 1 -name '*.patch' -printf '%f\n' | LC_ALL=C sort)
[[ $listed == "$present" ]] || fail "dsh-pet/README.md does not list the patches in dsh-pet-patches: vendor again"
header="// Vendored from PC2005-cloud/dsh-pet ${pinned:0:12} (MIT, see LICENSE)."
while IFS= read -r -d '' file; do
  first=$(head -n 1 -- "$file")
  [[ $first != "// Vendored from "* || $first == "$header" ]] || fail "${file#"$repo/"} does not start with: $header"
done < <(find "$vendored" -name '*.ts' -print0)
echo "vendor-check: pool $pool, $(wc -w <<<"$present") patches, copied from ${pinned:0:12}"

if [[ -z ${1:-} ]]; then
  echo "vendor-check: SKIP vendoring again: no dsh-pet checkout given (make vendor-check DSH_PET=<checkout at ${pinned:0:12}> [DSH_PET_MOV_ZIP=<pinned assets-mov zip>])"
  exit 0
fi
checkout=$(cd "$1" && pwd)
scratch=$(mktemp -d)
trap 'rm -rf -- "$scratch"' EXIT
mkdir "$scratch/w"
cp -r "$patches" "$scratch/w/"
git -C "$scratch" init -q .
if [[ -n ${2:-} ]]; then
  mov=("$(cd "$(dirname "$2")" && pwd)/$(basename "$2")")
else
  mov=(--mov-from "$vendored/animations.sha256")
  echo "vendor-check: SKIP the HEVC animations: no assets-mov zip given, their hashes are the repository's"
fi
(cd "$scratch" && "$python" "$repo/scripts/vendor-dsh-pet.py" "$checkout" w/dsh-pet "${mov[@]}") ||
  fail "vendoring again failed"
diff -r "$scratch/w/dsh-pet" "$vendored" || fail "web/src/companion/dsh-pet is not what the script makes of ${pinned:0:12}"
echo "vendor-check: vendoring again makes the same files"

# A checkout with a change is refused: here a shared clone, so as not to touch
# the given one, with only LICENSE on disk.
git clone -q --shared --no-checkout "$checkout" "$scratch/dirty"
git -C "$scratch/dirty" sparse-checkout set --no-cone /LICENSE
git -C "$scratch/dirty" checkout -q "$pinned"
echo changed >> "$scratch/dirty/LICENSE"
rm -rf "$scratch/w/dsh-pet"
refusal=$(cd "$scratch" && "$python" "$repo/scripts/vendor-dsh-pet.py" "$scratch/dirty" w/dsh-pet "${mov[@]}" 2>&1) &&
  fail "the script vendored from a checkout with a change"
grep -q 'differs from' <<<"$refusal" || fail "the script failed on a checkout with a change, but not for the change: $refusal"
echo "vendor-check: the script refuses a checkout with a change"
