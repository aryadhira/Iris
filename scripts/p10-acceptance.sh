#!/usr/bin/env bash
# P10 acceptance — a room you can leave and come back to, against the REAL
# Hermes gateway.
#
# Throwaway: database iris_p10_mine, irisd on :3312, temp data dir. The live
# iris/lab databases, irisd on :3300, the gateway on :8642 and every service
# port are never touched. Every Hermes session created here is deleted on exit.
#
# Scenarios:
#   a. start a 60 s+ turn, disconnect the POST at 5 s, reattach /events at 15 s:
#      the snapshot names the run and the partial text, and done arrives with no
#      user action.
#   b. two simultaneous /events subscribers receive the same deltas and one done.
#   c. kill irisd mid-turn and restart: the row reads interrupted, the room does
#      not look idle, and a terputus note is visible.
#   d. the room list carries run.status=running while it runs and clears it when
#      the turn finishes.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
DB_NAME="iris_p10_mine"
API_PORT="${IRIS_ACCEPT_API_PORT:-3312}"
BASE="http://127.0.0.1:${API_PORT}"
DSN="postgres://lab:labdev@127.0.0.1:5432/${DB_NAME}?sslmode=disable"
PG_CONTAINER="ailab-postgres"
KEY_FILE="${IRIS_HERMES_KEY_FILE:-$HOME/.config/iris-api-key}"
HERMES_URL="${IRIS_HERMES_URL:-http://127.0.0.1:8642}"
TMP="$(mktemp -d /tmp/iris-p10-XXXXXX)"
DATA_DIR="$TMP/data"
PASS_FILE="$TMP/passphrase"
COOKIE="$TMP/cookie"
API_PID=""
RESULTS="$TMP/results"

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
    echo; echo "--- irisd log (tail) ---"; tail -c 20000 "$TMP/api.log"
  fi
  stop_api
  if [[ -f "$KEY_FILE" ]]; then
    for sid in $(psql_iris "SELECT hermes_session_id FROM rooms" 2>/dev/null); do
      curl -sS -o /dev/null -X DELETE "$HERMES_URL/api/sessions/${sid}" \
        -H "Authorization: Bearer $(cat "$KEY_FILE")" 2>/dev/null
    done
  fi
  psql_admin "DROP DATABASE IF EXISTS ${DB_NAME} WITH (FORCE);" >/dev/null 2>&1
  if [[ "${P10_KEEP:-0}" == "1" ]]; then
    echo "kept: $TMP"
  else
    rm -rf "$TMP"
  fi
  exit "$code"
}
trap on_exit EXIT

[[ -f "$KEY_FILE" ]] || fail "Hermes key file not found at $KEY_FILE"
command -v docker >/dev/null || fail "docker is required"
command -v jq >/dev/null || fail "jq is required"
command -v python3 >/dev/null || fail "python3 is required"
[[ -x "$HOME/go/bin/migrate" ]] || fail "golang-migrate not found at ~/go/bin/migrate"
port_busy "$API_PORT" && fail "port ${API_PORT} is busy; refusing to borrow it"

say "P10 acceptance — database ${DB_NAME} (throwaway), irisd ${BASE}, hermes ${HERMES_URL}"

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
  IRIS_VAPID_KEY_FILE="$TMP/vapid.json" \
  "$ROOT/bin/irisd" >>"$TMP/api.log" 2>&1 &
  API_PID=$!
  for _ in $(seq 1 100); do
    curl -fsS "$BASE/api/health" >/dev/null 2>&1 && break
    sleep 0.2
  done
  [[ "$(curl -s -m 3 -o /dev/null -w '%{http_code}' "$BASE/api/health")" == "200" ]] || fail "irisd did not become healthy"
  curl -sS -c "$COOKIE" -X POST "$BASE/api/auth/login" -H 'Content-Type: application/json' \
    -d "{\"passphrase\":\"$(cat "$PASS_FILE")\"}" >/dev/null || fail "login failed"
}

start_api
say "irisd healthy; schema version $(psql_iris 'SELECT version FROM schema_migrations;' | tr -d '[:space:]')"

create_room() {
  curl -sS -b "$COOKIE" -X POST "$BASE/api/rooms" -H 'Content-Type: application/json' \
    -d "$(jq -nc --arg n "$1" '{name:$n,topic:"P10 live rooms"}')" | jq -r '.room.id'
}

send_bg() {
  local room="$1" text="$2" out="$3"
  curl -sS -b "$COOKIE" -N --max-time 420 -X POST "$BASE/api/rooms/${room}/messages" \
    -H 'Content-Type: application/json' \
    -d "$(jq -nc --arg t "$text" '{text:$t}')" >"$out" 2>"${out}.err" &
  echo $!
}

subscribe_bg() {
  local room="$1" out="$2"
  curl -sS -b "$COOKIE" -N --max-time 420 "$BASE/api/rooms/${room}/events" >"$out" 2>"${out}.err" &
  echo $!
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

wait_for_done() { wait_for "$1" '^event: done' "$2" "${3:-}"; }

# first_event_json prints the JSON data of the first frame with the given name.
first_event_json() {
  python3 - "$1" "$2" <<'PY'
import json, sys
path, want = sys.argv[1], sys.argv[2]
event, data = '', []
with open(path, encoding='utf-8', errors='replace') as fh:
    for line in fh:
        line = line.rstrip('\n')
        if line.startswith('event:'):
            event = line.split(':', 1)[1].strip()
        elif line.startswith('data:'):
            data.append(line.split(':', 1)[1].lstrip())
        elif line == '':
            if event == want:
                print('\n'.join(data))
                raise SystemExit(0)
            event, data = '', []
raise SystemExit(1)
PY
}

# delta_text concatenates every delta frame in the order a subscriber saw it.
delta_text() {
  python3 - "$1" <<'PY'
import json, sys
event, data, out = '', [], []
with open(sys.argv[1], encoding='utf-8', errors='replace') as fh:
    for line in fh:
        line = line.rstrip('\n')
        if line.startswith('event:'):
            event = line.split(':', 1)[1].strip()
        elif line.startswith('data:'):
            data.append(line.split(':', 1)[1].lstrip())
        elif line == '':
            if event == 'delta':
                out.append(json.loads('\n'.join(data)).get('text', ''))
            event, data = '', []
print(''.join(out), end='')
PY
}

count_events() {
  grep -c "^event: $1$" "$2" 2>/dev/null || true
}

LONG_PROMPT="${IRIS_P10_LONG_PROMPT:-Jawaban pertamamu HARUS berisi kalimat persis: \"Saya sedang mengerjakan tugas yang butuh waktu.\" Setelah kalimat itu, panggil tool terminal dengan perintah tepat 'sleep 75' secara foreground — JANGAN background dan JANGAN process_manage. Tunggu sampai perintah itu selesai di turn ini. Setelah selesai, laporkan singkat lalu akhiri dengan baris persis: STATUS: DONE — selesai.}" 

# ---------------------------------------------------------------------------
say "=== scenario a: leave at 5 s, return at 15 s ==="
ROOM_A="$(create_room 'P10 kembali')"
[[ -n "$ROOM_A" && "$ROOM_A" != "null" ]] || fail "room a was not created"
A_START=$(ms)
PID_A="$(send_bg "$ROOM_A" "$LONG_PROMPT" "$TMP/a-post.sse")"
sleep 5
kill "$PID_A" 2>/dev/null || true
wait "$PID_A" 2>/dev/null || true
say "scene a: POST client hung up at $(( $(ms) - A_START )) ms"
# Return once the turn is clearly mid-flight, not during its first second.
while (( $(ms) - A_START < 15000 )); do sleep 0.5; done
EV_A="$(subscribe_bg "$ROOM_A" "$TMP/a-events.sse")"
wait_for "$TMP/a-events.sse" '^event: snapshot' 15 "$EV_A" || fail "scene a: no snapshot after reattach"
SNAP_A="$(first_event_json "$TMP/a-events.sse" snapshot)" || fail "scene a: cannot parse the snapshot"
RUN_A="$(jq -r '.run.status // "null"' <<<"$SNAP_A")"
RUN_ID_A="$(jq -r '.run.id // ""' <<<"$SNAP_A")"
PARTIAL_A="$(jq -r '.partial // ""' <<<"$SNAP_A")"
PARTIAL_LEN_A="$(python3 -c 'import sys; print(len(sys.argv[1]))' "$PARTIAL_A")"
[[ "$RUN_A" == "running" ]] || fail "scene a: snapshot run.status = ${RUN_A}, want running (${SNAP_A})"
[[ -n "$RUN_ID_A" ]] || fail "scene a: snapshot has no run id"
(( PARTIAL_LEN_A > 0 )) || fail "scene a: snapshot partial text is empty (${SNAP_A})"
STARTED_A="$(jq -r '.run.started_at // ""' <<<"$SNAP_A")"
say "scene a: snapshot run=${RUN_ID_A} status=${RUN_A} started=${STARTED_A} partial=${PARTIAL_LEN_A} chars"
wait_for_done "$TMP/a-events.sse" 180 "$EV_A" || fail "scene a: done never arrived without a new POST"
A_DONE=$(ms)
kill "$EV_A" 2>/dev/null || true
wait "$EV_A" 2>/dev/null || true
(( A_DONE - A_START >= 60000 )) || fail "scene a: the turn lasted only $(( A_DONE - A_START )) ms, want 60 s+"
A_STATE="$(psql_iris "SELECT state, length(body) FROM messages WHERE room_id='$ROOM_A' AND role='assistant' ORDER BY created_at DESC LIMIT 1;")"
IFS='|' read -r A_ROW_STATE A_ROW_LEN <<<"$A_STATE"
[[ "$A_ROW_STATE" == "done" ]] || fail "scene a: final row state = ${A_ROW_STATE}, want done"
(( A_ROW_LEN > 0 )) || fail "scene a: final row is empty"
say "scene a PASSED: done after $(( A_DONE - A_START )) ms; final row ${A_ROW_STATE} (${A_ROW_LEN} chars)"
echo "a|snapshot_run=${RUN_A}|partial_chars=${PARTIAL_LEN_A}|done_ms=$(( A_DONE - A_START ))|final_state=${A_ROW_STATE}" >>"$RESULTS"

# ---------------------------------------------------------------------------
say "=== scenario b: two subscribers, same deltas, one done ==="
ROOM_B="$(create_room 'P10 dua layar')"
[[ -n "$ROOM_B" && "$ROOM_B" != "null" ]] || fail "room b was not created"
EV_B1="$(subscribe_bg "$ROOM_B" "$TMP/b1.sse")"
EV_B2="$(subscribe_bg "$ROOM_B" "$TMP/b2.sse")"
wait_for "$TMP/b1.sse" '^event: snapshot' 15 "$EV_B1" || fail "scene b: subscriber 1 got no snapshot"
wait_for "$TMP/b2.sse" '^event: snapshot' 15 "$EV_B2" || fail "scene b: subscriber 2 got no snapshot"
PID_B="$(send_bg "$ROOM_B" 'Tulis satu kalimat pembuka, lalu jalankan terminal `sleep 20` dan tunggu. Akhiri dengan STATUS: DONE — selesai.' "$TMP/b-post.sse")"
wait_for_done "$TMP/b1.sse" 180 "$EV_B1" || fail "scene b: subscriber 1 never saw done"
wait_for_done "$TMP/b2.sse" 180 "$EV_B2" || fail "scene b: subscriber 2 never saw done"
kill "$EV_B1" "$EV_B2" "$PID_B" 2>/dev/null || true
wait "$EV_B1" 2>/dev/null || true
wait "$EV_B2" 2>/dev/null || true
wait "$PID_B" 2>/dev/null || true
DELTA_1="$(delta_text "$TMP/b1.sse")"
DELTA_2="$(delta_text "$TMP/b2.sse")"
[[ -n "$DELTA_1" ]] || fail "scene b: subscriber 1 saw no deltas"
[[ "$DELTA_1" == "$DELTA_2" ]] || fail "scene b: subscribers saw different deltas"
DONE_1="$(count_events done "$TMP/b1.sse")"
DONE_2="$(count_events done "$TMP/b2.sse")"
[[ "$DONE_1" == "1" && "$DONE_2" == "1" ]] || fail "scene b: done count = ${DONE_1}/${DONE_2}, want 1/1"
DELTA_LEN_B="$(python3 -c 'import sys; print(len(sys.argv[1]))' "$DELTA_1")"
say "scene b PASSED: both subscribers saw ${DELTA_LEN_B} delta chars and one done each"
echo "b|delta_chars=${DELTA_LEN_B}|done=${DONE_1}/${DONE_2}|identical=yes" >>"$RESULTS"

# ---------------------------------------------------------------------------
say "=== scenario c: kill irisd mid-turn, restart, terputus ==="
ROOM_C="$(create_room 'P10 restart')"
[[ -n "$ROOM_C" && "$ROOM_C" != "null" ]] || fail "room c was not created"
PID_C="$(send_bg "$ROOM_C" "$LONG_PROMPT" "$TMP/c-post.sse")"
STREAMING_C=""
for _ in $(seq 1 120); do
  STREAMING_C="$(psql_iris "SELECT id, length(body) FROM messages WHERE room_id='$ROOM_C' AND role='assistant' AND state='streaming' AND length(body) > 0 LIMIT 1;")"
  [[ -n "$STREAMING_C" ]] && break
  sleep 0.25
done
[[ -n "$STREAMING_C" ]] || fail "scene c: no persisted streaming row appeared"
IFS='|' read -r STREAM_ID_C STREAM_LEN_C <<<"$STREAMING_C"
say "scene c: streaming row ${STREAM_ID_C:0:8} has ${STREAM_LEN_C} persisted chars; killing irisd"
kill -9 "$API_PID" 2>/dev/null || true
wait "$API_PID" 2>/dev/null || true
API_PID=""
kill "$PID_C" 2>/dev/null || true
wait "$PID_C" 2>/dev/null || true
start_api
STATE_C="$(psql_iris "SELECT state, length(body) FROM messages WHERE id='$STREAM_ID_C';")"
IFS='|' read -r ROW_STATE_C ROW_LEN_C <<<"$STATE_C"
[[ "$ROW_STATE_C" == "interrupted" ]] || fail "scene c: row state after restart = ${ROW_STATE_C}, want interrupted"
(( ROW_LEN_C > 0 )) || fail "scene c: interrupted row lost its partial text"
NOTE_C="$(psql_iris "SELECT count(*) FROM messages WHERE room_id='$ROOM_C' AND role='system' AND body LIKE '%terputus%';")"
[[ "$NOTE_C" -ge 1 ]] || fail "scene c: no visible terputus note"
ROOM_C_JSON="$(curl -sS -b "$COOKIE" "$BASE/api/rooms/${ROOM_C}")"
RUN_C="$(jq -r '.room.run.status // "null"' <<<"$ROOM_C_JSON")"
[[ "$RUN_C" == "interrupted" ]] || fail "scene c: room run.status = ${RUN_C}, want interrupted (${ROOM_C_JSON})"
MSG_C="$(curl -sS -b "$COOKIE" "$BASE/api/rooms/${ROOM_C}/messages" | jq -r '.messages[] | select(.state=="interrupted") | .state' | head -1)"
[[ "$MSG_C" == "interrupted" ]] || fail "scene c: the messages API does not expose the interrupted row"
say "scene c PASSED: row interrupted with ${ROW_LEN_C} chars, note visible, room run=interrupted"
echo "c|row_state=${ROW_STATE_C}|row_chars=${ROW_LEN_C}|note=${NOTE_C}|room_run=${RUN_C}" >>"$RESULTS"

# ---------------------------------------------------------------------------
say "=== scenario d: room list run while running, gone when finished ==="
ROOM_D="$(create_room 'P10 daftar room')"
[[ -n "$ROOM_D" && "$ROOM_D" != "null" ]] || fail "room d was not created"
PID_D="$(send_bg "$ROOM_D" 'Tulis satu kalimat pembuka, lalu jalankan terminal `sleep 35` dan tunggu. Akhiri dengan STATUS: DONE — selesai.' "$TMP/d-post.sse")"
RUNNING_D=""
for _ in $(seq 1 120); do
  RUNNING_D="$(curl -sS -b "$COOKIE" "$BASE/api/rooms" | jq -r --arg id "$ROOM_D" '.rooms[] | select(.id==$id) | .run.status // "null"')"
  [[ "$RUNNING_D" == "running" ]] && break
  sleep 0.25
done
[[ "$RUNNING_D" == "running" ]] || fail "scene d: room list never showed run.status=running (last ${RUNNING_D})"
say "scene d: room list shows run.status=running"
wait_for_done "$TMP/d-post.sse" 180 "$PID_D" || fail "scene d: the turn never finished"
wait "$PID_D" 2>/dev/null || true
CLEARED_D=""
for _ in $(seq 1 80); do
  CLEARED_D="$(curl -sS -b "$COOKIE" "$BASE/api/rooms" | jq -r --arg id "$ROOM_D" '.rooms[] | select(.id==$id) | if .run == null then "null" else .run.status end')"
  [[ "$CLEARED_D" == "null" ]] && break
  sleep 0.25
done
[[ "$CLEARED_D" == "null" ]] || fail "scene d: room list still carries run=${CLEARED_D} after the turn"
say "scene d PASSED: running while live, cleared after done"
echo "d|during=${RUNNING_D}|after=${CLEARED_D}" >>"$RESULTS"

# ---------------------------------------------------------------------------
echo
say "RESULTS"
cat "$RESULTS"
echo
say "PASS: P10 live rooms — snapshot, fan-out, restart, room run"
