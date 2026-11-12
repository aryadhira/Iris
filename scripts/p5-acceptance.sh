#!/usr/bin/env bash
# P5 acceptance against the real stack, built from this tree:
#   irisd (serving web/dist) + the real voice sidecar + a throwaway Postgres.
#
# Proves in one run:
#   - /r/<id> is served by irisd and /api/* never returns HTML;
#   - one real Hermes turn;
#   - export.md contains the turn and the attachment name;
#   - full-text search finds a known word and misses an unseen one;
#   - backup.sh makes a verified archive; restore.sh restores it into
#     iris_restore_check and the row counts match; the target is dropped.
#
# Ports 3301/3310 are used, never 3300. The live iris/lab databases, the live
# gateway and ports 8080/4173/8642 are never touched.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
DB_NAME="iris_p5_accept"
API_PORT="${IRIS_ACCEPT_API_PORT:-3301}"
VOICE_PORT="${IRIS_ACCEPT_VOICE_PORT:-3310}"
BASE="http://127.0.0.1:${API_PORT}"
VOICE="http://127.0.0.1:${VOICE_PORT}"
DSN="postgres://lab:labdev@127.0.0.1:5432/${DB_NAME}?sslmode=disable"
PG_CONTAINER="ailab-postgres"
KEY_FILE="${IRIS_HERMES_KEY_FILE:-$HOME/.config/iris-api-key}"
HERMES_URL="${IRIS_HERMES_URL:-http://127.0.0.1:8642}"
PYTHON="${IRIS_VOICE_PYTHON:-$HOME/Documents/projects/voice-hud/bridge/.venv/bin/python}"
TMP="$(mktemp -d /tmp/iris-p5-XXXXXX)"
DATA_DIR="$TMP/data"
SHOTS="$ROOT/changelogs/assets"
PASS_FILE="$TMP/passphrase"
COOKIE="$TMP/cookie"
TOKEN="NELAYANKAPAL"
API_PID=""
VOICE_PID=""

fail() { echo "FAIL: $*" >&2; exit 1; }
say() { echo "[$(date -Iseconds)] $*"; }
psql_admin() { docker exec "$PG_CONTAINER" psql -U lab -d postgres -v ON_ERROR_STOP=1 -At -F'|' -c "$1"; }
psql_iris() { docker exec "$PG_CONTAINER" psql -U lab -d "$DB_NAME" -v ON_ERROR_STOP=1 -At -F'|' -c "$1"; }
psql_other() { docker exec "$PG_CONTAINER" psql -U lab -d "$1" -v ON_ERROR_STOP=1 -At -F'|' -c "$2"; }
port_busy() { ss -ltn 2>/dev/null | grep -q ":$1 "; }

on_exit() {
  local code=$?
  set +e
  if [[ $code -ne 0 ]]; then
    [[ -f "$TMP/api.log" ]] && { echo; echo "--- irisd log (tail) ---"; tail -c 8000 "$TMP/api.log"; }
    [[ -f "$TMP/voice.log" ]] && { echo; echo "--- voice log (tail) ---"; tail -c 4000 "$TMP/voice.log"; }
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
  psql_admin "DROP DATABASE IF EXISTS iris_restore_check WITH (FORCE);" >/dev/null 2>&1
  rm -rf "$TMP"
  exit "$code"
}
trap on_exit EXIT

[[ -f "$KEY_FILE" ]] || fail "Hermes key file not found at $KEY_FILE"
[[ -x "$PYTHON" ]] || fail "voice Python not found at $PYTHON"
command -v docker >/dev/null || fail "docker is required"
command -v jq >/dev/null || fail "jq is required"
command -v node >/dev/null || fail "node is required"
[[ -x "$HOME/go/bin/migrate" ]] || fail "golang-migrate not found at ~/go/bin/migrate"
port_busy "$API_PORT" && fail "port ${API_PORT} is busy; refusing to borrow it"
port_busy "$VOICE_PORT" && fail "port ${VOICE_PORT} is busy; refusing to borrow it"

say "P5 acceptance — $DB_NAME (throwaway), irisd $BASE, voice $VOICE"
psql_admin "DROP DATABASE IF EXISTS ${DB_NAME} WITH (FORCE);" >/dev/null
psql_admin "CREATE DATABASE ${DB_NAME};" >/dev/null
"$HOME/go/bin/migrate" -path "$ROOT/db/migrations" -database "$DSN" up >/dev/null
mkdir -p "$DATA_DIR" "$SHOTS"
openssl rand -hex 16 >"$PASS_FILE"; chmod 600 "$PASS_FILE"

say "building the SPA and irisd"
(cd "$ROOT/web" && npm run build >"$TMP/build.log" 2>&1) || { tail -40 "$TMP/build.log"; fail "npm run build failed"; }
(cd "$ROOT" && go build -o "$ROOT/bin/irisd" ./cmd/irisd) || fail "go build failed"

say "starting the voice sidecar"
IRIS_VOICE_PORT="$VOICE_PORT" "$PYTHON" "$ROOT/voice/server.py" >"$TMP/voice.log" 2>&1 &
VOICE_PID=$!
for _ in $(seq 1 320); do curl -fsS "$VOICE/health" 2>/dev/null | grep -q '"warm": *true' && break; sleep 0.25; done
curl -fsS "$VOICE/health" | grep -q '"warm": *true' || fail "voice sidecar did not warm up"

say "starting irisd (serving web/dist)"
IRIS_ADDR="127.0.0.1:${API_PORT}" \
IRIS_DATABASE_URL="$DSN" \
IRIS_HERMES_URL="$HERMES_URL" \
IRIS_HERMES_KEY_FILE="$KEY_FILE" \
IRIS_PASSPHRASE="$(cat "$PASS_FILE")" \
IRIS_COOKIE_SECURE=false \
IRIS_DATA_DIR="$DATA_DIR" \
IRIS_VOICE_URL="$VOICE" \
IRIS_WEB_DIR="$ROOT/web/dist" \
"$ROOT/bin/irisd" >"$TMP/api.log" 2>&1 &
API_PID=$!
for _ in $(seq 1 80); do curl -fsS "$BASE/api/health" >/dev/null 2>&1 && break; sleep 0.25; done
[[ "$(curl -s -m 3 -o /dev/null -w '%{http_code}' "$BASE/api/health")" == "200" ]] || fail "irisd did not become healthy"

# ---------------------------------------------------------------------------
# 1. irisd serves the SPA; the API never leaks index.html.
# ---------------------------------------------------------------------------
SPA_BYTES="$(curl -sS "$BASE/" | wc -c)"
curl -sS "$BASE/" | grep -q '<div id="app">' || fail "GET / did not return the SPA shell"
curl -sS "$BASE/r/00000000-0000-0000-0000-000000000000" | grep -q '<div id="app">' || fail "/r/<id> did not fall back to index.html"
ASSET="$(grep -o '/assets/index-[^"]*\.js' "$ROOT/web/dist/index.html" | head -1)"
ASSET_CACHE="$(curl -sSI "$BASE$ASSET" | tr -d '\r' | awk -F': ' 'tolower($1)=="cache-control"{print $2}')"
ASSET_CODE="$(curl -s -o /dev/null -w '%{http_code}' "$BASE$ASSET")"
[[ "$ASSET_CODE" == "200" ]] || fail "hashed asset $ASSET returned $ASSET_CODE"
[[ "$ASSET_CACHE" == *immutable* ]] || fail "hashed asset is not immutable: $ASSET_CACHE"
NOAUTH_CODE="$(curl -s -o "$TMP/api-nope.json" -w '%{http_code}' "$BASE/api/nope")"
[[ "$NOAUTH_CODE" == "401" ]] || fail "/api/nope without a cookie = $NOAUTH_CODE, want 401"
grep -qi '<html' "$TMP/api-nope.json" && fail "/api/nope returned HTML without a cookie"

curl -sS -c "$COOKIE" -X POST "$BASE/api/auth/login" -H 'Content-Type: application/json' \
  -d "{\"passphrase\":\"$(cat "$PASS_FILE")\"}" >/dev/null || fail "login failed"
AUTH_CODE="$(curl -s -o "$TMP/api-nope-auth.json" -w '%{http_code}' -b "$COOKIE" "$BASE/api/nope")"
[[ "$AUTH_CODE" == "404" ]] || fail "authenticated /api/nope = $AUTH_CODE, want 404"
grep -qi '<html' "$TMP/api-nope-auth.json" && fail "authenticated /api/nope returned HTML"
jq -e '.error' "$TMP/api-nope-auth.json" >/dev/null || fail "/api/nope is not JSON"
say "SPA: / ${SPA_BYTES} bytes; $ASSET immutable; /api/nope $NOAUTH_CODE then $AUTH_CODE JSON"

# ---------------------------------------------------------------------------
# 2. Real room, real attachment, real Hermes turn.
# ---------------------------------------------------------------------------
ROOM_JSON="$(curl -sS -b "$COOKIE" -X POST "$BASE/api/rooms" -H 'Content-Type: application/json' \
  -d '{"name":"P5 Pelayaran","topic":"Verifikasi hardening"}')"
ROOM="$(jq -r '.room.id' <<<"$ROOM_JSON")"
[[ -n "$ROOM" && "$ROOM" != "null" ]] || { cat "$ROOM_JSON"; fail "room was not created"; }
say "room: $ROOM"

printf 'bukti backup dan ekspor\n' >"$TMP/bukti-p5.txt"
UPLOAD="$(curl -sS -b "$COOKIE" -X POST "$BASE/api/rooms/${ROOM}/uploads" -F "file=@$TMP/bukti-p5.txt")"
FILE_ID="$(jq -r '.file.id' <<<"$UPLOAD")"
[[ -n "$FILE_ID" && "$FILE_ID" != "null" ]] || { cat "$UPLOAD"; fail "upload failed"; }
say "uploaded bukti-p5.txt (${#FILE_ID} char id)"

SEND_START=$(date +%s)
curl -sS -b "$COOKIE" -N --max-time 240 -X POST "$BASE/api/rooms/${ROOM}/messages" \
  -H 'Content-Type: application/json' \
  -d "$(jq -nc --arg t "Jawab satu kalimat singkat. Sebut kata ${TOKEN} dan nama file bukti-p5.txt. Jangan memakai tool." --arg f "$FILE_ID" '{text:$t, attachmentIds:[$f]}')" \
  >"$TMP/turn.sse" || fail "the Hermes turn did not finish"
SEND_END=$(date +%s)
grep -E '^event: done' "$TMP/turn.sse" >/dev/null || fail "the turn produced no done event"
MESSAGE_COUNT="$(psql_iris "SELECT count(*) FROM messages WHERE room_id='$ROOM'")"
[[ "$MESSAGE_COUNT" -ge 2 ]] || fail "expected user+assistant messages, found $MESSAGE_COUNT"
say "Hermes turn done in $((SEND_END - SEND_START)) s; messages in room: $MESSAGE_COUNT"

# ---------------------------------------------------------------------------
# 3. Export and search.
# ---------------------------------------------------------------------------
curl -sS -b "$COOKIE" -D "$TMP/export.headers" -o "$TMP/export.md" "$BASE/api/rooms/${ROOM}/export.md"
EXPORT_BYTES="$(wc -c <"$TMP/export.md")"
EXPORT_LINES="$(wc -l <"$TMP/export.md")"
for needle in "$TOKEN" 'bukti-p5.txt' '**Kamu**' '**Hermes**' '# P5 Pelayaran' '**Topik:** Verifikasi hardening'; do
  grep -qF "$needle" "$TMP/export.md" || { cat "$TMP/export.md"; fail "export is missing: $needle"; }
done
grep -qi '^content-disposition: attachment' "$TMP/export.headers" || fail "export is not an attachment"
EXPORT_NAME="$(tr -d '\r' <"$TMP/export.headers" | awk -F': ' 'tolower($1)=="content-disposition"{print $2}')"
say "export: ${EXPORT_BYTES} bytes, ${EXPORT_LINES} lines, ${EXPORT_NAME}"

SEARCH_JSON="$(curl -sS -b "$COOKIE" "$BASE/api/search?q=${TOKEN}&limit=10")"
SEARCH_HITS="$(jq '.results | length' <<<"$SEARCH_JSON")"
[[ "$SEARCH_HITS" -ge 1 ]] || { cat "$SEARCH_JSON"; fail "search found no result for $TOKEN"; }
SEARCH_ID="$(jq -r '.results[0].messageId' <<<"$SEARCH_JSON")"
SEARCH_ROOM="$(jq -r '.results[0].roomId' <<<"$SEARCH_JSON")"
SEARCH_SNIPPET="$(jq -r '.results[0].snippet' <<<"$SEARCH_JSON")"
[[ "$SEARCH_ROOM" == "$ROOM" ]] || fail "search returned room $SEARCH_ROOM, want $ROOM"
psql_iris "SELECT 1 FROM messages WHERE id='$SEARCH_ID'" | grep -qx 1 || fail "search returned an unknown message id"
MISS_JSON="$(curl -sS -b "$COOKIE" "$BASE/api/search?q=zzzunseentermzzz")"
MISS_HITS="$(jq '.results | length' <<<"$MISS_JSON")"
[[ "$MISS_HITS" == "0" ]] || fail "unseen term returned $MISS_HITS results"
say "search: ${SEARCH_HITS} hit(s) for ${TOKEN}, snippet=$(tr -d '\n' <<<"$SEARCH_SNIPPET" | head -c 140)"
say "search: unseen term returned ${MISS_HITS}"

# ---------------------------------------------------------------------------
# 4. Backup, verify, restore into iris_restore_check, compare row counts.
# ---------------------------------------------------------------------------
BEFORE="$(psql_iris "SELECT (SELECT count(*) FROM rooms), (SELECT count(*) FROM messages), (SELECT count(*) FROM files), (SELECT count(*) FROM continuations)")"
IRIS_DATABASE_URL="$DSN" IRIS_DATA_DIR="$DATA_DIR" IRIS_BACKUP_DIR="$TMP/backups" \
  "$ROOT/scripts/backup.sh" >"$TMP/backup.log" 2>&1 || { cat "$TMP/backup.log"; fail "backup failed"; }
ARCHIVE="$(ls -1t "$TMP/backups"/iris-*.tar.gz | head -1)"
[[ -f "$ARCHIVE" ]] || fail "no archive was written"
tar -tzf "$ARCHIVE" | grep -qx db.dump || fail "archive has no db.dump"
tar -tzf "$ARCHIVE" | grep -qx manifest.txt || fail "archive has no manifest.txt"
tar -tzf "$ARCHIVE" | grep -q "^rooms/${ROOM}/inbox/" || fail "archive has no room folder for $ROOM"
ARCHIVE_BYTES="$(stat -c%s "$ARCHIVE")"
say "backup: $(basename "$ARCHIVE"), ${ARCHIVE_BYTES} bytes, source rows ${BEFORE}"

IRIS_DATABASE_URL="$DSN" IRIS_BACKUP_DIR="$TMP/backups" IRIS_ENV_FILE="$TMP/no-env" \
  "$ROOT/scripts/restore.sh" "$ARCHIVE" iris_restore_check --force --data-dir "$TMP/restored" \
  >"$TMP/restore.log" 2>&1 || { cat "$TMP/restore.log"; fail "restore failed"; }
AFTER="$(psql_other iris_restore_check "SELECT (SELECT count(*) FROM rooms), (SELECT count(*) FROM messages), (SELECT count(*) FROM files), (SELECT count(*) FROM continuations)")"
[[ "$BEFORE" == "$AFTER" ]] || fail "row counts differ: source [$BEFORE] restored [$AFTER]"
compgen -G "$TMP/restored/rooms/${ROOM}/inbox/*" >/dev/null || fail "restored data dir has no inbox file"
say "restore: iris_restore_check rows ${AFTER} — identical to source"
psql_admin "DROP DATABASE iris_restore_check WITH (FORCE);" >/dev/null
say "dropped iris_restore_check"

say "running the Go restore parity test (it creates and drops iris_restore_check again)"
IRIS_BACKUP_TEST_DSN="$DSN" go test ./internal/backup -run TestBackupRestoreParity -v >"$TMP/parity.log" 2>&1 \
  || { cat "$TMP/parity.log"; fail "Go backup/restore parity test failed"; }
grep -E 'row-count parity|dropped iris_restore_check' "$TMP/parity.log" | sed 's/^/    /'

echo
echo "P5 measured summary"
echo "  SPA shell:              ${SPA_BYTES} bytes at /"
echo "  hashed asset cache:     ${ASSET_CACHE}"
echo "  /api/nope:              ${NOAUTH_CODE} anonymous, ${AUTH_CODE} authenticated, JSON both"
echo "  Hermes turn:            $((SEND_END - SEND_START)) s, ${MESSAGE_COUNT} messages"
echo "  export:                 ${EXPORT_BYTES} bytes, ${EXPORT_LINES} lines"
echo "  search:                 ${SEARCH_HITS} hit(s), ${MISS_HITS} for an unseen term"
echo "  backup:                 ${ARCHIVE_BYTES} bytes"
echo "  restore parity:         source [${BEFORE}] = restored [${AFTER}]"
echo
echo "Archives and screenshots are in $TMP (removed on exit); run scripts/backup.sh with a real"
echo "IRIS_BACKUP_DIR to keep one."
echo "PASS: P5 hardening against the real stack"
