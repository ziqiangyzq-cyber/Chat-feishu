#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "${ROOT_DIR}"

failed=0

search_production_go() {
  local pattern="$1"
  shift
  if command -v rg >/dev/null 2>&1; then
    rg -n "${pattern}" "$@" --glob '!**/*_test.go'
  else
    grep -RInE --include='*.go' --exclude='*_test.go' "${pattern}" "$@"
  fi
}

legacy_uievent_refs="$(search_production_go '\bcontrol\.UIEvent\b' internal || true)"
if [[ -n "${legacy_uievent_refs}" ]]; then
  echo "Forbidden control.UIEvent references in production code:" >&2
  printf '%s\n' "${legacy_uievent_refs}" >&2
  failed=1
fi

legacy_compat_refs="$(search_production_go 'eventcontractcompat' internal || true)"
if [[ -n "${legacy_compat_refs}" ]]; then
  echo "Forbidden eventcontractcompat references in production code:" >&2
  printf '%s\n' "${legacy_compat_refs}" >&2
  failed=1
fi

legacy_projector_entry_matches="$(search_production_go '\bprojector\.Project\(' internal || true)"
if [[ -n "${legacy_projector_entry_matches}" ]]; then
  echo "Forbidden legacy projector entrypoint usage in production code (use ProjectEvent instead):" >&2
  printf '%s\n' "${legacy_projector_entry_matches}" >&2
  failed=1
fi

followup_heuristic_matches="$(search_production_go 'Notice[[:space:]]*!=[[:space:]]*nil|ThreadSelection[[:space:]]*!=[[:space:]]*nil' \
  internal/app/daemon/app_ingress.go \
  internal/core/orchestrator/service_followup_filter.go \
  internal/core/orchestrator/service_path_picker_contract.go \
  internal/core/orchestrator/service_target_picker_owner_card.go || true)"
if [[ -n "${followup_heuristic_matches}" ]]; then
  echo "Forbidden followup payload heuristics in followup filters (use eventcontract semantics):" >&2
  printf '%s\n' "${followup_heuristic_matches}" >&2
  failed=1
fi

resolver_matches="$(search_production_go 'resolveGatewayTarget\(' internal/adapter/feishu || true)"
resolver_disallowed="$(printf '%s\n' "${resolver_matches}" | \
  grep -vE '^internal/adapter/feishu/(controller_gateway|controller_preview|controller_target_resolver)\.go:' || true)"
if [[ -n "${resolver_disallowed}" ]]; then
  echo "Forbidden resolveGatewayTarget call sites outside controller resolver boundary:" >&2
  printf '%s\n' "${resolver_disallowed}" >&2
  failed=1
fi

if [[ "${failed}" -ne 0 ]]; then
  exit 1
fi
