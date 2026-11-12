#!/usr/bin/env bash
# P8 browser acceptance: throwaway DB + data dir, production build served by
# irisd on :3309, a real voice sidecar on :3311, Chromium over CDP at 390x844.
#
# Never touches port 3300, 3310, 4173, 8642, the live gateway or iris/lab
# databases.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
DB_NAME="iris_p8_browser"
API_PORT=3309
VOICE_PORT=3311
BASE="http://127.0.0.1:${API_PORT}"
VOICE="http://127.0.0.1:${VOICE_PORT}"
DSN="postgres://lab:labdev@127.0.0.1:5432/${DB_NAME}?sslmode=disable"
PG_CONTAINER="ailab-postgres"
KEY_FILE="${IRIS_HERMES_KEY_FILE:-$HOME/.config/iris-api-key}"
HERMES_URL="${IRIS_HERMES_URL:-http://127.0.0.1:8642}"
VOICE_PYTHON="${IRIS_VOICE_PYTHON:-$HOME/Documents/projects/voice-hud/bridge/.venv/bin/python}"
TMP="$(mktemp -d /tmp/iris-p8b-XXXXXX)"
DATA_DIR="$TMP/data"
PASS_FILE="$TMP/passphrase"
VAPID_FILE="$TMP/vapid.json"
PRICES_FILE="$TMP/prices.json"
SHOTS="$ROOT/changelogs/assets"
API_PID=""
VOICE_PID=""

fail() { echo "FAIL: $*" >&2; exit 1; }
say() { echo "[$(date -Iseconds)] $*"; }
psql_admin() { docker exec "$PG_CONTAINER" psql -U lab -d postgres -v ON_ERROR_STOP=1 -At -c "$1"; }
psql_iris() { docker exec "$PG_CONTAINER" psql -U lab -d "$DB_NAME" -v ON_ERROR_STOP=1 -At -c "$1"; }
port_busy() { ss -ltn 2>/dev/null | grep -q ":$1 "; }

on_exit() {
  local code=$?
  set +e
  if [[ $code -ne 0 ]]; then
    [[ -f "$TMP/api.log" ]] && { echo; echo "--- irisd log (tail) ---"; tail -c 12000 "$TMP/api.log"; }
    [[ -f "$TMP/voice.log" ]] && { echo; echo "--- voice log (tail) ---"; tail -c 6000 "$TMP/voice.log"; }
  fi
  [[ -n "$API_PID" ]] && kill "$API_PID" 2>/dev/null && wait "$API_PID" 2>/dev/null
  [[ -n "$VOICE_PID" ]] && kill "$VOICE_PID" 2>/dev/null && wait "$VOICE_PID" 2>/dev/null
  if [[ -f "$KEY_FILE" ]]; then
    for sid in $(psql_iris "SELECT hermes_session_id FROM rooms" 2>/dev/null); do
      curl -sS -o /dev/null -X DELETE "$HERMES_URL/api/sessions/${sid}" \
        -H "Authorization: Bearer $(cat "$KEY_FILE")" 2>/dev/null
    done
  fi
  psql_admin "DROP DATABASE IF EXISTS ${DB_NAME} WITH (FORCE);" >/dev/null 2>&1
  rm -rf "$TMP"
  exit "$code"
}
trap on_exit EXIT

[[ -f "$KEY_FILE" ]] || fail "Hermes key file not found at $KEY_FILE"
[[ -x "$VOICE_PYTHON" ]] || fail "voice Python not found at $VOICE_PYTHON"
command -v docker >/dev/null || fail "docker is required"
command -v node >/dev/null || fail "node is required"
[[ -x "$HOME/go/bin/migrate" ]] || fail "golang-migrate not found"
port_busy "$API_PORT" && fail "port ${API_PORT} is busy; refusing to borrow it"
port_busy "$VOICE_PORT" && fail "port ${VOICE_PORT} is busy; refusing to borrow it"

WORKSPACE="$(ls -d "$HOME"/.hermes/installs/*/environments/*/workspace 2>/dev/null | head -1 || true)"
[[ -n "$WORKSPACE" && -d "$WORKSPACE" ]] || fail "Hermes workspace not found"
HERMES_PYTHON="$(dirname "$WORKSPACE")/venv/bin/python"
[[ -x "$HERMES_PYTHON" ]] || fail "Hermes interpreter not found at $HERMES_PYTHON"

say "P8 browser acceptance — $DB_NAME (throwaway)"
psql_admin "DROP DATABASE IF EXISTS ${DB_NAME} WITH (FORCE);" >/dev/null
psql_admin "CREATE DATABASE ${DB_NAME};" >/dev/null
"$HOME/go/bin/migrate" -path "$ROOT/db/migrations" -database "$DSN" up >/dev/null
mkdir -p "$DATA_DIR" "$SHOTS"
openssl rand -hex 16 >"$PASS_FILE"; chmod 600 "$PASS_FILE"
PYTHONPATH="$WORKSPACE" "$HERMES_PYTHON" "$ROOT/scripts/seed-prices.py" --out "$PRICES_FILE" >/dev/null

say "building the SPA and irisd"
(cd "$ROOT/web" && npm run build >"$TMP/build.log" 2>&1) || { tail -40 "$TMP/build.log"; fail "npm run build failed"; }
(cd "$ROOT" && go build -o "$ROOT/bin/irisd" ./cmd/irisd) || fail "go build failed"

IRIS_VOICE_PORT="$VOICE_PORT" "$VOICE_PYTHON" "$ROOT/voice/server.py" >"$TMP/voice.log" 2>&1 &
VOICE_PID=$!
for _ in $(seq 1 320); do
  curl -fsS "$VOICE/health" 2>/dev/null | grep -q '"warm": *true' && break
  sleep 0.25
done
curl -fsS "$VOICE/health" | grep -q '"warm": *true' || fail "voice sidecar did not warm up"

IRIS_ADDR="127.0.0.1:${API_PORT}" \
IRIS_DATABASE_URL="$DSN" \
IRIS_HERMES_URL="$HERMES_URL" \
IRIS_HERMES_KEY_FILE="$KEY_FILE" \
IRIS_PASSPHRASE="$(cat "$PASS_FILE")" \
IRIS_COOKIE_SECURE=false \
IRIS_DATA_DIR="$DATA_DIR" \
IRIS_WEB_DIR="$ROOT/web/dist" \
IRIS_VOICE_URL="$VOICE" \
IRIS_PRICES_FILE="$PRICES_FILE" \
IRIS_DAILY_BUDGET_USD="" \
IRIS_VAPID_KEY_FILE="$VAPID_FILE" \
"$ROOT/bin/irisd" >"$TMP/api.log" 2>&1 &
API_PID=$!
for _ in $(seq 1 80); do curl -fsS "$BASE/api/health" >/dev/null 2>&1 && break; sleep 0.25; done
[[ "$(curl -s -m 3 -o /dev/null -w '%{http_code}' "$BASE/api/health")" == "200" ]] || fail "irisd unhealthy"
curl -fsS "$BASE/" | grep -q '<div id="app">' || fail "irisd is not serving the built SPA"

IRIS_WEB_BASE="$BASE" IRIS_PASSPHRASE_FILE="$PASS_FILE" IRIS_SHOTS_DIR="$SHOTS" \
  IRIS_RESULTS_FILE="$SHOTS/p8-results.json" \
  node "$ROOT/scripts/browser-p8.mjs"

echo
echo "PASS: P8 browser acceptance (screenshots in $SHOTS)"
