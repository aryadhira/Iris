# Iris — Architecture

**Iris** is a private gateway to one Hermes agent: rooms, text, voice, files, artifacts — and a
conversation that does not need nudging.

## The shape

```
Browser — Vue 3 + Vite + TS + Tailwind v4 + PWA (installed on the phone)
   │ HTTPS (Tailscale hostname; later a domain behind cloudflared)
   │
   ├── REST + SSE ──► irisd (Go + Echo)  ── owns rooms, messages, artifacts, search, notifications
   │                     │  Authorization: Bearer <API_SERVER_KEY>   (the key never reaches the browser)
   │                     └──► Hermes gateway :8642  (api_server platform, loopback only)
   │                              /api/sessions, /v1/runs, /v1/runs/{id}/events (SSE), /approval, /steer, /stop
   │
   └── audio ─────────► iris-audio (edge-tts + STT relay; loopback)
```

One agent ("main"). One Hermes profile. One session per room.

## Why this surface

The api_server platform is the only shipped surface with durable runs, SSE, approvals, steering and
stop. ACP has none of the continuation machinery and is what made Arya AI Lab stall. Measured evidence:
`~/Documents/research/research-20261105-hermes-private-gateway.md` §1–2.

## Continuity — the part that decides the product

Hermes will **not** wake an API session when a background job finishes in this build (no
`HERMES_SESSION_PLATFORM` stamp → the notification router finds no route; measured, see
`DECISIONS.md` ADR-003). We keep Hermes stock and do it ourselves, in two layers:

1. **Wait in-turn (primary).** The room prompt requires: if you start a job, either wait for it with
   `process_manage wait` before ending the turn, or detach it with a log and say when it will be done.
   A turn that stays open cannot lose its result.
2. **Bounded continuation pump (safety net).** When a run completes, irisd inspects its tool events and
   assembled reply for `proc_…` ids. If any remain, it stores `{procs, tools, statusLine}` on the message
   (`messages.meta`) and schedules a follow-up run ("check your job, report") with a 20 s delay. Bounded per
   chain (max 3, max 30 min total), cancelled by a new user message or archive, and always visible in the
   room as a system line with a countdown and a stop button. Pending chains live in the `continuations`
   table, so a restart cannot double them and a chain inside its window resumes on boot.

## Approvals and stop

A turn is identified by its Hermes `run_id` (stored on the assistant message and in `runs`). Without it
neither an approval nor a stop can target anything. The id is visible only in the room's run inspector.

When Hermes flags a command it emits `approval.request` on the same SSE stream, with the command already
redacted and a `request_id`. irisd stores an `approvals` row (run, request, tool, redacted command, the
choices Hermes actually offered, deadline, state) and publishes `event: approval` to the room's stream.
The phone answers through `POST /api/rooms/{id}/approvals/{approvalId}`; irisd validates the choice against
the stored list, proxies `/v1/runs/{run_id}/approval` with the stored `request_id`, and turns a 409 into
"izinnya sudah tidak berlaku" rather than a 500. A denied or expired approval is never replayed by the
continuation pump. At `approvals.timeout` (mirrored from Hermes) the row becomes `expired`; Iris never
claims it was answered. The card remains in the transcript as a receipt.

`POST /api/rooms/{id}/stop` finds live runs in `runs`, calls `/v1/runs/{id}/stop`, and is idempotent. It
cancels pending continuations, writes one visible system line ("dihentikan oleh Boss"), and a stopped run
cannot arm a follow-up: a stop is a decision, not a pause.

## Rooms

A room is a row in irisd plus one Hermes session id. Rename/archive are ours; the conversation is
Hermes'. Room history is stored by Hermes (sessions) and mirrored for search. Each room owns a folder at
`$IRIS_DATA_DIR/rooms/<room-id>/` with `inbox/` (files Boss sends) and `outbox/` (files Hermes hands over).
Every path is resolved through one guard: absolute paths, `..`, the room root itself and symlinks are
refused, so one room cannot read another.

## Files and artifacts

Uploads stream into `inbox/<uuid>-<sanitised name>`; the MIME is sniffed from the bytes, never trusted from
the client. `files` records source (`user`/`agent`), display name, relative path, MIME, size and sha256.

`/v1/artifacts/*` is gated behind browser-control and its downloads are **one-shot** (second GET 404s).
So Iris owns artifacts: the agent writes into `outbox/`, ends the turn with
`[[artifact: outbox/<name>]]`, and irisd stores the row, strips the marker from the visible reply, and
serves a normal download. One link that works on a phone, forever. A marker whose file is missing stays
in the reply — a broken promise must remain visible.

The room contract is six lines of Indonesian stored on the room. Measured on v0.21.6+636: a session's
`system_prompt` is persisted by `POST /api/sessions` but is **not applied to `/chat/stream` turns**. irisd
therefore sends the same text as per-turn `instructions`, which this build does apply; the session prompt
is still stored for the record.

## Voice

Push-to-talk → transcript → editable → send. Speech out is a **summary**, never the formatted text:
markdown stripped, code/tables dropped, secrets redacted, numbers spoken, max two sentences, full answer
on screen. edge-tts, `-8%`, Indonesian female.

Local engines cannot be called from the browser, so a loopback sidecar (`voice/`, `127.0.0.1:3310`) owns
`faster-whisper` and edge-tts. `irisd` proxies `POST /api/voice/transcribe` (multipart audio) and
`POST /api/voice/speak` behind the owner cookie; the sidecar is never exposed. A cold first turn reads as a
hang, so the model and one throwaway synthesis are warmed at boot. The microphone needs a secure context:
`localhost` is one, a LAN IP over plain http is not — the phone must use the Tailscale HTTPS hostname.

See `voice/README.md` for endpoints, environment variables, and how to run it with `irisd`.

## Reminders

`ingatkan saya …` is parsed by irisd, not Hermes, before the turn is dispatched. The parser is a pure
function on Boss' own sentence: trigger (`ingatkan|ingetin`) + an explicit `jam HH[:.]MM`, `dalam N
menit|jam`, `besok jam …`, or `nanti jam …`, in `Asia/Jakarta`. `jam 9` is 09:00; only `malam` (or
`sore`/`siang`/`pagi`) moves the clock. No explicit time means no reminder and Hermes answers normally.
A created reminder appears as a small toast in the room.

The scheduler ticks every 30 s and reads `reminders` from Postgres, so a restart neither loses nor
duplicates work. `FireReminder` updates the row to `fired` **and** inserts the visible
`⏰ Pengingat: …` system message in one statement. A due time that passed before this process started
is fired once on boot with `(terlambat …)`. Cancelling a pending row means it can never fire.

## Web Push

The VAPID keypair is generated once at `~/.config/iris/vapid.json` (mode 0600); only the public key is
served at `GET /api/push/key`. A subscription is one device endpoint with its own keys; the room scope
records where it was enabled. `web/public/sw.js` is hand-written: `push` shows a notification whose
`tag` is the room id, and `notificationclick` focuses or opens `/r/<roomId>#m-<messageId>`.

irisd pushes when a run finishes and no SSE client is attached to that room, when an approval is
pending (always), and when a reminder fires. It debounces to at most one push per room per 30 s,
except approvals. The wire format is `aes128gcm` + VAPID ES256 via `webpush-go`; the body is plain
text capped at ~120 characters. iOS requires the PWA installed to the home screen; Android requires
the installed app, not a bare tab.

## Off-site backups and the boot receipt

`scripts/backup.sh` still builds and verifies one archive. If `IRIS_BACKUP_TARGET` is set it then
copies off-machine: `rclone:<remote>:<path>` (copy, list the remote, prove the filename) or
`telegram` (only with `IRIS_BACKUP_TELEGRAM=1`, using the existing bot token without printing it).
A configured target that fails exits non-zero and rebuilds the archive manifest with `offsite=false`.
Unset means `off-site: not configured (local only)` and `offsite=false`. Rotation happens only after
a successful off-site proof.

`deploy/iris-bootcheck.service` is a oneshot after iris and voice. `scripts/bootcheck.sh` waits up to
60 s for `/api/health`, then writes `~/iris-data/bootcheck/last-boot.json`
(`{boot_id, at, health, schema, voice}`) and one line in `history.log`.

## Non-goals

Multi-agent rooms, an isometric office, agent personas, per-agent profiles. One agent, many rooms.
