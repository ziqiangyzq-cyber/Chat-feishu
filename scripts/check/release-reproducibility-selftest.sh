#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
BUILD_SCRIPT="${ROOT_DIR}/scripts/release/build-artifacts.sh"
GO_BIN="${GO_BIN:-go}"
work_dir="$(mktemp -d)"
cleanup() {
  chmod -R u+w "${work_dir}" 2>/dev/null || true
  rm -rf "${work_dir}"
}
trap cleanup EXIT

version="v0.0.0-repro.1"
build_time="2026-07-22T00:00:00Z"
common_env=(
  "GO_BIN=${GO_BIN}"
  "CODEX_REMOTE_BUILD_TIME_UTC=${build_time}"
  "CODEX_REMOTE_BUILD_FLAVOR=shipping"
  "CODEX_REMOTE_BUILD_BRANCH=reproducibility-selftest"
)

for output in "${work_dir}/first" "${work_dir}/second"; do
  env "${common_env[@]}" bash "${BUILD_SCRIPT}" "${version}" "${output}" \
    --current-platform-only \
    --skip-admin-ui-build \
    --allow-dirty-fixture >"${output##*/}.log"
done

python3 - "${work_dir}/first" "${work_dir}/second" <<'PY'
import hashlib
import sys
from pathlib import Path

def inventory(root: Path):
    result = {}
    for path in sorted(p for p in root.rglob('*') if p.is_file()):
        rel = path.relative_to(root).as_posix()
        result[rel] = hashlib.sha256(path.read_bytes()).hexdigest()
    return result

first = inventory(Path(sys.argv[1]))
second = inventory(Path(sys.argv[2]))
if first != second:
    names = sorted(set(first) | set(second))
    for name in names:
        if first.get(name) != second.get(name):
            print(f"non-reproducible: {name}: {first.get(name)} != {second.get(name)}", file=sys.stderr)
    raise SystemExit(1)
if not first:
    print("release reproducibility selftest produced no files", file=sys.stderr)
    raise SystemExit(1)
print(f"release reproducibility selftest: ok ({len(first)} files)")
PY
