#!/usr/bin/env bash
# P13 browser acceptance: play image, video and sound inside the room.
#
# Throwaway DB + data dir, production build served by irisd on :3316, Chromium
# over CDP at 390x844 and 1440x900.
#
# Never touches 3300, the iris database, ~/iris-data, or the live Hermes
# gateway on :8642. The Hermes URL is a dead loopback port and the key is a
# throwaway dummy, so a misdirected call fails locally instead of reaching live.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
DB_NAME="iris_p13_media"
API_PORT="${IRIS_P13_MEDIA_PORT:-3316}"
BASE="http://127.0.0.1:${API_PORT}"
DSN="postgres://lab:labdev@127.0.0.1:5432/${DB_NAME}?sslmode=disable"
PG_CONTAINER="ailab-postgres"
HERMES_URL="http://127.0.0.1:9"
TMP_ROOT="${IRIS_TMP_ROOT:-$HOME/.hermes/cache/scratch}"
mkdir -p "$TMP_ROOT"
TMP="$(mktemp -d "$TMP_ROOT/iris-p13-XXXXXX")"
DATA_DIR="$TMP/data"
ROOM_ID="11111111-1111-4111-8111-111111111111"
MESSAGE_ID="22222222-2222-4222-8222-222222222222"
PNG_ID="33333333-3333-4333-8333-333333333301"
MP3_ID="33333333-3333-4333-8333-333333333302"
MP4_ID="33333333-3333-4333-8333-333333333303"
PDF_ID="33333333-3333-4333-8333-333333333304"
ROOM_DIR="$DATA_DIR/rooms/$ROOM_ID"
PASS_FILE="$TMP/passphrase"
VAPID_FILE="$TMP/vapid.json"
KEY_FILE="$TMP/hermes-key"
COOKIE_JAR="$TMP/cookies"
SHOTS="$ROOT/changelogs/assets"
DOWNLOAD_DIR="$TMP/downloads"
FFMPEG="${FFMPEG:-$HOME/.hermes/tools/ffmpeg-9.0.1-linux-x64/bin/ffmpeg}"
GO_BIN="${GO_BIN:-$(command -v go || echo "$HOME/.local/go/bin/go")}"
API_PID=""

fail() { echo "FAIL: $*" >&2; exit 1; }
say() { echo "[$(date -Iseconds)] $*"; }
psql_admin() { docker exec "$PG_CONTAINER" psql -U lab -d postgres -v ON_ERROR_STOP=1 -At -c "$1"; }
psql_iris() { docker exec "$PG_CONTAINER" psql -U lab -d "$DB_NAME" -v ON_ERROR_STOP=1 -At -c "$1"; }
port_busy() { ss -ltn 2>/dev/null | grep -q ":$1 "; }
sha() { sha256sum "$1" | awk '{print $1}'; }
size() { stat -c%s "$1"; }

on_exit() {
  local code=$?
  set +e
  if [[ $code -ne 0 ]]; then
    [[ -f "$TMP/api.log" ]] && { echo; echo "--- irisd log (tail) ---"; tail -c 12000 "$TMP/api.log"; }
  fi
  [[ -n "$API_PID" ]] && kill "$API_PID" 2>/dev/null && wait "$API_PID" 2>/dev/null
  psql_admin "DROP DATABASE IF EXISTS ${DB_NAME} WITH (FORCE);" >/dev/null 2>&1
  rm -rf "$TMP"
  exit "$code"
}
trap on_exit EXIT

command -v docker >/dev/null || fail "docker is required"
command -v node >/dev/null || fail "node is required"
[[ -x "$GO_BIN" ]] || fail "go not found (set GO_BIN)"
[[ -x "$FFMPEG" ]] || fail "ffmpeg not found at $FFMPEG"
[[ -x "$HOME/go/bin/migrate" ]] || fail "golang-migrate not found at ~/go/bin/migrate"
port_busy "$API_PORT" && fail "port ${API_PORT} is busy; refusing to borrow it"

say "P13 media acceptance — $DB_NAME (throwaway) on ${BASE}"
psql_admin "DROP DATABASE IF EXISTS ${DB_NAME} WITH (FORCE);" >/dev/null
psql_admin "CREATE DATABASE ${DB_NAME};" >/dev/null
"$HOME/go/bin/migrate" -path "$ROOT/db/migrations" -database "$DSN" up >/dev/null
mkdir -p "$DATA_DIR" "$ROOM_DIR/outbox" "$SHOTS" "$DOWNLOAD_DIR"
openssl rand -hex 16 >"$PASS_FILE"; chmod 600 "$PASS_FILE"
printf 'throwaway-dummy-key\n' >"$KEY_FILE"; chmod 600 "$KEY_FILE"

say "generating four real files (png, mp3, mp4, pdf)"
"$FFMPEG" -hide_banner -loglevel error -f lavfi -i "testsrc=size=640x360:rate=1" \
  -frames:v 1 -y "$ROOM_DIR/outbox/foto-pantai.png"
"$FFMPEG" -hide_banner -loglevel error -f lavfi -i "sine=frequency=440:duration=2" \
  -c:a libmp3lame -b:a 96k -y "$ROOM_DIR/outbox/lagu-uji.mp3"
"$FFMPEG" -hide_banner -loglevel error \
  -f lavfi -i "testsrc=size=320x180:rate=15:duration=2" \
  -f lavfi -i "sine=frequency=660:duration=2" \
  -c:v libx264 -pix_fmt yuv420p -c:a aac -shortest -movflags +faststart \
  -y "$ROOM_DIR/outbox/klip-pendek.mp4"
python3 - "$ROOM_DIR/outbox/catatan.pdf" <<'PY'
import sys

out = []
out.append(b"%PDF-1.4\n")
objects = [
    b"<< /Type /Catalog /Pages 2 0 R >>",
    b"<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
    b"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 200 200] >>",
]
offsets = []
for index, body in enumerate(objects, start=1):
    offsets.append(sum(len(part) for part in out))
    out.append(f"{index} 0 obj\n".encode())
    out.append(body + b"\nendobj\n")
xref_at = sum(len(part) for part in out)
out.append(b"xref\n0 4\n0000000000 65535 f \n")
for offset in offsets:
    out.append(f"{offset:010d} 00000 n \n".encode())
out.append(b"trailer\n<< /Size 4 /Root 1 0 R >>\nstartxref\n")
out.append(f"{xref_at}\n".encode())
out.append(b"%%EOF\n")
with open(sys.argv[1], "wb") as handle:
    handle.write(b"".join(out))
PY

[[ -s "$ROOM_DIR/outbox/foto-pantai.png" ]] || fail "png was not generated"
[[ -s "$ROOM_DIR/outbox/lagu-uji.mp3" ]] || fail "mp3 was not generated"
[[ -s "$ROOM_DIR/outbox/klip-pendek.mp4" ]] || fail "mp4 was not generated"
[[ -s "$ROOM_DIR/outbox/catatan.pdf" ]] || fail "pdf was not generated"

say "seeding one room, one assistant message and four files"
psql_iris "
INSERT INTO rooms (id, name, topic, hermes_session_id, folder_path, system_prompt)
VALUES ('$ROOM_ID', 'P13 media', 'Putar media di room', 'sess-p13-media', '$ROOM_DIR', 'Folder room ini: $ROOM_DIR');
INSERT INTO messages (id, room_id, role, body, created_at)
VALUES ('$MESSAGE_ID', '$ROOM_ID', 'assistant', 'Ini hasilnya, Boss. Ada gambar, lagu, klip pendek, dan catatan.', now());
INSERT INTO files (id, room_id, message_id, source, name, path, mime, size, sha256) VALUES
  ('$PNG_ID', '$ROOM_ID', '$MESSAGE_ID', 'agent', 'foto-pantai.png', 'outbox/foto-pantai.png', 'image/png', $(size "$ROOM_DIR/outbox/foto-pantai.png"), '$(sha "$ROOM_DIR/outbox/foto-pantai.png")'),
  ('$MP3_ID', '$ROOM_ID', '$MESSAGE_ID', 'agent', 'lagu-uji.mp3', 'outbox/lagu-uji.mp3', 'audio/mpeg', $(size "$ROOM_DIR/outbox/lagu-uji.mp3"), '$(sha "$ROOM_DIR/outbox/lagu-uji.mp3")'),
  ('$MP4_ID', '$ROOM_ID', '$MESSAGE_ID', 'agent', 'klip-pendek.mp4', 'outbox/klip-pendek.mp4', 'video/mp4', $(size "$ROOM_DIR/outbox/klip-pendek.mp4"), '$(sha "$ROOM_DIR/outbox/klip-pendek.mp4")'),
  ('$PDF_ID', '$ROOM_ID', '$MESSAGE_ID', 'agent', 'catatan.pdf', 'outbox/catatan.pdf', 'application/pdf', $(size "$ROOM_DIR/outbox/catatan.pdf"), '$(sha "$ROOM_DIR/outbox/catatan.pdf")');
" >/dev/null

say "building the SPA and irisd"
(cd "$ROOT/web" && npm run build >"$TMP/build.log" 2>&1) || { tail -60 "$TMP/build.log"; fail "npm run build failed"; }
(cd "$ROOT" && "$GO_BIN" build -o "$ROOT/bin/irisd" ./cmd/irisd) || fail "go build failed"

IRIS_ADDR="127.0.0.1:${API_PORT}" \
IRIS_DATABASE_URL="$DSN" \
IRIS_HERMES_URL="$HERMES_URL" \
IRIS_HERMES_KEY_FILE="$KEY_FILE" \
IRIS_PASSPHRASE="$(cat "$PASS_FILE")" \
IRIS_COOKIE_SECURE=false \
IRIS_DATA_DIR="$DATA_DIR" \
IRIS_WEB_DIR="$ROOT/web/dist" \
IRIS_VAPID_KEY_FILE="$VAPID_FILE" \
"$ROOT/bin/irisd" >"$TMP/api.log" 2>&1 &
API_PID=$!
for _ in $(seq 1 100); do curl -fsS "$BASE/api/health" >/dev/null 2>&1 && break; sleep 0.25; done
[[ "$(curl -s -m 3 -o /dev/null -w '%{http_code}' "$BASE/api/health")" == "200" ]] || fail "irisd unhealthy"
curl -fsS "$BASE/" | grep -q '<div id="app">' || fail "irisd is not serving the built SPA"

curl -fsS -c "$COOKIE_JAR" -H 'Content-Type: application/json' \
  -d "$(printf '{"passphrase":"%s"}' "$(cat "$PASS_FILE")")" \
  "$BASE/api/auth/login" >/dev/null

echo
echo "--- GET /api/files/$MP3_ID with Range: bytes=0-1023 ---"
curl -sS -D - -o /dev/null -b "$COOKIE_JAR" -H 'Range: bytes=0-1023' \
  "$BASE/api/files/$MP3_ID"
echo
echo "--- GET /api/files/$MP3_ID?download=1 (download control) ---"
curl -sS -D - -o /dev/null -b "$COOKIE_JAR" "$BASE/api/files/$MP3_ID?download=1"
echo

IRIS_WEB_BASE="$BASE" IRIS_PASSPHRASE_FILE="$PASS_FILE" IRIS_SHOTS_DIR="$SHOTS" \
  IRIS_ROOM_ID="$ROOM_ID" IRIS_MP3_ID="$MP3_ID" IRIS_MP4_ID="$MP4_ID" IRIS_PNG_ID="$PNG_ID" \
  IRIS_DOWNLOAD_DIR="$DOWNLOAD_DIR" \
  IRIS_RESULTS_FILE="$SHOTS/p13-results.json" \
  node "$ROOT/scripts/browser-p13.mjs"

echo
echo "PASS: P13 media acceptance (screenshots in $SHOTS)"
