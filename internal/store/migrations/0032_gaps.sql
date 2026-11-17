-- Skill-gap analyses (job-evaluation). An analysis is a run's artifact, not a
-- posting report: it has its own tables and no score. The result is stored
-- whole; the user's confirmations and study progress sit beside it so that
-- neither touches the profile, the answer bank or any fit weight.

CREATE TABLE gap_reports (
    run_id       TEXT PRIMARY KEY REFERENCES runs (id),
    created_at   TEXT NOT NULL,
    result_json  TEXT NOT NULL,
    plan_json    TEXT
) STRICT;

-- A skill the user said they lack. Without a row a gap stays not-evidenced.
CREATE TABLE gap_confirmations (
    skill        TEXT PRIMARY KEY CHECK (skill <> ''),
    confirmed_at TEXT NOT NULL
) STRICT;

-- A study exercise the user marked complete. It records progress only.
CREATE TABLE gap_progress (
    run_id  TEXT NOT NULL REFERENCES gap_reports (run_id),
    skill   TEXT NOT NULL,
    done_at TEXT NOT NULL,
    PRIMARY KEY (run_id, skill)
) STRICT;
