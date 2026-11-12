-- P3: continuity. A message records what the turn left behind, and a
-- continuation row makes the safety-net pump survive a restart.
ALTER TABLE messages ADD COLUMN meta jsonb NOT NULL DEFAULT '{}'::jsonb;

CREATE TABLE continuations (
    id uuid PRIMARY KEY,
    room_id uuid NOT NULL REFERENCES rooms (id) ON DELETE CASCADE,
    chain_id uuid NOT NULL,                 -- the first assistant message of the chain
    attempt int NOT NULL CHECK (attempt BETWEEN 1 AND 3),
    status text NOT NULL CHECK (status IN ('pending', 'running', 'done', 'skipped', 'cancelled', 'capped')),
    reason text NOT NULL DEFAULT '',
    due_at timestamptz NOT NULL,
    deadline timestamptz NOT NULL,          -- first turn + 30 minutes
    started_at timestamptz,
    finished_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now()
);

-- One row per attempt: a second irisd (or a restart replay) cannot double it.
CREATE UNIQUE INDEX continuations_chain_attempt_idx ON continuations (chain_id, attempt);
CREATE INDEX continuations_due_idx ON continuations (status, due_at);
CREATE INDEX continuations_room_status_idx ON continuations (room_id, status);
