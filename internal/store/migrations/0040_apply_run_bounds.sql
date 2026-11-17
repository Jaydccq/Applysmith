-- The per-run bounds a run spent (captcha solves, verification-email presses,
-- password resets per host), so a run the user resumes with Done continues
-- them instead of starting from zero.
ALTER TABLE apply_runs ADD COLUMN bounds_json TEXT NOT NULL DEFAULT '{}';
