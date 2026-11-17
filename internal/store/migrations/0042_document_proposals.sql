-- Document refine (application-documents, "Document commands and API"). A
-- proposal is a model's revision of one stored version, kept apart from the
-- version until the user accepts it. At most one proposal per version is
-- pending, and a pending one blocks that version's PDF. A failed revision is
-- kept as a row too, so the conversation can show its error turn.
CREATE TABLE document_proposals (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    document_id INTEGER NOT NULL REFERENCES documents (id) ON DELETE CASCADE,
    run_id      TEXT,
    status      TEXT NOT NULL CHECK (status IN ('pending', 'accepted', 'discarded', 'failed')),
    instruction TEXT NOT NULL CHECK (length(instruction) BETWEEN 1 AND 2000),
    -- The prior turns the request carried, as sent.
    turns_json  TEXT NOT NULL DEFAULT '[]',
    -- The proposed structured content, its readable text and the line diff
    -- against the version it revises. Empty on a failed proposal.
    content_json TEXT NOT NULL DEFAULT '',
    text        TEXT NOT NULL DEFAULT '',
    diff_json   TEXT NOT NULL DEFAULT '[]',
    error       TEXT NOT NULL DEFAULT '',
    created_at  TEXT NOT NULL,
    decided_at  TEXT
) STRICT;

CREATE INDEX document_proposals_document ON document_proposals (document_id, id);
CREATE UNIQUE INDEX document_proposals_pending ON document_proposals (document_id) WHERE status = 'pending';

-- The document run table gains the refine action. SQLite cannot widen a
-- CHECK, so the table is rebuilt; nothing references it by foreign key.
CREATE TABLE document_jobs_new (
    run_id       TEXT PRIMARY KEY REFERENCES runs (id),
    kind         TEXT NOT NULL CHECK (kind IN ('resume', 'cover-letter', 'pair', 'pdf', 'interview', 'refine')),
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
