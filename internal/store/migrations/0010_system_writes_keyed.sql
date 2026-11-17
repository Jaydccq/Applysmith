-- What the system itself wrote into a form control, kept only so that a manual
-- fill equal to it is never taken as the user's ground truth. A row is one
-- HMAC-SHA256 digest over the run id and the normalized value, keyed with a
-- per-workspace secret that lives outside the database: nothing here reads back
-- as a value, and a guessed value cannot be confirmed from the table alone.
-- Rows of the unkeyed table are discarded.
DROP TABLE system_writes;

CREATE TABLE system_writes (
    value_hash  TEXT PRIMARY KEY CHECK (length(value_hash) = 64),
    recorded_at TEXT NOT NULL
) STRICT;

CREATE INDEX system_writes_recorded ON system_writes (recorded_at);
