#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd -P)"
OPERATOR="${ROOT_DIR}/deploy-local-release.sh"
TEST_COMMIT="0123456789abcdef0123456789abcdef01234567"
TEST_VERSION="v1.2.3"
work_dir="$(mktemp -d)"

cleanup() {
  local status=$?
  if [[ "${CODEX_REMOTE_SELFTEST_KEEP:-}" == "1" ]]; then
    printf 'unified local release selftest retained: %s\n' "${work_dir}" >&2
  else
    chmod -R u+w "${work_dir}" 2>/dev/null || true
    rm -rf "${work_dir}"
  fi
  return "${status}"
}
trap cleanup EXIT

write_legacy_binary() {
  local path="$1"
  local version="$2"
  local commit="$3"
  mkdir -p "$(dirname "${path}")"
  cat > "${path}" <<EOF
#!/usr/bin/env bash
set -euo pipefail
if [[ "\${1:-}" == "--version-detail" ]]; then
  printf '%s\n' 'codex-remote version=${version} commit=${commit} built_at=2026-07-01T00:00:00Z dirty=false branch=legacy flavor=shipping'
elif [[ "\${1:-}" == "version" ]]; then
  printf '%s\n' '${version}'
else
  exit 0
fi
EOF
  chmod +x "${path}"
}

make_fake_tools() {
  cat > "${fake_dir}/git" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
case "${1:-}" in
  rev-parse)
    printf '%s\n' "${FAKE_GIT_COMMIT}"
    ;;
  status)
    ;;
  show-ref)
    exit 1
    ;;
  *)
    echo "unexpected fake git call: $*" >&2
    exit 1
    ;;
esac
EOF

  cat > "${fake_dir}/build-helper" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
output=""
version=""
flavor=""
while [[ $# -gt 0 ]]; do
  case "$1" in
    --output) output="$2"; shift 2 ;;
    --version) version="$2"; shift 2 ;;
    --flavor) flavor="$2"; shift 2 ;;
    --branch|--expected-ref|--build-time|--goos|--goarch) shift 2 ;;
    --require-clean) shift ;;
    *) echo "unexpected fake build option: $1" >&2; exit 1 ;;
  esac
done
[[ -n "${output}" && -n "${version}" && -n "${flavor}" ]]
printf 'build\n' >> "${FAKE_STATE_DIR}/build.log"
mkdir -p "$(dirname "${output}")"
cat > "${output}" <<SCRIPT
#!/usr/bin/env bash
set -euo pipefail
if [[ "\${1:-}" == "--version-detail" ]]; then
  printf '%s\n' 'codex-remote version=${version} commit=${FAKE_GIT_COMMIT} built_at=2026-07-22T03:04:05Z dirty=false branch=final/unified-release flavor=${flavor}'
elif [[ "\${1:-}" == "version" ]]; then
  printf '%s\n' '${version}'
else
  exit 0
fi
SCRIPT
chmod +x "${output}"
EOF

  cat > "${fake_dir}/test-runner" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
printf 'tests\n' >> "${FAKE_STATE_DIR}/test.log"
EOF

  cat > "${fake_dir}/curl" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
url="${*: -1}"
if [[ -f "${FAKE_STATE_DIR}/fail-health" && ! -f "${FAKE_STATE_DIR}/fail-health-used" && "${url}" == */healthz ]]; then
  : > "${FAKE_STATE_DIR}/fail-health-used"
  exit 22
fi
case "${url}" in
  */api/admin/bootstrap-state) printf '%s\n' '{"phase":"uninitialized","setupRequired":true,"gateways":[{"state":"connected"}]}' ;;
  *) printf '%s\n' '{}' ;;
esac
EOF

  cat > "${fake_dir}/systemctl" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
[[ "${1:-}" == "--user" ]] || { echo "fake systemctl requires --user" >&2; exit 1; }
shift
command_name="${1:-}"
shift || true
printf '%s %s\n' "${command_name}" "$*" >> "${FAKE_STATE_DIR}/systemctl.log"
case "${command_name}" in
  list-unit-files|list-units)
    while IFS= read -r unit; do
      [[ -n "${unit}" ]] && printf '%s enabled\n' "${unit}"
    done < "${FAKE_STATE_DIR}/unit-list"
    ;;
  show)
    unit="${1:-}"
    property=""
    for arg in "$@"; do
      case "${arg}" in --property=*) property="${arg#--property=}" ;; esac
    done
    [[ -n "${unit}" && -n "${property}" ]]
    active="inactive"
    if [[ -f "${FAKE_STATE_DIR}/${unit}.active" ]]; then
      active="$(<"${FAKE_STATE_DIR}/${unit}.active")"
    fi
    case "${property}" in
      ExecStart)
        path="$(<"${FAKE_STATE_DIR}/${unit}.exec")"
        printf '{ path=%s ; argv[]=%s daemon ; ignore_errors=no ; }\n' "${path}" "${path}"
        ;;
      LoadState) printf 'loaded\n' ;;
      Id)
        if [[ -f "${FAKE_STATE_DIR}/${unit}.empty-id" ]]; then
          printf '\n'
        else
          printf '%s\n' "${unit}"
        fi
        ;;
      ActiveState) printf '%s\n' "${active}" ;;
      SubState) [[ "${active}" == "active" ]] && printf 'running\n' || printf 'dead\n' ;;
      MainPID) [[ "${active}" == "active" ]] && cat "${FAKE_STATE_DIR}/${unit}.pid" || printf '0\n' ;;
      NRestarts) cat "${FAKE_STATE_DIR}/${unit}.restarts" ;;
      Result) printf 'success\n' ;;
      Transient) [[ "${unit}" == codex-remote-unified-release-*.service ]] && printf 'yes\n' || printf 'no\n' ;;
      *) echo "unexpected property: ${property}" >&2; exit 1 ;;
    esac
    ;;
  stop)
    unit="${1:-}"
    if [[ -f "${FAKE_STATE_DIR}/fail-stop-unit" && "$(<"${FAKE_STATE_DIR}/fail-stop-unit")" == "${unit}" && ! -f "${FAKE_STATE_DIR}/fail-stop-used" ]]; then
      : > "${FAKE_STATE_DIR}/fail-stop-used"
      exit 1
    fi
    printf 'inactive\n' > "${FAKE_STATE_DIR}/${unit}.active"
    pid="$(<"${FAKE_STATE_DIR}/${unit}.pid")"
    rm -f "${PROC_ROOT}/${pid}/exe"
    ;;
  start)
    unit="${1:-}"
    if [[ -f "${FAKE_STATE_DIR}/fail-start-unit" && "$(<"${FAKE_STATE_DIR}/fail-start-unit")" == "${unit}" && ! -f "${FAKE_STATE_DIR}/fail-start-used" ]]; then
      : > "${FAKE_STATE_DIR}/fail-start-used"
      exit 1
    fi
    printf 'active\n' > "${FAKE_STATE_DIR}/${unit}.active"
    pid="$(<"${FAKE_STATE_DIR}/${unit}.pid")"
    path="$(<"${FAKE_STATE_DIR}/${unit}.exec")"
    mkdir -p "${PROC_ROOT}/${pid}"
    rm -f "${PROC_ROOT}/${pid}/exe"
    ln -s "$(readlink -f "${path}")" "${PROC_ROOT}/${pid}/exe"
    ;;
  *)
    echo "unexpected fake systemctl call: ${command_name} $*" >&2
    exit 1
    ;;
esac
EOF

cat > "${fake_dir}/systemd-run" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
printf '%s\n' "$*" >> "${FAKE_STATE_DIR}/systemd-run.log"
args=("$@")
command_index=-1
unit_name=""
saw_user=0
saw_wait=0
saw_collect=0
saw_exec_service=0
for index in "${!args[@]}"; do
  case "${args[index]}" in
    --user) saw_user=1 ;;
    --wait) saw_wait=1 ;;
    --collect) saw_collect=1 ;;
    --service-type=exec) saw_exec_service=1 ;;
    --unit=*) unit_name="${args[index]#--unit=}" ;;
  esac
  if [[ "${args[index]}" == "bash" ]]; then
    command_index="${index}"
    break
  fi
done
[[ "${saw_user}${saw_wait}${saw_collect}${saw_exec_service}" == "1111" ]] || {
  echo "fake systemd-run requires a durable user service transaction" >&2
  exit 1
}
[[ "${command_index}" -ge 0 ]] || { echo "fake systemd-run did not find command" >&2; exit 1; }
[[ -n "${unit_name}" ]] || { echo "fake systemd-run did not find unit" >&2; exit 1; }
printf '0::/user.slice/app.slice/%s\n' "${unit_name}" > "${SELF_CGROUP_FILE}"
command=("${args[@]:command_index}")
CODEX_REMOTE_UNIFIED_RELEASE_GUARD=1 "${command[@]}"
EOF

  cat > "${fake_dir}/mv" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
destination="${*: -1}"
if [[ -f "${FAKE_STATE_DIR}/fail-mv-destination" && "${destination}" == *"$(<"${FAKE_STATE_DIR}/fail-mv-destination")"* && ! -f "${FAKE_STATE_DIR}/fail-mv-used" ]]; then
  : > "${FAKE_STATE_DIR}/fail-mv-used"
  exit 1
fi
exec /bin/mv "$@"
EOF

  cat > "${fake_dir}/ln" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
destination="${*: -1}"
if [[ -f "${FAKE_STATE_DIR}/fail-ln-destination" && "${destination}" == *"$(<"${FAKE_STATE_DIR}/fail-ln-destination")"* && ! -f "${FAKE_STATE_DIR}/fail-ln-used" ]]; then
  : > "${FAKE_STATE_DIR}/fail-ln-used"
  exit 1
fi
exec /bin/ln "$@"
EOF

  cat > "${fake_dir}/cp" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
destination="${*: -1}"
if [[ -f "${FAKE_STATE_DIR}/fail-cp-destination" && "${destination}" == *"$(<"${FAKE_STATE_DIR}/fail-cp-destination")"* && ! -f "${FAKE_STATE_DIR}/fail-cp-used" ]]; then
  : > "${FAKE_STATE_DIR}/fail-cp-used"
  exit 1
fi
exec /bin/cp "$@"
EOF

  chmod +x "${fake_dir}"/*
}

setup_fixture() {
  local name="$1"
  scenario_dir="${work_dir}/${name}"
  fake_home="${scenario_dir}/home"
  fake_state="${scenario_dir}/state"
  fake_dir="${scenario_dir}/fake-bin"
  proc_root="${scenario_dir}/proc"
  self_cgroup="${fake_state}/self.cgroup"
  release_root="${scenario_dir}/releases"
  manifest="${scenario_dir}/manifest.tsv"
  mkdir -p "${fake_home}" "${fake_state}" "${fake_dir}" "${proc_root}" "${release_root}"

  default_binary="${fake_home}/.local/share/codex-remote/bin/codex-remote"
  second_binary="${fake_home}/.local/share/codex-remote-2/bin/codex-remote"
  claude_daemon_binary="${fake_home}/.local/bin/codex-remote-claude-wecom"
  claude_site_binary="${fake_home}/.local/share/claude-remote/bin/codex-remote"
  write_legacy_binary "${default_binary}" "v0.9.0" "1111111111111111111111111111111111111111"
  write_legacy_binary "${second_binary}" "v0.8.0" "2222222222222222222222222222222222222222"
  write_legacy_binary "${claude_daemon_binary}" "v0.7.0" "3333333333333333333333333333333333333333"
  write_legacy_binary "${claude_site_binary}" "v0.6.0" "4444444444444444444444444444444444444444"

  cat > "${manifest}" <<'EOF'
# stack|xdg_identity|health_base_url|unit|role|start_order|allowed_exec_paths|lifecycle
codex-remote|codex-remote|http://127.0.0.1:19501|codex-remote.service|daemon|10|@HOME@/.local/share/codex-remote/bin/codex-remote|active
codex-remote|codex-remote|http://127.0.0.1:19501|codex-remote-site.service|site|20|@HOME@/.local/share/codex-remote/bin/codex-remote|active
codex-remote-2|codex-remote-2|http://127.0.0.1:19521|codex-remote-2.service|daemon|10|@HOME@/.local/share/codex-remote-2/bin/codex-remote|active
codex-remote-2|codex-remote-2|http://127.0.0.1:19521|codex-remote-2-site.service|site|20|@HOME@/.local/share/codex-remote-2/bin/codex-remote|active
claude-remote|claude-remote|http://127.0.0.1:19541|claude-remote.service|daemon|10|@HOME@/.local/bin/codex-remote-claude-wecom|active
claude-remote|claude-remote|http://127.0.0.1:19541|claude-remote-site.service|site|20|@HOME@/.local/share/claude-remote/bin/codex-remote|active
EOF

  units=(
    codex-remote.service
    codex-remote-site.service
    codex-remote-2.service
    codex-remote-2-site.service
    claude-remote.service
    claude-remote-site.service
  )
  printf '%s\n' "${units[@]}" > "${fake_state}/unit-list"
  printf '%s\n' "${default_binary}" > "${fake_state}/codex-remote.service.exec"
  printf '%s\n' "${default_binary}" > "${fake_state}/codex-remote-site.service.exec"
  printf '%s\n' "${second_binary}" > "${fake_state}/codex-remote-2.service.exec"
  printf '%s\n' "${second_binary}" > "${fake_state}/codex-remote-2-site.service.exec"
  printf '%s\n' "${claude_daemon_binary}" > "${fake_state}/claude-remote.service.exec"
  printf '%s\n' "${claude_site_binary}" > "${fake_state}/claude-remote-site.service.exec"

  local pid=4100 unit
  for unit in "${units[@]}"; do
    printf 'inactive\n' > "${fake_state}/${unit}.active"
    printf '%s\n' "${pid}" > "${fake_state}/${unit}.pid"
    printf '0\n' > "${fake_state}/${unit}.restarts"
    pid=$((pid + 1))
  done
  : > "${fake_state}/systemctl.log"
  : > "${fake_state}/build.log"
  : > "${fake_state}/test.log"
  : > "${fake_state}/systemd-run.log"
  printf '0::/user.slice/app.slice/codex-remote.service\n' > "${self_cgroup}"
  make_fake_tools
  active_ln_bin=/bin/ln
  active_cp_bin=/bin/cp
  active_commit="${TEST_COMMIT}"
  active_version="${TEST_VERSION}"

  for unit in "${units[@]}"; do
    FAKE_STATE_DIR="${fake_state}" PROC_ROOT="${proc_root}" "${fake_dir}/systemctl" --user start "${unit}"
  done
  : > "${fake_state}/systemctl.log"
}

run_guarded_operator() {
  printf '0::/user.slice/app.slice/codex-remote-unified-release-test.service\n' > "${self_cgroup}"
  FAKE_STATE_DIR="${fake_state}" \
  FAKE_GIT_COMMIT="${active_commit}" \
  HOME="${fake_home}" \
  SYSTEMCTL_BIN="${fake_dir}/systemctl" \
  SYSTEMD_RUN_BIN="${fake_dir}/systemd-run" \
  CURL_BIN="${fake_dir}/curl" \
  GIT_BIN="${fake_dir}/git" \
  GO_BIN="/bin/true" \
  BUILD_HELPER="${fake_dir}/build-helper" \
  TEST_RUNNER="${fake_dir}/test-runner" \
  PROC_ROOT="${proc_root}" \
  SELF_CGROUP_FILE="${self_cgroup}" \
  MV_BIN="${active_mv_bin:-/bin/mv}" \
  LN_BIN="${active_ln_bin:-/bin/ln}" \
  CP_BIN="${active_cp_bin:-/bin/cp}" \
  CODEX_REMOTE_DEPLOY_STABILITY_SECONDS=0 \
  CODEX_REMOTE_DEPLOY_STOP_TIMEOUT_SECONDS=2 \
  CODEX_REMOTE_UNIFIED_RELEASE_GUARD=1 \
    bash "${OPERATOR}" "$@"
}

run_outer_operator() {
  printf '0::/user.slice/app.slice/codex-remote.service\n' > "${self_cgroup}"
  FAKE_STATE_DIR="${fake_state}" \
  FAKE_GIT_COMMIT="${active_commit}" \
  HOME="${fake_home}" \
  SYSTEMCTL_BIN="${fake_dir}/systemctl" \
  SYSTEMD_RUN_BIN="${fake_dir}/systemd-run" \
  CURL_BIN="${fake_dir}/curl" \
  GIT_BIN="${fake_dir}/git" \
  GO_BIN="/bin/true" \
  BUILD_HELPER="${fake_dir}/build-helper" \
  TEST_RUNNER="${fake_dir}/test-runner" \
  PROC_ROOT="${proc_root}" \
  SELF_CGROUP_FILE="${self_cgroup}" \
  MV_BIN="${active_mv_bin:-/bin/mv}" \
  LN_BIN="${active_ln_bin:-/bin/ln}" \
  CP_BIN="${active_cp_bin:-/bin/cp}" \
  CODEX_REMOTE_DEPLOY_STABILITY_SECONDS=0 \
  CODEX_REMOTE_DEPLOY_STOP_TIMEOUT_SECONDS=2 \
  env -u CODEX_REMOTE_UNIFIED_RELEASE_GUARD bash "${OPERATOR}" "$@"
}

run_forged_guard_operator() {
  printf '0::/user.slice/app.slice/codex-remote.service\n' > "${self_cgroup}"
  FAKE_STATE_DIR="${fake_state}" \
  FAKE_GIT_COMMIT="${active_commit}" \
  HOME="${fake_home}" \
  SYSTEMCTL_BIN="${fake_dir}/systemctl" \
  SYSTEMD_RUN_BIN="${fake_dir}/systemd-run" \
  CURL_BIN="${fake_dir}/curl" \
  GIT_BIN="${fake_dir}/git" \
  GO_BIN="/bin/true" \
  BUILD_HELPER="${fake_dir}/build-helper" \
  TEST_RUNNER="${fake_dir}/test-runner" \
  PROC_ROOT="${proc_root}" \
  SELF_CGROUP_FILE="${self_cgroup}" \
  MV_BIN="${active_mv_bin:-/bin/mv}" \
  LN_BIN="${active_ln_bin:-/bin/ln}" \
  CP_BIN="${active_cp_bin:-/bin/cp}" \
  CODEX_REMOTE_DEPLOY_STABILITY_SECONDS=0 \
  CODEX_REMOTE_DEPLOY_STOP_TIMEOUT_SECONDS=2 \
  CODEX_REMOTE_UNIFIED_RELEASE_GUARD=1 \
    bash "${OPERATOR}" "$@"
}

deploy_args() {
  printf '%s\n' deploy --ref "${active_commit}" --version "${active_version}" --manifest "${manifest}" --release-root "${release_root}"
}

find_transaction_for_commit() {
  local wanted_commit="$1"
  local path
  while IFS= read -r path; do
    if [[ "$(<"${path}")" == "${wanted_commit}" ]]; then
      dirname "${path}"
      return 0
    fi
  done < <(find "${release_root}/transactions" -name commit -type f)
  return 1
}

assert_legacy_restored() {
  [[ -f "${default_binary}" && ! -L "${default_binary}" ]]
  [[ -f "${second_binary}" && ! -L "${second_binary}" ]]
  [[ -f "${claude_daemon_binary}" && ! -L "${claude_daemon_binary}" ]]
  [[ -f "${claude_site_binary}" && ! -L "${claude_site_binary}" ]]
  [[ "$("${claude_daemon_binary}" version)" == "v0.7.0" ]]
  [[ "$("${claude_site_binary}" version)" == "v0.6.0" ]]
  local stack unit
  for stack in codex-remote codex-remote-2 claude-remote; do
    [[ ! -e "${release_root}/stacks/${stack}/current" && ! -L "${release_root}/stacks/${stack}/current" ]]
  done
  for unit in "${units[@]}"; do
    [[ "$(<"${fake_state}/${unit}.active")" == "active" ]]
  done
}

test_success_and_audit() {
  setup_fixture success
  active_mv_bin=/bin/mv
  local index
  local -a stopped_units=()
  local -a started_units=()
  mapfile -t args < <(deploy_args)
  run_guarded_operator "${args[@]}" > "${scenario_dir}/deploy.log" 2>&1
  [[ "$(wc -l < "${fake_state}/build.log")" -eq 1 ]]
  [[ "$(wc -l < "${fake_state}/test.log")" -eq 1 ]]
  [[ -L "${default_binary}" && -L "${second_binary}" && -L "${claude_daemon_binary}" && -L "${claude_site_binary}" ]]
  daemon_target="$(readlink -f "${claude_daemon_binary}")"
  site_target="$(readlink -f "${claude_site_binary}")"
  [[ "${daemon_target}" == "${site_target}" ]]
  [[ "$(stat -Lc '%d:%i' "${claude_daemon_binary}")" == "$(stat -Lc '%d:%i' "${claude_site_binary}")" ]]
  [[ "$(stat -Lc '%d:%i' "${default_binary}")" == "$(stat -Lc '%d:%i' "${second_binary}")" ]]
  [[ "$(stat -Lc '%d:%i' "${default_binary}")" == "$(stat -Lc '%d:%i' "${claude_daemon_binary}")" ]]
  [[ "$(find "${release_root}/releases" -type f -name codex-remote | wc -l)" -eq 1 ]]
  mapfile -t stopped_units < <(sed -n 's/^stop //p' "${fake_state}/systemctl.log")
  mapfile -t started_units < <(sed -n 's/^start //p' "${fake_state}/systemctl.log")
  [[ "${#stopped_units[@]}" -eq 6 && "${#started_units[@]}" -eq 6 ]]
  for index in 0 1 2; do
    [[ "${stopped_units[index]}" == *-site.service ]]
    [[ "${started_units[index]}" != *-site.service ]]
  done
  for index in 3 4 5; do
    [[ "${stopped_units[index]}" != *-site.service ]]
    [[ "${started_units[index]}" == *-site.service ]]
  done

  : > "${fake_state}/systemctl.log"
  filesystem_before="$(find "${fake_home}" "${release_root}" -printf '%p|%y|%m|%s|%T@|%l\n' | sort)"
  run_guarded_operator audit --manifest "${manifest}" --release-root "${release_root}" > "${scenario_dir}/audit.log"
  filesystem_after="$(find "${fake_home}" "${release_root}" -printf '%p|%y|%m|%s|%T@|%l\n' | sort)"
  [[ "${filesystem_before}" == "${filesystem_after}" ]]
  grep -F 'global_consensus=true' "${scenario_dir}/audit.log" >/dev/null
  if grep -E '^(stop|start) ' "${fake_state}/systemctl.log" >/dev/null; then
    echo "audit mutated service state" >&2
    exit 1
  fi
}

test_inventory_fail_closed() {
  setup_fixture unknown-unit
  extra_binary="${fake_home}/.local/share/codex-remote-extra/bin/codex-remote"
  write_legacy_binary "${extra_binary}" "v0.1.0" "5555555555555555555555555555555555555555"
  printf '%s\n' 'codex-remote-extra.service' >> "${fake_state}/unit-list"
  printf '%s\n' "${extra_binary}" > "${fake_state}/codex-remote-extra.service.exec"
  printf 'active\n' > "${fake_state}/codex-remote-extra.service.active"
  printf '4200\n' > "${fake_state}/codex-remote-extra.service.pid"
  printf '0\n' > "${fake_state}/codex-remote-extra.service.restarts"
  if run_guarded_operator audit --manifest "${manifest}" --release-root "${release_root}" > "${scenario_dir}/unknown.log" 2>&1; then
    echo "unknown unit should fail closed" >&2
    exit 1
  fi
  grep -F 'unknown candidate service codex-remote-extra.service' "${scenario_dir}/unknown.log" >/dev/null

  setup_fixture missing-unit
  sed -i '/codex-remote-2-site.service/d' "${fake_state}/unit-list"
  if run_guarded_operator audit --manifest "${manifest}" --release-root "${release_root}" > "${scenario_dir}/missing.log" 2>&1; then
    echo "missing unit should fail closed" >&2
    exit 1
  fi
  grep -F 'allowlisted service is missing: codex-remote-2-site.service' "${scenario_dir}/missing.log" >/dev/null

  setup_fixture empty-unit-id
  : > "${fake_state}/codex-remote.service.empty-id"
  if run_guarded_operator audit --manifest "${manifest}" --release-root "${release_root}" > "${scenario_dir}/empty-id.log" 2>&1; then
    echo "empty unit Id should fail closed" >&2
    exit 1
  fi
  grep -F 'service alias codex-remote.service resolves to unexpected Id <empty>' "${scenario_dir}/empty-id.log" >/dev/null

  setup_fixture duplicate-health-origin
  sed -i 's/19521/19501/g' "${manifest}"
  if run_guarded_operator audit --manifest "${manifest}" --release-root "${release_root}" > "${scenario_dir}/duplicate-health.log" 2>&1; then
    echo "duplicate stack health origins should fail closed" >&2
    exit 1
  fi
  grep -F 'stacks codex-remote and codex-remote-2 share health origin' "${scenario_dir}/duplicate-health.log" >/dev/null
}

test_preflight_cleans_staging() {
  setup_fixture preflight-cleanup
  run_guarded_operator preflight \
    --ref "${active_commit}" \
    --version "${active_version}" \
    --manifest "${manifest}" \
    --release-root "${release_root}" > "${scenario_dir}/preflight.log" 2>&1
  [[ "$(wc -l < "${fake_state}/build.log")" -eq 1 ]]
  [[ "$(wc -l < "${fake_state}/test.log")" -eq 1 ]]
  if find "${release_root}/.staging" -mindepth 1 -print -quit | grep -q .; then
    echo "standalone preflight left an artifact in staging" >&2
    exit 1
  fi
  if grep -E '^(stop|start) ' "${fake_state}/systemctl.log" >/dev/null; then
    echo "standalone preflight changed service state" >&2
    exit 1
  fi
}

test_failure_rolls_back() {
  local failure="$1"
  setup_fixture "rollback-${failure}"
  active_mv_bin=/bin/mv
  case "${failure}" in
    publish)
      printf '%s\n' 'codex-remote-claude-wecom' > "${fake_state}/fail-mv-destination"
      active_mv_bin="${fake_dir}/mv"
      ;;
    restart)
      printf '%s\n' 'codex-remote.service' > "${fake_state}/fail-start-unit"
      ;;
    health)
      : > "${fake_state}/fail-health"
      ;;
    stop)
      printf '%s\n' 'claude-remote-site.service' > "${fake_state}/fail-stop-unit"
      ;;
    *) exit 1 ;;
  esac
  mapfile -t args < <(deploy_args)
  if run_guarded_operator "${args[@]}" > "${scenario_dir}/failure.log" 2>&1; then
    echo "${failure} failure scenario unexpectedly succeeded" >&2
    exit 1
  fi
  assert_legacy_restored
  grep -F 'all changed targets were rolled back' "${scenario_dir}/failure.log" >/dev/null
}

test_capture_failure_cleans_without_stopping() {
  setup_fixture capture
  active_mv_bin=/bin/mv
  active_cp_bin="${fake_dir}/cp"
  printf '%s\n' 'aliases/2/object' > "${fake_state}/fail-cp-destination"
  mapfile -t args < <(deploy_args)
  if run_guarded_operator "${args[@]}" > "${scenario_dir}/failure.log" 2>&1; then
    echo "capture failure scenario unexpectedly succeeded" >&2
    exit 1
  fi
  assert_legacy_restored
  if grep -E '^(stop|start) ' "${fake_state}/systemctl.log" >/dev/null; then
    echo "capture failure changed service state" >&2
    exit 1
  fi
  grep -F 'failed before service publication' "${scenario_dir}/failure.log" >/dev/null
}

test_staging_failure_cleans_without_stopping() {
  setup_fixture staging
  active_mv_bin=/bin/mv
  active_ln_bin="${fake_dir}/ln"
  printf '%s\n' 'codex-remote-claude-wecom.stage-' > "${fake_state}/fail-ln-destination"
  mapfile -t args < <(deploy_args)
  if run_guarded_operator "${args[@]}" > "${scenario_dir}/failure.log" 2>&1; then
    echo "staging failure scenario unexpectedly succeeded" >&2
    exit 1
  fi
  assert_legacy_restored
  if find "${fake_home}" "${release_root}" -name '*.stage-*' -print -quit | grep -q .; then
    echo "staging failure left temporary links behind" >&2
    exit 1
  fi
  if grep -E '^(stop|start) ' "${fake_state}/systemctl.log" >/dev/null; then
    echo "staging failure changed service state" >&2
    exit 1
  fi
  transaction_dir="$(find "${release_root}/transactions" -mindepth 1 -maxdepth 1 -type d -print -quit)"
  [[ "$(<"${transaction_dir}/status")" == "failed" ]]
  [[ "$(<"${transaction_dir}/primary-exit-status")" -ne 0 ]]
  grep -F 'failed before service publication' "${scenario_dir}/failure.log" >/dev/null
}

test_systemd_run_guard() {
  setup_fixture systemd-run
  active_mv_bin=/bin/mv
  mapfile -t args < <(deploy_args)
  run_outer_operator "${args[@]}" > "${scenario_dir}/outer.log" 2>&1
  [[ "$(wc -l < "${fake_state}/systemd-run.log")" -eq 1 ]]
  grep -F -- '--user --wait --collect' "${fake_state}/systemd-run.log" >/dev/null
  grep -F -- '--service-type=exec' "${fake_state}/systemd-run.log" >/dev/null
  [[ "$(wc -l < "${fake_state}/build.log")" -eq 1 ]]
}

test_forged_guard_reexecs() {
  setup_fixture forged-guard
  active_mv_bin=/bin/mv
  mapfile -t args < <(deploy_args)
  run_forged_guard_operator "${args[@]}" > "${scenario_dir}/forged.log" 2>&1
  [[ "$(wc -l < "${fake_state}/systemd-run.log")" -eq 1 ]]
  [[ "$(wc -l < "${fake_state}/build.log")" -eq 1 ]]
}

test_manual_rollback_keeps_unified_layout() {
  setup_fixture manual-rollback
  active_mv_bin=/bin/mv
  mapfile -t args < <(deploy_args)
  run_guarded_operator "${args[@]}" > "${scenario_dir}/deploy-a.log" 2>&1
  first_transaction="$(find_transaction_for_commit "${TEST_COMMIT}")"
  [[ -n "${first_transaction}" ]]

  active_commit="abcdef0123456789abcdef0123456789abcdef01"
  active_version="v1.2.4"
  mapfile -t args < <(deploy_args)
  run_guarded_operator "${args[@]}" > "${scenario_dir}/deploy-b.log" 2>&1
  second_transaction="$(find_transaction_for_commit "${active_commit}")"
  [[ -n "${second_transaction}" ]]

  run_guarded_operator rollback \
    --transaction "$(basename "${second_transaction}")" \
    --manifest "${manifest}" \
    --release-root "${release_root}" > "${scenario_dir}/rollback.log" 2>&1
  "${default_binary}" --version-detail | grep -F "commit=${TEST_COMMIT}" >/dev/null
  [[ "$(stat -Lc '%d:%i' "${default_binary}")" == "$(stat -Lc '%d:%i' "${second_binary}")" ]]
  [[ "$(stat -Lc '%d:%i' "${claude_daemon_binary}")" == "$(stat -Lc '%d:%i' "${claude_site_binary}")" ]]
  [[ "$(<"${second_transaction}/status")" == "rolled_back" ]]

  : > "${fake_state}/systemctl.log"
  if run_guarded_operator rollback \
    --transaction "$(basename "${first_transaction}")" \
    --manifest "${manifest}" \
    --release-root "${release_root}" > "${scenario_dir}/first-migration.log" 2>&1; then
    echo "first migration rollback should fail closed" >&2
    exit 1
  fi
  grep -F 'first-migration rollback requires the unified deploy path' "${scenario_dir}/first-migration.log" >/dev/null
  if grep -E '^(stop|start) ' "${fake_state}/systemctl.log" >/dev/null; then
    echo "rejected first-migration rollback changed service state" >&2
    exit 1
  fi
}

test_inactive_lifecycle_preserved() {
  setup_fixture inactive-lifecycle
  active_mv_bin=/bin/mv
  sed -i 's#codex-remote-site.service|site|20|@HOME@/.local/share/codex-remote/bin/codex-remote|active#codex-remote-site.service|site|20|@HOME@/.local/share/codex-remote/bin/codex-remote|inactive#' "${manifest}"
  FAKE_STATE_DIR="${fake_state}" PROC_ROOT="${proc_root}" "${fake_dir}/systemctl" --user stop codex-remote-site.service
  : > "${fake_state}/systemctl.log"
  mapfile -t args < <(deploy_args)
  run_guarded_operator "${args[@]}" > "${scenario_dir}/deploy.log" 2>&1
  [[ "$(<"${fake_state}/codex-remote-site.service.active")" == "inactive" ]]
  if grep -Fx 'start codex-remote-site.service' "${fake_state}/systemctl.log" >/dev/null; then
    echo "inactive lifecycle unit was started" >&2
    exit 1
  fi
  run_guarded_operator audit --manifest "${manifest}" --release-root "${release_root}" > "${scenario_dir}/audit.log"
  grep -F 'unit=codex-remote-site.service role=site lifecycle=inactive active=inactive' "${scenario_dir}/audit.log" >/dev/null
  grep -F 'global_consensus=true' "${scenario_dir}/audit.log" >/dev/null
}

test_mutation_lock_contention_fails_closed() {
  setup_fixture mutation-lock
  active_mv_bin=/bin/mv
  exec {held_lock_fd}>"${default_binary}.codex-remote-mutation.lock"
  flock -n "${held_lock_fd}"
  mapfile -t args < <(deploy_args)
  if run_guarded_operator "${args[@]}" > "${scenario_dir}/lock.log" 2>&1; then
    echo "mutation lock contention unexpectedly succeeded" >&2
    exit 1
  fi
  assert_legacy_restored
  if grep -E '^(stop|start) ' "${fake_state}/systemctl.log" >/dev/null; then
    echo "mutation lock contention changed service state" >&2
    exit 1
  fi
  grep -F 'unable to lock every live binary path' "${scenario_dir}/lock.log" >/dev/null
  exec {held_lock_fd}>&-
}

test_success_and_audit
test_inactive_lifecycle_preserved
test_mutation_lock_contention_fails_closed
test_inventory_fail_closed
test_preflight_cleans_staging
test_failure_rolls_back publish
test_failure_rolls_back restart
test_failure_rolls_back health
test_failure_rolls_back stop
test_capture_failure_cleans_without_stopping
test_staging_failure_cleans_without_stopping
test_systemd_run_guard
test_forged_guard_reexecs
test_manual_rollback_keeps_unified_layout

echo "unified local release selftest: ok"
