-- A turn a button started ("Let the agent finish") is recorded with origin
-- button: the user pressed it, the model did not choose it. SQLite cannot widen
-- a CHECK, so the table is rebuilt; nothing references it by foreign key.
CREATE TABLE chat_turns_new (
    id              TEXT PRIMARY KEY,
    session_id      TEXT NOT NULL REFERENCES chat_sessions (id) ON DELETE CASCADE,
    channel         TEXT NOT NULL,
    origin          TEXT NOT NULL CHECK (origin IN ('model', 'router', 'button')),
    rule_id         TEXT NOT NULL DEFAULT '',
    status          TEXT NOT NULL CHECK (status IN ('running', 'final', 'error')),
    reason          TEXT NOT NULL DEFAULT '',
    detail          TEXT NOT NULL DEFAULT '',
    started_at      TEXT NOT NULL,
    finished_at     TEXT,
    harness_version INTEGER NOT NULL DEFAULT 0,
    tier            TEXT NOT NULL DEFAULT '' CHECK (tier IN ('', 'light', 'heavy'))
) STRICT;

INSERT INTO chat_turns_new (id, session_id, channel, origin, rule_id, status, reason, detail, started_at, finished_at, harness_version, tier)
SELECT id, session_id, channel, origin, rule_id, status, reason, detail, started_at, finished_at, harness_version, tier FROM chat_turns;

DROP TABLE chat_turns;
ALTER TABLE chat_turns_new RENAME TO chat_turns;

CREATE INDEX chat_turns_session ON chat_turns (session_id, started_at);

-- The end of a run its conversation was last told: the state, reason and
-- outcome of its board entry (origins.EndOf). A delegated task waits for an
-- end of its conversation's runs until it is told. The ends standing now were
-- told by the queue the stored mark replaces. (One file with the change above:
-- the repository is at its file budget.)
ALTER TABLE agent_run_origins ADD COLUMN told TEXT NOT NULL DEFAULT '';

UPDATE agent_run_origins SET told = (SELECT b.state || '|' || b.reason || '|' || b.outcome FROM board_entries b WHERE b.id = agent_run_origins.run_id)
WHERE EXISTS (SELECT 1 FROM board_entries b WHERE b.id = agent_run_origins.run_id AND b.state IN ('done', 'needs-you', 'blocked'));
