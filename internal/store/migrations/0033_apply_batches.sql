-- The journal of apply lists (apply-chain, "List driving and the batch
-- journal"): one row per batch with the request it was started from, one row
-- per listed posting with its state. A row is `started` before its chain runs,
-- so a process that dies mid-row leaves it started, which a later list reads as
-- "possibly submitted" and re-runs by nothing. Closed batches are deleted after
-- 7 days. Timestamps are RFC 3339 UTC text with millisecond precision. A row
-- holds the posting address the user's tracker holds and no form value.
CREATE TABLE apply_batches (
    batch_id    TEXT PRIMARY KEY CHECK (batch_id <> ''),
    created_at  TEXT NOT NULL,
    deadline_at TEXT NOT NULL,
    closed_at   TEXT,
    caller      TEXT NOT NULL,
    tool        TEXT NOT NULL,
    submit      INTEGER NOT NULL CHECK (submit IN (0, 1)),
    explicit    INTEGER NOT NULL CHECK (explicit IN (0, 1)),
    request     TEXT NOT NULL DEFAULT '{}'
) STRICT;

CREATE INDEX apply_batches_closed ON apply_batches (closed_at);

CREATE TABLE apply_batch_rows (
    batch_id     TEXT NOT NULL REFERENCES apply_batches (batch_id) ON DELETE CASCADE,
    idx          INTEGER NOT NULL CHECK (idx >= 0),
    url          TEXT NOT NULL,
    employer_url TEXT NOT NULL DEFAULT '',
    company      TEXT NOT NULL DEFAULT '',
    role         TEXT NOT NULL DEFAULT '',
    named        INTEGER NOT NULL DEFAULT 0 CHECK (named IN (0, 1)),
    mirrors      INTEGER NOT NULL DEFAULT 0 CHECK (mirrors >= 0),
    state        TEXT NOT NULL DEFAULT 'pending' CHECK (state IN ('pending', 'started', 'ended')),
    run_id       TEXT NOT NULL DEFAULT '',
    -- note says why a row has no run: a skip rule, a time budget, an aborted list.
    note         TEXT NOT NULL DEFAULT '',
    updated_at   TEXT NOT NULL,
    PRIMARY KEY (batch_id, idx)
) STRICT;

CREATE INDEX apply_batch_rows_url ON apply_batch_rows (url, state);

-- What a retry and the list's holds need from a run without parsing its JSON.
ALTER TABLE apply_runs ADD COLUMN company  TEXT NOT NULL DEFAULT '';
ALTER TABLE apply_runs ADD COLUMN role     TEXT NOT NULL DEFAULT '';
ALTER TABLE apply_runs ADD COLUMN submit   INTEGER NOT NULL DEFAULT 0 CHECK (submit IN (0, 1));
ALTER TABLE apply_runs ADD COLUMN batch_id TEXT;
CREATE INDEX apply_runs_batch ON apply_runs (batch_id);
