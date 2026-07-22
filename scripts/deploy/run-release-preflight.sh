#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd -P)"
GO_BIN="${GO_BIN:-go}"
if [[ "${GO_BIN}" != */* ]]; then
  GO_BIN="$(command -v "${GO_BIN}" || true)"
fi
[[ -x "${GO_BIN}" ]] || { echo "release preflight: Go toolchain not found" >&2; exit 1; }
export PATH="$(dirname "${GO_BIN}"):${PATH}"
unset http_proxy https_proxy HTTP_PROXY HTTPS_PROXY ALL_PROXY all_proxy

cd "${ROOT_DIR}"

bash scripts/check/no-local-paths.sh
bash scripts/check/no-legacy-names.sh
bash scripts/check/feishu-call-broker.sh
bash scripts/check/eventcontract-legacy-guards.sh
bash scripts/check/go-file-length.sh
bash scripts/check/go-format.sh
syntax_files=(
  deploy-local-release.sh
  scripts/build/build-codex-remote.sh
  scripts/check/build-provenance-selftest.sh
  scripts/check/release-reproducibility-selftest.sh
  scripts/check/canonical-checkout-selftest.sh
  scripts/check/unified-local-release-selftest.sh
  scripts/deploy/canonical-checkout.sh
  scripts/deploy/local-release.sh
  scripts/deploy/run-release-preflight.sh
  upgrade-local.sh
  upgrade-self.sh
)
for syntax_file in "${syntax_files[@]}"; do
  bash -n "${syntax_file}"
done
bash scripts/check/canonical-checkout-selftest.sh
bash scripts/check/build-provenance-selftest.sh
bash scripts/check/release-reproducibility-selftest.sh
bash scripts/check/unified-local-release-selftest.sh
"${GO_BIN}" test ./...
