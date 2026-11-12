-- name: CreateContinuation :one
INSERT INTO continuations (id, room_id, chain_id, attempt, status, reason, due_at, deadline)
VALUES ($1, $2, $3, $4, 'pending', '', $5, $6)
RETURNING *;

-- name: GetContinuation :one
SELECT * FROM continuations
WHERE id = $1;

-- name: ListPendingContinuations :many
SELECT * FROM continuations
WHERE room_id = $1
  AND status IN ('pending', 'running')
ORDER BY due_at ASC, created_at ASC;

-- name: ListContinuationsForChain :many
SELECT * FROM continuations
WHERE chain_id = $1
ORDER BY attempt ASC;

-- name: CountContinuationsForChain :one
SELECT count(*) FROM continuations
WHERE chain_id = $1;

-- name: ClaimDueContinuations :many
UPDATE continuations
SET status = 'running', started_at = $1
WHERE id IN (
    SELECT id FROM continuations
    WHERE status = 'pending' AND due_at <= $1
    ORDER BY due_at ASC
    LIMIT $2
    FOR UPDATE SKIP LOCKED
)
RETURNING *;

-- name: FinishContinuation :one
UPDATE continuations
SET status = $2, reason = $3, finished_at = $4
WHERE id = $1
RETURNING *;

-- name: CancelPendingContinuationsForRoom :many
UPDATE continuations
SET status = 'cancelled', reason = $2, finished_at = $3
WHERE room_id = $1
  AND status IN ('pending', 'running')
RETURNING *;

-- name: ResumeRunningContinuations :many
UPDATE continuations
SET status = 'pending', started_at = NULL
WHERE status = 'running'
RETURNING *;

-- name: HasNewerUserMessage :one
SELECT EXISTS (
    SELECT 1 FROM messages
    WHERE room_id = $1
      AND role = 'user'
      AND created_at > $2
) AS found;
