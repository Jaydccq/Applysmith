-- Remote access audit (remote-access). One row per event; none holds a pairing
-- code, a device token or a cookie value. device_sessions (0001) keeps the
-- paired devices.

CREATE TABLE remote_events (
    id        INTEGER PRIMARY KEY AUTOINCREMENT,
    at        TEXT NOT NULL,
    kind      TEXT NOT NULL CHECK (kind IN ('mode_change', 'pair_offer', 'pair_claim', 'pair_failed', 'revoke', 'device_limit', 'auth_block')),
    device_id TEXT,
    source    TEXT NOT NULL DEFAULT '',
    detail    TEXT NOT NULL DEFAULT ''
) STRICT;

CREATE INDEX remote_events_at ON remote_events (at);
