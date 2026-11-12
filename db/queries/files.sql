-- name: InsertFile :one
INSERT INTO files (id, room_id, message_id, source, name, path, mime, size, sha256)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
RETURNING *;

-- name: GetFile :one
SELECT * FROM files
WHERE id = $1;

-- name: ListFiles :many
SELECT * FROM files
WHERE room_id = $1
ORDER BY created_at DESC, id DESC;

-- name: DeleteFile :one
DELETE FROM files
WHERE id = $1
RETURNING *;

-- name: AttachFileToMessage :execrows
UPDATE files
SET message_id = $1
WHERE id = $2
  AND room_id = $3
  AND source = 'user'
  AND message_id IS NULL;
