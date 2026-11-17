-- The completion ladder's memory of the questions its grounded rung declined:
-- one row per (question, run) so a question declined by two distinct runs is
-- recognised as a repeat. A row is reused for 30 days and only under the build
-- and the answer-bank/profile/CV revision that recorded it. Timestamps are
-- RFC 3339 UTC text with millisecond precision. The key names a host, a
-- normalised question label, a control kind and its option set; it holds no
-- answer and no profile value.
CREATE TABLE apply_declines (
    decline_key TEXT NOT NULL CHECK (decline_key <> ''),
    run_id      TEXT NOT NULL CHECK (run_id <> ''),
    build       TEXT NOT NULL,
    revision    TEXT NOT NULL,
    declined_at TEXT NOT NULL,
    PRIMARY KEY (decline_key, run_id)
) STRICT;

CREATE INDEX apply_declines_at ON apply_declines (declined_at);
