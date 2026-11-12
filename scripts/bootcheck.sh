#!/usr/bin/env bash
# Iris boot proof: wait for irisd, then write one machine-readable receipt.
#
#   scripts/bootcheck.sh
#
# This cannot prove a real reboot by itself; the systemd unit runs it after
# iris.service and iris-voice.service. Boss can rehearse it with:
#   systemctl --user start iris-bootcheck.service
set -euo pipefail

BOOT_DIR="${IRIS_BOOTCHECK_DIR:-$HOME/iris-data/bootcheck}"
HEALTH_URL="${IRIS_BOOTCHECK_URL:-http://127.0.0.1:3300/api/health}"
VOICE_URL="${IRIS_BOOTCHECK_VOICE_URL:-http://127.0.0.1:3310/health}"
TIMEOUT="${IRIS_BOOTCHECK_TIMEOUT:-60}"

mkdir -p "$BOOT_DIR"
boot_id="$(cat /proc/sys/kernel/random/boot_id 2>/dev/null || echo unknown)"
at="$(date -u +%Y-%m-%dT%H:%M:%SZ)"

health=false
health_body=""
deadline=$((SECONDS + TIMEOUT))
while (( SECONDS < deadline )); do
  if health_body="$(curl -fsS -m 3 "$HEALTH_URL" 2>/dev/null)"; then
    if [[ "$health_body" == *'"ok":true'* || "$health_body" == *'"ok": true'* ]]; then
      health=true
      break
    fi
  fi
  sleep 1
done

schema=""
if [[ "$health" == true ]]; then
  schema="$(printf '%s' "$health_body" | python3 -c 'import json,sys; print(json.load(sys.stdin).get("schema", ""))' 2>/dev/null || true)"
fi

voice=false
if curl -fsS -m 5 "$VOICE_URL" >/dev/null 2>&1; then
  voice=true
fi

python3 - "$BOOT_DIR/last-boot.json" "$boot_id" "$at" "$health" "$schema" "$voice" <<'PY'
import json
import sys

path, boot_id, at, health, schema, voice = sys.argv[1:7]
data = {
    "boot_id": boot_id,
    "at": at,
    "health": health == "true",
    "schema": schema,
    "voice": voice == "true",
}
with open(path, "w", encoding="utf-8") as handle:
    json.dump(data, handle, ensure_ascii=False, indent=2)
    handle.write("\n")
PY

printf '%s boot=%s health=%s schema=%s voice=%s\n' \
  "$at" "$boot_id" "$health" "${schema:-unknown}" "$voice" >>"$BOOT_DIR/history.log"

if [[ "$health" != true ]]; then
  echo "FAIL: $HEALTH_URL did not answer with ok=true within ${TIMEOUT}s" >&2
  exit 1
fi

echo "bootcheck: health=true schema=${schema:-unknown} voice=$voice"
