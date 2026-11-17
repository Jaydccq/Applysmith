-- The request a run the user started from the dashboard or the command line
-- came from (ask-agent "Resume after a hand-off", F-3): the facts its submit
-- gate decides from, so Done, a saved answer and "Submit this form" continue
-- the run from what the user asked, never from the press. url is the whole
-- address the run opened, query included. A row holds no form value; rows
-- older than 45 days are deleted. Timestamps are RFC 3339 UTC text with
-- millisecond precision.
CREATE TABLE apply_requests (
    run_id       TEXT PRIMARY KEY CHECK (run_id <> ''),
    caller       TEXT NOT NULL,
    tool         TEXT NOT NULL,
    submit       INTEGER NOT NULL CHECK (submit IN (0, 1)),
    explicit     INTEGER NOT NULL CHECK (explicit IN (0, 1)),
    url          TEXT NOT NULL,
    employer_url TEXT NOT NULL DEFAULT '',
    company      TEXT NOT NULL DEFAULT '',
    role         TEXT NOT NULL DEFAULT '',
    named        INTEGER NOT NULL DEFAULT 0 CHECK (named IN (0, 1)),
    resubmit     INTEGER NOT NULL DEFAULT 0 CHECK (resubmit IN (0, 1)),
    created_at   TEXT NOT NULL
) STRICT;

CREATE INDEX apply_requests_created ON apply_requests (created_at);
