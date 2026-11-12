# P8 — cost meter, budget guard, streaming speech, retry

**Date:** 2026-11-13
**Status:** shipped; tree left dirty (no commit)
**Hermes:** stock `v0.21.6+636` (workspace `~/.hermes/installs/ba9ff03a91df87ff/environments/635c12c571ef4f0eb0b4d0f906f45b63/workspace`)

## What changed

1. **Cost meter.** One `usage` row per assistant turn, captured from the terminal Hermes payload.
   `scripts/seed-prices.py` writes an editable `~/.config/iris/prices.json` from Hermes' own price
   snapshot. `GET /api/usage` returns totals, a Jakarta-day series and a global per-room breakdown.
   The room inspector gained a today chip at 390 px and a 14-day bar chart.
2. **Budget guard.** `IRIS_DAILY_BUDGET_USD` (empty = off) refuses a new user message before anything
   is written, with HTTP 429 and a visible Indonesian system line. `lanjut` writes one
   `budget_overrides` row and lets exactly one turn through.
3. **Streaming speech.** `POST /speak/stream` splits the summary into sentences and emits each MP3 as a
   newline-delimited JSON line as soon as it exists. irisd proxies without buffering; the browser plays
   a serial queue and flips to "membaca…" on the first chunk.
4. **Retry.** `POST /api/rooms/{id}/messages/{mid}/retry` reuses the same user row, marks the previous
   assistant reply `superseded` (dimmed, labelled, never deleted), and streams a fresh answer. 409 while
   the room is busy. UI: "Coba lagi" on a failed/empty reply and in every user message menu.

## The captured terminal payload (real, 2026-11-12)

Request: a throwaway session (`api_1794472917_53c0e61d`) asking `Jawab satu kata: halo`. This is the
verbatim `run.completed` frame from the live gateway, not a fixture:

```json
{
  "session_id": "api_1794472917_53c0e61d",
  "message_id": "msg_6809a8203c694af18f8ae42dc1d9ed34",
  "completed": true,
  "partial": false,
  "interrupted": false,
  "messages": [
    {
      "role": "assistant",
      "content": "Halo, Boss.",
      "timestamp": 1794472919.771412,
      "finish_reason": "stop",
      "reasoning": null
    }
  ],
  "usage": {
    "input_tokens": 16619,
    "output_tokens": 6,
    "total_tokens": 16625,
    "runtime": {
      "provider": "opencode-go",
      "model": "deepseek-v4.1-flash",
      "route_source": "global",
      "requested": {"provider": "", "model": ""}
    }
  },
  "runtime": {
    "provider": "opencode-go",
    "model": "deepseek-v4.1-flash",
    "route_source": "global",
    "requested": {"provider": "", "model": ""}
  },
  "run_id": "run_0ccc7b5966e84c509a5aac9b27757aef",
  "seq": 8,
  "ts": 1794472919.8106072
}
```

What this proves against the brief:

- `usage` is real and non-zero; `runtime` names the served pair (`opencode-go` / `deepseek-v4.1-flash`).
- **This build reports only three of the five counters.** `cache_read_tokens` and
  `cache_write_tokens` are absent, and `runtime` is nested *inside* `usage` as well as at the top level.
  The parser reads both shapes; the row stores the missing cache counters as `0` with
  `cache_reported=false`. The cost is therefore an upper bound, and the changelog says so rather than
  pretending the cache was free. A payload with no usage object at all is stored with zeros and
  `usage_reported=false`.
- The five-counter `/v1/runs` shape is also parsed and tested, so a future gateway change is a parser
  test, not a rewrite.

## Price source (no invented numbers)

`agent/usage_pricing.py` in the Hermes workspace, snapshot `deepseek-pricing-2026-09-10`:
`https://api-docs.deepseek.com/quick_start/pricing`.

| Model | input | output | cache_read | cache_write |
|---|---:|---:|---:|---:|
| `deepseek-flash`, `deepseek-v4-flash`, `deepseek-chat`, `deepseek-reasoner` | 0.15 | 0.60 | 0.003 | — (not published) |
| `deepseek-v4-pro` | 0.66 | 1.98 | 0.022 | — (not published) |

USD per 1M tokens. The live served id is `deepseek-v4.1-flash`, which is not a literal key in that
table. The table's own comment says: *"`deepseek-v4-flash` and the retired aliases are served by
V4.1-Flash at the Flash price."* `seed-prices.py` adds that alias with the table's Flash row and writes
the reasoning into the `source` string. This is the table's documented model identity, not a new rate.
The OpenCode Go catalogue independently lists the same `0.15 / 0.60 / 0.003` for the id, but the seed
script does not use that catalogue.

`~/.config/iris/prices.json` was written for real during this work: **162 entries, mode 0600**, 13
tiered rows skipped because the flat file cannot express "a different rate above N tokens" honestly.
A provider-qualified key (`provider/model`) wins over a bare id. Missing model → `cost_usd = NULL`; the
UI shows tokens and "harga model ini belum ada". A null rate with reported tokens also yields `NULL`,
never zero.

## Measured acceptance (real Hermes, throwaway DB `iris_p8_mine`, irisd `:3309`, voice `:3311`)

`scripts/p8-acceptance.sh` — final run, exit 0.

| Scenario | Result | Real number |
|---|---|---|
| a. real turn metered | PASS | turn 8,121 ms; `opencode-go/deepseek-v4.1-flash`; 34,038 in + 283 out = **34,321 tokens**; **cost $0.0052755**; 1 row; API totals = DB sum (turns, tokens, cost) and cost = tokens × prices.json |
| b. budget guard | PASS | budget **$0.0001**; next message **429** with "Pesan ditolak … Kirim `lanjut` …"; no user row; 1 system line; `lanjut` → 1 turn, 1 `budget_overrides` row (spent **$0.0052755**, budget **$0.0001**); next message **429** again, overrides still 1 |
| c. streaming speech | PASS | stream first **audio** byte **671.9 ms**, total **2,043.2 ms**, 2 chunks; buffered first byte **2,279.4 ms**, total **2,279.5 ms** → **1,607.5 ms faster to first sound (70.5% lower)**; concatenated audio 46,944 bytes transcribed back to *"Selesai. Port delapan ribu kosong. Server lama sudah dimatikan."* |
| d. retry a stopped run | PASS | user rows **1 → 1**; assistant rows **1 → 2**; old reply `meta->>'superseded' = true`; new reply 412 chars with a new `run_id` |

The exact `c` numbers from the probe JSON:

```json
{"non_streaming_first_byte_ms":2279.4,"non_streaming_total_ms":2279.5,
 "streaming_first_audio_ms":671.9,"streaming_total_ms":2043.2,
 "streaming_chunks":2,"streaming_bytes":46944,
 "streaming_transcript":"Selesai. Port delapan ribu kosong. Server lama sudah dimatikan.",
 "problems":[]}
```

### Slowest step

The slowest measured step was scenario d's stopped first turn: **24 s** from sending the prompt to the
first delta plus a cancelled run (the model took its time before the first token), then **7 s** for the
retry. The whole acceptance run was 64 s. Scene a's real turn was 8.1 s; the speech probe was **9 s**
including both transcriptions.

## Browser at 390×844 (real Chromium, production build)

`scripts/p8-browser.sh` / `scripts/browser-p8.mjs` — exit 0. Screenshots:
`changelogs/assets/p8-390-usage.png`, `p8-390-retry.png`, `p8-390-speaking.png`.

- Usage chip: **"hari ini $0.0025 · 1 turn"**; chart has **14 day columns**; total **"$0.0025 · 1 turn"**;
  top rooms list present; panel width **366 px** in a **390 px** viewport (no horizontal overflow).
- Retry: menu trigger **44×44 px**, item **44 px tall**; enabled while idle, **disabled while the turn is
  running**; retry produced **2 assistant bubbles and still 1 user bubble**; old bubble
  `data-superseded="true"`, computed opacity **0.55**, label **"digantikan"**.
- Streaming speech: the **"membaca…"** state appeared while the speech response was still open
  (`streamFinishedAtAppearance === false`), then the stream finished. The fetch wrapper sees the `meta`
  line almost immediately; that is not audio. The audio TTFB is the 671.9 ms measured directly in scene
  c. In this browser run the reply was shorter and edge-tts took ~2.6 s for its first sentence, which is
  why the state appeared later than the acceptance probe; the ordering claim is the one under test.

**What looks wrong / stays rough**

- The 13 empty days on the chart are 2 px baseline marks; the chart reads as one spike rather than a
  14-day history. A faint full-height track per empty day would read better.
- Day labels are day-of-month only (`30 31 1 2 … 12`), so a range crossing a month boundary is
  ambiguous at a glance.
- The inspector chip and heading share one 390 px row. The current short chip fits; a long
  "… tanpa harga" chip can wrap. The row now wraps instead of overflowing, but it is not beautiful.
- Empty headroom above a short transcript is the existing `mt-auto` layout, not a P8 regression.

## Tests

- `go test ./...` — all packages pass. New coverage: terminal usage parsing (captured 3-counter and
  five-counter shapes, absent usage, nested runtime), cost maths against a prices file, unknown model and
  missing-rate honesty, budget threshold + exactly one override + no mid-turn check, retry with no
  duplicate user row / superseded meta / 409 while busy, and the speech proxy flushing more than once
  with the first flush before the last write.
- `web` — `npm run typecheck` and `npm test`: **62 tests pass**. New coverage: streaming speech flips
  "membaca…" on the first chunk before the generator finishes, barge-in aborts, a failed chunk is
  skipped, retry keeps one user row and dims the old reply, sub-cent USD formatting.
- `voice` — `python -m unittest discover -s voice/tests -t . -v`: **38 tests pass**, including the real
  `/speak/stream` round trip (two audio chunks, first audio before `done`, concatenated MP3 transcribes
  back to "selesai" and "port").

## Honest gaps

1. **The live session stream reports 3 of 5 counters.** Cache reads/writes are 0 with
   `cache_reported=false`; the cost is an upper bound, not an invoice. A gateway that starts reporting
   cache counters will improve the estimate without a schema change.
2. **The served model id is not a literal table key.** `deepseek-v4.1-flash` is priced through the
   table's own "V4.1-Flash is served at the Flash price" comment. Boss should confirm this is the rate he
   is actually billed; if not, edit `prices.json`. A price added after a turn does **not** re-price that
   historical `NULL` row: the cost is the price at the time of the turn.
3. **Unknown-cost turns cannot count toward the budget.** The guard sums `cost_usd`; a turn with no
   price contributes tokens but $0 to the sum. The usage API exposes `unknown_cost_turns` so this is
   visible, but a budget cannot police what it cannot price.
4. **`lanjut` sends the literal word as the prompt.** It grants one turn, but it does not replay the
   refused message. That is a deliberate, simple contract; replaying the refused text would need a
   server-side pending-message store and is not in P8.
5. **The budget is global**, not per-room. `IRIS_DAILY_BUDGET_USD` is one daemon-wide number.
6. **Browser audio was not heard.** Headless Chromium proves the state and the open stream, not that a
   phone speaker is audible. The browser harness stubs `HTMLMediaElement.play()` to resolve after 350 ms
   because headless has no audio device; that affects only how long a chunk "plays", never the app's
   state machine. Gaplessness between sentence chunks is a serial `HTMLAudio` queue: usually
   imperceptible locally, never measured on Boss' phone.
7. **TTFB was measured over loopback.** A phone over Tailscale adds network latency; the 671.9 ms is not
   an end-to-end phone number.
8. **Tiered prices are skipped** (13 entries). A flat `prices.json` cannot express "above 272k tokens".
9. **Chart day keys use the browser's local date**, while the API buckets in Asia/Jakarta. On a device
   in another timezone the last column can disagree with the API around midnight.
10. **Retry is also budget-gated.** It reuses a user message, but it spends money; a retry while over
    budget gets the same 429. There is no `lanjut` shortcut on the retry endpoint.
11. The usage response exposes the prices-file path (owner-only API); it is not a secret but it is
    implementation detail.

## How Boss switches the guard on

1. Refresh prices once (Hermes venv + workspace):

   ```bash
   W=$(ls -d ~/.hermes/installs/*/environments/*/workspace | head -1)
   PY=$(dirname "$W")/venv/bin/python
   PYTHONPATH="$W" "$PY" scripts/seed-prices.py
   ```

2. Put a number in the env file (`deploy/iris.env.example` has the line):

   ```bash
   IRIS_DAILY_BUDGET_USD=1.00
   ```

   Empty means off. Restart irisd. The boot log says `daily budget guard on usd=…` or
   `daily budget guard off`.

3. When over budget, the room says exactly what was spent and what the budget is. Sending `lanjut` buys
   one turn and writes a `budget_overrides` row; the next message is refused again.

## Verified by the operator, and one number that is not a win

**Acceptance re-run by me (throwaway `iris_p8_mine`, real Hermes):**

```
a  turn 6 345 ms → usage run_3a78… provider=opencode-go model=deepseek-v4.1-flash
   34 087 + 312 = 34 399 tokens, cost 0.00530025, cost matches tokens × prices.json exactly
b  budget $0.0001 → 429, no user row, one system notice; "lanjut" let exactly one turn through; 429 again
c  streaming first audio 773.0 ms / total 1 484.2 ms
   buffered  first byte 853.5 ms / total 853.6 ms  (2 chunks)
   concatenated audio transcribes back to the same words
d  stopped reply kept, retried the SAME user message (no new user row), new reply created,
   old reply kept and marked superseded
134 go tests, 15 packages
```

**Scene c is the honest one: streaming is 80 ms faster to first sound and 630 ms slower to finish** on a
two-sentence answer. The edge-tts connection cost dominates at this length; the win only appears when the text is
long. Reported as measured, not as "voice is instant now".

## Boss is on a flat plan, so the live instance shows tokens, not dollars

`prices.json` is moved to `prices.json.disabled`; `/api/usage` reports `prices.entries: 0` and
`budget.enabled: false`. The meter therefore counts **turns and tokens** — the numbers that still describe how
hard Iris is working — and the budget guard stays dormant. No dollar figure that does not apply to his billing is
shown anywhere in the UI.

## Deployed

schema 10, web + `irisd` rebuilt, units restarted, local 200, public 200.

## Bug found by Boss: the small speaker played nothing

**Cause.** In `playStream`, the wait for a chunk resolved on `play()` **starting**, not on playback ending —
and the very next line called `audio.dispose()`, which pauses the element and revokes the blob URL. Every chunk
was killed microseconds after it began, so the speaker looked dead. The existing test double had hidden it by
firing `onended` inside `play()`, which no real 6-second clip does.

**Fix.** The wait now resolves on `onended` (or on abort, or on a failed start, or after a 60 s guard), and the
element is disposed only afterwards. A chunk that cannot start is skipped so the rest still plays.

**Proof, in three layers.**
1. A regression test whose fake resolves `play()` and never ends by itself: fails on the old code
   (`expected '' to be 'm1'` — the state never even reached "playing"), passes now.
2. Real browser, trusted click on the speaker: state flips to "Hentikan bacaan" and stays.
3. The media element itself, instrumented through the `Audio` constructor:
   `playing: 1`, `currentTime 0.86 → 4.84` of a `6.264 s` clip, `errors: []`.

63 web tests, build clean. Bundle `index-X8n7mQ7U.js` deployed.
