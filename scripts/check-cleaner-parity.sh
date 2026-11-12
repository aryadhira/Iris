#!/usr/bin/env bash
# Prove the Go and Python reply cleaners cannot drift.
#
# Every file in testdata/replies/ is fed to internal/reply.Clean (Go) and
# voice.normalise.clean_reply (Python). The script fails on the first byte
# difference and prints both outputs with newlines made visible.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
PYTHON="${IRIS_PYTHON:-python3}"
FIXTURES=("$ROOT"/testdata/replies/*.txt)

fail() { echo "FAIL: $*" >&2; exit 1; }

[[ ${#FIXTURES[@]} -gt 0 && -f "${FIXTURES[0]}" ]] || fail "no fixtures in testdata/replies"
command -v "$PYTHON" >/dev/null || fail "python interpreter not found: $PYTHON"

count=0
for fixture in "${FIXTURES[@]}"; do
  go_out="$(cd "$ROOT" && go run ./scripts/cleaner-fixtures "$fixture")"
  py_out="$("$PYTHON" "$ROOT/scripts/cleaner_fixtures.py" "$fixture")"
  count=$((count + 1))
  if [[ "$go_out" != "$py_out" ]]; then
    echo "DIVERGED: ${fixture#$ROOT/}" >&2
    "$PYTHON" - "$go_out" "$py_out" <<'PY' >&2
import base64, sys
go = base64.b64decode(sys.argv[1]).decode("utf-8", "replace")
py = base64.b64decode(sys.argv[2]).decode("utf-8", "replace")
print("--- Go (internal/reply.Clean) ---")
print(repr(go))
print("--- Python (voice.normalise.clean_reply) ---")
print(repr(py))
PY
    fail "cleaner parity broken on ${fixture#$ROOT/}"
  fi
done

echo "PASS: cleaner parity — ${count} fixture(s), Go == Python"
