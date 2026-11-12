#!/usr/bin/env bash
# Iris backup: Postgres dump + room folders in one verified tarball.
#
#   scripts/backup.sh
#
# Output: backups/iris-YYYYmmdd-HHMM.tar.gz (keeps the last 7).
# Configuration comes from the environment first, then ~/.config/iris/env.
# Nothing here touches the live gateway or a database other than the one named
# by IRIS_DATABASE_URL.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
ENV_FILE="${IRIS_ENV_FILE:-$HOME/.config/iris/env}"
BACKUP_DIR="${IRIS_BACKUP_DIR:-$ROOT/backups}"
PG_CONTAINER="${IRIS_PG_CONTAINER:-ailab-postgres}"
KEEP="${IRIS_BACKUP_KEEP:-7}"

fail() { echo "FAIL: $*" >&2; exit 1; }
say() { echo "[$(date -Iseconds)] $*"; }

read_env() {
  local key="$1"
  [[ -f "$ENV_FILE" ]] || return 0
  awk -F= -v k="$key" '$1 == k { sub(/^[^=]*=/, ""); print; exit }' "$ENV_FILE"
}

DSN="${IRIS_DATABASE_URL:-$(read_env IRIS_DATABASE_URL)}"
DATA_DIR="${IRIS_DATA_DIR:-$(read_env IRIS_DATA_DIR)}"
DATA_DIR="${DATA_DIR:-$HOME/iris-data}"

[[ -n "$DSN" ]] || fail "IRIS_DATABASE_URL is not set (environment or $ENV_FILE)"
[[ "$DSN" == postgres://* || "$DSN" == postgresql://* ]] || fail "IRIS_DATABASE_URL is not a postgres URL"
[[ -d "$DATA_DIR" ]] || fail "IRIS_DATA_DIR does not exist: $DATA_DIR"

# Parse postgres://user:pass@host:port/db?params without echoing the password.
rest="${DSN#*://}"
creds="${rest%%@*}"
hostpart="${rest#*@}"
DB_USER="${creds%%:*}"
DB_PASS=""
if [[ "$creds" == *:* ]]; then DB_PASS="${creds#*:}"; fi
DB_NAME="${hostpart#*/}"
DB_NAME="${DB_NAME%%\?*}"
DB_USER="${DB_USER%%\?*}"
[[ -n "$DB_USER" && -n "$DB_NAME" ]] || fail "cannot parse IRIS_DATABASE_URL"

use_docker=0
if ! command -v pg_dump >/dev/null; then
  command -v docker >/dev/null || fail "neither pg_dump nor docker is available"
  docker inspect -f '{{.State.Running}}' "$PG_CONTAINER" 2>/dev/null | grep -qx true \
    || fail "pg_dump is not installed and container $PG_CONTAINER is not running"
  use_docker=1
fi

psql_db() {
  if [[ "$use_docker" == "1" ]]; then
    docker exec -e PGPASSWORD="$DB_PASS" "$PG_CONTAINER" psql -U "$DB_USER" -d "$DB_NAME" -v ON_ERROR_STOP=1 -At -F'|' -c "$1"
  else
    PGPASSWORD="$DB_PASS" psql "$DSN" -v ON_ERROR_STOP=1 -At -F'|' -c "$1"
  fi
}

ts="$(date +%Y%m%d-%H%M)"
archive="$BACKUP_DIR/iris-$ts.tar.gz"
tmp="$(mktemp -d "${TMPDIR:-/tmp}/iris-backup-XXXXXX")"
trap 'rm -rf "$tmp"' EXIT

mkdir -p "$BACKUP_DIR"
say "backing up database $DB_NAME and $DATA_DIR/rooms"

if [[ "$use_docker" == "1" ]]; then
  docker exec -e PGPASSWORD="$DB_PASS" "$PG_CONTAINER" \
    pg_dump -U "$DB_USER" -d "$DB_NAME" --no-owner --no-acl -Fc >"$tmp/db.dump"
else
  PGPASSWORD="$DB_PASS" pg_dump "$DSN" --no-owner --no-acl -Fc >"$tmp/db.dump"
fi
[[ -s "$tmp/db.dump" ]] || fail "pg_dump produced an empty file"

mkdir -p "$tmp/rooms"
if [[ -d "$DATA_DIR/rooms" ]]; then
  cp -a "$DATA_DIR/rooms/." "$tmp/rooms/"
fi

counts="$(psql_db "SELECT (SELECT count(*) FROM rooms), (SELECT count(*) FROM messages), (SELECT count(*) FROM files), (SELECT count(*) FROM continuations)")"
{
  echo "iris backup"
  echo "created=$(date -Iseconds)"
  echo "database=$DB_NAME"
  echo "data_dir=$DATA_DIR"
  echo "counts_rooms_messages_files_continuations=$counts"
  echo "format=pg_dump -Fc + rooms/"
} >"$tmp/manifest.txt"

tar -czf "$archive.partial" -C "$tmp" db.dump rooms manifest.txt
mv "$archive.partial" "$archive"

# A backup is only a backup if it can be listed. gzip -t catches a truncated
# write; the listing proves all three members survived the round trip.
gzip -t "$archive"
listing="$(tar -tzf "$archive")"
for member in db.dump manifest.txt rooms/; do
  grep -qx "$member" <<<"$listing" || fail "archive is missing $member"
done

size="$(stat -c%s "$archive")"
sha="$(sha256sum "$archive" | awk '{print $1}')"

# Keep the last N, newest by mtime.
mapfile -t old < <(ls -1t "$BACKUP_DIR"/iris-*.tar.gz 2>/dev/null | tail -n +"$((KEEP + 1))" || true)
deleted=0
if [[ ${#old[@]} -gt 0 ]]; then
  rm -f -- "${old[@]}"
  deleted=${#old[@]}
fi

IFS='|' read -r c_rooms c_messages c_files c_conts <<<"$counts"
say "archive: $archive"
say "bytes: $size  sha256: $sha"
say "rows: rooms=$c_rooms messages=$c_messages files=$c_files continuations=$c_conts"
say "verified: $(wc -l <<<"$listing") archive entries; rotated out $deleted old archive(s), keeping $KEEP"
