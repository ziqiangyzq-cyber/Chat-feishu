#!/usr/bin/env bash
set -euo pipefail

base_dir="${HOME}"
wait_timeout=600
command="${1:-apply}"
[[ $# -eq 0 ]] || shift

while (($# > 0)); do
  case "$1" in
    --base-dir) base_dir="${2:?missing --base-dir value}"; shift 2 ;;
    --wait-timeout) wait_timeout="${2:?missing --wait-timeout value}"; shift 2 ;;
    *) echo "unknown option: $1" >&2; exit 2 ;;
  esac
done
[[ "${base_dir}" == /* && "${base_dir}" != / ]] || { echo "unsafe base dir" >&2; exit 2; }
[[ "${wait_timeout}" =~ ^[0-9]+$ ]] || { echo "invalid wait timeout" >&2; exit 2; }

config_main="${base_dir}/.config/codex-remote/config.json"
config_second="${base_dir}/.config/codex-remote-2/codex-remote/config.json"
config_claude="${base_dir}/.config/claude-remote/codex-remote/config.json"
state_main="${base_dir}/.local/state/codex-remote/surface-resume-state.json"
state_second="${base_dir}/.local/state/codex-remote-2/codex-remote/surface-resume-state.json"
state_claude="${base_dir}/.local/state/claude-remote/codex-remote/surface-resume-state.json"
backup_root="${base_dir}/.local/state/codex-remote-consolidation"
latest_link="${backup_root}/latest"
legacy_units=(codex-remote-2-site.service codex-remote-2.service claude-remote-site.service claude-remote.service)
active_units=(codex-remote.service codex-remote-site.service)

require_file() { [[ -f "$1" ]] || { echo "required file missing: $1" >&2; exit 1; }; }
systemctl_user() { systemctl --user "$@"; }
active_turns() {
  local port total=0 value
  for port in 9501 9601 9701; do
    value="$(curl --noproxy '*' --connect-timeout 1 --max-time 2 -fsS "http://127.0.0.1:${port}/v1/status" 2>/dev/null | jq -r '(.activeRemoteTurns // []) | length' 2>/dev/null || printf 0)"
    total=$((total + value))
  done
  printf '%s' "${total}"
}
wait_for_idle() {
  local deadline=$((SECONDS + wait_timeout))
  while ((SECONDS < deadline)); do
    [[ "$(active_turns)" == 0 ]] && return 0
    sleep 2
  done
  echo "active turns did not drain within ${wait_timeout}s" >&2
  return 1
}
restore_backup() {
  local dir="$1" unit
  cp -a "${dir}/config-main.json" "${config_main}"
  cp -a "${dir}/state-main.json" "${state_main}"
  systemctl_user daemon-reload
  for unit in "${active_units[@]}" "${legacy_units[@]}"; do systemctl_user enable "$unit" >/dev/null 2>&1 || true; done
  for unit in "${active_units[@]}" "${legacy_units[@]}"; do systemctl_user restart "$unit" >/dev/null 2>&1 || true; done
}
health_gate() {
  local deadline=$((SECONDS + 60)) payload
  while ((SECONDS < deadline)); do
    payload="$(curl --noproxy '*' --connect-timeout 1 --max-time 3 -fsS http://127.0.0.1:9501/v1/status 2>/dev/null || true)"
    if jq -e '
      ([.gateways[]? | select(.disabled == false and .state == "connected")] | length) == 3
      and any(.wecomBots[]?; .enabled == true and .connected == true)
      and any(.instances[]?; .Backend == "codex" and .Online == true)
    ' <<<"${payload}" >/dev/null 2>&1; then return 0; fi
    sleep 2
  done
  return 1
}

case "${command}" in
  status)
    jq '{gateways:[.gateways[]?|{gatewayId,state}],wecomBots:[.wecomBots[]?|{gatewayId,enabled,connected}],onlineInstances:[.instances[]?|select(.Online==true)|{Backend,WorkspaceKey}]}' < <(curl --noproxy '*' -fsS --max-time 3 http://127.0.0.1:9501/v1/status)
    exit 0
    ;;
  rollback)
    [[ -L "${latest_link}" ]] || { echo "no consolidation backup" >&2; exit 1; }
    restore_backup "${backup_root}/$(readlink "${latest_link}")"
    echo "local stack consolidation rolled back"
    exit 0
    ;;
  apply) ;;
  *) echo "usage: $0 [apply|status|rollback] [--base-dir PATH] [--wait-timeout SEC]" >&2; exit 2 ;;
esac

for file in "${config_main}" "${config_second}" "${config_claude}" "${state_main}" "${state_second}" "${state_claude}"; do require_file "${file}"; done
wait_for_idle
stamp="$(date -u +%Y%m%dT%H%M%SZ)-$$"
backup_dir="${backup_root}/${stamp}"
install -d -m 0700 "${backup_dir}"
cp -a "${config_main}" "${backup_dir}/config-main.json"
cp -a "${state_main}" "${backup_dir}/state-main.json"
ln -sfn "${stamp}" "${latest_link}"

jq -s '
  .[0] as $main | .[1] as $second | .[2] as $claude
  | $main
  | .feishu.apps = (($main.feishu.apps + $second.feishu.apps + $claude.feishu.apps) | unique_by(.id))
  | .wecom = $claude.wecom
  | .claude = $claude.claude
  | .workspace.displayNames = (($main.workspace.displayNames // {}) + ($second.workspace.displayNames // {}) + ($claude.workspace.displayNames // {}))
' "${config_main}" "${config_second}" "${config_claude}" >"${backup_dir}/config-merged.json"
jq -s '.[0] as $main | .[1] as $second | .[2] as $claude | $main | .entries = (($main.entries // {}) + ($second.entries // {}) + ($claude.entries // {}))' \
  "${state_main}" "${state_second}" "${state_claude}" >"${backup_dir}/state-merged.json"

for unit in "${legacy_units[@]}" "${active_units[@]}"; do systemctl_user stop "$unit" >/dev/null 2>&1 || true; done
install -m 0600 "${backup_dir}/config-merged.json" "${config_main}"
install -m 0600 "${backup_dir}/state-merged.json" "${state_main}"
for unit in "${legacy_units[@]}"; do systemctl_user disable "$unit" >/dev/null 2>&1 || true; done
for unit in "${active_units[@]}"; do systemctl_user enable "$unit" >/dev/null; done
systemctl_user daemon-reload
for unit in "${active_units[@]}"; do systemctl_user start "$unit"; done

if ! health_gate; then
  echo "consolidated gateway failed health gate; restoring three-stack state" >&2
  restore_backup "${backup_dir}"
  exit 1
fi
echo "local stacks consolidated: backup=${backup_dir}"
