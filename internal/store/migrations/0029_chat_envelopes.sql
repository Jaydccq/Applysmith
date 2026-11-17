-- The answer envelope of an assistant message: the sources its claims rest on,
-- what it could not establish and the next step. JSON text; empty when the
-- message carried none.
ALTER TABLE chat_messages ADD COLUMN envelope TEXT NOT NULL DEFAULT '';
