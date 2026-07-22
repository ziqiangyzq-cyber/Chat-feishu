#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd -P)"
BUILD_HELPER="${ROOT_DIR}/scripts/build/build-codex-remote.sh"
GO_BIN="${GO_BIN:-go}"
work_dir="$(mktemp -d)"
sentinel=""

cleanup() {
  local status=$?
  if [[ -n "${sentinel}" ]]; then
    rm -f -- "${sentinel}"
  fi
  rm -rf -- "${work_dir}"
  return "${status}"
}
trap cleanup EXIT

cd "${ROOT_DIR}"
commit="$(git rev-parse --verify HEAD^{commit})"
version="v0.0.0-provenance.1"
built_at="2026-07-22T00:00:00Z"
dirty=false
if [[ -n "$(git status --porcelain --untracked-files=normal)" ]]; then
  dirty=true
fi
embed_fingerprint_before="$(
  sha256sum \
    internal/externalaccess/cloudflaredembed/cloudflared_linux_amd64_embed.go \
    internal/externalaccess/cloudflaredembed/assets/cloudflared-linux-amd64.zst \
    internal/managedshim/embed/managed_shim_linux_amd64_embed.go \
    internal/managedshim/embed/assets/vscode-shim-linux-amd64.zst \
    internal/upgradeshim/embed/upgrade_shim_linux_amd64_embed.go \
    internal/upgradeshim/embed/assets/upgrade-shim-linux-amd64.zst
)"

GO_BIN="${GO_BIN}" bash "${BUILD_HELPER}" \
  --output "${work_dir}/codex-remote" \
  --version "${version}" \
  --branch provenance-selftest \
  --flavor dev \
  --expected-ref "${commit}" \
  --build-time "${built_at}"

expected="codex-remote version=${version} commit=${commit} built_at=${built_at} dirty=${dirty} branch=provenance-selftest flavor=dev"
[[ "$("${work_dir}/codex-remote" --version-detail)" == "${expected}" ]]
[[ "$("${work_dir}/codex-remote" --version)" == "${version}" ]]
[[ "$("${work_dir}/codex-remote" version)" == "${version}" ]]
embed_fingerprint_after="$(
  sha256sum \
    internal/externalaccess/cloudflaredembed/cloudflared_linux_amd64_embed.go \
    internal/externalaccess/cloudflaredembed/assets/cloudflared-linux-amd64.zst \
    internal/managedshim/embed/managed_shim_linux_amd64_embed.go \
    internal/managedshim/embed/assets/vscode-shim-linux-amd64.zst \
    internal/upgradeshim/embed/upgrade_shim_linux_amd64_embed.go \
    internal/upgradeshim/embed/assets/upgrade-shim-linux-amd64.zst
)"
[[ "${embed_fingerprint_after}" == "${embed_fingerprint_before}" ]] || {
  echo "build provenance selftest: shared build mutated live embed assets" >&2
  exit 1
}

if CGO_ENABLED=0 GOOS=linux GOARCH=amd64 "${GO_BIN}" list -buildvcs=false -tags codex_remote_upgrade_shim -deps \
  -f '{{range .EmbedFiles}}{{$.Dir}}/{{.}}{{"\n"}}{{end}}' ./cmd/upgrade-shim |
  grep -E '/internal/(managedshim|upgradeshim)/embed/assets/' >/dev/null; then
  echo "build provenance selftest: upgrade shim recursively embeds shim assets" >&2
  exit 1
fi

if [[ "${dirty}" == "false" ]]; then
  sentinel="${ROOT_DIR}/.codex-remote-provenance-selftest-$$"
  : > "${sentinel}"
fi
if GO_BIN="${GO_BIN}" bash "${BUILD_HELPER}" \
  --output "${work_dir}/dirty-rejected" \
  --version "${version}" \
  --expected-ref "${commit}" \
  --require-clean >"${work_dir}/dirty.log" 2>&1; then
  echo "build provenance selftest: dirty deployment build was accepted" >&2
  exit 1
fi
grep -F 'require a clean worktree' "${work_dir}/dirty.log" >/dev/null
if [[ -n "${sentinel}" ]]; then
  rm -f -- "${sentinel}"
  sentinel=""
fi

if GO_BIN="${GO_BIN}" bash "${BUILD_HELPER}" \
  --output "${work_dir}/branch-rejected" \
  --version "${version}" \
  --expected-ref definitely-not-an-exact-ref >"${work_dir}/branch.log" 2>&1; then
  echo "build provenance selftest: branch-like source ref was accepted" >&2
  exit 1
fi
grep -F 'must be a full commit id or exact tag' "${work_dir}/branch.log" >/dev/null

if GO_BIN="${GO_BIN}" bash "${BUILD_HELPER}" \
  --output "${work_dir}/untagged-shipping-rejected" \
  --version v9.9.9 \
  --flavor shipping \
  --expected-ref "${commit}" >"${work_dir}/untagged-shipping.log" 2>&1; then
  echo "build provenance selftest: untagged shipping version was accepted" >&2
  exit 1
fi
grep -F 'must already exist as an exact tag' "${work_dir}/untagged-shipping.log" >/dev/null

cat > "${work_dir}/fake-git" <<EOF
#!/usr/bin/env bash
set -euo pipefail
case "\${1:-}" in
  rev-parse) printf '%s\n' '${commit}' ;;
  show-ref) exit 0 ;;
  *) exit 1 ;;
esac
EOF
chmod +x "${work_dir}/fake-git"
if GO_BIN=/bin/true GIT_BIN="${work_dir}/fake-git" bash "${BUILD_HELPER}" \
  --output "${work_dir}/tag-rejected" \
  --version v9.9.9 \
  --expected-ref v1.2.3 >"${work_dir}/tag.log" 2>&1; then
  echo "build provenance selftest: mismatched tag/version was accepted" >&2
  exit 1
fi
grep -F 'does not match exact source tag' "${work_dir}/tag.log" >/dev/null

echo "build provenance selftest: ok"
