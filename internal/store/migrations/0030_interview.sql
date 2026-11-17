-- Interview preparation (application-documents). Stages, feedback, mock turns
-- and pack notices are an append-only event log of at most 16,000 bytes per
-- event; a pack revision is stored whole in its own write-once table because
-- its bounds exceed an event. Only the candidacy link may change afterwards,
-- so that merging two candidacies keeps the history.

CREATE TABLE interview_events (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    candidacy_id INTEGER NOT NULL REFERENCES tracker_entries (id),
    kind         TEXT NOT NULL CHECK (kind IN ('stage', 'pack', 'feedback', 'mock-turn')),
    stage_id     TEXT NOT NULL,
    body         TEXT NOT NULL CHECK (length(CAST(body AS BLOB)) <= 16000),
    created_at   TEXT NOT NULL
) STRICT;

CREATE INDEX interview_events_candidacy ON interview_events (candidacy_id, id);

CREATE TRIGGER interview_events_append_only BEFORE UPDATE ON interview_events
WHEN NEW.id IS NOT OLD.id OR NEW.kind IS NOT OLD.kind OR NEW.stage_id IS NOT OLD.stage_id OR NEW.body IS NOT OLD.body OR NEW.created_at IS NOT OLD.created_at
BEGIN
    SELECT RAISE(ABORT, 'an interview event is written once');
END;

CREATE TRIGGER interview_events_no_delete BEFORE DELETE ON interview_events
BEGIN
    SELECT RAISE(ABORT, 'an interview event is written once');
END;

-- Revisions are not unique by key: merging two candidacies moves both sets of
-- packs onto one row, and a merge must not fail on a repeated revision number.
CREATE TABLE interview_packs (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    candidacy_id INTEGER NOT NULL REFERENCES tracker_entries (id),
    stage_id     TEXT NOT NULL,
    revision     INTEGER NOT NULL CHECK (revision >= 1),
    body         TEXT NOT NULL,
    created_at   TEXT NOT NULL
) STRICT;

CREATE INDEX interview_packs_stage ON interview_packs (candidacy_id, stage_id, revision);

CREATE TRIGGER interview_packs_write_once BEFORE UPDATE ON interview_packs
WHEN NEW.id IS NOT OLD.id OR NEW.stage_id IS NOT OLD.stage_id OR NEW.revision IS NOT OLD.revision OR NEW.body IS NOT OLD.body OR NEW.created_at IS NOT OLD.created_at
BEGIN
    SELECT RAISE(ABORT, 'a pack revision is never edited');
END;

-- The document run table gains the interview-prep action. SQLite cannot widen
-- a CHECK, so the table is rebuilt; nothing references it by foreign key.
CREATE TABLE document_jobs_new (
    run_id       TEXT PRIMARY KEY REFERENCES runs (id),
    kind         TEXT NOT NULL CHECK (kind IN ('resume', 'cover-letter', 'pair', 'pdf', 'interview')),
    candidacy_id INTEGER,
    outcome      TEXT CHECK (outcome IS NULL OR outcome IN ('published', 'failed')),
    detail_json  TEXT NOT NULL DEFAULT '{}',
    created_at   TEXT NOT NULL,
    finished_at  TEXT
) STRICT;

INSERT INTO document_jobs_new (run_id, kind, candidacy_id, outcome, detail_json, created_at, finished_at)
SELECT run_id, kind, candidacy_id, outcome, detail_json, created_at, finished_at FROM document_jobs;

DROP TABLE document_jobs;
ALTER TABLE document_jobs_new RENAME TO document_jobs;
