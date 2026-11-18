#!/usr/bin/env bash
# P12 acceptance — the work is visible while it happens, against the REAL
# Hermes gateway.
#
# Throwaway: database iris_p12_mine, irisd on :3314, temp data dir. The live
# iris/lab databases, irisd on :3300 and the gateway on :8642 are never touched.
# Every Hermes session created here is deleted on exit.
#
# One turn must run a shell command and read the file it wrote:
#   - at least 3 steps (thinking and/or tool), each with a duration;
#   - every step frame before `done`;
#   - two subscribers see the same sequence;
#   - the steps are persisted on the message meta, and the private reasoning is
#     absent from the body, the export and search.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
DB_NAME="iris_p12_mine"
API_PORT="${IRIS_ACCEPT_API_PORT:-3314}"
BASE="http://127.0.0.1:${API_PORT}"
DSN="postgres://lab:labdev@127.0.0.1:5432/${DB_NAME}?sslmode=disable"
PG_CONTAINER="ailab-postgres"
KEY_FILE="${IRIS_HERMES_KEY_FILE:-$HOME/.config/iris-api-key}"
HERMES_URL="${IRIS_HERMES_URL:-http://127.0.0.1:8642}"
TMP="$(mktemp -d /tmp/iris-p12-XXXXXX)"
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
  if [[ "${P12_KEEP:-0}" == "1" ]]; then
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

say "P12 acceptance — database ${DB_NAME} (throwaway), irisd ${BASE}, hermes ${HERMES_URL}"

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
say "irisd healthy"

ROOM="$(curl -sS -b "$COOKIE" -X POST "$BASE/api/rooms" -H 'Content-Type: application/json' \
  -d "$(jq -nc '{name:"P12 progres",topic:"P12 live progress"}')" | jq -r '.room.id')"
[[ -n "$ROOM" && "$ROOM" != "null" ]] || fail "room was not created"

subscribe_bg() {
  local out="$1"
  curl -sS -b "$COOKIE" -N --max-time 300 "$BASE/api/rooms/${ROOM}/events" >"$out" 2>"${out}.err" &
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

PID_A="$(subscribe_bg "$TMP/a.sse")"
PID_B="$(subscribe_bg "$TMP/b.sse")"
wait_for "$TMP/a.sse" '^event: snapshot' 20 "$PID_A" || fail "subscriber A got no snapshot"
wait_for "$TMP/b.sse" '^event: snapshot' 20 "$PID_B" || fail "subscriber B got no snapshot"

PROMPT="$(
  cat <<'TXT'
Kerjakan berurutan di turn ini, jangan dilewati:
1. Panggil tool terminal dengan perintah tepat: sh -c "printf 'p12 ok\n' > /tmp/iris-p12-step.txt"
2. Panggil tool terminal dengan perintah tepat: sh -c "cat /tmp/iris-p12-step.txt"
3. Panggil tool read_file pada path /tmp/iris-p12-step.txt
4. Jawab satu kalimat hasilnya, lalu akhiri dengan baris persis: STATUS: DONE — selesai.
TXT
)"

START=$(ms)
curl -sS -b "$COOKIE" -N --max-time 300 -X POST "$BASE/api/rooms/${ROOM}/messages" \
  -H 'Content-Type: application/json' \
  -d "$(jq -nc --arg t "$PROMPT" '{text:$t}')" >"$TMP/post.sse" 2>"$TMP/post.err" &
POST_PID=$!

wait_for "$TMP/a.sse" '^event: done' 240 "$PID_A" || fail "subscriber A never saw done"
wait_for "$TMP/b.sse" '^event: done' 240 "$PID_B" || fail "subscriber B never saw done"
wait "$POST_PID" 2>/dev/null || true
DONE_MS=$(( $(ms) - START ))

python3 - "$TMP/a.sse" <<'PY' >"$TMP/a-steps"
import json, sys
event, data = '', []
for line in open(sys.argv[1], encoding='utf-8', errors='replace'):
    line = line.rstrip('\n')
    if line.startswith('event:'):
        event = line.split(':', 1)[1].strip()
    elif line.startswith('data:'):
        data.append(line.split(':', 1)[1].lstrip())
    elif line == '':
        if event == 'step':
            step = json.loads('\n'.join(data))
            print('|'.join(str(step.get(k, '')) for k in
                           ('seq', 'kind', 'name', 'status', 'durationMs')))
        event, data = '', []
PY
python3 - "$TMP/b.sse" <<'PY' >"$TMP/b-steps"
import json, sys
event, data = '', []
for line in open(sys.argv[1], encoding='utf-8', errors='replace'):
    line = line.rstrip('\n')
    if line.startswith('event:'):
        event = line.split(':', 1)[1].strip()
    elif line.startswith('data:'):
        data.append(line.split(':', 1)[1].lstrip())
    elif line == '':
        if event == 'step':
            step = json.loads('\n'.join(data))
            print('|'.join(str(step.get(k, '')) for k in
                           ('seq', 'kind', 'name', 'status', 'durationMs')))
        event, data = '', []
PY

# Every step publication must precede done, in both subscribers.
python3 - "$TMP/a.sse" "$TMP/b.sse" <<'PY' || exit 1
import sys
for path in sys.argv[1:]:
    done_at = None
    step_at = []
    event = ''
    for number, line in enumerate(open(path, encoding='utf-8', errors='replace'), 1):
        if line.startswith('event:'):
            event = line.split(':', 1)[1].strip()
            if event == 'done':
                done_at = number
            elif event == 'step':
                step_at.append(number)
    if done_at is None:
        raise SystemExit('no done frame in ' + path)
    late = [n for n in step_at if n > done_at]
    if late:
        raise SystemExit('step frames after done in %s: %s' % (path, late))
PY

DISTINCT_A="$(cut -d'|' -f1 "$TMP/a-steps" | sort -n -u | wc -l | tr -d ' ')"
DISTINCT_B="$(cut -d'|' -f1 "$TMP/b-steps" | sort -n -u | wc -l | tr -d ' ')"
(( DISTINCT_A >= 3 )) || fail "subscriber A saw ${DISTINCT_A} steps, want >= 3"
(( DISTINCT_B >= 3 )) || fail "subscriber B saw ${DISTINCT_B} steps, want >= 3"
cmp -s "$TMP/a-steps" "$TMP/b-steps" || fail "subscribers saw different step sequences"
python3 - "$TMP/a-steps" <<'PY' || exit 1
import sys
last = {}
for line in open(sys.argv[1], encoding='utf-8', errors='replace'):
    seq, kind, name, status, duration = line.strip().split('|')
    last[int(seq)] = (kind, name, status, int(float(duration or 0)))
if len(last) < 3:
    raise SystemExit('fewer than 3 distinct steps: %s' % last)
missing = [seq for seq, (_, _, _, duration) in last.items() if duration <= 0]
if missing:
    raise SystemExit('steps without a duration: %s (%s)' % (missing, last))
print('steps=' + ','.join('%d:%s:%dms' % (seq, kind, duration)
                          for seq, (kind, _, _, duration) in sorted(last.items())))
PY

META_STEPS="$(psql_iris "SELECT COALESCE(jsonb_array_length(meta->'steps'),0) FROM messages WHERE room_id='${ROOM}' AND role='assistant' ORDER BY created_at DESC LIMIT 1;" | tr -d '[:space:]')"
(( META_STEPS >= 3 )) || fail "persisted meta.steps = ${META_STEPS}, want >= 3"
BODY="$(psql_iris "SELECT body FROM messages WHERE room_id='${ROOM}' AND role='assistant' ORDER BY created_at DESC LIMIT 1;")"
REASONING="$(psql_iris "SELECT COALESCE(meta->>'reasoning','') FROM messages WHERE room_id='${ROOM}' AND role='assistant' ORDER BY created_at DESC LIMIT 1;")"
# The body/export equality and the reasoning-exclusion are pinned by the Go
# tests (a model can legitimately think its own final sentence). Here we check
# the surface that would otherwise index meta: search. Use a long token that is
# absent from the answer, so a hit could only come from the private excerpt.
PROBE="$(python3 - "$BODY" "$REASONING" <<'PY'
import re, sys
body = sys.argv[1].lower()
for token in re.findall(r'[A-Za-z]{12,}', sys.argv[2]):
    if token.lower() not in body:
        print(token)
        break
PY
)"
if [[ -n "$PROBE" ]]; then
  HITS="$(curl -sS -b "$COOKIE" --get --data-urlencode "q=${PROBE}" "$BASE/api/search" | jq '.results | length')"
  [[ "$HITS" == "0" ]] || fail "search found reasoning text for probe ${PROBE}"
fi

FIRST_KIND="$(head -1 "$TMP/a-steps" | cut -d'|' -f2)"
STEP_LINES="$(wc -l <"$TMP/a-steps" | tr -d ' ')"
say "P12 PASSED: ${DISTINCT_A} steps (${STEP_LINES} frames), first kind=${FIRST_KIND}, done after ${DONE_MS} ms"
echo "turn|room=${ROOM}|steps=${DISTINCT_A}|frames=${STEP_LINES}|meta_steps=${META_STEPS}|done_ms=${DONE_MS}|subscribers=identical" >>"$RESULTS"

echo
say "RESULTS"
cat "$RESULTS"
echo
say "PASS: P12 live progress — steps before done, persisted, private reasoning excluded"
