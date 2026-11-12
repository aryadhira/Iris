-- P7: reminders and Web Push.
--
-- A reminder belongs to one room and is fired by irisd itself: Hermes is not
-- woken up and nothing is patched. `state` is the whole lifecycle; a due row is
-- claimed atomically together with its system message so a restart can neither
-- lose it nor post it twice.
CREATE TABLE reminders (
    id uuid PRIMARY KEY,
    room_id uuid NOT NULL REFERENCES rooms (id) ON DELETE CASCADE,
    text text NOT NULL,
    due_at timestamptz NOT NULL,
    state text NOT NULL DEFAULT 'pending' CHECK (state IN ('pending', 'fired', 'cancelled')),
    created_at timestamptz NOT NULL DEFAULT now(),
    fired_at timestamptz
);

CREATE INDEX reminders_due_idx ON reminders (state, due_at);
CREATE INDEX reminders_room_idx ON reminders (room_id, created_at DESC);

-- One row per installed device. The endpoint is the identity: subscribing
-- again from the same device updates the keys instead of stacking a second
-- notification. room_id records where notifications were enabled; the push
-- payload always names the room that caused it.
CREATE TABLE push_subscriptions (
    id uuid PRIMARY KEY,
    room_id uuid NOT NULL REFERENCES rooms (id) ON DELETE CASCADE,
    endpoint text NOT NULL UNIQUE,
    p256dh text NOT NULL,
    auth text NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX push_subscriptions_room_idx ON push_subscriptions (room_id);
