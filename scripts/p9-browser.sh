#!/usr/bin/env bash
# P9 browser acceptance: throwaway DB + data dir, production build served by
# irisd on :3309, a real voice sidecar on :3311, Chromium over CDP at 390x844.
#
# The microphone is a synthetic stream made from real speech audio; the
# transcript comes from the real sidecar. No tap on Kirim is allowed.
#
# Hermes is a local fake on :8659 (scripts/fake-hermes-p9.py), so this run never
# touches the live gateway on :8642, port 3300, 3310, 4173, or the iris/lab
# databases.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
DB_NAME="iris_p9_browser"
API_PORT="${IRIS_P9_API_PORT:-3309}"
VOICE_PORT="${IRIS_ACCEPT_VOICE_PORT:-3311}"
BASE="http://127.0.0.1:${API_PORT}"
VOICE="http://127.0.0.1:${VOICE_PORT}"
DSN="postgres://lab:labdev@127.0.0.1:5432/${DB_NAME}?sslmode=disable"
PG_CONTAINER="ailab-postgres"
FAKE_PORT="${IRIS_P9_FAKE_PORT:-8659}"
HERMES_URL="http://127.0.0.1:${FAKE_PORT}"
VOICE_PYTHON="${IRIS_VOICE_PYTHON:-$HOME/Documents/projects/voice-hud/bridge/.venv/bin/python}"
TMP="$(mktemp -d /tmp/iris-p9b-XXXXXX)"
KEY_FILE="$TMP/key"
DATA_DIR="$TMP/data"
PASS_FILE="$TMP/passphrase"
VAPID_FILE="$TMP/vapid.json"
PRICES_FILE="$TMP/prices.json"
SHOTS="$ROOT/changelogs/assets"
API_PID=""
VOICE_PID=""
FAKE_PID=""

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
  [[ -n "$FAKE_PID" ]] && kill "$FAKE_PID" 2>/dev/null && wait "$FAKE_PID" 2>/dev/null
  psql_admin "DROP DATABASE IF EXISTS ${DB_NAME} WITH (FORCE);" >/dev/null 2>&1
  rm -rf "$TMP"
  exit "$code"
}
trap on_exit EXIT

[[ -x "$VOICE_PYTHON" ]] || fail "voice Python not found at $VOICE_PYTHON"
command -v docker >/dev/null || fail "docker is required"
command -v node >/dev/null || fail "node is required"
[[ -x "$HOME/go/bin/migrate" ]] || fail "golang-migrate not found"
port_busy "$API_PORT" && fail "port ${API_PORT} is busy; refusing to borrow it"
port_busy "$VOICE_PORT" && fail "port ${VOICE_PORT} is busy; refusing to borrow it"
port_busy "$FAKE_PORT" && fail "port ${FAKE_PORT} is busy; refusing to borrow it"

say "P9 browser acceptance — $DB_NAME (throwaway)"
psql_admin "DROP DATABASE IF EXISTS ${DB_NAME} WITH (FORCE);" >/dev/null
psql_admin "CREATE DATABASE ${DB_NAME};" >/dev/null
"$HOME/go/bin/migrate" -path "$ROOT/db/migrations" -database "$DSN" up >/dev/null
mkdir -p "$DATA_DIR" "$SHOTS"
openssl rand -hex 16 >"$PASS_FILE"; chmod 600 "$PASS_FILE"
printf 'fake-p9-key\n' >"$KEY_FILE"; chmod 600 "$KEY_FILE"
printf '{}\n' >"$PRICES_FILE"; chmod 600 "$PRICES_FILE"

say "building the SPA and irisd"
(cd "$ROOT/web" && npm run build >"$TMP/build.log" 2>&1) || { tail -40 "$TMP/build.log"; fail "npm run build failed"; }
(cd "$ROOT" && go build -o "$ROOT/bin/irisd" ./cmd/irisd) || fail "go build failed"

python3 "$ROOT/scripts/fake-hermes-p9.py" "$FAKE_PORT" >"$TMP/fake.log" 2>&1 &
FAKE_PID=$!
for _ in $(seq 1 40); do
  curl -fsS -o /dev/null -X POST "http://127.0.0.1:${FAKE_PORT}/api/sessions" 2>/dev/null && break
  sleep 0.1
done
curl -fsS -o /dev/null -X POST "http://127.0.0.1:${FAKE_PORT}/api/sessions" || fail "fake Hermes did not start"

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

IRIS_WEB_BASE="$BASE" IRIS_VOICE_URL="$VOICE" IRIS_PASSPHRASE_FILE="$PASS_FILE" \
  IRIS_SHOTS_DIR="$SHOTS" IRIS_RESULTS_FILE="$SHOTS/p9-results.json" \
  node "$ROOT/scripts/browser-p9.mjs"

echo
echo "PASS: P9 browser acceptance (screenshots in $SHOTS)"
