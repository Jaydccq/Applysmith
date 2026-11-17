-- Remote access scoping (remote-access). A card remembers that its turn came
-- from a remote device, so the call that runs when it is approved keeps the
-- remote tool envelope; a conversation a remote device started is the only
-- conversation that device may post to or read.

ALTER TABLE agent_confirmations ADD COLUMN remote INTEGER NOT NULL DEFAULT 0;

CREATE TABLE remote_sessions (
    session_id TEXT PRIMARY KEY REFERENCES chat_sessions (id) ON DELETE CASCADE,
    device_id  TEXT NOT NULL
) STRICT;
