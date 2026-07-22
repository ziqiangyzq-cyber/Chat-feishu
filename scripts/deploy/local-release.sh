#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd -P)"
SCRIPT_PATH="${ROOT_DIR}/scripts/deploy/local-release.sh"
DEFAULT_MANIFEST="${ROOT_DIR}/deploy/local-stacks.tsv"
DEFAULT_RELEASE_ROOT="${HOME}/.local/share/codex-remote-unified"

SYSTEMCTL_BIN="${SYSTEMCTL_BIN:-systemctl}"
SYSTEMD_RUN_BIN="${SYSTEMD_RUN_BIN:-systemd-run}"
CURL_BIN="${CURL_BIN:-curl}"
GIT_BIN="${GIT_BIN:-git}"
GO_BIN="${GO_BIN:-go}"
BUILD_HELPER="${BUILD_HELPER:-${ROOT_DIR}/scripts/build/build-codex-remote.sh}"
TEST_RUNNER="${TEST_RUNNER:-${ROOT_DIR}/scripts/deploy/run-release-preflight.sh}"
PROC_ROOT="${PROC_ROOT:-/proc}"
MV_BIN="${MV_BIN:-mv}"
CP_BIN="${CP_BIN:-cp}"
LN_BIN="${LN_BIN:-ln}"
RM_BIN="${RM_BIN:-rm}"
CMP_BIN="${CMP_BIN:-cmp}"
FLOCK_BIN="${FLOCK_BIN:-flock}"
SLEEP_BIN="${SLEEP_BIN:-sleep}"
SELF_CGROUP_FILE="${SELF_CGROUP_FILE:-/proc/self/cgroup}"

STOP_TIMEOUT_SECONDS="${CODEX_REMOTE_DEPLOY_STOP_TIMEOUT_SECONDS:-20}"
HEALTH_STABILITY_SECONDS="${CODEX_REMOTE_DEPLOY_STABILITY_SECONDS:-5}"

usage() {
  cat <<'EOF'
usage: ./deploy-local-release.sh <command> [options]

commands:
  preflight           validate source, inventory, tests, build, and provenance
  deploy              deploy one immutable artifact to every allowlisted stack
  audit               read-only configured/running artifact and service audit
  rollback            restore the previous state recorded by a deployment
  canonical-checkout  converge a read-only exact-ref deployment checkout

release options:
  --ref <tag|commit>       exact tag or full commit (required for preflight/deploy)
  --version <semver>       required when --ref is a commit; must match a tag ref
  --flavor <value>         shipping (default), alpha, or dev
  --manifest <path>        stack allowlist (default deploy/local-stacks.tsv)
  --release-root <path>    immutable store and transaction journal root
  --transaction <id>       rollback a specific committed transaction

canonical-checkout options are forwarded to scripts/deploy/canonical-checkout.sh.
EOF
}

die() {
  echo "deploy-local-release: $*" >&2
  exit 1
}

trim() {
  local value="$1"
  value="${value#"${value%%[![:space:]]*}"}"
  value="${value%"${value##*[![:space:]]}"}"
  printf '%s' "${value}"
}

sha256_file() {
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum "$1" | awk '{print $1}'
    return
  fi
  shasum -a 256 "$1" | awk '{print $1}'
}

systemctl_user() {
  "${SYSTEMCTL_BIN}" --user "$@"
}

show_property() {
  local unit="$1"
  local property="$2"
  systemctl_user show "${unit}" --property="${property}" --value
}

expand_home_path() {
  local value="$1"
  [[ "${value}" == @HOME@/* ]] || return 1
  value="${HOME}${value#@HOME@}"
  [[ "${value}" == /* && "${value}" != *'/../'* && "${value}" != *'/./'* ]] || return 1
  printf '%s\n' "${value}"
}

extract_exec_path() {
  local raw="$1"
  local value path
  raw="${raw//$'\r'/}"
  [[ -n "${raw}" && "${raw}" != *$'\n'* ]] || return 1
  if [[ "${raw}" == *"path="* ]]; then
    value="${raw#*path=}"
    [[ "${value}" != *"path="* ]] || return 1
    path="${value%%[[:space:];]*}"
  else
    read -r path _ <<<"${raw}"
  fi
  path="$(trim "${path}")"
  [[ "${path}" == /* && "${path}" != *'\\'* ]] || return 1
  printf '%s\n' "${path}"
}

declare -a STACKS=()
declare -a UNITS=()
declare -A STACK_SEEN=()
declare -A UNIT_SEEN=()
declare -A UNIT_STACK=()
declare -A UNIT_ROLE=()
declare -A UNIT_ORDER=()
declare -A UNIT_ALLOWED_PATHS=()
declare -A UNIT_LIFECYCLE=()
declare -A STACK_HEALTH_URL=()
declare -A STACK_XDG_IDENTITY=()
declare -A STACK_DAEMON_COUNT=()
declare -A STACK_SITE_COUNT=()
declare -A UNIT_EXEC_PATH=()
declare -A DISCOVERED_UNITS=()
operator_unit=""

reset_inventory() {
  STACKS=()
  UNITS=()
  STACK_SEEN=()
  UNIT_SEEN=()
  UNIT_STACK=()
  UNIT_ROLE=()
  UNIT_ORDER=()
  UNIT_ALLOWED_PATHS=()
  UNIT_LIFECYCLE=()
  STACK_HEALTH_URL=()
  STACK_XDG_IDENTITY=()
  STACK_DAEMON_COUNT=()
  STACK_SITE_COUNT=()
  UNIT_EXEC_PATH=()
  DISCOVERED_UNITS=()
}

parse_manifest() {
  local path="$1"
  local line_no=0 stack xdg_identity health_url unit role order allowed lifecycle extra
  local existing
  [[ -f "${path}" ]] || die "manifest not found: ${path}"
  reset_inventory
  while IFS='|' read -r stack xdg_identity health_url unit role order allowed lifecycle extra || [[ -n "${stack}${xdg_identity}${health_url}${unit}${role}${order}${allowed}${lifecycle}${extra}" ]]; do
    line_no=$((line_no + 1))
    stack="$(trim "${stack}")"
    [[ -n "${stack}" && "${stack}" != \#* ]] || continue
    xdg_identity="$(trim "${xdg_identity}")"
    health_url="$(trim "${health_url}")"
    unit="$(trim "${unit}")"
    role="$(trim "${role}")"
    order="$(trim "${order}")"
    allowed="$(trim "${allowed}")"
    lifecycle="$(trim "${lifecycle}")"
    [[ -z "${extra:-}" ]] || die "manifest ${path}:${line_no} has too many fields"
    [[ "${stack}" =~ ^[a-z0-9][a-z0-9._-]*$ ]] || die "manifest ${path}:${line_no} has invalid stack id"
    [[ "${xdg_identity}" =~ ^[a-z0-9][a-z0-9._-]*$ ]] || die "manifest ${path}:${line_no} has invalid XDG identity"
    [[ "${health_url}" =~ ^http://(127\.0\.0\.1|localhost):[0-9]+$ ]] || die "manifest ${path}:${line_no} health URL must be an explicit loopback origin"
    [[ "${unit}" =~ ^[a-zA-Z0-9_.@-]+\.service$ ]] || die "manifest ${path}:${line_no} has invalid unit"
    [[ "${role}" == "daemon" || "${role}" == "site" ]] || die "manifest ${path}:${line_no} role must be daemon or site"
    [[ "${order}" =~ ^[0-9]+$ ]] || die "manifest ${path}:${line_no} start order must be numeric"
    [[ -n "${allowed}" ]] || die "manifest ${path}:${line_no} has no allowed ExecStart paths"
    [[ "${lifecycle}" == "active" || "${lifecycle}" == "inactive" ]] || die "manifest ${path}:${line_no} lifecycle must be active or inactive"
    [[ -z "${UNIT_SEEN[${unit}]:-}" ]] || die "unit ${unit} appears more than once in manifest"

    if [[ -z "${STACK_SEEN[${stack}]:-}" ]]; then
      STACK_SEEN["${stack}"]=1
      STACKS+=("${stack}")
      STACK_HEALTH_URL["${stack}"]="${health_url}"
      STACK_XDG_IDENTITY["${stack}"]="${xdg_identity}"
      STACK_DAEMON_COUNT["${stack}"]=0
      STACK_SITE_COUNT["${stack}"]=0
    else
      [[ "${STACK_HEALTH_URL[${stack}]}" == "${health_url}" ]] || die "stack ${stack} has inconsistent health URLs"
      [[ "${STACK_XDG_IDENTITY[${stack}]}" == "${xdg_identity}" ]] || die "stack ${stack} has inconsistent XDG identity"
    fi

    for existing in "${STACKS[@]}"; do
      if [[ "${existing}" != "${stack}" ]]; then
        if [[ "${STACK_XDG_IDENTITY[${existing}]}" == "${xdg_identity}" ]]; then
          die "stacks ${existing} and ${stack} share XDG identity ${xdg_identity}"
        fi
        if [[ "${STACK_HEALTH_URL[${existing}]}" == "${health_url}" ]]; then
          die "stacks ${existing} and ${stack} share health origin ${health_url}"
        fi
      fi
    done

    UNIT_SEEN["${unit}"]=1
    UNITS+=("${unit}")
    UNIT_STACK["${unit}"]="${stack}"
    UNIT_ROLE["${unit}"]="${role}"
    UNIT_ORDER["${unit}"]="${order}"
    UNIT_ALLOWED_PATHS["${unit}"]="${allowed}"
    UNIT_LIFECYCLE["${unit}"]="${lifecycle}"
    if [[ "${role}" == "daemon" ]]; then
      STACK_DAEMON_COUNT["${stack}"]=$((STACK_DAEMON_COUNT["${stack}"] + 1))
    else
      STACK_SITE_COUNT["${stack}"]=$((STACK_SITE_COUNT["${stack}"] + 1))
    fi
  done < "${path}"

  [[ "${#STACKS[@]}" -gt 0 ]] || die "manifest contains no stacks"
  for stack in "${STACKS[@]}"; do
    [[ "${STACK_DAEMON_COUNT[${stack}]}" -gt 0 && "${STACK_SITE_COUNT[${stack}]}" -gt 0 ]] || die "stack ${stack} must declare daemon and site units"
  done
}

collect_discovered_unit_names() {
  local output line first second unit
  output="$(systemctl_user list-unit-files --type=service --no-legend --no-pager)" || die "unable to list systemd user unit files"
  output+=$'\n'
  output+="$(systemctl_user list-units --all --type=service --no-legend --no-pager)" || die "unable to list loaded systemd user services"
  while IFS= read -r line; do
    read -r first second _ <<<"${line}"
    unit="${first}"
    [[ "${unit}" != "●" ]] || unit="${second}"
    [[ "${unit}" == *.service ]] || continue
    DISCOVERED_UNITS["${unit}"]=1
  done <<<"${output}"
}

is_internal_transient_unit() {
  [[ -n "${operator_unit}" && "$1" == "${operator_unit}" ]]
}

is_candidate_unit() {
  local unit="$1"
  local exec_path="$2"
  local base="${exec_path##*/}"
  case "${base}" in
    codex-remote|codex-remote-claude-wecom) return 0 ;;
  esac
  return 1
}

path_is_allowed_for_unit() {
  local unit="$1"
  local actual="$2"
  local choice expanded
  local -a choices=()
  IFS=',' read -r -a choices <<<"${UNIT_ALLOWED_PATHS[${unit}]}"
  for choice in "${choices[@]}"; do
    choice="$(trim "${choice}")"
    expanded="$(expand_home_path "${choice}")" || die "manifest unit ${unit} contains invalid path ${choice}"
    if [[ "${actual}" == "${expanded}" ]]; then
      return 0
    fi
  done
  return 1
}

discover_and_validate_inventory() {
  local unit raw exec_path load_state id
  collect_discovered_unit_names

  for unit in "${!DISCOVERED_UNITS[@]}"; do
    is_internal_transient_unit "${unit}" && continue
    raw="$(show_property "${unit}" ExecStart 2>/dev/null || true)"
    exec_path=""
    if [[ -n "${raw}" ]]; then
      exec_path="$(extract_exec_path "${raw}" || true)"
    fi
    if is_candidate_unit "${unit}" "${exec_path}" && [[ -z "${UNIT_SEEN[${unit}]:-}" ]]; then
      die "unknown candidate service ${unit} (${exec_path:-unresolved}); add it to the manifest or remove it"
    fi
  done

  for unit in "${UNITS[@]}"; do
    [[ -n "${DISCOVERED_UNITS[${unit}]:-}" ]] || die "allowlisted service is missing: ${unit}"
    load_state="$(show_property "${unit}" LoadState)" || die "unable to read LoadState for ${unit}"
    [[ "${load_state}" == "loaded" ]] || die "allowlisted service ${unit} is not loaded (state=${load_state})"
    id="$(show_property "${unit}" Id)" || die "unable to read Id for ${unit}"
    [[ "${id}" == "${unit}" ]] || die "service alias ${unit} resolves to unexpected Id ${id:-<empty>}"
    raw="$(show_property "${unit}" ExecStart)" || die "unable to read ExecStart for ${unit}"
    exec_path="$(extract_exec_path "${raw}")" || die "service ${unit} has ambiguous or unsupported ExecStart"
    path_is_allowed_for_unit "${unit}" "${exec_path}" || die "service ${unit} ExecStart path is not allowlisted: ${exec_path}"
    UNIT_EXEC_PATH["${unit}"]="${exec_path}"
  done
}

ordered_units() {
  local direction="$1"
  local unit role_rank
  for unit in "${UNITS[@]}"; do
    if [[ "${UNIT_ROLE[${unit}]}" == "daemon" ]]; then
      role_rank=0
    else
      role_rank=1
    fi
    printf '%d|%08d|%s\n' "${role_rank}" "${UNIT_ORDER[${unit}]}" "${unit}"
  done | if [[ "${direction}" == "reverse" ]]; then sort -r; else sort; fi | cut -d'|' -f3-
}

declare -a ALIAS_PATHS=()
declare -A ALIAS_STACK=()
declare -A ALIAS_SEEN=()
declare -a BINARY_MUTATION_LOCK_FDS=()
readonly BINARY_MUTATION_LOCK_SUFFIX=".codex-remote-mutation.lock"

collect_alias_paths() {
  local unit path
  ALIAS_PATHS=()
  ALIAS_STACK=()
  ALIAS_SEEN=()
  for unit in "${UNITS[@]}"; do
    path="${UNIT_EXEC_PATH[${unit}]}"
    if [[ -n "${ALIAS_SEEN[${path}]:-}" && "${ALIAS_STACK[${path}]}" != "${UNIT_STACK[${unit}]}" ]]; then
      die "ExecStart path ${path} is shared across isolated stacks"
    fi
    if [[ -z "${ALIAS_SEEN[${path}]:-}" ]]; then
      ALIAS_SEEN["${path}"]=1
      ALIAS_STACK["${path}"]="${UNIT_STACK[${unit}]}"
      ALIAS_PATHS+=("${path}")
    fi
  done
}

acquire_alias_mutation_locks() {
  local path lock_path lock_fd
  local -a sorted_paths=()
  mapfile -t sorted_paths < <(printf '%s\n' "${ALIAS_PATHS[@]}" | sort)
  for path in "${sorted_paths[@]}"; do
    lock_path="${path}${BINARY_MUTATION_LOCK_SUFFIX}"
    exec {lock_fd}>"${lock_path}" || { echo "unable to open binary mutation lock: ${lock_path}" >&2; return 1; }
    if ! "${FLOCK_BIN}" -n "${lock_fd}"; then
      exec {lock_fd}>&-
      echo "another installer or release transaction owns ${lock_path}" >&2
      return 1
    fi
    BINARY_MUTATION_LOCK_FDS+=("${lock_fd}")
  done
}

check_writable_targets() {
  local path parent resolved
  [[ -d "${release_root}" && -w "${release_root}" ]] || die "release root is not writable: ${release_root}"
  for path in "${ALIAS_PATHS[@]}"; do
    [[ ! -d "${path}" ]] || die "ExecStart path is a directory: ${path}"
    [[ -e "${path}" || -L "${path}" ]] || die "ExecStart path is missing: ${path}"
    resolved="$(readlink -f "${path}")" || die "ExecStart path is an unresolved symlink: ${path}"
    [[ -f "${resolved}" && -x "${resolved}" ]] || die "ExecStart path is not an executable regular file: ${path}"
    parent="$(dirname "${path}")"
    [[ -d "${parent}" && -w "${parent}" ]] || die "ExecStart parent is not writable: ${parent}"
  done
}

resolve_go_binary() {
  if [[ "${GO_BIN}" != */* ]]; then
    GO_BIN="$(command -v "${GO_BIN}" || true)"
  fi
  if [[ ! -x "${GO_BIN}" && -x "${HOME}/.local/go/bin/go" ]]; then
    GO_BIN="${HOME}/.local/go/bin/go"
  fi
  [[ -x "${GO_BIN}" ]] || die "Go toolchain not found"
}

source_commit=""
source_version=""

resolve_exact_source() {
  local resolved source_status
  [[ -n "${source_ref}" ]] || die "--ref is required"
  if [[ "${source_ref}" =~ ^[0-9a-fA-F]{40,64}$ ]]; then
    [[ -n "${source_version}" ]] || die "--version is required when --ref is a commit"
  elif "${GIT_BIN}" show-ref --verify --quiet "refs/tags/${source_ref}"; then
    if [[ -n "${source_version}" && "${source_version}" != "${source_ref}" ]]; then
      die "--version ${source_version} does not match tag ${source_ref}"
    fi
    source_version="${source_ref}"
  else
    die "--ref must be a full commit id or exact tag, not a branch: ${source_ref}"
  fi
  [[ "${source_version}" =~ ^v?[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?(\+[0-9A-Za-z.-]+)?$ ]] || die "--version must be a semantic release version"
  resolved="$("${GIT_BIN}" rev-parse --verify "${source_ref}^{commit}")"
  source_commit="${resolved,,}"
  [[ "${source_commit}" =~ ^[0-9a-f]{40,64}$ ]] || die "source ref did not resolve to a full commit"
  head_commit="$("${GIT_BIN}" rev-parse --verify HEAD^{commit})"
  head_commit="${head_commit,,}"
  [[ "${head_commit}" == "${source_commit}" ]] || die "checkout HEAD ${head_commit} does not match source ${source_commit}"
  source_status="$("${GIT_BIN}" status --porcelain --untracked-files=normal)" || die "unable to inspect deployment source checkout"
  [[ -z "${source_status}" ]] || die "deployment source checkout is dirty"
}

detail_version=""
detail_commit=""
detail_built_at=""
detail_dirty=""
detail_branch=""
detail_flavor=""

parse_detailed_version() {
  local output="$1"
  local product token key value
  local -a tokens=()
  declare -A seen=()
  detail_version=""
  detail_commit=""
  detail_built_at=""
  detail_dirty=""
  detail_branch=""
  detail_flavor=""
  [[ -n "${output}" && "${output}" != *$'\n'* ]] || return 1
  read -r product _ <<<"${output}"
  [[ "${product}" == "codex-remote" ]] || return 1
  read -r -a tokens <<<"${output#codex-remote }"
  [[ "${#tokens[@]}" -eq 6 ]] || return 1
  for token in "${tokens[@]}"; do
    [[ "${token}" == *=* ]] || return 1
    key="${token%%=*}"
    value="${token#*=}"
    [[ -n "${value}" && -z "${seen[${key}]:-}" ]] || return 1
    seen["${key}"]=1
    case "${key}" in
      version) detail_version="${value}" ;;
      commit) detail_commit="${value}" ;;
      built_at) detail_built_at="${value}" ;;
      dirty) detail_dirty="${value}" ;;
      branch) detail_branch="${value}" ;;
      flavor) detail_flavor="${value}" ;;
      *) return 1 ;;
    esac
  done
  [[ "${detail_version}" =~ ^[A-Za-z0-9][A-Za-z0-9._+:-]*$ ]] || return 1
  [[ "${detail_commit}" =~ ^[0-9a-f]{40,64}$ ]] || return 1
  [[ "${detail_built_at}" =~ ^[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}Z$ ]] || return 1
  [[ "${detail_dirty}" == "true" || "${detail_dirty}" == "false" ]] || return 1
  [[ "${detail_branch}" =~ ^[A-Za-z0-9][A-Za-z0-9._/+:-]*$ ]] || return 1
  [[ "${detail_flavor}" == "dev" || "${detail_flavor}" == "alpha" || "${detail_flavor}" == "shipping" ]] || return 1
}

validated_release_sha=""
validated_release_commit=""
validated_release_version=""

validate_release_artifact() {
  local path="$1"
  local expected_sha="${2:-}"
  local expected_commit="${3:-}"
  local releases_real path_real binary marker actual_sha version_output
  local line key value extra line_count=0
  declare -A marker_value=()

  validated_release_sha=""
  validated_release_commit=""
  validated_release_version=""
  [[ -d "${path}" && ! -L "${path}" ]] || return 1
  releases_real="$(readlink -f "${release_root}/releases")" || return 1
  path_real="$(readlink -f "${path}")" || return 1
  [[ "$(dirname "${path_real}")" == "${releases_real}" ]] || return 1
  binary="${path}/codex-remote"
  marker="${path}/.codex-remote-unified-release"
  [[ -f "${binary}" && ! -L "${binary}" && -x "${binary}" ]] || return 1
  [[ -f "${marker}" && ! -L "${marker}" ]] || return 1
  actual_sha="$(sha256_file "${binary}")" || return 1
  [[ "${actual_sha}" =~ ^[0-9a-f]{64}$ ]] || return 1
  if [[ -n "${expected_sha}" ]]; then
    [[ "${expected_sha}" =~ ^[0-9a-f]{64}$ && "${actual_sha}" == "${expected_sha}" ]] || return 1
  fi
  version_output="$("${binary}" --version-detail)" || return 1
  parse_detailed_version "${version_output}" || return 1
  [[ "${detail_dirty}" == "false" ]] || return 1
  if [[ -n "${expected_commit}" ]]; then
    [[ "${expected_commit}" =~ ^[0-9a-f]{40,64}$ && "${detail_commit}" == "${expected_commit}" ]] || return 1
  fi
  [[ "$(basename "${path_real}")" == "${detail_commit}-${actual_sha}" ]] || return 1

  while IFS='=' read -r key value extra || [[ -n "${key}${value}${extra}" ]]; do
    line_count=$((line_count + 1))
    [[ -n "${key}" && -n "${value}" && -z "${extra}" && -z "${marker_value[${key}]:-}" ]] || return 1
    case "${key}" in
      version|commit|built_at|dirty|sha256) marker_value["${key}"]="${value}" ;;
      *) return 1 ;;
    esac
  done < "${marker}"
  [[ "${line_count}" -eq 5 ]] || return 1
  [[ "${marker_value[version]:-}" == "${detail_version}" ]] || return 1
  [[ "${marker_value[commit]:-}" == "${detail_commit}" ]] || return 1
  [[ "${marker_value[built_at]:-}" == "${detail_built_at}" ]] || return 1
  [[ "${marker_value[dirty]:-}" == "false" ]] || return 1
  [[ "${marker_value[sha256]:-}" == "${actual_sha}" ]] || return 1
  validated_release_sha="${actual_sha}"
  validated_release_commit="${detail_commit}"
  validated_release_version="${detail_version}"
}

artifact_path=""
artifact_sha=""
artifact_version_output=""

run_preflight_build() {
  local output_path="$1"
  local source_status
  resolve_exact_source
  parse_manifest "${manifest_path}"
  discover_and_validate_inventory
  collect_alias_paths
  check_writable_targets
  validate_transaction_service_state
  resolve_go_binary

  printf 'preflight: tests\n'
  GO_BIN="${GO_BIN}" bash "${TEST_RUNNER}"
  source_status="$("${GIT_BIN}" status --porcelain --untracked-files=normal)" || die "unable to re-inspect deployment source checkout"
  [[ -z "${source_status}" ]] || die "test preflight changed the source checkout"

  printf 'preflight: build once from %s\n' "${source_commit}"
  GO_BIN="${GO_BIN}" GIT_BIN="${GIT_BIN}" bash "${BUILD_HELPER}" \
    --output "${output_path}" \
    --version "${source_version}" \
    --flavor "${build_flavor}" \
    --expected-ref "${source_ref}" \
    --require-clean

  [[ -x "${output_path}" ]] || die "build helper did not produce an executable artifact"
  artifact_version_output="$("${output_path}" --version-detail)"
  parse_detailed_version "${artifact_version_output}" || die "artifact --version-detail output is incomplete: ${artifact_version_output}"
  [[ "${detail_version}" == "${source_version}" ]] || die "artifact version ${detail_version} does not match ${source_version}"
  [[ "${detail_commit}" == "${source_commit}" ]] || die "artifact commit ${detail_commit} does not match ${source_commit}"
  [[ "${detail_dirty}" == "false" ]] || die "dirty artifact cannot be deployed"
  [[ "${detail_flavor}" == "${build_flavor}" ]] || die "artifact flavor ${detail_flavor} does not match ${build_flavor}"
  artifact_sha="$(sha256_file "${output_path}")"
  [[ "${artifact_sha}" =~ ^[0-9a-f]{64}$ ]] || die "unable to calculate artifact SHA-256"
  artifact_path="${output_path}"
  printf 'preflight: artifact sha256=%s version=%s commit=%s\n' "${artifact_sha}" "${detail_version}" "${detail_commit}"
}

stop_all_units() {
  local unit failed=0 state pid deadline
  mapfile -t stop_units < <(ordered_units reverse)
  for unit in "${stop_units[@]}"; do
    if ! systemctl_user stop "${unit}"; then
      echo "failed to stop ${unit}" >&2
      failed=1
    fi
  done
  [[ "${failed}" == "0" ]] || return 1

  deadline=$(( $(date +%s) + STOP_TIMEOUT_SECONDS ))
  for unit in "${stop_units[@]}"; do
    while true; do
      state="$(show_property "${unit}" ActiveState)" || return 1
      pid="$(show_property "${unit}" MainPID)" || return 1
      if [[ ("${state}" == "inactive" || "${state}" == "failed") && ("${pid}" == "0" || -z "${pid}") ]]; then
        break
      fi
      (( $(date +%s) < deadline )) || { echo "unit ${unit} did not stop" >&2; return 1; }
      "${SLEEP_BIN}" 1
    done
  done
}

start_expected_units() {
  local unit
  mapfile -t start_units < <(ordered_units forward)
  for unit in "${start_units[@]}"; do
    [[ "${UNIT_LIFECYCLE[${unit}]}" == "active" ]] || continue
    systemctl_user start "${unit}" || { echo "failed to start ${unit}" >&2; return 1; }
  done
}

unit_is_expected_inactive() {
  local unit="$1"
  local active pid result
  active="$(show_property "${unit}" ActiveState)" || return 1
  pid="$(show_property "${unit}" MainPID)" || return 1
  result="$(show_property "${unit}" Result)" || return 1
  result="${result:-success}"
  [[ "${active}" == "inactive" && ("${pid}" == "0" || -z "${pid}") && "${result}" == "success" ]]
}

unit_runtime_snapshot() {
  local unit="$1"
  local active sub pid restarts result
  active="$(show_property "${unit}" ActiveState)" || return 1
  sub="$(show_property "${unit}" SubState)" || return 1
  pid="$(show_property "${unit}" MainPID)" || return 1
  restarts="$(show_property "${unit}" NRestarts)" || return 1
  result="$(show_property "${unit}" Result)" || return 1
  restarts="${restarts:-0}"
  result="${result:-success}"
  [[ "${active}" == "active" && "${sub}" == "running" && "${pid}" =~ ^[1-9][0-9]*$ && "${restarts}" =~ ^[0-9]+$ && "${result}" == "success" ]] || return 1
  printf '%s|%s\n' "${pid}" "${restarts}"
}

validate_transaction_service_state() {
  local unit
  for unit in "${UNITS[@]}"; do
    if [[ "${UNIT_LIFECYCLE[${unit}]}" == "active" ]]; then
      unit_runtime_snapshot "${unit}" >/dev/null || die "allowlisted active service is not ready for a transaction: ${unit}"
    else
      unit_is_expected_inactive "${unit}" || die "allowlisted inactive service does not match its lifecycle contract: ${unit}"
    fi
  done
}

verify_running_artifact() {
  local unit="$1"
  local expected_real="$2"
  local expected_sha="$3"
  local expected_commit="$4"
  local pid proc_exe running_real running_sha running_version
  pid="$(show_property "${unit}" MainPID)" || return 1
  proc_exe="${PROC_ROOT}/${pid}/exe"
  running_real="$(readlink -f "${proc_exe}")" || return 1
  [[ "${running_real}" == "${expected_real}" ]] || { echo "${unit} runs ${running_real}, expected ${expected_real}" >&2; return 1; }
  running_sha="$(sha256_file "${proc_exe}")" || return 1
  [[ "${running_sha}" == "${expected_sha}" ]] || return 1
  running_version="$("${proc_exe}" --version-detail)" || return 1
  parse_detailed_version "${running_version}" || return 1
  [[ "${detail_commit}" == "${expected_commit}" && "${detail_dirty}" == "false" ]] || return 1
}

probe_stack_http_health() {
  local stack="$1"
  local base="${STACK_HEALTH_URL[${stack}]}"
  local bootstrap
  "${CURL_BIN}" --noproxy '*' --max-time 5 -fsS "${base}/healthz" >/dev/null || return 1
  bootstrap="$("${CURL_BIN}" --noproxy '*' --max-time 5 -fsS "${base}/api/admin/bootstrap-state")" || return 1
  [[ "${bootstrap}" == *'"setupRequired":'* && "${bootstrap}" == *'"phase":'* ]] || return 1
  "${CURL_BIN}" --noproxy '*' --max-time 5 -fsS "${base}/api/admin/runtime-status" >/dev/null || return 1
  "${CURL_BIN}" --noproxy '*' --max-time 5 -fsS "${base}/v1/status" >/dev/null || return 1
}

health_check_all() {
  local expected_release="${1:-}"
  local expected_sha="${2:-}"
  local expected_commit="${3:-}"
  local unit stack snapshot pid restarts after_snapshot after_pid after_restarts expected_real
  declare -A initial_pid=()
  declare -A initial_restarts=()

  for unit in "${UNITS[@]}"; do
    [[ "${UNIT_LIFECYCLE[${unit}]}" == "active" ]] || { unit_is_expected_inactive "${unit}" || return 1; continue; }
    snapshot="$(unit_runtime_snapshot "${unit}")" || { echo "unit ${unit} is not stably running" >&2; return 1; }
    pid="${snapshot%%|*}"
    restarts="${snapshot#*|}"
    initial_pid["${unit}"]="${pid}"
    initial_restarts["${unit}"]="${restarts}"
  done

  if [[ "${HEALTH_STABILITY_SECONDS}" -gt 0 ]]; then
    "${SLEEP_BIN}" "${HEALTH_STABILITY_SECONDS}"
  fi

  for unit in "${UNITS[@]}"; do
    [[ "${UNIT_LIFECYCLE[${unit}]}" == "active" ]] || { unit_is_expected_inactive "${unit}" || return 1; continue; }
    after_snapshot="$(unit_runtime_snapshot "${unit}")" || { echo "unit ${unit} failed during stability window" >&2; return 1; }
    after_pid="${after_snapshot%%|*}"
    after_restarts="${after_snapshot#*|}"
    [[ "${after_pid}" == "${initial_pid[${unit}]}" && "${after_restarts}" == "${initial_restarts[${unit}]}" ]] || {
      echo "unit ${unit} restarted during health stability window" >&2
      return 1
    }
    if [[ -n "${expected_release}" ]]; then
      expected_real="$(readlink -f "${expected_release}/codex-remote")" || return 1
      verify_running_artifact "${unit}" "${expected_real}" "${expected_sha}" "${expected_commit}" || return 1
    fi
  done

  for stack in "${STACKS[@]}"; do
    probe_stack_http_health "${stack}" || { echo "stack ${stack} failed HTTP health checks" >&2; return 1; }
  done
}

verify_configured_release() {
  local expected_release="$1"
  local expected_real unit configured_real
  expected_real="$(readlink -f "${expected_release}/codex-remote")" || return 1
  for unit in "${UNITS[@]}"; do
    configured_real="$(readlink -f "${UNIT_EXEC_PATH[${unit}]}")" || return 1
    [[ "${configured_real}" == "${expected_real}" ]] || return 1
  done
}

declare -a PUBLISHED_CURRENTS=()
declare -a PUBLISHED_ALIASES=()
declare -A STAGED_CURRENT_LINK=()
declare -A BEFORE_CURRENT_TARGET=()
declare -A STAGED_ALIAS_LINK=()
declare -A ALIAS_BACKUP_STATE=()
declare -A ALIAS_BACKUP_PATH=()
deployment_stage_root=""
release_created_by_transaction=0

capture_state() {
  local record_dir="$1"
  local stack current target path index=0 backup_dir backup_path
  mkdir -p "${record_dir}/current" "${record_dir}/aliases" || return 1
  for stack in "${STACKS[@]}"; do
    current="${release_root}/stacks/${stack}/current"
    if [[ -L "${current}" ]]; then
      target="$(readlink "${current}")" || return 1
      printf '%s\n' "${target}" > "${record_dir}/current/${stack}.target" || return 1
    elif [[ -e "${current}" ]]; then
      return 1
    else
      printf '%s\n' "__ABSENT__" > "${record_dir}/current/${stack}.target" || return 1
    fi
  done
  for path in "${ALIAS_PATHS[@]}"; do
    backup_dir="${record_dir}/aliases/${index}"
    backup_path="${backup_dir}/object"
    mkdir -p "${backup_dir}" || return 1
    printf '%s\n' "${path}" > "${backup_dir}/path" || return 1
    if [[ -e "${path}" || -L "${path}" ]]; then
      "${CP_BIN}" -aP -- "${path}" "${backup_path}" || return 1
      printf '%s\n' "present" > "${backup_dir}/state" || return 1
    else
      printf '%s\n' "absent" > "${backup_dir}/state" || return 1
    fi
    index=$((index + 1))
  done
  printf '%s\n' "${index}" > "${record_dir}/alias-count" || return 1
}

prepare_publish_links() {
  local record_dir="$1"
  local release_dir="$2"
  local stack current staged path staged_alias index=0 backup_dir state
  PUBLISHED_CURRENTS=()
  PUBLISHED_ALIASES=()
  STAGED_CURRENT_LINK=()
  BEFORE_CURRENT_TARGET=()
  STAGED_ALIAS_LINK=()
  ALIAS_BACKUP_STATE=()
  ALIAS_BACKUP_PATH=()

  capture_state "${record_dir}" || return 1
  for stack in "${STACKS[@]}"; do
    mkdir -p "${release_root}/stacks/${stack}" || return 1
    current="${release_root}/stacks/${stack}/current"
    BEFORE_CURRENT_TARGET["${stack}"]="$(<"${record_dir}/current/${stack}.target")"
    staged="${current}.stage-${transaction_id}"
    [[ ! -e "${staged}" && ! -L "${staged}" ]] || return 1
    "${LN_BIN}" -s -- "${release_dir}" "${staged}" || return 1
    STAGED_CURRENT_LINK["${stack}"]="${staged}"
  done

  for path in "${ALIAS_PATHS[@]}"; do
    backup_dir="${record_dir}/aliases/${index}"
    state="$(<"${backup_dir}/state")"
    ALIAS_BACKUP_STATE["${path}"]="${state}"
    ALIAS_BACKUP_PATH["${path}"]="${backup_dir}/object"
    staged_alias="${path}.stage-${transaction_id}"
    [[ ! -e "${staged_alias}" && ! -L "${staged_alias}" ]] || return 1
    "${LN_BIN}" -s -- "${release_root}/stacks/${ALIAS_STACK[${path}]}/current/codex-remote" "${staged_alias}" || return 1
    STAGED_ALIAS_LINK["${path}"]="${staged_alias}"
    index=$((index + 1))
  done
}

cleanup_deploy_staging() {
  local failed=0 stack path staged
  for path in "${ALIAS_PATHS[@]}"; do
    staged="${STAGED_ALIAS_LINK[${path}]:-}"
    if [[ -n "${staged}" ]] && ! "${RM_BIN}" -f -- "${staged}"; then
      failed=1
    fi
  done
  for stack in "${STACKS[@]}"; do
    staged="${STAGED_CURRENT_LINK[${stack}]:-}"
    if [[ -n "${staged}" ]] && ! "${RM_BIN}" -f -- "${staged}"; then
      failed=1
    fi
  done
  if [[ -n "${deployment_stage_root}" && -e "${deployment_stage_root}" ]]; then
    chmod -R u+w -- "${deployment_stage_root}" 2>/dev/null || failed=1
    "${RM_BIN}" -rf -- "${deployment_stage_root}" || failed=1
  fi
  if [[ "${transaction_active}" == "0" && "${release_created_by_transaction}" == "1" && -n "${release_dir:-}" && -d "${release_dir}" ]]; then
    chmod -R u+w -- "${release_dir}" 2>/dev/null || failed=1
    "${RM_BIN}" -rf -- "${release_dir}" || failed=1
  fi
  return "${failed}"
}

publish_all_links() {
  local stack path current
  for stack in "${STACKS[@]}"; do
    current="${release_root}/stacks/${stack}/current"
    "${MV_BIN}" -Tf -- "${STAGED_CURRENT_LINK[${stack}]}" "${current}"
    PUBLISHED_CURRENTS+=("${stack}")
  done
  for path in "${ALIAS_PATHS[@]}"; do
    "${MV_BIN}" -Tf -- "${STAGED_ALIAS_LINK[${path}]}" "${path}"
    PUBLISHED_ALIASES+=("${path}")
  done
}

restore_alias_object() {
  local path="$1"
  local state="$2"
  local backup="$3"
  local staged="${path}.restore-${transaction_id}"
  if [[ "${state}" == "present" ]]; then
    [[ ! -e "${staged}" && ! -L "${staged}" ]] || return 1
    if ! "${CP_BIN}" -aP -- "${backup}" "${staged}"; then
      "${RM_BIN}" -f -- "${staged}" 2>/dev/null || true
      return 1
    fi
    if ! "${MV_BIN}" -Tf -- "${staged}" "${path}"; then
      "${RM_BIN}" -f -- "${staged}" 2>/dev/null || true
      return 1
    fi
  else
    "${RM_BIN}" -f -- "${path}" || return 1
  fi
  return 0
}

restore_current_target() {
  local stack="$1"
  local target="$2"
  local current="${release_root}/stacks/${stack}/current"
  local staged="${current}.restore-${transaction_id}"
  if [[ "${target}" == "__ABSENT__" ]]; then
    "${RM_BIN}" -f -- "${current}" || return 1
    return 0
  fi
  [[ ! -e "${staged}" && ! -L "${staged}" ]] || return 1
  "${LN_BIN}" -s -- "${target}" "${staged}" || return 1
  if ! "${MV_BIN}" -Tf -- "${staged}" "${current}"; then
    "${RM_BIN}" -f -- "${staged}" 2>/dev/null || true
    return 1
  fi
  return 0
}

rollback_changed_state() {
  local failed=0 index path stack
  if ! stop_all_units; then
    echo "rollback could not stop every unit; refusing to mutate paths while a mixed stack may be active" >&2
    return 1
  fi
  for ((index=${#ALIAS_PATHS[@]}-1; index>=0; index--)); do
    path="${ALIAS_PATHS[index]}"
    if ! restore_alias_object "${path}" "${ALIAS_BACKUP_STATE[${path}]}" "${ALIAS_BACKUP_PATH[${path}]}"; then
      echo "rollback failed to restore ExecStart path: ${path}" >&2
      failed=1
    fi
    "${RM_BIN}" -f -- "${STAGED_ALIAS_LINK[${path}]:-}" 2>/dev/null || true
  done
  for ((index=${#STACKS[@]}-1; index>=0; index--)); do
    stack="${STACKS[index]}"
    if ! restore_current_target "${stack}" "${BEFORE_CURRENT_TARGET[${stack}]}"; then
      echo "rollback failed to restore current link for stack: ${stack}" >&2
      failed=1
    fi
    "${RM_BIN}" -f -- "${STAGED_CURRENT_LINK[${stack}]:-}" 2>/dev/null || true
  done
  [[ "${failed}" == "0" ]] || return 1
  start_expected_units || return 1
  health_check_all || return 1
}

transaction_active=0
transaction_dir=""

deploy_exit_trap() {
  local status=$?
  local rollback_status=0
  local cleanup_status=0
  trap - EXIT INT TERM
  if [[ "${status}" -ne 0 ]]; then
    set +e
    cleanup_deploy_staging
    cleanup_status=$?
    if [[ "${transaction_active}" == "1" ]]; then
      rollback_changed_state
      rollback_status=$?
    fi
    if [[ -n "${transaction_dir}" ]]; then
      printf '%s\n' "failed" > "${transaction_dir}/status"
      printf '%s\n' "${status}" > "${transaction_dir}/primary-exit-status"
      printf '%s\n' "${rollback_status}" > "${transaction_dir}/rollback-exit-status"
      printf '%s\n' "${cleanup_status}" > "${transaction_dir}/cleanup-exit-status"
    fi
    if [[ "${transaction_active}" == "1" && "${rollback_status}" -ne 0 ]]; then
      echo "automatic rollback was incomplete; all reachable units were left stopped where possible" >&2
    elif [[ "${transaction_active}" == "1" ]]; then
      echo "deployment failed; all changed targets were rolled back" >&2
    else
      echo "deployment failed before service publication; no service paths were changed" >&2
    fi
    if [[ "${cleanup_status}" -ne 0 ]]; then
      echo "deployment staging cleanup was incomplete" >&2
    fi
  fi
  exit "${status}"
}

publish_release_artifact() {
  local staged_dir="$1"
  local release_id
  release_id="${source_commit}-${artifact_sha}"
  release_dir="${release_root}/releases/${release_id}"
  if [[ -e "${release_dir}" ]]; then
    validate_release_artifact "${release_dir}" "${artifact_sha}" "${source_commit}" || die "existing immutable release failed provenance validation: ${release_dir}"
    return
  fi
  printf 'version=%s\ncommit=%s\nbuilt_at=%s\ndirty=false\nsha256=%s\n' \
    "${detail_version}" "${detail_commit}" "${detail_built_at}" "${artifact_sha}" > "${staged_dir}/.codex-remote-unified-release"
  chmod 0555 "${staged_dir}/codex-remote"
  chmod 0444 "${staged_dir}/.codex-remote-unified-release"
  mkdir -p "${release_root}/releases"
  "${MV_BIN}" -- "${staged_dir}" "${release_dir}"
  release_created_by_transaction=1
  chmod 0555 "${release_dir}"
  validate_release_artifact "${release_dir}" "${artifact_sha}" "${source_commit}" || die "published immutable release failed provenance validation: ${release_dir}"
}

run_deploy() {
  local stage_dir build_output
  transaction_id="$(date -u +%Y%m%dT%H%M%SZ)-$$"
  transaction_dir="${release_root}/transactions/${transaction_id}"
  deployment_stage_root="${release_root}/.staging/${transaction_id}"
  stage_dir="${deployment_stage_root}/artifact"
  build_output="${stage_dir}/codex-remote"
  mkdir -p "${transaction_dir}" "${stage_dir}"
  printf '%s\n' "preflighting" > "${transaction_dir}/status"
  trap deploy_exit_trap EXIT
  trap 'exit 130' INT
  trap 'exit 143' TERM

  run_preflight_build "${build_output}"
  acquire_alias_mutation_locks || die "unable to lock every live binary path"
  check_writable_targets
  validate_transaction_service_state
  printf '%s\n' "staged" > "${transaction_dir}/status"
  publish_release_artifact "${stage_dir}"
  printf '%s\n' "${release_dir}" > "${transaction_dir}/release-path"
  printf '%s\n' "${artifact_sha}" > "${transaction_dir}/artifact-sha256"
  printf '%s\n' "${source_commit}" > "${transaction_dir}/commit"
  printf '%s\n' "${source_version}" > "${transaction_dir}/version"
  "${CP_BIN}" -- "${manifest_path}" "${transaction_dir}/manifest.tsv"

  prepare_publish_links "${transaction_dir}/before" "${release_dir}" || die "failed to stage all target links"
  transaction_active=1
  printf '%s\n' "stopping" > "${transaction_dir}/status"
  stop_all_units || die "failed to stop the complete allowlisted service set"
  printf '%s\n' "publishing" > "${transaction_dir}/status"
  publish_all_links
  verify_configured_release "${release_dir}" || die "published ExecStart paths do not converge on one artifact"
  printf '%s\n' "starting" > "${transaction_dir}/status"
  start_expected_units || die "failed to start the expected active service set"
  printf '%s\n' "observing" > "${transaction_dir}/status"
  health_check_all "${release_dir}" "${artifact_sha}" "${source_commit}" || die "one or more stacks failed health validation"
  if ! cleanup_deploy_staging; then
    echo "warning: deployment committed with residual staging paths" >&2
  fi
  printf '%s\n' "committed" > "${transaction_dir}/status"
  transaction_active=0
  trap - EXIT INT TERM
  printf 'deployment committed: transaction=%s release=%s sha256=%s\n' "${transaction_id}" "${release_dir}" "${artifact_sha}"
}

read_record_value() {
  local path="$1"
  [[ -f "${path}" ]] || return 1
  local value
  value="$(<"${path}")"
  [[ -n "${value}" && "${value}" != *$'\n'* ]] || return 1
  printf '%s\n' "${value}"
}

write_record_value() {
  local path="$1"
  local value="$2"
  local temporary="${path}.tmp-${transaction_id:-$$}"
  "${RM_BIN}" -f -- "${temporary}" 2>/dev/null || true
  printf '%s\n' "${value}" > "${temporary}" || { "${RM_BIN}" -f -- "${temporary}" 2>/dev/null || true; return 1; }
  "${MV_BIN}" -f -- "${temporary}" "${path}" || { "${RM_BIN}" -f -- "${temporary}" 2>/dev/null || true; return 1; }
}

latest_committed_transaction() {
  local dir status
  while IFS= read -r dir; do
    status="$(read_record_value "${release_root}/transactions/${dir}/status" 2>/dev/null || true)"
    if [[ "${status}" == "committed" ]]; then
      printf '%s\n' "${dir}"
      return 0
    fi
  done < <(find "${release_root}/transactions" -mindepth 1 -maxdepth 1 -type d -printf '%f\n' 2>/dev/null | sort -r)
  return 1
}

recorded_release=""
recorded_sha=""
recorded_commit=""

validate_recorded_state() {
  local record_dir="$1"
  local count index path state stack target expected_path backup_target first_target=""
  recorded_release=""
  recorded_sha=""
  recorded_commit=""
  count="$(read_record_value "${record_dir}/alias-count")" || return 1
  [[ "${count}" =~ ^[0-9]+$ && "${count}" -eq "${#ALIAS_PATHS[@]}" ]] || return 1
  for ((index=0; index<count; index++)); do
    path="$(read_record_value "${record_dir}/aliases/${index}/path")" || return 1
    state="$(read_record_value "${record_dir}/aliases/${index}/state")" || return 1
    expected_path="${ALIAS_PATHS[index]}"
    [[ "${path}" == "${expected_path}" && "${state}" == "present" ]] || return 1
    [[ -L "${record_dir}/aliases/${index}/object" ]] || return 1
    backup_target="$(readlink "${record_dir}/aliases/${index}/object")" || return 1
    [[ "${backup_target}" == "${release_root}/stacks/${ALIAS_STACK[${path}]}/current/codex-remote" ]] || return 1
  done
  for stack in "${STACKS[@]}"; do
    target="$(read_record_value "${record_dir}/current/${stack}.target")" || return 1
    [[ "${target}" != "__ABSENT__" ]] || return 1
    if [[ -z "${first_target}" ]]; then
      first_target="${target}"
    else
      [[ "${target}" == "${first_target}" ]] || return 1
    fi
  done
  validate_release_artifact "${first_target}" || return 1
  recorded_release="${first_target}"
  recorded_sha="${validated_release_sha}"
  recorded_commit="${validated_release_commit}"
}

restore_recorded_state() {
  local record_dir="$1"
  local count index path state stack target
  validate_recorded_state "${record_dir}" || return 1
  count="$(read_record_value "${record_dir}/alias-count")" || return 1
  for ((index=0; index<count; index++)); do
    path="${ALIAS_PATHS[index]}"
    state="$(read_record_value "${record_dir}/aliases/${index}/state")" || return 1
    restore_alias_object "${path}" "${state}" "${record_dir}/aliases/${index}/object" || return 1
  done
  for stack in "${STACKS[@]}"; do
    target="$(read_record_value "${record_dir}/current/${stack}.target")" || return 1
    restore_current_target "${stack}" "${target}" || return 1
  done
}

manual_rollback_active=0
manual_rollback_paths_changed=0
manual_safety_dir=""
manual_current_release=""
manual_current_sha=""
manual_current_commit=""
manual_target_dir=""

manual_rollback_exit_trap() {
  local status=$?
  local recovery_status=0
  trap - EXIT INT TERM
  if [[ "${status}" -ne 0 && "${manual_rollback_active}" == "1" ]]; then
    set +e
    if [[ "${manual_rollback_paths_changed}" == "1" ]]; then
      if stop_all_units; then
        restore_recorded_state "${manual_safety_dir}/before" &&
          verify_configured_release "${manual_current_release}" &&
          start_expected_units &&
          health_check_all "${manual_current_release}" "${manual_current_sha}" "${manual_current_commit}"
        recovery_status=$?
      else
        recovery_status=1
      fi
    else
      start_expected_units && health_check_all "${manual_current_release}" "${manual_current_sha}" "${manual_current_commit}"
      recovery_status=$?
    fi
    write_record_value "${manual_safety_dir}/status" "failed" || true
    if [[ "${recovery_status}" == "0" && -n "${manual_target_dir}" ]]; then
      write_record_value "${manual_target_dir}/status" "committed" || recovery_status=1
    fi
    printf '%s\n' "${status}" > "${manual_safety_dir}/primary-exit-status" 2>/dev/null || true
    printf '%s\n' "${recovery_status}" > "${manual_safety_dir}/recovery-exit-status" 2>/dev/null || true
    if [[ "${recovery_status}" == "0" ]]; then
      echo "rollback failed; the pre-rollback unified release was restored" >&2
    else
      echo "rollback failed and safety restoration was incomplete; reachable units were left stopped where possible" >&2
    fi
  fi
  exit "${status}"
}

run_rollback() {
  local target_id target_dir target_release target_sha target_commit target_version safety_id safety_dir
  local prior_release prior_sha prior_commit
  parse_manifest "${manifest_path}"
  discover_and_validate_inventory
  collect_alias_paths
  check_writable_targets
  acquire_alias_mutation_locks || die "unable to lock every live binary path"
  check_writable_targets
  validate_transaction_service_state
  if [[ -z "${requested_transaction}" ]]; then
    requested_transaction="$(latest_committed_transaction)" || die "no committed deployment transaction is available"
  fi
  [[ "${requested_transaction}" =~ ^[A-Za-z0-9._-]+$ ]] || die "invalid transaction id"
  target_id="${requested_transaction}"
  target_dir="${release_root}/transactions/${target_id}"
  [[ "$(read_record_value "${target_dir}/status" 2>/dev/null || true)" == "committed" ]] || die "transaction is not committed: ${target_id}"
  [[ -f "${target_dir}/manifest.tsv" ]] || die "transaction manifest is missing"
  "${CMP_BIN}" -s -- "${manifest_path}" "${target_dir}/manifest.tsv" || die "transaction manifest differs from the active allowlist"
  target_release="$(read_record_value "${target_dir}/release-path")" || die "transaction release path is missing"
  target_sha="$(read_record_value "${target_dir}/artifact-sha256")" || die "transaction artifact hash is missing"
  target_commit="$(read_record_value "${target_dir}/commit")" || die "transaction commit is missing"
  target_version="$(read_record_value "${target_dir}/version")" || die "transaction version is missing"
  [[ "${target_sha}" =~ ^[0-9a-f]{64}$ && "${target_commit}" =~ ^[0-9a-f]{40,64}$ ]] || die "transaction provenance is malformed"
  validate_release_artifact "${target_release}" "${target_sha}" "${target_commit}" || die "transaction release failed immutable provenance validation"
  [[ "${validated_release_version}" == "${target_version}" ]] || die "transaction version does not match its release artifact"
  verify_configured_release "${target_release}" || die "current configured paths have drifted since transaction ${target_id}; refusing ambiguous rollback"
  validate_recorded_state "${target_dir}/before" || die "transaction has no validated prior unified release; first-migration rollback requires the unified deploy path"
  prior_release="${recorded_release}"
  prior_sha="${recorded_sha}"
  prior_commit="${recorded_commit}"

  transaction_id="rollback-$(date -u +%Y%m%dT%H%M%SZ)-$$"
  safety_id="${transaction_id}"
  safety_dir="${release_root}/transactions/${safety_id}"
  mkdir -p "${safety_dir}" || die "unable to create rollback safety journal"
  capture_state "${safety_dir}/before" || die "unable to capture rollback safety state"
  validate_recorded_state "${safety_dir}/before" || die "captured rollback safety state is invalid"
  write_record_value "${safety_dir}/status" "rollback-safety" || die "unable to initialize rollback safety journal"
  "${CP_BIN}" -- "${manifest_path}" "${safety_dir}/manifest.tsv" || die "unable to capture rollback manifest"

  manual_safety_dir="${safety_dir}"
  manual_current_release="${target_release}"
  manual_current_sha="${target_sha}"
  manual_current_commit="${target_commit}"
  manual_target_dir="${target_dir}"
  trap manual_rollback_exit_trap EXIT
  trap 'exit 130' INT
  trap 'exit 143' TERM
  manual_rollback_active=1
  stop_all_units || die "failed to stop all units for rollback"
  manual_rollback_paths_changed=1
  restore_recorded_state "${target_dir}/before" || die "failed to restore the recorded prior release"
  verify_configured_release "${prior_release}" || die "restored paths do not converge on the prior release"
  start_expected_units || die "prior release could not restart"
  health_check_all "${prior_release}" "${prior_sha}" "${prior_commit}" || die "prior release failed health validation"
  write_record_value "${target_dir}/status" "rolled_back" || die "unable to commit rollback journal state"
  write_record_value "${safety_dir}/status" "rollback-complete" || die "unable to commit rollback safety journal"
  manual_rollback_active=0
  trap - EXIT INT TERM
  printf 'rollback complete: transaction=%s\n' "${target_id}"
}

audit_status=0
audit_first_sha=""
audit_first_commit=""
audit_first_real=""
audit_first_inode=""

audit_binary() {
  local label="$1"
  local path="$2"
  local real inode sha version commit="unknown" valid=1
  AUDIT_LAST_REAL=""
  AUDIT_LAST_INODE=""
  AUDIT_LAST_SHA=""
  AUDIT_LAST_COMMIT="unknown"
  if [[ ! -e "${path}" && ! -L "${path}" ]]; then
    printf '%s_path=%s %s_state=missing\n' "${label}" "${path}" "${label}"
    audit_status=1
    return 1
  fi
  real="$(readlink -f "${path}" 2>/dev/null || true)"
  inode="$(stat -Lc '%d:%i' "${path}" 2>/dev/null || true)"
  sha="$(sha256_file "${path}" 2>/dev/null || true)"
  version="$("${path}" --version-detail 2>/dev/null || true)"
  if parse_detailed_version "${version}" && [[ "${detail_dirty}" == "false" ]]; then
    commit="${detail_commit}"
  else
    valid=0
  fi
  [[ "${real}" == /* ]] || valid=0
  [[ "${inode}" =~ ^[0-9]+:[0-9]+$ ]] || valid=0
  [[ "${sha}" =~ ^[0-9a-f]{64}$ ]] || valid=0
  [[ "${valid}" == "1" ]] || audit_status=1
  printf '%s_path=%s %s_resolved=%s %s_inode=%s %s_sha256=%s %s_version="%s"\n' \
    "${label}" "${path}" "${label}" "${real:-unresolved}" "${label}" "${inode:-unknown}" "${label}" "${sha:-unknown}" "${label}" "${version:-unknown}"
  AUDIT_LAST_REAL="${real}"
  AUDIT_LAST_INODE="${inode}"
  AUDIT_LAST_SHA="${sha}"
  AUDIT_LAST_COMMIT="${commit}"
  [[ "${valid}" == "1" ]]
}

run_audit() {
  local stack unit active sub pid restarts result proc_path
  local stack_sha="" stack_commit="" stack_inode="" stack_real="" configured_ok running_ok configured_seen
  audit_status=0
  audit_first_sha=""
  audit_first_commit=""
  audit_first_real=""
  audit_first_inode=""
  parse_manifest "${manifest_path}"
  discover_and_validate_inventory
  printf 'manifest=%s stacks=%d units=%d\n' "${manifest_path}" "${#STACKS[@]}" "${#UNITS[@]}"
  for stack in "${STACKS[@]}"; do
    stack_sha=""
    stack_commit=""
    stack_inode=""
    stack_real=""
    configured_ok=1
    running_ok=1
    configured_seen=0
    printf 'stack=%s xdg_identity=%s health=%s\n' "${stack}" "${STACK_XDG_IDENTITY[${stack}]}" "${STACK_HEALTH_URL[${stack}]}"
    for unit in "${UNITS[@]}"; do
      [[ "${UNIT_STACK[${unit}]}" == "${stack}" ]] || continue
      active="$(show_property "${unit}" ActiveState 2>/dev/null || printf unknown)"
      sub="$(show_property "${unit}" SubState 2>/dev/null || printf unknown)"
      pid="$(show_property "${unit}" MainPID 2>/dev/null || printf 0)"
      restarts="$(show_property "${unit}" NRestarts 2>/dev/null || printf unknown)"
      result="$(show_property "${unit}" Result 2>/dev/null || printf unknown)"
      printf 'unit=%s role=%s lifecycle=%s active=%s sub=%s pid=%s restarts=%s result=%s\n' "${unit}" "${UNIT_ROLE[${unit}]}" "${UNIT_LIFECYCLE[${unit}]}" "${active}" "${sub}" "${pid}" "${restarts}" "${result}"
      if [[ "${UNIT_LIFECYCLE[${unit}]}" == "active" ]]; then
        if [[ "${active}" != "active" || "${sub}" != "running" || "${result}" != "success" || ! "${restarts}" =~ ^[0-9]+$ ]]; then
          running_ok=0
        fi
      elif [[ "${active}" != "inactive" || ("${pid}" != "0" && -n "${pid}") || "${result}" != "success" ]]; then
        running_ok=0
      fi
      if ! audit_binary configured "${UNIT_EXEC_PATH[${unit}]}"; then
        configured_ok=0
      fi
      if [[ "${configured_seen}" == "0" ]]; then
        stack_sha="${AUDIT_LAST_SHA}"
        stack_commit="${AUDIT_LAST_COMMIT}"
        stack_inode="${AUDIT_LAST_INODE}"
        stack_real="${AUDIT_LAST_REAL}"
        configured_seen=1
      elif [[ "${AUDIT_LAST_SHA}" != "${stack_sha}" || "${AUDIT_LAST_COMMIT}" != "${stack_commit}" || "${AUDIT_LAST_INODE}" != "${stack_inode}" || "${AUDIT_LAST_REAL}" != "${stack_real}" ]]; then
        configured_ok=0
      fi
      if [[ "${UNIT_LIFECYCLE[${unit}]}" == "inactive" ]]; then
        printf 'running_state=expected-inactive\n'
      elif [[ "${pid}" =~ ^[1-9][0-9]*$ ]]; then
        proc_path="${PROC_ROOT}/${pid}/exe"
        if ! audit_binary running "${proc_path}"; then
          running_ok=0
        fi
        if [[ "${AUDIT_LAST_SHA}" != "${stack_sha}" || "${AUDIT_LAST_COMMIT}" != "${stack_commit}" || "${AUDIT_LAST_INODE}" != "${stack_inode}" || "${AUDIT_LAST_REAL}" != "${stack_real}" ]]; then
          running_ok=0
        fi
      else
        printf 'running_state=missing-main-pid\n'
        running_ok=0
      fi
    done
    [[ "${configured_ok}" == "1" && "${running_ok}" == "1" ]] || audit_status=1
    printf 'stack_consensus=%s configured_same=%s running_same=%s resolved=%s inode=%s sha256=%s commit=%s\n' "${stack}" "${configured_ok}" "${running_ok}" "${stack_real:-unknown}" "${stack_inode:-unknown}" "${stack_sha:-unknown}" "${stack_commit:-unknown}"
    if [[ -z "${audit_first_sha}" ]]; then
      audit_first_sha="${stack_sha}"
      audit_first_commit="${stack_commit}"
      audit_first_real="${stack_real}"
      audit_first_inode="${stack_inode}"
    elif [[ "${stack_sha}" != "${audit_first_sha}" || "${stack_commit}" != "${audit_first_commit}" || "${stack_real}" != "${audit_first_real}" || "${stack_inode}" != "${audit_first_inode}" ]]; then
      audit_status=1
    fi
  done
  if [[ "${audit_status}" == "0" ]]; then
    printf 'global_consensus=true resolved=%s inode=%s sha256=%s commit=%s\n' "${audit_first_real}" "${audit_first_inode}" "${audit_first_sha}" "${audit_first_commit}"
  else
    printf 'global_consensus=false\n'
  fi
  return "${audit_status}"
}

standalone_preflight_dir=""

standalone_preflight_exit_trap() {
  local status=$?
  local cleanup_status=0
  trap - EXIT INT TERM
  if [[ -n "${standalone_preflight_dir}" && -e "${standalone_preflight_dir}" ]]; then
    "${RM_BIN}" -rf -- "${standalone_preflight_dir}" || cleanup_status=$?
  fi
  if [[ "${status}" -ne 0 ]]; then
    exit "${status}"
  fi
  exit "${cleanup_status}"
}

validate_release_root_argument() {
  [[ "${release_root}" == /* ]] || die "--release-root must be absolute"
  [[ "${release_root}" != "/" && "${release_root}" != "${HOME}" ]] || die "--release-root is too broad"
  [[ "${release_root}" != *$'\n'* && "${release_root}" != *'/../'* && "${release_root}" != */.. && "${release_root}" != *'/./'* && "${release_root}" != */. ]] || {
    die "--release-root must not contain relative path components"
  }
  [[ ! -L "${release_root}" ]] || die "--release-root must not be a symlink"
}

durable_reexec() {
  local unit_name log_path
  mkdir -p "${release_root}/transactions"
  log_path="${release_root}/operator.log"
  unit_name="codex-remote-unified-release-$(date -u +%Y%m%dT%H%M%SZ)-$$.service"
  printf 're-executing as durable systemd user service %s\n' "${unit_name}"
  "${SYSTEMD_RUN_BIN}" \
    --user \
    --wait \
    --collect \
    --quiet \
    --service-type=exec \
    --unit="${unit_name}" \
    --description="codex-remote unified local release" \
    --property="WorkingDirectory=${ROOT_DIR}" \
    --setenv=CODEX_REMOTE_UNIFIED_RELEASE_GUARD=1 \
    --property="StandardOutput=append:${log_path}" \
    --property="StandardError=append:${log_path}" \
    bash "${SCRIPT_PATH}" "${original_args[@]}"
}

detect_durable_context() {
  local line cgroup_path candidate="" id transient
  operator_unit=""
  [[ "${CODEX_REMOTE_UNIFIED_RELEASE_GUARD:-}" == "1" && -r "${SELF_CGROUP_FILE}" ]] || return 1
  while IFS= read -r line; do
    cgroup_path="${line##*:}"
    case "${cgroup_path}" in
      */codex-remote-unified-release-*.service)
        [[ -z "${candidate}" ]] || return 1
        candidate="${cgroup_path##*/}"
        ;;
    esac
  done < "${SELF_CGROUP_FILE}"
  [[ -n "${candidate}" ]] || return 1
  id="$(show_property "${candidate}" Id 2>/dev/null)" || return 1
  transient="$(show_property "${candidate}" Transient 2>/dev/null)" || return 1
  [[ "${id}" == "${candidate}" && "${transient}" == "yes" ]] || return 1
  operator_unit="${candidate}"
}

original_args=("$@")
[[ "$#" -gt 0 ]] || { usage >&2; exit 2; }
command_name="$1"
shift

if [[ "${command_name}" == "canonical-checkout" ]]; then
  exec bash "${ROOT_DIR}/scripts/deploy/canonical-checkout.sh" "$@"
fi

manifest_path="${DEFAULT_MANIFEST}"
release_root="${DEFAULT_RELEASE_ROOT}"
source_ref=""
source_version=""
build_flavor="shipping"
requested_transaction=""

while [[ $# -gt 0 ]]; do
  case "$1" in
    --manifest)
      [[ $# -ge 2 ]] || die "missing value for --manifest"
      manifest_path="$2"
      shift 2
      ;;
    --release-root)
      [[ $# -ge 2 ]] || die "missing value for --release-root"
      release_root="$2"
      shift 2
      ;;
    --ref)
      [[ $# -ge 2 ]] || die "missing value for --ref"
      source_ref="$2"
      shift 2
      ;;
    --version)
      [[ $# -ge 2 ]] || die "missing value for --version"
      source_version="$2"
      shift 2
      ;;
    --flavor)
      [[ $# -ge 2 ]] || die "missing value for --flavor"
      build_flavor="$2"
      shift 2
      ;;
    --transaction)
      [[ $# -ge 2 ]] || die "missing value for --transaction"
      requested_transaction="$2"
      shift 2
      ;;
    -h|--help)
      usage
      exit 0
      ;;
    *) die "unknown argument: $1" ;;
  esac
done

case "${build_flavor}" in
  shipping|alpha|dev) ;;
  *) die "unsupported flavor: ${build_flavor}" ;;
esac
while [[ "${release_root}" != "/" && "${release_root}" == */ ]]; do
  release_root="${release_root%/}"
done
validate_release_root_argument

case "${command_name}" in
  deploy|rollback)
    if ! detect_durable_context; then
      durable_reexec
      exit $?
    fi
    ;;
esac

case "${command_name}" in
  preflight)
    mkdir -p "${release_root}/.staging"
    transaction_id="preflight-$(date -u +%Y%m%dT%H%M%SZ)-$$"
    preflight_dir="${release_root}/.staging/${transaction_id}"
    mkdir -p "${preflight_dir}"
    standalone_preflight_dir="${preflight_dir}"
    trap standalone_preflight_exit_trap EXIT
    trap 'exit 130' INT
    trap 'exit 143' TERM
    run_preflight_build "${preflight_dir}/codex-remote"
    printf 'preflight complete: sha256=%s\n' "${artifact_sha}"
    "${RM_BIN}" -rf -- "${preflight_dir}"
    standalone_preflight_dir=""
    trap - EXIT INT TERM
    ;;
  deploy)
    mkdir -p "${release_root}/transactions" "${release_root}/.staging"
    exec {lock_fd}>"${release_root}/deploy.lock"
    flock -n "${lock_fd}" || die "another unified release transaction is active"
    run_deploy
    ;;
  audit)
    run_audit
    ;;
  rollback)
    [[ -d "${release_root}/transactions" ]] || die "transaction directory is missing"
    exec {lock_fd}>"${release_root}/deploy.lock"
    flock -n "${lock_fd}" || die "another unified release transaction is active"
    run_rollback
    ;;
  *)
    usage >&2
    exit 2
    ;;
esac
