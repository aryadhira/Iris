-- name: CreateReminder :one
INSERT INTO reminders (id, room_id, text, due_at, state)
VALUES ($1, $2, $3, $4, 'pending')
RETURNING *;

-- name: ListPendingReminders :many
SELECT * FROM reminders
WHERE room_id = $1
  AND state = 'pending'
ORDER BY due_at ASC, id ASC;

-- name: CancelReminder :one
UPDATE reminders
SET state = 'cancelled'
WHERE id = $1
  AND room_id = $2
  AND state = 'pending'
RETURNING *;

-- name: ListDueReminders :many
SELECT * FROM reminders
WHERE state = 'pending'
  AND due_at <= $1
ORDER BY due_at ASC, id ASC
LIMIT sqlc.arg('row_limit')::int;

-- FireReminder marks the reminder fired and inserts its visible system message
-- in the same statement. Either both happen or neither does, so a crash can
-- never leave a fired reminder with no message (or two messages for one row).
-- name: FireReminder :one
WITH claimed AS (
    UPDATE reminders AS r
    SET state = 'fired', fired_at = sqlc.arg('fired_at')
    WHERE r.id = sqlc.arg('reminder_id')
      AND r.state = 'pending'
    RETURNING r.id, r.room_id
)
INSERT INTO messages (id, room_id, role, body, meta)
SELECT sqlc.arg('message_id'), claimed.room_id, 'system', sqlc.arg('body'), '{}'::jsonb
FROM claimed
RETURNING id, room_id, role, body, run_id, created_at, meta, search_vector;
