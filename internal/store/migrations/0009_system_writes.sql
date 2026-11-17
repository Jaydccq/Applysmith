-- What the system itself wrote into a form control, kept only so that a manual
-- fill equal to it is never taken as the user's ground truth. A row is one
-- digest over the run id, the control's selector and the normalized value:
-- nothing here can be read back as the value, and no column names a control.
CREATE TABLE system_writes (
    write_hash  TEXT PRIMARY KEY CHECK (length(write_hash) = 64),
    recorded_at TEXT NOT NULL
) STRICT;

CREATE INDEX system_writes_recorded ON system_writes (recorded_at);
