#!/usr/bin/env bash
# Prints the Debian version of the product built from a commit (ADR 0034).
#
# - A release tag on the commit gives the release's version: v1.0.2 is 1.0.2,
#   v1.0.1-rc.7 is 1.0.1~rc.7, which Debian sorts before 1.0.1.
# - A commit after a release tag vX.Y.Z is a development build of the next
#   patch release: X.Y.(Z+1)~dev<date>.<n>+g<commit>, before that release
#   and before its candidates (~dev sorts before ~rc).
# - A commit after a pre-release tag vX.Y.Z-rc.N is a development build of
#   that candidate: X.Y.Z~rc.N+dev<date>.<n>+g<commit>, after rc.N and before
#   rc.N+1 and X.Y.Z. Written as X.Y.Z~dev..., it would sort before rc.N.
#
# <date> is the commit's committer date in UTC (YYYYMMDD) and <n> the number
# of first-parent commits since the tag, so versions only grow along main.
# <commit> is the commit's 12-character abbreviation. Without Git (a copy of
# the tree without .git), the version is 0~dev, which sorts before them all.
#
# Usage: scripts/release-version.sh [REPO]
#        scripts/release-version.sh --check   checks the rule's ordering
set -euo pipefail

# debian_of_tag TAG: the Debian version of a release tag, or nothing for a
# tag that is not vMAJOR.MINOR.PATCH[-PRERELEASE].
debian_of_tag() {
  [[ "$1" =~ ^v([0-9]+\.[0-9]+\.[0-9]+)(-([0-9A-Za-z.]+))?$ ]] || return 1
  printf '%s%s\n' "${BASH_REMATCH[1]}" "${BASH_REMATCH[3]:+~${BASH_REMATCH[3]}}"
}

# dev_version TAG DATE N COMMIT: a development build N first-parent commits
# after TAG.
dev_version() {
  local tag=$1 date=$2 count=$3 commit=$4 base
  base=$(debian_of_tag "$tag") || return 1
  if [[ "$base" == *'~'* ]]; then
    printf '%s+dev%s.%s+g%s\n' "$base" "$date" "$count" "$commit"
  else
    local major minor patch
    IFS=. read -r major minor patch <<<"$base"
    printf '%s.%s.%s~dev%s.%s+g%s\n' "$major" "$minor" "$((patch + 1))" "$date" "$count" "$commit"
  fi
}

if [[ "${1:-}" == --check ]]; then
  failed=0
  expect() {
    local got
    got=$("${@:2}") || got='(none)'
    [[ "$got" == "$1" ]] || { echo "release-version: $* gave $got, not $1" >&2; failed=1; }
  }
  expect 1.0.2 debian_of_tag v1.0.2
  expect 1.0.1~rc.7 debian_of_tag v1.0.1-rc.7
  expect '(none)' debian_of_tag video-edit-results-A-r1
  expect '(none)' debian_of_tag v1.0
  expect 1.0.1~rc.6+dev20261111.3+g083bb9db1234 dev_version v1.0.1-rc.6 20261111 3 083bb9db1234
  expect 1.0.2~dev20261120.1+gabcdefabcdef dev_version v1.0.1 20261120 1 abcdefabcdef
  # Each version sorts after the one before it: the host packages the
  # Experimental NAS has, a candidate, development builds after it, the next
  # candidate, the release, development builds of the next one, its candidate
  # and the next release.
  ordered=(
    0~61744597719c
    0~dev
    1.0.1~rc.6
    "$(dev_version v1.0.1-rc.6 20261111 3 083bb9db1234)"
    "$(dev_version v1.0.1-rc.6 20261111 12 0123456789ab)"
    "$(dev_version v1.0.1-rc.6 20261112 13 fedcba987654)"
    1.0.1~rc.7
    1.0.1
    "$(dev_version v1.0.1 20261120 1 abcdefabcdef)"
    "$(dev_version v1.0.1 20261121 2 000000000000)"
    1.0.2~rc.1
    "$(dev_version v1.0.2-rc.1 20261125 1 111111111111)"
    1.0.2
    1.0.10
  )
  for ((i = 1; i < ${#ordered[@]}; i++)); do
    if ! dpkg --compare-versions "${ordered[i - 1]}" lt "${ordered[i]}"; then
      echo "release-version: ${ordered[i - 1]} does not sort before ${ordered[i]}" >&2
      failed=1
    fi
  done
  # The tag a commit's version follows, in a repository of its own: a v-tag
  # that is not a release (v1.1) is passed over for the release before it.
  scratch=$(mktemp -d)
  trap 'rm -rf -- "$scratch"' EXIT
  in_scratch() {
    GIT_CONFIG_GLOBAL=/dev/null GIT_CONFIG_NOSYSTEM=1 GIT_AUTHOR_NAME=test GIT_AUTHOR_EMAIL=test@invalid \
      GIT_COMMITTER_NAME=test GIT_COMMITTER_EMAIL=test@invalid GIT_AUTHOR_DATE=2026-11-20T10:00:00Z \
      GIT_COMMITTER_DATE=2026-11-20T10:00:00Z git -C "$scratch" "$@" >/dev/null 2>&1
  }
  in_scratch -c init.defaultBranch=main init
  in_scratch commit --allow-empty -m one
  in_scratch tag v1.0.1
  in_scratch commit --allow-empty -m two
  in_scratch tag v1.1
  in_scratch commit --allow-empty -m three
  expect "1.0.2~dev20261120.2+g$(git -C "$scratch" rev-parse --short=12 HEAD)" bash "$0" "$scratch"
  in_scratch tag v1.0.2-rc.1
  expect 1.0.2~rc.1 bash "$0" "$scratch"
  exit "$failed"
fi

repo=${1:-$(cd "$(dirname "$0")/.." && pwd)}
if ! git -C "$repo" rev-parse --verify --quiet HEAD >/dev/null 2>&1; then
  echo 0~dev
  exit 0
fi
# A release tag on the commit itself, the newest if there are several.
while read -r tag; do
  if version=$(debian_of_tag "$tag"); then
    echo "$version"
    exit 0
  fi
done < <(git -C "$repo" tag --points-at HEAD --sort=-version:refname --list 'v[0-9]*')
commit=$(git -C "$repo" rev-parse --short=12 HEAD)
date=$(TZ=UTC git -C "$repo" log -1 --format=%cd --date=format-local:%Y%m%d HEAD)
# The newest release tag the commit follows on its first-parent line. git
# describe names one tag; a v-tag that is not a release, such as v1.1, is
# excluded and the next one asked for.
tag=
excluded=()
while candidate=$(git -C "$repo" describe --tags --first-parent --abbrev=0 --match 'v[0-9]*' "${excluded[@]}" HEAD 2>/dev/null); do
  if debian_of_tag "$candidate" >/dev/null; then
    tag=$candidate
    break
  fi
  excluded+=(--exclude "$candidate")
done
if [[ -z "$tag" ]]; then
  echo "0~dev$date.$(git -C "$repo" rev-list --count --first-parent HEAD)+g$commit"
  exit 0
fi
dev_version "$tag" "$date" "$(git -C "$repo" rev-list --count --first-parent "$tag..HEAD")" "$commit"
