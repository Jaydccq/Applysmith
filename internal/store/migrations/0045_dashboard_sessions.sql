-- Dashboard sessions (local-api). One row per live session; only the SHA-256 of
-- the cookie value and of the session key is stored, never a value, so a copy of
-- the database cannot open the dashboard. used_at is written at most hourly, so
-- the 30-day idle lifetime is read with that much slack. A restore clears them.
CREATE TABLE dashboard_sessions (
    cookie_sha256 TEXT PRIMARY KEY,
    key_sha256    TEXT NOT NULL,
    used_at       TEXT NOT NULL
) STRICT;

CREATE INDEX dashboard_sessions_used ON dashboard_sessions (used_at);
