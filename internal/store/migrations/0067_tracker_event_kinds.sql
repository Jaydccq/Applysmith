-- No code writes the event kind applied_by_changed: who applied is recorded by
-- the status change itself. SQLite cannot narrow a CHECK, so the table is
-- rebuilt with its index and its two append-only triggers; nothing references
-- it by foreign key. Every row is copied unchanged. No code has ever written
-- that kind, and a row of it would stop this migration (the database stays as
-- it was) rather than be changed or dropped.
CREATE TABLE tracker_events_new (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    entry_id        INTEGER NOT NULL REFERENCES tracker_entries (id),
    at              TEXT NOT NULL,
    actor           TEXT NOT NULL,
    kind            TEXT NOT NULL CHECK (kind IN (
        'created', 'status_changed', 'status_corrected', 'transition_refused',
        'score_updated', 'report_linked', 'note_added', 'document_flag_set',
        'document_flag_cleared', 'documents_override', 'followup_recorded',
        'followup_snoozed', 'archived', 'unarchived', 'merged',
        'duplicate_attached', 'applied_date_set')),
    previous_status TEXT,
    new_status      TEXT,
    detail_json     TEXT NOT NULL DEFAULT '{}'
) STRICT;

INSERT INTO tracker_events_new (id, entry_id, at, actor, kind, previous_status, new_status, detail_json)
SELECT id, entry_id, at, actor, kind, previous_status, new_status, detail_json FROM tracker_events;

DROP TABLE tracker_events;
ALTER TABLE tracker_events_new RENAME TO tracker_events;

CREATE INDEX tracker_events_entry ON tracker_events (entry_id, id);

CREATE TRIGGER tracker_events_no_rewrite BEFORE UPDATE ON tracker_events
WHEN NEW.id IS NOT OLD.id OR NEW.at IS NOT OLD.at OR NEW.actor IS NOT OLD.actor OR NEW.kind IS NOT OLD.kind
  OR NEW.previous_status IS NOT OLD.previous_status OR NEW.new_status IS NOT OLD.new_status
  OR NEW.detail_json IS NOT OLD.detail_json
BEGIN
    SELECT RAISE(ABORT, 'tracker events are append-only');
END;

CREATE TRIGGER tracker_events_no_delete BEFORE DELETE ON tracker_events
BEGIN
    SELECT RAISE(ABORT, 'tracker events are append-only');
END;
