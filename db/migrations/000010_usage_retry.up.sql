-- P8: one row per assistant turn, metered from the terminal Hermes payload.
--
-- usage_reported is the honesty bit: a run whose terminal event carried no
-- `usage` object is stored with zeros AND usage_reported = false, so "not
-- reported" can never be displayed as "free". cache_reported says whether the
-- gateway named its cache counters at all (some builds report only three of
-- the five counters; the missing ones read as 0 but cost is then an upper
-- bound, not an invoice).
--
-- cost_usd is NULL when the model has no price in prices.json. It is never
-- filled with 0 to make a chart look complete.
CREATE TABLE "usage" (
    id uuid PRIMARY KEY,
    run_id text NOT NULL,
    room_id uuid NOT NULL REFERENCES rooms (id) ON DELETE CASCADE,
    message_id uuid REFERENCES messages (id) ON DELETE SET NULL,
    provider text NOT NULL DEFAULT '',
    model text NOT NULL DEFAULT '',
    input_tokens bigint NOT NULL DEFAULT 0,
    output_tokens bigint NOT NULL DEFAULT 0,
    total_tokens bigint NOT NULL DEFAULT 0,
    cache_read_tokens bigint NOT NULL DEFAULT 0,
    cache_write_tokens bigint NOT NULL DEFAULT 0,
    cost_usd double precision,
    usage_reported boolean NOT NULL DEFAULT true,
    cache_reported boolean NOT NULL DEFAULT false,
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE UNIQUE INDEX usage_run_idx ON "usage" (run_id);
CREATE INDEX usage_created_idx ON "usage" (created_at DESC);
CREATE INDEX usage_room_created_idx ON "usage" (room_id, created_at DESC);

-- One row per explicit `lanjut`. Each row records the spend and budget Boss
-- overrode, for the room and Jakarta day, and is the receipt for exactly that
-- one turn. Nothing here changes the budget itself.
CREATE TABLE budget_overrides (
    id uuid PRIMARY KEY,
    room_id uuid NOT NULL REFERENCES rooms (id) ON DELETE CASCADE,
    day date NOT NULL,
    spent_usd double precision NOT NULL,
    budget_usd double precision NOT NULL,
    message_id uuid REFERENCES messages (id) ON DELETE SET NULL,
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX budget_overrides_room_day_idx ON budget_overrides (room_id, day, created_at DESC);
