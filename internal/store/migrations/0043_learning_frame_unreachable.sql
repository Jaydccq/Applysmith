-- A frame the fill could not enter is a learning upload of its own kind
-- (answer-bank, the event upload route). It is kept in learning_uploads only,
-- like an unsafe-control record. SQLite cannot widen a CHECK, so the table is
-- rebuilt; nothing references it by foreign key.
CREATE TABLE learning_uploads_new (
    event_id      TEXT PRIMARY KEY CHECK (length(event_id) BETWEEN 1 AND 100),
    kind          TEXT NOT NULL CHECK (kind IN ('uncertainty', 'correction', 'user-reported', 'manual-fill', 'unsafe-control', 'frame-unreachable')),
    host          TEXT NOT NULL,
    question      TEXT NOT NULL,
    control_kind  TEXT NOT NULL,
    at            TEXT NOT NULL,
    received_at   TEXT NOT NULL,
    reason        TEXT NOT NULL DEFAULT '',
    report_number INTEGER,
    run_id        TEXT NOT NULL DEFAULT '',
    report_json   TEXT
) STRICT;

INSERT INTO learning_uploads_new (event_id, kind, host, question, control_kind, at, received_at, reason, report_number, run_id, report_json)
SELECT event_id, kind, host, question, control_kind, at, received_at, reason, report_number, run_id, report_json FROM learning_uploads;

DROP TABLE learning_uploads;
ALTER TABLE learning_uploads_new RENAME TO learning_uploads;

CREATE INDEX learning_uploads_received ON learning_uploads (received_at);
