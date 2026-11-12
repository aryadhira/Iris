# 2026-11-12 — P3: continuity (wait in-turn, a bounded pump, and a visible run timeline)

A job that keeps running must report itself. Boss must never have to nudge.

## Why this exists (measured, not re-litigated)

Hermes does not wake an API session when a background job finishes in this build: `api_server*.py` never
sets `HERMES_SESSION_PLATFORM`, so `run_notifications._raw_process_event_session_id` finds no route and drops
the completion. The earlier probe heard nothing for 150 s and 300 s after jobs of 45 s and 70 s. ADR-003:
Hermes stays stock; continuity is Iris' job.

P3 builds three layers:

1. the room contract asks Hermes to wait in the same turn whenever it can,
2. a bounded continuation pump re-prompts a turn that deliberately left work running,
3. the room shows what happened (tools, countdown, cancel, state chip) instead of a black box.

## One measured correction to the brief

On this build (`v0.21.6+636`) a background `terminal` call **does** produce `proc_…`, but the SSE frame
`tool.completed` is `{"tool_name":"terminal","preview":null,"args":null,…}` — the id is not in that frame.
It appears in the tool result carried by `run.completed` and in the assistant's assembled text
(`session_id: proc_…`). This is why extraction scans the raw tool-event frame, the `done` frame and the
assembled reply; the 90 s run below stored `procs: ["proc_f5913489c703"]` from the reply. The rest of the
brief's facts held: no completion wake-up, the id is the only handle, and only `process_manage` can check it.

## A. Wait in-turn (primary contract)

`internal/rooms/folder.go` now owns `ContinuityPrompt`, four short Indonesian lines appended to the room
contract:

```
Kalau kamu memulai pekerjaan panjang, utamakan menunggu di turn ini dengan process_manage wait lalu laporkan hasilnya — turn yang lama tidak apa-apa.
Kalau kamu sengaja membiarkannya berjalan, sebutkan persis apa yang berjalan dan kapan selesainya.
Akhiri balasan dengan satu baris: STATUS: RUNNING — <apa dan berapa lama>, STATUS: DONE — <hasil>, atau STATUS: BLOCKED — <yang kamu butuhkan>.
Jangan pernah mengaku selesai selama pekerjaan yang kamu mulai masih berjalan.
```

The contract is prepended to every turn as per-request `instructions` (ADR-006). Rooms created before P3
store the old seven-line prompt, so `rooms.TurnInstructions(stored)` appends the four lines when
`STATUS: RUNNING` is absent; new rooms already contain them and are not duplicated. Tested in
`internal/rooms/folder_test.go`.

## B. Bounded continuation pump

### What a turn records

During the stream, irisd collects:

- every `proc_…` id from tool events and from the assembled reply (deduplicated, first-seen order),
- every visible tool call with its real duration (`tool.started`/`tool.completed` `ts` from the gateway;
  internal tools whose names start with `_`, such as `_thinking`, never become a row),
- the last `STATUS: RUNNING|DONE|BLOCKED` line.

It is stored as `messages.meta jsonb` (migration 000006):

```json
{
  "procs": ["proc_f5913489c703"],
  "tools": [{"name": "terminal", "status": "completed", "durationMs": 74}],
  "statusLine": "STATUS: RUNNING — menunggu proses 90 detik."
}
```

### When it arms

After a clean turn, `continuation.Decide` is the only decision point. The counters are read from
`continuations` rows, never from memory. Precedence:

| Condition | Result |
|---|---|
| room archived | cancel, visible reason |
| newer user message in the room | cancel, visible reason |
| `STATUS: DONE` / `STATUS: BLOCKED` | never continues |
| this is a continuation and it used zero visible tools | stop, visible reason |
| no `STATUS: RUNNING` and no `proc_…` id | normal turn, no pump |
| already 3 continuations in this chain | cap, visible reason |
| now ≥ first turn + 30 min | cap, visible reason |
| otherwise | schedule attempt `n+1`, due in 20 s |

`continuations` (migration 000006) stores `chain_id`, `attempt`, `status`, `reason`, `due_at`, `deadline`,
`started_at`, `finished_at`, with a unique `(chain_id, attempt)` index. A restart or a duplicate scheduler
cannot double an attempt.

### What a continuation does

1. writes a system line in the room: `lanjut otomatis (1/3) — menunggu proses yang kamu jalankan`,
2. sends Hermes: check with `process_manage`; report now if finished, wait and report if still running, say
   plainly if it cannot be found; never invent progress,
3. persists the reply with its own meta and schedules the next attempt if the answer still says RUNNING.

Cancellation happens immediately when Boss types (the pending row is cancelled before the new turn streams),
when the room is archived, or when Boss presses **hentikan**. Every scheduled/claimed/skipped/capped/cancelled
decision is logged with its reason (`continuation scheduled`, `continuations claimed`, `continuation
stopped`, `continuation cancelled`, `continuation chains resumed after restart`), and skip/cap/cancel reasons
are also written into the room as system messages.

On boot, `ResumeRunningContinuations` turns rows that were `running` at the crash back into `pending`, then
the scheduler claims everything that is due. A chain inside its window is never silently dropped.

### One subtle race fixed

If Boss types *while* a continuation is inside its Hermes turn, the row is already `cancelled`. When the
turn returns, `processContinuation` re-reads the row and leaves the cancellation untouched instead of
overwriting it with `done` or scheduling attempt `n+1`. Tested in
`TestCancellationDuringARunningContinuationIsNotOverwritten`.

## C. Run timeline (the room is not a black box)

- Every assistant message with tools shows a collapsed **“apa yang Hermes lakukan”** disclosure: tool name,
  duration (`750 ms`, `1.3 dtk`, `1 mnt 30 dtk`), failed/running state, stored order (newest last). A turn
  with no tools renders nothing.
- A pending continuation is a live line: `lanjut otomatis dalam 12 detik`, `percobaan 1/3`, and a
  **hentikan** button. The line ticks locally; the room polls the pending row every 4 s so another tab or
  the pump itself cannot leave it stale.
- The room header has a small chip: `idle` / `bekerja` / `menunggu proses` / `butuh kamu` (the last one when
  the newest assistant reply ended `STATUS: BLOCKED`).

API: `GET /api/rooms/{id}/continuation`, `POST /api/rooms/{id}/continuation/cancel`.

## Tests

```
$ go test ./... -count=1
ok  internal/artifacts
ok  internal/continuation
ok  internal/files
ok  internal/hermes
ok  internal/httpapi
ok  internal/rooms
41 tests passed, 0 failures (verbose `--- PASS` count)

$ go vet ./...
(clean)

$ go test -race ./internal/httpapi ./internal/continuation -count=1
ok  internal/httpapi
ok  internal/continuation

$ cd web && npx vue-tsc --noEmit
(clean)
$ npx vitest run
Test Files  7 passed (7)
     Tests  39 passed (39)
$ npm run build
✓ built in 560ms
```

Go coverage of the requested cases:

- proc ids from tool events, from text, both (deduplicated) and none — `TestExtractProcsFromEventsTextBothAndNone`
- 3 continuations then a capped fourth — `TestContinuationStopsAfterThree`
- 30 minute cap with a fake clock — `TestContinuationCapsAtThirtyMinutesWithFakeClock`
- cancel on a newer user message — `TestContinuationCancelsOnANewerUserMessage`
- cancel on archived — `TestContinuationCancelsWhenTheRoomIsArchived`
- restart resumes a pending/running chain — `TestRestartResumesAPendingChain`
- `STATUS: DONE` never continues, `STATUS: RUNNING` does — `TestContinuationSchedulesOnRunningButNeverOnDone`
- stream → meta row → scheduled continuation — `TestStreamStoresProcMetaAndSchedulesContinuation`
- endpoints expose and cancel the row — `TestContinuationEndpointsExposeAndCancel`
- cancellation during a run is not overwritten — `TestCancellationDuringARunningContinuationIsNotOverwritten`

Web coverage:

- tools render in stored order, newest last; empty meta renders nothing — `web/src/lib/timeline.test.ts`
- countdown copy (`lanjut otomatis dalam 12 detik`, `menyiapkan lanjutan…`) — same file
- the chip never lets a running chain look idle — same file
- **hentikan** calls the cancel API and clears the line — `web/src/stores/room.test.ts`

## Real end-to-end (real Hermes, throwaway DB `iris_p3_accept`, data dir in a temp dir)

`scripts/p3-acceptance.sh` created a throwaway database, migrated it to version 6, started `irisd` on 3301
(never 3300), created rooms through the real API, and asked for `sleep 90; echo IRIS-DONE` with an explicit
“do not wait in this turn” instruction. Table times are host local time (WIB, +07:00); the raw DB rows
printed below come from the container's UTC clock (the same instant, seven hours behind).

### Case 1 — nobody types

| Event | Time |
|---|---|
| user turn posted | `09:04:55` |
| first assistant reply stored (`STATUS: RUNNING — menunggu proses 90 detik.`) | `09:05:08.645` |
| continuation system line `lanjut otomatis (1/3)` | `09:05:28.726` (20.1 s after the reply) |
| continuation reply stored: `IRIS-DONE`, `STATUS: DONE — proses 90 detik selesai, exit code 0` | `09:06:39.606` |

The room timeline, read from the throwaway DB:

```
02:04:55.052|user|Jalankan pekerjaan latar … sleep 90; echo IRIS-DONE …
02:05:08.645|assistant|Proses latar sudah jalan, Boss — id sesi proc_15c56c316280, PID 3742705 …
02:05:28.726|system|lanjut otomatis (1/3) — menunggu proses yang kamu jalankan
02:06:39.606|assistant|Sudah selesai, Boss — proses proc_15c56c316280 keluar dengan exit code 0 … / STATUS: DONE — proses 90 detik selesai, exit code 0, output "IRIS-DONE".
```

Continuation row: `1|done|lanjutan selesai|due 02:05:28.648|finished 02:06:39.608`.
Stored meta on the continuation reply:

```json
{"procs":["proc_15c56c31628","proc_15c56c316280","proc_4dae56ca81f6"],
 "tools":[{"name":"tool_describe","status":"completed","durationMs":5},
          {"name":"process_manage","status":"completed","durationMs":56242}],
 "statusLine":"STATUS: DONE — proses 90 detik selesai, exit code 0, output \"IRIS-DONE\"."}
```

The 56,242 ms `process_manage` row is the real wait, not a fabricated duration. Nobody typed between
`09:05:08` and `09:06:39`.

### Case 2 — Boss types mid-wait

| Event | Time |
|---|---|
| first assistant reply stored (`STATUS: RUNNING`) | `09:06:51.269` |
| continuation row pending (due at `09:07:11.271`) | `09:06:51.334` |
| Boss message stored | `09:06:51.344` |
| cancellation line `lanjut otomatis dibatalkan — ada pesan baru dari Boss.` | `09:06:51.347` |
| re-checked after the due time (30 s later) | `09:07:34.743` |

Continuation row: `1|cancelled|ada pesan baru dari Boss`, finished at `09:06:51.345`. No
`lanjut otomatis (1/3)` line ever appeared, and no later assistant message reported `IRIS-DONE`. The Boss
message (“2+2 berapa?”) got its own answer at `09:07:04.674`, with `STATUS: DONE — dijawab.` — the new
conversation was not delayed by the dead chain.

### Browser check (real Chromium, production build, throwaway stack)

`scripts/p3-browser.sh` at 390×844 and 1440×900:

```
[PASS] room created at 390x844
[INFO] chip while streaming: bekerja
[PASS] live line with countdown at 390: lanjut otomatis dalam 20 detik
       menunggu proses yang dijalankan · percobaan 1/3   hentikan
[PASS] timeline: ["Tool describe\n6 ms", "Mengelola proses\n4 ms"]
[PASS] 1440x900 layout has no horizontal overflow
[PASS] hentikan cancelled the chain and left a visible reason
[PASS] no continuation ran after cancellation
```

Screenshots (in `changelogs/assets/`):

- `p3-390-live-continuation.png` — header chip **menunggu proses**, the RUNNING reply, the live countdown
  line and **hentikan** at 390×844.
- `p3-390-timeline.png` — the follow-up reply with `STATUS: DONE` and the expanded
  “apa yang Hermes lakukan” rows.
- `p3-1440-timeline.png` — the same room at 1440×900; no overflow, list and composer aligned.
- `p3-390-cancel-before.png` — the pending line before cancellation.
- `p3-390-cancelled.png` — after **hentikan**: the line is gone and the room shows
  `lanjut otomatis dibatalkan — kamu menghentikannya.`

Screenshot findings: the countdown and cancel button fit at 390 without pushing the composer; the state chip
is readable next to the room name at both widths; the tool disclosure is quiet enough to ignore and expands
to two real rows (no placeholder row for an empty turn); the system lines are centered and visibly distinct
from Hermes and Boss.

## What is still missing / limits

- **The pump is armed by text the agent chose to write.** If Hermes detaches a job but omits both the literal
  `proc_…` id and `STATUS: RUNNING`, irisd has nothing to detect and will not schedule a follow-up. That is
  the strongest remaining path to silence; the contract reduces it but cannot eliminate it while Hermes is
  stock.
- A `STATUS: DONE` claim is taken at face value and stops the chain even if a process is still alive. The
  contract forbids lying; irisd cannot verify the agent's claim because there is no Hermes API for process
  liveness.
- A continuation whose own Hermes turn stalls hits the 90 s idle watchdog. It writes
  `lanjut otomatis gagal — Hermes tidak menjawab. Coba tanya lagi.` and stops; it does not burn another
  attempt in a loop.
- If `irisd` dies while a continuation's Hermes turn is running, that turn's output is not persisted; the row
  is reset to `pending` on boot and the chain asks again. The job itself is safe in Hermes, but the first
  answer after the crash is lost.
- The UI shows the first pending continuation only (one chain per room is the normal state; a new Boss message
  cancels any previous chain). Deadline/attempt history is in the DB and logs, not in a separate panel.
- Polling is every 4 s, not a push channel.

## Files touched

- Schema: `db/migrations/000006_continuity.{up,down}.sql`, `db/queries/{messages,continuations}.sql`,
  regenerated `internal/store/*`.
- Backend: `internal/continuation/`, `internal/httpapi/{turn,continuation,messages,rooms,server}.go`,
  `internal/rooms/folder.go`, `internal/hermes/{client,sse}.go`, `cmd/irisd/main.go`.
- Web: `web/src/lib/{types,merge,api,timeline}.ts`, `web/src/stores/room.ts`,
  `web/src/components/{RunTimeline,ContinuationLine,MessageBubble}.vue`, `web/src/views/RoomView.vue`.
- Tests/acceptance: `internal/continuation/continuation_test.go`,
  `internal/httpapi/continuation_test.go`, `internal/rooms/folder_test.go`,
  `web/src/lib/timeline.test.ts`, `web/src/stores/room.test.ts`,
  `scripts/p3-acceptance.sh`, `scripts/p3-browser.sh`, `scripts/browser-p3.mjs`.
- Docs: `ARCHITECTURE.md` continuity section, `PROJECT_PLAN.md` P3 marked ✅.

No commit, no push; the tree is left dirty. Hermes was not patched; ports 3300/8080/4173/8642 and the live
`lab`/`iris` databases were never used by the checks.

## Verified by the operator (independent probes, real Hermes)

**The pump fires with nobody typing.** A turn that ends `STATUS: RUNNING` with `sleep 90; echo IRIS-DONE`:

```
02:13:07  user       Jalankan proses background: sleep 90 …
02:13:25  assistant  Sudah jalan di background — session proc_1662fac1c130 (PID 3755576) …
02:13:46  system     lanjut otomatis (1/3) — menunggu proses yang kamu jalankan
          → the room later received the job output (IRIS-DONE) without a nudge
          → chain stayed within the cap (1 continuation note)
```

The continuation was created **+39 s** after the user message, with the human speaking nothing in between.

**Typing mid-wait cancels it, visibly.**

```
02:17:50  assistant  Sudah jalan di background — PID 3760895, session proc_075c35673e1c, sekitar 120 detik …
02:17:50  log        continuation scheduled   reason="turn meninggalkan proses yang masih berjalan"
02:17:56  user       stop, ganti rencana
02:17:56  system     lanjut otomatis dibatalkan — ada pesan baru dari Boss.
02:17:56  log        continuation cancelled    reason="ada pesan baru dari Boss"
02:18:34  assistant  Sudah distop, Boss. Proses background tadi saya kill satu process group …
```

**`STATUS: DONE` never arms it:** notes before 1, after 1, created after the DONE turn **0**.

## Two bugs in the operator's own referee, found and fixed

1. The continuation counter counted `like '%lanjut otomatis%'`, which also matches the **cancellation**
   note (`lanjut otomatis dibatalkan`) — so a correct cancellation looked like a fired continuation. The
   referee now matches `'lanjut otomatis (%'`.
2. The first run compared note totals before/after a DONE turn without isolating timestamps, so an
   in-flight chain from the previous step looked like the DONE turn arming the pump. The second probe
   measured `created_at > <DONE turn timestamp>` and found zero.

Both were measurement faults, not product faults — and both would have produced a false negative on a
correct product, which is exactly why the probe was rerun instead of reporting "FAIL" as the truth.

## Still missing

- **Armed only by text.** If Hermes detaches a job and writes neither `proc_…` nor `STATUS: RUNNING`, the pump
  stays idle. The contract asks for it, but nothing forces it.
- `tool.completed` on this build does not carry the process id even though the brief assumed it; the id is
  read from the assistant text instead.
- No approvals yet ("butuh kamu" only reflects failures and waiting chains).
