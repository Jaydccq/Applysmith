-- The user marked a Gmail thread done. covers_ms is the received time of the
-- newest message the thread held then: the decision covers the mail seen so
-- far and a newer message re-opens the thread. No row holds message text.
CREATE TABLE gmail_thread_done (
    thread_id TEXT PRIMARY KEY,
    covers_ms INTEGER NOT NULL,
    done_at   TEXT NOT NULL
) STRICT;
