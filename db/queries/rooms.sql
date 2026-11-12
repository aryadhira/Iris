-- name: CreateRoom :one
INSERT INTO rooms (id, name, topic, hermes_session_id, folder_path, system_prompt)
VALUES ($1, $2, $3, $4, $5, $6)
RETURNING *;

-- name: ListRooms :many
SELECT * FROM rooms
WHERE archived_at IS NULL
ORDER BY created_at DESC, id DESC;

-- name: GetRoom :one
SELECT * FROM rooms
WHERE id = $1;

-- name: ArchiveRoom :one
UPDATE rooms
SET archived_at = now()
WHERE id = $1
RETURNING *;

-- name: UpdateRoomPrompt :one
UPDATE rooms
SET system_prompt = $2
WHERE id = $1
RETURNING *;
