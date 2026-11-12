-- Messages mirror the table shape. The generated search vector is only used
-- by SearchMessages; it is never part of an API response.

-- name: InsertMessage :one
INSERT INTO messages (id, room_id, role, body, run_id, meta)
VALUES ($1, $2, $3, $4, $5, $6)
RETURNING *;

-- name: ListMessages :many
SELECT * FROM messages
WHERE room_id = $1
  AND created_at < COALESCE(sqlc.narg('before')::timestamptz, 'infinity'::timestamptz)
ORDER BY created_at DESC, id DESC
LIMIT sqlc.arg('row_limit')::int;

-- name: ListAllMessages :many
SELECT * FROM messages
WHERE room_id = $1
ORDER BY created_at ASC, id ASC;

-- name: GetMessage :one
SELECT * FROM messages
WHERE id = $1;

-- name: ListMessagesAfter :many
-- Retry walks the transcript from one user message forward, in real order, to
-- find the reply it supersedes. The id is the tiebreaker when two rows share a
-- timestamp (a fast fake clock, or two inserts in the same millisecond).
SELECT * FROM messages
WHERE room_id = $1
  AND (created_at > $2 OR (created_at = $2 AND id > $3::uuid))
ORDER BY created_at ASC, id ASC;

-- name: MarkMessageSuperseded :one
-- The old reply is dimmed and labelled, never deleted: hiding a failure would
-- lie about what happened.
UPDATE messages
SET meta = jsonb_set(COALESCE(meta, '{}'::jsonb), '{superseded}', 'true'::jsonb)
WHERE id = $1
RETURNING *;

-- name: GetLatestAssistantMessage :one
SELECT * FROM messages
WHERE room_id = $1
  AND role = 'assistant'
ORDER BY created_at DESC, id DESC
LIMIT 1;

-- name: SearchMessages :many
-- ts_headline highlights the match without trusting message HTML: the browser
-- escapes the text and only restores the <mark> tags.
SELECT
    m.id,
    m.room_id,
    r.name AS room_name,
    m.role,
    m.created_at,
    ts_headline(
        'indonesian'::regconfig,
        m.body,
        plainto_tsquery('indonesian'::regconfig, sqlc.arg('query')::text),
        'StartSel=<mark>, StopSel=</mark>, MaxWords=28, MinWords=6, MaxFragments=2'
    ) AS snippet
FROM messages m
JOIN rooms r ON r.id = m.room_id
WHERE m.search_vector @@ plainto_tsquery('indonesian'::regconfig, sqlc.arg('query')::text)
ORDER BY m.created_at DESC, m.id DESC
LIMIT sqlc.arg('row_limit')::int;
