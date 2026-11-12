#!/usr/bin/env bash
# Iris restore: extract one backup into a database you name and (optionally)
# a data directory.
#
#   scripts/restore.sh <archive> <database> [--force] [--data-dir DIR]
#
# Guards:
#   - the live database named by IRIS_DATABASE_URL is refused without --force;
#   - postgres/template* are always refused;
#   - an existing target is refused without --force.
# With --force an existing target is dropped and recreated. Room folders are
# merged into --data-dir (default $IRIS_DATA_DIR for the live database, and ~/iris-data.<db> for any
#   other target); the path is
# printed before anything is written.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
ENV_FILE="${IRIS_ENV_FILE:-$HOME/.config/iris/env}"
PG_CONTAINER="${IRIS_PG_CONTAINER:-ailab-postgres}"

fail() { echo "FAIL: $*" >&2; exit 1; }
say() { echo "[$(date -Iseconds)] $*"; }

read_env() {
  local key="$1"
  [[ -f "$ENV_FILE" ]] || return 0
  awk -F= -v k="$key" '$1 == k { sub(/^[^=]*=/, ""); print; exit }' "$ENV_FILE"
}

archive=""
target=""
force=0
data_dir=""

while [[ $# -gt 0 ]]; do
  case "$1" in
    --force) force=1; shift ;;
    --data-dir)
      [[ $# -ge 2 ]] || fail "--data-dir needs a path"
      data_dir="$2"; shift 2 ;;
    -h|--help)
      sed -n '2,20p' "${BASH_SOURCE[0]}" | sed 's/^# \{0,1\}//'
      exit 0 ;;
    -*)
      fail "unknown option: $1" ;;
    *)
      if [[ -z "$archive" ]]; then archive="$1"
      elif [[ -z "$target" ]]; then target="$1"
      else fail "unexpected argument: $1"
      fi
      shift ;;
  esac
done

[[ -n "$archive" && -n "$target" ]] || fail "usage: scripts/restore.sh <archive> <database> [--force] [--data-dir DIR]"
[[ -f "$archive" ]] || fail "archive not found: $archive"
[[ "$target" =~ ^[a-zA-Z0-9_]+$ ]] || fail "database name must be letters, digits and underscores: $target"
case "$target" in
  postgres|template0|template1) fail "refusing to restore into the reserved database $target" ;;
esac

DSN="${IRIS_DATABASE_URL:-$(read_env IRIS_DATABASE_URL)}"
[[ -n "$DSN" ]] || fail "IRIS_DATABASE_URL is not set (environment or $ENV_FILE)"
base="${DSN%%\?*}"
query=""
[[ "$DSN" == *\?* ]] && query="?${DSN#*\?}"
live_db="${base##*/}"
target_dsn="${base%/*}/${target}${query}"

DATA_DIR="${data_dir:-${IRIS_DATA_DIR:-$(read_env IRIS_DATA_DIR)}}"
DATA_DIR="${DATA_DIR:-$HOME/iris-data}"

# Restoring a SCRATCH database must never merge files into the live room folders: that would mix two
# databases' attachments on disk while the live one is still running. Only the live target may write to
# the live data dir; anything else gets its own sibling directory unless --data-dir says otherwise.
if [[ -z "$data_dir" && "$target" != "$live_db" ]]; then
  DATA_DIR="$HOME/iris-data.$target"
  say "target is not the live database, so room files go to $DATA_DIR (override with --data-dir)"
fi

if [[ "$target" == "$live_db" && "$force" != "1" ]]; then
  fail "refusing to overwrite the live database '$target' without --force"
fi

rest="${DSN#*://}"
creds="${rest%%@*}"
hostpart="${rest#*@}"
DB_USER="${creds%%:*}"
DB_PASS=""
if [[ "$creds" == *:* ]]; then DB_PASS="${creds#*:}"; fi
DB_USER="${DB_USER%%\?*}"

use_docker=0
if ! command -v psql >/dev/null; then
  command -v docker >/dev/null || fail "neither psql nor docker is available"
  docker inspect -f '{{.State.Running}}' "$PG_CONTAINER" 2>/dev/null | grep -qx true \
    || fail "psql is not installed and container $PG_CONTAINER is not running"
  use_docker=1
fi

psql_admin() {
  if [[ "$use_docker" == "1" ]]; then
    docker exec -e PGPASSWORD="$DB_PASS" "$PG_CONTAINER" psql -U "$DB_USER" -d postgres -v ON_ERROR_STOP=1 -At -c "$1"
  else
    PGPASSWORD="$DB_PASS" psql "postgres://${DB_USER}:${DB_PASS}@${hostpart%%/*}/postgres${query}" -v ON_ERROR_STOP=1 -At -c "$1"
  fi
}

psql_target() {
  if [[ "$use_docker" == "1" ]]; then
    docker exec -e PGPASSWORD="$DB_PASS" "$PG_CONTAINER" psql -U "$DB_USER" -d "$target" -v ON_ERROR_STOP=1 -At -F'|' -c "$1"
  else
    PGPASSWORD="$DB_PASS" psql "$target_dsn" -v ON_ERROR_STOP=1 -At -F'|' -c "$1"
  fi
}

tmp="$(mktemp -d "${TMPDIR:-/tmp}/iris-restore-XXXXXX")"
trap 'rm -rf "$tmp"' EXIT

say "archive: $archive"
tar -xzf "$archive" -C "$tmp"
[[ -s "$tmp/db.dump" ]] || fail "archive has no db.dump"
[[ -f "$tmp/manifest.txt" ]] || fail "archive has no manifest.txt"

exists="$(psql_admin "SELECT 1 FROM pg_database WHERE datname = '$target'")"
if [[ "$exists" == "1" ]]; then
  [[ "$force" == "1" ]] || fail "database '$target' already exists; pass --force to replace it"
  say "dropping existing database $target (--force)"
  psql_admin "DROP DATABASE IF EXISTS \"$target\" WITH (FORCE);" >/dev/null
fi
psql_admin "CREATE DATABASE \"$target\";" >/dev/null
say "created database $target"

if [[ "$use_docker" == "1" ]]; then
  docker exec -i -e PGPASSWORD="$DB_PASS" "$PG_CONTAINER" \
    pg_restore -U "$DB_USER" -d "$target" --no-owner --no-acl --exit-on-error <"$tmp/db.dump"
else
  PGPASSWORD="$DB_PASS" pg_restore --dbname="$target_dsn" --no-owner --no-acl --exit-on-error "$tmp/db.dump"
fi
say "restored schema and rows"

mkdir -p "$DATA_DIR/rooms"
if [[ -d "$tmp/rooms" ]] && [[ -n "$(ls -A "$tmp/rooms")" ]]; then
  cp -a "$tmp/rooms/." "$DATA_DIR/rooms/"
  say "room files merged into $DATA_DIR/rooms"
else
  say "archive has no room files; database restored only"
fi

counts="$(psql_target "SELECT (SELECT count(*) FROM rooms), (SELECT count(*) FROM messages), (SELECT count(*) FROM files), (SELECT count(*) FROM continuations)")"
IFS='|' read -r c_rooms c_messages c_files c_conts <<<"$counts"
say "restored rows: rooms=$c_rooms messages=$c_messages files=$c_files continuations=$c_conts"
say "target: $target (data dir: $DATA_DIR)"
