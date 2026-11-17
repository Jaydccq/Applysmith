-- A draft written from the linked application keeps its claims (JSON:
-- confirmed and to-check lists, each naming its source; empty otherwise) and
-- method application. The table is a cache keyed by fingerprint, so it is
-- rebuilt empty and refilled on demand.
DROP TABLE gmail_thread_texts;
CREATE TABLE gmail_thread_texts (
    thread_id    TEXT NOT NULL,
    kind         TEXT NOT NULL CHECK (kind IN ('summary', 'draft')),
    fingerprint  TEXT NOT NULL,
    body         TEXT NOT NULL,
    method       TEXT NOT NULL CHECK (method IN ('model', 'deterministic', 'application')),
    claims       TEXT NOT NULL DEFAULT '',
    generated_at TEXT NOT NULL,
    PRIMARY KEY (thread_id, kind)
) STRICT;
