-- Which conversation started a run, so the run's end can be told to it and a
-- run that waits for the user can be resumed with Done (ask-agent: "Background
-- runs on the runs board", "Resume after a hand-off"). Timestamps are RFC 3339
-- UTC text with millisecond precision. A row holds no form value: the facts of
-- an application run are the ones its submit gate decides from.

CREATE TABLE agent_run_origins (
    run_id     TEXT PRIMARY KEY CHECK (run_id <> ''),
    session_id TEXT NOT NULL REFERENCES chat_sessions (id) ON DELETE CASCADE,
    tool       TEXT NOT NULL,
    created_at TEXT NOT NULL
) STRICT;

CREATE INDEX agent_run_origins_session ON agent_run_origins (session_id);

-- The request an application run was started from. pressed_by and pressed_at
-- record who last pressed Done on the run and when.
CREATE TABLE agent_apply_requests (
    run_id       TEXT PRIMARY KEY REFERENCES agent_run_origins (run_id) ON DELETE CASCADE,
    turn_id      TEXT NOT NULL DEFAULT '',
    caller       TEXT NOT NULL,
    tool         TEXT NOT NULL,
    submit       INTEGER NOT NULL CHECK (submit IN (0, 1)),
    explicit     INTEGER NOT NULL CHECK (explicit IN (0, 1)),
    outside      INTEGER NOT NULL CHECK (outside IN (0, 1)),
    confirmed    INTEGER NOT NULL CHECK (confirmed IN (0, 1)),
    user_message TEXT NOT NULL DEFAULT '',
    url          TEXT NOT NULL,
    employer_url TEXT NOT NULL DEFAULT '',
    company      TEXT NOT NULL DEFAULT '',
    role         TEXT NOT NULL DEFAULT '',
    named        INTEGER NOT NULL DEFAULT 0 CHECK (named IN (0, 1)),
    resubmit     INTEGER NOT NULL DEFAULT 0 CHECK (resubmit IN (0, 1)),
    pressed_by   TEXT NOT NULL DEFAULT '',
    pressed_at   TEXT
) STRICT;

-- What a stopped run left in the tab it kept: a salted hash of the value of
-- each control, by selector. A resumed run compares the page to it to tell
-- which fields the user changed by hand.
CREATE TABLE apply_handoffs (
    run_id     TEXT PRIMARY KEY CHECK (run_id <> ''),
    fields     TEXT NOT NULL DEFAULT '{}',
    created_at TEXT NOT NULL
) STRICT;
