-- The Ask Agent's records: conversations (chat sessions), their ordered
-- messages and tool calls, one row per turn, the full-text index over message
-- text, the idempotency claims of run-starting tools, and the guard-chain
-- audit. gate_decisions is the one table apply-safety-gates describes; it is
-- declared with IF NOT EXISTS so that whichever migration declares it first
-- wins and the two definitions stay identical. Timestamps are RFC 3339 UTC
-- text with millisecond precision.

CREATE TABLE chat_sessions (
    id                TEXT PRIMARY KEY,
    title             TEXT NOT NULL DEFAULT '',
    channel           TEXT NOT NULL CHECK (channel IN ('dashboard', 'telegram', 'scheduled', 'digest')),
    parent_id         TEXT,
    parent_message_id INTEGER,
    created_at        TEXT NOT NULL,
    updated_at        TEXT NOT NULL
) STRICT;

CREATE INDEX chat_sessions_updated ON chat_sessions (updated_at);

-- A message is append-only. tool_call rows carry the arguments as JSON in body;
-- a tool_result row is paired with its call by (turn_id, tool_call_id), and a
-- result the system made after a restart has system_made set.
CREATE TABLE chat_messages (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    session_id   TEXT NOT NULL REFERENCES chat_sessions (id) ON DELETE CASCADE,
    turn_id      TEXT NOT NULL DEFAULT '',
    role         TEXT NOT NULL CHECK (role IN ('user', 'assistant', 'steer', 'tool_call', 'tool_result', 'note')),
    body         TEXT NOT NULL,
    tool_name    TEXT NOT NULL DEFAULT '',
    tool_call_id TEXT NOT NULL DEFAULT '',
    ok           INTEGER NOT NULL DEFAULT 1 CHECK (ok IN (0, 1)),
    system_made  INTEGER NOT NULL DEFAULT 0 CHECK (system_made IN (0, 1)),
    created_at   TEXT NOT NULL
) STRICT;

CREATE INDEX chat_messages_session ON chat_messages (session_id, id);
CREATE UNIQUE INDEX chat_messages_result ON chat_messages (session_id, turn_id, tool_call_id) WHERE role = 'tool_result';

-- Full-text recall covers what the user and the agent said, not tool traffic.
CREATE VIRTUAL TABLE chat_messages_fts USING fts5 (
    body,
    content = 'chat_messages',
    content_rowid = 'id',
    tokenize = 'unicode61 remove_diacritics 2'
);

CREATE TRIGGER chat_messages_fts_insert AFTER INSERT ON chat_messages
WHEN new.role IN ('user', 'assistant', 'steer')
BEGIN
    INSERT INTO chat_messages_fts (rowid, body) VALUES (new.id, new.body);
END;

CREATE TRIGGER chat_messages_fts_delete AFTER DELETE ON chat_messages
WHEN old.role IN ('user', 'assistant', 'steer')
BEGIN
    INSERT INTO chat_messages_fts (chat_messages_fts, rowid, body) VALUES ('delete', old.id, old.body);
END;

-- One row per turn. origin says who chose the first action: the model loop or
-- the intent router. A turn still running when the process stopped is ended as
-- runtime_unavailable at the next start.
CREATE TABLE chat_turns (
    id          TEXT PRIMARY KEY,
    session_id  TEXT NOT NULL REFERENCES chat_sessions (id) ON DELETE CASCADE,
    channel     TEXT NOT NULL,
    origin      TEXT NOT NULL CHECK (origin IN ('model', 'router')),
    rule_id     TEXT NOT NULL DEFAULT '',
    status      TEXT NOT NULL CHECK (status IN ('running', 'final', 'error')),
    reason      TEXT NOT NULL DEFAULT '',
    detail      TEXT NOT NULL DEFAULT '',
    started_at  TEXT NOT NULL,
    finished_at TEXT
) STRICT;

CREATE INDEX chat_turns_session ON chat_turns (session_id, started_at);

-- The claim of a run-starting tool call: the pair (tool, key) is written before
-- the tool starts and settled with its outcome. Kept 30 days.
CREATE TABLE agent_claims (
    tool       TEXT NOT NULL,
    key        TEXT NOT NULL,
    claimed_at TEXT NOT NULL,
    settled_at TEXT,
    outcome    TEXT NOT NULL DEFAULT '',
    PRIMARY KEY (tool, key)
) STRICT;

CREATE INDEX agent_claims_claimed ON agent_claims (claimed_at);

CREATE TABLE IF NOT EXISTS gate_decisions (
    id                INTEGER PRIMARY KEY AUTOINCREMENT,
    at                TEXT NOT NULL,
    gate              TEXT NOT NULL CHECK (gate <> ''),
    verdict           TEXT NOT NULL CHECK (verdict IN ('allow', 'hold', 'deny', 'allow-with-trace')),
    code              TEXT NOT NULL CHECK (code <> ''),
    reason            TEXT NOT NULL CHECK (length(reason) <= 300),
    run_id            TEXT,
    caller            TEXT,
    tool              TEXT,
    turn_id           TEXT,
    conversation_id   TEXT,
    risk_tier         TEXT,
    arg_hash          TEXT,
    user_message_hash TEXT,
    target_host       TEXT,
    target_path       TEXT,
    target_scheme     TEXT
) STRICT;

CREATE INDEX IF NOT EXISTS gate_decisions_run ON gate_decisions (run_id) WHERE run_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS gate_decisions_at ON gate_decisions (at);

CREATE TRIGGER IF NOT EXISTS gate_decisions_no_update BEFORE UPDATE ON gate_decisions
BEGIN
    SELECT RAISE(ABORT, 'gate decisions are append-only');
END;
