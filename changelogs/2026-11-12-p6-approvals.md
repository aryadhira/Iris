# P6 — approvals and stop

**Date:** 2026-11-12
**Scope:** every assistant turn keeps its Hermes `run_id`; a parked `approval.request` becomes an inline
card that can be answered once/session/always/deny; Stop interrupts the real run and kills its continuation
chain. No change to `~/.hermes` (ADR-003).

## Why this phase exists

P1–P5 made a room that streams, keeps files, continues a job, and speaks. Two things still made it a demo:
a dangerous command parked forever with no way to answer it, and a Stop button that admitted it was not
connected. This phase makes both real without weakening Hermes' own redaction or timeout.

## The captured event (real gateway, 2026-11-12)

The pinned build emits this on the session SSE stream. It is the frame the parser is built from; the command
is already redacted by Hermes and the ids are truncated only for this document.

```
event: approval.request
data: {
  "command": "rtk curl -s --max-time 2 http://169.254.169.254/latest/meta-data/ || echo P6-PROBE-TIDAK-ADA",
  "pattern_key": "cloud metadata endpoint access (instance credentials)",
  "pattern_keys": ["cloud metadata endpoint access (instance credentials)"],
  "description": "cloud metadata endpoint access (instance credentials)",
  "allow_permanent": true,
  "allow_session": true,
  "request_id": "3902fce5…",
  "event": "approval.request",
  "run_id": "run_2a1004c7…",
  "timestamp": 1794458198.9067833,
  "message_id": "msg_71721666…",
  "choices": ["once", "session", "always", "deny"],
  "session_id": "api_1794457951…",
  "seq": 4,
  "ts": 1794458198.9068284
}
```

The full raw data object is stored at `/tmp/p6-approval-event.json` from the capture. The parser test
`internal/hermes/approval_test.go` reads a checked-in copy (`internal/hermes/testdata/approval-request.json`)
and asserts the exact run id, request id, command, description, session id and four choices.

Two parser facts that were not obvious:

- The session-stream payload does **not** carry a `tool_name`. The tool is known from the immediately
  preceding `tool.started`; irisd uses that real stream value as the display fallback, never a guess.
- A parked approval can be silent for the whole `approvals.timeout` (300 s here). The old 90 s idle
  watchdog would have killed the stream, so an approval now raises the watchdog to 10 minutes.

## What was built

### Run identity

- Migration `000008_approvals` adds `runs` (`run_id`, `room_id`, `session_id`, status, timestamps) and
  `approvals` (run, session, request id, tool, redacted command, description, offered choices, state,
  choice, created/resolved/expiry).
- Every assistant message already had `run_id`; it is now always populated from `run.started` and shown
  only inside the expanded "apa yang Hermes lakukan" inspector.
- `runs` is what lets `POST /stop` find live work after the browser has gone; the message row alone cannot.

### Approvals

- `runTurn` handles `approval.request`, stores the row with `expires_at = now + approvals.timeout`, and
  publishes `event: approval` through a small room hub. The active turn stream gets the frame before the
  turn continues; a second tab on the console polls `GET /api/rooms/{id}/approvals` every 4 s as fallback.
- `POST /api/rooms/{id}/approvals/{approvalId}` `{choice}`:
  - 404 if the approval is not in that room;
  - 400 if the choice is not in the stored `choices` (so `always` is never invented);
  - 409 "Izinnya sudah tidak berlaku." if the row is expired/superseded or Hermes answers
    `approval_not_active` / `approval_not_pending`;
  - 200 with the updated receipt when Hermes accepted the answer.
- Only an authenticated Iris owner can call it; the API key stays in irisd.
- Expiry is two-sided: a 2 s sweeper marks pending rows `expired`, and every read (`GET approvals`,
  `GET rooms`) expires first, so a stale row can never look answerable. At the deadline the card says
  "Kedaluwarsa — Hermes menolak sendiri, tidak pernah dijalankan"; Iris never writes `allowed` for it.
- A denied or expired approval makes `runWasDenied` true; the continuation pump is not armed. A refusal is a
  decision, not a retry.

### Stop

- `POST /api/rooms/{id}/stop` lists live `runs` (queued/running/waiting_for_approval), calls
  `/v1/runs/{id}/stop` per run, marks them `stopping`, and answers `{stopped: n}`.
- A second call finds nothing and answers 200 `{stopped: 0, reason: "tidak ada yang berjalan"}`.
- Hermes 404/409 (`run_not_active`) also answers 200 with a reason and marks the stale row `interrupted`;
  a real gateway failure is a 502, never a dishonest success.
- One system line `dihentikan oleh Boss` is written; pending and running continuations are cancelled.
- The trailing `done` sentinel from Hermes used to overwrite `cancelled` with `completed`. That was measured
  in the smoke run and fixed: terminal status is decided from `run.*` / `assistant.*`, and the bare `done`
  sentinel no longer wins. `TestStopSentinelDoesNotOverwriteCancelledWithCompleted` pins it.

### Web

- `ApprovalCard.vue`: redacted command in `<code>`, tool name, description, countdown, and exactly the
  choices Hermes offered. `Izinkan sekali` is violet primary; `Sesi ini` / `Selalu izinkan` appear only when
  offered; `Tolak` is the red, last action. Buttons are 44 px high and stack full-width under 420 px.
- While resolving, the card is disabled and shows the pending choice. After resolution it becomes a receipt
  ("Diizinkan sekali oleh Boss", "Ditolak oleh Boss", "Kedaluwarsa — Hermes menolak sendiri…") with the
  command and time, and stays in the chronological transcript.
- `Enter` answers the primary choice only when focus is inside the card; `Escape` denies only there too.
  There is no global approval shortcut.
- Header chip adds `butuh izin · n`; the room list shows an orchid dot for rooms with
  `pending_approvals > 0` (list refresh every 5 s).
- Stop is enabled only while the room is `bekerja` / `menunggu proses` and no approval is pending. A run
  older than 60 s asks for an inline confirmation; a younger one stops at once. The
  "Stop belum tersambung" title and screen-reader note are gone.

## Test numbers

- `go test ./... -count=1`: **89 Go tests, all pass** (8 packages with tests).
- `go test -race ./internal/httpapi ./internal/hermes`: **pass**.
- `npm test -- --run`: **57 web tests, 9 files, all pass**.
- P6 Go tests specifically: captured-event parser; once lifecycle (run id/session id persisted, approval
  count on the room); unoffered `always` rejected without calling Hermes; expiry clears pending and refuses
  409; denied approval does not arm a continuation; stop idempotent + exactly one system line; stale run
  stop is 200; Hermes 409 is honest and not retried; stop sentinel; unauthenticated answer 401.
- Browser: `changelogs/assets/p6-results.json` — **28 assertions, 0 failures**.
- Acceptance: `scripts/p6-acceptance.sh` — **4 scenarios, all pass** (a/b/c requested, plus d for the real
  expiry deadline).

## Real acceptance results (`scripts/p6-acceptance.sh`, real Hermes)

Stack: throwaway database `iris_p6_mine`, irisd `127.0.0.1:3307`, real gateway `127.0.0.1:8642`, real
`approvals.timeout` read from the gateway (300 s). Whole run: 12:09:25 → 12:16:13 (+07:00).

| Scenario | What happened | Measured |
|---|---|---|
| **a. once** | Protected-file write (`AGENTS.md`) asked for approval; `once` was sent; the file landed and the turn completed. | approval after **10,182 ms**; resolve → done **3,846 ms**; run `completed` |
| **b. deny** | `CLAUDE.md` write was denied; the file never appeared; the reply ended `STATUS: BLOCKED`; no retry after the 25 s continuation window. | approval after **4,341 ms**; deny → done **4,344 ms**; 0 continuations; 0 `lanjut otomatis` lines |
| **c. stop** | A 120 s background job held open by `process_manage wait` was stopped; run ended `cancelled`; one `dihentikan oleh Boss`; no continuation 25 s later. | reached `process_manage` at **+17,055 ms**; stop → done **1,066 ms**; second stop `{stopped:0}` |
| **d. expiry** | Nobody answered the real card. At the gateway's own 300 s timeout the row became `expired`, `pending_approvals` went to 0, Hermes failed closed and the turn completed. | expired after **310,099 ms**; `pending_approvals=0`; choice empty; 0 continuations |

Raw log excerpt:

```
scene a: approval aaa722bb-… after 10182 ms; tool=write_file; choices=["once","deny"]
scene a PASSED: approval +10182 ms, resolve->done 3846 ms, run completed
scene b PASSED: approval +4341 ms, deny->done 4344 ms, no retry, run=completed
scene c PASSED: run reached process_manage +17055 ms, stop->done 1066 ms, status cancelled, no continuation
scene d PASSED: expired after 310099 ms, pending_approvals=0, run=completed
```

The deterministic real approval is the protected-instruction-file gate: it always asks and offers only
`once`/`deny`, so it also proves that Iris does not invent a wider scope. The captured four-choice event
above came from a cloud-metadata probe; smart approval sometimes auto-approves that probe, so it was used
for the parser capture, not as a flaky acceptance trigger.

## Browser results

`scripts/p6-browser.sh` (throwaway `iris_p6_browser`, irisd `:3307` serving `web/dist`, Chromium over CDP,
`IRIS_APPROVAL_FIXTURE=1`). Synthetic approvals were injected through the server's room hub while a real
Hermes turn held the SSE stream open.

- 390×844: card arrived **25 ms** after injection (before the 4 s poll), command in `<code>`, tool name,
  four choices, **44 px** buttons, **328 px** full-width stack; `Izinkan sekali` became an allowed receipt.
- 1440×900: card renders, no horizontal overflow; `Sesi ini` became its receipt.
- Synthetic `once/deny` card: `Selalu izinkan` and `Sesi ini` are absent.
- Global `Escape` did nothing; `Escape` after focusing the card denied it. `Enter` on the focused card
  answered `once`.
- 3 s synthetic card: card read expired with no buttons; the chip left `butuh izin` (`bekerja` because the
  real run was still going).
- No "belum tersambung" text anywhere; Stop was enabled during the real run, stopped without a
  confirmation (run younger than 60 s), and the transcript showed `dihentikan oleh Boss`.

Screenshots:

- `changelogs/assets/p6-390-approval.png`
- `changelogs/assets/p6-390-receipt.png`
- `changelogs/assets/p6-390-denied.png`
- `changelogs/assets/p6-390-expired.png`
- `changelogs/assets/p6-390-stopped.png`
- `changelogs/assets/p6-1440-approval.png`
- `changelogs/assets/p6-1440-receipt.png`

## Exact sequence that proves once / deny / stop

Run against the throwaway stack used by `scripts/p6-acceptance.sh` (`BASE=http://127.0.0.1:3307`,
`DATA_DIR` is that irisd's `IRIS_DATA_DIR`, real Hermes on `:8642`).

```bash
BASE=http://127.0.0.1:3307
COOKIE=/tmp/p6-cookie
DATA_DIR=/tmp/iris-p6-xxxx/data

curl -sS -c "$COOKIE" -X POST "$BASE/api/auth/login" \
  -H 'Content-Type: application/json' -d '{"passphrase":"…"}'

new_room() {
  curl -sS -b "$COOKIE" -X POST "$BASE/api/rooms" -H 'Content-Type: application/json' \
    -d "$(jq -nc --arg n "$1" '{name:$n,topic:"P6 proof"}')" | jq -r '.room.id'
}
write_prompt() {
  local room="$1" name="$2"
  printf 'Pakai tool write_file untuk menulis tepat ke %s/rooms/%s/outbox/%s dengan isi: p6 proof. Jangan pakai terminal. Setelah selesai jawab: P6-SELESAI.' \
    "$DATA_DIR" "$room" "$name"
}
turn() { # room prompt outfile
  curl -sS -b "$COOKIE" -N --max-time 180 -X POST "$BASE/api/rooms/$1/messages" \
    -H 'Content-Type: application/json' -d "$(jq -nc --arg t "$2" '{text:$t}')" >"$3" &
}

# ---------- ONCE ----------
ROOM=$(new_room 'P6 once')
turn "$ROOM" "$(write_prompt "$ROOM" AGENTS.md)" /tmp/p6-once.sse
for i in $(seq 1 480); do grep -q '^event: approval' /tmp/p6-once.sse && break; sleep 0.25; done
AID=$(grep -A1 '^event: approval' /tmp/p6-once.sse | sed -n 's/^data: //p' | jq -r '.id')
jq -r '.choices' <<<"$(grep -A1 '^event: approval' /tmp/p6-once.sse | sed -n 's/^data: //p' | head -1)"
# ["once","deny"] — Hermes did not offer a wider scope
curl -sS -b "$COOKIE" -X POST "$BASE/api/rooms/$ROOM/approvals/$AID" \
  -H 'Content-Type: application/json' -d '{"choice":"once"}'
# {"approval":{"state":"allowed","choice":"once",…}}
# the file exists, the turn ends with STATUS: DONE

# ---------- DENY ----------
ROOM2=$(new_room 'P6 deny')
turn "$ROOM2" "$(write_prompt "$ROOM2" CLAUDE.md)" /tmp/p6-deny.sse
for i in $(seq 1 480); do grep -q '^event: approval' /tmp/p6-deny.sse && break; sleep 0.25; done
AID2=$(grep -A1 '^event: approval' /tmp/p6-deny.sse | sed -n 's/^data: //p' | jq -r '.id')
curl -sS -b "$COOKIE" -X POST "$BASE/api/rooms/$ROOM2/approvals/$AID2" \
  -H 'Content-Type: application/json' -d '{"choice":"deny"}'
# {"approval":{"state":"denied","choice":"deny",…}}
# CLAUDE.md does not exist; the reply says STATUS: BLOCKED; no continuation

# ---------- STOP ----------
ROOM3=$(new_room 'P6 stop')
turn "$ROOM3" \
  'Jalankan dengan tool terminal(background=true, notify=true): sleep 120; echo P6-STOP-DONE. Lalu tunggu proses itu dengan process_manage wait sampai selesai. Jangan akhiri turn sebelum prosesnya selesai.' \
  /tmp/p6-stop.sse
for i in $(seq 1 480); do grep -q 'process_manage' /tmp/p6-stop.sse && break; sleep 0.25; done
curl -sS -b "$COOKIE" -X POST "$BASE/api/rooms/$ROOM3/stop"
# {"stopped":1,"reason":"stopping"}
curl -sS -b "$COOKIE" -X POST "$BASE/api/rooms/$ROOM3/stop"
# {"stopped":0,"reason":"tidak ada yang berjalan"}
# runs.status='cancelled', one system line 'dihentikan oleh Boss', 0 continuations
```

`scripts/p6-acceptance.sh` wraps exactly this, then queries `runs`, `approvals`, `messages` and
`continuations` to assert the result rather than trusting the HTTP body alone.

## What is still not verified

- **Real phone hardware / HTTPS** — the browser checks are Chromium at 390×844 and 1440×900. The P4
  phone-over-HTTPS item remains open; nothing in this phase changes that.
- **A real four-choice approval** — the deterministic real gate offers `once`/`deny` only. The four-choice
  card, `Selalu izinkan`, `Sesi ini`, `Enter` and the expiry UI were exercised with fixture approvals through
  the same room SSE path. The four-choice payload itself is the captured real event above.
- **Multi-tab push** — the hub fans out to active room streams; a tab that has reloaded and has no turn
  stream falls back to the 4 s `GET /approvals` refresh. There is no standalone room-events endpoint yet.
- **`request_id`-less approvals** — the pinned build always sends one (the capture proves it). irisd logs
  that frame's absence and does not show an unanswerable card rather than guessing an id.
- **`approvals.timeout` drift** — Iris mirrors the value read at boot (`IRIS_APPROVAL_TIMEOUT`, the
  acceptance reads `hermes config get approvals.timeout`). Changing the gateway config while irisd is
  running would need a restart to re-align.
- **Stop confirmation >60 s** — the threshold is unit-tested (`needsStopConfirm`, 59 s vs 61 s) and the
  browser proves the young-run no-confirm path; a 60+ second browser wait was not added.
- **Device clock** — the countdown and the client-side "expired" appearance use the phone's clock; the
  server's `expires_at` is authoritative, so a wrong phone clock can make the card look expired early but
  cannot make an expired approval succeed.

## Files

- `db/migrations/000008_approvals.{up,down}.sql`, `db/queries/{runs,approvals}.sql`, generated `internal/store/*`
- `internal/hermes/{client,sse,approval}.go`, `internal/hermes/testdata/approval-request.json`
- `internal/httpapi/{approvals,events,stop,turn,messages,rooms,server}.go`, `internal/config/config.go`
- `web/src/components/{ApprovalCard,Composer,RunTimeline}.vue`, `web/src/views/{RoomView,RoomsView}.vue`,
  `web/src/components/{RoomList,MessageBubble}.vue`, `web/src/stores/room.ts`, `web/src/lib/{api,types,merge,timeline}.ts`
- `scripts/p6-acceptance.sh`, `scripts/p6-browser.sh`, `scripts/browser-p6.mjs`
- Tests: `internal/hermes/approval_test.go`, `internal/httpapi/p6_test.go`, web `timeline.test.ts`,
  `stores/room.test.ts`

## Verified by the operator

**P6 acceptance re-run by me (real Hermes, throwaway `iris_p6_mine`, irisd :3307):**

```
a  approval appeared +5 068 ms (tool=write_file, choices=[once,deny]) → once → done 4 074 ms, run completed
b  deny → done 5 346 ms, no retry, run ended
c  run reached process_manage +12 402 ms → stop → done 1 053 ms, status cancelled, NO continuation in 25 s
d  nobody answered → expired after 311 113 ms, pending_approvals=0, run completed
   ALL P6 SCENARIOS PASSED
88 go tests, 12 packages
```

**Live instance, through the public HTTPS URL, migrations applied (schema 8):**

```
a real approval landed on production: tool=write_file, command "<write to AGENTS.md>" (redacted by Hermes),
  choices=[once,deny], state=pending
deny → HTTP 200, state=denied choice=deny, room shows Hermes' own explanation
the same approval answered twice → HTTP 409 (an answered approval is closed)
units restarted cleanly, local 200, public 200
```

## My own mistake, recorded so it is not repeated

My first live smoke script queried a `tool` column that does not exist (it is `tool_name`), so it never saw the
pending approval and left a real run parked on production. The product was correct the whole time; the probe
was wrong. Fixed lesson: read the table before writing a probe against it — the same rule the rest of this
project follows.
