-- Scheduler history (scheduler spec). One row per decision about an
-- occurrence of a schedule, plus the informational rows that explain a
-- deferral or the end of a rule. The schedules, runs and resource_leases
-- tables are in 0001_core.sql; a fired occurrence is also a runs row keyed by
-- (schedule_id, scheduled_for), which is what keeps it from starting twice.
--
-- outcome: fired, manual, skipped and interrupted are the four outcomes of the
-- spec. deferred (an occurrence that stays due) and ended (the rule has no
-- further occurrence and the schedule was disabled) are informational and never
-- count as the decision of an occurrence.
CREATE TABLE schedule_history (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    schedule_id   INTEGER NOT NULL REFERENCES schedules (id) ON DELETE CASCADE,
    scheduled_for TEXT NOT NULL,
    outcome       TEXT NOT NULL CHECK (outcome IN ('fired', 'manual', 'skipped', 'interrupted', 'deferred', 'ended')),
    reason        TEXT NOT NULL DEFAULT '',
    detail        TEXT NOT NULL DEFAULT '',
    run_id        TEXT,
    started_at    TEXT,
    finished_at   TEXT,
    attempts      INTEGER NOT NULL DEFAULT 0,
    summary       TEXT NOT NULL DEFAULT '',
    created_at    TEXT NOT NULL
) STRICT;

-- Each occurrence has exactly one decision.
CREATE UNIQUE INDEX schedule_history_decision ON schedule_history (schedule_id, scheduled_for)
    WHERE outcome IN ('fired', 'skipped', 'interrupted');
-- A deferral is written once per occurrence, not once per tick.
CREATE UNIQUE INDEX schedule_history_deferred ON schedule_history (schedule_id, scheduled_for)
    WHERE outcome = 'deferred';
CREATE INDEX schedule_history_recent ON schedule_history (schedule_id, id DESC);
CREATE INDEX schedule_history_created ON schedule_history (created_at);
