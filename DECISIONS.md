# Decisions

## ADR-001 — Build on the Hermes api_server platform, not ACP

**Status:** accepted (2026-11-05)

**Context.** Arya AI Lab speaks ACP and an agent that ends a turn while a job runs is never called back.
Reading the installed build: `acp_adapter/` has no reference to the background-completion queue, while the
gateway drains it and re-enters the conversation.

**Decision.** Iris talks to `gateway/platforms/api_server.py`: durable runs, SSE events, approvals,
steering, stop.

**Consequences.** We get a real conversation API and structured tool events. We do not get continuation
for free (ADR-003). ACP stays available if a structured editor-style host is ever needed.

## ADR-002 — One agent, one profile; a room is a session

**Status:** accepted (2026-11-05, Boss)

Boss's decision: one agent, like Telegram. Rooms are separate conversations, not separate colleagues.
No personas, no multi-agent orchestration, no office.

**Consequences.** Every room binds to one Hermes session id. Removing the orchestration layer removes
most of the complexity of the lab. If several agents are ever wanted, it is an additive change (session per
room per agent), not a rewrite.

## ADR-003 — Keep Hermes stock; continuity lives in Iris

**Status:** accepted (2026-11-05, Boss: "do A")

**Context.** Measured on 2026-11-11, build v0.21.6+636:
- A `terminal(background=true, notify=true)` job starts correctly from an API run (`tool.completed`
  reports `proc_6a1c1303b005`).
- Nothing happens when it finishes: 150 s and 300 s of listening, session message count unchanged.
- Cause: `api_server*.py` never sets `HERMES_SESSION_PLATFORM`/session id for its agents, so
  `run_notifications._raw_process_event_session_id` finds no route and drops the event.
- Related: `/v1/artifacts/*` answers `404 browser_control_disabled`; downloads are one-shot.

**Decision.** Do not patch `~/.hermes/hermes-agent` (a patch does not survive `hermes update`, and a
forked agent is a maintenance liability). Implement continuity in Iris:
1. **Wait in-turn** — the room prompt requires `process_manage wait` before ending a turn that started a
   job; detaching is allowed only with a log and an explicit promise.
2. **Bounded continuation pump** — irisd re-prompts a run that left live background work, max 3 per chain,
   30 min cap, cancelled by a new user message, always visible in the room.

**Consequences.** Iris needs a small process-state model (what a run left running) and a scheduler. In
exchange the upgrade path stays clean. If Hermes later stamps the platform for API sessions, layer 1 can
be relaxed and the pump kept as a safety net.

## ADR-004 — Iris owns artifacts

**Status:** accepted (2026-11-05)

Hermes' artifact endpoints are gated and one-shot. Files belong to a room and must be downloadable more
than once, on a phone. irisd stores bytes under `$IRIS_DATA_DIR/rooms/<room-id>/` (`inbox/` for uploads,
`outbox/` for deliverables), records sha256/size/mime, and serves them itself. The agent writes the file
and ends its reply with `[[artifact: outbox/<name>]]`; it never hands us a URL.

## ADR-005 — Voice: push-to-talk, summary speech, free engines

**Status:** accepted (2026-11-05, Boss)

Push-to-talk first (open-mic has too many edge cases). Speech out is edge-tts, Indonesian female, `-8%`,
and speaks a **summary** — markdown stripped, code and tables dropped, at most two sentences, full text on
screen. Realtime voice (WebRTC, paid) is explicitly out of scope for v1.

## ADR-006 — Room files: inbox/outbox, per-turn instructions

**Status:** accepted (2026-11-12)

**Context.** P2 needs files both ways: Boss uploads into a room, Hermes hands artifacts back. The plan was
to set a room folder and artifact contract once as the Hermes session's `system_prompt`. Measured on
build v0.21.6+636: `POST /api/sessions` accepts and persists `system_prompt` (`has_system_prompt: true`),
but `/api/sessions/{id}/chat/stream` ignores it; a throwaway session wrote to `~/outbox` instead of the
room folder. `PATCH /api/sessions/{id}` also refuses `system_prompt`. The same build **does** apply a
per-request `instructions` field as the ephemeral system prompt — measured: the next probe wrote exactly
into the room's `outbox/` and ended with the marker.

**Decision.** Each room owns `$IRIS_DATA_DIR/rooms/<room-id>/{inbox,outbox}` and stores a short Indonesian
contract on the room row. irisd sends that text as per-turn `instructions` (and still records it as the
session's `system_prompt`). Markers `[[artifact: outbox/<name>]]` are parsed from the assembled reply,
resolved through the room path guard, stored in `files` with sha256, and stripped from the visible body.
Broken markers stay visible.

**Consequences.** The contract costs ~100 tokens per turn. It is editable without recreating the Hermes
session (the prompt is ours, not the session's). Existing P1 rooms have an empty folder path and need a
backfill or recreation before they can carry files.
