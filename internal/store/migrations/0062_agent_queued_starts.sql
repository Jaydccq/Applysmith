-- A start the Ask Agent queued because two of its runs were going: the board
-- line it shows as (entry_id) and the conversation it was asked in. The row
-- lives until the start begins, is refused or gives up. A row a stopped server
-- left is told to its conversation at the next start, and the start is not
-- replayed. While it lives, the conversation's delegated task counts the start
-- as work still going.
CREATE TABLE agent_queued_starts (
    entry_id   TEXT PRIMARY KEY CHECK (entry_id <> ''),
    session_id TEXT NOT NULL REFERENCES chat_sessions (id) ON DELETE CASCADE,
    label      TEXT NOT NULL,
    created_at TEXT NOT NULL
) STRICT;

CREATE INDEX agent_queued_starts_session ON agent_queued_starts (session_id);
