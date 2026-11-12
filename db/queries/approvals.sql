-- name: UpsertApproval :one
INSERT INTO approvals (
    id, room_id, run_id, session_id, request_id, tool_name, command,
    description, choices, state, created_at, expires_at
)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, 'pending', $10, $11)
ON CONFLICT (run_id, request_id) DO UPDATE SET
    tool_name = EXCLUDED.tool_name,
    command = EXCLUDED.command,
    description = EXCLUDED.description,
    choices = EXCLUDED.choices
RETURNING *;

-- name: GetApproval :one
SELECT * FROM approvals
WHERE id = $1;

-- name: ListApprovals :many
SELECT * FROM approvals
WHERE room_id = $1
ORDER BY created_at ASC, id ASC;

-- name: ResolveApproval :one
UPDATE approvals
SET state = $2,
    choice = sqlc.narg('choice'),
    resolved_at = $3
WHERE id = $1
  AND state = 'pending'
RETURNING *;

-- name: ExpireApprovals :many
UPDATE approvals
SET state = 'expired',
    resolved_at = $1
WHERE state = 'pending'
  AND expires_at <= $1
RETURNING *;

-- name: CountPendingApprovalsByRoom :many
SELECT room_id, count(*) AS count
FROM approvals
WHERE state = 'pending'
  AND expires_at > $1
GROUP BY room_id;
