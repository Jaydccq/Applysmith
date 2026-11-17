-- Gmail outcomes, candidacy links, thread texts, digests, alert state and the
-- signal event record (gmail-integration). Every row is derived from mail the
-- product already stored; none holds a message body. Timestamps are RFC 3339
-- UTC text with millisecond precision.

-- The link decision of one signal. A signal is identified by its message id.
-- candidacy_id is set only when the decision is auto, and confirmed marks a
-- link the user made, which no later pass changes.
CREATE TABLE gmail_links (
    message_id     TEXT PRIMARY KEY,
    thread_id      TEXT NOT NULL,
    decision       TEXT NOT NULL CHECK (decision IN ('auto', 'review', 'unlinked')),
    candidacy_id   INTEGER REFERENCES tracker_entries (id) ON DELETE SET NULL,
    score          REAL NOT NULL DEFAULT 0,
    runner_up      REAL NOT NULL DEFAULT 0,
    reasons        TEXT NOT NULL DEFAULT '',
    linker_version TEXT NOT NULL,
    confirmed      INTEGER NOT NULL DEFAULT 0 CHECK (confirmed IN (0, 1)),
    decided_at     TEXT NOT NULL
) STRICT;

CREATE INDEX gmail_links_thread ON gmail_links (thread_id);
CREATE INDEX gmail_links_decision ON gmail_links (decision);

-- A confirmation applies to the whole Gmail thread and is final.
CREATE TABLE gmail_thread_links (
    thread_id    TEXT PRIMARY KEY,
    candidacy_id INTEGER NOT NULL REFERENCES tracker_entries (id) ON DELETE CASCADE,
    confirmed_at TEXT NOT NULL
) STRICT;

-- Signals the user marked as not job-related. The tier of such a signal reads
-- as suppressed whatever a later reclassification says.
CREATE TABLE gmail_link_rejections (
    message_id  TEXT PRIMARY KEY,
    rejected_at TEXT NOT NULL
) STRICT;

-- The idempotent ledger of outcomes: one row per message id applied to the
-- tracker, so a replay writes nothing. result is changed (the status moved),
-- no_change, review (the tracker refused; reason and the transition are kept)
-- or confirmation (an offer waits for the user).
CREATE TABLE gmail_outcomes (
    message_id   TEXT PRIMARY KEY,
    candidacy_id INTEGER NOT NULL,
    signal_type  TEXT NOT NULL,
    result       TEXT NOT NULL CHECK (result IN ('changed', 'no_change', 'review', 'confirmation')),
    from_status  TEXT NOT NULL DEFAULT '',
    to_status    TEXT NOT NULL DEFAULT '',
    reason       TEXT NOT NULL DEFAULT '',
    at           TEXT NOT NULL
) STRICT;

-- A request for the user to confirm an offer. The tracker never lets the
-- Gmail actor set offer, so the status moves only when the user confirms.
CREATE TABLE gmail_confirmations (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    message_id   TEXT NOT NULL UNIQUE,
    thread_id    TEXT NOT NULL,
    candidacy_id INTEGER NOT NULL,
    status       TEXT NOT NULL CHECK (status = 'offer'),
    state        TEXT NOT NULL DEFAULT 'pending' CHECK (state IN ('pending', 'confirmed', 'dismissed')),
    created_at   TEXT NOT NULL,
    decided_at   TEXT
) STRICT;

-- One event per message that newly got a classified signal, for the Ask Agent
-- signal triggers. seq orders the events; the unique message id makes the
-- record once per message. No row carries message text.
CREATE TABLE gmail_signal_events (
    seq         INTEGER PRIMARY KEY AUTOINCREMENT,
    message_id  TEXT NOT NULL UNIQUE,
    signal_type TEXT NOT NULL,
    tier        TEXT NOT NULL,
    direction   TEXT NOT NULL,
    employer    TEXT NOT NULL,
    received_ms INTEGER NOT NULL,
    recorded_at TEXT NOT NULL
) STRICT;

CREATE INDEX gmail_signal_events_recorded ON gmail_signal_events (recorded_at);

-- The last alert state recorded per Gmail thread; an empty state means the
-- thread is not in an alert state. seq counts the changes of one thread.
CREATE TABLE gmail_alert_states (
    thread_id TEXT PRIMARY KEY,
    state     TEXT NOT NULL,
    seq       INTEGER NOT NULL DEFAULT 1,
    at        TEXT NOT NULL
) STRICT;

-- Generated thread summaries and reply drafts, cached by a fingerprint of the
-- thread state. A draft is never sent by the product.
CREATE TABLE gmail_thread_texts (
    thread_id    TEXT NOT NULL,
    kind         TEXT NOT NULL CHECK (kind IN ('summary', 'draft')),
    fingerprint  TEXT NOT NULL,
    body         TEXT NOT NULL,
    method       TEXT NOT NULL CHECK (method IN ('model', 'deterministic')),
    generated_at TEXT NOT NULL,
    PRIMARY KEY (thread_id, kind)
) STRICT;

-- Digests and weekly reviews. slot is the slot instant of a scheduled one and
-- is the dedupe key; a manual or agent digest has none. A row is written
-- before delivery and written again with the delivery outcome.
-- starts_window marks a delivered digest that starts the next one's window.
CREATE TABLE gmail_digests (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    kind          TEXT NOT NULL CHECK (kind IN ('daily', 'weekly')),
    trigger_kind  TEXT NOT NULL CHECK (trigger_kind IN ('scheduled', 'manual', 'agent')),
    slot          TEXT UNIQUE,
    status        TEXT NOT NULL CHECK (status IN ('running', 'done', 'failed')),
    created_at    TEXT NOT NULL,
    window_start  TEXT NOT NULL,
    window_end    TEXT NOT NULL,
    method        TEXT NOT NULL DEFAULT 'deterministic' CHECK (method IN ('model', 'deterministic')),
    headline      TEXT NOT NULL DEFAULT '',
    body_json     TEXT NOT NULL DEFAULT '{}' CHECK (json_valid(body_json)),
    delivery_json TEXT NOT NULL DEFAULT '{}' CHECK (json_valid(delivery_json)),
    error         TEXT NOT NULL DEFAULT '',
    starts_window INTEGER NOT NULL DEFAULT 0 CHECK (starts_window IN (0, 1))
) STRICT;

CREATE INDEX gmail_digests_created ON gmail_digests (created_at);

-- Thumbs-up and thumbs-down on the thread items of a digest. The table is
-- append-only; the latest row per thread decides.
CREATE TABLE gmail_digest_feedback (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    digest_id  INTEGER NOT NULL REFERENCES gmail_digests (id) ON DELETE CASCADE,
    thread_key TEXT NOT NULL,
    verdict    TEXT NOT NULL CHECK (verdict IN ('up', 'down', 'clear')),
    company    TEXT NOT NULL DEFAULT '',
    lane       TEXT NOT NULL DEFAULT '',
    state      TEXT NOT NULL DEFAULT '',
    at         TEXT NOT NULL
) STRICT;

CREATE INDEX gmail_digest_feedback_thread ON gmail_digest_feedback (thread_key, id);
