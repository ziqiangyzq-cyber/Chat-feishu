#!/usr/bin/env bash
set -euo pipefail

root_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
script_path="${root_dir}/scripts/release/pin-release-tag.sh"
work_dir="$(mktemp -d)"

cleanup() {
  rm -rf "${work_dir}"
}
trap cleanup EXIT

remote_dir="${work_dir}/remote.git"
publisher_dir="${work_dir}/publisher"
racer_dir="${work_dir}/racer"

git init -q --bare "${remote_dir}"
git clone -q "${remote_dir}" "${publisher_dir}"
git -C "${publisher_dir}" config user.name "Release Tag Tests"
git -C "${publisher_dir}" config user.email "tests@example.com"
printf 'first\n' > "${publisher_dir}/README.md"
git -C "${publisher_dir}" add README.md
git -C "${publisher_dir}" commit -qm "feat: first release"
git -C "${publisher_dir}" push -q origin HEAD:master
first_commit="$(git -C "${publisher_dir}" rev-parse HEAD)"

git clone -q "${remote_dir}" "${racer_dir}"

ROOT_DIR_OVERRIDE="${publisher_dir}" bash "${script_path}" v1.2.3 "${first_commit}" >/dev/null
ROOT_DIR_OVERRIDE="${publisher_dir}" bash "${script_path}" v1.2.3 "${first_commit}" >/dev/null
ROOT_DIR_OVERRIDE="${racer_dir}" bash "${script_path}" v1.2.3 "${first_commit}" >/dev/null 2>&1

remote_commit="$(git --git-dir="${remote_dir}" rev-parse 'refs/tags/v1.2.3^{commit}')"
[[ "${remote_commit}" == "${first_commit}" ]]

printf 'second\n' >> "${publisher_dir}/README.md"
git -C "${publisher_dir}" add README.md
git -C "${publisher_dir}" commit -qm "fix: prepare next release"
second_commit="$(git -C "${publisher_dir}" rev-parse HEAD)"

conflict_log="${work_dir}/conflict.log"
if ROOT_DIR_OVERRIDE="${publisher_dir}" bash "${script_path}" v1.2.3 "${second_commit}" >"${conflict_log}" 2>&1; then
  echo "expected mismatched release tag to fail" >&2
  exit 1
fi
grep -F "points to ${first_commit}, expected ${second_commit}" "${conflict_log}" >/dev/null

echo "release tag pin selftest: ok"
