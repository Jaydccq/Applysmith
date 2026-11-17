-- Hiring signals. Every table is prefixed signal_. Timestamps are RFC 3339 UTC
-- text. No key, token or contact email is stored in any of them.

-- One row per canonical URL: the same article reached through two adapters is
-- one document. identified_at stays empty until the document has been turned
-- into company events, so a pass that could not reach the model leaves it for
-- the next one.
CREATE TABLE signal_documents (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    source_id     TEXT NOT NULL,
    adapter       TEXT NOT NULL,
    url           TEXT NOT NULL,
    canon_url     TEXT NOT NULL UNIQUE,
    fetched_at    TEXT NOT NULL,
    published_at  TEXT,
    title         TEXT NOT NULL,
    body          TEXT NOT NULL,
    body_dropped  INTEGER NOT NULL DEFAULT 0 CHECK (body_dropped IN (0, 1)),
    filing_json   TEXT,
    identified_at TEXT,
    last_used_at  TEXT NOT NULL
) STRICT;

CREATE INDEX signal_documents_pending ON signal_documents (id) WHERE identified_at IS NULL;

CREATE TABLE signal_companies (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    name       TEXT NOT NULL,
    canon      TEXT NOT NULL UNIQUE,
    state      TEXT NOT NULL DEFAULT 'new' CHECK (state IN ('new', 'watching', 'dismissed')),
    created_at TEXT NOT NULL
) STRICT;

-- A raw name that the rules could not canonicalize, with the model's answer
-- (empty when it named nothing), so the same name never costs a second call.
CREATE TABLE signal_aliases (
    raw        TEXT PRIMARY KEY,
    canon      TEXT NOT NULL,
    created_at TEXT NOT NULL
) STRICT;

CREATE TABLE signal_events (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    company_id INTEGER NOT NULL REFERENCES signal_companies (id),
    type       TEXT NOT NULL,
    event_time TEXT NOT NULL,
    amount     REAL,
    created_at TEXT NOT NULL
) STRICT;

CREATE INDEX signal_events_company ON signal_events (company_id, type, event_time);

-- A document attaches to at most one event of a company, and to the events of
-- several companies.
CREATE TABLE signal_event_docs (
    event_id    INTEGER NOT NULL REFERENCES signal_events (id),
    document_id INTEGER NOT NULL REFERENCES signal_documents (id),
    company_id  INTEGER NOT NULL REFERENCES signal_companies (id),
    PRIMARY KEY (event_id, document_id),
    UNIQUE (document_id, company_id)
) STRICT;

CREATE TABLE signal_assessments (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    event_id        INTEGER NOT NULL REFERENCES signal_events (id),
    rubric_version  TEXT NOT NULL,
    status          TEXT NOT NULL CHECK (status IN ('ok', 'schema_invalid', 'quote_failed', 'mid_confidence', 'contradictory')),
    event_type      TEXT NOT NULL,
    hiring_intent   TEXT NOT NULL,
    teams_json      TEXT NOT NULL DEFAULT '[]',
    use_of_funds    INTEGER CHECK (use_of_funds IN (0, 1)),
    scale           TEXT NOT NULL,
    strength        INTEGER NOT NULL,
    confidence      REAL NOT NULL,
    reasoning       TEXT NOT NULL,
    quotes_json     TEXT NOT NULL DEFAULT '[]',
    payload_json    TEXT NOT NULL DEFAULT '',
    created_at      TEXT NOT NULL,
    UNIQUE (event_id, rubric_version)
) STRICT;

CREATE TABLE signal_review (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    assessment_id INTEGER NOT NULL UNIQUE REFERENCES signal_assessments (id),
    reason        TEXT NOT NULL,
    state         TEXT NOT NULL DEFAULT 'open' CHECK (state IN ('open', 'approved', 'rejected')),
    created_at    TEXT NOT NULL,
    decided_at    TEXT
) STRICT;

CREATE TABLE signal_tiers (
    company_id   INTEGER PRIMARY KEY REFERENCES signal_companies (id),
    tier         TEXT NOT NULL CHECK (tier IN ('hot', 'warm', 'watch', 'cold')),
    momentum     REAL NOT NULL,
    priority     REAL NOT NULL,
    policy       TEXT NOT NULL,
    hot_since    TEXT,
    changed_at   TEXT NOT NULL,
    updated_at   TEXT NOT NULL
) STRICT;

CREATE TABLE signal_blocks (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    company_id INTEGER NOT NULL REFERENCES signal_companies (id),
    event_id   INTEGER REFERENCES signal_events (id),
    opened_at  TEXT NOT NULL,
    expires_at TEXT NOT NULL,
    lifted_at  TEXT
) STRICT;

CREATE INDEX signal_blocks_company ON signal_blocks (company_id, expires_at);

CREATE TABLE signal_ats_identities (
    company_id       INTEGER PRIMARY KEY REFERENCES signal_companies (id),
    status           TEXT NOT NULL CHECK (status IN ('resolved', 'unresolved', 'no_ats')),
    ats              TEXT NOT NULL DEFAULT '',
    slug             TEXT NOT NULL DEFAULT '',
    careers_url      TEXT NOT NULL DEFAULT '',
    lane             TEXT NOT NULL DEFAULT '',
    resolver_version INTEGER NOT NULL,
    resolved_at      TEXT NOT NULL
) STRICT;

CREATE TABLE signal_postings (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    company_id    INTEGER NOT NULL REFERENCES signal_companies (id),
    ats           TEXT NOT NULL,
    board         TEXT NOT NULL,
    external_id   TEXT NOT NULL,
    url           TEXT NOT NULL,
    title         TEXT NOT NULL,
    location      TEXT NOT NULL,
    remote        INTEGER NOT NULL DEFAULT 0,
    description   TEXT NOT NULL DEFAULT '',
    verdict       TEXT NOT NULL CHECK (verdict IN ('pass', 'fail')),
    reasons_json  TEXT NOT NULL DEFAULT '[]',
    first_seen_at TEXT NOT NULL,
    handed_off_at TEXT,
    handoff       TEXT NOT NULL DEFAULT '',
    inbox_id      INTEGER,
    UNIQUE (ats, board, external_id)
) STRICT;

CREATE INDEX signal_postings_company ON signal_postings (company_id, first_seen_at);

-- The key is a hash of company, tier, rounded momentum, policy version and the
-- 24-hour window, so a check cannot repeat inside one revision.
CREATE TABLE signal_checks (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    company_id   INTEGER NOT NULL REFERENCES signal_companies (id),
    revision_key TEXT NOT NULL UNIQUE,
    tier         TEXT NOT NULL,
    momentum     REAL NOT NULL,
    tick_id      INTEGER,
    status       TEXT NOT NULL DEFAULT 'started',
    detail       TEXT NOT NULL DEFAULT '',
    checked_at   TEXT NOT NULL
) STRICT;

CREATE INDEX signal_checks_company ON signal_checks (company_id, checked_at);

CREATE TABLE signal_outcomes (
    company_id   INTEGER NOT NULL REFERENCES signal_companies (id),
    event_id     INTEGER NOT NULL REFERENCES signal_events (id),
    event_type   TEXT NOT NULL,
    window_start TEXT NOT NULL,
    window_end   TEXT NOT NULL,
    status       TEXT NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'hit', 'miss')),
    lead_days    INTEGER,
    posting_id   INTEGER REFERENCES signal_postings (id),
    resolved_at  TEXT,
    PRIMARY KEY (company_id, event_id)
) STRICT;

-- A channel that is not configured records a no-op, which must not use up the
-- key: the unique index covers delivered rows only.
CREATE TABLE signal_notifications (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    key        TEXT NOT NULL,
    kind       TEXT NOT NULL,
    state      TEXT NOT NULL CHECK (state IN ('sent', 'noop')),
    company_id INTEGER,
    detail     TEXT NOT NULL DEFAULT '',
    created_at TEXT NOT NULL
) STRICT;

CREATE UNIQUE INDEX signal_notifications_sent ON signal_notifications (key) WHERE state = 'sent';

-- Cursors, validators and per-scope state of the collectors.
CREATE TABLE signal_collector_state (
    source_id     TEXT NOT NULL,
    scope         TEXT NOT NULL,
    etag          TEXT NOT NULL DEFAULT '',
    last_modified TEXT NOT NULL DEFAULT '',
    cursor        INTEGER NOT NULL DEFAULT 0,
    updated_at    TEXT NOT NULL,
    PRIMARY KEY (source_id, scope)
) STRICT;

CREATE TABLE signal_lanes (
    source_id        TEXT PRIMARY KEY,
    last_produced_at TEXT,
    last_status      TEXT NOT NULL DEFAULT '',
    updated_at       TEXT NOT NULL
) STRICT;

-- Leads the handoff stored, so a review lead is written once per company and
-- evidence link.
CREATE TABLE signal_lead_keys (
    key        TEXT PRIMARY KEY,
    company_id INTEGER NOT NULL REFERENCES signal_companies (id),
    category   TEXT NOT NULL,
    created_at TEXT NOT NULL
) STRICT;

CREATE TABLE signal_ticks (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    run_id       TEXT NOT NULL DEFAULT '',
    status       TEXT NOT NULL CHECK (status IN ('running', 'completed', 'failed')),
    started_at   TEXT NOT NULL,
    heartbeat_at TEXT NOT NULL,
    finished_at  TEXT,
    summary_json TEXT NOT NULL DEFAULT '{}'
) STRICT;

CREATE UNIQUE INDEX signal_ticks_one_running ON signal_ticks (status) WHERE status = 'running';

CREATE TABLE signal_digests (
    day        TEXT PRIMARY KEY,
    body       TEXT NOT NULL,
    created_at TEXT NOT NULL
) STRICT;

CREATE TABLE signal_audit (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    at         TEXT NOT NULL,
    kind       TEXT NOT NULL,
    company_id INTEGER,
    detail     TEXT NOT NULL
) STRICT;
