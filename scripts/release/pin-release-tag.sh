#!/usr/bin/env bash
set -euo pipefail

version="${1:-}"
source_ref="${2:-}"
git_bin="${GIT_BIN:-git}"
remote="${RELEASE_REMOTE:-origin}"
root_dir="${ROOT_DIR_OVERRIDE:-$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)}"

[[ -n "${version}" ]] || {
  echo "usage: pin-release-tag.sh <version> <source-commit>" >&2
  exit 2
}
[[ -n "${source_ref}" ]] || {
  echo "usage: pin-release-tag.sh <version> <source-commit>" >&2
  exit 2
}

cd "${root_dir}"
"${git_bin}" check-ref-format "refs/tags/${version}" >/dev/null || {
  echo "Invalid release tag: ${version}" >&2
  exit 1
}

source_commit="$("${git_bin}" rev-parse --verify "${source_ref}^{commit}")"

if ! "${git_bin}" show-ref --verify --quiet "refs/tags/${version}"; then
  "${git_bin}" tag "${version}" "${source_commit}"
  if ! "${git_bin}" push "${remote}" "refs/tags/${version}"; then
    "${git_bin}" tag --delete "${version}" >/dev/null
    "${git_bin}" fetch --force "${remote}" "refs/tags/${version}:refs/tags/${version}"
  fi
fi

tag_commit="$("${git_bin}" rev-parse --verify "refs/tags/${version}^{commit}")"
[[ "${tag_commit}" == "${source_commit}" ]] || {
  echo "Release tag ${version} points to ${tag_commit}, expected ${source_commit}." >&2
  exit 1
}

"${git_bin}" ls-remote --exit-code --tags "${remote}" "refs/tags/${version}" >/dev/null || {
  echo "Release tag ${version} is not present on remote ${remote}." >&2
  exit 1
}

printf 'release tag pinned: version=%s commit=%s remote=%s\n' "${version}" "${source_commit}" "${remote}"
