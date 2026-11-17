-- Why an attempt ran on its candidate: empty for the configured primary, else
-- fallback, first-token, rested or cap-downgrade.
ALTER TABLE llm_ledger ADD COLUMN reason TEXT NOT NULL DEFAULT '';
