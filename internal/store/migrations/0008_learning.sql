-- Answer-bank learning (answer-bank spec: the learning candidates, manual fills
-- as ground truth, the event upload route and usage telemetry). Timestamps are
-- RFC 3339 UTC text with millisecond precision so they sort as text.
--
-- A value column is NULL whenever the control is value-withheld; the
-- `withheld` column then names the class. Nothing here stores a password, a
-- one-time code, a payment or government-identifier value, a
-- self-identification answer, a date of birth or a profile-identity value.

-- Every accepted upload, by the client's event id: the idempotency record, and
-- the whole record of the kinds that carry no row of their own.
CREATE TABLE learning_uploads (
    event_id      TEXT PRIMARY KEY CHECK (length(event_id) BETWEEN 1 AND 100),
    kind          TEXT NOT NULL CHECK (kind IN ('uncertainty', 'correction', 'user-reported', 'manual-fill', 'unsafe-control')),
    host          TEXT NOT NULL,
    question      TEXT NOT NULL,
    control_kind  TEXT NOT NULL,
    at            TEXT NOT NULL,
    received_at   TEXT NOT NULL,
    reason        TEXT NOT NULL DEFAULT '',
    report_number INTEGER,
    run_id        TEXT NOT NULL DEFAULT '',
    report_json   TEXT
) STRICT;

CREATE INDEX learning_uploads_received ON learning_uploads (received_at);

-- What the user typed or chose themselves. The server never writes a row for
-- a value the system's own fill produced.
CREATE TABLE manual_fills (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    event_id      TEXT UNIQUE,
    at            TEXT NOT NULL,
    received_at   TEXT NOT NULL,
    host          TEXT NOT NULL,
    question      TEXT NOT NULL,
    section_path  TEXT NOT NULL DEFAULT '',
    control_kind  TEXT NOT NULL,
    options_json  TEXT NOT NULL DEFAULT '[]',
    value         TEXT,
    option_label  TEXT,
    withheld      TEXT NOT NULL DEFAULT '',
    -- 0 when the value's shape contradicts a short identity label.
    ground_truth  INTEGER NOT NULL DEFAULT 1 CHECK (ground_truth IN (0, 1)),
    prior_value   TEXT,
    entry_id      TEXT NOT NULL DEFAULT '',
    run_id        TEXT NOT NULL DEFAULT '',
    report_number INTEGER
) STRICT;

CREATE INDEX manual_fills_at ON manual_fills (at);

-- Telemetry and the abstain, composed-fill and correction evidence.
CREATE TABLE answer_events (
    id             INTEGER PRIMARY KEY AUTOINCREMENT,
    kind           TEXT NOT NULL CHECK (kind IN ('match', 'applied', 'abstained', 'user-corrected', 'candidate-approved', 'candidate-rejected', 'abstain-reason')),
    at             TEXT NOT NULL,
    host           TEXT NOT NULL DEFAULT '',
    question       TEXT NOT NULL DEFAULT '',
    section_path   TEXT NOT NULL DEFAULT '',
    control_kind   TEXT NOT NULL DEFAULT '',
    options_json   TEXT NOT NULL DEFAULT '[]',
    entry_id       TEXT NOT NULL DEFAULT '',
    answer_key     TEXT NOT NULL DEFAULT '',
    outcome        TEXT NOT NULL DEFAULT '',
    confidence     REAL,
    lane           TEXT NOT NULL DEFAULT '' CHECK (lane IN ('', 'answer-bank', 'model-option', 'composed', 'manual')),
    reason         TEXT NOT NULL DEFAULT '',
    value          TEXT,
    option_label   TEXT,
    withheld       TEXT NOT NULL DEFAULT '',
    origin         TEXT NOT NULL DEFAULT '',
    -- A correction counts toward its entry's stale rule; a control failure
    -- (the right answer driven the wrong way) is stored with 0.
    counted        INTEGER NOT NULL DEFAULT 0 CHECK (counted IN (0, 1)),
    run_id         TEXT NOT NULL DEFAULT '',
    report_number  INTEGER,
    -- The manual fill a correction was made from; deleting the fill deletes it.
    fill_id        INTEGER REFERENCES manual_fills (id) ON DELETE CASCADE
) STRICT;

CREATE INDEX answer_events_at ON answer_events (at);
CREATE INDEX answer_events_kind ON answer_events (kind, at);
CREATE INDEX answer_events_entry ON answer_events (entry_id, kind);
CREATE UNIQUE INDEX answer_events_fill ON answer_events (fill_id) WHERE fill_id IS NOT NULL;

-- Proposals only. A pending candidate is never deleted by a generation run.
CREATE TABLE answer_candidates (
    id               INTEGER PRIMARY KEY AUTOINCREMENT,
    key              TEXT NOT NULL UNIQUE,
    type             TEXT NOT NULL CHECK (type IN ('new-entry', 'extend-examples')),
    state            TEXT NOT NULL DEFAULT 'pending' CHECK (state IN ('pending', 'approved', 'rejected')),
    source           TEXT NOT NULL CHECK (source IN ('abstain', 'composed', 'manual', 'model-fill')),
    target_entry_id  TEXT NOT NULL DEFAULT '',
    entry_id         TEXT NOT NULL DEFAULT '',
    seed_question    TEXT NOT NULL,
    questions_json   TEXT NOT NULL,
    hosts_json       TEXT NOT NULL,
    proposed_kind    TEXT NOT NULL DEFAULT '' CHECK (proposed_kind IN ('', 'fixed', 'pick-option')),
    proposed_answer  TEXT NOT NULL DEFAULT '',
    long_form        INTEGER NOT NULL DEFAULT 0 CHECK (long_form IN (0, 1)),
    flags_json       TEXT NOT NULL DEFAULT '[]',
    support_count    INTEGER NOT NULL CHECK (support_count >= 0),
    first_seen_at    TEXT NOT NULL,
    last_seen_at     TEXT NOT NULL,
    created_at       TEXT NOT NULL,
    updated_at       TEXT NOT NULL,
    decided_at       TEXT
) STRICT;

CREATE INDEX answer_candidates_state ON answer_candidates (state, updated_at);

-- The rows a candidate rests on, so deleting one lowers the support by one.
CREATE TABLE answer_candidate_evidence (
    candidate_id INTEGER NOT NULL REFERENCES answer_candidates (id) ON DELETE CASCADE,
    event_id     INTEGER REFERENCES answer_events (id) ON DELETE CASCADE,
    fill_id      INTEGER REFERENCES manual_fills (id) ON DELETE CASCADE,
    CHECK ((event_id IS NULL) <> (fill_id IS NULL))
) STRICT;

CREATE UNIQUE INDEX answer_candidate_evidence_event ON answer_candidate_evidence (candidate_id, event_id) WHERE event_id IS NOT NULL;
CREATE UNIQUE INDEX answer_candidate_evidence_fill ON answer_candidate_evidence (candidate_id, fill_id) WHERE fill_id IS NOT NULL;
CREATE INDEX answer_candidate_evidence_event_row ON answer_candidate_evidence (event_id);
CREATE INDEX answer_candidate_evidence_fill_row ON answer_candidate_evidence (fill_id);
