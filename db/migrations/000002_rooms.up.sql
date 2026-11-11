CREATE TABLE rooms (
    id uuid PRIMARY KEY,
    name text NOT NULL,
    topic text NOT NULL DEFAULT '',
    hermes_session_id text NOT NULL UNIQUE,
    created_at timestamptz NOT NULL DEFAULT now(),
    archived_at timestamptz
);
