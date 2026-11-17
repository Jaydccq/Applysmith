-- Model replies of the answer-bank lanes (answer-bank, "Model-assisted lanes:
-- eligibility, budgets and cache"). An identical question, option set, fact set
-- and grounding mode within 24 hours reuses the earlier reply, so a form
-- re-scan, or a restart between two scans, never changes a model answer.
-- The key is a hash of those parts; the question text itself is not stored.
-- Expired rows are deleted on every write.

CREATE TABLE answer_reply_cache (
    key_hash   TEXT PRIMARY KEY CHECK (length(key_hash) = 64),
    lane       TEXT NOT NULL CHECK (length(lane) BETWEEN 1 AND 20),
    mode       TEXT NOT NULL DEFAULT '',
    reply      TEXT NOT NULL,
    created_at TEXT NOT NULL,
    expires_at TEXT NOT NULL
);

CREATE INDEX answer_reply_cache_expiry ON answer_reply_cache (expires_at);
