-- The Ask Agent turn an apply list came from: the conversation and the salted
-- hash of the turn's own message, so rows that run after a restart carry the
-- same trigger as the rows that ran before it. The message text is never kept.
ALTER TABLE apply_batches ADD COLUMN trigger_conversation_id TEXT NOT NULL DEFAULT '';
ALTER TABLE apply_batches ADD COLUMN trigger_message_hash    TEXT NOT NULL DEFAULT '';
