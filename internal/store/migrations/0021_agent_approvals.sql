-- Confirmation cards for Ask Agent calls the guard chain held, and the
-- standing approvals the user may grant from a card (ask-agent: "Confirmation
-- cards", "Standing approvals for tiers 2 and 3"). Timestamps are RFC 3339 UTC
-- text with millisecond precision. Both tables end with the conversation they
-- belong to; the audit of every change is in gate_decisions.

-- One card per held call. args is the exact argument object the single re-run
-- uses; args_shown is the same text after redaction and is all the dashboard
-- sees. target is what a standing approval would be bound to, empty when the
-- call is not eligible for one.
CREATE TABLE agent_confirmations (
    id          TEXT PRIMARY KEY,
    session_id  TEXT NOT NULL REFERENCES chat_sessions (id) ON DELETE CASCADE,
    turn_id     TEXT NOT NULL,
    call_id     TEXT NOT NULL,
    channel     TEXT NOT NULL CHECK (channel IN ('dashboard', 'telegram', 'scheduled', 'digest')),
    origin      TEXT NOT NULL CHECK (origin IN ('model', 'router', 'button')),
    tool        TEXT NOT NULL,
    tier        INTEGER NOT NULL CHECK (tier BETWEEN 0 AND 4),
    code        TEXT NOT NULL,
    reason      TEXT NOT NULL,
    summary     TEXT NOT NULL,
    company     TEXT NOT NULL DEFAULT '',
    role        TEXT NOT NULL DEFAULT '',
    host        TEXT NOT NULL DEFAULT '',
    target      TEXT NOT NULL DEFAULT '',
    args        TEXT NOT NULL,
    args_shown  TEXT NOT NULL,
    arg_hash    TEXT NOT NULL,
    purpose     TEXT NOT NULL,
    state       TEXT NOT NULL CHECK (state IN ('pending', 'approved', 'declined', 'expired')),
    decided_by  TEXT NOT NULL DEFAULT '',
    created_at  TEXT NOT NULL,
    expires_at  TEXT NOT NULL,
    decided_at  TEXT,
    CHECK ((state = 'pending') = (decided_at IS NULL))
) STRICT;

CREATE INDEX agent_confirmations_pending ON agent_confirmations (state, expires_at);
CREATE INDEX agent_confirmations_session ON agent_confirmations (session_id, state);

-- A standing approval binds one tool to one target. A conversation-scoped one
-- names its conversation and ends when the conversation is deleted; a fork is
-- another conversation and is not covered. uses counts the calls it lifted.
CREATE TABLE agent_approvals (
    id           TEXT PRIMARY KEY,
    card_id      TEXT NOT NULL,
    tool         TEXT NOT NULL,
    target       TEXT NOT NULL CHECK (target <> ''),
    scope        TEXT NOT NULL CHECK (scope IN ('conversation', 'today')),
    session_id   TEXT REFERENCES chat_sessions (id) ON DELETE CASCADE,
    created_at   TEXT NOT NULL,
    expires_at   TEXT NOT NULL,
    revoked_at   TEXT,
    uses         INTEGER NOT NULL DEFAULT 0,
    last_used_at TEXT,
    CHECK ((scope = 'conversation') = (session_id IS NOT NULL))
) STRICT;

CREATE INDEX agent_approvals_match ON agent_approvals (tool, target) WHERE revoked_at IS NULL;
