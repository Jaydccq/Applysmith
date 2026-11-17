-- The runs board, the announcement outbox and the Telegram gateway's durable
-- state (notifications-and-telegram). Timestamps are RFC 3339 UTC text with
-- millisecond precision. No row holds a bot token, a pairing code in clear
-- text or the text of a Telegram message.

-- One entry per unit of work the user wants to watch. revision grows on every
-- change; epoch grows each time the entry enters a state or changes the reason
-- of a waiting state, and is part of an announcement's identity. ref_updated
-- is the update stamp of the run record the entry mirrors, so a sync only
-- touches entries whose record moved.
CREATE TABLE board_entries (
    id          TEXT PRIMARY KEY CHECK (id <> ''),
    kind        TEXT NOT NULL CHECK (kind IN ('apply', 'scan', 'evaluate', 'documents', 'agent', 'task')),
    origin      TEXT NOT NULL CHECK (origin IN ('user', 'schedule', 'agent')),
    title       TEXT NOT NULL CHECK (length(title) <= 120),
    stage       TEXT NOT NULL DEFAULT '',
    detail      TEXT NOT NULL DEFAULT '' CHECK (length(detail) <= 300),
    state       TEXT NOT NULL CHECK (state IN ('working', 'needs-you', 'blocked', 'done', 'idle')),
    reason      TEXT NOT NULL DEFAULT '',
    outcome     TEXT NOT NULL DEFAULT '',
    batch_id    TEXT NOT NULL DEFAULT '',
    link        TEXT NOT NULL DEFAULT '',
    ref         TEXT NOT NULL DEFAULT '',
    focus_tab   TEXT NOT NULL DEFAULT '',
    revision    INTEGER NOT NULL DEFAULT 1,
    epoch       INTEGER NOT NULL DEFAULT 1,
    started_at  TEXT NOT NULL,
    updated_at  TEXT NOT NULL,
    state_at    TEXT NOT NULL,
    finished_at TEXT,
    seen_at     TEXT,
    acked_at    TEXT,
    ref_updated TEXT NOT NULL DEFAULT '',
    CHECK ((state = 'needs-you' AND reason IN ('sign-in', 'answer', 'challenge', 'review', 'one-time-code', 'confirmation', 'may-have-submitted', 'other'))
        OR (state = 'blocked' AND reason IN ('credential-refused', 'lockout', 'lane-unavailable', 'key-missing', 'cap-reached', 'no-connection', 'other'))
        OR (state NOT IN ('needs-you', 'blocked') AND reason = '')),
    CHECK ((state = 'done' AND outcome IN ('ok', 'failed', 'cancelled', 'interrupted')) OR (state <> 'done' AND outcome = ''))
) STRICT;

CREATE INDEX board_entries_state ON board_entries (state, acked_at);
CREATE INDEX board_entries_batch ON board_entries (batch_id) WHERE batch_id <> '';
CREATE INDEX board_entries_ref ON board_entries (ref) WHERE ref <> '';

-- An announcement waiting for its turn. key is its identity: each key is
-- announced once, so a restart cannot repeat it. due_at is when it may first
-- be considered (a finished run waits 20 seconds); a normal one inside quiet
-- hours becomes held and is delivered in id order afterwards. only_sink limits
-- a burst summary to the sink that was over its limit.
CREATE TABLE notify_outbox (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    key         TEXT NOT NULL UNIQUE,
    entry_id    TEXT NOT NULL DEFAULT '',
    entry_epoch INTEGER NOT NULL DEFAULT 0,
    batch_id    TEXT NOT NULL DEFAULT '',
    kind        TEXT NOT NULL,
    urgent      INTEGER NOT NULL CHECK (urgent IN (0, 1)),
    never_hold  INTEGER NOT NULL DEFAULT 0 CHECK (never_hold IN (0, 1)),
    only_sink   TEXT NOT NULL DEFAULT '',
    title       TEXT NOT NULL,
    body        TEXT NOT NULL,
    link        TEXT NOT NULL DEFAULT '',
    state       TEXT NOT NULL CHECK (state IN ('pending', 'held', 'settled', 'discarded', 'dropped')),
    due_at      TEXT NOT NULL,
    created_at  TEXT NOT NULL,
    held_at     TEXT,
    settled_at  TEXT
) STRICT;

CREATE INDEX notify_outbox_open ON notify_outbox (id) WHERE state IN ('pending', 'held');
CREATE INDEX notify_outbox_entry ON notify_outbox (entry_id) WHERE entry_id <> '';

-- What each sink did with an announcement. ext_id is the sink's handle for it
-- (a Telegram chat and message id) so the message can be edited later.
CREATE TABLE notify_deliveries (
    outbox_id INTEGER NOT NULL REFERENCES notify_outbox (id) ON DELETE CASCADE,
    sink      TEXT NOT NULL,
    state     TEXT NOT NULL CHECK (state IN ('delivered', 'coalesced', 'summarized', 'dropped', 'withdrawn', 'superseded')),
    ext_id    TEXT NOT NULL DEFAULT '',
    normal    INTEGER NOT NULL DEFAULT 0 CHECK (normal IN (0, 1)),
    at        TEXT NOT NULL,
    PRIMARY KEY (outbox_id, sink)
) STRICT;

CREATE INDEX notify_deliveries_window ON notify_deliveries (sink, at) WHERE normal = 1;

-- The runtime notification settings: desktop, telegram and failed_scans.
CREATE TABLE notify_settings (
    key        TEXT PRIMARY KEY CHECK (key IN ('desktop', 'telegram', 'failed_scans')),
    value      TEXT NOT NULL CHECK (value IN ('true', 'false')),
    updated_at TEXT NOT NULL
) STRICT;

-- The bot's public name, recorded when a token is validated.
CREATE TABLE telegram_bot (
    id         INTEGER PRIMARY KEY CHECK (id = 1),
    username   TEXT NOT NULL,
    updated_at TEXT NOT NULL
) STRICT;

-- The one paired private chat. generation grows with every pairing so a
-- button issued under an earlier pairing is refused.
CREATE TABLE telegram_owner (
    id         INTEGER PRIMARY KEY CHECK (id = 1),
    chat_id    INTEGER NOT NULL,
    user_id    INTEGER NOT NULL,
    generation INTEGER NOT NULL,
    paired_at  TEXT NOT NULL
) STRICT;

-- The pairing code in flight: only its hash, a wrong-attempt counter and the
-- window it is valid in.
CREATE TABLE telegram_pairing (
    id         INTEGER PRIMARY KEY CHECK (id = 1),
    code_hash  TEXT NOT NULL,
    expires_at TEXT NOT NULL,
    wrong      INTEGER NOT NULL DEFAULT 0,
    closed     INTEGER NOT NULL DEFAULT 0 CHECK (closed IN (0, 1))
) STRICT;

CREATE TABLE telegram_meta (
    key   TEXT PRIMARY KEY,
    value TEXT NOT NULL
) STRICT;

-- The poller lease: one owner per bot token, fenced by a generation.
CREATE TABLE telegram_lease (
    id         INTEGER PRIMARY KEY CHECK (id = 1),
    owner      TEXT NOT NULL,
    generation INTEGER NOT NULL,
    expires_at TEXT NOT NULL
) STRICT;

-- One receipt per update. A duplicate is suppressed only once the receipt is
-- processed or terminal.
CREATE TABLE telegram_receipts (
    update_id   INTEGER PRIMARY KEY,
    callback_id TEXT,
    state       TEXT NOT NULL CHECK (state IN ('received', 'processing', 'processed', 'failed-retryable', 'terminal')),
    attempts    INTEGER NOT NULL DEFAULT 0,
    action      TEXT NOT NULL DEFAULT '',
    chat_id     INTEGER NOT NULL DEFAULT 0,
    created_at  TEXT NOT NULL,
    updated_at  TEXT NOT NULL
) STRICT;

CREATE UNIQUE INDEX telegram_receipts_callback ON telegram_receipts (callback_id) WHERE callback_id IS NOT NULL;
CREATE INDEX telegram_receipts_updated ON telegram_receipts (updated_at);

-- Single-use button tokens, bound to a chat and a pairing generation.
CREATE TABLE telegram_tokens (
    token      TEXT PRIMARY KEY,
    chat_id    INTEGER NOT NULL,
    action     TEXT NOT NULL,
    payload    TEXT NOT NULL,
    generation INTEGER NOT NULL,
    created_at TEXT NOT NULL,
    used_at    TEXT
) STRICT;

-- The audit of handled updates: update id, action name and chat id only.
CREATE TABLE telegram_events (
    id        INTEGER PRIMARY KEY AUTOINCREMENT,
    at        TEXT NOT NULL,
    update_id INTEGER NOT NULL,
    action    TEXT NOT NULL,
    chat_id   INTEGER NOT NULL
) STRICT;

CREATE INDEX telegram_events_action ON telegram_events (action, at);

-- The rolling conversation of the paired chat.
CREATE TABLE telegram_chats (
    chat_id    INTEGER PRIMARY KEY,
    session_id TEXT NOT NULL,
    last_at    TEXT NOT NULL
) STRICT;
