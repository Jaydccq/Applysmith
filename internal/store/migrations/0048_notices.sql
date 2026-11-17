-- The dashboard's Notices list (docs/architecture/notify.md, Desktop sink):
-- in-page messages no desktop notification showed. handle is the
-- announcement's handle (an entry id), so a newer notice of an entry replaces
-- the earlier one; a plain message has none. read_at is set by the user.
CREATE TABLE notify_notices (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    handle     TEXT UNIQUE,
    title      TEXT NOT NULL,
    body       TEXT NOT NULL,
    link       TEXT NOT NULL DEFAULT '',
    created_at TEXT NOT NULL,
    read_at    TEXT
) STRICT;
