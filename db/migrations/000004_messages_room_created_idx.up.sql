CREATE INDEX messages_room_created_idx ON messages (room_id, created_at DESC, id DESC);
