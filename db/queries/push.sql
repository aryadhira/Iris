-- name: ListPushSubscriptions :many
SELECT * FROM push_subscriptions
ORDER BY created_at ASC, id ASC;

-- name: UpsertPushSubscription :one
INSERT INTO push_subscriptions (id, room_id, endpoint, p256dh, auth)
VALUES ($1, $2, $3, $4, $5)
ON CONFLICT (endpoint) DO UPDATE SET
    room_id = EXCLUDED.room_id,
    p256dh = EXCLUDED.p256dh,
    auth = EXCLUDED.auth,
    updated_at = now()
RETURNING *;

-- name: DeletePushSubscription :execrows
DELETE FROM push_subscriptions
WHERE endpoint = $1;
