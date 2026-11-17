-- Gmail integration (gmail-integration spec). Every piece of Gmail-derived data
-- lives in these tables and nowhere else: no workspace file and no ledger
-- holds any of it. Timestamps are RFC 3339 UTC text with millisecond
-- precision so they sort as text. History ids are Gmail's decimal strings and
-- are compared as integers by the code, never by SQL.
--
-- The OAuth client and the refresh token are keychain secrets; this schema
-- stores neither, nor any access token.

-- Small facts keyed by name: mailbox address, authorization status and the
-- provider's last refresh error, token issue time, consent-screen record,
-- history cursor, cursor reset time, bootstrap resume token.
CREATE TABLE gmail_state (
    key   TEXT PRIMARY KEY,
    value TEXT NOT NULL
) STRICT;

-- One durable record per sync, resync or reclassify attempt.
CREATE TABLE gmail_runs (
    id                 INTEGER PRIMARY KEY AUTOINCREMENT,
    trigger_kind       TEXT NOT NULL CHECK (trigger_kind IN ('scheduled', 'manual', 'cli')),
    status             TEXT NOT NULL CHECK (status IN ('running', 'succeeded', 'failed')),
    mode               TEXT NOT NULL CHECK (mode IN ('bootstrap', 'incremental', 'fallback', 'reclassify')),
    started_at         TEXT NOT NULL,
    ended_at           TEXT,
    coverage_complete  INTEGER NOT NULL DEFAULT 0 CHECK (coverage_complete IN (0, 1)),
    fallback_used      INTEGER NOT NULL DEFAULT 0 CHECK (fallback_used IN (0, 1)),
    classifier_version TEXT NOT NULL,
    query_version      TEXT NOT NULL,
    history_start      TEXT NOT NULL DEFAULT '',
    history_end        TEXT NOT NULL DEFAULT '',
    resume_token       TEXT NOT NULL DEFAULT '',
    failed_stage       TEXT NOT NULL DEFAULT '',
    error              TEXT NOT NULL DEFAULT '',
    stages_json        TEXT NOT NULL CHECK (json_valid(stages_json)),
    counts_json        TEXT NOT NULL CHECK (json_valid(counts_json))
) STRICT;

CREATE INDEX gmail_runs_started ON gmail_runs (started_at);

-- Immutable per-message metadata, written once for a message that produced a
-- signal. A message that matched no event rule has no row here: only its
-- decision is kept. The snippet is the only message text kept for good.
CREATE TABLE gmail_evidence (
    message_id    TEXT PRIMARY KEY,
    thread_id     TEXT NOT NULL,
    history_id    TEXT NOT NULL,
    received_ms   INTEGER NOT NULL,
    from_header   TEXT NOT NULL,
    to_header     TEXT NOT NULL,
    cc_header     TEXT NOT NULL,
    subject       TEXT NOT NULL,
    snippet       TEXT NOT NULL CHECK (length(snippet) <= 220),
    labels        TEXT NOT NULL,
    first_run_id  INTEGER NOT NULL REFERENCES gmail_runs (id),
    first_seen_at TEXT NOT NULL
) STRICT;

CREATE INDEX gmail_evidence_received ON gmail_evidence (received_ms);
CREATE INDEX gmail_evidence_thread ON gmail_evidence (thread_id);

-- Message bodies, kept for reclassification only and removed by the retention
-- sweep after 30 days or when the total passes 200 MB, oldest first.
CREATE TABLE gmail_bodies (
    message_id TEXT PRIMARY KEY REFERENCES gmail_evidence (message_id) ON DELETE CASCADE,
    body_text  TEXT NOT NULL,
    raw_text   TEXT NOT NULL,
    bytes      INTEGER NOT NULL,
    stored_at  TEXT NOT NULL
) STRICT;

CREATE INDEX gmail_bodies_stored ON gmail_bodies (stored_at);

-- The classifier's verdict per message and classifier version. An ignored
-- message keeps only its id, outcome and reason.
CREATE TABLE gmail_decisions (
    message_id         TEXT NOT NULL,
    classifier_version TEXT NOT NULL,
    outcome            TEXT NOT NULL CHECK (outcome IN ('signal', 'ignored')),
    reason             TEXT NOT NULL DEFAULT '',
    signal_type        TEXT NOT NULL DEFAULT '',
    tier               TEXT NOT NULL DEFAULT '' CHECK (tier IN ('', 'actionable', 'review', 'low_priority', 'suppressed')),
    direction          TEXT NOT NULL DEFAULT '' CHECK (direction IN ('', 'incoming', 'outgoing')),
    employer           TEXT NOT NULL DEFAULT '',
    role               TEXT NOT NULL DEFAULT '',
    confidence         REAL NOT NULL DEFAULT 0,
    due_ms             INTEGER,
    rule_id            TEXT NOT NULL DEFAULT '',
    review_reasons     TEXT NOT NULL DEFAULT '',
    detail_json        TEXT NOT NULL DEFAULT '{}' CHECK (json_valid(detail_json)),
    run_id             INTEGER NOT NULL REFERENCES gmail_runs (id),
    decided_at         TEXT NOT NULL,
    PRIMARY KEY (message_id, classifier_version)
) STRICT;

CREATE INDEX gmail_decisions_decided ON gmail_decisions (decided_at);

-- A message Gmail reported as deleted. Its signals leave every view and stay
-- here for audit; tracker statuses already written from them are untouched.
CREATE TABLE gmail_tombstones (
    message_id    TEXT PRIMARY KEY,
    tombstoned_at TEXT NOT NULL,
    run_id        INTEGER REFERENCES gmail_runs (id)
) STRICT;

-- Who reset the cursor, reclassified or purged, and how it ended.
CREATE TABLE gmail_audit (
    id      INTEGER PRIMARY KEY AUTOINCREMENT,
    at      TEXT NOT NULL,
    actor   TEXT NOT NULL,
    command TEXT NOT NULL,
    outcome TEXT NOT NULL
) STRICT;
