#!/usr/bin/env bash
# P1 acceptance: boot irisd against a throwaway database and the real Hermes,
# log in, create a room, stream two real turns, and prove the room keeps one
# Hermes session.
#
# Nothing secret is printed: the passphrase is generated and used in memory,
# and the API key is read by irisd / curl but never echoed.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
DB_NAME="iris_p1_accept"
DSN="postgres://lab:labdev@127.0.0.1:5432/${DB_NAME}?sslmode=disable"
PG_CONTAINER="ailab-postgres"
KEY_FILE="${IRIS_HERMES_KEY_FILE:-$HOME/.config/iris-api-key}"
HERMES_URL="http://127.0.0.1:8642"
BASE="http://127.0.0.1:3300"
TMP="$(mktemp -d "${TMPDIR:-/tmp}/iris-p1-XXXXXX")"
JAR="$TMP/cookies.txt"
PASS="$(openssl rand -hex 16)"
IRIS_PID=""
HERMES_SID=""

fail() {
  echo "FAIL: $*" >&2
  exit 1
}

psql_admin() {
  docker exec "$PG_CONTAINER" psql -U lab -d postgres -v ON_ERROR_STOP=1 -At -c "$1"
}

psql_iris() {
  docker exec "$PG_CONTAINER" psql -U lab -d "$DB_NAME" -v ON_ERROR_STOP=1 -At -c "$1"
}

on_exit() {
  local code=$?
  set +e
  if [[ $code -ne 0 && -f "$TMP/irisd.log" ]]; then
    echo
    echo "--- irisd log (tail) ---"
    tail -c 6000 "$TMP/irisd.log"
    echo
  fi
  if [[ -n "$IRIS_PID" ]] && kill -0 "$IRIS_PID" 2>/dev/null; then
    kill "$IRIS_PID" 2>/dev/null
    wait "$IRIS_PID" 2>/dev/null
  fi
  if [[ -n "$HERMES_SID" && -f "$KEY_FILE" ]]; then
    curl -sS -o /dev/null -X DELETE "$HERMES_URL/api/sessions/$HERMES_SID" \
      -H "Authorization: Bearer $(cat "$KEY_FILE")" 2>/dev/null
  fi
  psql_admin "DROP DATABASE IF EXISTS ${DB_NAME} WITH (FORCE);" >/dev/null 2>&1
  rm -rf "$TMP"
  exit "$code"
}
trap on_exit EXIT

[[ -f "$KEY_FILE" ]] || fail "Hermes key file not found at $KEY_FILE"
command -v curl >/dev/null || fail "curl is required"
command -v python3 >/dev/null || fail "python3 is required"
[[ -x "$HOME/go/bin/migrate" ]] || fail "golang-migrate not found at ~/go/bin/migrate"

echo "Iris P1 acceptance — $(date -Iseconds)"
echo "database: ${DB_NAME} (throwaway, dropped on exit)"
echo "hermes:   ${HERMES_URL}"
echo

# Fresh throwaway database.
psql_admin "DROP DATABASE IF EXISTS ${DB_NAME} WITH (FORCE);" >/dev/null
psql_admin "CREATE DATABASE ${DB_NAME};" >/dev/null
echo "database created"

"$HOME/go/bin/migrate" -path "$ROOT/db/migrations" -database "$DSN" up >/dev/null
echo "migrations applied: schema version $(psql_iris "SELECT version FROM schema_migrations;" | tr -d '[:space:]')"

(cd "$ROOT" && go build -o "$ROOT/bin/irisd" ./cmd/irisd) || fail "go build failed"
echo "irisd built"

IRIS_ADDR="127.0.0.1:3300" \
IRIS_DATABASE_URL="$DSN" \
IRIS_HERMES_URL="$HERMES_URL" \
IRIS_HERMES_KEY_FILE="$KEY_FILE" \
IRIS_PASSPHRASE="$PASS" \
IRIS_COOKIE_SECURE=false \
"$ROOT/bin/irisd" >"$TMP/irisd.log" 2>&1 &
IRIS_PID=$!

for _ in $(seq 1 60); do
  if curl -fsS "$BASE/api/health" >"$TMP/health.json" 2>/dev/null; then
    break
  fi
  sleep 0.2
done
[[ -s "$TMP/health.json" ]] || fail "irisd did not become healthy"
echo "health:   $(cat "$TMP/health.json")"

LOGIN_STATUS="$(curl -sS -o "$TMP/login.json" -w '%{http_code}' -c "$JAR" \
  -X POST "$BASE/api/auth/login" \
  -H 'Content-Type: application/json' \
  -d "{\"passphrase\":\"$PASS\"}")"
[[ "$LOGIN_STATUS" == "200" ]] || fail "login returned HTTP $LOGIN_STATUS"
grep -q "$(printf 'iris_session')" "$JAR" || fail "login did not leave a cookie in the jar"
echo "login:    HTTP 200, iris_session cookie stored (HttpOnly)"
echo

ROOM_STATUS="$(curl -sS -o "$TMP/room.json" -w '%{http_code}' -b "$JAR" \
  -X POST "$BASE/api/rooms" \
  -H 'Content-Type: application/json' \
  -d '{"name":"P1 acceptance","topic":"backend only"}')"
[[ "$ROOM_STATUS" == "201" ]] || { cat "$TMP/room.json"; fail "create room HTTP $ROOM_STATUS"; }
ROOM_ID="$(python3 -c 'import json,sys; print(json.load(open(sys.argv[1]))["room"]["id"])' "$TMP/room.json")"
HERMES_SID="$(psql_iris "SELECT hermes_session_id FROM rooms WHERE id = '${ROOM_ID}';" | tr -d '[:space:]')"
[[ -n "$ROOM_ID" && -n "$HERMES_SID" ]] || fail "room or session id missing"
echo "room:     ${ROOM_ID}"
echo "hermes:   ${HERMES_SID}"
echo

send_turn() {
  local text="$1" out="$2"
  local status
  status="$(curl -sS -N --max-time 240 -o "$out" -w '%{http_code}' -b "$JAR" \
    -X POST "$BASE/api/rooms/${ROOM_ID}/messages" \
    -H 'Content-Type: application/json' \
    -H 'Accept: text/event-stream' \
    -d "{\"text\":$(python3 -c 'import json,sys; print(json.dumps(sys.argv[1]))' "$text")}")"
  [[ "$status" == "200" ]] || { cat "$out"; fail "message turn HTTP $status"; }
}

sse_summary() {
  python3 - "$1" <<'PY'
import collections, sys
names = []
for line in open(sys.argv[1], encoding="utf-8"):
    if line.startswith("event:"):
        names.append(line.split(":", 1)[1].strip())
print(", ".join(f"{k}={v}" for k, v in collections.Counter(names).items()))
PY
}

assert_delta_contains() {
  local file="$1" needle="$2"
  python3 - "$file" "$needle" <<'PY'
import json, sys
text = []
for line in open(sys.argv[1], encoding="utf-8"):
    if line.startswith("data:"):
        try:
            payload = json.loads(line.split(":", 1)[1].strip())
        except json.JSONDecodeError:
            continue
        if "text" in payload:
            text.append(payload["text"])
joined = "".join(text)
if sys.argv[2].lower() not in joined.lower():
    print(f"FAIL: delta text does not contain {sys.argv[2]!r}; got {joined!r}", file=sys.stderr)
    sys.exit(1)
PY
}

assert_assistant() {
  local file="$1" needle="$2"
  python3 - "$file" "$needle" <<'PY'
import json, sys
messages = json.load(open(sys.argv[1], encoding="utf-8"))["messages"]
needle = sys.argv[2].lower()
bodies = [m["body"] for m in messages if m["role"] == "assistant"]
if not any(needle in body.lower() for body in bodies):
    print(f"FAIL: no assistant message contains {sys.argv[2]!r}; got {bodies!r}", file=sys.stderr)
    sys.exit(1)
PY
}

print_messages() {
  python3 - "$1" <<'PY'
import json, sys
messages = json.load(open(sys.argv[1], encoding="utf-8"))["messages"]
for m in reversed(messages):
    body = m["body"].replace("\n", " ")
    if len(body) > 100:
        body = body[:97] + "..."
    print(f"  {m['role']:<9} {body}")
PY
}

send_turn "Reply with exactly: p1 works" "$TMP/turn1.sse"
grep -q '^event: delta$' "$TMP/turn1.sse" || fail "turn 1 had no delta event"
assert_delta_contains "$TMP/turn1.sse" "p1 works"
echo "turn 1:   SSE $(sse_summary "$TMP/turn1.sse")"
curl -sS -b "$JAR" "$BASE/api/rooms/${ROOM_ID}/messages" -o "$TMP/messages1.json"
assert_assistant "$TMP/messages1.json" "p1 works"
echo "turn 1:   persisted messages:"
print_messages "$TMP/messages1.json"
echo

SID_BEFORE="$(psql_iris "SELECT hermes_session_id FROM rooms WHERE id = '${ROOM_ID}';" | tr -d '[:space:]')"
send_turn "Reply with exactly: p1 works again" "$TMP/turn2.sse"
grep -q '^event: delta$' "$TMP/turn2.sse" || fail "turn 2 had no delta event"
assert_delta_contains "$TMP/turn2.sse" "p1 works again"
curl -sS -b "$JAR" "$BASE/api/rooms/${ROOM_ID}/messages" -o "$TMP/messages2.json"
assert_assistant "$TMP/messages2.json" "p1 works again"
SID_AFTER="$(psql_iris "SELECT hermes_session_id FROM rooms WHERE id = '${ROOM_ID}';" | tr -d '[:space:]')"

[[ "$SID_BEFORE" == "$SID_AFTER" ]] || fail "Hermes session changed between turns: $SID_BEFORE -> $SID_AFTER"
[[ "$SID_BEFORE" == "$HERMES_SID" ]] || fail "room no longer points at the original Hermes session"

echo "turn 2:   SSE $(sse_summary "$TMP/turn2.sse")"
echo "turn 2:   persisted messages:"
print_messages "$TMP/messages2.json"
echo
echo "session:  before=${SID_BEFORE}"
echo "session:  after =${SID_AFTER}"
echo "session:  SAME (one Hermes session for both turns)"
echo
echo "PASS: p1 backend acceptance against real Hermes and real Postgres"
