-- Cost meter queries. Daily buckets are cut in Asia/Jakarta because the budget
-- guard's "today" is Boss' day, not UTC's.

-- name: InsertUsage :one
INSERT INTO "usage" (
    id, run_id, room_id, message_id, provider, model,
    input_tokens, output_tokens, total_tokens, cache_read_tokens, cache_write_tokens,
    cost_usd, usage_reported, cache_reported, created_at
) VALUES (
    $1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15
)
ON CONFLICT (run_id) DO UPDATE SET
    message_id = EXCLUDED.message_id,
    provider = EXCLUDED.provider,
    model = EXCLUDED.model,
    input_tokens = EXCLUDED.input_tokens,
    output_tokens = EXCLUDED.output_tokens,
    total_tokens = EXCLUDED.total_tokens,
    cache_read_tokens = EXCLUDED.cache_read_tokens,
    cache_write_tokens = EXCLUDED.cache_write_tokens,
    cost_usd = EXCLUDED.cost_usd,
    usage_reported = EXCLUDED.usage_reported,
    cache_reported = EXCLUDED.cache_reported
RETURNING *;

-- name: UsageTotals :one
SELECT
    count(*)::bigint AS turns,
    COALESCE(sum(input_tokens), 0)::bigint AS input_tokens,
    COALESCE(sum(output_tokens), 0)::bigint AS output_tokens,
    COALESCE(sum(total_tokens), 0)::bigint AS total_tokens,
    COALESCE(sum(cache_read_tokens), 0)::bigint AS cache_read_tokens,
    COALESCE(sum(cache_write_tokens), 0)::bigint AS cache_write_tokens,
    COALESCE(sum(cost_usd), 0)::double precision AS cost_usd,
    count(*) FILTER (WHERE cost_usd IS NULL)::bigint AS unknown_cost_turns,
    count(*) FILTER (WHERE NOT usage_reported)::bigint AS unreported_turns,
    count(*) FILTER (WHERE NOT cache_reported)::bigint AS unreported_cache_turns
FROM "usage"
WHERE (sqlc.narg('room_id')::uuid IS NULL OR room_id = sqlc.narg('room_id')::uuid)
  AND created_at >= sqlc.arg('from_at')::timestamptz
  AND created_at < sqlc.arg('to_at')::timestamptz;

-- name: UsageDaily :many
SELECT
    to_char((created_at AT TIME ZONE 'Asia/Jakarta')::date, 'YYYY-MM-DD') AS day,
    count(*)::bigint AS turns,
    COALESCE(sum(input_tokens + output_tokens), 0)::bigint AS tokens,
    COALESCE(sum(cost_usd), 0)::double precision AS cost_usd,
    count(*) FILTER (WHERE cost_usd IS NULL)::bigint AS unknown_cost_turns
FROM "usage"
WHERE (sqlc.narg('room_id')::uuid IS NULL OR room_id = sqlc.narg('room_id')::uuid)
  AND created_at >= sqlc.arg('from_at')::timestamptz
  AND created_at < sqlc.arg('to_at')::timestamptz
GROUP BY 1
ORDER BY 1 ASC;

-- name: UsageByRoom :many
SELECT
    u.room_id,
    r.name AS room_name,
    count(*)::bigint AS turns,
    COALESCE(sum(u.input_tokens + u.output_tokens), 0)::bigint AS tokens,
    COALESCE(sum(u.cost_usd), 0)::double precision AS cost_usd,
    count(*) FILTER (WHERE u.cost_usd IS NULL)::bigint AS unknown_cost_turns
FROM "usage" u
JOIN rooms r ON r.id = u.room_id
WHERE u.created_at >= sqlc.arg('from_at')::timestamptz
  AND u.created_at < sqlc.arg('to_at')::timestamptz
GROUP BY u.room_id, r.name
ORDER BY cost_usd DESC, turns DESC, r.name ASC;

-- name: UsageSpendBetween :one
SELECT
    COALESCE(sum(cost_usd), 0)::double precision AS cost_usd,
    count(*) FILTER (WHERE cost_usd IS NULL)::bigint AS unknown_cost_turns,
    count(*)::bigint AS turns
FROM "usage"
WHERE created_at >= sqlc.arg('from_at')::timestamptz
  AND created_at < sqlc.arg('to_at')::timestamptz;

-- name: InsertBudgetOverride :one
INSERT INTO budget_overrides (id, room_id, day, spent_usd, budget_usd, message_id, created_at)
VALUES ($1, $2, $3, $4, $5, $6, $7)
RETURNING *;
