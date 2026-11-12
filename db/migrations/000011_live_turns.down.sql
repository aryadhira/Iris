-- P10: streaming rows and interrupted rows are removed with the messages table.
ALTER TABLE messages DROP COLUMN IF EXISTS state;
