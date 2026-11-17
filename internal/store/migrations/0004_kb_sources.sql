-- Knowledge base sources: ingested GitHub repositories, the content-addressed
-- cache of the files read from them, and story-mining sessions. Timestamps are
-- RFC 3339 UTC text.

-- Drafts built from a repository carry its id, so removing the repository
-- removes its unpublished drafts and leaves what was published.
ALTER TABLE kb_drafts ADD COLUMN repo_id INTEGER NOT NULL DEFAULT 0;

CREATE TABLE kb_repos (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    owner       TEXT NOT NULL,
    name        TEXT NOT NULL,
    branch      TEXT NOT NULL DEFAULT '',
    commit_sha  TEXT NOT NULL DEFAULT '',
    status      TEXT NOT NULL DEFAULT 'added' CHECK (status IN ('added', 'ingested', 'failed')),
    report_json TEXT NOT NULL DEFAULT '{}',
    created_at  TEXT NOT NULL,
    ingested_at TEXT NOT NULL DEFAULT '',
    UNIQUE (owner, name)
) STRICT;

-- One row per file decision at the commit that was read: the stored text is
-- cut to the digest budget and holds no secret. A file with a secret keeps its
-- row (status 'redacted') without any text.
CREATE TABLE kb_repo_blobs (
    repo_id  INTEGER NOT NULL REFERENCES kb_repos (id) ON DELETE CASCADE,
    blob_sha TEXT NOT NULL,
    path     TEXT NOT NULL,
    status   TEXT NOT NULL CHECK (status IN ('ok', 'redacted', 'binary')),
    reason   TEXT NOT NULL DEFAULT '',
    size     INTEGER NOT NULL DEFAULT 0,
    text     TEXT NOT NULL DEFAULT '',
    PRIMARY KEY (repo_id, blob_sha)
) STRICT;

CREATE TABLE kb_mining_sessions (
    id          TEXT PRIMARY KEY,
    quadrant    TEXT NOT NULL CHECK (quadrant IN ('project', 'team', 'customer', 'failure')),
    state       TEXT NOT NULL CHECK (state IN ('collecting', 'ready', 'saved', 'abandoned')),
    notes_from  TEXT NOT NULL DEFAULT '',
    fields_json TEXT NOT NULL DEFAULT '{}',
    log_json    TEXT NOT NULL DEFAULT '[]',
    draft_id    INTEGER NOT NULL DEFAULT 0,
    created_at  TEXT NOT NULL,
    updated_at  TEXT NOT NULL
) STRICT;
