-- name: InsertMessage :one
INSERT INTO messages (id, room_id, role, body, run_id)
VALUES ($1, $2, $3, $4, $5)
RETURNING *;

-- name: ListMessages :many
SELECT * FROM messages
WHERE room_id = $1
  AND created_at < COALESCE(sqlc.narg('before')::timestamptz, 'infinity'::timestamptz)
ORDER BY created_at DESC, id DESC
LIMIT sqlc.arg('row_limit')::int;
