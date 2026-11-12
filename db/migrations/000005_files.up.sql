ALTER TABLE rooms ADD COLUMN folder_path text NOT NULL DEFAULT '';
ALTER TABLE rooms ADD COLUMN system_prompt text NOT NULL DEFAULT '';

CREATE TABLE files (
    id uuid PRIMARY KEY,
    room_id uuid NOT NULL REFERENCES rooms (id) ON DELETE CASCADE,
    message_id uuid REFERENCES messages (id) ON DELETE SET NULL,
    source text NOT NULL CHECK (source IN ('user', 'agent')),
    name text NOT NULL,
    path text NOT NULL,
    mime text NOT NULL,
    size bigint NOT NULL CHECK (size >= 0),
    sha256 text NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX files_room_created_idx ON files (room_id, created_at DESC, id DESC);
CREATE INDEX files_message_idx ON files (message_id);
