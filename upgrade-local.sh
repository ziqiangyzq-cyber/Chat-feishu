#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd -P)"
BIN_DIR="${ROOT_DIR}/bin"
BUILD_OUTPUT="${BIN_DIR}/codex-remote"
GO_BIN="${GO_BIN:-go}"
BUILD_HELPER="${ROOT_DIR}/scripts/build/build-codex-remote.sh"
BASE_DIR=""
INSTANCE=""
UPGRADE_SLOT=""
ALLOW_DIRTY=0
BASE_DIR_SET=0
REPO_TARGET_SCRIPT="${ROOT_DIR}/scripts/install/repo-install-target.sh"

usage() {
  cat <<'EOF'
usage: ./upgrade-local.sh [--instance <id>] [--base-dir <dir>] [--slot <slot>] [--allow-dirty]

Pull the current branch to the latest upstream commit, rebuild ./bin/codex-remote,
stage it into the fixed local-upgrade artifact path, and trigger the built-in
local upgrade transaction against the installed daemon state.

options:
  --instance <id>   override the repo install target instance
  --base-dir <dir>  override the install base dir resolved for that instance
  --slot <slot>     optional explicit upgrade slot label
  --allow-dirty     deprecated compatibility flag; dirty deployment still fails
  -h, --help        show this help text
EOF
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --instance)
      [[ $# -ge 2 ]] || { echo "missing value for --instance" >&2; exit 1; }
      INSTANCE="$2"
      shift 2
      ;;
    --base-dir)
      [[ $# -ge 2 ]] || { echo "missing value for --base-dir" >&2; exit 1; }
      BASE_DIR="$2"
      BASE_DIR_SET=1
      shift 2
      ;;
    --slot)
      [[ $# -ge 2 ]] || { echo "missing value for --slot" >&2; exit 1; }
      UPGRADE_SLOT="$2"
      shift 2
      ;;
    --allow-dirty)
      ALLOW_DIRTY=1
      shift
      ;;
    -h|--help)
      usage
      exit 0
      ;;
    *)
      echo "unknown argument: $1" >&2
      usage >&2
      exit 1
      ;;
  esac
done

cd "${ROOT_DIR}"

WORKTREE_STATUS="$(git status --porcelain --untracked-files=normal)" || { echo "unable to inspect working tree" >&2; exit 1; }
if ! git diff --quiet --ignore-submodules -- || ! git diff --cached --quiet --ignore-submodules -- || [[ -n "${WORKTREE_STATUS}" ]]; then
  echo "working tree has uncommitted changes; deployment builds must be clean" >&2
  exit 1
fi
if [[ "${ALLOW_DIRTY}" == "1" ]]; then
  echo "warning: --allow-dirty is deprecated and no longer bypasses deployment provenance checks" >&2
fi

printf '[1/5] git pull --ff-only\n'
git pull --ff-only

printf '[2/5] resolve repo install target\n'
resolver_args=()
if [[ -n "${INSTANCE}" ]]; then
  resolver_args+=("--instance" "${INSTANCE}")
fi
if [[ "${BASE_DIR_SET}" == "1" ]]; then
  resolver_args+=("--base-dir" "${BASE_DIR}")
fi
eval "$("${REPO_TARGET_SCRIPT}" --format shell "${resolver_args[@]}")"

printf 'target instance: %s\n' "${CODEX_REMOTE_TARGET_INSTANCE_ID}"
printf 'target state: %s\n' "${CODEX_REMOTE_TARGET_STATE_PATH}"
printf 'target log: %s\n' "${CODEX_REMOTE_TARGET_LOG_PATH}"
printf 'target admin: %s\n' "${CODEX_REMOTE_TARGET_ADMIN_URL}"

printf '[3/5] build %s\n' "${BUILD_OUTPUT}"
mkdir -p "${BIN_DIR}"
SOURCE_COMMIT="$(git rev-parse --verify HEAD^{commit})"
GO_BIN="${GO_BIN}" bash "${BUILD_HELPER}" \
  --output "${BUILD_OUTPUT}" \
  --expected-ref "${SOURCE_COMMIT}" \
  --flavor dev \
  --require-clean

if [[ ! -f "${CODEX_REMOTE_TARGET_STATE_PATH}" ]]; then
  echo "install state not found: ${CODEX_REMOTE_TARGET_STATE_PATH}" >&2
  echo "build ./bin/codex-remote and run './bin/codex-remote install -bootstrap-only -start-daemon' first, or pass --base-dir for the installed environment" >&2
  exit 1
fi

printf '[4/5] stage local artifact %s\n' "${CODEX_REMOTE_TARGET_LOCAL_UPGRADE_ARTIFACT_PATH}"
mkdir -p "$(dirname "${CODEX_REMOTE_TARGET_LOCAL_UPGRADE_ARTIFACT_PATH}")"
cp "${BUILD_OUTPUT}" "${CODEX_REMOTE_TARGET_LOCAL_UPGRADE_ARTIFACT_PATH}"
chmod +x "${CODEX_REMOTE_TARGET_LOCAL_UPGRADE_ARTIFACT_PATH}"

printf '[5/5] request built-in local upgrade transaction\n'
unset http_proxy https_proxy HTTP_PROXY HTTPS_PROXY ALL_PROXY all_proxy

cmd=("${BUILD_OUTPUT}" local-upgrade "-state-path" "${CODEX_REMOTE_TARGET_STATE_PATH}")
if [[ -n "${UPGRADE_SLOT}" ]]; then
  cmd+=("-slot" "${UPGRADE_SLOT}")
fi
CODEX_REMOTE_REPO_ROOT="${ROOT_DIR}" "${cmd[@]}"
