#!/usr/bin/env bash
# P5 browser acceptance: throwaway DB + data dir, real Hermes turn, production
# build served by irisd itself on 3301 (no Vite process), Chromium over CDP.
#
# Seeds one real room with one real attachment and one real Hermes reply, then
# scripts/browser-p5.mjs searches it from the rooms panel, opens the deep link,
# exports the transcript from the room menu and reads the downloaded file.
#
# Never touches port 3300, 4173, 8642, the live gateway or iris/lab databases.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
DB_NAME="iris_p5_browser"
API_PORT="${IRIS_ACCEPT_API_PORT:-3301}"
BASE="http://127.0.0.1:${API_PORT}"
DSN="postgres://lab:labdev@127.0.0.1:5432/${DB_NAME}?sslmode=disable"
PG_CONTAINER="ailab-postgres"
KEY_FILE="${IRIS_HERMES_KEY_FILE:-$HOME/.config/iris-api-key}"
HERMES_URL="${IRIS_HERMES_URL:-http://127.0.0.1:8642}"
TMP="$(mktemp -d /tmp/iris-p5b-XXXXXX)"
DATA_DIR="$TMP/data"
PASS_FILE="$TMP/passphrase"
COOKIE="$TMP/cookie"
SHOTS="$ROOT/changelogs/assets"
TOKEN="LAYARKAPALP5"
API_PID=""

fail() { echo "FAIL: $*" >&2; exit 1; }
say() { echo "[$(date -Iseconds)] $*"; }
psql_admin() { docker exec "$PG_CONTAINER" psql -U lab -d postgres -v ON_ERROR_STOP=1 -At -c "$1"; }
psql_iris() { docker exec "$PG_CONTAINER" psql -U lab -d "$DB_NAME" -v ON_ERROR_STOP=1 -At -c "$1"; }
port_busy() { ss -ltn 2>/dev/null | grep -q ":$1 "; }

on_exit() {
  local code=$?
  set +e
  if [[ $code -ne 0 ]]; then
    [[ -f "$TMP/api.log" ]] && { echo; echo "--- irisd log (tail) ---"; tail -c 8000 "$TMP/api.log"; }
  fi
  [[ -n "$API_PID" ]] && kill "$API_PID" 2>/dev/null && wait "$API_PID" 2>/dev/null
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
command -v jq >/dev/null || fail "jq is required"
command -v node >/dev/null || fail "node is required"
[[ -x "$HOME/go/bin/migrate" ]] || fail "golang-migrate not found"
port_busy "$API_PORT" && fail "port ${API_PORT} is busy; refusing to borrow it"

say "P5 browser acceptance — $DB_NAME (throwaway)"
psql_admin "DROP DATABASE IF EXISTS ${DB_NAME} WITH (FORCE);" >/dev/null
psql_admin "CREATE DATABASE ${DB_NAME};" >/dev/null
"$HOME/go/bin/migrate" -path "$ROOT/db/migrations" -database "$DSN" up >/dev/null
mkdir -p "$DATA_DIR" "$SHOTS"
openssl rand -hex 16 >"$PASS_FILE"; chmod 600 "$PASS_FILE"

(cd "$ROOT/web" && npm run build >"$TMP/build.log" 2>&1) || { tail -40 "$TMP/build.log"; fail "npm run build failed"; }
(cd "$ROOT" && go build -o "$ROOT/bin/irisd" ./cmd/irisd) || fail "go build failed"

IRIS_ADDR="127.0.0.1:${API_PORT}" \
IRIS_DATABASE_URL="$DSN" \
IRIS_HERMES_URL="$HERMES_URL" \
IRIS_HERMES_KEY_FILE="$KEY_FILE" \
IRIS_PASSPHRASE="$(cat "$PASS_FILE")" \
IRIS_COOKIE_SECURE=false \
IRIS_DATA_DIR="$DATA_DIR" \
IRIS_WEB_DIR="$ROOT/web/dist" \
"$ROOT/bin/irisd" >"$TMP/api.log" 2>&1 &
API_PID=$!
for _ in $(seq 1 80); do curl -fsS "$BASE/api/health" >/dev/null 2>&1 && break; sleep 0.25; done
[[ "$(curl -s -m 3 -o /dev/null -w '%{http_code}' "$BASE/api/health")" == "200" ]] || fail "irisd unhealthy"
curl -sS "$BASE/" | grep -q '<div id="app">' || fail "irisd is not serving the built SPA"

curl -sS -c "$COOKIE" -X POST "$BASE/api/auth/login" -H 'Content-Type: application/json' \
  -d "{\"passphrase\":\"$(cat "$PASS_FILE")\"}" >/dev/null || fail "login failed"

ROOM_JSON="$(curl -sS -b "$COOKIE" -X POST "$BASE/api/rooms" -H 'Content-Type: application/json' \
  -d '{"name":"P5 Cari dan Ekspor","topic":"Bukti pencarian dan transkrip"}')"
ROOM="$(jq -r '.room.id' <<<"$ROOM_JSON")"
[[ -n "$ROOM" && "$ROOM" != "null" ]] || { cat "$ROOM_JSON"; fail "room not created"; }

printf 'lampiran nyata untuk ekspor P5\n' >"$TMP/bukti-p5.txt"
UPLOAD="$(curl -sS -b "$COOKIE" -X POST "$BASE/api/rooms/${ROOM}/uploads" -F "file=@$TMP/bukti-p5.txt")"
FILE_ID="$(jq -r '.file.id' <<<"$UPLOAD")"
[[ -n "$FILE_ID" && "$FILE_ID" != "null" ]] || { cat "$UPLOAD"; fail "upload failed"; }

say "seeded room $ROOM; asking Hermes for one real reply"
curl -sS -b "$COOKIE" -N --max-time 240 -X POST "$BASE/api/rooms/${ROOM}/messages" \
  -H 'Content-Type: application/json' \
  -d "$(jq -nc --arg t "Jawab satu kalimat singkat dan sertakan kata ${TOKEN}. Sebut juga file bukti-p5.txt. Jangan memakai tool." --arg f "$FILE_ID" '{text:$t, attachmentIds:[$f]}')" \
  >"$TMP/turn.sse" || fail "Hermes turn failed"
grep -E '^event: done' "$TMP/turn.sse" >/dev/null || fail "Hermes turn did not complete"
grep -qi "$TOKEN" <(psql_iris "SELECT body FROM messages WHERE room_id='$ROOM'") \
  || fail "the seeded token is not in the room"
say "room ready; messages=$(psql_iris "SELECT count(*) FROM messages WHERE room_id='$ROOM'")"

IRIS_WEB_BASE="$BASE" IRIS_PASSPHRASE_FILE="$PASS_FILE" IRIS_SHOTS_DIR="$SHOTS" \
  IRIS_P5_ROOM_ID="$ROOM" IRIS_P5_TOKEN="$TOKEN" IRIS_P5_ATTACHMENT="bukti-p5.txt" \
  IRIS_RESULTS_FILE="$SHOTS/p5-results.json" \
  node "$ROOT/scripts/browser-p5.mjs"

echo
echo "PASS: P5 browser acceptance (screenshots in $SHOTS)"
