#!/usr/bin/env bash
# P3 acceptance: real irisd, real Hermes, throwaway Postgres and data dir.
#
# 1. Ask for a 90 s background job, end the turn, type NOTHING, and prove the
#    continuation pump fired and reported the real result.
# 2. Repeat, but send a user message while the continuation is pending, and
#    prove the chain was cancelled (no follow-up assistant turn).
#
# Never touches port 3300, the live lab/iris databases, the live gateway, or
# an existing irisd.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
DB_NAME="iris_p3_accept"
API_PORT="${IRIS_ACCEPT_API_PORT:-3301}"
BASE="http://127.0.0.1:${API_PORT}"
DSN="postgres://lab:labdev@127.0.0.1:5432/${DB_NAME}?sslmode=disable"
PG_CONTAINER="ailab-postgres"
KEY_FILE="${IRIS_HERMES_KEY_FILE:-$HOME/.config/iris-api-key}"
HERMES_URL="${IRIS_HERMES_URL:-http://127.0.0.1:8642}"
TMP="$(mktemp -d "${TMPDIR:-/tmp}/iris-p3-XXXXXX")"
DATA_DIR="$TMP/data"
PASS_FILE="$TMP/passphrase"
COOKIE="$TMP/cookie"
API_PID=""

ts() { date +%H:%M:%S.%3N; }
say() { echo "[$(date -Iseconds)] $*"; }
fail() { echo "FAIL: $*" >&2; exit 1; }

psql_admin() { docker exec "$PG_CONTAINER" psql -U lab -d postgres -v ON_ERROR_STOP=1 -At -c "$1"; }
psql_iris() { docker exec "$PG_CONTAINER" psql -U lab -d "$DB_NAME" -v ON_ERROR_STOP=1 -At -c "$1"; }

port_busy() { ss -ltn 2>/dev/null | grep -q ":$1 "; }

on_exit() {
  local code=$?
  set +e
  if [[ $code -ne 0 && -f "$TMP/api.log" ]]; then
    echo; echo "--- irisd log (tail) ---"; tail -c 8000 "$TMP/api.log"
  fi
  if [[ -n "$API_PID" ]] && kill -0 "$API_PID" 2>/dev/null; then
    kill "$API_PID" 2>/dev/null
    wait "$API_PID" 2>/dev/null
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
[[ -x "$HOME/go/bin/migrate" ]] || fail "golang-migrate not found at ~/go/bin/migrate"
port_busy "$API_PORT" && fail "port ${API_PORT} is busy; refusing to borrow it"

say "P3 acceptance — database ${DB_NAME} (throwaway), irisd ${BASE}, hermes ${HERMES_URL}"

psql_admin "DROP DATABASE IF EXISTS ${DB_NAME} WITH (FORCE);" >/dev/null
psql_admin "CREATE DATABASE ${DB_NAME};" >/dev/null
"$HOME/go/bin/migrate" -path "$ROOT/db/migrations" -database "$DSN" up >/dev/null
mkdir -p "$DATA_DIR"

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
curl -sS -c "$COOKIE" -X POST "$BASE/api/auth/login" -H 'Content-Type: application/json' \
  -d "{\"passphrase\":\"$(cat "$PASS_FILE")\"}" >/dev/null || fail "login failed"
say "irisd healthy; schema version $(psql_iris 'SELECT version FROM schema_migrations;' | tr -d '[:space:]')"

create_room() {
  curl -sS -b "$COOKIE" -X POST "$BASE/api/rooms" -H 'Content-Type: application/json' \
    -d "{\"name\":\"$1\",\"topic\":\"P3 continuity\"}" | jq -r '.room.id'
}

send_turn() {
  local room="$1" text="$2" out="$3"
  curl -sS -b "$COOKIE" -N -X POST "$BASE/api/rooms/${room}/messages" \
    -H 'Content-Type: application/json' -d "$(jq -nc --arg t "$text" '{text:$t}')" >"$out"
}

timeline() {
  psql_iris "SELECT to_char(created_at, 'HH24:MI:SS.MS'), role, replace(left(body, 220), E'\\n', ' / ')
             FROM messages WHERE room_id = '$1' ORDER BY created_at, id;"
}

# ---------------------------------------------------------------------------
# 1. The wait: type nothing, the room must report by itself.
# ---------------------------------------------------------------------------
say "=== case 1: 90 s job, nobody types ==="
ROOM1="$(create_room 'P3 wait')"
[[ -n "$ROOM1" && "$ROOM1" != "null" ]] || fail "room 1 was not created"
say "room 1: ${ROOM1}"

TURN1_START=$(ts)
say "T+0 turn 1 starts (background sleep 90, explicitly detached)"
send_turn "$ROOM1" \
  "Jalankan pekerjaan latar berikut dengan tool terminal(background=true, notify=true): sleep 90; echo IRIS-DONE. JANGAN menunggu hasilnya di turn ini — akhiri turn sekarang. Laporkan id prosesnya, lalu akhiri dengan baris persis: STATUS: RUNNING — menunggu proses 90 detik." \
  "$TMP/room1-turn.sse"
TURN1_END=$(ts)
say "turn 1 ended at ${TURN1_END}; nobody will type from here"
grep -E '^event: done' "$TMP/room1-turn.sse" >/dev/null || fail "turn 1 did not complete"

# Wait for the continuation to appear, then for the real result.
CONT1_SEEN=""
RESULT1=""
for i in $(seq 1 90); do
  CONT1=$(psql_iris "SELECT count(*) FROM messages WHERE room_id='$ROOM1' AND role='system' AND body LIKE 'lanjut otomatis%';")
  if [[ "$CONT1" != "0" && -z "$CONT1_SEEN" ]]; then
    CONT1_SEEN=$(ts)
    say "continuation system line appeared at ${CONT1_SEEN}"
  fi
  # The first reply names the command ("echo IRIS-DONE"), so only a later
  # assistant message that ends STATUS: DONE counts as the reported result.
  if psql_iris "SELECT count(*) FROM messages a
                WHERE a.room_id='$ROOM1' AND a.role='assistant'
                  AND a.body LIKE '%IRIS-DONE%' AND a.body LIKE '%STATUS: DONE%'
                  AND a.created_at > (SELECT min(created_at) FROM messages
                                      WHERE room_id='$ROOM1' AND role='system'
                                        AND body LIKE 'lanjut otomatis (1/3)%');" | grep -qv '^0$'; then
    RESULT1=$(ts)
    break
  fi
  sleep 2
done
[[ -n "$CONT1_SEEN" ]] || fail "no continuation system line appeared"
[[ -n "$RESULT1" ]] || fail "the continuation never reported IRIS-DONE"
say "result reported at ${RESULT1}"

echo
echo "room 1 timeline:"
timeline "$ROOM1"
echo
echo "room 1 continuation rows:"
psql_iris "SELECT attempt, status, reason, to_char(due_at, 'HH24:MI:SS.MS'), to_char(finished_at, 'HH24:MI:SS.MS') FROM continuations WHERE room_id='$ROOM1' ORDER BY attempt;"
echo
echo "room 1 meta:"
psql_iris "SELECT role, meta::text FROM messages WHERE room_id='$ROOM1' ORDER BY created_at;"
echo

psql_iris "SELECT count(*) FROM messages WHERE room_id='$ROOM1' AND role='system' AND body LIKE 'lanjut otomatis (1/3)%';" | grep -qv '^0$' \
  || fail "the 1/3 system line is missing"
psql_iris "SELECT count(*) FROM messages WHERE room_id='$ROOM1' AND role='assistant' AND body LIKE '%IRIS-DONE%' AND body LIKE '%STATUS: DONE%';" | grep -qv '^0$' \
  || fail "the continuation did not report the real result and STATUS: DONE"
say "case 1 PASSED (turn ended ${TURN1_END}, continuation ${CONT1_SEEN}, result ${RESULT1})"

# ---------------------------------------------------------------------------
# 2. The cancellation: Boss sends a message while the chain is pending.
# ---------------------------------------------------------------------------
say "=== case 2: 90 s job, Boss types mid-wait ==="
ROOM2="$(create_room 'P3 cancel')"
[[ -n "$ROOM2" && "$ROOM2" != "null" ]] || fail "room 2 was not created"
say "room 2: ${ROOM2}"

TURN2_START=$(ts)
say "T+0 turn 1 starts (background sleep 90, detached)"
send_turn "$ROOM2" \
  "Jalankan pekerjaan latar berikut dengan tool terminal(background=true, notify=true): sleep 90; echo IRIS-DONE. JANGAN menunggu hasilnya di turn ini — akhiri turn sekarang. Laporkan id prosesnya, lalu akhiri dengan baris persis: STATUS: RUNNING — menunggu proses 90 detik." \
  "$TMP/room2-turn.sse"
TURN2_END=$(ts)
say "turn 1 ended at ${TURN2_END}"

PENDING_AT=""
for i in $(seq 1 40); do
  if psql_iris "SELECT count(*) FROM continuations WHERE room_id='$ROOM2' AND status='pending';" | grep -qv '^0$'; then
    PENDING_AT=$(ts)
    break
  fi
  sleep 0.5
done
[[ -n "$PENDING_AT" ]] || fail "no pending continuation was scheduled"
say "continuation pending at ${PENDING_AT}; Boss now types"

CANCEL_SEND=$(ts)
send_turn "$ROOM2" \
  "Abaikan proses latar tadi, jangan ditunggu. Jawab singkat saja: 2+2 berapa? Akhiri dengan STATUS: DONE — dijawab." \
  "$TMP/room2-user.sse"
CANCEL_DONE=$(ts)
say "Boss message ended at ${CANCEL_DONE}"

CANCEL_SEEN=""
for i in $(seq 1 20); do
  if psql_iris "SELECT count(*) FROM messages WHERE room_id='$ROOM2' AND role='system' AND body LIKE '%ada pesan baru dari Boss%';" | grep -qv '^0$'; then
    CANCEL_SEEN=$(ts)
    break
  fi
  sleep 0.5
done
[[ -n "$CANCEL_SEEN" ]] || fail "the cancellation line is missing"
say "cancellation line at ${CANCEL_SEEN}"

# Wait past the 20 s due time and prove no follow-up turn ran.
sleep 30
AFTER=$(ts)
if psql_iris "SELECT count(*) FROM messages WHERE room_id='$ROOM2' AND role='system' AND body LIKE 'lanjut otomatis (1/3)%';" | grep -qv '^0$'; then
  fail "a continuation ran after the cancellation"
fi
psql_iris "SELECT status, reason FROM continuations WHERE room_id='$ROOM2' ORDER BY attempt;" | grep -q 'cancelled|ada pesan baru dari Boss' \
  || fail "the continuation row was not cancelled"
if psql_iris "SELECT count(*) FROM messages a
              WHERE a.room_id='$ROOM2' AND a.role='assistant'
                AND a.body LIKE '%IRIS-DONE%' AND a.body LIKE '%STATUS: DONE%'
                AND a.created_at > (SELECT min(created_at) FROM messages
                                    WHERE room_id='$ROOM2' AND role='system'
                                      AND body LIKE 'lanjut otomatis (1/3)%');" | grep -qv '^0$'; then
  fail "a cancelled chain still reported IRIS-DONE"
fi

echo
echo "room 2 timeline:"
timeline "$ROOM2"
echo
echo "room 2 continuation rows:"
psql_iris "SELECT attempt, status, reason, to_char(due_at, 'HH24:MI:SS.MS'), to_char(finished_at, 'HH24:MI:SS.MS') FROM continuations WHERE room_id='$ROOM2' ORDER BY attempt;"
echo
say "case 2 PASSED (pending ${PENDING_AT}, typed ${CANCEL_SEND}..${CANCEL_DONE}, cancelled ${CANCEL_SEEN}, checked ${AFTER})"

echo
echo "PASS: P3 continuity against the real stack"
