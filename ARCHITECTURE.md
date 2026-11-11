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
2. **Bounded continuation pump (safety net).** When a run completes, irisd inspects its tool events for
   `proc_…` ids. If any are still alive, it schedules a follow-up run ("check your job, report") with a
   short backoff. Bounded per chain (max 3, max 30 min total), cancelled by a new user message, and always
   visible in the room as a system line. Derived from the database, so a restart cannot double it.

## Rooms

A room is a row in irisd plus one Hermes session id. Rename/archive are ours; the conversation is
Hermes'. Room history is stored by Hermes (sessions) and mirrored for search. Artifacts live in
`rooms/<room-id>/files/`, never in Hermes' one-shot artifact store.

## Artifacts

`/v1/artifacts/*` is gated behind browser-control and its downloads are **one-shot** (second GET 404s).
So Iris owns artifacts: the agent writes into the room folder, publishes via the office-style call, irisd
records name/size/type/sha256 and serves a normal download. One link that works on a phone, forever.

## Voice

Push-to-talk → transcript → editable → send. Speech out is a **summary**, never the formatted text:
markdown stripped, code/tables dropped, numbers spoken, max two sentences, full answer on screen.
edge-tts, `-8%`, Indonesian female. Audio is relayed by our own service (local engines cannot be called
from the browser).

## Non-goals

Multi-agent rooms, an isometric office, agent personas, per-agent profiles. One agent, many rooms.
