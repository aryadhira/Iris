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

## ADR-007 — Approvals are Iris receipts; stop is final

**Status:** accepted (2026-11-12)

**Context.** Hermes redacts a flagged command before it leaves the gateway and parks a run as
`waiting_for_approval`. The api_server surface resolves the approval by `run_id` + `request_id` and stops a
run by `run_id`, both behind the bearer key. A phone that drops its SSE connection must not lose the
decision, and a denied or expired command must never be replayed by the continuation pump.

**Decision.** irisd owns a `runs` row and an `approvals` row. The command is stored exactly as Hermes sent
it (already redacted) and is never logged. The choices list is authoritative: Iris never offers `always` or
`session` unless Hermes did. Iris' approval deadline mirrors `approvals.timeout`; at the deadline the row is
`expired`, never `allowed`. A stop writes `dihentikan oleh Boss`, marks the run stopping/cancelled, and
cancels the chain; a later continuation cannot resurrect it.

**Consequences.** A Hermes upgrade that changes the approval payload is a parser change in one place
(`internal/hermes/sse.go`) with a captured-event test. The browser never sees the API key or `request_id`.
Expiry depends on `IRIS_APPROVAL_TIMEOUT` matching the gateway's `approvals.timeout`; the acceptance script
reads the gateway's value instead of hard-coding it.

## ADR-008 — Reminders and push belong to Iris; off-site proof is mandatory

**Status:** accepted (2026-11-13)

**Context.** Hermes cannot wake an API session for a timer (ADR-003), and a phone that is not looking
at the room needs a notification. A reminder that fires twice is worse than one that is late; a
backup that was never copied is not a backup. Reboot survival has been claimed but never measured.

**Decision.**
1. Reminders are a Postgres row owned by a room. A pure Indonesian parser creates one only when the
   sentence names an explicit time. The 30 s scheduler is DB-derived, and `FireReminder` marks the row
   and inserts its system message atomically. A row due before process start fires once with
   `(terlambat …)`.
2. Web Push uses a locally generated VAPID keypair (`~/.config/iris/vapid.json`, 0600), a hand-written
   service worker, and `aes128gcm` + VAPID ES256. Push is sent when nobody is attached to the room, on
   every pending approval, and when a reminder fires; one push per room per 30 s except approvals.
3. `IRIS_BACKUP_TARGET` is opt-in. A configured copy must be proved (remote listing or Telegram
   `ok:true`); failure exits non-zero and the manifest says `offsite=false`. Unset is local-only.
4. A bootcheck oneshot records `{boot_id, at, health, schema, voice}` after every boot.

**Consequences.** iOS needs the PWA on the home screen and Android needs the installed app; neither
can be proven from this machine. A real phone and a real reboot remain Boss' measurements. A push
library upgrade is one dependency, pinned and reported in the changelog. The bootcheck service must
be enabled by `deploy/install.sh` and requires linger to run without a login session.

## ADR-009 — Cost is an estimate from reported tokens; the budget guard refuses before writing

**Status:** accepted (2026-11-13)

**Context.** Hermes' terminal payload is the only honest source of token counts: `usage` plus the
`{provider, model}` that actually served the turn. A missing usage object is not a free turn, and a
model with no published rate is not a $0 turn. A daily budget that drops messages silently is worse
than no guard, and a guard that inspects a running turn would kill work mid-flight.

**Decision.**
1. One `usage` row per assistant turn, linked to the message. Zeros plus `usage_reported=false` when
   the payload had no usage; `cost_usd=NULL` when the model has no price. The live session stream on
   this build reports three of five counters, so the missing cache counters are stored as 0 with
   `cache_reported=false` and the cost is labelled an estimate.
2. Prices live in `~/.config/iris/prices.json`, generated from the Hermes snapshot by
   `scripts/seed-prices.py` and editable by Boss. A missing or null rate is never guessed.
3. `IRIS_DAILY_BUDGET_USD` is read at boot. The check happens once, before the user row is written;
   a refusal is a visible Indonesian system line and HTTP 429. `lanjut` writes one
   `budget_overrides` row (linked to its user message) and lets exactly one turn through.
4. Streaming speech is newline-delimited JSON with one complete MP3 chunk per sentence. The first
   audio chunk leaves before sentence 2 is synthesised; `POST /speak` stays unchanged.
5. Retry reuses the user row and marks the previous assistant reply `superseded`; the old reply is
   dimmed and labelled, never deleted.

**Consequences.** The cost is an estimate, not an invoice: a gateway that omits cache counters makes
the estimate an upper bound, and a turn with an unknown model contributes tokens but no dollars, so it
cannot count toward the budget. Those gaps are visible in the API (`unknown_cost_turns`,
`unreported_turns`, `cache_reported`) and in the changelog, not hidden behind a zero.

## ADR-010 — Sound is allowed, streams inline, and stays downloadable

**Status:** accepted (2026-11-20)

**Context.** A user could upload an mp4 but not an mp3: the allowlist had no sound types, so a song was
rejected as unsupported. Hermes artifacts bypassed the allowlist and were stored with their detected
MIME, but `Inline()` was true only for images and PDF, so every shared song or clip was served as an
attachment and could only be downloaded. The room already had a player for user videos and none for
assistant media or sound.

**Decision.**
1. The upload allowlist gains `audio/mpeg`, `audio/mp4` (M4A brand), `audio/ogg`, `audio/wav`,
   `audio/webm` (when the container or name says sound), `audio/flac` and `audio/aac`. Detection stays
   byte-first; an M4A is never labelled `video/mp4`.
2. Sound and video share one 64 MB media ceiling through `files.IsMedia`; `IsVideo` keeps working and
   `IsAudio` is explicit. Documents keep 25 MB.
3. `Inline()` is true for `video/*` and `audio/*`. Range requests keep working through
   `http.ServeContent`, and `Content-Disposition` still names the file.
4. A media card's download control uses the same `/api/files/:id` URL with `?download=1`, which forces
   `attachment`. Streaming and downloading are two explicit intents, not a guess from the user agent.

**Consequences.** Every shared sound and clip can play in the room and still be saved with its real
name. WebM is ambiguous by design: without a reliable audio marker it stays `video/webm`, so a sound-only
WebM with no `.weba` name and no early codec marker may render as a video element. The lightbox and the
file panel depend on the same frontend classification, so a new MIME must be added in `sniff.go` and
`web/src/lib/files.ts` together.
