#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd -P)"
GO_BIN="${GO_BIN:-go}"
GIT_BIN="${GIT_BIN:-git}"
snapshot_dir=""

cleanup() {
  local status=$?
  if [[ -n "${snapshot_dir}" && -d "${snapshot_dir}" ]]; then
    rm -rf "${snapshot_dir}"
  fi
  return "${status}"
}
trap cleanup EXIT

if [[ "${GO_BIN}" != */* ]]; then
  GO_BIN="$(command -v "${GO_BIN}" || true)"
fi
[[ -x "${GO_BIN}" ]] || { echo "build-codex-remote: Go toolchain not found" >&2; exit 1; }
export PATH="$(dirname "${GO_BIN}"):${PATH}"

output=""
version=""
branch=""
flavor=""
expected_ref=""
build_time_utc=""
target_goos=""
target_goarch=""
require_clean=0

usage() {
  cat <<'EOF'
usage: scripts/build/build-codex-remote.sh --output <path> [options]

Build codex-remote with explicit source provenance. This is the only repository
helper that should assemble codex-remote ldflags.

options:
  --output <path>       required binary output path
  --version <value>     semantic version/tag; defaults to an exact tag or dev-<sha>
  --branch <value>      source branch label; defaults to current branch or detached
  --flavor <value>      dev, alpha, or shipping; defaults to dev
  --expected-ref <ref>  require HEAD to equal this full commit or exact tag
  --build-time <UTC>    RFC3339 UTC timestamp; defaults to the current UTC time
  --goos <value>        target GOOS; defaults to go env GOOS
  --goarch <value>      target GOARCH; defaults to go env GOARCH
  --require-clean       reject dirty source before and after embed preparation
  -h, --help            show this help
EOF
}

die() {
  echo "build-codex-remote: $*" >&2
  exit 1
}

require_value() {
  local option="$1"
  local value="${2:-}"
  [[ -n "${value}" ]] || die "missing value for ${option}"
}

validate_field() {
  local name="$1"
  local value="$2"
  [[ "${value}" =~ ^[A-Za-z0-9][A-Za-z0-9._/+:-]*$ ]] || die "${name} contains unsupported characters: ${value}"
}

is_semantic_version() {
  [[ "$1" =~ ^v?[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?(\+[0-9A-Za-z.-]+)?$ ]]
}

sha256_file() {
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum "$1" | awk '{print $1}'
    return
  fi
  shasum -a 256 "$1" | awk '{print $1}'
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --output)
      require_value "$1" "${2:-}"
      output="$2"
      shift 2
      ;;
    --version)
      require_value "$1" "${2:-}"
      version="$2"
      shift 2
      ;;
    --branch)
      require_value "$1" "${2:-}"
      branch="$2"
      shift 2
      ;;
    --flavor)
      require_value "$1" "${2:-}"
      flavor="$2"
      shift 2
      ;;
    --expected-ref)
      require_value "$1" "${2:-}"
      expected_ref="$2"
      shift 2
      ;;
    --build-time)
      require_value "$1" "${2:-}"
      build_time_utc="$2"
      shift 2
      ;;
    --goos)
      require_value "$1" "${2:-}"
      target_goos="$2"
      shift 2
      ;;
    --goarch)
      require_value "$1" "${2:-}"
      target_goarch="$2"
      shift 2
      ;;
    --require-clean)
      require_clean=1
      shift
      ;;
    -h|--help)
      usage
      exit 0
      ;;
    *)
      die "unknown argument: $1"
      ;;
  esac
done

[[ -n "${output}" ]] || die "--output is required"

cd "${ROOT_DIR}"
if [[ "${output}" != /* ]]; then
  output="${ROOT_DIR}/${output#./}"
fi

head_commit="$("${GIT_BIN}" rev-parse --verify HEAD^{commit})"
[[ "${head_commit}" =~ ^[0-9a-fA-F]{40,64}$ ]] || die "HEAD did not resolve to a full commit id"
head_commit="${head_commit,,}"

if [[ -n "${expected_ref}" ]]; then
  exact_ref=0
  if [[ "${expected_ref}" =~ ^[0-9a-fA-F]{40,64}$ ]]; then
    exact_ref=1
  elif "${GIT_BIN}" show-ref --verify --quiet "refs/tags/${expected_ref}"; then
    exact_ref=1
  fi
  [[ "${exact_ref}" == "1" ]] || die "--expected-ref must be a full commit id or exact tag, not a branch: ${expected_ref}"
  expected_commit="$("${GIT_BIN}" rev-parse --verify "${expected_ref}^{commit}")"
  expected_commit="${expected_commit,,}"
  [[ "${expected_commit}" == "${head_commit}" ]] || die "HEAD ${head_commit} does not match ${expected_ref} (${expected_commit})"
  if "${GIT_BIN}" show-ref --verify --quiet "refs/tags/${expected_ref}" && [[ -n "${version}" && "${version}" != "${expected_ref}" ]]; then
    die "version ${version} does not match exact source tag ${expected_ref}"
  fi
fi

worktree_status="$("${GIT_BIN}" status --porcelain --untracked-files=normal)" || die "unable to inspect source worktree"
if [[ "${require_clean}" == "1" && -n "${worktree_status}" ]]; then
  die "deployment/release builds require a clean worktree"
fi

if [[ -z "${version}" ]]; then
  mapfile -t exact_tags < <("${GIT_BIN}" tag --points-at "${head_commit}" | sed '/^[[:space:]]*$/d' | sort)
  if [[ "${#exact_tags[@]}" -eq 1 ]]; then
    version="${exact_tags[0]}"
  elif [[ "${#exact_tags[@]}" -gt 1 ]]; then
    die "multiple tags point at HEAD; pass --version explicitly"
  else
    version="dev-${head_commit:0:12}"
  fi
fi

if [[ -z "${branch}" ]]; then
  branch="${CODEX_REMOTE_BUILD_BRANCH:-}"
fi
if [[ -z "${branch}" ]]; then
  branch="$("${GIT_BIN}" branch --show-current)"
fi
branch="${branch:-detached}"
flavor="${flavor:-${CODEX_REMOTE_BUILD_FLAVOR:-dev}}"
build_time_utc="${build_time_utc:-${CODEX_REMOTE_BUILD_TIME_UTC:-$(date -u +%Y-%m-%dT%H:%M:%SZ)}}"
target_goos="${target_goos:-$("${GO_BIN}" env GOOS)}"
target_goarch="${target_goarch:-$("${GO_BIN}" env GOARCH)}"

validate_field "version" "${version}"
validate_field "branch" "${branch}"
case "${flavor}" in
  dev|alpha|shipping) ;;
  *) die "unsupported build flavor: ${flavor}" ;;
esac
if [[ "${flavor}" != "dev" ]] && ! is_semantic_version "${version}"; then
  die "${flavor} builds require a semantic version"
fi
if [[ "${flavor}" != "dev" && "${CODEX_REMOTE_ALLOW_UNTAGGED_FIXTURE:-0}" != "1" ]]; then
  "${GIT_BIN}" show-ref --verify --quiet "refs/tags/${version}" || die "${flavor} build version must already exist as an exact tag: ${version}"
  version_tag_commit="$("${GIT_BIN}" rev-parse --verify "refs/tags/${version}^{commit}")"
  version_tag_commit="${version_tag_commit,,}"
  [[ "${version_tag_commit}" == "${head_commit}" ]] || die "version tag ${version} points to ${version_tag_commit}, not HEAD ${head_commit}"
fi
[[ "${build_time_utc}" =~ ^[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}Z$ ]] || die "--build-time must be RFC3339 UTC (YYYY-MM-DDTHH:MM:SSZ)"
[[ "${target_goos}" =~ ^[a-z0-9]+$ ]] || die "invalid GOOS: ${target_goos}"
[[ "${target_goarch}" =~ ^[a-z0-9]+$ ]] || die "invalid GOARCH: ${target_goarch}"

dirty=false
if [[ -n "${worktree_status}" ]]; then
  dirty=true
fi

mkdir -p "$(dirname "${output}")"
ldflags=(
  "-X github.com/kxn/codex-remote-feishu/internal/buildinfo.VersionValue=${version}"
  "-X github.com/kxn/codex-remote-feishu/internal/buildinfo.BranchValue=${branch}"
  "-X github.com/kxn/codex-remote-feishu/internal/buildinfo.CommitValue=${head_commit}"
  "-X github.com/kxn/codex-remote-feishu/internal/buildinfo.BuildTimeUTCValue=${build_time_utc}"
  "-X github.com/kxn/codex-remote-feishu/internal/buildinfo.DirtyValue=${dirty}"
  "-X github.com/kxn/codex-remote-feishu/internal/buildinfo.FlavorValue=${flavor}"
)

snapshot_dir="$(mktemp -d)"
"${GIT_BIN}" archive --format=tar "${head_commit}" | tar -xf - -C "${snapshot_dir}"
if [[ "${dirty}" == "true" ]]; then
  if ! "${GIT_BIN}" diff --cached --quiet --; then
    "${GIT_BIN}" diff --cached --binary --full-index --no-ext-diff -- | (
      cd "${snapshot_dir}"
      "${GIT_BIN}" apply --binary --whitespace=nowarn -
    )
  fi
  if ! "${GIT_BIN}" diff --quiet --; then
    "${GIT_BIN}" diff --binary --full-index --no-ext-diff -- | (
      cd "${snapshot_dir}"
      "${GIT_BIN}" apply --binary --whitespace=nowarn -
    )
  fi
  while IFS= read -r -d '' untracked_path; do
    mkdir -p "${snapshot_dir}/$(dirname "${untracked_path}")"
    cp -a -- "${untracked_path}" "${snapshot_dir}/${untracked_path}"
  done < <("${GIT_BIN}" ls-files --others --exclude-standard -z)
fi

(
  cd "${snapshot_dir}"
  CLOUDFLARED_EMBED_ALLOW_DOWNLOAD=0 bash scripts/externalaccess/prepare-cloudflared-embed.sh "${target_goos}" "${target_goarch}"
  bash scripts/managedshim/prepare-vscode-shim-embed.sh "${target_goos}" "${target_goarch}"
  bash scripts/upgradeshim/prepare-upgrade-shim-embed.sh "${target_goos}" "${target_goarch}"
  CGO_ENABLED=0 GOOS="${target_goos}" GOARCH="${target_goarch}" \
    "${GO_BIN}" build -trimpath -buildvcs=false -ldflags "${ldflags[*]}" -o "${output}" ./cmd/codex-remote
)

if [[ "${require_clean}" == "1" ]]; then
  final_worktree_status="$("${GIT_BIN}" status --porcelain --untracked-files=normal)" || die "unable to re-inspect source worktree"
  [[ -z "${final_worktree_status}" ]] || die "checkout became dirty during build"
fi

host_goos="$("${GO_BIN}" env GOOS)"
host_goarch="$("${GO_BIN}" env GOARCH)"
if [[ "${target_goos}/${target_goarch}" == "${host_goos}/${host_goarch}" ]]; then
  version_output="$("${output}" --version-detail)"
  expected_output="codex-remote version=${version} commit=${head_commit} built_at=${build_time_utc} dirty=${dirty} branch=${branch} flavor=${flavor}"
  [[ "${version_output}" == "${expected_output}" ]] || die "built binary provenance mismatch: ${version_output}"
fi

artifact_sha256="$(sha256_file "${output}")"
printf 'version=%s\n' "${version}"
printf 'commit=%s\n' "${head_commit}"
printf 'built_at=%s\n' "${build_time_utc}"
printf 'dirty=%s\n' "${dirty}"
printf 'sha256=%s\n' "${artifact_sha256}"
printf 'output=%s\n' "${output}"
