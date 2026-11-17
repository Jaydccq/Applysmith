-- Delegated tasks of the Ask Agent (ask-agent: "Delegated tasks"). A task is an
-- outcome the user handed to the agent in one dashboard conversation; the agent
-- works it over several turns of that conversation until it is done, it asks
-- the user, or the user stops it. mandate is the user's own message that
-- delegated it, which the guard reads as the user's request in the turns the
-- task starts (owner decision DT-2). steps is a JSON array of
-- {"text","status","note"}. turns counts the turns the task started itself and
-- idle the latest of them in a row that moved nothing. seen_note is the id of
-- the last conversation note a task turn was given. Times are RFC 3339 UTC
-- text with millisecond precision.
CREATE TABLE agent_tasks (
    id          TEXT PRIMARY KEY,
    session_id  TEXT NOT NULL REFERENCES chat_sessions (id) ON DELETE CASCADE,
    title       TEXT NOT NULL CHECK (title <> ''),
    mandate     TEXT NOT NULL CHECK (mandate <> ''),
    state       TEXT NOT NULL CHECK (state IN ('working', 'waiting_runs', 'waiting_you', 'paused', 'done', 'stopped')),
    steps       TEXT NOT NULL DEFAULT '[]',
    question    TEXT NOT NULL DEFAULT '',
    outcome     TEXT NOT NULL DEFAULT '',
    turns       INTEGER NOT NULL DEFAULT 0,
    idle        INTEGER NOT NULL DEFAULT 0,
    progress_at TEXT NOT NULL,
    seen_note   INTEGER NOT NULL DEFAULT 0,
    created_at  TEXT NOT NULL,
    updated_at  TEXT NOT NULL
) STRICT;

-- A conversation has at most one open task.
CREATE UNIQUE INDEX agent_tasks_open ON agent_tasks (session_id) WHERE state NOT IN ('done', 'stopped');
CREATE INDEX agent_tasks_state ON agent_tasks (state);
