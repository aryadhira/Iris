-- name: CreateAuthSession :exec
INSERT INTO auth_sessions (token_hash, expires_at, user_agent)
VALUES ($1, $2, $3);

-- name: GetAuthSession :one
SELECT * FROM auth_sessions
WHERE token_hash = $1
  AND expires_at > now();

-- name: DeleteAuthSession :exec
DELETE FROM auth_sessions
WHERE token_hash = $1;

-- name: DeleteExpiredAuthSessions :exec
DELETE FROM auth_sessions
WHERE expires_at <= now();
