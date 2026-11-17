-- Scheduled Ask Agent prompts, each on a cadence or on a Gmail signal trigger,
-- and the firings of the triggers (ask-agent: "Scheduled prompts", "Signal
-- triggers"). Times are RFC 3339 UTC text with millisecond precision. No gate
-- reads these tables: a prompt is data, and a firing's draft or proposal is
-- inert until the user acts on it.

-- cadence_days is a comma list of sun..sat and cadence_time is HH:MM in the
-- server's zone, both set for a cadence prompt and both empty for a trigger.
-- trigger_types is a comma list of signal types and trigger_company an optional
-- employer name. active_since is the confirmation, the last resume or the last
-- edit of the schedule: a slot or a message before it never runs. last_slot is
-- the latest cadence slot that was claimed, so a slot runs at most once.
CREATE TABLE agent_prompts (
    id              TEXT PRIMARY KEY,
    kind            TEXT NOT NULL CHECK (kind IN ('cadence', 'trigger')),
    text            TEXT NOT NULL CHECK (text <> ''),
    state           TEXT NOT NULL CHECK (state IN ('proposed', 'active', 'paused')),
    origin          TEXT NOT NULL CHECK (origin IN ('user', 'agent')),
    cadence_days    TEXT NOT NULL DEFAULT '',
    cadence_time    TEXT NOT NULL DEFAULT '',
    trigger_types   TEXT NOT NULL DEFAULT '',
    trigger_company TEXT NOT NULL DEFAULT '',
    confirmed_at    TEXT NOT NULL DEFAULT '',
    active_since    TEXT NOT NULL DEFAULT '',
    last_slot       TEXT NOT NULL DEFAULT '',
    last_at         TEXT NOT NULL DEFAULT '',
    last_status     TEXT NOT NULL DEFAULT '' CHECK (last_status IN ('', 'answered', 'failed', 'timed_out')),
    last_session_id TEXT NOT NULL DEFAULT '',
    last_summary    TEXT NOT NULL DEFAULT '',
    created_at      TEXT NOT NULL,
    updated_at      TEXT NOT NULL,
    CHECK ((kind = 'cadence') = (cadence_days <> '' AND cadence_time <> '')),
    CHECK ((kind = 'trigger') = (trigger_types <> ''))
) STRICT;

-- One row per (trigger, Gmail message), claimed before the turn starts. The
-- primary key is the exactly-once rule: a restart, a resync and a
-- re-classification find the row and fire nothing. A row is kept without a
-- foreign key so deleting a trigger leaves its drafts to the user.
CREATE TABLE agent_trigger_firings (
    prompt_id       TEXT NOT NULL,
    message_id      TEXT NOT NULL,
    seq             INTEGER NOT NULL,
    signal_type     TEXT NOT NULL,
    employer        TEXT NOT NULL DEFAULT '',
    state           TEXT NOT NULL CHECK (state IN ('claimed', 'skipped', 'answered', 'failed', 'timed_out')),
    detail          TEXT NOT NULL DEFAULT '',
    session_id      TEXT NOT NULL DEFAULT '',
    draft           TEXT NOT NULL DEFAULT '',
    proposed_status TEXT NOT NULL DEFAULT '',
    proposed_reason TEXT NOT NULL DEFAULT '',
    claimed_at      TEXT NOT NULL,
    finished_at     TEXT NOT NULL DEFAULT '',
    PRIMARY KEY (prompt_id, message_id)
) STRICT;

CREATE INDEX agent_trigger_firings_claimed ON agent_trigger_firings (claimed_at);
