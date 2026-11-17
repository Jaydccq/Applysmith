-- The model tier the intent classifier chose for a model-loop turn: light or
-- heavy. Empty when intent routing was off or the turn was not classified.
ALTER TABLE chat_turns ADD COLUMN tier TEXT NOT NULL DEFAULT '' CHECK (tier IN ('', 'light', 'heavy'));
