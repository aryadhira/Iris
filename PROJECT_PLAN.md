# Iris — project plan

**Goal.** A private, phone-friendly room to talk to one Hermes agent — text, voice, files, artifacts — where
a task keeps running until it is done or the agent asks a question.

**Non-negotiables.** One agent. Hermes stays stock. Every claim measured. PWA over HTTPS from day one.

| Phase | Scope | Exit criteria (measured) |
|---|---|---|
| **P0 — spike** ✅ | Prove the api_server surface: sessions, runs, SSE, approvals, artifacts, continuity | 6-event run streamed; continuity FAILED and is documented → ADR-003 |
| **P1 — shell + chat** | `irisd` (Go+Echo) with rooms and a session proxy; Vue shell; one room, streaming replies | Send a message in the browser, watch it stream, reload and still see it, restart `irisd` and the room survives |
| **P2 — files + artifacts** ✅ | Uploads (image/PDF/DOCX/MD/CSV), room file store, artifact cards, downloads, per-room library | Photo arrives and opens in a lightbox; a markdown artifact downloads byte-identical to what Hermes wrote, at 390×844 and 1440×900 |
| **P3 — continuity** ✅ | Wait-in-turn prompt contract + bounded continuation pump + run timeline + "needs you" badge | A 90 s job ends and the room reports it with nobody typing, or the pump resumes it within 3 attempts |
| **P4 — voice** | Push-to-talk, editable transcript, summary TTS, barge-in | On a phone over HTTPS: hold, speak, review, send; reply is spoken as ≤2 sentences without markdown |
| **P5 — hardening** | systemd units, Tailscale HTTPS, backups, search, export, cost meter | Reboot the machine: Iris is up, rooms intact, backup restores in one command |

P4 is built and measured in Chromium with a synthetic but real-speech microphone (see
`changelogs/2026-11-12-p4-voice.md`). The phone-over-HTTPS half of its exit criterion is still open until a
real device and microphone are used.

## P1 definition of done

- `rooms` table: id, name, topic, hermes_session_id, created_at, archived_at
- `POST /rooms`, `GET /rooms`, `POST /rooms/:id/messages` (streams via SSE to the client)
- One Vue view: room list, message list, composer, streaming bubble, honest "working" timer
- Auth: single owner token or a passphrase gate; `API_SERVER_KEY` never leaves the server
- Tests: Go handler tests with a fake Hermes; one Playwright-free browser smoke check (no JS framework test debt)

## Open questions

- Auth model for a single user: shared secret cookie vs a passphrase + device cookie (leaning passphrase,
  because the phone keyboard is the only input).
- Search: SQLite FTS over a mirror of messages, or ask Hermes. (Mirror — Hermes' endpoints don't search.)
