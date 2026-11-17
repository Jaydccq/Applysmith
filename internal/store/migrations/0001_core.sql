-- Phase 0-2 core tables. Timestamps are RFC 3339 UTC text. This file may be
-- rewritten until the Phase 2 exit check (development databases are wiped);
-- from then on, changes ship as new migration files.

CREATE TABLE workspace_meta (
    key   TEXT PRIMARY KEY,
    value TEXT NOT NULL
) STRICT;

CREATE TABLE schedules (
    id                INTEGER PRIMARY KEY,
    name              TEXT NOT NULL UNIQUE CHECK (length(name) BETWEEN 1 AND 80),
    job_kind          TEXT NOT NULL,
    inputs_json       TEXT NOT NULL DEFAULT '{}',
    preset            TEXT NOT NULL DEFAULT '',
    enabled           INTEGER NOT NULL DEFAULT 0 CHECK (enabled IN (0, 1)),
    missed_policy     TEXT NOT NULL DEFAULT 'skip' CHECK (missed_policy IN ('skip', 'fire-once', 'fire-all')),
    dtstart           TEXT NOT NULL,
    pack_id           TEXT,
    pack_key          TEXT,
    last_fired_at     TEXT,
    last_evaluated_at TEXT,
    active_run_id     TEXT,
    last_result       TEXT CHECK (last_result IN ('running', 'ok', 'failed', 'skipped')),
    last_skip_reason  TEXT,
    created_at        TEXT NOT NULL,
    updated_at        TEXT NOT NULL
) STRICT;

CREATE TABLE runs (
    id             TEXT PRIMARY KEY,
    kind           TEXT NOT NULL,
    status         TEXT NOT NULL CHECK (status IN ('queued', 'running', 'succeeded', 'failed', 'cancelled', 'review_required')),
    stage          TEXT NOT NULL DEFAULT '',
    failure_reason TEXT NOT NULL DEFAULT '',
    input_json     TEXT NOT NULL DEFAULT '{}',
    schedule_id    INTEGER REFERENCES schedules (id) ON DELETE SET NULL,
    scheduled_for  TEXT,
    created_at     TEXT NOT NULL,
    updated_at     TEXT NOT NULL,
    finished_at    TEXT,
    -- One run per scheduled occurrence: a second evaluation cannot start it twice.
    UNIQUE (schedule_id, scheduled_for)
) STRICT;

CREATE INDEX runs_status ON runs (status);

CREATE TABLE idempotency_keys (
    principal  TEXT NOT NULL,
    operation  TEXT NOT NULL,
    key        TEXT NOT NULL,
    input_hash TEXT NOT NULL,
    run_id     TEXT NOT NULL REFERENCES runs (id),
    created_at TEXT NOT NULL,
    PRIMARY KEY (principal, operation, key)
) STRICT;

CREATE TABLE tracker_entries (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    date          TEXT NOT NULL,
    company       TEXT NOT NULL CHECK (trim(company) <> ''),
    role          TEXT NOT NULL CHECK (trim(role) <> ''),
    score         REAL CHECK (score IS NULL OR (score >= 0.0 AND score <= 5.0)),
    score_kind    TEXT CHECK (score_kind IN ('final', 'screening')),
    status        TEXT NOT NULL CHECK (status IN ('evaluated', 'skipped', 'applied', 'responded', 'interview', 'offer', 'rejected', 'discarded')),
    applied_by    TEXT CHECK (applied_by IN ('user', 'agent')),
    applied_at    TEXT,
    resume_pdf    INTEGER NOT NULL DEFAULT 0 CHECK (resume_pdf IN (0, 1)),
    cover_letter  INTEGER NOT NULL DEFAULT 0 CHECK (cover_letter IN (0, 1)),
    report_number INTEGER,
    notes         TEXT NOT NULL DEFAULT '',
    posting_url   TEXT NOT NULL DEFAULT '',
    -- The place the posting lists; two rows of one role in places that share
    -- no token are two openings.
    location      TEXT NOT NULL DEFAULT '',
    source        TEXT,
    identity_key  TEXT NOT NULL CHECK (identity_key <> ''),
    archived_at   TEXT,
    version       INTEGER NOT NULL DEFAULT 1,
    created_at    TEXT NOT NULL,
    updated_at    TEXT NOT NULL
) STRICT;

-- A race cannot create two rows for one posting or one report.
CREATE UNIQUE INDEX tracker_entries_identity ON tracker_entries (identity_key);
CREATE UNIQUE INDEX tracker_entries_report ON tracker_entries (report_number) WHERE report_number IS NOT NULL;

-- Identity keys a row answers to besides its own: mirror reports attached to
-- it and the keys of rows merged into it. No cascade: a row with aliases
-- cannot be deleted before they are re-pointed.
CREATE TABLE tracker_aliases (
    identity_key TEXT PRIMARY KEY CHECK (identity_key <> ''),
    entry_id     INTEGER NOT NULL REFERENCES tracker_entries (id)
) STRICT;

CREATE INDEX tracker_aliases_entry ON tracker_aliases (entry_id);

CREATE TABLE tracker_events (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    entry_id        INTEGER NOT NULL REFERENCES tracker_entries (id),
    at              TEXT NOT NULL,
    actor           TEXT NOT NULL,
    kind            TEXT NOT NULL CHECK (kind IN (
        'created', 'status_changed', 'status_corrected', 'transition_refused',
        'score_updated', 'report_linked', 'note_added', 'document_flag_set',
        'document_flag_cleared', 'documents_override', 'followup_recorded',
        'followup_snoozed', 'archived', 'unarchived', 'merged',
        'duplicate_attached', 'applied_by_changed', 'applied_date_set')),
    previous_status TEXT,
    new_status      TEXT,
    detail_json     TEXT NOT NULL DEFAULT '{}'
) STRICT;

CREATE INDEX tracker_events_entry ON tracker_events (entry_id, id);

-- Events are append-only. The one allowed change is re-pointing an event to
-- the surviving row of a merge.
CREATE TRIGGER tracker_events_no_rewrite BEFORE UPDATE ON tracker_events
WHEN NEW.id IS NOT OLD.id OR NEW.at IS NOT OLD.at OR NEW.actor IS NOT OLD.actor OR NEW.kind IS NOT OLD.kind
  OR NEW.previous_status IS NOT OLD.previous_status OR NEW.new_status IS NOT OLD.new_status
  OR NEW.detail_json IS NOT OLD.detail_json
BEGIN
    SELECT RAISE(ABORT, 'tracker events are append-only');
END;

CREATE TRIGGER tracker_events_no_delete BEFORE DELETE ON tracker_events
BEGIN
    SELECT RAISE(ABORT, 'tracker events are append-only');
END;

-- Submission ledger. It fails closed: while a posting has an open claim
-- (submitting, submitted or may-have-submitted) no second claim can be
-- written. Lifting uncertain evidence (resubmit, correction) first marks the
-- old row superseded in the same transaction that inserts the new one.
CREATE TABLE submit_claims (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    identity_key TEXT NOT NULL CHECK (identity_key <> ''),
    run_id       TEXT NOT NULL REFERENCES runs (id),
    status       TEXT NOT NULL CHECK (status IN ('submitting', 'submitted', 'failed', 'may-have-submitted')),
    confirmed    INTEGER NOT NULL DEFAULT 0 CHECK (confirmed IN (0, 1)),
    superseded   INTEGER NOT NULL DEFAULT 0 CHECK (superseded IN (0, 1)),
    supersedes   INTEGER REFERENCES submit_claims (id),
    detail_json  TEXT NOT NULL DEFAULT '{}',
    claimed_at   TEXT NOT NULL,
    updated_at   TEXT NOT NULL
) STRICT;

CREATE UNIQUE INDEX submit_claims_open ON submit_claims (identity_key)
    WHERE superseded = 0 AND status IN ('submitting', 'submitted', 'may-have-submitted');
CREATE INDEX submit_claims_identity ON submit_claims (identity_key);

CREATE TABLE resource_leases (
    name           TEXT PRIMARY KEY CHECK (name IN ('browser', 'llm:evaluation', 'tracker:write', 'gmail:read')),
    holder         TEXT NOT NULL,
    -- The process and the server start that took the lease; startup clears
    -- every lease of an earlier start, and an expired lease can be cleared.
    owner_pid      INTEGER NOT NULL,
    owner_instance TEXT NOT NULL,
    acquired_at    TEXT NOT NULL,
    expires_at     TEXT NOT NULL
) STRICT;

CREATE TABLE device_sessions (
    id           TEXT PRIMARY KEY,
    label        TEXT NOT NULL,
    token_sha256 TEXT NOT NULL UNIQUE,
    last_network TEXT CHECK (last_network = 'tunnel'),
    created_at   TEXT NOT NULL,
    last_seen_at TEXT,
    revoked_at   TEXT
) STRICT;

-- Report and document artifacts. The number is allocated here and never reused.
CREATE TABLE report_artifacts (
    report_number INTEGER PRIMARY KEY AUTOINCREMENT,
    kind          TEXT NOT NULL,
    path          TEXT NOT NULL UNIQUE,
    status        TEXT NOT NULL CHECK (status IN ('pending', 'ready', 'unavailable', 'conflict')),
    planned_hash  TEXT NOT NULL,
    content_hash  TEXT,
    created_at    TEXT NOT NULL,
    updated_at    TEXT NOT NULL,
    CHECK (status <> 'ready' OR content_hash IS NOT NULL)
) STRICT;

-- ---------------------------------------------------------------------------
-- Discovery (internal/discovery): registry, blocked companies, inbox, scan
-- history, scan runs.
-- ---------------------------------------------------------------------------

-- Public provenance of fetched data (slug lists, public tables). Shared by
-- every capability that downloads data at setup.
CREATE TABLE IF NOT EXISTS data_sources (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    kind         TEXT NOT NULL,
    url          TEXT NOT NULL,
    retrieved_at TEXT NOT NULL,
    sha256       TEXT NOT NULL,
    licence      TEXT NOT NULL DEFAULT ''
) STRICT;

CREATE TABLE ats_registry (
    ats            TEXT NOT NULL CHECK (ats IN ('greenhouse', 'lever', 'ashby', 'smartrecruiters', 'workable')),
    slug           TEXT NOT NULL CHECK (slug = lower(slug) AND length(slug) BETWEEN 1 AND 100),
    company        TEXT NOT NULL DEFAULT '',
    source         TEXT NOT NULL,
    first_added_at TEXT NOT NULL,
    -- For a dead row this is the time of the last probe.
    last_scanned_at TEXT,
    last_job_count INTEGER NOT NULL DEFAULT 0,
    etag           TEXT NOT NULL DEFAULT '',
    status         TEXT NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'dead', 'blocked')),
    strikes        INTEGER NOT NULL DEFAULT 0 CHECK (strikes >= 0),
    dead_since     TEXT,
    PRIMARY KEY (ats, slug)
) STRICT, WITHOUT ROWID;

CREATE INDEX ats_registry_due ON ats_registry (status, last_scanned_at);

CREATE TABLE blocked_companies (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    name       TEXT NOT NULL CHECK (trim(name) <> ''),
    normalized TEXT NOT NULL UNIQUE CHECK (normalized <> ''),
    reason     TEXT NOT NULL DEFAULT '',
    added_by   TEXT NOT NULL,
    added_at   TEXT NOT NULL
) STRICT;

-- The pipeline inbox. Dismissed and expired rows stay so that de-duplication
-- keeps working.
CREATE TABLE inbox (
    id                 INTEGER PRIMARY KEY AUTOINCREMENT,
    url                TEXT NOT NULL,
    original_url       TEXT NOT NULL,
    aliases_json       TEXT NOT NULL DEFAULT '[]',
    identity_key       TEXT NOT NULL UNIQUE CHECK (identity_key <> ''),
    company            TEXT NOT NULL DEFAULT '',
    role               TEXT NOT NULL DEFAULT '',
    location           TEXT NOT NULL DEFAULT '',
    source             TEXT NOT NULL,
    detail             TEXT NOT NULL DEFAULT '',
    score              INTEGER NOT NULL DEFAULT 0,
    reasons_json       TEXT NOT NULL DEFAULT '[]',
    early_career       INTEGER NOT NULL DEFAULT 0 CHECK (early_career IN (0, 1)),
    clearance_required INTEGER NOT NULL DEFAULT 0 CHECK (clearance_required IN (0, 1)),
    required_years     INTEGER,
    flags_json         TEXT NOT NULL DEFAULT '[]',
    description        TEXT NOT NULL DEFAULT '',
    posted_at          TEXT,
    notes              TEXT NOT NULL DEFAULT '',
    status             TEXT NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'queued', 'evaluated', 'dismissed', 'expired')),
    attempts           INTEGER NOT NULL DEFAULT 0,
    job_id             TEXT,
    run_id             TEXT,
    first_seen_at      TEXT NOT NULL,
    updated_at         TEXT NOT NULL
) STRICT;

CREATE INDEX inbox_status ON inbox (status, score DESC);
CREATE INDEX inbox_job ON inbox (job_id) WHERE job_id IS NOT NULL;

-- One row per canonical URL.
CREATE TABLE scan_history (
    canonical_url TEXT PRIMARY KEY,
    identity_key  TEXT NOT NULL,
    first_seen_at TEXT NOT NULL,
    last_seen_at  TEXT NOT NULL,
    source        TEXT NOT NULL,
    title         TEXT NOT NULL DEFAULT '',
    company       TEXT NOT NULL DEFAULT '',
    location      TEXT NOT NULL DEFAULT '',
    status        TEXT NOT NULL CHECK (status IN ('inbox', 'duplicate', 'expired', 'filtered')),
    score         INTEGER,
    last_run_id   TEXT NOT NULL
) STRICT, WITHOUT ROWID;

CREATE INDEX scan_history_key ON scan_history (identity_key);

CREATE TABLE scan_runs (
    id          TEXT PRIMARY KEY,
    source      TEXT NOT NULL,
    status      TEXT NOT NULL CHECK (status IN ('running', 'completed', 'failed', 'skipped', 'blocked', 'needs-sign-in', 'interrupted', 'cancelled')),
    reason      TEXT NOT NULL DEFAULT '',
    error       TEXT NOT NULL DEFAULT '',
    started_at  TEXT NOT NULL,
    updated_at  TEXT NOT NULL,
    finished_at TEXT,
    duration_ms INTEGER,
    record_json TEXT NOT NULL DEFAULT '{}'
) STRICT;

-- One run of a source at a time.
CREATE UNIQUE INDEX scan_runs_running ON scan_runs (source) WHERE status = 'running';
CREATE INDEX scan_runs_started ON scan_runs (started_at DESC);

-- Evaluation jobs a run enqueued, so outcomes can be attributed later.
CREATE TABLE scan_run_jobs (
    job_id TEXT PRIMARY KEY,
    run_id TEXT NOT NULL REFERENCES scan_runs (id)
) STRICT;

-- ===========================================================================
-- Evaluation (job-evaluation). Everything below belongs to the evaluation
-- capability; discovery's tables sit in their own section.
-- ===========================================================================

-- One row per evaluation job. The job is a run; the phase is runs.stage and
-- the terminal outcome is stored here.
CREATE TABLE evaluation_jobs (
    run_id         TEXT PRIMARY KEY REFERENCES runs (id),
    identity_key   TEXT NOT NULL CHECK (identity_key <> ''),
    mode           TEXT NOT NULL CHECK (mode IN ('auto', 'screen', 'deep')),
    priority       TEXT NOT NULL CHECK (priority IN ('user', 'scan')),
    source         TEXT NOT NULL DEFAULT '',
    posting_url    TEXT NOT NULL DEFAULT '',
    inbox_row_id   INTEGER,
    inbox_released INTEGER NOT NULL DEFAULT 0 CHECK (inbox_released IN (0, 1)),
    outcome        TEXT CHECK (outcome IS NULL OR outcome IN ('reported', 'screened', 'gated', 'duplicate', 'failed')),
    report_number  INTEGER,
    -- Decision, reason and the prompt hashes the job used.
    detail_json    TEXT NOT NULL DEFAULT '{}',
    created_at     TEXT NOT NULL,
    finished_at    TEXT
) STRICT;

CREATE INDEX evaluation_jobs_identity ON evaluation_jobs (identity_key);

-- Report index. The number is the report_artifacts number, allocated once and
-- never reused. A row is written together with the tracker upsert.
CREATE TABLE report_index (
    report_number      INTEGER PRIMARY KEY REFERENCES report_artifacts (report_number),
    kind               TEXT NOT NULL CHECK (kind IN ('final', 'screening', 'gated')),
    company            TEXT NOT NULL,
    role               TEXT NOT NULL,
    posting_url        TEXT NOT NULL DEFAULT '',
    identity_key       TEXT NOT NULL CHECK (identity_key <> ''),
    score              REAL CHECK (score IS NULL OR (score >= 0.0 AND score <= 5.0)),
    score_kind         TEXT NOT NULL CHECK (score_kind IN ('final', 'screening')),
    recommendation     TEXT NOT NULL CHECK (recommendation IN ('skip', 'consider', 'apply', 'strong')),
    legitimacy         TEXT NOT NULL CHECK (legitimacy IN ('credible', 'unclear', 'doubtful', 'unassessed')),
    verification       TEXT NOT NULL,
    role_family        TEXT NOT NULL DEFAULT '',
    markdown_path      TEXT NOT NULL,
    html_path          TEXT NOT NULL,
    job_id             TEXT NOT NULL,
    documents_eligible INTEGER NOT NULL DEFAULT 0 CHECK (documents_eligible IN (0, 1)),
    created_at         TEXT NOT NULL
) STRICT;

CREATE INDEX report_index_identity ON report_index (identity_key, kind);
CREATE INDEX report_index_created ON report_index (created_at);

-- Historical H-1B petitions per normalized employer name and fiscal year.
CREATE TABLE h1b_employers (
    employer_key         TEXT NOT NULL,
    fiscal_year          INTEGER NOT NULL,
    initial_approvals    INTEGER NOT NULL DEFAULT 0,
    initial_denials      INTEGER NOT NULL DEFAULT 0,
    continuing_approvals INTEGER NOT NULL DEFAULT 0,
    continuing_denials   INTEGER NOT NULL DEFAULT 0,
    sample_name          TEXT NOT NULL,
    PRIMARY KEY (employer_key, fiscal_year)
) STRICT;
