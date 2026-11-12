#!/usr/bin/env bash
# P8 acceptance — cost meter, budget guard, streaming speech and retry against
# the REAL Hermes gateway.
#
# Throwaway: database iris_p8_mine, irisd on :3309, voice sidecar on :3311, temp
# data dir and temp prices file. The live iris/lab databases, irisd on :3300,
# the live voice sidecar on :3310 and the gateway on :8642 are only reached
# through their public APIs. Every Hermes session created here is deleted on
# exit.
#
# Scenarios:
#   a. one real turn stores a usage row with real tokens and a served model;
#      the /api/usage totals equal the sum of the rows.
#   b. IRIS_DAILY_BUDGET_USD=0.0001 blocks the next message; `lanjut` writes one
#      budget_overrides row and lets exactly one turn through; the next message
#      is blocked again.
#   c. streaming speech TTFB (first audio byte) vs the buffered path, plus a
#      /transcribe round trip of the concatenated audio.
#   d. a stopped run retried with the same user message: new reply, user count
#      unchanged, old reply superseded.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
DB_NAME="iris_p8_mine"
API_PORT="${IRIS_ACCEPT_API_PORT:-3309}"
VOICE_PORT="${IRIS_ACCEPT_VOICE_PORT:-3311}"
BASE="http://127.0.0.1:${API_PORT}"
VOICE="http://127.0.0.1:${VOICE_PORT}"
DSN="postgres://lab:labdev@127.0.0.1:5432/${DB_NAME}?sslmode=disable"
PG_CONTAINER="ailab-postgres"
KEY_FILE="${IRIS_HERMES_KEY_FILE:-$HOME/.config/iris-api-key}"
HERMES_URL="${IRIS_HERMES_URL:-http://127.0.0.1:8642}"
VOICE_PYTHON="${IRIS_VOICE_PYTHON:-$HOME/Documents/projects/voice-hud/bridge/.venv/bin/python}"
TMP="$(mktemp -d /tmp/iris-p8-XXXXXX)"
DATA_DIR="$TMP/data"
PASS_FILE="$TMP/passphrase"
COOKIE="$TMP/cookie"
VAPID_FILE="$TMP/vapid.json"
PRICES_FILE="$TMP/prices.json"
API_PID=""
VOICE_PID=""
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
    echo; echo "--- irisd log (tail) ---"; tail -c 16000 "$TMP/api.log"
  fi
  if [[ $code -ne 0 && -f "$TMP/voice.log" ]]; then
    echo; echo "--- voice log (tail) ---"; tail -c 8000 "$TMP/voice.log"
  fi
  stop_api
  if [[ -n "$VOICE_PID" ]] && kill -0 "$VOICE_PID" 2>/dev/null; then
    kill "$VOICE_PID" 2>/dev/null
    wait "$VOICE_PID" 2>/dev/null
  fi
  if [[ -f "$KEY_FILE" ]]; then
    for sid in $(psql_iris "SELECT hermes_session_id FROM rooms" 2>/dev/null); do
      curl -sS -o /dev/null -X DELETE "$HERMES_URL/api/sessions/${sid}" \
        -H "Authorization: Bearer $(cat "$KEY_FILE")" 2>/dev/null
    done
  fi
  psql_admin "DROP DATABASE IF EXISTS ${DB_NAME} WITH (FORCE);" >/dev/null 2>&1
  if [[ "${P8_KEEP:-0}" == "1" ]]; then
    echo "kept: $TMP"
  else
    rm -rf "$TMP"
  fi
  exit "$code"
}
trap on_exit EXIT

[[ -f "$KEY_FILE" ]] || fail "Hermes key file not found at $KEY_FILE"
[[ -x "$VOICE_PYTHON" ]] || fail "voice Python not found at $VOICE_PYTHON"
command -v docker >/dev/null || fail "docker is required"
command -v jq >/dev/null || fail "jq is required"
command -v python3 >/dev/null || fail "python3 is required"
[[ -x "$HOME/go/bin/migrate" ]] || fail "golang-migrate not found at ~/go/bin/migrate"
port_busy "$API_PORT" && fail "port ${API_PORT} is busy; refusing to borrow it"
port_busy "$VOICE_PORT" && fail "port ${VOICE_PORT} is busy; refusing to borrow it"

WORKSPACE="$(ls -d "$HOME"/.hermes/installs/*/environments/*/workspace 2>/dev/null | head -1 || true)"
[[ -n "$WORKSPACE" && -d "$WORKSPACE" ]] || fail "Hermes workspace not found (ls -d ~/.hermes/installs/*/environments/*/workspace)"
HERMES_PYTHON="$(dirname "$WORKSPACE")/venv/bin/python"
[[ -x "$HERMES_PYTHON" ]] || fail "Hermes interpreter not found at $HERMES_PYTHON"

say "P8 acceptance — database ${DB_NAME} (throwaway), irisd ${BASE}, voice ${VOICE}, hermes ${HERMES_URL}"

psql_admin "DROP DATABASE IF EXISTS ${DB_NAME} WITH (FORCE);" >/dev/null
psql_admin "CREATE DATABASE ${DB_NAME};" >/dev/null
"$HOME/go/bin/migrate" -path "$ROOT/db/migrations" -database "$DSN" up >/dev/null
mkdir -p "$DATA_DIR"
openssl rand -hex 16 >"$PASS_FILE"; chmod 600 "$PASS_FILE"
(cd "$ROOT" && go build -o "$ROOT/bin/irisd" ./cmd/irisd) || fail "go build failed"

say "seeding prices from the Hermes table"
PYTHONPATH="$WORKSPACE" "$HERMES_PYTHON" "$ROOT/scripts/seed-prices.py" --out "$PRICES_FILE" \
  >"$TMP/seed.log" 2>&1 || { cat "$TMP/seed.log"; fail "seed-prices.py failed"; }
tail -3 "$TMP/seed.log" | sed 's/^/  /'

start_voice() {
  IRIS_VOICE_PORT="$VOICE_PORT" "$VOICE_PYTHON" "$ROOT/voice/server.py" >"$TMP/voice.log" 2>&1 &
  VOICE_PID=$!
  for _ in $(seq 1 320); do
    curl -fsS "$VOICE/health" 2>/dev/null | grep -q '"warm": *true' && return 0
    sleep 0.25
  done
  return 1
}
start_voice || fail "voice sidecar did not warm up on ${VOICE}"
say "voice healthy: $(curl -fsS "$VOICE/health")"

start_api() {
  local budget="$1"
  IRIS_ADDR="127.0.0.1:${API_PORT}" \
  IRIS_DATABASE_URL="$DSN" \
  IRIS_HERMES_URL="$HERMES_URL" \
  IRIS_HERMES_KEY_FILE="$KEY_FILE" \
  IRIS_PASSPHRASE="$(cat "$PASS_FILE")" \
  IRIS_COOKIE_SECURE=false \
  IRIS_DATA_DIR="$DATA_DIR" \
  IRIS_VOICE_URL="$VOICE" \
  IRIS_PRICES_FILE="$PRICES_FILE" \
  IRIS_DAILY_BUDGET_USD="$budget" \
  IRIS_VAPID_KEY_FILE="$VAPID_FILE" \
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

say "starting irisd with IRIS_DAILY_BUDGET_USD=0.0001"
start_api "0.0001"
say "irisd healthy; schema version $(psql_iris 'SELECT version FROM schema_migrations;' | tr -d '[:space:]')"

create_room() {
  curl -sS -b "$COOKIE" -X POST "$BASE/api/rooms" -H 'Content-Type: application/json' \
    -d "$(jq -nc --arg n "$1" '{name:$n,topic:"P8 cost, voice, retry"}')" | jq -r '.room.id'
}

send_bg() {
  local room="$1" text="$2" out="$3"
  curl -sS -b "$COOKIE" -N --max-time 420 -X POST "$BASE/api/rooms/${room}/messages" \
    -H 'Content-Type: application/json' \
    -d "$(jq -nc --arg t "$text" '{text:$t}')" >"$out" 2>"${out}.err" &
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

wait_for_done() {
  local file="$1" seconds="$2" pid="${3:-}"
  wait_for "$file" '^event: done' "$seconds" "$pid"
}

A_START=$(ms)
say "=== scenario a: one real turn is metered ==="
ROOM_A="$(create_room 'P8 biaya nyata')"
[[ -n "$ROOM_A" && "$ROOM_A" != "null" ]] || fail "room a was not created"
PID_A="$(send_bg "$ROOM_A" 'Jawab singkat dalam dua kalimat: port delapan ribu kosong dan server lama sudah dimatikan.' "$TMP/a.sse")"
wait_for_done "$TMP/a.sse" 240 "$PID_A" || fail "scene a: the real turn never finished"
A_DONE=$(ms)
say "scene a: turn finished in $((A_DONE - A_START)) ms"

ROW="$(psql_iris "SELECT run_id, provider, model, input_tokens, output_tokens, total_tokens, cache_read_tokens, cache_write_tokens, COALESCE(cost_usd::text,'NULL'), usage_reported, (message_id IS NOT NULL) FROM \"usage\" WHERE room_id='$ROOM_A' ORDER BY created_at DESC LIMIT 1;")"
[[ -n "$ROW" ]] || fail "scene a: no usage row was written"
IFS='|' read -r RUN_ID PROVIDER MODEL IN_TOK OUT_TOK TOTAL_TOK CR_TOK CW_TOK COST REPORTED LINKED <<<"$ROW"
[[ "$TOTAL_TOK" =~ ^[0-9]+$ ]] && (( TOTAL_TOK > 0 )) || fail "scene a: total_tokens is not non-zero: $ROW"
[[ -n "$PROVIDER" ]] || fail "scene a: provider is empty"
[[ -n "$MODEL" ]] || fail "scene a: model is empty"
[[ "$REPORTED" == "t" ]] || fail "scene a: usage_reported is false for a real payload"
[[ "$LINKED" == "t" ]] || fail "scene a: the usage row has no assistant message id"
say "scene a: usage run=${RUN_ID} provider=${PROVIDER} model=${MODEL} tokens=${IN_TOK}+${OUT_TOK}=${TOTAL_TOK} cost=${COST}"

USAGE_JSON="$(curl -sS -b "$COOKIE" "$BASE/api/usage?room=$ROOM_A")"
jq -e '.totals.turns == 1' <<<"$USAGE_JSON" >/dev/null || fail "scene a: usage totals turns != 1: $USAGE_JSON"
API_TURNS="$(jq -r '.totals.turns' <<<"$USAGE_JSON")"
API_TOKENS="$(jq -r '.totals.total_tokens' <<<"$USAGE_JSON")"
API_COST="$(jq -r '.totals.cost_usd' <<<"$USAGE_JSON")"
DB_TURNS="$(psql_iris "SELECT count(*) FROM \"usage\" WHERE room_id='$ROOM_A';")"
DB_SUM_TOKENS="$(psql_iris "SELECT COALESCE(sum(total_tokens),0) FROM \"usage\" WHERE room_id='$ROOM_A';")"
DB_SUM_COST="$(psql_iris "SELECT COALESCE(sum(cost_usd),0) FROM \"usage\" WHERE room_id='$ROOM_A';")"
[[ "$API_TURNS" == "$DB_TURNS" ]] || fail "scene a: API turns ${API_TURNS} != DB ${DB_TURNS}"
[[ "$API_TOKENS" == "$DB_SUM_TOKENS" ]] || fail "scene a: API tokens ${API_TOKENS} != DB sum ${DB_SUM_TOKENS}"
[[ "$API_TOKENS" == "$TOTAL_TOK" ]] || fail "scene a: API tokens ${API_TOKENS} != row ${TOTAL_TOK}"
python3 - "$API_COST" "$DB_SUM_COST" <<'PY' || fail "scene a: API cost and DB sum disagree"
import sys
api, db = float(sys.argv[1]), float(sys.argv[2])
if abs(api - db) > 1e-12:
    raise SystemExit(1)
PY
if [[ "$COST" != "NULL" ]]; then
  EXPECTED="$(python3 - "$PRICES_FILE" "$MODEL" "$IN_TOK" "$OUT_TOK" "$CR_TOK" "$CW_TOK" <<'PY'
import json, sys
prices = json.load(open(sys.argv[1]))
rate = prices.get(sys.argv[2])
if rate is None:
    print("unknown")
    raise SystemExit(0)
tokens = [int(x) for x in sys.argv[3:7]]
keys = ["input", "output", "cache_read", "cache_write"]
total = 0.0
for count, key in zip(tokens, keys):
    if count == 0:
        continue
    price = rate.get(key)
    if price is None:
        print("unknown")
        raise SystemExit(0)
    total += count * price / 1_000_000
print(f"{total:.12f}")
PY
)"
  [[ "$EXPECTED" == "unknown" ]] || python3 - "$COST" "$EXPECTED" <<'PY' || fail "scene a: stored cost is not tokens x prices.json"
import sys
if abs(float(sys.argv[1]) - float(sys.argv[2])) > 1e-12:
    raise SystemExit(1)
PY
  say "scene a: cost matches tokens x prices.json (${EXPECTED})"
else
  say "scene a: cost is NULL because ${MODEL} has no price (honest, not zero)"
fi
echo "a|turns=${API_TURNS}|tokens=${API_TOKENS}|cost=${API_COST}|model=${MODEL}" >>"$RESULTS"

# ---------------------------------------------------------------------------
say "=== scenario b: budget guard ==="
USERS_BEFORE="$(psql_iris "SELECT count(*) FROM messages WHERE room_id='$ROOM_A' AND role='user';")"
CODE="$(curl -sS -b "$COOKIE" -o "$TMP/b1.json" -w '%{http_code}' -X POST "$BASE/api/rooms/${ROOM_A}/messages" \
  -H 'Content-Type: application/json' -d '{"text":"halo lagi setelah batas"}')"
[[ "$CODE" == "429" ]] || fail "scene b: the blocked message answered ${CODE}, want 429 (body $(cat "$TMP/b1.json"))"
grep -q 'Pesan ditolak' "$TMP/b1.json" || fail "scene b: 429 copy is not the Indonesian refusal: $(cat "$TMP/b1.json")"
grep -q 'lanjut' "$TMP/b1.json" || fail "scene b: 429 copy does not name the override"
USERS_AFTER="$(psql_iris "SELECT count(*) FROM messages WHERE room_id='$ROOM_A' AND role='user';")"
[[ "$USERS_BEFORE" == "$USERS_AFTER" ]] || fail "scene b: a refused message left a user row (${USERS_BEFORE} -> ${USERS_AFTER})"
psql_iris "SELECT body FROM messages WHERE room_id='$ROOM_A' AND role='system' ORDER BY created_at DESC LIMIT 1;" \
  | grep -q 'Pesan ditolak' || fail "scene b: the refusal is not in the room transcript"
OVERRIDES="$(psql_iris "SELECT count(*) FROM budget_overrides WHERE room_id='$ROOM_A';")"
[[ "$OVERRIDES" == "0" ]] || fail "scene b: overrides exist before lanjut"
say "scene b: blocked with ${CODE}; no user row, one visible system notice"

PID_B="$(send_bg "$ROOM_A" 'lanjut' "$TMP/b2.sse")"
wait_for_done "$TMP/b2.sse" 240 "$PID_B" || fail "scene b: lanjut did not produce a turn"
OVERRIDES="$(psql_iris "SELECT count(*) FROM budget_overrides WHERE room_id='$ROOM_A';")"
[[ "$OVERRIDES" == "1" ]] || fail "scene b: lanjut wrote ${OVERRIDES} override rows, want 1"
RECEIPT="$(psql_iris "SELECT spent_usd, budget_usd, (message_id IS NOT NULL) FROM budget_overrides WHERE room_id='$ROOM_A';")"
IFS='|' read -r O_SPENT O_BUDGET O_LINKED <<<"$RECEIPT"
[[ "$O_BUDGET" == "0.0001" ]] || fail "scene b: override budget = ${O_BUDGET}"
[[ "$O_LINKED" == "t" ]] || fail "scene b: override is not linked to the user message"
LANJUT_USERS="$(psql_iris "SELECT count(*) FROM messages WHERE room_id='$ROOM_A' AND role='user' AND body='lanjut';")"
[[ "$LANJUT_USERS" == "1" ]] || fail "scene b: lanjut user row = ${LANJUT_USERS}"
say "scene b: lanjut let one turn through (spent ${O_SPENT}, budget ${O_BUDGET})"

CODE="$(curl -sS -b "$COOKIE" -o "$TMP/b3.json" -w '%{http_code}' -X POST "$BASE/api/rooms/${ROOM_A}/messages" \
  -H 'Content-Type: application/json' -d '{"text":"halo ketiga"}')"
[[ "$CODE" == "429" ]] || fail "scene b: the message after the override answered ${CODE}, want 429"
OVERRIDES="$(psql_iris "SELECT count(*) FROM budget_overrides WHERE room_id='$ROOM_A';")"
[[ "$OVERRIDES" == "1" ]] || fail "scene b: the pass was sticky (${OVERRIDES} overrides)"
echo "b|blocked=429|override=1|sticky=no|spent=${O_SPENT}|budget=${O_BUDGET}" >>"$RESULTS"
say "scene b PASSED: 429, exactly one override, then 429 again"

# ---------------------------------------------------------------------------
say "=== scenario c: streaming speech ==="
stop_api
say "restarting irisd with the budget guard off so the speech/retry scenes are not blocked"
start_api ""
COOKIE_HEADER="$(awk '$6=="iris_session"{print $6"="$7}' "$COOKIE")"
[[ -n "$COOKIE_HEADER" ]] || fail "scene c: no session cookie in the jar"
IRIS_BASE="$BASE" IRIS_COOKIE="$COOKIE_HEADER" python3 "$ROOT/scripts/p8-voice-probe.py" >"$TMP/voice.json" \
  || { cat "$TMP/voice.json"; fail "scene c: streaming speech probe failed"; }
cat "$TMP/voice.json" | jq -c .
TTFB="$(jq -r '.streaming_first_audio_ms' "$TMP/voice.json")"
TOTAL="$(jq -r '.streaming_total_ms' "$TMP/voice.json")"
NON_TTFB="$(jq -r '.non_streaming_first_byte_ms' "$TMP/voice.json")"
NON_TOTAL="$(jq -r '.non_streaming_total_ms' "$TMP/voice.json")"
TRANSCRIPT="$(jq -r '.streaming_transcript' "$TMP/voice.json")"
say "scene c: streaming first audio ${TTFB} ms / total ${TOTAL} ms; buffered first byte ${NON_TTFB} ms / total ${NON_TOTAL} ms"
say "scene c: transcript -> ${TRANSCRIPT}"
echo "c|stream_ttfb_ms=${TTFB}|stream_total_ms=${TOTAL}|buffered_ttfb_ms=${NON_TTFB}|buffered_total_ms=${NON_TOTAL}|chunks=$(jq -r '.streaming_chunks' "$TMP/voice.json")" >>"$RESULTS"

# ---------------------------------------------------------------------------
say "=== scenario d: retry a stopped run ==="
ROOM_D="$(create_room 'P8 ulangi')"
[[ -n "$ROOM_D" && "$ROOM_D" != "null" ]] || fail "room d was not created"
PID_D="$(send_bg "$ROOM_D" 'Tulis lima kalimat pendek tentang laut, satu per baris.' "$TMP/d.sse")"
wait_for "$TMP/d.sse" '^event: delta' 180 "$PID_D" || fail "scene d: no delta arrived to stop"
sleep 0.4
curl -sS -b "$COOKIE" -X POST "$BASE/api/rooms/${ROOM_D}/stop" >/dev/null || fail "scene d: stop request failed"
wait_for_done "$TMP/d.sse" 120 "$PID_D" || true
wait "$PID_D" 2>/dev/null || true
for _ in $(seq 1 60); do
  ACTIVE="$(psql_iris "SELECT count(*) FROM runs WHERE room_id='$ROOM_D' AND status IN ('queued','running','waiting_for_approval','stopping');")"
  [[ "$ACTIVE" == "0" ]] && break
  sleep 0.5
done
OLD="$(psql_iris "SELECT id, run_id, COALESCE(length(body),0) FROM messages WHERE room_id='$ROOM_D' AND role='assistant' ORDER BY created_at DESC LIMIT 1;")"
[[ -n "$OLD" ]] || fail "scene d: the stopped run left no assistant row to supersede"
IFS='|' read -r OLD_ID OLD_RUN OLD_LEN <<<"$OLD"
UMID="$(psql_iris "SELECT id FROM messages WHERE room_id='$ROOM_D' AND role='user' ORDER BY created_at DESC LIMIT 1;")"
[[ -n "$UMID" ]] || fail "scene d: no user message to retry"
U_BEFORE="$(psql_iris "SELECT count(*) FROM messages WHERE room_id='$ROOM_D' AND role='user';")"
A_BEFORE="$(psql_iris "SELECT count(*) FROM messages WHERE room_id='$ROOM_D' AND role='assistant';")"
say "scene d: stopped reply ${OLD_ID:0:8} kept; retrying user message ${UMID:0:8}"

sleep 0.5
curl -sS -b "$COOKIE" -N --max-time 420 -X POST "$BASE/api/rooms/${ROOM_D}/messages/${UMID}/retry" \
  -o "$TMP/d-retry.sse" 2>"$TMP/d-retry.err" &
RETRY_PID=$!
wait_for_done "$TMP/d-retry.sse" 240 "$RETRY_PID" || { cat "$TMP/d-retry.err"; fail "scene d: retry never finished"; }
wait "$RETRY_PID" 2>/dev/null || true

U_AFTER="$(psql_iris "SELECT count(*) FROM messages WHERE room_id='$ROOM_D' AND role='user';")"
A_AFTER="$(psql_iris "SELECT count(*) FROM messages WHERE room_id='$ROOM_D' AND role='assistant';")"
SUPERSEDED="$(psql_iris "SELECT COALESCE(meta->>'superseded','false') FROM messages WHERE id='$OLD_ID';")"
NEWEST="$(psql_iris "SELECT id, length(body), run_id FROM messages WHERE room_id='$ROOM_D' AND role='assistant' ORDER BY created_at DESC LIMIT 1;")"
IFS='|' read -r NEW_ID NEW_LEN NEW_RUN <<<"$NEWEST"

[[ "$U_AFTER" == "$U_BEFORE" ]] || fail "scene d: retry added a user row (${U_BEFORE} -> ${U_AFTER})"
[[ "$A_AFTER" == "$((A_BEFORE + 1))" ]] || fail "scene d: assistant rows ${A_BEFORE} -> ${A_AFTER}, want exactly one new"
[[ "$SUPERSEDED" == "true" ]] || fail "scene d: the old reply is not marked superseded"
[[ "$NEW_ID" != "$OLD_ID" ]] || fail "scene d: retry reused the old assistant row"
[[ "$NEW_RUN" != "$OLD_RUN" ]] || fail "scene d: retry reused the old run id"
(( NEW_LEN > 0 )) || fail "scene d: the retried reply is empty"
say "scene d: new reply ${NEW_ID:0:8} (${NEW_LEN} chars, run ${NEW_RUN}); old reply kept and superseded"
echo "d|user_rows=${U_AFTER}|assistant_rows=${A_AFTER}|superseded=${SUPERSEDED}|new_run=${NEW_RUN}" >>"$RESULTS"

# ---------------------------------------------------------------------------
echo
say "RESULTS"
cat "$RESULTS"
echo
say "PASS: P8 cost meter, budget guard, streaming speech and retry against the real stack"
