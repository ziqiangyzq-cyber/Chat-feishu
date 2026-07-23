#!/usr/bin/env bash
set -euo pipefail

usage() {
  cat <<'EOF'
usage: scripts/relay/install-codex-remote-watchdog-macos.sh [--uninstall]

Install or uninstall the per-user macOS watchdog for com.codex-remote.service.
EOF
}

action="install"
case "${1:-}" in
  "")
    ;;
  --uninstall)
    action="uninstall"
    ;;
  --help|-h)
    usage
    exit 0
    ;;
  *)
    usage >&2
    exit 1
    ;;
esac

if [[ "$(uname -s)" != "Darwin" ]]; then
  echo "this watchdog installer only supports macOS" >&2
  exit 1
fi

base_dir="${HOME:?HOME is required}"
uid="$(id -u)"
watchdog_label="com.codex-remote.watchdog"
watchdog_target="gui/${uid}/${watchdog_label}"
install_dir="${base_dir}/Library/Application Support/codex-remote/bin"
installed_script="${install_dir}/codex-remote-watchdog"
plist_dir="${base_dir}/Library/LaunchAgents"
plist_path="${plist_dir}/${watchdog_label}.plist"
logs_dir="${base_dir}/.local/share/codex-remote/logs"
launcher_log="${logs_dir}/codex-remote-watchdog-launchd.log"
source_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source_script="${source_dir}/codex-remote-watchdog-macos.sh"

if [[ "${action}" == "uninstall" ]]; then
  launchctl bootout "${watchdog_target}" >/dev/null 2>&1 || true
  rm -f "${plist_path}" "${installed_script}"
  echo "uninstalled ${watchdog_label}"
  exit 0
fi

if [[ ! -f "${source_script}" ]]; then
  echo "watchdog source script not found: ${source_script}" >&2
  exit 1
fi

mkdir -p "${install_dir}" "${plist_dir}" "${logs_dir}"
install -m 0755 "${source_script}" "${installed_script}"

cat > "${plist_path}" <<EOF
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
    <key>Label</key>
    <string>${watchdog_label}</string>
    <key>ProgramArguments</key>
    <array>
        <string>${installed_script}</string>
    </array>
    <key>RunAtLoad</key>
    <true/>
    <key>StartInterval</key>
    <integer>60</integer>
    <key>ProcessType</key>
    <string>Background</string>
    <key>StandardOutPath</key>
    <string>${launcher_log}</string>
    <key>StandardErrorPath</key>
    <string>${launcher_log}</string>
</dict>
</plist>
EOF
chmod 0644 "${plist_path}"

launchctl bootout "${watchdog_target}" >/dev/null 2>&1 || true
launchctl bootstrap "gui/${uid}" "${plist_path}"
launchctl enable "${watchdog_target}"
launchctl kickstart "${watchdog_target}"

echo "installed ${watchdog_label}"
echo "plist: ${plist_path}"
echo "log: ${logs_dir}/codex-remote-watchdog.log"
