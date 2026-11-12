# P7 — notifications, reminders, off-machine backup, boot receipt

**Date:** 2026-11-13
**Status:** shipped in the working tree (no commit, no push)
**Baseline:** P6 `fbf6bdb`; live on `:3300` behind
`https://server-001.tail4ed9c7.ts.net:9443` as `iris.service` + `iris-voice.service`.

## What shipped

1. Web Push with a local VAPID keypair and a hand-written service worker.
2. Server-side Indonesian reminders with a DB-derived 30 s scheduler.
3. `scripts/backup.sh` replication to rclone or Telegram, with proof before success.
4. `iris-bootcheck.service` + `scripts/bootcheck.sh` for a machine-readable reboot receipt.

Hermes was not touched (ADR-003). No new frontend dependency.

## Versions actually resolved

`go get github.com/SherClockHolmes/webpush-go@latest` resolved:

| Module | Version | Role |
|---|---|---|
| `github.com/SherClockHolmes/webpush-go` | **v1.4.0** | `aes128gcm` + VAPID ES256 sender |
| `github.com/golang-jwt/jwt/v5` | **v5.2.1** | indirect, VAPID JWT signing |
| Go toolchain | go1.23.4 | module `go 1.23.4` |

The library is usable as published; no crypto was hand-rolled. `go.mod` now lists
`webpush-go v1.4.0` as a direct dependency.

## 1. Web Push

- VAPID keys are generated once at `~/.config/iris/vapid.json` (default
  `IRIS_VAPID_KEY_FILE`), written through a temp file with mode **0600**. An existing pair is reused.
  Only `GET /api/push/key` exposes the public key; the private key is never logged or returned.
- `POST /api/push/subscribe` stores `{room_id, endpoint, keys:{p256dh,auth}}`; the endpoint is unique,
  so subscribing again from the same device updates it. `DELETE /api/push/subscribe` removes it.
  The room is loaded through the owner-scoped store, so an unauthenticated request gets 401 and a
  missing room gets 404.
- `web/public/sw.js` is hand-written, no Vite plugin. `push` shows a notification with the room name,
  a plain-text body capped at 120 characters, and `tag = roomId`. `notificationclick` focuses an open
  Iris window or opens `/r/<roomId>#m-<messageId>`.
- Server sends:
  - when a run finishes and **no SSE client is attached to that room** (the acceptance proves both
    sides: detached run → one POST; attached run → none);
  - when an approval is pending, always (exempt from debounce);
  - when a reminder fires.
- Debounce: one push per room per 30 s, except approvals.
- Honest limits: iOS needs Iris installed to the home screen; Android needs the installed app, not a
  bare tab. A real phone was not available to this machine.

## 2. Reminders

### Parser

`internal/reminder` is a pure function, timezone `Asia/Jakarta` (fixed +07:00 fallback if tzdata is
missing). It requires a trigger (`ingatkan|ingetin`) **and** an explicit time. No explicit time means
no row and Hermes answers normally.

17 table-driven cases (`internal/reminder/parser_test.go`):

| Sentence (now = 13 Nov 2026, 10:00 WIB) | Result |
|---|---|
| `ingatkan saya jam 9` | tomorrow **09:00**, not 21:00 |
| `ingatkan saya jam 15.30 telepon Budi` | today 15:30, text `telepon Budi` |
| `ingetin aku jam 9 malam` | today 21:00 |
| `ingatkan saya nanti jam 3 sore buat laporan` | today 15:00, text `laporan` |
| `tolong ingatkan saya jam 1 siang` | today 13:00 |
| `ingatkan saya besok jam 8 untuk rapat` | tomorrow 08:00, text `rapat` |
| `ingetin saya besok jam 9 pagi antar dokumen` | tomorrow 09:00, text `antar dokumen` |
| `ingatkan saya dalam 30 menit cek oven` | now + 30 min, text `cek oven` |
| `ingetin dalam 2 jam buat laporan` | now + 2 h, text `laporan` |
| `ingatkan saya dalam 45 detik` | now + 45 s |
| `ingatkan saya jam 7 pagi minum obat` | tomorrow 07:00 (07:00 already passed) |
| `ingatkan saya jam 12 malam` | tomorrow 00:00 |
| `Ingatkan saya jam 3 sore, ya` | today 15:00, text `waktunya` |
| `jam 3 saja` | **no reminder** (no trigger) |
| `ingatkan saya nanti` | **no reminder** (no time) |
| `ingatkan saya jam 25` | **no reminder** (invalid hour) |
| `ingatkan saya jam 9:75` | **no reminder** (invalid minute) |

A passed hour with no day part rolls to the next day. The parser never assumes PM: only
`malam`, `sore`, `siang`, or `pagi` move the clock.

### API and scheduler

- `POST /api/rooms/{id}/reminders {text,due_at}`
- `GET /api/rooms/{id}/reminders` (pending only)
- `DELETE /api/rooms/{id}/reminders/{rid}` (cancel; a cancelled row can never fire)
- Migration `000009_reminders_push` adds `reminders(id, room_id, text, due_at, state, created_at,
  fired_at)` and `push_subscriptions`.
- `FireReminder` updates the reminder to `fired` **and** inserts the
  `⏰ Pengingat: <text>` system message in one SQL statement. A crash cannot leave a fired row with no
  message, and a second sweep cannot insert a second message.
- 30 s tick, DB-derived. On boot it sweeps immediately. A due time earlier than this process's
  `bootTime` fires once with `(terlambat …)` and the lateness in seconds/minutes/hours.
- A typed message that created a reminder emits an SSE `reminder` event; the room shows
  `Pengingat dibuat untuk HH.MM` for 6 s.
- The room menu now opens **Pengingat & notifikasi**, a bottom-sheet inspector listing pending
  reminders with a cancel button, plus the per-device notification toggle and the iOS/Android install
  copy.

## 3. Off-machine backup

`scripts/backup.sh` still builds one archive with `db.dump`, `rooms/`, and `manifest.txt`.
`IRIS_BACKUP_TARGET` then decides:

| Target | Action | Proof |
|---|---|---|
| unset | none | prints `off-site: not configured (local only)`; manifest `offsite=false` |
| `rclone:<remote>:<path>` | `rclone copy --checksum`, then `rclone lsf` | filename present in the remote listing |
| `telegram` | `sendDocument` to `IRIS_BACKUP_CHAT` | API response `ok:true` |
| unknown | none | exits non-zero |

`telegram` only runs when `IRIS_BACKUP_TELEGRAM=1` is explicitly set. The token is read from
`~/.hermes/.env` (`TELEGRAM_BOT_TOKEN`) and is never printed; failure output contains only the
Telegram description or a generic copy error.

If a configured copy fails, the local archive is rebuilt with `offsite=false` and the script exits
non-zero. Rotation to `IRIS_BACKUP_KEEP` happens only after a successful off-site proof, so a failed
copy never deletes the previous good archive. `scripts/restore.sh` is unchanged; restoring an off-site
archive means fetching that file first.

rclone is not installed on this machine. The local-target proof therefore used the explicit
`rclone:local:<directory>` fallback: `cp` + remote file check + sha256 comparison, and the manifest says
`verified local directory … (rclone not installed)` rather than pretending rclone ran.

## 4. Boot receipt

`deploy/iris-bootcheck.service` is a oneshot with `After=iris.service iris-voice.service`, installed
and enabled by `deploy/install.sh`. `scripts/bootcheck.sh` waits up to 60 s for `/api/health`, reads
`schema` from that response, checks `/health` on the voice sidecar, then writes:

```json
{"boot_id":"...","at":"...","health":true,"schema":"9","voice":true}
```

to `~/iris-data/bootcheck/last-boot.json`, and appends one line to `history.log`. It exits non-zero only
when health never becomes true; `voice:false` is recorded honestly but does not fail the receipt.

## Measured evidence

### Go and web

```
go test ./...                 → 10 packages ok, 118 test entries
                                (77 top-level + 41 subtests); P6 reported 88
go vet ./...                  → clean
(cd web && npm test -- --run) → 9 files, 57 tests passed
(cd web && npm run build)     → vue-tsc + vite build ok
```

### `./scripts/p7-acceptance.sh` — throwaway `iris_p7_mine`, irisd `:3308`, real Hermes

```
scene a PASSED: ⏰ Pengingat: cek p7 — fired +29684 ms after due
scene b PASSED: ⏰ Pengingat: cek p7 terlambat (terlambat 3 detik) — one message after two restarts
scene c: detached run pushed
  Content-Encoding: aes128gcm
  Authorization: vapid t=…, k=…
  body: 4096 bytes (encrypted record)
scene c PASSED: detached run pushed once; attached run sent no push
scene d PASSED: verified copy, broken target exit 1, local-only manifest offsite=false
bootcheck PASSED: {"boot_id":"…","at":"…","health":true,"schema":"9","voice":true}
ALL P7 SCENARIOS PASSED
```

Scenario timings from that run: a **91 s** (60 s due + up to 30 s sweep), b 14 s,
c 10 s, d + bootcheck 0.82 s; total **117 s**. The **slowest step is scenario a**, by design.

### Browser at 390×844 — `./scripts/p7-browser.sh`

```
PASS 390: parser-created reminder shows a toast — Pengingat dibuat untuk 14.38
PASS 390: inspector lists the pending reminder — rapat p7, Kam 12 Nov, 14.38
PASS 390: inspector fits the phone width — 390/390
PASS 390: notification control is present — Notifikasi belum aktif di perangkat ini
PASS 390: push state resolves instead of hanging
PASS 390: cancel removes the reminder from the list
```

Screenshots: `changelogs/assets/p7-390-reminder-toast.png`,
`p7-390-reminders.png`, `p7-390-reminder-cancelled.png`.

What looks wrong: none functionally; no horizontal overflow. Cosmetically, the inspector is a bottom
sheet whose height follows its content, so with one reminder there is a large dimmed empty area above
it. The 6 s toast can still be seen behind the dim overlay if the inspector is opened immediately.
Both are presentation choices, not broken state.

## What could not be verified here

- A real phone: iOS home-screen PWA and Android installed-app push delivery.
- A real reboot (the machine was not rebooted). The receipt script was exercised against the
  acceptance irisd; the systemd ordering is written but not executed at boot.
- A real rclone remote (rclone is not installed) and a real Telegram send (deliberately not sent).
- The live `iris` database migration to schema 9 (the rules forbid touching the live DB). It must be
  run by Boss before or with the new binary.

## What Boss needs

1. Review the dirty tree, then commit when satisfied. No commits or pushes were made.
2. Apply migration 000009 to the live database, rebuild, install, restart:
   ```bash
   cd ~/Documents/projects/iris
   ~/go/bin/migrate -path db/migrations -database "$IRIS_DATABASE_URL" up
   (cd web && npm run build)
   go build -o bin/irisd ./cmd/irisd
   deploy/install.sh
   ```
   The new binary creates `~/.config/iris/vapid.json` once (0600) on first start.
3. Confirm linger is enabled (`loginctl enable-linger "$USER"`), otherwise the user units and the
   bootcheck will not run without a login session.
4. Choose one off-site target and put it in `~/.config/iris/env`:
   - real rclone: install rclone, configure a remote, then
     `IRIS_BACKUP_TARGET=rclone:<remote>:<path>`;
   - Telegram: `IRIS_BACKUP_TARGET=telegram`, `IRIS_BACKUP_TELEGRAM=1`,
     `IRIS_BACKUP_CHAT=<chat id>` (token already in `~/.hermes/.env`).
   Nothing was enabled and no Telegram message was sent.
5. Rehearse the reboot check after installing:
   ```bash
   systemctl --user start iris-bootcheck.service && cat ~/iris-data/bootcheck/last-boot.json
   ```
6. On a real phone: open the Tailscale HTTPS URL, install Iris to the home screen, open the installed
   app, and enable notifications from **Pengingat & notifikasi**.

## Verified by the operator

**P7 acceptance re-run by me (throwaway `iris_p7_mine`, real Hermes):**

```
a  reminder created by a typed message → "⏰ Pengingat: cek p7" fired +29 717 ms after due (30 s sweep)
b  due time passed while irisd was stopped → one message after two restarts,
   "⏰ Pengingat: cek p7 terlambat (terlambat 3 detik)", fired 3 927 ms after due
c  a detached run pushed exactly once; an attached run sent no push
d  backup: verified copy to a directory target, broken target exit 1, local-only manifest offsite=false
bootcheck  {"boot_id":"870d0f97…","health":true,"schema":"9","voice":true}
ALL P7 SCENARIOS PASSED
118 go tests, 14 packages
```

**Live instance:** migrated to schema 9, web and `irisd` rebuilt, units restarted, local 200, public 200,
`/api/push/key` 401 without a session (correct), bootcheck rehearsal wrote its file.

## Decisions taken by Boss

- **Off-site backup: deliberately skipped for now.** The nightly archive therefore lives on the same SSD as the
  database, and a dead disk loses both. `IRIS_BACKUP_TARGET` is implemented and tested against a directory
  target; switching on rclone or Telegram is a one-line env change plus a restart. Recorded as a known risk,
  not an oversight.
- **Reboot: scheduled by Boss** once P7 was safe to land. `iris-bootcheck.service` is installed and enabled, so
  the next boot leaves evidence in `~/iris-data/bootcheck/last-boot.json` instead of a claim that it "should"
  work.
