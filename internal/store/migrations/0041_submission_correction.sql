-- A correction re-sends an application that was already submitted. Its ledger
-- row names the row it replaces (`supersedes`) and is marked `correction`; the
-- two are set together, so the row reads as the same application sent again,
-- never as a new one. A posting is corrected once: no two rows that still
-- count supersede the same row, and a failed correction attempt drops out of
-- the index and does not use the correction up.
ALTER TABLE submissions ADD COLUMN supersedes INTEGER REFERENCES submissions (id);
ALTER TABLE submissions ADD COLUMN correction INTEGER NOT NULL DEFAULT 0 CHECK (correction = (supersedes IS NOT NULL));

CREATE UNIQUE INDEX submissions_supersedes ON submissions (supersedes) WHERE supersedes IS NOT NULL AND state <> 'failed';
