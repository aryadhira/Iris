#!/usr/bin/env bash
# P2 acceptance: real irisd, real Hermes, throwaway Postgres and data dir.
# Uploads a PNG, sees the thumbnail, asks Hermes for a markdown artifact,
# downloads it and compares the bytes with the file the agent wrote. Runs at
# 390x844 and 1440x900, then deletes the throwaway database and Hermes sessions.
#
# Never touches port 3300, the lab/iris databases, or an existing irisd.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
DB_NAME="iris_p2_accept"
API_PORT="${IRIS_ACCEPT_API_PORT:-3301}"
WEB_PORT="${IRIS_ACCEPT_WEB_PORT:-4175}"
BASE="http://127.0.0.1:${API_PORT}"
WEB="http://127.0.0.1:${WEB_PORT}"
DSN="postgres://lab:labdev@127.0.0.1:5432/${DB_NAME}?sslmode=disable"
PG_CONTAINER="ailab-postgres"
KEY_FILE="${IRIS_HERMES_KEY_FILE:-$HOME/.config/iris-api-key}"
HERMES_URL="${IRIS_HERMES_URL:-http://127.0.0.1:8642}"
TMP="$(mktemp -d "${TMPDIR:-/tmp}/iris-p2-XXXXXX")"
DATA_DIR="$TMP/data"
PASS_FILE="$TMP/passphrase"
SHOTS="$ROOT/changelogs/assets"
API_PID=""
WEB_PID=""

fail() {
  echo "FAIL: $*" >&2
  exit 1
}

psql_admin() {
  docker exec "$PG_CONTAINER" psql -U lab -d postgres -v ON_ERROR_STOP=1 -At -c "$1"
}

psql_iris() {
  docker exec "$PG_CONTAINER" psql -U lab -d "$DB_NAME" -v ON_ERROR_STOP=1 -At -c "$1"
}

port_busy() {
  ss -ltn 2>/dev/null | grep -q ":$1 "
}

on_exit() {
  local code=$?
  set +e
  if [[ $code -ne 0 ]]; then
    [[ -f "$TMP/api.log" ]] && { echo; echo "--- irisd log (tail) ---"; tail -c 6000 "$TMP/api.log"; }
    [[ -f "$TMP/web.log" ]] && { echo; echo "--- web preview log (tail) ---"; tail -c 3000 "$TMP/web.log"; }
  fi
  if [[ -n "$API_PID" ]] && kill -0 "$API_PID" 2>/dev/null; then
    kill "$API_PID" 2>/dev/null
    wait "$API_PID" 2>/dev/null
  fi
  if [[ -n "$WEB_PID" ]] && kill -0 "$WEB_PID" 2>/dev/null; then
    kill "$WEB_PID" 2>/dev/null
    wait "$WEB_PID" 2>/dev/null
  fi
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
command -v docker >/dev/null || fail "docker is required"
command -v node >/dev/null || fail "node is required"
command -v openssl >/dev/null || fail "openssl is required"
[[ -x "$HOME/go/bin/migrate" ]] || fail "golang-migrate not found at ~/go/bin/migrate"
port_busy "$API_PORT" && fail "port ${API_PORT} is busy; refusing to borrow it"
port_busy "$WEB_PORT" && fail "port ${WEB_PORT} is busy; refusing to borrow it"

echo "Iris P2 acceptance — $(date -Iseconds)"
echo "database: ${DB_NAME} (throwaway, dropped on exit)"
echo "data dir: ${DATA_DIR} (throwaway)"
echo "irisd:    ${BASE}"
echo "web:      ${WEB}"
echo "hermes:   ${HERMES_URL}"
echo

# 1. Throwaway database and data dir ----------------------------------------
psql_admin "DROP DATABASE IF EXISTS ${DB_NAME} WITH (FORCE);" >/dev/null
psql_admin "CREATE DATABASE ${DB_NAME};" >/dev/null
"$HOME/go/bin/migrate" -path "$ROOT/db/migrations" -database "$DSN" up >/dev/null
mkdir -p "$DATA_DIR"
echo "database: created, schema version $(psql_iris "SELECT version FROM schema_migrations;" | tr -d '[:space:]')"

# 2. Build must pass before anything is served ------------------------------
(cd "$ROOT/web" && npm run build >"$TMP/build.log" 2>&1) || {
  tail -30 "$TMP/build.log" || true
  fail "npm run build failed"
}
echo "build:    npm run build passed"
grep -E 'dist/assets/index-.*\.(js|css)' "$TMP/build.log" | sed 's/^/          /' || true

# 3. irisd on the throwaway port -------------------------------------------
openssl rand -hex 16 >"$PASS_FILE"
chmod 600 "$PASS_FILE"

(cd "$ROOT" && go build -o "$ROOT/bin/irisd" ./cmd/irisd) || fail "go build failed"

IRIS_ADDR="127.0.0.1:${API_PORT}" \
IRIS_DATABASE_URL="$DSN" \
IRIS_HERMES_URL="$HERMES_URL" \
IRIS_HERMES_KEY_FILE="$KEY_FILE" \
IRIS_PASSPHRASE="$(cat "$PASS_FILE")" \
IRIS_COOKIE_SECURE=false \
IRIS_DATA_DIR="$DATA_DIR" \
"$ROOT/bin/irisd" >"$TMP/api.log" 2>&1 &
API_PID=$!

for _ in $(seq 1 80); do
  curl -fsS "$BASE/api/health" >/dev/null 2>&1 && break
  sleep 0.25
done
[[ "$(curl -s -m 3 -o /dev/null -w '%{http_code}' "$BASE/api/health")" == "200" ]] || fail "irisd did not become healthy"
echo "irisd:    healthy ($(curl -s "$BASE/api/health"))"

# 4. Serve the production build through Vite's proxy ------------------------
mkdir -p "$SHOTS"
(cd "$ROOT/web" && exec env IRIS_API_TARGET="$BASE" node node_modules/vite/bin/vite.js preview \
  --port "$WEB_PORT" --strictPort >"$TMP/web.log" 2>&1) &
WEB_PID=$!

for _ in $(seq 1 80); do
  curl -fsS "$WEB/" >/dev/null 2>&1 && break
  sleep 0.25
done
[[ "$(curl -s -m 3 -o /dev/null -w '%{http_code}' "$WEB/")" == "200" ]] || fail "web preview did not become healthy"
echo "web:      serving the build ($WEB)"
echo

# 5. Drive a real browser ---------------------------------------------------
DRIVER_CODE=0
IRIS_WEB_BASE="$WEB" \
IRIS_PASSPHRASE_FILE="$PASS_FILE" \
IRIS_SHOTS_DIR="$SHOTS" \
IRIS_RESULTS_FILE="$SHOTS/p2-results.json" \
IRIS_DATA_DIR="$DATA_DIR" \
IRIS_TEMP_DIR="$TMP" \
node "$ROOT/scripts/browser-p2.mjs" || DRIVER_CODE=$?

echo
echo "database after the run:"
echo "  rooms:     $(psql_iris "SELECT count(*) FROM rooms")"
echo "  messages:  $(psql_iris "SELECT count(*) FROM messages")"
echo "  files:     $(psql_iris "SELECT count(*) FROM files")"
echo "  user files:$(psql_iris "SELECT count(*) FROM files WHERE source = 'user'")"
echo "  agent files:$(psql_iris "SELECT count(*) FROM files WHERE source = 'agent'")"
echo
echo "room folders:"
find "$DATA_DIR/rooms" -maxdepth 3 -type f -printf '  %P %s bytes\n' 2>/dev/null | sort || true
echo
echo "screenshots: ${SHOTS}/p2-*.png"
echo "results:     ${SHOTS}/p2-results.json"

[[ $DRIVER_CODE -eq 0 ]] || fail "browser checks failed (exit ${DRIVER_CODE})"

echo
echo "PASS: P2 files acceptance against the real stack"
