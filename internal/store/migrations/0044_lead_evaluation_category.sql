-- Review categories must not erase whether a lead is evaluated from its own
-- text or from its owning board. Existing review rows have no trustworthy
-- original category; leave them empty rather than guessing from prose/URLs.
ALTER TABLE leads ADD COLUMN evaluation_category TEXT NOT NULL DEFAULT ''
    CHECK (evaluation_category IN ('', 'posting', 'no-ats', 'email', 'dm', 'comment'));
