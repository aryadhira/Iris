#!/usr/bin/env bash
# P4 voice acceptance: real sidecar, real irisd, real Postgres, no browser.
#
#   1. /api/voice/speak streams MP3 and names the exact spoken sentence.
#   2. The returned MP3 is transcribed by the real faster-whisper model.
#   3. A stopped sidecar becomes a 503 in Indonesian, never a stack trace.
#
# Never touches port 3300, 4173, 8642, the live gateway, or the live iris/lab
# databases. Throwaway DB and data dir only.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
DB_NAME="iris_p4_accept"
API_PORT="${IRIS_ACCEPT_API_PORT:-3301}"
VOICE_PORT="${IRIS_ACCEPT_VOICE_PORT:-3310}"
BASE="http://127.0.0.1:${API_PORT}"
VOICE="http://127.0.0.1:${VOICE_PORT}"
DSN="postgres://lab:labdev@127.0.0.1:5432/${DB_NAME}?sslmode=disable"
PG_CONTAINER="ailab-postgres"
KEY_FILE="${IRIS_HERMES_KEY_FILE:-$HOME/.config/iris-api-key}"
HERMES_URL="${IRIS_HERMES_URL:-http://127.0.0.1:8642}"
PYTHON="${IRIS_VOICE_PYTHON:-$HOME/Documents/projects/voice-hud/bridge/.venv/bin/python}"
TMP="$(mktemp -d "${HOME}/.hermes/cache/scratch/iris-p4-XXXXXX")"
DATA_DIR="$TMP/data"
PASS_FILE="$TMP/passphrase"
COOKIE="$TMP/cookie"
API_PID=""
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

[[ -x "$PYTHON" ]] || fail "voice Python not found at $PYTHON"
[[ -f "$KEY_FILE" ]] || fail "Hermes key file not found at $KEY_FILE"
command -v docker >/dev/null || fail "docker is required"
[[ -x "$HOME/go/bin/migrate" ]] || fail "golang-migrate not found at ~/go/bin/migrate"
port_busy "$API_PORT" && fail "port ${API_PORT} is busy"
port_busy "$VOICE_PORT" && fail "port ${VOICE_PORT} is busy"

echo "P4 acceptance — $(date -Iseconds)"
psql_admin "DROP DATABASE IF EXISTS ${DB_NAME} WITH (FORCE);" >/dev/null
psql_admin "CREATE DATABASE ${DB_NAME};" >/dev/null
"$HOME/go/bin/migrate" -path "$ROOT/db/migrations" -database "$DSN" up >/dev/null
mkdir -p "$DATA_DIR"
openssl rand -hex 16 >"$PASS_FILE"; chmod 600 "$PASS_FILE"
(cd "$ROOT" && go build -o "$ROOT/bin/irisd" ./cmd/irisd) || fail "go build failed"

IRIS_VOICE_PORT="$VOICE_PORT" "$PYTHON" "$ROOT/voice/server.py" >"$TMP/voice.log" 2>&1 &
VOICE_PID=$!
for _ in $(seq 1 240); do
  curl -fsS "$VOICE/health" 2>/dev/null | grep -q '"warm": *true' && break
  sleep 0.25
done
curl -fsS "$VOICE/health" | grep -q '"warm": *true' || fail "voice sidecar did not warm up"
echo "voice healthy: $(curl -fsS "$VOICE/health")"

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
curl -sS -c "$COOKIE" -X POST "$BASE/api/auth/login" -H 'Content-Type: application/json' \
  -d "{\"passphrase\":\"$(cat "$PASS_FILE")\"}" >/dev/null || fail "login failed"
echo "irisd healthy: $(curl -fsS "$BASE/api/health")"

ROOM=$(curl -sS -b "$COOKIE" -X POST "$BASE/api/rooms" -H 'Content-Type: application/json' \
  -d '{"name":"P4 voice","topic":"voice acceptance"}' | jq -r '.room.id')
[[ -n "$ROOM" && "$ROOM" != "null" ]] || fail "room was not created"

TEXT='## Ringkasan
**Selesai**, port delapan ribu kosong. Biaya Rp50.000, naik 12%.
Server lama sudah dimatikan, jadi port delapan ribu sekarang benar-benar kosong.
```python
print("rahasia")
```
| kolom | nilai |
| --- | --- |
| a | 1 |'

START_MS=$(date +%s%3N)
curl -sS -b "$COOKIE" -D "$TMP/speak.headers" -o "$TMP/speak.mp3" \
  -X POST "$BASE/api/voice/speak" -H 'Content-Type: application/json' \
  -d "$(jq -nc --arg t "$TEXT" '{text:$t}')"
SPEAK_MS=$(( $(date +%s%3N) - START_MS ))
SIZE=$(stat -c%s "$TMP/speak.mp3")
[[ "$SIZE" -gt 2000 ]] || fail "speak returned ${SIZE} bytes, want real MP3 audio"
SPOKEN=$(grep -i '^x-iris-spoken-text:' "$TMP/speak.headers" | sed 's/^[^:]*: *//' | tr -d '\r')
echo "speak: ${SIZE} bytes in ${SPEAK_MS} ms"
echo "spoken: ${SPOKEN}"
for banned in '*' '#' '`' '|' 'http' 'print'; do
  [[ "$SPOKEN" != *"$banned"* ]] || fail "markdown leaked into speech: ${SPOKEN}"
done
[[ "$SPOKEN" == *"detailnya ada di layar"* ]] || fail "long answer was not summarised: ${SPOKEN}"

START_MS=$(date +%s%3N)
curl -sS -b "$COOKIE" -o "$TMP/transcribe.json" \
  -X POST "$BASE/api/voice/transcribe" -F "audio=@$TMP/speak.mp3;type=audio/mpeg"
TRANSCRIBE_MS=$(( $(date +%s%3N) - START_MS ))
TRANSCRIPT=$(jq -r '.text' "$TMP/transcribe.json")
echo "transcribe: ${TRANSCRIBE_MS} ms -> ${TRANSCRIPT}"
echo "$TRANSCRIPT" | grep -qi 'selesai' || fail "round trip lost 'selesai': ${TRANSCRIPT}"
echo "$TRANSCRIPT" | grep -qi 'port' || fail "round trip lost 'port': ${TRANSCRIPT}"

# A quick tap that records nothing must be a 200 no_speech, never a 500.
CODE=$(curl -sS -b "$COOKIE" -o "$TMP/empty.json" -w '%{http_code}' \
  -X POST "$BASE/api/voice/transcribe" -F "audio=@/dev/null;filename=empty.webm;type=audio/webm")
[[ "$CODE" == "200" ]] || fail "empty recording returned ${CODE}"
jq -e '.reason == "no_speech"' "$TMP/empty.json" >/dev/null || fail "empty recording was not no_speech"
echo "empty recording: 200 no_speech"

# Stop the sidecar and prove the proxy answers with Indonesian copy, not a trace.
kill "$VOICE_PID" 2>/dev/null; wait "$VOICE_PID" 2>/dev/null; VOICE_PID=""
CODE=$(curl -sS -b "$COOKIE" -o "$TMP/down.json" -w '%{http_code}' \
  -X POST "$BASE/api/voice/transcribe" -F "audio=@$TMP/speak.mp3;type=audio/mpeg")
[[ "$CODE" == "503" ]] || fail "sidecar down returned ${CODE}, want 503"
grep -q 'suara sedang tidak tersedia' "$TMP/down.json" || fail "503 copy is not Indonesian"
grep -qi 'goroutine\|connection refused\|dial tcp' "$TMP/down.json" && fail "503 leaked internals"
echo "sidecar down: 503 suara sedang tidak tersedia"

echo
echo "voice boot timings:"
grep -E 'stt model|tts warmed|warm-up finished' "$TMP/voice.log" || true
echo
echo "PASS: P4 voice pipeline against the real stack (speak ${SPEAK_MS} ms, transcribe ${TRANSCRIBE_MS} ms)"
