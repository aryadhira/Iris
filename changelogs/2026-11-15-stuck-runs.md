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
