-- The signed risk acknowledgement and the risk signals that start a cooldown on
-- account creation. An acknowledgement row is appended each time the user
-- signs; the newest row is the one in force, and it counts only while its hash
-- equals the SHA-256 of the current statement text and policy.yml names the same
-- version. A signal row carries the instant its cooldown ends, computed from
-- the policy's hours when the signal was recorded (equal to its own time when
-- the policy disabled the cooldown). Timestamps are RFC 3339 UTC text with
-- millisecond precision. No row holds the typed name.
CREATE TABLE risk_acknowledgements (
    id               INTEGER PRIMARY KEY AUTOINCREMENT,
    version          TEXT NOT NULL CHECK (version <> ''),
    statement_sha256 TEXT NOT NULL CHECK (length(statement_sha256) = 64),
    signed_at        TEXT NOT NULL,
    channel          TEXT NOT NULL CHECK (channel IN ('dashboard', 'cli'))
) STRICT;

CREATE TABLE risk_signals (
    id      INTEGER PRIMARY KEY AUTOINCREMENT,
    system  TEXT NOT NULL CHECK (system <> ''),
    signal  TEXT NOT NULL CHECK (signal <> ''),
    run_id  TEXT NOT NULL DEFAULT '',
    at      TEXT NOT NULL,
    ends_at TEXT NOT NULL
) STRICT;

CREATE INDEX risk_signals_system ON risk_signals (system, ends_at);
