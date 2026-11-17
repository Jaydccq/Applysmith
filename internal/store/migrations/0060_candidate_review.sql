-- The model's sort of a waiting new-entry suggestion: whether it is worth an
-- entry (verdict), the one line the user reads why, and for a reusable one an
-- answer drafted from the user's own profile and CV with the facts it cites.
-- reviewed_at is NULL until the review is stored; a suggestion brought back
-- after a rejection is reviewed again.
ALTER TABLE answer_candidates ADD COLUMN review_verdict TEXT NOT NULL DEFAULT ''
    CHECK (review_verdict IN ('', 'reusable', 'per-application', 'optional', 'follow-up', 'company-specific', 'trap', 'sensitive'));
ALTER TABLE answer_candidates ADD COLUMN review_reason TEXT NOT NULL DEFAULT '';
ALTER TABLE answer_candidates ADD COLUMN review_draft TEXT NOT NULL DEFAULT '';
ALTER TABLE answer_candidates ADD COLUMN review_cited_json TEXT NOT NULL DEFAULT '[]';
ALTER TABLE answer_candidates ADD COLUMN reviewed_at TEXT;
