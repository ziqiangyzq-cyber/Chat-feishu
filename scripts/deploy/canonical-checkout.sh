#!/usr/bin/env bash
set -euo pipefail

GIT_BIN="${GIT_BIN:-git}"
repository="https://github.com/ziqiangyzq-cyber/Chat-feishu.git"
checkout_path="${HOME}/deploy/Chat-feishu"
source_ref=""
made_writable=0
managed_marker=""

usage() {
  cat <<'EOF'
usage: ./deploy-local-release.sh canonical-checkout --ref <tag|commit> [options]

Create or update one detached, exact-ref deployment checkout and make its
working tree and git metadata read-only between operator updates.

options:
  --ref <tag|commit>   exact tag or full commit (required; branches rejected)
  --checkout <path>    default ~/deploy/Chat-feishu
  --repository <url>   canonical clone/fetch source
  -h, --help           show this help
EOF
}

die() {
  echo "canonical-checkout: $*" >&2
  exit 1
}

restore_read_only() {
  local status=$?
  if [[ "${made_writable}" == "1" && -d "${checkout_path}" ]]; then
    find "${checkout_path}" -type f -exec chmod a-w {} + 2>/dev/null || true
    find "${checkout_path}" -type d -exec chmod a-w {} + 2>/dev/null || true
  fi
  return "${status}"
}
trap restore_read_only EXIT

while [[ $# -gt 0 ]]; do
  case "$1" in
    --ref)
      [[ $# -ge 2 ]] || die "missing value for --ref"
      source_ref="$2"
      shift 2
      ;;
    --checkout)
      [[ $# -ge 2 ]] || die "missing value for --checkout"
      checkout_path="$2"
      shift 2
      ;;
    --repository)
      [[ $# -ge 2 ]] || die "missing value for --repository"
      repository="$2"
      shift 2
      ;;
    -h|--help)
      usage
      exit 0
      ;;
    *) die "unknown argument: $1" ;;
  esac
done

[[ -n "${source_ref}" ]] || die "--ref is required"
[[ "${checkout_path}" == /* ]] || die "--checkout must be absolute"
while [[ "${checkout_path}" != "/" && "${checkout_path}" == */ ]]; do
  checkout_path="${checkout_path%/}"
done
[[ "${checkout_path}" != "/" && "${checkout_path}" != "${HOME}" ]] || die "--checkout is too broad"
[[ "${checkout_path}" != *$'\n'* && "${checkout_path}" != *'/../'* && "${checkout_path}" != */.. && "${checkout_path}" != *'/./'* && "${checkout_path}" != */. ]] || {
  die "--checkout must not contain relative path components"
}
[[ -n "${repository}" ]] || die "--repository is required"
managed_marker="${checkout_path}/.git/codex-remote-deployment-checkout"

if [[ "${source_ref}" =~ ^[0-9a-fA-F]{40,64}$ ]]; then
  :
elif [[ "${source_ref}" =~ ^[A-Za-z0-9._+-]+$ ]]; then
  :
else
  die "--ref contains unsupported characters"
fi

if [[ -e "${checkout_path}" ]]; then
  [[ ! -L "${checkout_path}" && -d "${checkout_path}/.git" && ! -L "${checkout_path}/.git" ]] || die "existing checkout is not a standalone Git worktree: ${checkout_path}"
  [[ -f "${managed_marker}" && ! -L "${managed_marker}" ]] || die "existing checkout is not managed by the canonical deployment helper"
  chmod -R u+w "${checkout_path}"
  made_writable=1
  checkout_status="$("${GIT_BIN}" -C "${checkout_path}" status --porcelain --untracked-files=normal)" || die "unable to inspect canonical checkout"
  [[ -z "${checkout_status}" ]] || die "canonical checkout has manual changes; inspect them before convergence"
  existing_repository="$("${GIT_BIN}" -C "${checkout_path}" remote get-url origin)"
  [[ "${existing_repository}" == "${repository}" ]] || die "existing origin does not match the requested repository"
else
  mkdir -p "$(dirname "${checkout_path}")"
  mkdir "${checkout_path}"
  made_writable=1
  "${GIT_BIN}" -C "${checkout_path}" init
  "${GIT_BIN}" -C "${checkout_path}" remote add origin "${repository}"
  printf 'state=initializing\n' > "${managed_marker}"
fi

if [[ "${source_ref}" =~ ^[0-9a-fA-F]{40,64}$ ]]; then
  "${GIT_BIN}" -C "${checkout_path}" fetch --no-tags origin "${source_ref}"
  commit="$("${GIT_BIN}" -C "${checkout_path}" rev-parse --verify FETCH_HEAD^{commit})"
  [[ "${commit,,}" == "${source_ref,,}" ]] || die "fetched commit does not match the requested full commit"
else
  "${GIT_BIN}" -C "${checkout_path}" fetch --no-tags origin "refs/tags/${source_ref}:refs/tags/${source_ref}"
  "${GIT_BIN}" -C "${checkout_path}" show-ref --verify --quiet "refs/tags/${source_ref}" || die "exact tag was not fetched: ${source_ref}"
  commit="$("${GIT_BIN}" -C "${checkout_path}" rev-parse --verify "${source_ref}^{commit}")"
fi

commit="${commit,,}"
[[ "${commit}" =~ ^[0-9a-f]{40,64}$ ]] || die "ref did not resolve to a full commit"
"${GIT_BIN}" -C "${checkout_path}" checkout --detach "${commit}"
[[ "$("${GIT_BIN}" -C "${checkout_path}" rev-parse --verify HEAD^{commit})" == "${commit}" ]] || die "checkout verification failed"
checkout_status="$("${GIT_BIN}" -C "${checkout_path}" status --porcelain --untracked-files=normal)" || die "unable to verify canonical checkout"
[[ -z "${checkout_status}" ]] || die "checkout is not clean after exact-ref update"

printf 'state=ready\nref=%s\ncommit=%s\nupdated_at=%s\n' "${source_ref}" "${commit}" "$(date -u +%Y-%m-%dT%H:%M:%SZ)" > "${managed_marker}"
find "${checkout_path}" -type f -exec chmod a-w {} +
find "${checkout_path}" -type d -exec chmod a-w {} +
made_writable=0
trap - EXIT

printf 'canonical checkout ready: path=%s commit=%s mode=read-only\n' "${checkout_path}" "${commit}"
