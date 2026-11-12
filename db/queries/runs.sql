-- name: UpsertRun :one
INSERT INTO runs (run_id, room_id, session_id, status, started_at, updated_at)
VALUES ($1, $2, $3, $4, $5, $5)
ON CONFLICT (run_id) DO UPDATE SET
    session_id = EXCLUDED.session_id,
    status = EXCLUDED.status,
    updated_at = EXCLUDED.updated_at
RETURNING *;

-- name: UpdateRunStatus :one
UPDATE runs
SET status = $2,
    updated_at = $3,
    finished_at = sqlc.narg('finished_at')
WHERE run_id = $1
RETURNING *;

-- name: ListActiveRuns :many
SELECT * FROM runs
WHERE room_id = $1
  AND status IN ('queued', 'running', 'waiting_for_approval')
ORDER BY started_at ASC, run_id ASC;

-- name: GetRun :one
SELECT * FROM runs
WHERE run_id = $1;

-- name: GetLatestRun :one
SELECT * FROM runs
WHERE room_id = $1
ORDER BY started_at DESC, run_id DESC
LIMIT 1;

-- name: ListLatestRuns :many
-- One row per room for a cold load of the sidebar. The room payload only shows
-- it while it is live (or interrupted), never as a stale "done".
SELECT DISTINCT ON (room_id) *
FROM runs
ORDER BY room_id, started_at DESC, run_id DESC;

-- name: MarkActiveRunsInterrupted :many
-- A run that was live when irisd died cannot still be running. Say so.
UPDATE runs
SET status = 'interrupted',
    updated_at = $1,
    finished_at = $1
WHERE status IN ('queued', 'running', 'waiting_for_approval', 'stopping')
RETURNING *;
