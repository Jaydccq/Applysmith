-- Hiring signals that are not yet a single posting. A lead keeps the evidence
-- that produced it; dedup_key is the identity a source de-duplicates on, so a
-- post seen again is the same lead whatever the model said about it.
-- Timestamps are RFC 3339 UTC text.
CREATE TABLE leads (
    id                 INTEGER PRIMARY KEY AUTOINCREMENT,
    source             TEXT NOT NULL CHECK (source <> ''),
    dedup_key          TEXT NOT NULL UNIQUE CHECK (dedup_key <> ''),
    body_hash          TEXT NOT NULL DEFAULT '',
    poster_type        TEXT NOT NULL DEFAULT '' CHECK (poster_type IN ('', 'person', 'company')),
    author             TEXT NOT NULL DEFAULT '',
    company            TEXT NOT NULL DEFAULT '',
    roles_json         TEXT NOT NULL DEFAULT '[]',
    locations_json     TEXT NOT NULL DEFAULT '[]',
    work_model         TEXT NOT NULL DEFAULT '',
    compensation       TEXT NOT NULL DEFAULT '',
    apply_links_json   TEXT NOT NULL DEFAULT '[]',
    contact_emails_json TEXT NOT NULL DEFAULT '[]',
    contact_method     TEXT NOT NULL DEFAULT '' CHECK (contact_method IN ('', 'link', 'email', 'dm', 'comment', 'tag')),
    sponsorship        TEXT NOT NULL DEFAULT '' CHECK (sponsorship IN ('', 'yes', 'no', 'unknown')),
    confidence         REAL NOT NULL DEFAULT 0 CHECK (confidence >= 0 AND confidence <= 1),
    category           TEXT NOT NULL CHECK (category IN ('posting', 'careers-page', 'no-ats', 'email', 'dm', 'comment',
                                                         'generic-page', 'unreachable', 'low-confidence', 'eval-cap', 'eval-failed')),
    state              TEXT NOT NULL DEFAULT 'new' CHECK (state IN ('new', 'evaluated', 'dismissed')),
    reason             TEXT NOT NULL DEFAULT '',
    expanded           TEXT NOT NULL DEFAULT '',
    description        TEXT NOT NULL DEFAULT '',
    evidence_url       TEXT NOT NULL DEFAULT '',
    evidence_title     TEXT NOT NULL DEFAULT '',
    evidence_text      TEXT NOT NULL DEFAULT '',
    evidence_adapter   TEXT NOT NULL DEFAULT '',
    evidence_at        TEXT,
    evidence_absent    TEXT NOT NULL DEFAULT '',
    inbox_id           INTEGER,
    created_at         TEXT NOT NULL,
    updated_at         TEXT NOT NULL,
    CHECK (evidence_text <> '' OR evidence_absent <> '')
) STRICT;

CREATE INDEX leads_state ON leads (state, category);
CREATE INDEX leads_created ON leads (created_at);
CREATE INDEX leads_body_hash ON leads (body_hash) WHERE body_hash <> '';

-- Every careers URL attempted, including those that yielded nothing, so a URL
-- is fetched once.
CREATE TABLE lead_expansions (
    url         TEXT PRIMARY KEY CHECK (url <> ''),
    expanded_at TEXT NOT NULL,
    found       INTEGER NOT NULL CHECK (found >= 0),
    outcome     TEXT NOT NULL,
    lead_id     INTEGER
) STRICT, WITHOUT ROWID;

-- Evidence only: every candidate a lead path rejected, with every rule it
-- failed. No stage reads it to re-admit or retry.
CREATE TABLE lead_gate_audit (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    at           TEXT NOT NULL,
    ingress      TEXT NOT NULL CHECK (ingress IN ('feed', 'expansion', 'signals', 'exa', 'evaluate')),
    identity_key TEXT NOT NULL DEFAULT '',
    title        TEXT NOT NULL DEFAULT '',
    location     TEXT NOT NULL DEFAULT '',
    company      TEXT NOT NULL DEFAULT '',
    url          TEXT NOT NULL DEFAULT '',
    rules_json   TEXT NOT NULL
) STRICT;

CREATE INDEX lead_gate_audit_at ON lead_gate_audit (at);
