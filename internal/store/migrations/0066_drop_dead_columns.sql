-- Columns nothing reads: an apply run's ladder and budget, an evaluation job's
-- inbox release flag, an artifact's planned hash (always its content hash) and
-- a Gmail message's history id (always empty).
ALTER TABLE apply_runs DROP COLUMN ladder_json;
ALTER TABLE apply_runs DROP COLUMN budget_json;
ALTER TABLE evaluation_jobs DROP COLUMN inbox_released;
ALTER TABLE report_artifacts DROP COLUMN planned_hash;
ALTER TABLE gmail_evidence DROP COLUMN history_id;
