#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd -P)"
service_user="chat-feishu-codex"
service_group="chat-feishu-codex"
service_home="/var/lib/chat-feishu-codex"
env_source=""
config_source=""
binary_source=""
codex_binary_source=""
root_prefix=""
activate=1
command="${1:-}"
[[ -n "${command}" ]] && shift

usage() {
  cat <<'EOF'
usage: scripts/deploy/chat-feishu-codex.sh <install|upgrade|rollback|status> [options]

options:
  --binary <path>       codex-remote binary for install/upgrade
  --codex-binary <path> Codex CLI to install into the isolated runtime
  --env-file <path>     secret env file containing OPENAI_API_KEY
  --config-file <path>  WeCom-enabled codex-remote config.json
  --user <name>         dedicated service user (default chat-feishu-codex)
  --group <name>        dedicated service group (default same as user)
  --home <path>         isolated HOME (default /var/lib/chat-feishu-codex)
  --root <path>         prefix filesystem writes for smoke tests/images
  --no-activate         do not call useradd/systemctl/chown

Install requires --binary, --codex-binary, --env-file and --config-file. Upgrade
requires only --binary and preserves the managed Codex CLI and live env/config.
Pass --codex-binary during upgrade only when upgrading Codex too. Every deployment
records a restorable snapshot before publishing changes.
EOF
}

while (($# > 0)); do
  case "$1" in
    --binary) binary_source="${2:?missing --binary value}"; shift 2 ;;
    --codex-binary) codex_binary_source="${2:?missing --codex-binary value}"; shift 2 ;;
    --env-file) env_source="${2:?missing --env-file value}"; shift 2 ;;
    --config-file) config_source="${2:?missing --config-file value}"; shift 2 ;;
    --user) service_user="${2:?missing --user value}"; shift 2 ;;
    --group) service_group="${2:?missing --group value}"; shift 2 ;;
    --home) service_home="${2:?missing --home value}"; shift 2 ;;
    --root) root_prefix="${2:?missing --root value}"; shift 2 ;;
    --no-activate) activate=0; shift ;;
    -h|--help) usage; exit 0 ;;
    *) echo "unknown option: $1" >&2; usage >&2; exit 2 ;;
  esac
done

case "${command}" in install|upgrade|rollback|status) ;; *) usage >&2; exit 2 ;; esac
[[ "${service_user}" =~ ^[a-z_][a-z0-9_-]*[$]?$ ]] || { echo "invalid service user: ${service_user}" >&2; exit 2; }
[[ "${service_group}" =~ ^[a-z_][a-z0-9_-]*[$]?$ ]] || { echo "invalid service group: ${service_group}" >&2; exit 2; }
[[ "${service_home}" == "/var/lib/${service_user}" ]] || {
  echo "--home must be the dedicated /var/lib/${service_user} directory" >&2
  exit 2
}
[[ -z "${root_prefix}" || "${root_prefix}" == /* ]] || { echo "--root must be absolute" >&2; exit 2; }

target() { printf '%s%s' "${root_prefix}" "$1"; }
lib_dir="$(target /usr/local/lib/chat-feishu-codex)"
codex_binary_path="${lib_dir}/codex"
wrapper_path="$(target /usr/local/bin/chat-feishu-codex)"
unit_path="$(target /etc/systemd/system/chat-feishu-codex.service)"
env_path="$(target /etc/chat-feishu-codex/chat-feishu-codex.env)"
home_path="$(target "${service_home}")"
config_path="${home_path}/xdg/config/codex-remote/config.json"
snapshot_root="$(target /var/lib/chat-feishu-codex-deploy/snapshots)"

require_file() { [[ -f "$1" ]] || { echo "required file is missing: $1" >&2; exit 1; }; }
validate_env() {
  local path="$1" openai_key feishu_gateway feishu_id feishu_secret wecom_id wecom_secret
  env_value() { awk -F= -v wanted="$1" '$1 == wanted {sub(/^[^=]*=/, ""); print; exit}' "${path}"; }
  openai_key="$(env_value OPENAI_API_KEY)"
  feishu_gateway="$(env_value FEISHU_GATEWAY_ID)"
  feishu_id="$(env_value FEISHU_APP_ID)"
  feishu_secret="$(env_value FEISHU_APP_SECRET)"
  wecom_id="$(env_value WECOM_BOT_ID)"
  wecom_secret="$(env_value WECOM_SECRET)"
  [[ -n "${openai_key}" && "${openai_key}" != replace-me ]] || {
    echo "env file must define a non-placeholder OPENAI_API_KEY" >&2
    exit 1
  }
  if [[ -n "${feishu_gateway}${feishu_id}${feishu_secret}" ]] &&
    [[ -z "${feishu_gateway}" || -z "${feishu_id}" || -z "${feishu_secret}" ]]; then
    echo "FEISHU_GATEWAY_ID, FEISHU_APP_ID and FEISHU_APP_SECRET must be set together" >&2
    exit 1
  fi
  if [[ -n "${wecom_id}${wecom_secret}" ]] && [[ -z "${wecom_id}" || -z "${wecom_secret}" ]]; then
    echo "WECOM_BOT_ID and WECOM_SECRET must be set together" >&2
    exit 1
  fi
  [[ -n "${feishu_id}" || -n "${wecom_id}" ]] || {
    echo "env file must enable at least one Feishu or WeCom channel" >&2
    exit 1
  }
}
render_unit() {
  sed \
    -e "s|@SERVICE_USER@|${service_user}|g" \
    -e "s|@SERVICE_GROUP@|${service_group}|g" \
    -e "s|@SERVICE_HOME@|${service_home}|g" \
    -e 's|@ENV_FILE@|/etc/chat-feishu-codex/chat-feishu-codex.env|g' \
    -e 's|@WRAPPER_PATH@|/usr/local/bin/chat-feishu-codex|g' \
    "${repo_root}/deploy/linux-systemd/chat-feishu-codex.service.in"
}
assert_managed_paths_safe() {
  local path
  for path in "${home_path}" "${home_path}/codex-home" "${home_path}/xdg" "${home_path}/xdg/config" \
    "${home_path}/xdg/config/codex-remote" "${home_path}/xdg/data" "${home_path}/xdg/state" "${home_path}/xdg/cache"; do
    [[ ! -L "${path}" ]] || { echo "managed service path must not be a symlink: ${path}" >&2; return 1; }
    [[ ! -e "${path}" || -d "${path}" ]] || { echo "managed service path is not a directory: ${path}" >&2; return 1; }
  done
  for path in "${config_path}" "${env_path}" "${unit_path}" "${wrapper_path}" "${lib_dir}/codex-remote" "${codex_binary_path}"; do
    [[ ! -L "${path}" ]] || { echo "managed deployment target must not be a symlink: ${path}" >&2; return 1; }
  done
}
snapshot() {
  local stamp snapshot_dir item relative original_active=inactive original_enabled=disabled
  stamp="$(date -u +%Y%m%dT%H%M%SZ)-$$"
  snapshot_dir="${snapshot_root}/${stamp}"
  if ((activate == 1)); then
    install -d -o root -g root -m 0700 "${snapshot_root}" "${snapshot_dir}" "${snapshot_dir}/files" || return 1
  else
    install -d -m 0700 "${snapshot_root}" "${snapshot_dir}" "${snapshot_dir}/files" || return 1
  fi
  if ((activate == 1)); then
    if systemctl is-enabled --quiet chat-feishu-codex.service; then original_enabled=enabled; fi
    printf '%s\n' "${original_enabled}" >"${snapshot_dir}/enabled" || return 1
    if systemctl is-active --quiet chat-feishu-codex.service; then original_active=active; fi
    printf '%s\n' "${original_active}" >"${snapshot_dir}/active" || return 1
    if [[ "${original_active}" == active ]]; then systemctl stop chat-feishu-codex.service || return 1; fi
  else
    printf 'disabled\n' >"${snapshot_dir}/enabled" || return 1
    printf 'inactive\n' >"${snapshot_dir}/active" || return 1
  fi
  if ! assert_managed_paths_safe; then
    if ((activate == 1)) && [[ "${original_active}" == active ]]; then systemctl start chat-feishu-codex.service || true; fi
    return 1
  fi
  for item in "${lib_dir}/codex-remote" "${codex_binary_path}" "${wrapper_path}" "${unit_path}" "${env_path}" "${config_path}"; do
    relative="${item#${root_prefix}/}"
    if ! mkdir -p "${snapshot_dir}/files/$(dirname "${relative}")"; then
      if ((activate == 1)) && [[ "${original_active}" == active ]]; then systemctl start chat-feishu-codex.service || true; fi
      return 1
    fi
    if [[ -e "${item}" || -L "${item}" ]]; then
      if ! cp -aP -- "${item}" "${snapshot_dir}/files/${relative}" || ! printf 'present\t%s\n' "${relative}" >>"${snapshot_dir}/manifest.tsv"; then
        if ((activate == 1)) && [[ "${original_active}" == active ]]; then systemctl start chat-feishu-codex.service || true; fi
        return 1
      fi
    else
      if ! printf 'absent\t%s\n' "${relative}" >>"${snapshot_dir}/manifest.tsv"; then
        if ((activate == 1)) && [[ "${original_active}" == active ]]; then systemctl start chat-feishu-codex.service || true; fi
        return 1
      fi
    fi
  done
  if ! ln -sfn "${stamp}" "${snapshot_root}/latest"; then
    if ((activate == 1)) && [[ "${original_active}" == active ]]; then systemctl start chat-feishu-codex.service || true; fi
    return 1
  fi
}
restore_latest() {
  local snapshot_dir state relative destination expected
  [[ -L "${snapshot_root}/latest" ]] || { echo "no rollback snapshot is available" >&2; exit 1; }
  snapshot_dir="${snapshot_root}/$(readlink "${snapshot_root}/latest")"
  require_file "${snapshot_dir}/manifest.tsv"
  while IFS=$'\t' read -r state relative; do
    case "${relative}" in
      "${lib_dir#${root_prefix}/}/codex-remote"|"${lib_dir#${root_prefix}/}/codex"|"${wrapper_path#${root_prefix}/}"|"${unit_path#${root_prefix}/}"|"${env_path#${root_prefix}/}"|"${config_path#${root_prefix}/}") ;;
      *) echo "rollback manifest contains an unexpected target: ${relative}" >&2; return 1 ;;
    esac
    [[ "${state}" == present || "${state}" == absent ]] || { echo "invalid rollback state: ${state}" >&2; return 1; }
    destination="${root_prefix}/${relative}"
    if [[ "${state}" == present ]]; then
      mkdir -p "$(dirname "${destination}")"
      cp -aP -- "${snapshot_dir}/files/${relative}" "${destination}.rollback-stage"
      mv -Tf -- "${destination}.rollback-stage" "${destination}"
    else
      rm -f -- "${destination}"
    fi
  done <"${snapshot_dir}/manifest.tsv"
}
latest_snapshot_dir() { printf '%s/%s' "${snapshot_root}" "$(readlink "${snapshot_root}/latest")"; }
restore_service_state() {
  ((activate == 1)) || return 0
  local snapshot_dir enabled_state active_state
  snapshot_dir="$(latest_snapshot_dir)"
  enabled_state="$(<"${snapshot_dir}/enabled")"
  active_state="$(<"${snapshot_dir}/active")"
  systemctl daemon-reload
  if [[ -f "${unit_path}" ]]; then
    if [[ "${enabled_state}" == enabled ]]; then systemctl enable chat-feishu-codex.service; else systemctl disable chat-feishu-codex.service || true; fi
    if [[ "${active_state}" == active ]]; then
      systemctl restart chat-feishu-codex.service
      wait_for_healthy_service
    else
      systemctl stop chat-feishu-codex.service || true
    fi
  else
    systemctl disable --now chat-feishu-codex.service || true
    systemctl reset-failed chat-feishu-codex.service || true
  fi
}
service_env_value() { awk -F= -v wanted="$1" '$1 == wanted {sub(/^[^=]*=/, ""); print; exit}' "${env_path}"; }
wait_for_healthy_service() {
  local host port deadline pid_before restarts_before pid_after restarts_after status_json feishu_gateway wecom_id
  host="$(service_env_value RELAY_API_HOST)"; host="${host:-127.0.0.1}"
  [[ "${host}" != "0.0.0.0" && "${host}" != "::" ]] || host="127.0.0.1"
  port="$(service_env_value RELAY_API_PORT)"; port="${port:-9501}"
  feishu_gateway="$(service_env_value FEISHU_GATEWAY_ID)"
  wecom_id="$(service_env_value WECOM_BOT_ID)"
  deadline=$((SECONDS + 30))
  while ((SECONDS < deadline)); do
    if systemctl is-active --quiet chat-feishu-codex.service &&
      curl --noproxy '*' --connect-timeout 1 --max-time 2 -fsS "http://${host}:${port}/healthz" >/dev/null &&
      status_json="$(curl --noproxy '*' --connect-timeout 1 --max-time 2 -fsS "http://${host}:${port}/v1/status")" &&
      jq -e --arg feishu "${feishu_gateway}" --arg wecom "${wecom_id}" '
        any(.instances[]?; .Backend == "codex" and .Managed == true and .Online == true)
        and ($feishu == "" or any(.gateways[]?; .gatewayId == $feishu and .disabled == false and .state == "connected"))
        and ($wecom == "" or any(.wecomBots[]?; .enabled == true and .connected == true))
      ' <<<"${status_json}" >/dev/null; then
      pid_before="$(systemctl show -p MainPID --value chat-feishu-codex.service)"
      restarts_before="$(systemctl show -p NRestarts --value chat-feishu-codex.service)"
      sleep 3
      pid_after="$(systemctl show -p MainPID --value chat-feishu-codex.service)"
      restarts_after="$(systemctl show -p NRestarts --value chat-feishu-codex.service)"
      if [[ "${pid_before}" != 0 && "${pid_before}" == "${pid_after}" && "${restarts_before}" == "${restarts_after}" ]] &&
        systemctl is-active --quiet chat-feishu-codex.service &&
        curl --noproxy '*' --connect-timeout 1 --max-time 2 -fsS "http://${host}:${port}/healthz" >/dev/null &&
        status_json="$(curl --noproxy '*' --connect-timeout 1 --max-time 2 -fsS "http://${host}:${port}/v1/status")" &&
        jq -e --arg feishu "${feishu_gateway}" --arg wecom "${wecom_id}" '
          any(.instances[]?; .Backend == "codex" and .Managed == true and .Online == true)
          and ($feishu == "" or any(.gateways[]?; .gatewayId == $feishu and .disabled == false and .state == "connected"))
          and ($wecom == "" or any(.wecomBots[]?; .enabled == true and .connected == true))
        ' <<<"${status_json}" >/dev/null; then
        return 0
      fi
    fi
    sleep 1
  done
  echo "service did not become healthy and stable within 30 seconds" >&2
  return 1
}
activate_service() {
  ((activate == 1)) || return 0
  systemctl daemon-reload
  systemctl enable chat-feishu-codex.service
  systemctl restart chat-feishu-codex.service
  wait_for_healthy_service
}
transaction_active=0
rollback_failed_deploy() {
  local status=$?
  trap - EXIT INT TERM
  if ((transaction_active == 1)); then
    transaction_active=0
    echo "deployment failed; restoring the pre-deploy snapshot" >&2
    set +e
    restore_latest
    local restore_status=$?
    if ((activate == 1)); then
      restore_service_state
      if (($? != 0)); then restore_status=1; fi
    fi
    if ((restore_status != 0)); then
      echo "automatic rollback failed; manual intervention is required" >&2
    fi
  fi
  exit "${status}"
}

if [[ "${command}" == status ]]; then
  printf 'unit=%s\nwrapper=%s\nbinary=%s\ncodex=%s\nenv=%s\nconfig=%s\nsnapshots=%s\n' "${unit_path}" "${wrapper_path}" "${lib_dir}/codex-remote" "${codex_binary_path}" "${env_path}" "${config_path}" "${snapshot_root}"
  ((activate == 0)) || systemctl --no-pager status chat-feishu-codex.service
  exit 0
fi

if [[ "${command}" == rollback ]]; then
  ((activate == 0)) || systemctl stop chat-feishu-codex.service || true
  restore_latest
  restore_service_state
  echo "chat-feishu-codex rollback complete"
  exit 0
fi

require_file "${binary_source}"
[[ -x "${binary_source}" ]] || { echo "binary is not executable: ${binary_source}" >&2; exit 1; }
if [[ "${command}" == install ]]; then
  require_file "${codex_binary_source}"
  require_file "${env_source}"
  require_file "${config_source}"
else
  [[ -z "${env_source}" ]] || require_file "${env_source}"
  [[ -z "${config_source}" ]] || require_file "${config_source}"
  require_file "${env_path}"
  require_file "${config_path}"
  require_file "${codex_binary_path}"
fi
[[ -z "${codex_binary_source}" ]] || { require_file "${codex_binary_source}"; [[ -x "${codex_binary_source}" ]] || { echo "Codex binary is not executable: ${codex_binary_source}" >&2; exit 1; }; }
if [[ -n "${codex_binary_source}" ]] && ! "${codex_binary_source}" app-server --help 2>&1 | grep -q 'Usage: codex app-server'; then
  echo "Codex binary does not provide the required app-server command" >&2
  exit 1
fi
[[ -z "${env_source}" ]] || validate_env "${env_source}"
[[ ! -L "${home_path}" ]] || { echo "service home must not be a symlink: ${service_home}" >&2; exit 1; }
[[ ! -L "$(dirname "${snapshot_root}")" && ! -L "${snapshot_root}" ]] || { echo "snapshot path must not contain a managed symlink" >&2; exit 1; }

if ((activate == 1)); then
  [[ -z "${root_prefix}" ]] || { echo "--root requires --no-activate" >&2; exit 2; }
  if ! getent group "${service_group}" >/dev/null; then groupadd --system "${service_group}"; fi
  if ! id "${service_user}" >/dev/null 2>&1; then
    useradd --system --gid "${service_group}" --home-dir "${service_home}" --create-home --shell /usr/sbin/nologin "${service_user}"
  fi
fi

mkdir -p "${lib_dir}" "$(dirname "${wrapper_path}")" "$(dirname "${unit_path}")" "$(dirname "${env_path}")" "${snapshot_root}"
snapshot
transaction_active=1
trap rollback_failed_deploy EXIT INT TERM
if ((activate == 1)) && [[ "${command}" == install ]]; then
  install -d -o "${service_user}" -g "${service_group}" -m 0700 "${home_path}" "${home_path}/codex-home" "${home_path}/xdg" \
    "${home_path}/xdg/config" "${home_path}/xdg/config/codex-remote" "${home_path}/xdg/data" "${home_path}/xdg/state" "${home_path}/xdg/cache"
elif ((activate == 0)) && [[ "${command}" == install ]]; then
  mkdir -p "$(dirname "${config_path}")" "${home_path}/codex-home" "${home_path}/xdg/data" "${home_path}/xdg/state" "${home_path}/xdg/cache"
fi
install -m 0755 "${binary_source}" "${lib_dir}/codex-remote.stage"
mv -Tf "${lib_dir}/codex-remote.stage" "${lib_dir}/codex-remote"
if [[ -n "${codex_binary_source}" ]]; then
  install -m 0755 "${codex_binary_source}" "${codex_binary_path}.stage"
  mv -Tf "${codex_binary_path}.stage" "${codex_binary_path}"
fi
install -m 0755 "${repo_root}/deploy/linux-systemd/chat-feishu-codex" "${wrapper_path}"
render_unit >"${unit_path}.stage"
chmod 0644 "${unit_path}.stage"
mv -Tf "${unit_path}.stage" "${unit_path}"
if [[ -n "${env_source}" ]]; then install -m 0600 "${env_source}" "${env_path}"; fi
if [[ -n "${config_source}" ]]; then
  install -m 0600 "${config_source}" "${config_path}.stage"
  if ((activate == 1)); then chown "${service_user}:${service_group}" "${config_path}.stage"; fi
  mv -Tf "${config_path}.stage" "${config_path}"
fi
if ((activate == 1)); then
  chown root:"${service_group}" "${env_path}"
  chmod 0640 "${env_path}"
  runuser -u "${service_user}" -- env HOME="${service_home}" CODEX_HOME="${service_home}/codex-home" "${codex_binary_path}" app-server --help 2>&1 | grep -q 'Usage: codex app-server'
fi
activate_service
transaction_active=0
trap - EXIT INT TERM
echo "chat-feishu-codex ${command} complete"
