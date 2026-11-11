CREATE TABLE messages (
    id uuid PRIMARY KEY,
    room_id uuid NOT NULL REFERENCES rooms (id) ON DELETE CASCADE,
    role text NOT NULL CHECK (role IN ('user', 'assistant', 'system')),
    body text NOT NULL,
    run_id text,
    created_at timestamptz NOT NULL DEFAULT now()
);
