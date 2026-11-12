#!/usr/bin/env bash
# P6 acceptance — approvals and stop against the REAL Hermes gateway.
#
# Throwaway everything: database iris_p6_mine, irisd on :3307, a temp data dir.
# The live iris/lab databases, the live irisd on :3300, and the gateway on :8642
# are only used through the public API (sessions/runs/approvals), exactly as
# irisd does. Every Hermes session created here is deleted on exit.
#
# Scenarios:
#   a. a protected-file write asks for approval, "once" is sent, the write
#      lands and the turn completes.
#   b. the same card is denied: the file is never written, the turn ends
#      BLOCKED, and no automatic continuation runs afterwards.
#   c. a 120 s background job held open by process_manage wait is stopped:
#      the run ends cancelled, "dihentikan oleh Boss" is in the transcript,
#      and no continuation note appears afterwards.
#   d. nobody answers: at the gateway's own approvals.timeout the card is
#      expired, the room has zero pending approvals, and Hermes fails closed.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
DB_NAME="iris_p6_mine"
API_PORT=3307
BASE="http://127.0.0.1:${API_PORT}"
DSN="postgres://lab:labdev@127.0.0.1:5432/${DB_NAME}?sslmode=disable"
PG_CONTAINER="ailab-postgres"
KEY_FILE="${IRIS_HERMES_KEY_FILE:-$HOME/.config/iris-api-key}"
HERMES_URL="${IRIS_HERMES_URL:-http://127.0.0.1:8642}"
TMP="$(mktemp -d /tmp/iris-p6-XXXXXX)"
DATA_DIR="$TMP/data"
PASS_FILE="$TMP/passphrase"
COOKIE="$TMP/cookie"
API_PID=""

ts() { date +%H:%M:%S.%3N; }
ms() { date +%s%3N; }
say() { echo "[$(date -Iseconds)] $*"; }
fail() { echo "FAIL: $*" >&2; exit 1; }

psql_admin() { docker exec "$PG_CONTAINER" psql -U lab -d postgres -v ON_ERROR_STOP=1 -At -c "$1"; }
psql_iris() { docker exec "$PG_CONTAINER" psql -U lab -d "$DB_NAME" -v ON_ERROR_STOP=1 -At -F'|' -c "$1"; }
port_busy() { ss -ltn 2>/dev/null | grep -q ":$1 "; }

on_exit() {
  local code=$?
  set +e
  if [[ $code -ne 0 && -f "$TMP/api.log" ]]; then
    echo; echo "--- irisd log (tail) ---"; tail -c 12000 "$TMP/api.log"
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
command -v jq >/dev/null || fail "jq is required"
[[ -x "$HOME/go/bin/migrate" ]] || fail "golang-migrate not found at ~/go/bin/migrate"
port_busy "$API_PORT" && fail "port ${API_PORT} is busy; refusing to borrow it"

# Iris' deadline must match the gateway's own approval timeout. Read it, never
# reconfigure the gateway.
HERMES_APPROVAL_TIMEOUT="$(hermes config get approvals.timeout 2>/dev/null || true)"
[[ "$HERMES_APPROVAL_TIMEOUT" =~ ^[0-9]+$ ]] || HERMES_APPROVAL_TIMEOUT=300
say "P6 acceptance — database ${DB_NAME} (throwaway), irisd ${BASE}, hermes ${HERMES_URL}, approvals.timeout ${HERMES_APPROVAL_TIMEOUT}s"

psql_admin "DROP DATABASE IF EXISTS ${DB_NAME} WITH (FORCE);" >/dev/null
psql_admin "CREATE DATABASE ${DB_NAME};" >/dev/null
"$HOME/go/bin/migrate" -path "$ROOT/db/migrations" -database "$DSN" up >/dev/null
mkdir -p "$DATA_DIR"
openssl rand -hex 16 >"$PASS_FILE"; chmod 600 "$PASS_FILE"
(cd "$ROOT" && go build -o "$ROOT/bin/irisd" ./cmd/irisd) || fail "go build failed"

IRIS_ADDR="127.0.0.1:${API_PORT}" \
IRIS_DATABASE_URL="$DSN" \
IRIS_HERMES_URL="$HERMES_URL" \
IRIS_HERMES_KEY_FILE="$KEY_FILE" \
IRIS_PASSPHRASE="$(cat "$PASS_FILE")" \
IRIS_COOKIE_SECURE=false \
IRIS_DATA_DIR="$DATA_DIR" \
IRIS_APPROVAL_TIMEOUT="$HERMES_APPROVAL_TIMEOUT" \
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
    -d "$(jq -nc --arg n "$1" '{name:$n,topic:"P6 approvals and stop"}')" | jq -r '.room.id'
}

send_bg() {
  local room="$1" text="$2" out="$3"
  curl -sS -b "$COOKIE" -N --max-time 420 -X POST "$BASE/api/rooms/${room}/messages" \
    -H 'Content-Type: application/json' \
    -d "$(jq -nc --arg t "$text" '{text:$t}')" >"$out" 2>"${out}.err" &
  echo $!
}

first_approval() {
  grep -A1 '^event: approval' "$1" | sed -n 's/^data: //p' | head -1
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

approval_state() { psql_iris "SELECT state FROM approvals WHERE id='$1';"; }

request_approval_turn() {
  local room="$1" file="$2" filename="$3"
  local out="$DATA_DIR/rooms/${room}/outbox"
  mkdir -p "$out"
  local prompt="Pakai tool write_file untuk menulis tepat ke ${out}/${filename} dengan isi: p6 approval ${filename}. Jangan pakai terminal. Setelah selesai jawab satu baris: P6-${filename}-SELESAI."
  send_bg "$room" "$prompt" "$file"
}

# ---------------------------------------------------------------------------
# a. approval appears, "once" is sent, the turn continues to completion.
# ---------------------------------------------------------------------------
say "=== scenario a: approval -> Izinkan sekali ==="
ROOM_A="$(create_room 'P6 izin sekali')"
[[ -n "$ROOM_A" && "$ROOM_A" != "null" ]] || fail "room a was not created"
A_START=$(ms)
PID_A="$(request_approval_turn "$ROOM_A" "$TMP/a.sse" 'AGENTS.md')"
wait_for "$TMP/a.sse" '^event: approval' 120 "$PID_A" || fail "scene a: no approval appeared"
A_APPROVAL_AT=$(ms)
PAYLOAD_A="$(first_approval "$TMP/a.sse")"
[[ -n "$PAYLOAD_A" ]] || fail "scene a: approval frame had no data"
AID_A="$(jq -r '.id' <<<"$PAYLOAD_A")"
A_RUN_A="$(psql_iris "SELECT run_id FROM approvals WHERE id='$AID_A';")"
A_COMMAND="$(jq -r '.command' <<<"$PAYLOAD_A")"
A_TOOL="$(jq -r '.tool_name' <<<"$PAYLOAD_A")"
A_CHOICES="$(jq -c '.choices' <<<"$PAYLOAD_A")"
[[ "$A_TOOL" == "write_file" ]] || fail "scene a: tool_name=${A_TOOL}, want write_file"
[[ "$A_COMMAND" == "<write to AGENTS.md>" ]] || fail "scene a: command=${A_COMMAND}"
[[ "$A_CHOICES" == '["once","deny"]' ]] || fail "scene a: choices=${A_CHOICES}, want once/deny only"
say "scene a: approval ${AID_A} after $((A_APPROVAL_AT - A_START)) ms; tool=${A_TOOL}; choices=${A_CHOICES}"

RESOLVE_A=$(ms)
curl -sS -b "$COOKIE" -X POST "$BASE/api/rooms/${ROOM_A}/approvals/${AID_A}" \
  -H 'Content-Type: application/json' -d '{"choice":"once"}' >"$TMP/a-resolve.json" \
  || fail "scene a: resolve request failed"
[[ "$(jq -r '.approval.state' "$TMP/a-resolve.json")" == "allowed" ]] || { cat "$TMP/a-resolve.json"; fail "scene a: state was not allowed"; }
[[ "$(jq -r '.approval.choice' "$TMP/a-resolve.json")" == "once" ]] || fail "scene a: choice was not once"
wait_for_done "$TMP/a.sse" 180 "$PID_A" || fail "scene a: turn never completed"
A_DONE=$(ms)
[[ "$(approval_state "$AID_A")" == "allowed" ]] || fail "scene a: stored approval is not allowed"
[[ -f "$DATA_DIR/rooms/${ROOM_A}/outbox/AGENTS.md" ]] || fail "scene a: approved file was not written"
grep -q 'p6 approval AGENTS.md' "$DATA_DIR/rooms/${ROOM_A}/outbox/AGENTS.md" || fail "scene a: file content is wrong"
psql_iris "SELECT count(*) FROM messages WHERE room_id='$ROOM_A' AND role='assistant' AND body LIKE '%STATUS: DONE%';" | grep -qv '^0$' \
  || fail "scene a: assistant did not finish with STATUS: DONE"
RUN_A_STATUS="$(psql_iris "SELECT status FROM runs WHERE run_id='$A_RUN_A';")"
[[ "$RUN_A_STATUS" == "completed" ]] || fail "scene a: run status=${RUN_A_STATUS}, want completed"
say "scene a PASSED: approval +$((A_APPROVAL_AT - A_START)) ms, resolve->done $((A_DONE - RESOLVE_A)) ms, run ${RUN_A_STATUS}"

# ---------------------------------------------------------------------------
# b. denied: the file never appears, the turn ends BLOCKED, nothing is retried.
# ---------------------------------------------------------------------------
say "=== scenario b: approval -> Tolak ==="
ROOM_B="$(create_room 'P6 izin ditolak')"
[[ -n "$ROOM_B" && "$ROOM_B" != "null" ]] || fail "room b was not created"
B_START=$(ms)
PID_B="$(request_approval_turn "$ROOM_B" "$TMP/b.sse" 'CLAUDE.md')"
wait_for "$TMP/b.sse" '^event: approval' 120 "$PID_B" || fail "scene b: no approval appeared"
B_APPROVAL_AT=$(ms)
PAYLOAD_B="$(first_approval "$TMP/b.sse")"
AID_B="$(jq -r '.id' <<<"$PAYLOAD_B")"
B_RUN="$(psql_iris "SELECT run_id FROM approvals WHERE id='$AID_B';")"
DENY_AT=$(ms)
curl -sS -b "$COOKIE" -X POST "$BASE/api/rooms/${ROOM_B}/approvals/${AID_B}" \
  -H 'Content-Type: application/json' -d '{"choice":"deny"}' >"$TMP/b-resolve.json" \
  || fail "scene b: deny request failed"
[[ "$(jq -r '.approval.state' "$TMP/b-resolve.json")" == "denied" ]] || fail "scene b: state was not denied"
wait_for_done "$TMP/b.sse" 180 "$PID_B" || fail "scene b: turn never ended"
B_DONE=$(ms)
[[ ! -f "$DATA_DIR/rooms/${ROOM_B}/outbox/CLAUDE.md" ]] || fail "scene b: a denied file was written"
psql_iris "SELECT count(*) FROM messages WHERE room_id='$ROOM_B' AND role='assistant' AND body LIKE '%STATUS: BLOCKED%';" | grep -qv '^0$' \
  || fail "scene b: the assistant did not report BLOCKED"
[[ "$(psql_iris "SELECT count(*) FROM approvals WHERE run_id='$B_RUN';")" == "1" ]] || fail "scene b: approval was repeated"
say "scene b: denial accepted; waiting 25 s for the continuation window"
sleep 25
[[ "$(psql_iris "SELECT count(*) FROM continuations WHERE room_id='$ROOM_B';")" == "0" ]] || fail "scene b: a denied approval armed a continuation"
psql_iris "SELECT count(*) FROM messages WHERE room_id='$ROOM_B' AND role='system' AND body LIKE 'lanjut otomatis%';" | grep -qx '0' \
  || fail "scene b: a continuation note appeared after denial"
say "scene b PASSED: approval +$((B_APPROVAL_AT - B_START)) ms, deny->done $((B_DONE - DENY_AT)) ms, no retry, run=$(psql_iris "SELECT status FROM runs WHERE run_id='$B_RUN';")"

# ---------------------------------------------------------------------------
# c. a 120 s background job is stopped; the chain must be dead.
# ---------------------------------------------------------------------------
say "=== scenario c: 120 s job -> Stop ==="
ROOM_C="$(create_room 'P6 hentikan')"
[[ -n "$ROOM_C" && "$ROOM_C" != "null" ]] || fail "room c was not created"
C_PROMPT='Jalankan perintah ini dengan tool terminal(background=true, notify=true): sleep 120; echo P6-STOP-DONE. Lalu tunggu proses itu dengan process_manage wait sampai selesai. Jangan akhiri turn sebelum prosesnya selesai.'
PID_C="$(send_bg "$ROOM_C" "$C_PROMPT" "$TMP/c.sse")"
C_START=$(ms)
wait_for "$TMP/c.sse" 'process_manage' 120 "$PID_C" || fail "scene c: the agent never reached process_manage wait"
C_RUNNING_AT=$(ms)
sleep 3
STOP_AT=$(ms)
STOP_JSON="$(curl -sS -b "$COOKIE" -X POST "$BASE/api/rooms/${ROOM_C}/stop")"
[[ "$(jq -r '.stopped' <<<"$STOP_JSON")" == "1" ]] || { cat <<<"$STOP_JSON"; fail "scene c: stop did not stop one run"; }
wait_for_done "$TMP/c.sse" 60 "$PID_C" || fail "scene c: the run never ended after stop"
STOP_DONE=$(ms)
STOP_AGAIN="$(curl -sS -b "$COOKIE" -X POST "$BASE/api/rooms/${ROOM_C}/stop")"
[[ "$(jq -r '.stopped' <<<"$STOP_AGAIN")" == "0" ]] || fail "scene c: second stop was not idempotent"
[[ "$(jq -r '.reason' <<<"$STOP_AGAIN")" != "" ]] || fail "scene c: second stop gave no reason"
C_STATUS="$(psql_iris "SELECT status FROM runs WHERE room_id='$ROOM_C' ORDER BY started_at DESC LIMIT 1;")"
[[ "$C_STATUS" == "cancelled" ]] || fail "scene c: run status=${C_STATUS}, want cancelled"
psql_iris "SELECT count(*) FROM messages WHERE room_id='$ROOM_C' AND role='system' AND body='dihentikan oleh Boss';" | grep -qx '1' \
  || fail "scene c: the stop note is missing or duplicated"
[[ "$(psql_iris "SELECT count(*) FROM continuations WHERE room_id='$ROOM_C';")" == "0" ]] || fail "scene c: the stopped run armed a continuation"
say "scene c: stopping; waiting 25 s to prove the chain stays dead"
sleep 25
psql_iris "SELECT count(*) FROM messages WHERE room_id='$ROOM_C' AND role='system' AND body LIKE 'lanjut otomatis%';" | grep -qx '0' \
  || fail "scene c: a continuation note appeared after stop"
psql_iris "SELECT count(*) FROM messages WHERE room_id='$ROOM_C' AND role='assistant' AND body LIKE '%P6-STOP-DONE%';" | grep -qx '0' \
  || fail "scene c: the stopped job reported a result after the stop"
say "scene c PASSED: run reached process_manage +$((C_RUNNING_AT - C_START)) ms, stop->done $((STOP_DONE - STOP_AT)) ms, status ${C_STATUS}, no continuation"

# ---------------------------------------------------------------------------
# d. nobody answers: Iris expires its card at Hermes' own timeout, and the
#    room stops waiting.
# ---------------------------------------------------------------------------
say "=== scenario d: nobody answers -> expired, not answered ==="
ROOM_D="$(create_room 'P6 kedaluwarsa')"
[[ -n "$ROOM_D" && "$ROOM_D" != "null" ]] || fail "room d was not created"
D_START=$(ms)
PID_D="$(request_approval_turn "$ROOM_D" "$TMP/d.sse" 'AGENTS.md')"
wait_for "$TMP/d.sse" '^event: approval' 120 "$PID_D" || fail "scene d: no approval appeared"
D_APPROVAL_AT=$(ms)
AID_D="$(first_approval "$TMP/d.sse" | jq -r '.id')"
D_RUN="$(psql_iris "SELECT run_id FROM approvals WHERE id='$AID_D';")"
D_DEADLINE=$((D_APPROVAL_AT + HERMES_APPROVAL_TIMEOUT * 1000 + 10000))
NOW=$(ms)
if (( NOW < D_DEADLINE )); then
  say "scene d: waiting $(( (D_DEADLINE - NOW) / 1000 )) s for the real ${HERMES_APPROVAL_TIMEOUT}s deadline"
  sleep $(( (D_DEADLINE - NOW) / 1000 + 1 ))
fi
APPROVALS_D="$(curl -sS -b "$COOKIE" "$BASE/api/rooms/${ROOM_D}/approvals")"
STATE_D="$(jq -r --arg id "$AID_D" '.approvals[] | select(.id==$id) | .state' <<<"$APPROVALS_D")"
[[ "$STATE_D" == "expired" ]] || { cat <<<"$APPROVALS_D"; fail "scene d: state=${STATE_D}, want expired"; }
[[ "$(jq -r --arg id "$AID_D" '.approvals[] | select(.id==$id) | .choice // ""' <<<"$APPROVALS_D")" == "" ]] \
  || fail "scene d: Iris invented an answer for an expired approval"
ROOMS_D="$(curl -sS -b "$COOKIE" "$BASE/api/rooms")"
PENDING_D="$(jq -r --arg id "$ROOM_D" '.rooms[] | select(.id==$id) | .pending_approvals' <<<"$ROOMS_D")"
[[ "$PENDING_D" == "0" ]] || { cat <<<"$ROOMS_D"; fail "scene d: room still reports ${PENDING_D} pending approvals"; }
wait_for_done "$TMP/d.sse" 120 "$PID_D" || fail "scene d: the timed-out turn never ended"
D_DONE=$(ms)
psql_iris "SELECT count(*) FROM messages WHERE room_id='$ROOM_D' AND role='assistant' AND body LIKE '%STATUS: BLOCKED%';" | grep -qv '^0$' \
  || fail "scene d: Hermes did not fail closed after the timeout"
[[ "$(approval_state "$AID_D")" == "expired" ]] || fail "scene d: the stored row is not expired"
[[ "$(psql_iris "SELECT count(*) FROM continuations WHERE room_id='$ROOM_D';")" == "0" ]] || fail "scene d: an expired approval armed a continuation"
say "scene d PASSED: expired after $((D_DONE - D_APPROVAL_AT)) ms, pending_approvals=0, run=$(psql_iris "SELECT status FROM runs WHERE run_id='$D_RUN';")"

echo
say "ALL P6 SCENARIOS PASSED"
echo "timings: a approval $((A_APPROVAL_AT - A_START))ms resolve->done $((A_DONE - RESOLVE_A))ms; b deny->done $((B_DONE - DENY_AT))ms; c stop->done $((STOP_DONE - STOP_AT))ms; d expiry $((D_DONE - D_APPROVAL_AT))ms"
