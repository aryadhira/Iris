#!/usr/bin/env bash
# Install the Iris systemd user units. Idempotent: units are copied every run,
# an existing env file is never overwritten, and enable --now is safe to repeat.
#
# What this script does NOT do: enable linger. That changes who keeps your user
# manager alive at boot and is a login-session policy decision, so it stays with
# the operator. The exact command is printed at the end.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
UNIT_SRC="$ROOT/deploy"
UNIT_DIR="$HOME/.config/systemd/user"
ENV_DIR="$HOME/.config/iris"
ENV_FILE="$ENV_DIR/env"
TEMPLATE="$UNIT_SRC/iris.env.example"
NO_START=0

for arg in "$@"; do
  case "$arg" in
    --no-start) NO_START=1 ;;
    -h|--help)
      cat <<'USAGE'
Usage: deploy/install.sh [--no-start]

  --no-start   copy the units and create the env file, but do not enable/start
               the services (useful before the live database is migrated).
USAGE
      exit 0
      ;;
    *) echo "unknown argument: $arg" >&2; exit 2 ;;
  esac
done

command -v systemctl >/dev/null || { echo "systemctl not found; this is not a systemd host." >&2; exit 1; }

mkdir -p "$UNIT_DIR" "$ENV_DIR"
chmod 700 "$ENV_DIR"

install -m 0644 "$UNIT_SRC/iris.service" "$UNIT_DIR/iris.service"
install -m 0644 "$UNIT_SRC/iris-voice.service" "$UNIT_DIR/iris-voice.service"
install -m 0644 "$UNIT_SRC/iris-bootcheck.service" "$UNIT_DIR/iris-bootcheck.service"
echo "units installed in $UNIT_DIR"

if [[ -f "$ENV_FILE" ]]; then
  echo "env file kept as is: $ENV_FILE (never overwritten)"
else
  install -m 0600 "$TEMPLATE" "$ENV_FILE"
  echo "env file created: $ENV_FILE (chmod 600)"
fi
chmod 600 "$ENV_FILE"

systemctl --user daemon-reload
echo "systemd --user reloaded"

[[ -x "$ROOT/bin/irisd" ]] || echo "warning: $ROOT/bin/irisd is missing; run: (cd $ROOT && go build -o bin/irisd ./cmd/irisd)"
[[ -f "$ROOT/web/dist/index.html" ]] || echo "warning: web/dist is missing; run: (cd $ROOT/web && npm run build)"

pass_value="$(awk -F= '$1 == "IRIS_PASSPHRASE" { sub(/^[^=]*=/, ""); print; exit }' "$ENV_FILE" 2>/dev/null)"
ready=1
if [[ -z "$pass_value" || "$pass_value" == change-me* ]]; then
  ready=0
fi
if ! grep -Eq '^IRIS_DATABASE_URL=postgres(ql)?://' "$ENV_FILE" 2>/dev/null; then
  ready=0
fi

if [[ "$NO_START" == "1" ]]; then
  echo
  echo "--no-start: units are installed but not enabled (including iris-bootcheck)."
  echo "Edit $ENV_FILE, then run: $0"
  exit 0
fi

if [[ "$ready" != "1" ]]; then
  echo
  echo "STOPPED before enable --now: $ENV_FILE still has template values."
  echo "That is deliberate — starting now would only crash-loop and hide the"
  echo "real setup step. Do this, then run this script again:"
  echo
  echo "  1. create the database and schema (once):"
  echo "       ~/go/bin/migrate -path $ROOT/db/migrations -database \"\$IRIS_DATABASE_URL\" up"
  echo "  2. edit $ENV_FILE and replace the passphrase placeholder"
  echo "  3. $0"
  echo
  echo "The units are copied but not enabled yet."
  exit 0
fi

systemctl --user enable --now iris.service iris-voice.service
systemctl --user enable iris-bootcheck.service
echo "iris.service and iris-voice.service enabled and started"
echo "iris-bootcheck.service enabled (runs after the next boot or a manual start)"

cat <<EOF

Next commands:
  systemctl --user status iris.service iris-voice.service
  curl -fsS http://127.0.0.1:3300/api/health
  curl -fsS http://127.0.0.1:3310/health

Rehearse the boot proof now (does not reboot anything):
  systemctl --user start iris-bootcheck.service
  cat ~/iris-data/bootcheck/last-boot.json

Survive a reboot (the missing piece; this script does not run it):
  loginctl enable-linger $USER

Why it is yours to run: linger keeps your user manager (and these units) alive
when no session is open. It changes a login-session policy rather than a file in
this repo, so the operator enables it explicitly.
EOF
