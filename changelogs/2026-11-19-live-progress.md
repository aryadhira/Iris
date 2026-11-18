# P12 — show the work, live

**What changed:** a running turn now exposes what Hermes is actually doing —
thinking blocks, tools with names/status/durations, and a heartbeat that says
how long the stream has been silent. The room snapshot carries the in-flight
steps, so a phone returning from the background sees the same list as a late
laptop, and the panel folds into the stored message timeline once the turn ends.

## Captured frames (not guessed)

Captured from the real gateway on 2026-11-19 with a throwaway session
(`api_1795015658_90c4f0e6`, deleted afterwards). Raw: `/tmp/p12-frames.jsonl`
(42 frames); deterministic copy:
`internal/hermes/testdata/p12-frames.jsonl`.

| event | count | what it carried |
|---|---:|---|
| `run.started` | 1 | `run_id`, `session_id`, `seq`, `ts`, runtime request |
| `message.started` | 1 | message id, role |
| `tool.started` | 2 | `tool_name` (`terminal`, `read_file`), `preview`, `args` |
| `tool.completed` | 2 | `tool_name`; `preview`/`args` were null |
| `assistant.delta` | 32 | reply text |
| `tool.progress` | 1 | `tool_name: "_thinking"`, `delta` (the completed thinking block) |
| `assistant.completed` | 1 | final `content`, runtime (`opencode-go` / `deepseek-v4.1-flash`) |
| `run.completed` | 1 | transcript `messages[]`, including `reasoning` + `reasoning_content` |
| `done` | 1 | bare sentinel; no body |

The throwaway turn ran a shell command that wrote `/tmp/p12-capture-out.txt`
and read it back. `/tmp/p12-frames.jsonl` is the exact stream.

### What Hermes actually emits for “thinking”

`/v1/capabilities` advertises `reasoning_streaming: true`, but the **session
stream used by irisd has no token-level reasoning deltas**. Two real signals
exist:

1. `tool.progress` with `tool_name = "_thinking"` and a `delta` of up to 500
   characters — a *completed* thinking block between tool calls.
2. `run.completed.messages[].reasoning` / `reasoning_content` — the private
   working text, sent once at the end of the turn.

`normalize()` maps (1) to a new `thinking` kind. It is never appended to
`assistant.delta`, so it cannot reach the reply body. (2) is stored only as the
capped private excerpt on `meta.reasoning`. Both are de-duplicated: a 500-char
`_thinking` block followed by the full terminal reasoning shares one blob.

### What Hermes does **not** emit on this surface

- No `plan`/step events. A `todo` tool call, if the model makes one, is a
  **tool** step; Iris never invents a plan row.
- No progress percentage or partial output for a tool. Only start/completion.
- `tool.completed` carries `preview: null`; a failure reason is often absent
  from `tool.failed`. A failed step shows the short detail that exists, or
  “gagal” when the gateway sent no reason.
- No dedicated approval-resolved event on the chat stream; a parked approval is
  a real `status` step (`menunggu izin`) that closes when work resumes.
- `assistant.commentary` exists in the gateway code for commentary-capable
  providers, but this model/turn did not emit it. It remains `unknown` rather
  than being guessed into a step.

## Step model

Persisted on the assistant message in `meta.steps`, oldest first:

```json
{
  "seq": 1,
  "kind": "thinking | tool | plan | status",
  "name": "terminal",
  "detail": "printf 'p12 ok' + 1 command",
  "status": "running | ok | failed",
  "startedAt": "2026-11-19T...Z",
  "endedAt": "2026-11-19T...Z",
  "durationMs": 1750
}
```

- Durations use the gateway's `ts` when present, irisd's clock otherwise.
- A completion without a start is stored as a real completion with `durationMs
  = 0`, never an invented duration.
- An open step at turn end is closed with a real duration but keeps `running`:
  Iris knows the turn ended, not that the command succeeded.
- Tool `detail` comes from `preview` only, never raw `args`, and is collapsed to
  one line, truncated to 120 chars, and redacted (bearer/sk/API-key/password
  shapes). The raw args object is never persisted or sent to a browser.
- `meta.reasoning` is one blob, capped at 4096 bytes. It is **not** in `body`:
  search indexes `body`, export writes `body`, and speech is given `body`.

## Live protocol

- Hub event `step` — the full step payload, added or updated by `seq`.
- Hub event `activity` — `{ "ageMs": n }`, the age of the last *real* event. It
  is written every 5 s per attached stream while a turn is live; publishing the
  heartbeat never resets its own age.
- Snapshot adds `steps: [...]` and `last_event_age_ms`. The snapshot is taken
  under the hub lock that stores steps, so a step appears in the snapshot or as
  a frame, never both.
- `BeginTurn` replaces the old start-of-turn `Reset`, so an in-flight list
  cannot leak into the next turn; `Reset` at `done` clears it so history is read
  from the stored message.

## Panel behaviour

`TurnProgress.vue`, between the transcript and the composer (not in the bubble):

- header `Hermes bekerja · 0:42` plus the current step name; the clock ticks
  once a second;
- one compact row per step, newest last: kind icon (brain / wrench / checklist /
  info), spinner while running, tick or cross when done, and a compact duration
  (`7.6s`, `750ms`, `1m 30s`);
- thinking renders as `berpikir…` plus the first ~80 characters, dimmed and
  truncated to one line;
- failed steps are red and show their short reason;
- after 20 s with no real event: `menunggu Hermes… 20s`;
- after 2 minutes the composer shows `kamu bisa hentikan` next to Stop. Nothing
  auto-stops; the 10-minute idle watchdog still owns failure (unchanged);
- at 390 px every row is one line, the label truncates, the duration stays, and
  there is no horizontal scroll;
- at `done` the panel disappears and `RunTimeline` renders the persisted
  `meta.steps`. The panel and the message never show two lists at once.

## Measured evidence

`scripts/p12-acceptance.sh` (DB `iris_p12_mine`, irisd `:3314`, real Hermes):

```
steps=1:tool:1397ms,2:tool:1801ms,3:tool:4ms,4:thinking:15ms
P12 PASSED: 4 steps (8 frames), first kind=tool, done after 17972 ms
turn|steps=4|frames=8|meta_steps=4|done_ms=17972|subscribers=identical
```

Every step frame arrived before `event: done`, two subscribers saw an identical
sequence, and all four persisted steps had a non-zero duration.

`scripts/p12-browser.sh` (throwaway DB `iris_p12_browser`, irisd `:3315`,
Chromium 390×844):

- mid-turn panel: `Hermes bekerja · 0:14`, 2 step rows
  (`Menjalankan perintah 5.9s`, `Membaca berkas 4ms`), no row overflow;
- after completion: panel gone, stored timeline has 3 rows
  (`tool, tool, thinking`), and the mid-turn kinds are a prefix of the stored
  kinds — no duplicate list.
- Screenshots: `changelogs/assets/p12-390-mid-turn.png`,
  `changelogs/assets/p12-390-finished.png`.

Go tests: the captured 42-frame fixture parses into the expected kinds;
reasoning never reaches `body`, export, search or the TTS input; steps persist
in order with durations; the snapshot carries in-flight steps; two late
subscribers get the same list; the activity heartbeat never resets its age.

## Still missing

- No token-level reasoning stream: “thinking” appears when Hermes completes a
  block (`_thinking`) or at the end (`run.completed`). While the model is
  actively generating, the panel shows the last tool/step plus the ticking
  clock, then `menunggu Hermes…` after 20 s.
- No plan events, no per-tool progress percentage, and usually no failure
  reason on `tool.failed`.
- Step list is capped only by the turn, not by count; a pathological turn with
  thousands of tool calls would store them all. The UI scrolls inside the card.

## Verified by the operator, live

Watching a real turn with the room open in a browser while the message was sent through the API:

```
+2s   Hermes bekerja · 0:09
+6s   Hermes bekerja · 0:13   step: "Menjalankan perintah · 92ms"
+8s   Hermes bekerja · 0:15
+10s  panel gone — the turn folded into the message timeline
```

Boss's exact complaint ("Iris got timeout and the room said idle") is now visible in the opposite direction: while
work happens, the panel counts; when it ends, it folds into history. Between those two states there is no longer a
silent room that could mean either.

## What Hermes does NOT emit, stated plainly

`/v1/capabilities` advertises `reasoning_streaming: true`, but the session stream irisd consumes has **no
token-level reasoning deltas**. Reasoning reaches Iris in two places only: a `tool.progress` event whose
`tool_name` is `_thinking` (delta up to 500 chars), and the final transcript in `run.completed`. So the panel shows
"berpikir…" when Hermes flushes a thinking block, not as a live token stream. If Boss wants true token-level
thinking, that is a Hermes-side capability, not something tubing can invent.

Captured frames from a real turn: `run.started`, `message.started`, `tool.started` ×2 (terminal, read_file),
`tool.completed` ×2, `assistant.delta` ×32, `tool.progress` ×1 (`_thinking`), `assistant.completed`, `run.completed`,
`done`.
