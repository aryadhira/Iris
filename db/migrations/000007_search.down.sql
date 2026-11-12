DROP INDEX IF EXISTS messages_search_idx;
ALTER TABLE messages DROP COLUMN IF EXISTS search_vector;
