-- The Exa source keeps one state document: the watermark, the oldest-first set
-- of URLs already seen, the zero-yield run counts and the suspensions of its
-- queries. Timestamps are RFC 3339 UTC text.
CREATE TABLE exa_state (
    id         INTEGER PRIMARY KEY CHECK (id = 1),
    state_json TEXT NOT NULL,
    updated_at TEXT NOT NULL
) STRICT;

-- The latest liveness result per posting identity, with the reason code, the
-- method that produced it and the time of the check.
CREATE TABLE liveness_checks (
    identity_key TEXT PRIMARY KEY CHECK (identity_key <> ''),
    url          TEXT NOT NULL,
    verdict      TEXT NOT NULL CHECK (verdict IN ('active', 'expired', 'uncertain', 'mismatch')),
    reason       TEXT NOT NULL,
    method       TEXT NOT NULL,
    checked_at   TEXT NOT NULL
) STRICT;
