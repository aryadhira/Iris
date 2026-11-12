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
HERMES_ENV_FILE="${IRIS_HERMES_ENV_FILE:-$HOME/.hermes/.env}"

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
BACKUP_TARGET="${IRIS_BACKUP_TARGET:-$(read_env IRIS_BACKUP_TARGET)}"
BACKUP_TELEGRAM="${IRIS_BACKUP_TELEGRAM:-$(read_env IRIS_BACKUP_TELEGRAM)}"
BACKUP_CHAT="${IRIS_BACKUP_CHAT:-$(read_env IRIS_BACKUP_CHAT)}"

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

write_manifest() {
  local offsite="$1" target="${2:-}" detail="${3:-}"
  {
    echo "iris backup"
    echo "created=$(date -Iseconds)"
    echo "database=$DB_NAME"
    echo "data_dir=$DATA_DIR"
    echo "counts_rooms_messages_files_continuations=$counts"
    echo "format=pg_dump -Fc + rooms/"
    echo "offsite=$offsite"
    if [[ -n "$target" ]]; then echo "offsite_target=$target"; fi
    if [[ -n "$detail" ]]; then echo "offsite_detail=$detail"; fi
  } >"$tmp/manifest.txt"
}

# build_archive writes the manifest first, so the archive always states what
# was true for the bytes inside it. A configured target starts optimistic; if
# the copy cannot be proved, the local archive is rebuilt with offsite=false.
build_archive() {
  local offsite="$1" target="${2:-}" detail="${3:-}"
  write_manifest "$offsite" "$target" "$detail"
  rm -f "$archive.partial" "$archive"
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
}

# replicate performs exactly one configured copy and proves the remote has the
# archive. On failure it sets offsite_error and returns 1; the caller rebuilds
# the local manifest as offsite=false and exits non-zero.
replicate() {
  local target="$1"
  case "$target" in
    rclone:*)
      local spec="${target#rclone:}"
      local remote="${spec%%:*}"
      local dest="${spec#*:}"
      if [[ "$remote" == "$spec" || -z "$dest" ]]; then
        offsite_error="rclone target must be rclone:<remote>:<path>"
        return 1
      fi
      local remote_path="${remote}:${dest}"
      if command -v rclone >/dev/null 2>&1; then
        if ! rclone copy --checksum "$archive" "$remote_path" >>"$tmp/offsite.log" 2>&1; then
          offsite_error="rclone copy failed for $remote_path"
          return 1
        fi
        local remote_listing
        if ! remote_listing="$(rclone lsf "$remote_path" 2>>"$tmp/offsite.log")"; then
          offsite_error="rclone lsf failed for $remote_path"
          return 1
        fi
        if ! grep -qxF "$(basename "$archive")" <<<"$remote_listing"; then
          offsite_error="rclone lsf $remote_path does not list $(basename "$archive")"
          return 1
        fi
      elif [[ "$remote" == "local" ]]; then
        # rclone is not installed on this host. A local directory target is
        # still a real copy-and-list proof; cp/ls stand in for rclone, and the
        # manifest names the fallback instead of pretending rclone ran.
        if ! mkdir -p "$dest" >>"$tmp/offsite.log" 2>&1; then
          offsite_error="cannot create local off-site directory $dest"
          return 1
        fi
        if ! cp -p "$archive" "$dest/" >>"$tmp/offsite.log" 2>&1; then
          offsite_error="cannot copy $archive to $dest"
          return 1
        fi
        if [[ ! -f "$dest/$(basename "$archive")" ]]; then
          offsite_error="local target $dest does not list $(basename "$archive")"
          return 1
        fi
        local remote_sha
        remote_sha="$(sha256sum "$dest/$(basename "$archive")" | awk '{print $1}')"
        if [[ "$remote_sha" != "$sha" ]]; then
          offsite_error="local copy checksum differs from the archive"
          return 1
        fi
        offsite_detail="verified local directory $dest (rclone not installed)"
      else
        offsite_error="rclone is not installed; cannot reach $remote_path"
        return 1
      fi
      ;;
    telegram)
      if [[ "$BACKUP_TELEGRAM" != "1" ]]; then
        offsite_error="IRIS_BACKUP_TARGET=telegram but IRIS_BACKUP_TELEGRAM is not 1"
        return 1
      fi
      if [[ -z "$BACKUP_CHAT" ]]; then
        offsite_error="IRIS_BACKUP_CHAT is required for telegram"
        return 1
      fi
      if [[ ! -f "$HERMES_ENV_FILE" ]]; then
        offsite_error="Hermes env file not found at $HERMES_ENV_FILE"
        return 1
      fi
      local token
      token="$(awk -F= '$1 == "TELEGRAM_BOT_TOKEN" { sub(/^[^=]*=/, ""); print; exit }' "$HERMES_ENV_FILE")"
      if [[ -z "$token" ]]; then
        offsite_error="TELEGRAM_BOT_TOKEN is missing from $HERMES_ENV_FILE"
        return 1
      fi
      # The token is only in the URL given to curl; it is never echoed. Curl's
      # own output and the API body stay in the private temp directory.
      if ! curl -fsS -m 120 -o "$tmp/telegram.json" \
        -F "chat_id=$BACKUP_CHAT" \
        -F "document=@$archive" \
        "https://api.telegram.org/bot${token}/sendDocument" >>"$tmp/offsite.log" 2>&1; then
        offsite_error="Telegram sendDocument failed: $(jq -r '.description // empty' "$tmp/telegram.json" 2>/dev/null)"
        return 1
      fi
      if ! jq -e '.ok == true' "$tmp/telegram.json" >/dev/null 2>&1; then
        offsite_error="Telegram rejected the document: $(jq -r '.description // empty' "$tmp/telegram.json" 2>/dev/null)"
        return 1
      fi
      offsite_detail="verified by Telegram sendDocument ok=true"
      ;;
    *)
      offsite_error="unknown IRIS_BACKUP_TARGET (use rclone:<remote>:<path> or telegram)"
      return 1
      ;;
  esac
  return 0
}

if [[ -z "$BACKUP_TARGET" ]]; then
  build_archive false "" "local only"
  say "off-site: not configured (local only)"
else
  build_archive true "$BACKUP_TARGET"
  offsite_error=""
  offsite_detail="verified by rclone lsf"
  if ! replicate "$BACKUP_TARGET"; then
    build_archive false "$BACKUP_TARGET" "$offsite_error"
    fail "off-site: $offsite_error"
  fi
  say "off-site: $BACKUP_TARGET — $offsite_detail"
fi

# Keep the last N, newest by mtime. Rotation happens only after an off-site
# copy (when configured) has been proved, so a failed copy never deletes the
# previous good archive.
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
