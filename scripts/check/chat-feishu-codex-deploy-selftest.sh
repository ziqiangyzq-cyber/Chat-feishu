#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd -P)"
test_root="$(mktemp -d)"
cleanup() { rm -rf "${test_root}"; }
trap cleanup EXIT

printf '#!/usr/bin/env bash\necho v1\n' >"${test_root}/binary-v1"
printf '#!/usr/bin/env bash\necho v2\n' >"${test_root}/binary-v2"
chmod +x "${test_root}/binary-v1" "${test_root}/binary-v2"
printf '#!/usr/bin/env bash\nif [[ "$*" == "app-server --help" ]]; then echo "Usage: codex app-server"; exit 0; fi\necho codex-test\n' >"${test_root}/codex"
chmod +x "${test_root}/codex"
printf 'OPENAI_API_KEY=test-only\nFEISHU_GATEWAY_ID=main\nFEISHU_APP_ID=cli_test\nFEISHU_APP_SECRET=test-only\nWECOM_BOT_ID=bot_test\nWECOM_SECRET=test-only\n' >"${test_root}/service.env"
printf '{"wecom":{"enabled":true,"botId":"test","secret":"test"}}\n' >"${test_root}/config.json"
printf 'model = "test"\n' >"${test_root}/codex-config.toml"

deploy() {
  local command="$1"
  shift
  bash "${repo_root}/scripts/deploy/chat-feishu-codex.sh" "${command}" --root "${test_root}/root" --no-activate "$@"
}
if bash "${repo_root}/scripts/deploy/chat-feishu-codex.sh" install --root "${test_root}/unsafe" --no-activate --home / \
  --binary "${test_root}/binary-v1" --codex-binary "${test_root}/codex" --codex-config "${test_root}/codex-config.toml" --env-file "${test_root}/service.env" --config-file "${test_root}/config.json" >/dev/null 2>&1; then
  echo "unsafe service home was accepted" >&2
  exit 1
fi
[[ ! -e "${test_root}/unsafe" ]]
printf '#!/usr/bin/env bash\necho fake-version-only\n' >"${test_root}/bad-codex"
chmod +x "${test_root}/bad-codex"
if bash "${repo_root}/scripts/deploy/chat-feishu-codex.sh" install --root "${test_root}/bad-root" --no-activate \
  --binary "${test_root}/binary-v1" --codex-binary "${test_root}/bad-codex" --codex-config "${test_root}/codex-config.toml" --env-file "${test_root}/service.env" --config-file "${test_root}/config.json" >/dev/null 2>&1; then
  echo "Codex binary without app-server was accepted" >&2
  exit 1
fi
deploy install --binary "${test_root}/binary-v1" --codex-binary "${test_root}/codex" --codex-config "${test_root}/codex-config.toml" --env-file "${test_root}/service.env" --config-file "${test_root}/config.json"

unit="${test_root}/root/etc/systemd/system/chat-feishu-codex.service"
wrapper="${test_root}/root/usr/local/bin/chat-feishu-codex"
live_binary="${test_root}/root/usr/local/lib/chat-feishu-codex/codex-remote"
live_codex="${test_root}/root/usr/local/lib/chat-feishu-codex/codex"
live_codex_config="${test_root}/root/var/lib/chat-feishu-codex/codex-home/config.toml"
live_env="${test_root}/root/etc/chat-feishu-codex/chat-feishu-codex.env"
live_config="${test_root}/root/var/lib/chat-feishu-codex/xdg/config/codex-remote/config.json"
grep -q '^User=chat-feishu-codex$' "${unit}"
grep -q '^EnvironmentFile=/etc/chat-feishu-codex/chat-feishu-codex.env$' "${unit}"
grep -q '^Description=Chat Feishu dedicated Feishu and WeCom gateway$' "${unit}"
grep -q '^ProtectSystem=strict$' "${unit}"
grep -q '^ProtectHome=true$' "${unit}"
grep -q 'export CODEX_HOME=' "${wrapper}"
grep -q 'OPENAI_API_KEY is required' "${wrapper}"
grep -q 'CODEX_REAL_BINARY=' "${wrapper}"
[[ -x "${live_codex}" ]]
grep -q 'model = "test"' "${live_codex_config}"
snapshot_root="${test_root}/root/var/lib/chat-feishu-codex-deploy/snapshots"
[[ -d "${snapshot_root}" ]]
[[ ! -e "${test_root}/root/var/lib/chat-feishu-codex/deploy-snapshots" ]]

env_before="$(sha256sum "${live_env}" | cut -d' ' -f1)"
config_before="$(sha256sum "${live_config}" | cut -d' ' -f1)"
deploy upgrade --binary "${test_root}/binary-v2"
grep -q 'echo v2' "${live_binary}"
[[ "$(sha256sum "${live_env}" | cut -d' ' -f1)" == "${env_before}" ]]
[[ "$(sha256sum "${live_config}" | cut -d' ' -f1)" == "${config_before}" ]]

deploy rollback
grep -q 'echo v1' "${live_binary}"
[[ "$(sha256sum "${live_env}" | cut -d' ' -f1)" == "${env_before}" ]]
[[ "$(sha256sum "${live_config}" | cut -d' ' -f1)" == "${config_before}" ]]

# A fresh-install rollback restores the pre-install absence without trying to
# keep generated artifacts around.
fresh_root="${test_root}/fresh-root"
bash "${repo_root}/scripts/deploy/chat-feishu-codex.sh" install --root "${fresh_root}" --no-activate \
  --binary "${test_root}/binary-v1" --codex-binary "${test_root}/codex" --codex-config "${test_root}/codex-config.toml" --env-file "${test_root}/service.env" --config-file "${test_root}/config.json"
bash "${repo_root}/scripts/deploy/chat-feishu-codex.sh" rollback --root "${fresh_root}" --no-activate
[[ ! -e "${fresh_root}/etc/systemd/system/chat-feishu-codex.service" ]]
[[ ! -e "${fresh_root}/usr/local/lib/chat-feishu-codex/codex-remote" ]]
echo "chat-feishu-codex deploy self-test passed"
