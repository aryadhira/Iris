-- P6: run identity, approvals and stop.
--
-- A run row is what lets "stop" find live work after the browser is gone.
-- An approval row is the receipt: it keeps the redacted command Hermes sent,
-- the choices Hermes actually offered, and who decided what. It never holds
-- the pre-redaction command because Hermes redacts before egress.
CREATE TABLE runs (
    run_id text PRIMARY KEY,
    room_id uuid NOT NULL REFERENCES rooms (id) ON DELETE CASCADE,
    session_id text NOT NULL DEFAULT '',
    status text NOT NULL CHECK (status IN (
        'queued', 'running', 'waiting_for_approval', 'stopping',
        'completed', 'failed', 'cancelled', 'interrupted'
    )),
    started_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    finished_at timestamptz
);

CREATE INDEX runs_room_active_idx ON runs (room_id, status);
CREATE INDEX runs_started_idx ON runs (started_at DESC);

CREATE TABLE approvals (
    id uuid PRIMARY KEY,
    room_id uuid NOT NULL REFERENCES rooms (id) ON DELETE CASCADE,
    run_id text NOT NULL,
    session_id text NOT NULL DEFAULT '',
    request_id text NOT NULL,
    tool_name text NOT NULL DEFAULT '',
    command text NOT NULL,
    description text NOT NULL DEFAULT '',
    choices text[] NOT NULL,
    state text NOT NULL CHECK (state IN (
        'pending', 'allowed', 'denied', 'expired', 'superseded'
    )),
    choice text,
    created_at timestamptz NOT NULL DEFAULT now(),
    expires_at timestamptz NOT NULL,
    resolved_at timestamptz
);

-- A repeated SSE frame for the same request updates the record instead of
-- showing Boss two identical cards.
CREATE UNIQUE INDEX approvals_run_request_idx ON approvals (run_id, request_id);
CREATE INDEX approvals_room_created_idx ON approvals (room_id, created_at ASC, id ASC);
CREATE INDEX approvals_pending_idx ON approvals (state, expires_at);
