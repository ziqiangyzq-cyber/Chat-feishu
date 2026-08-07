#!/usr/bin/env bash
set -euo pipefail

if [[ "$(uname -s)" != "Darwin" ]]; then
  printf 'macOS stable code identity selftest: skipped (non-macOS host)\n'
  exit 0
fi

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd -P)"
BUILD_HELPER="${ROOT_DIR}/scripts/build/build-codex-remote.sh"
work_dir="$(mktemp -d)"

cleanup() {
  rm -rf "${work_dir}"
}
trap cleanup EXIT

identifier="com.kxn.codex-remote"
first="${work_dir}/first"
second="${work_dir}/second"

CODEX_REMOTE_BUILD_TIME_UTC=2026-08-07T00:00:00Z \
  bash "${BUILD_HELPER}" --output "${first}" >/dev/null
CODEX_REMOTE_BUILD_TIME_UTC=2026-08-07T00:00:01Z \
  bash "${BUILD_HELPER}" --output "${second}" >/dev/null

first_cdhash="$(codesign -dvvv "${first}" 2>&1 | awk -F= '/^CDHash=/{print $2; exit}')"
second_cdhash="$(codesign -dvvv "${second}" 2>&1 | awk -F= '/^CDHash=/{print $2; exit}')"
[[ -n "${first_cdhash}" && -n "${second_cdhash}" ]] || {
  echo "macOS stable code identity selftest: missing CDHash" >&2
  exit 1
}
[[ "${first_cdhash}" != "${second_cdhash}" ]] || {
  echo "macOS stable code identity selftest: fixtures unexpectedly share a CDHash" >&2
  exit 1
}

for binary in "${first}" "${second}"; do
  codesign --verify --strict "${binary}"
  requirement="$(codesign -d -r- "${binary}" 2>&1 | tail -n 1)"
  [[ "${requirement}" == "designated => identifier \"${identifier}\"" ]] || {
    echo "macOS stable code identity selftest: unexpected requirement: ${requirement}" >&2
    exit 1
  }
done

printf 'macOS stable code identity selftest: ok (%s; changing CDHash, stable DR)\n' "${identifier}"
