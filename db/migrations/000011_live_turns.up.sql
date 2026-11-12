-- P10: a room you can leave and come back to.
--
-- An assistant row now exists while the turn is still writing. state is the
-- honesty bit: 'streaming' means the body is a partial answer, 'interrupted'
-- means irisd stopped before Hermes finished, 'done' is a final answer.
-- Existing rows predate the column and are final by definition.
ALTER TABLE messages
    ADD COLUMN state text NOT NULL DEFAULT 'done'
    CHECK (state IN ('streaming', 'done', 'interrupted'));

CREATE INDEX messages_streaming_idx ON messages (state) WHERE state = 'streaming';
