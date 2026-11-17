-- The Ask Agent's harness versions, the self-improvement rounds that propose
-- them, and the lessons the rounds keep (docs/architecture/ask-agent-improve.md).
-- Version 0 is the empty harness and has no row. At most one row is active.

CREATE TABLE agent_harness_versions (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    parent_id  INTEGER NOT NULL DEFAULT 0,
    -- guidance is a JSON array of lines, tool_notes a JSON object tool -> note.
    guidance   TEXT NOT NULL DEFAULT '[]',
    tool_notes TEXT NOT NULL DEFAULT '{}',
    source     TEXT NOT NULL CHECK (source IN ('improve', 'manual')),
    summary    TEXT NOT NULL DEFAULT '',
    -- evidence is the round's JSON record: turns, verdicts, costs.
    evidence   TEXT NOT NULL DEFAULT '{}',
    round_id   TEXT NOT NULL DEFAULT '',
    active     INTEGER NOT NULL DEFAULT 0 CHECK (active IN (0, 1)),
    created_at TEXT NOT NULL,
    -- activated_at is when the version last became active.
    activated_at TEXT NOT NULL DEFAULT ''
) STRICT;

CREATE UNIQUE INDEX agent_harness_versions_active ON agent_harness_versions (active) WHERE active = 1;

-- The version a turn ran with; 0 is the empty harness and every turn before it.
ALTER TABLE chat_turns ADD COLUMN harness_version INTEGER NOT NULL DEFAULT 0;

CREATE TABLE agent_improve_rounds (
    id           TEXT PRIMARY KEY,
    started_at   TEXT NOT NULL,
    finished_at  TEXT NOT NULL DEFAULT '',
    base_version INTEGER NOT NULL DEFAULT 0,
    -- outcome: running, adopted, kept, skipped or failed.
    outcome      TEXT NOT NULL DEFAULT 'running',
    detail       TEXT NOT NULL DEFAULT '',
    adopted      INTEGER NOT NULL DEFAULT 0,
    probes       INTEGER NOT NULL DEFAULT 0,
    record       TEXT NOT NULL DEFAULT '{}'
) STRICT;

CREATE INDEX agent_improve_rounds_started ON agent_improve_rounds (started_at);

-- A turn a round looked at under a version is not a target again under it.
CREATE TABLE agent_improve_examined (
    turn_id    TEXT NOT NULL,
    version    INTEGER NOT NULL,
    round_id   TEXT NOT NULL,
    PRIMARY KEY (turn_id, version)
) STRICT;

CREATE TABLE agent_lessons (
    id         TEXT PRIMARY KEY,
    cue        TEXT NOT NULL,
    lesson     TEXT NOT NULL,
    evidence   TEXT NOT NULL,
    confidence TEXT NOT NULL CHECK (confidence IN ('low', 'medium', 'high')),
    round_id   TEXT NOT NULL,
    -- version is the version the round adopted, 0 when it kept the base.
    version    INTEGER NOT NULL DEFAULT 0,
    -- undone marks a lesson whose version was rolled back.
    undone     INTEGER NOT NULL DEFAULT 0 CHECK (undone IN (0, 1)),
    created_at TEXT NOT NULL
) STRICT;
