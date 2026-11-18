# Iris stopped answering: two zombie runs held the room

**Reported by Boss:** "iris doesn't respond anything on conversation room".

## What was wrong

Iris was healthy — service active, `/api/health` 200, public URL 200, gateway 200. The database showed the reason:

```
run_ea5d5989… | running | room c53c90c4 "App Improvement" | 32 minutes old
run_69f4a2b7… | running | same room                        |  8 minutes old
```

An active run blocks the room (that is the guard that stops two turns colliding), so every new message was
refused while those rows claimed to be working. Nothing was driving them: the journal had no events for either
run id, and one earlier turn had already died with `hermes stream went idle: no data for 10m0s`.

So: **a hung turn left its row `running` forever, and the guard read that as "busy"**. The only cure was editing
the database by hand — which is not a product behaviour.

## Immediate unblock

Both rows were marked `interrupted` and the streaming message rows closed. The room answered immediately:
`Tes koneksi…` → `Tes.`

## The fix

A reaper in irisd: every minute, any run still in `queued/running/waiting_for_approval/stopping` whose
`updated_at` is older than **20 minutes** (double the 10-minute idle budget, so a legitimate turn can never be
caught) is closed as `interrupted`, its streaming rows are closed, the room gets a visible Indonesian note, and a
`done` event clears any open client. Boot recovery already handled restart orphans; this handles hangs inside a
live process.

- `db/queries/runs.sql`: `MarkStaleRunsInterrupted` (sqlc-generated)
- `internal/httpapi/events.go`: `startRunReaper` + `reapStaleRuns`
- tests: a stale run is closed **and explained**; a two-minute-old run is left alone.

## Verified on the live database

```
inserted  run_zombie_probe  running, updated_at 40 minutes ago
after 75s status = interrupted
note   "giliran sebelumnya berhenti tanpa kabar selama lebih dari 20 menit, jadi Iris menutupnya."
journal "reaped a stale run" ×1
room   Tes koneksi… → "Tes."
```

13 Go packages ok.

## Still open

The hang itself is Hermes-side: `vision_analyze` on an mp4 never returned, and that is what produced the first
zombie. The reaper keeps Iris usable, but it treats the symptom; frame extraction before handing a video to a
still-image tool is the real cure.

## Follow-up: why the watchdog let a dead turn look alive for 20 minutes

Boss: "last message is taking long time, so Iris got timeout and not sending the response … room said idle".

The evidence: the run in room `9d9ff321` started 21:52:57, the client left at 21:55:30, **the assistant row stayed 0
bytes**, and the reaper closed it at 22:13:06 (20 minutes). The Hermes session for that room had **message_count
0** — the turn never produced anything at all. A probe run against the gateway afterwards completed in under 8 s,
so the gateway was not wedged; that one request died upstream.

**The Iris bug underneath it:** the SSE idle watchdog was reset by *any* `event:` or `data:` line, including
heartbeat-style frames. A gateway that keeps a socket warm while producing nothing therefore looked like work in
progress, so the 10-minute watchdog never fired and only the 20-minute reaper noticed.

Fix: the watchdog is now reset only when a dispatched event is not a keepalive (`ping`, `pong`, `heartbeat`,
`keepalive`, `noop`, `comment`). Test: 40 ping frames over one second with a 250 ms budget must raise `ErrIdle`,
and it does (1.01 s wall time including setup).

Net effect: a turn that produces nothing now fails at the idle budget with an honest error instead of sitting at
"bekerja" for twenty minutes and then going quiet.

Verified after deploy: the affected room answered a fresh message in 8 s —
`done | "Siap, Boss — watchdog sudah jalan dan saya merespons normal."`

14 Go packages ok.
