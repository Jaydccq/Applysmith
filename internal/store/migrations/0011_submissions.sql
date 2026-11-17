-- The submissions ledger: one row per application that was submitted, however
-- the system learned of it. The apply-safety-gates duplicate guard and press
-- claim read and write the same table. Timestamps are RFC 3339 UTC text with
-- millisecond precision so they sort as text. No row holds a form value.
CREATE TABLE submissions (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    entry_id     TEXT UNIQUE CHECK (entry_id IS NULL OR length(entry_id) BETWEEN 1 AND 100),
    identity_key TEXT NOT NULL,
    url          TEXT NOT NULL,
    host         TEXT NOT NULL,
    company      TEXT NOT NULL DEFAULT '',
    role         TEXT NOT NULL DEFAULT '',
    title        TEXT NOT NULL DEFAULT '',
    score        REAL,
    report_number INTEGER,
    state        TEXT NOT NULL CHECK (state IN ('submitting', 'submitted', 'failed', 'may-have-submitted')),
    confirmed    INTEGER NOT NULL DEFAULT 0 CHECK (confirmed IN (0, 1)),
    source       TEXT NOT NULL,
    observed_at  TEXT NOT NULL,
    created_at   TEXT NOT NULL,
    updated_at   TEXT NOT NULL
) STRICT;

CREATE INDEX submissions_identity ON submissions (identity_key);
