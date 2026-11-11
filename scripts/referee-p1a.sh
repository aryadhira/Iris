#!/usr/bin/env bash
# My referee for Iris P1a — independent of the implementation's own script.
set -uo pipefail
export PATH="$HOME/.local/go/bin:$HOME/go/bin:$PATH"
ROOT=/home/aryadhira/Documents/projects/iris
DB=iris_p1_mine
PORT=3301
BASE=http://127.0.0.1:$PORT
DSN="postgres://lab:labdev@127.0.0.1:5432/$DB?sslmode=disable"
JAR=/tmp/iris-ref-cookies.txt
pass=0; fail=0
ok(){ echo "  PASS  $*"; pass=$((pass+1)); }
bad(){ echo "  FAIL  $*"; fail=$((fail+1)); }
cleanup(){ [ -n "${PID:-}" ] && kill "$PID" 2>/dev/null; sleep 1
  docker exec -i ailab-postgres psql -U lab -d postgres -c "drop database if exists $DB" >/dev/null 2>&1
  rm -f "$JAR"; }
trap cleanup EXIT INT TERM

echo "── 0. throwaway db + migrate"
ss -ltn 2>/dev/null | grep -q ":$PORT " && { echo "  port $PORT busy"; exit 1; }
docker exec -i ailab-postgres psql -U lab -d postgres -c "drop database if exists $DB" >/dev/null 2>&1
docker exec -i ailab-postgres psql -U lab -d postgres -c "create database $DB" >/dev/null || { echo "db create failed"; exit 1; }
"$HOME/go/bin/migrate" -path "$ROOT/db/migrations" -database "$DSN" up >/tmp/iris-ref-migrate.log 2>&1 \
  && ok "migrations applied" || { bad "migrate: $(tail -2 /tmp/iris-ref-migrate.log)"; exit 1; }

echo "── 1. boot irisd"
cd "$ROOT"
IRIS_DATABASE_URL="$DSN" IRIS_PASSPHRASE='referee-passphrase-1' IRIS_ADDR="127.0.0.1:$PORT" \
  ./bin/irisd >/tmp/iris-ref-labd.log 2>&1 &
PID=$!
for i in $(seq 1 60); do curl -sf -m 1 "$BASE/api/health" >/dev/null 2>&1 && break; sleep 0.25; done
[ "$(curl -s -m 3 -o /dev/null -w '%{http_code}' "$BASE/api/health")" = 200 ] && ok "health 200" || { bad "no health"; exit 1; }

echo "── 2. auth"
[ "$(curl -s -m 3 -o /dev/null -w '%{http_code}' "$BASE/api/rooms")" = 401 ] && ok "rooms without cookie → 401" || bad "unauthenticated rooms not 401"
[ "$(curl -s -m 3 -o /dev/null -w '%{http_code}' -H 'content-type: application/json' -d '{"passphrase":"wrong-passphrase"}' "$BASE/api/auth/login")" = 401 ] \
  && ok "wrong passphrase → 401" || bad "wrong passphrase accepted"
curl -s -m 5 -c "$JAR" -H 'content-type: application/json' -d '{"passphrase":"referee-passphrase-1"}' "$BASE/api/auth/login" -o /dev/null
[ "$(curl -s -m 3 -b "$JAR" -o /dev/null -w '%{http_code}' "$BASE/api/rooms")" = 200 ] && ok "cookie works" || bad "cookie rejected"

echo "── 3. room creates a Hermes session"
ROOM=$(curl -s -m 30 -b "$JAR" -H 'content-type: application/json' -d '{"name":"P1 Referee","topic":"verification"}' "$BASE/api/rooms")
RID=$(echo "$ROOM" | python3 -c "import sys,json;d=json.load(sys.stdin);print((d.get('room') or d).get('id') or '')")
SID1=$(docker exec -i ailab-postgres psql -U lab -d "$DB" -tAc "select hermes_session_id from rooms where id='$RID';" | tr -d ' ')
[ -n "$RID" ] && ok "room created ($RID)" || { bad "room create: $(echo "$ROOM" | head -c 200)"; exit 1; }
[ -n "$SID1" ] && ok "room is bound to a Hermes session ($SID1)" || bad "no hermes session id in the rooms table"

echo "── 4. first message streams and persists"
timeout 180 curl -sN -b "$JAR" -H 'content-type: application/json' \
  -d '{"text":"Reply with exactly: p1 works"}' "$BASE/api/rooms/$RID/messages" > /tmp/iris-ref-stream1.txt 2>&1
JOINED=$(python3 - <<'PY2'
import json,re
out=[]
for line in open('/tmp/iris-ref-stream1.txt', errors='replace'):
    line=line.strip()
    if line.startswith('data:'):
        try: d=json.loads(line[5:].strip())
        except Exception: continue
        if 'text' in d: out.append(d['text'])
print(re.sub(r'\s+',' ',''.join(out)))
PY2
)
echo "$JOINED" | grep -qi "p1 works" && ok "SSE carried the answer (joined deltas: $(echo "$JOINED" | head -c 40))" || bad "SSE deltas joined: $(echo "$JOINED" | head -c 120)"
grep -qE "event: (done|message.done)" /tmp/iris-ref-stream1.txt && ok "SSE closed with a done event" || echo "  note  no explicit done event"
ROWS=$(docker exec -i ailab-postgres psql -U lab -d "$DB" -tAc "select count(*) from messages where room_id='$RID';")
[ "${ROWS:-0}" -ge 2 ] && ok "messages persisted (rows=$ROWS)" || bad "persisted rows=$ROWS"

echo "── 5. the second message reuses the same Hermes session"
timeout 180 curl -sN -b "$JAR" -H 'content-type: application/json' \
  -d '{"text":"Reply with exactly: second turn"}' "$BASE/api/rooms/$RID/messages" > /tmp/iris-ref-stream2.txt 2>&1
SID2=$(docker exec -i ailab-postgres psql -U lab -d "$DB" -tAc "select hermes_session_id from rooms where id='$RID';" | tr -d ' ')
[ "$SID1" = "$SID2" ] && ok "session id unchanged across turns ($SID2)" || bad "session drifted: $SID1 → $SID2"
ASSIST=$(docker exec -i ailab-postgres psql -U lab -d "$DB" -tAc "select count(*) from messages where room_id='$RID' and role='assistant';")
[ "${ASSIST:-0}" -ge 2 ] && ok "two assistant replies stored (n=$ASSIST)" || bad "assistant rows=$ASSIST"

echo "── 6. the turn survives the client leaving"
timeout 3 curl -sN -b "$JAR" -H 'content-type: application/json' \
  -d '{"text":"Count to three slowly, then reply with exactly: survived disconnect"}' \
  "$BASE/api/rooms/$RID/messages" > /tmp/iris-ref-stream3.txt 2>&1
echo "  (client killed at 3 s)"
sleep 45
SURV=$(docker exec -i ailab-postgres psql -U lab -d "$DB" -tAc "select count(*) from messages where room_id='$RID' and role='assistant' and body ilike '%survived disconnect%';")
[ "${SURV:-0}" -ge 1 ] && ok "reply persisted although the client left" || bad "no reply after disconnect (rows=$SURV)"

echo "── 7. hygiene"
grep -qi "api_key\|Authorization: Bearer" /tmp/iris-ref-labd.log && bad "key looks printed in the log" || ok "no API key in the log"
echo "── result"
echo "  $pass passed, $fail failed"
[ "$fail" -eq 0 ] && echo "IRIS P1a REFEREE: PASS" || echo "IRIS P1a REFEREE: FAIL"
exit $((fail > 0))
