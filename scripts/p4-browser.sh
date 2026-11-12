#!/usr/bin/env bash
# P4 browser acceptance: throwaway DB + data dir, real sidecar, real irisd,
# production build served by Vite preview on 4175, Chromium over DevTools.
#
# The microphone is a Web Audio stream fed by real edge-tts speech; the
# transcript is produced by the real faster-whisper pipeline. Nothing is typed
# into the composer or injected as a fake transcript.
#
# Never touches port 3300, 4173, 8642, the live gateway or the live databases.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
DB_NAME="iris_p4_browser"
API_PORT="${IRIS_ACCEPT_API_PORT:-3301}"
WEB_PORT="${IRIS_ACCEPT_WEB_PORT:-4175}"
VOICE_PORT="${IRIS_ACCEPT_VOICE_PORT:-3310}"
BASE="http://127.0.0.1:${API_PORT}"
WEB="http://127.0.0.1:${WEB_PORT}"
VOICE="http://127.0.0.1:${VOICE_PORT}"
DSN="postgres://lab:labdev@127.0.0.1:5432/${DB_NAME}?sslmode=disable"
PG_CONTAINER="ailab-postgres"
KEY_FILE="${IRIS_HERMES_KEY_FILE:-$HOME/.config/iris-api-key}"
HERMES_URL="${IRIS_HERMES_URL:-http://127.0.0.1:8642}"
PYTHON="${IRIS_VOICE_PYTHON:-$HOME/Documents/projects/voice-hud/bridge/.venv/bin/python}"
TMP="$(mktemp -d "${HOME}/.hermes/cache/scratch/iris-p4b-XXXXXX")"
DATA_DIR="$TMP/data"
PASS_FILE="$TMP/passphrase"
SHOTS="$ROOT/changelogs/assets"
API_PID=""
WEB_PID=""
VOICE_PID=""

fail() { echo "FAIL: $*" >&2; exit 1; }
psql_admin() { docker exec "$PG_CONTAINER" psql -U lab -d postgres -v ON_ERROR_STOP=1 -At -c "$1"; }
psql_iris() { docker exec "$PG_CONTAINER" psql -U lab -d "$DB_NAME" -v ON_ERROR_STOP=1 -At -c "$1"; }
port_busy() { ss -ltn 2>/dev/null | grep -q ":$1 "; }

on_exit() {
  local code=$?
  set +e
  if [[ $code -ne 0 ]]; then
    [[ -f "$TMP/api.log" ]] && { echo; echo "--- irisd log (tail) ---"; tail -c 7000 "$TMP/api.log"; }
    [[ -f "$TMP/voice.log" ]] && { echo; echo "--- voice log (tail) ---"; tail -c 7000 "$TMP/voice.log"; }
    [[ -f "$TMP/web.log" ]] && { echo; echo "--- web log (tail) ---"; tail -c 3000 "$TMP/web.log"; }
  fi
  [[ -n "$API_PID" ]] && kill "$API_PID" 2>/dev/null && wait "$API_PID" 2>/dev/null
  [[ -n "$WEB_PID" ]] && kill "$WEB_PID" 2>/dev/null && wait "$WEB_PID" 2>/dev/null
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

[[ -x "$PYTHON" ]] || fail "voice Python not found at $PYTHON"
[[ -f "$KEY_FILE" ]] || fail "Hermes key file not found at $KEY_FILE"
command -v docker >/dev/null || fail "docker is required"
command -v node >/dev/null || fail "node is required"
[[ -x "$HOME/go/bin/migrate" ]] || fail "golang-migrate not found"
port_busy "$API_PORT" && fail "port ${API_PORT} is busy"
port_busy "$WEB_PORT" && fail "port ${WEB_PORT} is busy"
port_busy "$VOICE_PORT" && fail "port ${VOICE_PORT} is busy"

echo "P4 browser check — $(date -Iseconds)"
psql_admin "DROP DATABASE IF EXISTS ${DB_NAME} WITH (FORCE);" >/dev/null
psql_admin "CREATE DATABASE ${DB_NAME};" >/dev/null
"$HOME/go/bin/migrate" -path "$ROOT/db/migrations" -database "$DSN" up >/dev/null
mkdir -p "$DATA_DIR" "$SHOTS"
openssl rand -hex 16 >"$PASS_FILE"; chmod 600 "$PASS_FILE"

(cd "$ROOT/web" && npm run build >"$TMP/build.log" 2>&1) || { tail -30 "$TMP/build.log"; fail "npm run build failed"; }
(cd "$ROOT" && go build -o "$ROOT/bin/irisd" ./cmd/irisd) || fail "go build failed"

IRIS_VOICE_PORT="$VOICE_PORT" "$PYTHON" "$ROOT/voice/server.py" >"$TMP/voice.log" 2>&1 &
VOICE_PID=$!
for _ in $(seq 1 240); do curl -fsS "$VOICE/health" 2>/dev/null | grep -q '"warm": *true' && break; sleep 0.25; done
curl -fsS "$VOICE/health" | grep -q '"warm": *true' || fail "voice sidecar did not warm up"

IRIS_ADDR="127.0.0.1:${API_PORT}" \
IRIS_DATABASE_URL="$DSN" \
IRIS_HERMES_URL="$HERMES_URL" \
IRIS_HERMES_KEY_FILE="$KEY_FILE" \
IRIS_PASSPHRASE="$(cat "$PASS_FILE")" \
IRIS_COOKIE_SECURE=false \
IRIS_DATA_DIR="$DATA_DIR" \
IRIS_VOICE_URL="$VOICE" \
"$ROOT/bin/irisd" >"$TMP/api.log" 2>&1 &
API_PID=$!
for _ in $(seq 1 80); do curl -fsS "$BASE/api/health" >/dev/null 2>&1 && break; sleep 0.25; done
[[ "$(curl -s -m 3 -o /dev/null -w '%{http_code}' "$BASE/api/health")" == "200" ]] || fail "irisd unhealthy"

(cd "$ROOT/web" && exec env IRIS_API_TARGET="$BASE" node node_modules/vite/bin/vite.js preview \
  --port "$WEB_PORT" --strictPort >"$TMP/web.log" 2>&1) &
WEB_PID=$!
for _ in $(seq 1 80); do curl -fsS "$WEB/" >/dev/null 2>&1 && break; sleep 0.25; done
[[ "$(curl -s -m 3 -o /dev/null -w '%{http_code}' "$WEB/")" == "200" ]] || fail "web preview unhealthy"

# Real speech for the synthetic microphone. Played into a Web Audio destination,
# recorded by MediaRecorder, then transcribed by the real model.
curl -sS -X POST "$VOICE/speak" -H 'Content-Type: application/json' \
  -d '{"text":"ingatkan saya besok pagi"}' -o "$TMP/mic.mp3"
[[ "$(stat -c%s "$TMP/mic.mp3")" -gt 2000 ]] || fail "could not make the microphone speech"

IRIS_WEB_BASE="$WEB" IRIS_PASSPHRASE_FILE="$PASS_FILE" IRIS_SHOTS_DIR="$SHOTS" \
  IRIS_PTT_AUDIO_FILE="$TMP/mic.mp3" IRIS_RESULTS_FILE="$SHOTS/p4-results.json" \
  node "$ROOT/scripts/browser-p4.mjs"

echo
echo "voice boot timings:"
grep -E 'stt model|tts warmed|warm-up finished' "$TMP/voice.log" || true
echo
echo "voice requests:"
grep -E ' stt | tts ' "$TMP/voice.log" | tail -20 || true
echo
echo "PASS: P4 browser acceptance"
