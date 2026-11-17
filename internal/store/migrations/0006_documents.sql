-- Application documents (application-documents). A document version is
-- written once: its files, its row and the tracker flags commit together, and
-- nothing but the freeze stamp and the candidacy link changes afterwards.
-- "Current" is the newest version of a kind for a candidacy (or for the
-- baseline); "ready" is the newest one whose PDF passed its checks. Both are
-- derived, so deleting a version or merging two candidacies cannot leave a
-- pointer behind.

CREATE TABLE documents (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    bundle        TEXT NOT NULL CHECK (length(bundle) BETWEEN 1 AND 80),
    version       INTEGER NOT NULL CHECK (version >= 1),
    kind          TEXT NOT NULL CHECK (kind IN ('resume', 'cover-letter', 'interview-pack')),
    -- A baseline document has no candidacy and claims no tailoring.
    mode          TEXT NOT NULL CHECK (mode IN ('tailored', 'baseline')),
    candidacy_id  INTEGER REFERENCES tracker_entries (id),
    path          TEXT NOT NULL,
    source_path   TEXT NOT NULL,
    content_hash  TEXT NOT NULL,
    manifest_hash TEXT NOT NULL,
    generation    TEXT NOT NULL CHECK (generation IN ('model', 'deterministic')),
    language      TEXT NOT NULL,
    paper         TEXT NOT NULL CHECK (paper IN ('a4', 'letter')),
    page_limit    INTEGER NOT NULL CHECK (page_limit IN (1, 2)),
    limited       INTEGER NOT NULL DEFAULT 0 CHECK (limited IN (0, 1)),
    pdf_state     TEXT NOT NULL CHECK (pdf_state IN ('none', 'ready', 'unverified', 'failed', 'unavailable')),
    pdf_path      TEXT,
    pdf_sha256    TEXT,
    pages         INTEGER,
    warnings_json TEXT NOT NULL DEFAULT '[]',
    run_id        TEXT,
    frozen_at     TEXT,
    created_at    TEXT NOT NULL,
    UNIQUE (bundle, version, kind),
    CHECK ((mode = 'baseline') = (candidacy_id IS NULL)),
    CHECK (pdf_state IN ('none', 'unavailable') OR pdf_sha256 IS NOT NULL)
) STRICT;

CREATE INDEX documents_candidacy ON documents (candidacy_id, kind, id);

CREATE TRIGGER documents_write_once BEFORE UPDATE ON documents
WHEN NEW.id IS NOT OLD.id OR NEW.bundle IS NOT OLD.bundle OR NEW.version IS NOT OLD.version OR NEW.kind IS NOT OLD.kind
  OR NEW.mode IS NOT OLD.mode OR NEW.path IS NOT OLD.path OR NEW.source_path IS NOT OLD.source_path
  OR NEW.content_hash IS NOT OLD.content_hash OR NEW.manifest_hash IS NOT OLD.manifest_hash
  OR NEW.generation IS NOT OLD.generation OR NEW.language IS NOT OLD.language OR NEW.paper IS NOT OLD.paper
  OR NEW.page_limit IS NOT OLD.page_limit OR NEW.limited IS NOT OLD.limited OR NEW.pdf_state IS NOT OLD.pdf_state
  OR NEW.pdf_path IS NOT OLD.pdf_path OR NEW.pdf_sha256 IS NOT OLD.pdf_sha256 OR NEW.pages IS NOT OLD.pages
  OR NEW.warnings_json IS NOT OLD.warnings_json OR NEW.run_id IS NOT OLD.run_id OR NEW.created_at IS NOT OLD.created_at
  OR (OLD.frozen_at IS NOT NULL AND NEW.frozen_at IS NOT OLD.frozen_at)
BEGIN
    SELECT RAISE(ABORT, 'a document version is written once');
END;

-- The outcome of a document run: the versions it published, or why it did not.
CREATE TABLE document_jobs (
    run_id       TEXT PRIMARY KEY REFERENCES runs (id),
    kind         TEXT NOT NULL CHECK (kind IN ('resume', 'cover-letter', 'pair', 'pdf')),
    candidacy_id INTEGER,
    outcome      TEXT CHECK (outcome IS NULL OR outcome IN ('published', 'failed')),
    -- Version ids, generation mode, warnings and retrieval metadata (counts and
    -- source paths, never retrieved text).
    detail_json  TEXT NOT NULL DEFAULT '{}',
    created_at   TEXT NOT NULL,
    finished_at  TEXT
) STRICT;

-- Workspace document preferences: one row, edited with its revision.
CREATE TABLE document_preferences (
    id           INTEGER PRIMARY KEY CHECK (id = 1),
    language     TEXT NOT NULL,
    paper        TEXT NOT NULL CHECK (paper IN ('a4', 'letter')),
    resume_pages INTEGER NOT NULL CHECK (resume_pages IN (1, 2)),
    revision     INTEGER NOT NULL CHECK (revision >= 1),
    updated_at   TEXT NOT NULL
) STRICT;

INSERT INTO document_preferences (id, language, paper, resume_pages, revision, updated_at)
VALUES (1, 'en', 'a4', 2, 1, strftime('%Y-%m-%dT%H:%M:%SZ', 'now'));
