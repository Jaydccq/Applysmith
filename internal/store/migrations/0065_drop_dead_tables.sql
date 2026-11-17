-- Two tables nothing writes or reads: the submission ledger is `submissions`
-- (internal/ledger), and a scan's evaluation jobs live in its scan record.
DROP TABLE IF EXISTS submit_claims;
DROP TABLE IF EXISTS scan_run_jobs;
