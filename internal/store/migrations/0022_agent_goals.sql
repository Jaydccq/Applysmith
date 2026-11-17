-- Job-search goals of the Ask Agent and the plan changes the agent proposes
-- (ask-agent: "Job-search goals", "Goal plan changes are proposals"). Times are
-- RFC 3339 UTC text with millisecond precision; due_date is a local calendar
-- date (YYYY-MM-DD) or empty. No gate reads these tables.

-- checkin_days is a comma list of sun..sat and checkin_time is HH:MM in the
-- server's zone; both are empty for a goal without a cadence. active_since is
-- the creation or the last resume, and last_slot the latest check-in slot that
-- was claimed, so a slot is sent at most once and never for a paused period.
CREATE TABLE agent_goals (
    id            TEXT PRIMARY KEY,
    title         TEXT NOT NULL CHECK (title <> ''),
    due_date      TEXT NOT NULL DEFAULT '',
    state         TEXT NOT NULL CHECK (state IN ('active', 'paused', 'completed', 'archived')),
    version       INTEGER NOT NULL DEFAULT 1,
    checkin_days  TEXT NOT NULL DEFAULT '',
    checkin_time  TEXT NOT NULL DEFAULT '',
    active_since  TEXT NOT NULL,
    last_slot     TEXT NOT NULL DEFAULT '',
    created_at    TEXT NOT NULL,
    updated_at    TEXT NOT NULL,
    CHECK ((checkin_days = '') = (checkin_time = ''))
) STRICT;

CREATE INDEX agent_goals_state ON agent_goals (state);

CREATE TABLE agent_goal_steps (
    goal_id        TEXT NOT NULL REFERENCES agent_goals (id) ON DELETE CASCADE,
    position       INTEGER NOT NULL CHECK (position >= 1),
    text           TEXT NOT NULL CHECK (text <> ''),
    status         TEXT NOT NULL CHECK (status IN ('pending', 'in_progress', 'done', 'blocked')),
    note           TEXT NOT NULL DEFAULT '',
    application_id TEXT NOT NULL DEFAULT '',
    company        TEXT NOT NULL DEFAULT '',
    PRIMARY KEY (goal_id, position)
) STRICT;

-- At most one pending proposal per goal: a newer one replaces the row. change
-- is the JSON of the revised remaining steps and the other proposed fields.
CREATE TABLE agent_goal_proposals (
    id         TEXT PRIMARY KEY,
    goal_id    TEXT NOT NULL UNIQUE REFERENCES agent_goals (id) ON DELETE CASCADE,
    reason     TEXT NOT NULL,
    change     TEXT NOT NULL,
    created_at TEXT NOT NULL
) STRICT;
