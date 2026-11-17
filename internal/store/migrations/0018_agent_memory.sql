-- The Ask Agent's compaction checkpoints, its runtime settings and its memory:
-- facts with their versions and tombstones, the review queue for statements
-- that steer answers, operating rules, and the audit of the nightly
-- consolidation. Timestamps are RFC 3339 UTC text with millisecond precision.

-- A checkpoint summarises the messages of a conversation up to covered_to.
-- tainted is set when any covered message, or any checkpoint it builds on,
-- carried text from outside the product.
CREATE TABLE chat_checkpoints (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    session_id    TEXT NOT NULL REFERENCES chat_sessions (id) ON DELETE CASCADE,
    covered_to    INTEGER NOT NULL,
    covered_turns INTEGER NOT NULL CHECK (covered_turns >= 1),
    summary       TEXT NOT NULL,
    tainted       INTEGER NOT NULL DEFAULT 0 CHECK (tainted IN (0, 1)),
    created_at    TEXT NOT NULL
) STRICT;

CREATE INDEX chat_checkpoints_session ON chat_checkpoints (session_id, id);

CREATE TABLE agent_settings (
    key        TEXT PRIMARY KEY,
    value      TEXT NOT NULL,
    updated_at TEXT NOT NULL
) STRICT;

-- A fact is versioned: amending inserts a new row that names the one it
-- replaces, and the old row turns superseded. needs_review rows are
-- candidates; only trusted rows reach a prompt.
CREATE TABLE memory_facts (
    id          TEXT PRIMARY KEY,
    kind        TEXT NOT NULL CHECK (kind IN ('preference', 'goal', 'constraint', 'decision', 'context', 'note')),
    subject     TEXT NOT NULL CHECK (subject IN ('user', 'company', 'application')),
    subject_ref TEXT NOT NULL DEFAULT '',
    body        TEXT NOT NULL CHECK (length(body) BETWEEN 1 AND 2000),
    body_hash   TEXT NOT NULL,
    source      TEXT NOT NULL CHECK (source IN ('user_declared', 'agent_extracted')),
    trust       TEXT NOT NULL CHECK (trust IN ('trusted', 'needs_review')),
    pii         TEXT NOT NULL CHECK (pii IN ('low', 'high')),
    status      TEXT NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'superseded')),
    supersedes  TEXT NOT NULL DEFAULT '',
    valid_from  TEXT NOT NULL,
    created_at  TEXT NOT NULL
) STRICT;

CREATE INDEX memory_facts_active ON memory_facts (status, trust);
CREATE INDEX memory_facts_hash ON memory_facts (body_hash);

-- Forgetting a fact deletes it and keeps only the hash of its text, so an
-- extraction cannot add it again.
CREATE TABLE memory_tombstones (
    hash       TEXT PRIMARY KEY,
    created_at TEXT NOT NULL
) STRICT;

-- Statements that would steer how questions are answered never become facts;
-- they wait here for the user.
CREATE TABLE memory_review (
    id          TEXT PRIMARY KEY,
    kind        TEXT NOT NULL,
    subject     TEXT NOT NULL,
    subject_ref TEXT NOT NULL DEFAULT '',
    body        TEXT NOT NULL,
    reason      TEXT NOT NULL CHECK (reason IN ('instruction_shaped')),
    source      TEXT NOT NULL,
    created_at  TEXT NOT NULL
) STRICT;

-- A rule is prompt guidance and becomes active only by the user's approval.
CREATE TABLE memory_rules (
    id          TEXT PRIMARY KEY,
    body        TEXT NOT NULL CHECK (length(body) BETWEEN 1 AND 1200),
    state       TEXT NOT NULL CHECK (state IN ('proposed', 'active', 'retired')),
    source      TEXT NOT NULL CHECK (source IN ('agent', 'consolidation', 'user')),
    created_at  TEXT NOT NULL,
    updated_at  TEXT NOT NULL
) STRICT;

CREATE INDEX memory_rules_state ON memory_rules (state);

-- One row per consolidation pass; until is the cut-off a later pass starts
-- from. A failed pass does not advance it.
CREATE TABLE memory_runs (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    started_at    TEXT NOT NULL,
    until_at      TEXT NOT NULL,
    outcome       TEXT NOT NULL CHECK (outcome IN ('ok', 'skipped', 'failed')),
    detail        TEXT NOT NULL DEFAULT '',
    conversations INTEGER NOT NULL DEFAULT 0,
    facts         INTEGER NOT NULL DEFAULT 0,
    rules         INTEGER NOT NULL DEFAULT 0
) STRICT;

CREATE TABLE memory_audit (
    id      INTEGER PRIMARY KEY AUTOINCREMENT,
    run_id  INTEGER NOT NULL REFERENCES memory_runs (id) ON DELETE CASCADE,
    at      TEXT NOT NULL,
    kind    TEXT NOT NULL CHECK (kind IN ('fact', 'rule', 'review')),
    item_id TEXT NOT NULL
) STRICT;
