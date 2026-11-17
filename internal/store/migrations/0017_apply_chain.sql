-- The apply chain's durable records. Every gate decision it makes is a row of
-- gate_decisions, which migration 0015_agent declares (apply-safety-gates,
-- "Gate model, decisions and audit table"); apply_runs holds one row per run
-- with queryable columns and JSON columns for the parts that are only ever
-- read whole. Timestamps are
-- RFC 3339 UTC text with millisecond precision. No row holds a password, a
-- one-time code, a token or a form value.
CREATE TABLE apply_runs (
    run_id          TEXT PRIMARY KEY CHECK (run_id <> ''),
    started_at      TEXT NOT NULL,
    finished_at     TEXT,
    caller          TEXT NOT NULL,
    posting_url     TEXT NOT NULL,
    identity_key    TEXT NOT NULL,
    ats             TEXT NOT NULL DEFAULT '',
    tenant          TEXT NOT NULL DEFAULT '',
    report_number   INTEGER,
    outcome_kind    TEXT NOT NULL DEFAULT 'running' CHECK (outcome_kind IN ('running', 'submitted', 'filled', 'stopped')),
    outcome_stage   TEXT NOT NULL DEFAULT '',
    outcome_class   TEXT NOT NULL DEFAULT '',
    outcome_reason  TEXT NOT NULL DEFAULT '',
    confirmed       INTEGER NOT NULL DEFAULT 0 CHECK (confirmed IN (0, 1)),
    confirm_source  TEXT NOT NULL DEFAULT '',
    build           TEXT NOT NULL DEFAULT '',
    trigger_conversation_id TEXT,
    trigger_message_hash    TEXT,
    correction      INTEGER NOT NULL DEFAULT 0 CHECK (correction IN (0, 1)),
    superseded_id   TEXT,
    stages_json     TEXT NOT NULL DEFAULT '[]',
    pages_json      TEXT NOT NULL DEFAULT '[]',
    signins_json    TEXT NOT NULL DEFAULT '[]',
    ladder_json     TEXT NOT NULL DEFAULT '{}',
    budget_json     TEXT NOT NULL DEFAULT '{}',
    captcha_json    TEXT NOT NULL DEFAULT '[]',
    learning_events INTEGER NOT NULL DEFAULT 0
) STRICT;

CREATE INDEX apply_runs_identity ON apply_runs (identity_key, started_at);
CREATE INDEX apply_runs_started ON apply_runs (started_at);

-- The chain's own claim and verdict columns on the submissions ledger.
ALTER TABLE submissions ADD COLUMN run_id TEXT;
ALTER TABLE submissions ADD COLUMN confirm_source TEXT;
ALTER TABLE submissions ADD COLUMN receipt_message_id TEXT;
ALTER TABLE submissions ADD COLUMN detail TEXT;

-- Every sign-up attempt the chain made, whatever its outcome: the account
-- creation gate counts them against the weekly quota.
CREATE TABLE signup_attempts (
    id     INTEGER PRIMARY KEY AUTOINCREMENT,
    system TEXT NOT NULL,
    host   TEXT NOT NULL,
    run_id TEXT NOT NULL,
    at     TEXT NOT NULL
) STRICT;

CREATE INDEX signup_attempts_at ON signup_attempts (system, at);
