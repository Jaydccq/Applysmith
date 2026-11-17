-- The line a dashboard pass (a company brief, story proposals, a knowledge
-- ingest, answer coaching) ended with, which the run board shows as what it
-- came to; empty for a run whose outcome lives elsewhere.
ALTER TABLE runs ADD COLUMN outcome TEXT NOT NULL DEFAULT '';
