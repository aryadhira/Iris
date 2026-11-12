#!/usr/bin/env bash
# P7 acceptance — reminders, Web Push and off-site backup against the REAL
# Hermes gateway.
#
# Throwaway everything: database iris_p7_mine, irisd on :3308, temp data dir,
# temp VAPID keypair, a local fake push endpoint on :9338. The live iris/lab
# databases, irisd on :3300 and the gateway on :8642 are only used through the
# public API. Every Hermes session created here is deleted on exit.
#
# Scenarios:
#   a. a typed reminder fires within 35 s of its due time and posts the system
#      message.
#   b. a reminder whose time passes while irisd is stopped fires once on
#      restart with (terlambat), and a second restart does not duplicate it.
#   c. a fake push endpoint receives a real aes128gcm POST when a run finishes
#      with no SSE client attached, and receives none while a client is attached.
#   d. backup to a local rclone-style directory verifies the copy; a broken
#      target exits non-zero and rebuilds the local manifest as offsite=false.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
DB_NAME="iris_p7_mine"
API_PORT=3308
PUSH_PORT=9338
BASE="http://127.0.0.1:${API_PORT}"
DSN="postgres://lab:labdev@127.0.0.1:5432/${DB_NAME}?sslmode=disable"
PG_CONTAINER="ailab-postgres"
KEY_FILE="${IRIS_HERMES_KEY_FILE:-$HOME/.config/iris-api-key}"
HERMES_URL="${IRIS_HERMES_URL:-http://127.0.0.1:8642}"
TMP="$(mktemp -d /tmp/iris-p7-XXXXXX)"
DATA_DIR="$TMP/data"
PASS_FILE="$TMP/passphrase"
COOKIE="$TMP/cookie"
VAPID_FILE="$TMP/vapid.json"
API_PID=""
PUSH_PID=""

ts() { date +%H:%M:%S.%3N; }
ms() { date +%s%3N; }
say() { echo "[$(date -Iseconds)] $*"; }
fail() { echo "FAIL: $*" >&2; exit 1; }

psql_admin() { docker exec "$PG_CONTAINER" psql -U lab -d postgres -v ON_ERROR_STOP=1 -At -c "$1"; }
psql_iris() { docker exec "$PG_CONTAINER" psql -U lab -d "$DB_NAME" -v ON_ERROR_STOP=1 -At -F'|' -c "$1"; }
port_busy() { ss -ltn 2>/dev/null | grep -q ":$1 "; }

stop_api() {
  if [[ -n "$API_PID" ]] && kill -0 "$API_PID" 2>/dev/null; then
    kill "$API_PID" 2>/dev/null || true
    wait "$API_PID" 2>/dev/null || true
  fi
  API_PID=""
}

on_exit() {
  local code=$?
  set +e
  if [[ $code -ne 0 && -f "$TMP/api.log" ]]; then
    echo; echo "--- irisd log (tail) ---"; tail -c 14000 "$TMP/api.log"
  fi
  stop_api
  if [[ -n "$PUSH_PID" ]] && kill -0 "$PUSH_PID" 2>/dev/null; then
    kill "$PUSH_PID" 2>/dev/null
    wait "$PUSH_PID" 2>/dev/null
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
command -v jq >/dev/null || fail "jq is required"
command -v openssl >/dev/null || fail "openssl is required"
[[ -x "$HOME/go/bin/migrate" ]] || fail "golang-migrate not found at ~/go/bin/migrate"
port_busy "$API_PORT" && fail "port ${API_PORT} is busy; refusing to borrow it"
port_busy "$PUSH_PORT" && fail "port ${PUSH_PORT} is busy; refusing to borrow it"

say "P7 acceptance — database ${DB_NAME} (throwaway), irisd ${BASE}, hermes ${HERMES_URL}"

psql_admin "DROP DATABASE IF EXISTS ${DB_NAME} WITH (FORCE);" >/dev/null
psql_admin "CREATE DATABASE ${DB_NAME};" >/dev/null
"$HOME/go/bin/migrate" -path "$ROOT/db/migrations" -database "$DSN" up >/dev/null
mkdir -p "$DATA_DIR"
openssl rand -hex 16 >"$PASS_FILE"; chmod 600 "$PASS_FILE"
(cd "$ROOT" && go build -o "$ROOT/bin/irisd" ./cmd/irisd) || fail "go build failed"

start_api() {
  IRIS_ADDR="127.0.0.1:${API_PORT}" \
  IRIS_DATABASE_URL="$DSN" \
  IRIS_HERMES_URL="$HERMES_URL" \
  IRIS_HERMES_KEY_FILE="$KEY_FILE" \
  IRIS_PASSPHRASE="$(cat "$PASS_FILE")" \
  IRIS_COOKIE_SECURE=false \
  IRIS_DATA_DIR="$DATA_DIR" \
  IRIS_VAPID_KEY_FILE="$VAPID_FILE" \
  "$ROOT/bin/irisd" >>"$TMP/api.log" 2>&1 &
  API_PID=$!
  for _ in $(seq 1 100); do
    curl -fsS "$BASE/api/health" >/dev/null 2>&1 && break
    sleep 0.2
  done
  [[ "$(curl -s -m 3 -o /dev/null -w '%{http_code}' "$BASE/api/health")" == "200" ]] || fail "irisd did not become healthy"
}

start_api
curl -sS -c "$COOKIE" -X POST "$BASE/api/auth/login" -H 'Content-Type: application/json' \
  -d "{\"passphrase\":\"$(cat "$PASS_FILE")\"}" >/dev/null || fail "login failed"
say "irisd healthy; schema version $(psql_iris 'SELECT version FROM schema_migrations;' | tr -d '[:space:]')"

create_room() {
  curl -sS -b "$COOKIE" -X POST "$BASE/api/rooms" -H 'Content-Type: application/json' \
    -d "$(jq -nc --arg n "$1" '{name:$n,topic:"P7 reminders and push"}')" | jq -r '.room.id'
}

send_bg() {
  local room="$1" text="$2" out="$3"
  curl -sS -b "$COOKIE" -N --max-time 420 -X POST "$BASE/api/rooms/${room}/messages" \
    -H 'Content-Type: application/json' \
    -d "$(jq -nc --arg t "$text" '{text:$t}')" >"$out" 2>"${out}.err" &
  echo $!
}

sse_data() {
  local file="$1" event="$2"
  awk -v want="$event" '$0 == "event: " want { getline; sub(/^data: /, ""); print; exit }' "$file"
}

wait_for() {
  local file="$1" pattern="$2" seconds="$3" pid="${4:-}"
  local i
  for i in $(seq 1 $((seconds * 4))); do
    grep -q "$pattern" "$file" 2>/dev/null && return 0
    if [[ -n "$pid" ]] && ! kill -0 "$pid" 2>/dev/null; then
      grep -q "$pattern" "$file" 2>/dev/null && return 0
      return 1
    fi
    sleep 0.25
  done
  return 1
}

wait_for_done() {
  local file="$1" seconds="$2" pid="${3:-}"
  wait_for "$file" '^event: done' "$seconds" "$pid" || return 1
}

wait_for_push() {
  local room="$1" seconds="$2"
  local i
  for i in $(seq 1 $((seconds * 4))); do
    jq -e --arg path "/push/${room}" 'select(.path == $path)' "$TMP/push.jsonl" >/dev/null 2>&1 && return 0
    sleep 0.25
  done
  return 1
}

# ---------------------------------------------------------------------------
# a. typed reminder -> fires within 35 s of due.
# ---------------------------------------------------------------------------
say "=== scenario a: typed reminder ==="
A_START=$(ms)
ROOM_A="$(create_room 'P7 pengingat ketik')"
[[ -n "$ROOM_A" && "$ROOM_A" != "null" ]] || fail "room a was not created"
PID_A="$(send_bg "$ROOM_A" 'ingatkan saya dalam 1 menit untuk cek p7' "$TMP/a.sse")"
wait_for "$TMP/a.sse" '^event: reminder' 30 "$PID_A" || fail "scene a: no reminder event from the typed message"
PAYLOAD_A="$(sse_data "$TMP/a.sse" 'reminder')"
[[ -n "$PAYLOAD_A" ]] || fail "scene a: reminder event had no data"
RID_A="$(jq -r '.id' <<<"$PAYLOAD_A")"
DUE_A="$(jq -r '.due_at' <<<"$PAYLOAD_A")"
DUE_MS="$(date -d "$DUE_A" +%s%3N)"
[[ "$(jq -r '.text' <<<"$PAYLOAD_A")" == "cek p7" ]] || fail "scene a: parsed text was not 'cek p7'"
say "scene a: reminder ${RID_A} created for ${DUE_A}; waiting for the 30 s sweep"

DEADLINE=$((DUE_MS + 35000))
while (( $(ms) < DEADLINE )); do
  [[ "$(psql_iris "SELECT count(*) FROM messages WHERE room_id='$ROOM_A' AND role='system' AND body LIKE '%Pengingat:%cek p7%';")" != "0" ]] && break
  sleep 1
done
A_MSG="$(psql_iris "SELECT body FROM messages WHERE room_id='$ROOM_A' AND role='system' AND body LIKE '%Pengingat:%cek p7%' LIMIT 1;")"
[[ -n "$A_MSG" ]] || fail "scene a: reminder never posted a system message within 35 s of due"
A_AT="$(psql_iris "SELECT (extract(epoch FROM created_at) * 1000)::bigint FROM messages WHERE room_id='$ROOM_A' AND role='system' AND body LIKE '%Pengingat:%cek p7%' LIMIT 1;")"
A_FIRED_AT=$(ms)
A_LATE=$((A_AT - DUE_MS))
(( A_LATE >= 0 && A_LATE <= 35000 )) || fail "scene a: fired ${A_LATE} ms after due (want 0..35000)"
[[ "$(psql_iris "SELECT state FROM reminders WHERE id='$RID_A';")" == "fired" ]] || fail "scene a: reminder state is not fired"
[[ "$(psql_iris "SELECT count(*) FROM messages WHERE room_id='$ROOM_A' AND role='system' AND body LIKE '%Pengingat:%cek p7%';")" == "1" ]] \
  || fail "scene a: reminder posted more than one message"
say "scene a PASSED: ${A_MSG} — fired +${A_LATE} ms after due"

kill "$PID_A" 2>/dev/null || true
wait "$PID_A" 2>/dev/null || true

# ---------------------------------------------------------------------------
# b. due while irisd is down -> one late reminder on boot, never duplicated.
# ---------------------------------------------------------------------------
say "=== scenario b: missed while down ==="
B_START=$(ms)
ROOM_B="$(create_room 'P7 pengingat terlewat')"
[[ -n "$ROOM_B" && "$ROOM_B" != "null" ]] || fail "room b was not created"
DUE_B="$(date -u -d '+8 seconds' +%Y-%m-%dT%H:%M:%SZ)"
DUE_B_MS="$(date -d "$DUE_B" +%s%3N)"
RID_B="$(curl -sS -b "$COOKIE" -X POST "$BASE/api/rooms/${ROOM_B}/reminders" \
  -H 'Content-Type: application/json' \
  -d "$(jq -nc --arg t 'cek p7 terlambat' --arg d "$DUE_B" '{text:$t,due_at:$d}')" | jq -r '.reminder.id')"
[[ -n "$RID_B" && "$RID_B" != "null" ]] || fail "scene b: reminder was not created"
stop_api
say "scene b: irisd stopped; due at ${DUE_B}"
sleep $(( (DUE_B_MS - $(ms)) / 1000 + 4 ))
start_api
B_RESTART=$(ms)
for _ in $(seq 1 40); do
  [[ "$(psql_iris "SELECT count(*) FROM messages WHERE room_id='$ROOM_B' AND role='system' AND body LIKE '%terlambat%';")" != "0" ]] && break
  sleep 0.5
done
B_MSG="$(psql_iris "SELECT body FROM messages WHERE room_id='$ROOM_B' AND role='system' AND body LIKE '%terlambat%' LIMIT 1;")"
[[ -n "$B_MSG" ]] || fail "scene b: no late reminder after restart"
[[ "$B_MSG" == *"(terlambat"* ]] || fail "scene b: late reminder does not say (terlambat): $B_MSG"
[[ "$(psql_iris "SELECT count(*) FROM messages WHERE room_id='$ROOM_B' AND role='system';")" == "1" ]] \
  || fail "scene b: the late reminder was posted more than once"
# Restart again to prove the fired row is not replayed.
stop_api
start_api
sleep 3
[[ "$(psql_iris "SELECT count(*) FROM messages WHERE room_id='$ROOM_B' AND role='system';")" == "1" ]] \
  || fail "scene b: a second restart duplicated the late reminder"
say "scene b PASSED: ${B_MSG} — fired $((B_RESTART - DUE_B_MS)) ms after due, one message after two restarts"

# ---------------------------------------------------------------------------
# c. push with no SSE attached, and none while a client is attached.
# ---------------------------------------------------------------------------
say "=== scenario c: Web Push ==="
C_START=$(ms)
: >"$TMP/push.jsonl"
python3 "$ROOT/scripts/fake-push-server.py" --port "$PUSH_PORT" --out "$TMP/push.jsonl" \
  >"$TMP/push-server.log" 2>&1 &
PUSH_PID=$!
for _ in $(seq 1 40); do
  curl -fsS "http://127.0.0.1:${PUSH_PORT}/" >/dev/null 2>&1 && break
  sleep 0.1
done

openssl ecparam -name prime256v1 -genkey -noout -out "$TMP/ec.pem" 2>/dev/null
openssl ec -in "$TMP/ec.pem" -pubout -outform DER -out "$TMP/pub.der" 2>/dev/null
P256DH="$(tail -c 65 "$TMP/pub.der" | base64 -w0 | tr '+/' '-_' | tr -d '=')"
AUTH="$(openssl rand 16 | base64 -w0 | tr '+/' '-_' | tr -d '=')"

subscribe() {
  local room="$1"
  curl -sS -b "$COOKIE" -X POST "$BASE/api/push/subscribe" -H 'Content-Type: application/json' \
    -d "$(jq -nc --arg r "$room" --arg e "http://127.0.0.1:${PUSH_PORT}/push/${room}" --arg p "$P256DH" --arg a "$AUTH" \
      '{room_id:$r,endpoint:$e,keys:{p256dh:$p,auth:$a}}')" >/dev/null || fail "push subscribe failed for $room"
}

ROOM_C1="$(create_room 'P7 push tanpa klien')"
[[ -n "$ROOM_C1" && "$ROOM_C1" != "null" ]] || fail "room c1 was not created"
subscribe "$ROOM_C1"
PID_C1="$(send_bg "$ROOM_C1" 'Jawab satu kata: halo' "$TMP/c1.sse")"
for _ in $(seq 1 50); do
  [[ "$(psql_iris "SELECT count(*) FROM messages WHERE room_id='$ROOM_C1' AND role='user';")" != "0" ]] && break
  sleep 0.1
done
sleep 1
kill "$PID_C1" 2>/dev/null || true
wait "$PID_C1" 2>/dev/null || true
say "scene c: client detached; waiting for the detached turn to finish"
for _ in $(seq 1 120); do
  [[ "$(psql_iris "SELECT count(*) FROM messages WHERE room_id='$ROOM_C1' AND role='assistant';")" != "0" ]] && break
  sleep 0.5
done
[[ "$(psql_iris "SELECT count(*) FROM messages WHERE room_id='$ROOM_C1' AND role='assistant';")" != "0" ]] \
  || fail "scene c: the detached turn never finished"
wait_for_push "$ROOM_C1" 30 || fail "scene c: no push arrived for a run with no SSE client attached"
PUSH_RECORD="$(jq -c --arg path "/push/${ROOM_C1}" 'select(.path == $path)' "$TMP/push.jsonl" | head -1)"
[[ "$(jq -r '.headers["Content-Encoding"]' <<<"$PUSH_RECORD")" == "aes128gcm" ]] \
  || fail "scene c: push Content-Encoding is not aes128gcm"
[[ "$(jq -r '.headers.Authorization' <<<"$PUSH_RECORD")" == vapid\ t=* ]] \
  || fail "scene c: push has no VAPID Authorization header"
[[ "$(jq -r '.bytes' <<<"$PUSH_RECORD")" -gt 102 ]] || fail "scene c: push body is too short to be encrypted"
say "scene c: detached run pushed (${PUSH_RECORD})"

ROOM_C2="$(create_room 'P7 push dengan klien')"
[[ -n "$ROOM_C2" && "$ROOM_C2" != "null" ]] || fail "room c2 was not created"
subscribe "$ROOM_C2"
PID_C2="$(send_bg "$ROOM_C2" 'Jawab satu kata: halo' "$TMP/c2.sse")"
wait_for_done "$TMP/c2.sse" 120 "$PID_C2" || fail "scene c: attached turn never finished"
sleep 3
if jq -e --arg path "/push/${ROOM_C2}" 'select(.path == $path)' "$TMP/push.jsonl" >/dev/null 2>&1; then
  fail "scene c: a push was sent while an SSE client was attached"
fi
say "scene c PASSED: detached run pushed once; attached run sent no push"

# ---------------------------------------------------------------------------
# d. off-site backup: verified copy, broken target fails non-zero.
# ---------------------------------------------------------------------------
say "=== scenario d: off-site backup ==="
D_START=$(ms)
BACKUP_A="$TMP/backups-verified"
OFFSITE_A="$TMP/offsite-verified"
IRIS_BACKUP_DIR="$BACKUP_A" IRIS_DATABASE_URL="$DSN" IRIS_DATA_DIR="$DATA_DIR" \
IRIS_ENV_FILE="$TMP/no-env" IRIS_BACKUP_TARGET="rclone:local:${OFFSITE_A}" \
  bash "$ROOT/scripts/backup.sh" >"$TMP/backup-a.log" 2>&1 \
  || { tail -40 "$TMP/backup-a.log"; fail "scene d: verified-target backup failed"; }
ARCHIVE_A="$(ls -1t "$BACKUP_A"/iris-*.tar.gz | head -1)"
[[ -f "$OFFSITE_A/$(basename "$ARCHIVE_A")" ]] || fail "scene d: off-site directory does not contain the archive"
[[ "$(sha256sum "$ARCHIVE_A" | awk '{print $1}')" == "$(sha256sum "$OFFSITE_A/$(basename "$ARCHIVE_A")" | awk '{print $1}')" ]] \
  || fail "scene d: off-site archive checksum differs"
tar -xOzf "$ARCHIVE_A" manifest.txt | grep -qx 'offsite=true' || fail "scene d: manifest does not record offsite=true"
grep -q 'off-site: rclone:local:' "$TMP/backup-a.log" || fail "scene d: backup did not report the verified target"

BACKUP_B="$TMP/backups-broken"
touch "$TMP/not-a-directory"
set +e
IRIS_BACKUP_DIR="$BACKUP_B" IRIS_DATABASE_URL="$DSN" IRIS_DATA_DIR="$DATA_DIR" \
IRIS_ENV_FILE="$TMP/no-env" IRIS_BACKUP_TARGET="rclone:local:$TMP/not-a-directory/child" \
  bash "$ROOT/scripts/backup.sh" >"$TMP/backup-b.log" 2>&1
BROKEN_CODE=$?
set -e
(( BROKEN_CODE != 0 )) || fail "scene d: a broken off-site target exited 0"
grep -q 'FAIL: off-site:' "$TMP/backup-b.log" || fail "scene d: broken target did not say what failed"
ARCHIVE_B="$(ls -1t "$BACKUP_B"/iris-*.tar.gz 2>/dev/null | head -1)"
[[ -n "$ARCHIVE_B" ]] || fail "scene d: broken target left no local archive"
tar -xOzf "$ARCHIVE_B" manifest.txt | grep -qx 'offsite=false' \
  || fail "scene d: failed copy left a manifest claiming offsite=true"

BACKUP_C="$TMP/backups-local-only"
env -u IRIS_BACKUP_TARGET IRIS_BACKUP_DIR="$BACKUP_C" IRIS_DATABASE_URL="$DSN" IRIS_DATA_DIR="$DATA_DIR" \
IRIS_ENV_FILE="$TMP/no-env" bash "$ROOT/scripts/backup.sh" >"$TMP/backup-c.log" 2>&1 \
  || { tail -30 "$TMP/backup-c.log"; fail "scene d: local-only backup failed"; }
grep -q 'off-site: not configured (local only)' "$TMP/backup-c.log" || fail "scene d: local-only wording is missing"
ARCHIVE_C="$(ls -1t "$BACKUP_C"/iris-*.tar.gz | head -1)"
tar -xOzf "$ARCHIVE_C" manifest.txt | grep -qx 'offsite=false' || fail "scene d: local-only manifest is not offsite=false"
say "scene d PASSED: verified copy, broken target exit ${BROKEN_CODE}, local-only manifest offsite=false"

# ---------------------------------------------------------------------------
# bootcheck harness: prove the script writes its receipt against this irisd.
# A real reboot is Boss' step.
# ---------------------------------------------------------------------------
BOOT_DIR="$TMP/bootcheck"
IRIS_BOOTCHECK_DIR="$BOOT_DIR" \
IRIS_BOOTCHECK_URL="$BASE/api/health" \
IRIS_BOOTCHECK_VOICE_URL="$BASE/api/health" \
IRIS_BOOTCHECK_TIMEOUT=10 \
  bash "$ROOT/scripts/bootcheck.sh" >"$TMP/bootcheck.log" 2>&1 || { cat "$TMP/bootcheck.log"; fail "bootcheck script failed"; }
[[ "$(jq -r '.health' "$BOOT_DIR/last-boot.json")" == "true" ]] || fail "bootcheck: health is not true"
[[ "$(jq -r '.schema' "$BOOT_DIR/last-boot.json")" == "9" ]] || fail "bootcheck: schema is not 9"
[[ -s "$BOOT_DIR/history.log" ]] || fail "bootcheck: history.log is empty"
say "bootcheck PASSED: $(jq -c . "$BOOT_DIR/last-boot.json")"

A_DONE=$(ms)
echo
say "ALL P7 SCENARIOS PASSED"
echo "timings: a $((A_DONE - A_START))ms total; b restart->late $((B_RESTART - DUE_B_MS))ms; c $((A_DONE - C_START))ms; d $((A_DONE - D_START))ms"
