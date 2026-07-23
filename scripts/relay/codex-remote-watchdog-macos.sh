#!/usr/bin/env bash
set -uo pipefail

# launchd runs this periodically. Two consecutive status timeouts trigger a
# diagnostic capture and a forced service restart.

base_dir="${CODEX_REMOTE_BASE_DIR:-${HOME:?HOME is required}}"
admin_port="${CODEX_REMOTE_ADMIN_PORT:-9501}"
pprof_port="${CODEX_REMOTE_PPROF_PORT:-17501}"
service_label="${CODEX_REMOTE_SERVICE_LABEL:-com.codex-remote.service}"
failure_threshold="${CODEX_REMOTE_WATCHDOG_FAILURE_THRESHOLD:-2}"
request_timeout="${CODEX_REMOTE_WATCHDOG_TIMEOUT_SECONDS:-5}"

state_dir="${base_dir}/.local/state/codex-remote/watchdog"
data_dir="${base_dir}/.local/share/codex-remote"
diagnostics_dir="${data_dir}/diagnostics"
relay_log="${data_dir}/logs/codex-remote-relayd.log"
watchdog_log="${data_dir}/logs/codex-remote-watchdog.log"
counter_file="${state_dir}/consecutive-failures"
lock_dir="${state_dir}/run.lock"

mkdir -p "${state_dir}" "${diagnostics_dir}" "$(dirname "${watchdog_log}")"
if ! mkdir "${lock_dir}" 2>/dev/null; then
  exit 0
fi
cleanup() {
  rmdir "${lock_dir}" 2>/dev/null || true
}
trap cleanup EXIT

log_message() {
  printf '%s %s\n' "$(date '+%Y-%m-%d %H:%M:%S %Z')" "$*" >> "${watchdog_log}"
}

status_file="$(mktemp "${state_dir}/status.XXXXXX")"
trap 'rm -f "${status_file}"; cleanup' EXIT
if env -u http_proxy -u https_proxy -u HTTP_PROXY -u HTTPS_PROXY -u ALL_PROXY -u all_proxy \
  curl --noproxy '*' --fail --silent --show-error \
  --connect-timeout "${request_timeout}" --max-time "${request_timeout}" \
  "http://127.0.0.1:${admin_port}/v1/status" > "${status_file}" 2>/dev/null; then
  printf '0\n' > "${counter_file}"
  exit 0
fi

failures=0
if [[ -f "${counter_file}" ]]; then
  read -r failures < "${counter_file}" || failures=0
fi
if [[ ! "${failures}" =~ ^[0-9]+$ ]]; then
  failures=0
fi
failures=$((failures + 1))
printf '%d\n' "${failures}" > "${counter_file}"
log_message "status check failed (${failures}/${failure_threshold}) on admin port ${admin_port}"

if (( failures < failure_threshold )); then
  exit 0
fi

timestamp="$(date '+%Y%m%d-%H%M%S')"
incident_dir="${diagnostics_dir}/watchdog-${timestamp}"
mkdir -p "${incident_dir}"

env -u http_proxy -u https_proxy -u HTTP_PROXY -u HTTPS_PROXY -u ALL_PROXY -u all_proxy \
  curl --noproxy '*' --fail --silent --show-error \
  --connect-timeout "${request_timeout}" --max-time "${request_timeout}" \
  "http://127.0.0.1:${pprof_port}/debug/pprof/goroutine?debug=2" \
  > "${incident_dir}/goroutines.txt" 2>&1 || true

uid="$(id -u)"
launchctl print "gui/${uid}/${service_label}" > "${incident_dir}/launchctl.txt" 2>&1 || true
ps -axo pid,ppid,state,lstart,etime,command > "${incident_dir}/processes.txt" 2>&1 || true
if command -v lsof >/dev/null 2>&1; then
  lsof -nP -iTCP -sTCP:LISTEN > "${incident_dir}/listening-ports.txt" 2>&1 || true
fi
if [[ -f "${relay_log}" ]]; then
  tail -n 500 "${relay_log}" > "${incident_dir}/relay-log-tail.txt" 2>&1 || true
fi

log_message "failure threshold reached; diagnostics=${incident_dir}; restarting ${service_label}"
if launchctl kickstart -k "gui/${uid}/${service_label}" >> "${watchdog_log}" 2>&1; then
  printf '0\n' > "${counter_file}"
  log_message "restart requested successfully"
else
  log_message "restart request failed"
fi
