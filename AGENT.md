# AGENT.md — rules for anyone (human or agent) working on Iris

## What this is
A private gateway to **one** Hermes agent. Rooms, text, voice, files, artifacts, and a conversation that
finishes what it starts. Read `ARCHITECTURE.md` and `DECISIONS.md` before changing anything.

## Hard rules

1. **Hermes stays stock.** Never edit `~/.hermes/hermes-agent`. Continuity is ours (ADR-003).
2. **One agent.** No personas, no multi-agent rooms, no orchestration layer.
3. **The API key never reaches the browser.** All Hermes calls go through `irisd`.
4. **Artifacts live in the room folder.** Never rely on Hermes' one-shot artifact store.
5. **Speech is a summary.** Markdown stripped, code and tables dropped, ≤2 sentences. The screen keeps the
   full text.
6. **Every claim is measured.** Paste real output; a self-report is not evidence.
7. **No invented versions.** Check the published version before adding a dependency.
8. **Write tests with the feature**, and make the failure honest: an empty state must never look like
   activity, and a stalled run must never look like work.

## Conventions
- Backend: Go + Echo, `sqlc` + `golang-migrate` + `pgx`, hand-written SQL, no ORM.
- Frontend: Vue 3 + Vite + TS + Tailwind v4, hand-built components, Inter Tight, Phosphor icons.
- Auth: single owner, passphrase → cookie; no public sign-up.
- Ports: `irisd` :3300, web dev :5175, preview :4175. Never collide with Arya AI Lab (:8080/:4173).

## Testing
- `go test ./...` for handlers with a fake Hermes client (no network).
- One real end-to-end check per phase against a throwaway Hermes session, cleaned up afterwards.
- Browser checks at 390×844 as well as 1440×900 — this is a phone product first.

## Changelogs
`changelogs/YYYY-MM-DD-<slug>.md`, one per shipped change: what changed, why, measured evidence, what is
still missing.
