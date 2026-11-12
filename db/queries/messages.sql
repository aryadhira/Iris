-- Messages mirror the table shape. The generated search vector is only used
-- by SearchMessages; it is never part of an API response.

-- name: InsertMessage :one
INSERT INTO messages (id, room_id, role, body, run_id, meta)
VALUES ($1, $2, $3, $4, $5, $6)
RETURNING *;

-- name: StartStreamingMessage :one
-- A turn owns its assistant row from its first moment. A phone that reloads
-- mid-turn finds a row marked streaming instead of an empty room.
INSERT INTO messages (id, room_id, role, body, run_id, meta, state)
VALUES ($1, $2, 'assistant', '', $3, $4, 'streaming')
RETURNING *;

-- name: UpdateMessagePartial :exec
UPDATE messages
SET body = $2
WHERE id = $1
  AND state = 'streaming';

-- name: SetMessageRunID :exec
UPDATE messages
SET run_id = $2
WHERE id = $1
  AND state = 'streaming';

-- name: FinalizeMessage :one
UPDATE messages
SET body = $2,
    run_id = $3,
    meta = $4,
    state = $5
WHERE id = $1
RETURNING *;

-- name: DeleteStreamingMessage :exec
DELETE FROM messages
WHERE id = $1
  AND state = 'streaming';

-- name: GetStreamingMessage :one
SELECT * FROM messages
WHERE room_id = $1
  AND state = 'streaming'
ORDER BY created_at DESC, id DESC
LIMIT 1;

-- name: MarkStreamingMessagesInterrupted :many
-- On boot a streaming row can only mean irisd died mid-turn. Mark it, never
-- delete it and never pretend it completed.
UPDATE messages
SET state = 'interrupted'
WHERE state = 'streaming'
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
  AND state = 'done'
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
  -- A streaming or interrupted body is not a finished answer; never surface it
  -- as a search hit as if Boss could rely on it.
  AND m.state = 'done'
ORDER BY m.created_at DESC, m.id DESC
LIMIT sqlc.arg('row_limit')::int;
