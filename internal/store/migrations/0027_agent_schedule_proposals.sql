-- Schedule changes the Ask Agent proposes (ask-agent: `propose_schedule_change`).
-- A proposal is inert: nothing reads this table to change a schedule except the
-- user's accept route. At most one pending proposal per schedule; a newer one
-- replaces the row. change is the JSON of the proposed enabled flag and preset.
-- Times are RFC 3339 UTC text with millisecond precision.
CREATE TABLE agent_schedule_proposals (
    id          TEXT PRIMARY KEY,
    schedule_id INTEGER NOT NULL UNIQUE,
    reason      TEXT NOT NULL CHECK (reason <> ''),
    change      TEXT NOT NULL,
    created_at  TEXT NOT NULL
) STRICT;
