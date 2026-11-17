-- The press claim (apply-safety-gates): a `submitting` row names the claim that
-- holds it, the process that took it and when, so the start-up sweep can tell a
-- dead owner from a live one and a settlement can find its row. Rows written by
-- the person's own submit carry none of the three.
ALTER TABLE submissions ADD COLUMN claim_id TEXT CHECK (claim_id IS NULL OR length(claim_id) BETWEEN 1 AND 100);
ALTER TABLE submissions ADD COLUMN pid INTEGER;
ALTER TABLE submissions ADD COLUMN claimed_at TEXT;

CREATE UNIQUE INDEX submissions_claim ON submissions (claim_id) WHERE claim_id IS NOT NULL;
