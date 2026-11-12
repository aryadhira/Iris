#!/usr/bin/env bash
# My referee for Iris P3 — continuity without nudging, and honest cancellation.
set -uo pipefail
export PATH="$HOME/.local/go/bin:$HOME/go/bin:$PATH"
ROOT=/home/aryadhira/Documents/projects/iris
DB=iris_p3_mine
PORT=3303
BASE=http://127.0.0.1:$PORT
DSN="postgres://lab:labdev@127.0.0.1:5432/$DB?sslmode=disable"
JAR=/tmp/iris-p3-cookies.txt
DATA=/tmp/iris-data-p3
pass=0; fail=0
ok(){ echo "  PASS  $*"; pass=$((pass+1)); }
bad(){ echo "  FAIL  $*"; fail=$((fail+1)); }
cleanup(){ [ -n "${PID:-}" ] && kill "$PID" 2>/dev/null; sleep 1
  docker exec -i ailab-postgres psql -U lab -d postgres -c "drop database if exists $DB" >/dev/null 2>&1
  rm -rf "$DATA" "$JAR"; }
trap cleanup EXIT INT TERM

echo "── 0. throwaway stack on :$PORT"
ss -ltn 2>/dev/null | grep -q ":$PORT " && { echo "port busy"; exit 1; }
docker exec -i ailab-postgres psql -U lab -d postgres -c "drop database if exists $DB" >/dev/null 2>&1
docker exec -i ailab-postgres psql -U lab -d postgres -c "create database $DB" >/dev/null
"$HOME/go/bin/migrate" -path "$ROOT/db/migrations" -database "$DSN" up >/dev/null 2>&1 || { echo "migrate failed"; exit 1; }
rm -rf "$DATA"; mkdir -p "$DATA"
cd "$ROOT"
IRIS_DATABASE_URL="$DSN" IRIS_PASSPHRASE='referee-passphrase-1' IRIS_ADDR="127.0.0.1:$PORT" IRIS_DATA_DIR="$DATA" \
  ./bin/irisd >/tmp/iris-p3-irisd.log 2>&1 &
PID=$!
for i in $(seq 1 60); do curl -sf -m 1 "$BASE/api/health" >/dev/null 2>&1 && break; sleep 0.25; done
ok "irisd up"
curl -s -m 5 -c "$JAR" -H 'content-type: application/json' -d '{"passphrase":"referee-passphrase-1"}' "$BASE/api/auth/login" -o /dev/null
RID=$(curl -s -m 40 -b "$JAR" -H 'content-type: application/json' -d '{"name":"P3 continuity","topic":"referee"}' "$BASE/api/rooms" \
      | python3 -c "import sys,json;d=json.load(sys.stdin);print((d.get('room') or d).get('id',''))")
[ -n "$RID" ] && ok "room $RID" || { bad "no room"; exit 1; }

send(){ curl -sN -m 300 -b "$JAR" -H 'content-type: application/json' -d "{\"text\":$(python3 -c 'import json,sys;print(json.dumps(sys.argv[1]))' "$1")}" "$BASE/api/rooms/$RID/messages"; }
rows(){ docker exec -i ailab-postgres psql -U lab -d "$DB" -tAc "$1" | tr -d ' '; }

echo "── 1. a 90-second job, then nobody speaks"
T0=$(date +%s)
send "Jalankan proses background: sleep 90; echo IRIS-DONE. Jangan tunggu prosesnya, jangan polling. Akhiri balasanmu dengan satu baris: STATUS: RUNNING — menunggu 90 detik." > /tmp/iris-p3-turn1.txt 2>&1
FIRST=$(( $(date +%s) - T0 ))
echo "  first turn returned after ${FIRST}s"
NOTE=""
DEADLINE=$(( $(date +%s) + 420 ))
while [ "$(date +%s)" -lt "$DEADLINE" ]; do
  N=$(rows "select count(*) from messages where room_id='$RID' and role='system';")
  if [ "${N:-0}" -ge 1 ]; then NOTE=$(( $(date +%s) - T0 )); break; fi
  sleep 5
done
if [ -n "$NOTE" ]; then ok "automatic continuation appeared at +${NOTE}s with nobody typing"
else bad "no continuation within 7 minutes"; fi

echo "  messages so far:"
docker exec -i ailab-postgres psql -U lab -d "$DB" -c "select role, left(regexp_replace(body,'\s+',' ','g'),90) as body, created_at::timestamp(0) from messages where room_id='$RID' order by created_at;"

echo "── 2. did the continuation actually report the job?"
DEADLINE=$(( $(date +%s) + 300 ))
while [ "$(date +%s)" -lt "$DEADLINE" ]; do
  DONE=$(rows "select count(*) from messages where room_id='$RID' and role='assistant' and body ilike '%IRIS-DONE%';")
  [ "${DONE:-0}" -ge 1 ] && break
  sleep 6
done
[ "${DONE:-0}" -ge 1 ] && ok "the room received the job output (IRIS-DONE) without a nudge" || bad "no IRIS-DONE report"
CHAIN=$(rows "select count(*) from messages where room_id='$RID' and body like '%lanjut otomatis (%';")
[ "${CHAIN:-0}" -le 3 ] && ok "chain stayed within the cap (${CHAIN} continuation notes)" || bad "chain exceeded cap: $CHAIN"

echo "── 3. a DONE turn must never continue"
BEFORE=$(rows "select count(*) from messages where room_id='$RID';")
send "Jawab tepat: selesai. Akhiri dengan STATUS: DONE." > /tmp/iris-p3-turn2.txt 2>&1
sleep 40
AFTER=$(rows "select count(*) from messages where room_id='$RID';")
NEWNOTES=$(rows "select count(*) from messages where room_id='$RID' and body like '%lanjut otomatis (%';")
[ "${NEWNOTES:-0}" -eq "${CHAIN:-0}" ] && ok "STATUS: DONE did not arm the pump (notes still $NEWNOTES)" || bad "DONE armed the pump"

echo "── 4. typing mid-wait cancels the chain"
send "Jalankan lagi: sleep 120; echo IRIS-SECOND. Jangan tunggu. Akhiri dengan STATUS: RUNNING — 120 detik." > /tmp/iris-p3-turn3.txt 2>&1
sleep 8
curl -s -m 60 -b "$JAR" -H 'content-type: application/json' -d '{"text":"stop, aku ganti rencana"}' "$BASE/api/rooms/$RID/messages" -o /dev/null
MARK=$(rows "select count(*) from messages where room_id='$RID' and body like '%lanjut otomatis (%';")
sleep 45
MARK2=$(rows "select count(*) from messages where room_id='$RID' and body like '%lanjut otomatis (%';")
[ "$MARK" = "$MARK2" ] && ok "no continuation after the user spoke (notes stayed $MARK2)" || bad "continuation fired after a user message ($MARK → $MARK2)"

echo "── result"
echo "  $pass passed, $fail failed"
[ "$fail" -eq 0 ] && echo "IRIS P3 REFEREE: PASS" || echo "IRIS P3 REFEREE: FAIL"
exit $((fail > 0))
