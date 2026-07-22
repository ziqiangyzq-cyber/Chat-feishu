#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd -P)"
HELPER="${ROOT_DIR}/scripts/deploy/canonical-checkout.sh"
GIT_BIN="${GIT_BIN:-git}"
work_dir="$(mktemp -d)"

cleanup() {
  local status=$?
  chmod -R u+w -- "${work_dir}" 2>/dev/null || true
  rm -rf -- "${work_dir}"
  return "${status}"
}
trap cleanup EXIT

seed="${work_dir}/seed"
remote="${work_dir}/remote.git"
tag_checkout="${work_dir}/tag-checkout"
commit_checkout="${work_dir}/commit-checkout"
branch_checkout="${work_dir}/branch-checkout"
unmanaged_checkout="${work_dir}/unmanaged-checkout"
symlink_checkout="${work_dir}/symlink-checkout"

"${GIT_BIN}" init -q "${seed}"
"${GIT_BIN}" -C "${seed}" config user.name "Canonical Checkout Selftest"
"${GIT_BIN}" -C "${seed}" config user.email "canonical-checkout-selftest@example.invalid"
printf '%s\n' "exact source" > "${seed}/source.txt"
"${GIT_BIN}" -C "${seed}" add source.txt
"${GIT_BIN}" -C "${seed}" commit -q -m "exact source"
"${GIT_BIN}" -C "${seed}" tag -a v1.2.3 -m "v1.2.3"
commit="$("${GIT_BIN}" -C "${seed}" rev-parse HEAD^{commit})"
"${GIT_BIN}" clone -q --bare "${seed}" "${remote}"

bash "${HELPER}" --ref v1.2.3 --checkout "${tag_checkout}" --repository "${remote}" > "${work_dir}/tag.log"
[[ "$("${GIT_BIN}" -C "${tag_checkout}" rev-parse HEAD^{commit})" == "${commit}" ]]
[[ -z "$("${GIT_BIN}" -C "${tag_checkout}" status --porcelain --untracked-files=normal)" ]]
[[ "$("${GIT_BIN}" -C "${tag_checkout}" symbolic-ref -q HEAD || true)" == "" ]]
[[ "$("${GIT_BIN}" -C "${tag_checkout}" for-each-ref --format='%(refname)')" == "refs/tags/v1.2.3" ]]
grep -F 'state=ready' "${tag_checkout}/.git/codex-remote-deployment-checkout" >/dev/null
if find "${tag_checkout}" -perm /222 -print -quit | grep -q .; then
  echo "canonical checkout selftest: tag checkout is writable" >&2
  exit 1
fi

bash "${HELPER}" --ref "${commit}" --checkout "${commit_checkout}" --repository "${remote}" > "${work_dir}/commit.log"
[[ "$("${GIT_BIN}" -C "${commit_checkout}" rev-parse HEAD^{commit})" == "${commit}" ]]
[[ -z "$("${GIT_BIN}" -C "${commit_checkout}" for-each-ref --format='%(refname)')" ]]
if find "${commit_checkout}" -perm /222 -print -quit | grep -q .; then
  echo "canonical checkout selftest: commit checkout is writable" >&2
  exit 1
fi

if bash "${HELPER}" --ref master --checkout "${branch_checkout}" --repository "${remote}" > "${work_dir}/branch.log" 2>&1; then
  echo "canonical checkout selftest: branch ref was accepted" >&2
  exit 1
fi

"${GIT_BIN}" init -q "${unmanaged_checkout}"
"${GIT_BIN}" -C "${unmanaged_checkout}" remote add origin "${remote}"
if bash "${HELPER}" --ref v1.2.3 --checkout "${unmanaged_checkout}" --repository "${remote}" > "${work_dir}/unmanaged.log" 2>&1; then
  echo "canonical checkout selftest: unmanaged checkout was adopted" >&2
  exit 1
fi
grep -F 'existing checkout is not managed by the canonical deployment helper' "${work_dir}/unmanaged.log" >/dev/null

ln -s "${tag_checkout}" "${symlink_checkout}"
if bash "${HELPER}" --ref v1.2.3 --checkout "${symlink_checkout}" --repository "${remote}" > "${work_dir}/symlink.log" 2>&1; then
  echo "canonical checkout selftest: symlink checkout was accepted" >&2
  exit 1
fi
grep -F 'existing checkout is not a standalone Git worktree' "${work_dir}/symlink.log" >/dev/null

if bash "${HELPER}" --ref v1.2.3 --checkout "${tag_checkout}" --repository "${work_dir}/other.git" > "${work_dir}/origin.log" 2>&1; then
  echo "canonical checkout selftest: mismatched origin was accepted" >&2
  exit 1
fi
grep -F 'existing origin does not match the requested repository' "${work_dir}/origin.log" >/dev/null
if grep -F "${remote}" "${work_dir}/origin.log" >/dev/null; then
  echo "canonical checkout selftest: origin mismatch leaked the configured URL" >&2
  exit 1
fi

echo "canonical checkout selftest: ok"
