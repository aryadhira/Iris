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


## Non-goals

Multi-agent rooms, an isometric office, agent personas, per-agent profiles. One agent, many rooms.
