-- Knowledge base: the index over the workspace's knowledge files, drafts,
-- retrieval gaps and the small state the company briefs need. The Markdown
-- files remain the source of truth; every index table can be rebuilt from them.
-- Timestamps are RFC 3339 UTC text.

-- One row per indexed file. path is workspace-relative with forward slashes.
-- state is 'ok', 'broken' (unparseable frontmatter), 'secret' (rejected by the
-- secret scan) or 'duplicate' (its id is declared by an earlier path).
CREATE TABLE kb_docs (
    path         TEXT PRIMARY KEY,
    kind         TEXT NOT NULL CHECK (kind IN ('snippet', 'company-brief', 'cv', 'profile', 'narrative')),
    id           TEXT NOT NULL DEFAULT '',
    category     TEXT NOT NULL DEFAULT '',
    state        TEXT NOT NULL CHECK (state IN ('ok', 'broken', 'secret', 'duplicate')),
    warning      TEXT NOT NULL DEFAULT '',
    file_hash    TEXT NOT NULL,
    content_hash TEXT NOT NULL DEFAULT '',
    title        TEXT NOT NULL DEFAULT '',
    description  TEXT NOT NULL DEFAULT '',
    meta_json    TEXT NOT NULL DEFAULT '{}',
    modified_at  TEXT NOT NULL,
    indexed_at   TEXT NOT NULL
) STRICT;

CREATE INDEX kb_docs_id ON kb_docs (id) WHERE id <> '';

-- The sections of an ok document. tokens is the space-joined list of the
-- section's indexed terms, repeats kept, so ranking needs no re-tokenizing.
-- sensitive names the profile category a section belongs to, or ''.
CREATE TABLE kb_sections (
    path         TEXT NOT NULL REFERENCES kb_docs (path) ON DELETE CASCADE,
    ord          INTEGER NOT NULL,
    heading      TEXT NOT NULL,
    text         TEXT NOT NULL,
    section_hash TEXT NOT NULL,
    tokens       TEXT NOT NULL,
    sensitive    TEXT NOT NULL DEFAULT '',
    PRIMARY KEY (path, ord)
) STRICT;

-- Section vectors keyed by the section's content hash and the embedding model.
CREATE TABLE kb_embeddings (
    model        TEXT NOT NULL,
    section_hash TEXT NOT NULL,
    vector       BLOB NOT NULL,
    used_at      TEXT NOT NULL,
    PRIMARY KEY (model, section_hash)
) STRICT;

-- The bytes a snippet had before its last edit, removed with the snippet.
CREATE TABLE kb_previous (
    path     TEXT PRIMARY KEY,
    content  BLOB NOT NULL,
    saved_at TEXT NOT NULL
) STRICT;

-- Drafts. key is stable across revisions of one draft; revision counts from 1.
-- Only the newest revision of a key can be published. source_run is set for a
-- draft built by a source run, of which a run keeps at most one.
CREATE TABLE kb_drafts (
    id             INTEGER PRIMARY KEY AUTOINCREMENT,
    key            TEXT NOT NULL,
    revision       INTEGER NOT NULL CHECK (revision >= 1),
    status         TEXT NOT NULL CHECK (status IN ('draft', 'ready', 'published', 'superseded')),
    kind           TEXT NOT NULL CHECK (kind IN ('snippet', 'story')),
    origin         TEXT NOT NULL CHECK (origin IN ('user', 'import', 'repo', 'mining')),
    category       TEXT NOT NULL DEFAULT '',
    snippet_id     TEXT NOT NULL,
    markdown       TEXT NOT NULL,
    facts_json     TEXT NOT NULL DEFAULT '[]',
    source_run     TEXT NOT NULL DEFAULT '',
    published_path TEXT NOT NULL DEFAULT '',
    created_at     TEXT NOT NULL,
    updated_at     TEXT NOT NULL,
    UNIQUE (key, revision)
) STRICT;

CREATE UNIQUE INDEX kb_drafts_source_run ON kb_drafts (source_run)
    WHERE source_run <> '' AND kind = 'snippet' AND status <> 'superseded';

-- Answers to follow-up questions, one per question of a draft key; a later
-- answer replaces the earlier one.
CREATE TABLE kb_story_answers (
    draft_key   TEXT NOT NULL,
    question_id TEXT NOT NULL,
    dimension   TEXT NOT NULL,
    kind        TEXT NOT NULL CHECK (kind IN ('metric', 'text')),
    text        TEXT NOT NULL,
    created_at  TEXT NOT NULL,
    updated_at  TEXT NOT NULL,
    PRIMARY KEY (draft_key, question_id)
) STRICT;

-- Retrieval calls that found nothing, folded on read by terms_key.
CREATE TABLE kb_gaps (
    id        INTEGER PRIMARY KEY AUTOINCREMENT,
    question  TEXT NOT NULL,
    terms_key TEXT NOT NULL,
    asked_at  TEXT NOT NULL
) STRICT;

CREATE INDEX kb_gaps_terms ON kb_gaps (terms_key);

-- Per company site: when a fetch last found no usable page, and the hash of the
-- brief file this product wrote, which tells a later user edit apart.
CREATE TABLE kb_brief_state (
    domain        TEXT PRIMARY KEY,
    miss_until    TEXT NOT NULL DEFAULT '',
    written_hash  TEXT NOT NULL DEFAULT ''
) STRICT;
