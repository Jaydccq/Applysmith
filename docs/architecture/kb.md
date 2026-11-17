# Knowledge base

Implementation notes for `internal/kb` (task 3.6). The normative text is
`specs/knowledge-base`; this file says where things live and records the
choices the spec leaves open. The plan to answer application essays from
structured experience records is the last section,
[Plan: essay questions answered from experience records](#plan-essay-questions-answered-from-experience-records-2026-11-06).

## Storage

The Markdown files under `<workspace>/knowledge/` are the truth for published
knowledge. The database holds an index that can be rebuilt from them
(`kb_docs`, `kb_sections`, `kb_embeddings`) and the state that has no file yet:
drafts, answers, gaps, repository sources and mining sessions (migrations
`0003_kb.sql`, `0004_kb_sources.sql`, `0005_kb_repo_names.sql` for the lower-case unique repository names, and `0055_kb_draft_base_hash.sql` for the file hash an update draft was made from):

| Table | Holds |
|---|---|
| `kb_docs`, `kb_sections` | one row per indexed file and one per section (text, section hash, indexed terms) |
| `kb_embeddings` | section vectors keyed by section hash and model id |
| `kb_previous` | the bytes a snippet had before its last edit |
| `kb_drafts`, `kb_story_answers` | drafts (revisions of one key) and follow-up answers |
| `kb_gaps` | retrieval calls that found nothing |
| `kb_repos`, `kb_repo_blobs` | repository sources (migration `0004`), with the commit read, and the per-file cache keyed by blob SHA |
| `kb_mining_sessions` | story-mining sessions with their fields and transcript (`0004`) |
| `kb_brief_state` | per company site: the 24-hour miss and the hash of the brief file this product wrote |

`Service.Sync` runs before every search and every listing. It compares each
file's SHA-256 with the stored one (never a timestamp), parses what changed and
drops what vanished; an index from another `indexVersion` is dropped whole.
The comparison is a read (`indexCurrent`): a pass that finds nothing to do
takes no write transaction, so searches do not queue on the single writer.
`Show`, `Find`, the answer of `Edit` and the page lookup of a repository read each load the index once.
`applysmith kb reindex` forces the same rebuild. A source that cannot be read
(`knowledge/` unreadable, `cv.md` unreadable) is reported as a failure, its
rows are left alone, and the search result says `sources_unavailable` or
`partial` instead of `no_match`.

## Files in the package

| File | Concern |
|---|---|
| `kb.go`, `files.go` | the service, its options and typed failures; atomic writes, `writeRecorded`, slugs, workspace paths |
| `snippet.go`, `markdown.go` | frontmatter parsing (unknown keys survive a rewrite), sections, links, citations, content hash |
| `index.go`, `sources.go` | file walk, fact sources (`cv.md`, `profile.yml`, `narrative.md`; `voice.md` is never indexed: letters and messages read it as style), index sync |
| `tokenize.go`, `retrieve.go`, `search.go`, `evidence.go` | tokenizer, BM25, triggers and boosts, facts, merge and conflicts, gaps |
| `semantic.go` | optional embedding stage |
| `secrets.go` | the secret scan every write path calls |
| `proc_unix.go`, `proc_windows.go` | the process group a git run is stopped with |
| `lint.go`, `dedup.go`, `trigger.go` | ingest lint, event fingerprints, trigger matching |
| `drafts.go`, `publish.go`, `manage.go` | drafts, import, publish, edit, delete, export |
| `pages.go` | the user's own snippets and stories, page revisions, story proposals from the CV |
| `brief.go`, `fetch.go`, `flight.go` | company briefs, the guarded fetch, the 30-minute memo of what a posting page declared, a site or nothing (concurrent fetches of one page share one run through `internal/flight`) |
| `competencies.go`, `coverage.go`, `quality.go`, `followup.go` | competency set, the two grids, story gates and trigger honesty, follow-up answers |
| `repo.go`, `repofiles.go`, `ghapi.go`, `ghclone.go`, `ghtoken.go`, `ingest.go`, `digest.go`, `repopages.go` | GitHub sources: reference parsing, file rules, REST client, shallow-clone listing, ingestion, model drafting, edits to existing pages from a repository read |
| `ghaccount.go` | the signed-in GitHub account (OAuth device flow) and its repository picker |
| `mining.go`, `mining_util.go`, `model.go` | story-mining sessions and the model interface |
| `storymatch.go` | `MatchStories` (ranked published stories for a text, no gap recorded; interview prep links questions with it) and `StoryTexts` |

## Choices

- **Tokenizer.** Letters and digits of any script, lowercased, inner
  apostrophe kept, a hyphenated word read as its parts and the parts joined,
  a run of Han or Kana (written without spaces) read as its overlapping
  character pairs, a possessive "'s" dropped, C++, C#, F# and .NET read as
  one term each, accents compared in composed form
  (a tokenizer change bumps `indexVersion`, which rebuilds the index on the
  next sync), one-character tokens and stop words dropped, then a light
  fold (`ies` to `y`, a final `s`, `-ing`/`-ed`, then a final `e` off every
  form, so "code", "coded" and "coding" are one term; "used" and "using" read
  "use"). The fold only has to map the forms of one word to one term, so the
  terms are not always words.
  The stop list holds the question scaffolding words.
- **Floor.** A section clears it with two distinct shared terms. A snippet's
  rendered section is the best-scoring one among those that clear; a snippet
  carried only by a trigger or an archetype tag keeps its best-scoring section.
  Non-snippet sources (CV, profile, narrative, briefs) have no triggers and need
  the floor.
- **Profile facts.** `profile.yml` is flattened to one "Label: value" section
  per fact. Settings blocks (`scan`, `notifications`, the eligibility gate) are
  not knowledge. Sections under work authorization, compensation, birth date,
  self-identification and criminal history are returned only when the query
  matches that category (`sensitiveQuery` in `evidence.go`).
- **Facts.** A single-line "Label: value" section is one fact with the id
  `semantic:<label>`; a longer section is one fact with its source and path
  plus a labeled fact per "Label: value" line it holds or lists as allowed
  facts. Facts of equal id and text merge; equal id and different text are both
  kept and name each other.
- **Gaps.** A search that finds nothing, and whose sources were all readable,
  records the question. The list folds on the stemmed content terms of the
  question after leaving out capitalised words that do not start it, so the
  same question about two employers is one row.
- **Secrets.** One scan (`ScanSecrets`) covers private key blocks, well-known
  token prefixes, signed tokens, bearer tokens, connection strings with a
  password, and high-entropy values assigned to a secret-like name. It runs on
  drafts, imports, publishes, edits, answers, fetched briefs and every file
  that is indexed; a file that fails is not indexed and shows up as a warning
  naming path and line. Provider keys live in the keychain and are never read
  here.
- **Briefs.** `DeclaredSite` and `EnsureBrief` read the declared site only;
  the fetch is behind `Fetcher`. `GuardedFetcher` resolves the host itself,
  refuses non-public addresses on every connection (so every redirect hop), and
  caps redirects, time and body. A brief the user edited is rewritten with
  `manual: true`; a file that differs from the hash this product wrote is also
  treated as manual. Search treats an expired non-manual brief as absent.
  A new brief is condensed by the `Summarizer` (research-search) unless the
  request is `Unsummarized`: the answer assist asks that way, because its
  company explainer condenses the brief under the caller's deadline, while a
  summary inside the assist's five-second enrichment budget was cancelled in
  70% of calls (ledger 2026-11-06 to 11-08: 123 aborted, billed, the text kept
  raw anyway).
- **Drafts.** A draft key is the slug of the snippet id; revisions share it and
  only the newest can be published. A source run keeps one snippet draft
  (`UpsertSourceDraft`). A source or repository story draft that was published
  is not rebuilt while its page exists; once the user deletes the page, the next
  read drafts it again. Publishing writes through a temp file and rename and
  keeps the replaced bytes in `kb_previous`, and promotes the draft's promotable
  statements into `allowed_facts` (a story through `PublishStory`, any other
  snippet through `PublishDraft`). A publish and an edit write the file as the
  last step of the transaction that records them (`writeRecorded`): a record the
  database refuses leaves the file alone, and a transaction that fails to commit
  puts the file back as it was, so the page, its previous version and the draft
  never disagree. An update draft replaces a published page and
  carries `base_hash`, the file hash it was made from; publishing it when the
  file has another hash, or is gone, is `ErrStale` (`revision_conflict`) and
  writes nothing.
- **Stories.** `PublishStory` is the only path for story drafts. It runs the
  structural gates (two principles, source type, dimensions, the four sections,
  no `TODO`) and the quality checks, merges the principles' question patterns
  into the triggers, drops the ones the story's text and facts do not support,
  keeps an inferred metric the user did not edit out of `allowed_facts`, and
  writes `dimensions` into the file. `RecordAnswer` is the one function behind
  both the dashboard and the agent: it stores the answer and folds it into a new
  revision in one write. Whether an answer is already in is read from the
  draft: a question the draft asks again (a repository re-read dropped the
  fold) takes the same answer again.

- **Edit token.** `Edit` takes `Document.FileHash`, the hash of the file's
  bytes, because the content hash leaves out title and description and a
  concurrent retitle would be reverted silently. A stale hash is `ErrStale`
  (`revision_conflict` on the wire): the editor keeps the user's text and
  offers to keep it over the new version or to load the new version.
- **Undoable deletes.** Owner decision 2026-10-23 ("make it undoable"), replacing
  the earlier "no hidden copy of deleted knowledge" rule: deleting a snippet or
  brief, rejecting a draft, removing a repository source and deleting an
  interview can be undone (Wiki → Company briefs has its own Delete). The delete returns a token; `Restore` puts back the
  file and the verbatim rows it removed (previous version, brief state,
  every draft revision, answers, the source with its cached files, the
  session) and syncs the index. The copy is held in the server's memory only
  (`UndoWindow`, 15 minutes, at most 20 deletes, gone on restart; never on
  disk), because draft rows must not round-trip through the client. A restore
  over an item made again since (same id, draft key or repository) is a
  conflict and writes nothing; an expired token is `not_found`. The CLI's
  `kb delete` stays final.
- **Files on disk.** The knowledge root may be a symlink (the walk starts at its
  target); a symlink inside it is never followed, and every read that precedes
  a write (`readRegular`) refuses a file that is not a regular file.
- **Triggers.** At most 32 per snippet and 512 bytes each. A pattern is compiled
  once and cached; one that does not compile, is too long or matches the empty
  string never matches and raises `invalid_trigger`.
- **Lint.** Warnings only. `empty_allowed_facts` is raised for a story alone:
  the duplicate-event check reads story facts, while any other snippet is used
  by its text, so one from the New snippet form (which has no fact field) is
  not flagged.
- **Search size.** `k` bounds the number of facts after the semantic hits are
  merged. A query that looks like a credential records no gap.
- **List search.** The Wiki page's search boxes (pages, stories) match
  title, tags, themes and company on the page and also ask `Find` (`GET
  /api/kb/documents?q=`) for the documents whose text holds every word of the
  query as a word start, with the search's tokenizer: "kafk" and "Kafka" find
  a snippet that says Kafka in its text, "migrations" finds "migration". It is
  not `Search`: it ranks nothing and records no gap, so typing never leaves
  half-words in `kb_gaps`.
- **Runs the page did not start.** `GET /api/kb/sources` names each repository's
  read that is still working (`readingRunId`) and `GET /api/kb/documents` the
  brief build (`briefRunId`), so a page opened during one follows it with the
  run's link instead of offering a second press the server would refuse. The
  repositories and the drafts read again once the `runs` notices have been
  quiet for a second (`useTopicsSettled`), not on each step a run announces.
- **Finding things on the page.** Snippets list newest first (or by title),
  and a snippet just added is marked New, brought into view, and a search
  that would hide it is cleared. Company briefs have a search (company or
  site) and sort by update or company. When the text search does not answer,
  a quiet note says only titles and tags were searched. A draft in Drafts to
  review reads as its rendered text (`body`, the Markdown without its
  frontmatter, from `kb.BodyOf`) with its statements listed; the frontmatter
  shows only in the editor. A repository's "It wrote N drafts" links Drafts
  to review, and an empty story bank points at the Story interview.

## Repository ingestion

`AddRepo` registers `owner/repo` (or an https GitHub URL); `IngestRepo` reads it.

- **One commit.** The default branch is resolved to a commit SHA once; the tree
  of that SHA is listed and every file is fetched by the blob SHA the tree
  names, so a branch that moves cannot mix versions. Only HTTPS to the API host
  is accepted (a plain-http address exists only as a test seam), redirects stay
  on the API host, and replies are size-capped.
- **Truncated tree.** The partial listing is never used. `git` lists the tree
  from a blobless shallow clone at the same SHA with a fixed argument vector
  (`-c core.hooksPath=/dev/null`, `fetch --depth 1 --filter=blob:none`, no
  `ls-tree -l` because sizes would make git fetch every blob), an environment
  with no prompt, no user or system configuration and `GIT_ALLOW_PROTOCOL=https`,
  its own process group (killed on cancel or timeout), a 120 second limit, an
  output cap and a disk watchdog. The temporary directory is always removed. The
  token travels in the environment, never on the command line.
- **No sizes on the clone path.** The clone listing has none, so a file larger
  than the blob reply cap is found when it is fetched and skipped as larger
  than 128 KiB; it does not fail the ingestion.
- **Which files.** `repofiles.go` holds the deny list (vendored, generated and
  build directories, lockfiles, minified files, source maps, private
  environment files, binaries, images, audio and video, archives, fonts, logs)
  and the allow list (README and project documents, manifests, a public
  environment template, documentation, workflows, recognised source languages).
  Deny wins and anything else is out. At most 80 files are read, chosen by a
  deterministic rank; larger than 128 KiB is skipped by declared and by decoded
  size; a NUL byte, invalid UTF-8 or many control characters marks content as
  binary.
- **Secrets.** Every file passes the secret scan before any of it is kept. A
  file with a secret is listed as redacted by path and kind of secret, its text
  is not stored, and the model never sees it.
- **Concurrency and limits.** Four blob fetches at a time; a 403 or 429 with a
  rate-limit signal is retried with the server's wait up to 30 seconds and then
  reported as rate limited; a 403 without a signal is a permission failure.
- **Unchanged commits.** The commit SHA is stored on the source; ingesting the
  same SHA again fetches nothing unless forced, and a forced run (or a new
  commit) reuses cached files by blob SHA (`kb_repo_blobs`). A read whose
  drafting failed (the model call or its reply) is a failed ingestion: the
  commit is not stored, so the next read drafts again from the cached files.
- **Token.** `Options.GitHubToken` is called per ingestion. The server and the
  CLI pass the vault's `github-token`, else the output of the user's `gh auth
  token`. It is not stored and error text is scrubbed of it. The vault token is
  what a pasted token or the GitHub account sign-in below stores.
- **GitHub account.** `GitHubAccount` (`ghaccount.go`) signs in with the OAuth
  device flow (`golang.org/x/oauth2` `DeviceAuth` / `DeviceAccessToken`, scope
  `repo`, endpoints `github.com/login/device/code` and
  `/login/oauth/access_token`) of an OAuth App whose public client id is the
  constant `kb.GitHubClientID` (the owner's "Applysmith" app, registered
  2026-10-26 with user tokens that do not expire: the vault keeps one token and
  nothing refreshes it).
  `POST /api/kb/github/signin` returns the code and the address to enter it at;
  the server then polls GitHub in the background (the library keeps to GitHub's
  interval and slows down when told to) and stores the token in the vault as
  `github-token`; a denied or expired code ends in `error` and stores nothing.
  `GET /api/kb/github` reads the account: `signed-in` through the vault token,
  else through `gh auth token`, with `source` `vault` or `gh` and the login that
  `GET /user` names; `pending` with the code while a sign-in waits; `error`
  after a failed one or for a token GitHub refuses; else `signed-out`.
  `DELETE /api/kb/github` ends a waiting sign-in and removes the vault token; it
  cannot sign the `gh` command out, and the reply then reads `signed-in` with
  source `gh`. `GET /api/kb/github/repos?page=` lists the account's repositories
  (`/user/repos`, newest push first, 50 a page, owner, collaborator and
  organization member) with `alreadyAdded` for those that are sources. The token
  never reaches the browser; the client is the ingestion's (https to the API host
  only, size caps, redirects on the host, errors scrubbed of the token). The
  endpoints are overridable only as a test seam.
- **Drafts.** With a model, the digest (4,000 characters per file, 8,000 for the
  12 highest ranked, 60,000 in total, the rest as symbol names) goes to the
  `story-extraction` task together with the published pages the repository
  touches (`repopages.go`): the project page when it exists, and up to five
  others found by `Find` over the repository name, its README title and the six
  terms the README uses most (a page needs the name or title, or two terms), at
  most 3,000 characters of the project page and 1,500 of each other. The reply
  is the project page with its key points (`text`, `path`, `quote`), evidence
  for other listed pages, and up to six candidate stories. A statement is
  `cited` when its quote is in the named file (whitespace aside), a metric is
  `measured` only when its numbers are in that quote, anything else is
  `inferred`, and an inferred metric the user has not edited is never promoted at
  publish. A key point whose quote is not in its file is dropped and counted in
  the report's notes; a page the model was not given, or with no valid point, is
  dropped too. A story with no measured metric carries a `TODO` in its Result.
  A new project page is the one snippet draft of the repository (run `repo:<id>`).
  A page that exists is replaced through an update draft (`base_hash` set,
  category and id of the page): the project page gets its Overview,
  Architecture, Tech, Key points and Source sections replaced and a Related
  list that keeps its links; any other page keeps its text and gains one section
  `## <owner>/<name> (repository)` (replaced on the next read) with the summary, the
  cited points and a link back to the project page. Reading the repository again
  refines the update draft still in review, with the file's new hash; a page
  with another draft in review is left alone and noted. Publishing promotes the
  cited and measured points into `allowed_facts`, keeping the facts the page had.
  Nothing is published by the read.
- **Removal.** `RemoveRepo` deletes the source, its cached files and its
  unpublished drafts; published snippets stay. While the repository is being
  read the server refuses the removal (`conflict`), so the run cannot write
  drafts for a repository that is gone; the page disables Remove until the read ends.
  The removal can be undone (see Undoable deletes).

## Wiki maintenance from Ask

The Ask Agent keeps wiki pages (published snippets, the same files and index as
any snippet) from what the user tells it. Three tools in `internal/agent/tools_wiki.go`
and the skill `skills/wiki.md`; no model call happens inside a tool, so the
approval card shows exactly the text that is written.

- **Find and read.** `wiki_find` lists the snippets whose title, tags or text
  hold every word of the query (`Service.Find`): id, category, title,
  description, the link path other pages use (`/projects/<id>.md`) and the file
  hash. `wiki_read` returns one page's body, its allowed facts, the links it
  holds and its file hash; a page over 10,000 characters is not returned, so
  the model never rewrites a page from a cut copy. `previous: true` returns the
  text before the last edit (`PreviousVersion`).
- **Write.** `wiki_write` without `id` is `CreateSnippet` (origin `user`,
  published at once, `facts` become `allowed_facts`); with `id` it is
  `Revise` with the `expected_hash` the model read, which is `Edit`'s file-hash
  check, so a page changed since the read is refused (`ErrStale`) and the tool
  tells the model to read it again. The model sends the body only; the kb
  service owns the frontmatter and keeps every key it does not manage. `facts`
  are appended to `allowed_facts` (existing ones stay) and are the user's own
  statements; the skill says so. Instruction-shaped text is refused before the
  service is called; secrets, invalid text and lint findings come back as the
  tool's text.
- **Scope.** Stories (`behavioral`, or any snippet with a story source type) and
  company briefs are not written here: stories have gates (`story_mining`,
  `record_story_answer`), briefs are rebuilt from the site.
- **Size.** The body is at most 3,000 characters and a call at most 3,800
  argument bytes, because the approval card shows the arguments cut at 4,000
  bytes and what the user approves must be what is written. A larger page is
  split into linked pages or edited on the Wiki page.
- **Undo.** There is no undo tool. An edit keeps the previous version
  (`kb_previous`); `wiki_read` with `previous: true` shows it and `wiki_write`
  with that text and the current hash puts it back (facts added stay). A new
  page is removed by Delete, which is undoable for 15 minutes.

## Story mining sessions

A session targets one quadrant and asks situation, task, tension, action and
result in that order; each reply fills the field being asked and quotes
`expectedRevision`, the transcript length it read, so a resubmitted reply or
one from a tab that missed a question is `revision_conflict` instead of the
next field's answer. State is
`collecting`, `ready`, `saved` or `abandoned`. Sessions and transcripts live in
`kb_mining_sessions`, so a restart resumes at the same question.

- **Notes.** Pasted notes or a regular file inside the workspace go to the
  model, which names a passage per field; a passage is kept only when it occurs
  in the notes word for word and is stored as `notes` (user supplied). Without a
  model, starting from notes is refused.
- **Saving.** Only a ready session saves; an early save fails naming the next
  question. The draft is a story with source type from the quadrant (`work` for
  `project`, since `project` is reserved for repositories), the user's text in
  the four sections, no principles yet, and every statement as a user-supplied
  fact marked pending: `allowed_facts` stays empty until `ConfirmDraftFacts`.
  Saving again returns the draft's newest revision, or makes a new draft when
  the draft was deleted; replies to a session that is not
  collecting are ignored and not recorded.
- **Edits.** A reply with `field` replaces an answered part (`ReviseMining`)
  while collecting or ready (else `conflict`): source `user`, an `edit` line in
  the transcript, the reply's size and secret checks. It quotes
  `expectedRevision`, the transcript length it read, which every answer and
  edit grows (`updatedAt` is to the second), so a stale tab's edit is
  `revision_conflict`. State and next question stay as they were.
- **Structure.** A reply's own lines that would open a heading or a code fence get a leading backslash, so the user's words cannot add or hide a story section; a story with a repeated labeled section is blocked at publish (`duplicate_section`).
- **Gates.** The draft goes through the same story gates as any story, so
  publishing is refused until it has two principles. `PATCH /api/kb/drafts/{id}`
  with `principles` writes the themes the user picked (`SetDraftPrinciples`);
  every story draft carries its `principles` on the wire, so Drafts to review
  picks them for a story from any origin (a GitHub source, an interview saved
  without them) without editing the front matter.
- **Page.** Story bank → New stories runs the interview (suggested quadrant,
  unfinished sessions resumed; each answered part has Edit, which saves the
  new text, announces it and hands the keyboard back to that Edit; a refused
  stale edit shows the shared `Changed` notice). A ready one is saved; with two themes picked
  the page then sets them, confirms the user's answers and publishes, else the
  draft waits in Drafts to review. Stopping deletes the session; abandoning is
  CLI and agent only, as is starting from notes (a model call in the request).
- **Wording.** The fixed prompts are the product behavior; no model rewords
  them.
- **Suggestions.** `SuggestQuadrants` ranks quadrants by the empty cells they
  could fill: missing story shapes first, weighted so that a missing failure story outranks a missing conflict story, which outranks the project shapes (an empty bank suggests failure, team, project, customer), then principles with no counting story.

## Seams and their owners

Each optional input of `kb.Options` and the task that wires it. A seam that is
not wired does nothing; none of them is a stub that pretends to work.

| Seam | State | Owning task |
|---|---|---|
| `Fetcher` | wired to `GuardedFetcher` in the server and the CLI; the answer assist (company questions) and the Knowledge page's brief run call `EnsureBrief` | a brief is built from the declared site only: no search-backed source, by the spec and the owner's answer on FBB-105 |
| `Model` | wired to the router's `story-extraction` task (`api.KBModel`) in the server and, for repository and notes commands, the CLI | done in 3.6b |
| `GitHubToken` | wired to the vault's `github-token`, else `gh auth token` | done in 3.6b |
| `Summarizer` | wired in the shared server/CLI assembly | routed `research-search`; extracted text survives a provider failure |
| `Embedder` | `KBModel.Embed` adapts the routed embedding task; enabled per search only by `knowledgeSemanticRetrieval` | Settings → Runtime; default off, provider failure retains keyword results with a diagnostic |
| `PDFText` | server and CLI use bounded, cancellable Poppler extraction, independent of TeX/Python | `toolchain.ExtractPDFText`; 5 MiB import, 1 MiB extracted output, 30 s deadline |
| `Sources` (answer bank, corrections) | fixed, active, reviewed bank answers; manual new-entry corrections only after approval and while the bank still holds that answer | shared `KnowledgeOptions`; pending/rejected, sensitive, placeholder, archived and host overrides are excluded |

## Surfaces

`GET /api/kb/stories/coverage` (each theme carries its competency's `question_patterns` as `questions`,
worded as prompts: the library of the interview practice drill);
a draft that replaces the published page `snippetId` answers
`revision_conflict` when published after the page changed; the Ask Agent's `wiki_find`, `wiki_read` and `wiki_write`
(see "Wiki maintenance from Ask") use `Service` directly and have no route;
`GET|POST /api/kb/sources`, `POST /api/kb/sources/{id}/ingest` (a background run: 202 with the run id; its final outcome is `ingested` or `up-to-date`),
`POST /api/kb/briefs` (a background run of kind `kb.brief`: 202 with the run id; it ends naming the model task that condensed the site text, saying none did (no model route, or the model refused) or that a brief the user edited was kept, or fails with the reason: no company website declared, or no usable page) and
`POST /api/kb/stories/proposals` (a background run of kind `kb.propose`; the proposals are kept in memory until the next run and read with `GET /api/kb/stories/proposals`; a dismissed (`POST .../dismiss` `{title}`, undone by `.../restore`) or saved one leaves that list and later runs while the server runs, and a title a snippet or draft holds is never proposed; a missing story-extraction route is refused before any run),
`DELETE /api/kb/sources/{id}`; `GET|DELETE /api/kb/github`, `POST /api/kb/github/signin`,
`GET /api/kb/github/repos` (the GitHub account above); `GET|POST /api/kb/stories/mining`,
`GET /api/kb/stories/mining/suggestions`, `GET|DELETE /api/kb/stories/mining/{id}`
(every knowledge `DELETE` answers `{undo}`; `POST /api/kb/restore` `{undo}`
answers `{kind, name}`),
`POST /api/kb/stories/mining/{id}/replies|save` (every reply quotes
`expectedRevision`; one with `field` edits an answered part; all `standard` access,
remote denied, general limit class). CLI: `applysmith kb search | list | show | lint |
reindex | delete | export | import | drafts | publish | confirm | repo add|list|ingest|remove |
mine suggest|start|run|reply|show|save|abandon`. `list`, `export` and `delete`
cover repositories (with the commit read), sessions, answers, embeddings and
gaps (`repo:<id>`, `session:<id>`, `draft:<id>`, `gaps`, `embeddings`).
The reads (`search`, `list`, `show`, `lint`, `drafts`, `export`, `repo list`,
`mine suggest` and `mine show`) work beside a running server (one more writer,
since they sync the index); the commands that change something take the
workspace lock. The
listing, edit, draft and answer routes belong to the Wiki page (task 3.8)
and call the same `Service` methods. A page read (`GET /api/kb/documents/{id}`)
also answers `links` (the page's links to other pages, each with the id of the
page at that path, empty when there is none) and `linkedFrom` (the pages whose
text links to it): the Wiki's page view follows the first and lists the second.

## File import

Wiki → Pages accepts PDF, Markdown and text files, up to 5 MiB. The draft the
file yields must fit in 1 MiB, the most the index reads from one knowledge file;
a larger one is refused (and so is publishing a page over that size).
`POST /api/kb/imports?name=<filename>&category=<optional>` accepts raw file bytes
and returns a `KBDraft` (201). Missing Poppler is 503 with installation guidance;
malformed, encrypted or image-only PDFs are refused, with no draft created.
Extraction passes PDF bytes to `pdftotext -enc UTF-8 - -` without a shell or
persistent upload. No OCR or model call is implied. Imports have empty
`allowed_facts` and remain outside search until explicitly published through
`POST /api/kb/drafts/{id}/publish` or `applysmith kb publish`.

## Production retrieval assembly

`api.KnowledgeOptions` supplies PDF extraction, routed embeddings and brief
summaries, and curated evidence to the server, `applysmith kb` and document
generation. Evaluations also retrieve published snippets and curated/corrected
answers through `EvaluationKnowledge` before their model calls. The `knowledgeSemanticRetrieval` runtime setting is stored with the
answer-bank settings and exposed by their GET/PATCH API. It is checked for each
search; a configured route alone never opts the user in. The semantic stage
sends only the question and published snippet sections, retaining the existing
thresholds, model-specific cache and lexical result on provider failure.

Curated evidence requires two shared content terms and honors source-kind
filters. Corrections are approved manual new-entry candidates whose current
active bank entry still equals the approved answer. Raw telemetry and pending
learning proposals never become evidence. General retrieval excludes sensitive
identity/policy categories; the form matcher owns those answers. Company brief
summaries treat the page as untrusted evidence and use `research-search`.

## Plan: essay questions answered from experience records (2026-11-06)

### Objective and checks

Owner request 2026-11-06: application essays ("Describe a time you worked
directly with an external enterprise customer on an ML/AI problem", "a funnel
metric you owned, with the real numbers", "the hardest thing you have worked
on", "a vulnerability you found and how you validated the fix") stop the
fill-only runs because the knowledge base holds too little about the owner.
The owner wants the system to dig into all of their material so the agent can
answer such questions from it, and asks for the most efficient way to gather
that material and a knowledge base that is detailed and of high quality.

What the knowledge base is today (map of 2026-11-06, the sections above):

- 37 published snippets, imported legacy material: 25 behavioral stories
  (several about the Applysmith product itself, four with a test persona's id),
  6 experience packets of 10-40 KB, 4 project pages, 2 answer patterns. No
  draft, repository, mining session or follow-up answer was ever made.
- A story is free text in Situation/Task/Action/Result sections. Nothing
  records the context (internal tool or external customer, enterprise or
  consumer), the owner's own share, the scale, or a metric with its baseline,
  result, unit and period.
- The autofill composer sees at most 4 facts of 400 characters (one section per
  snippet); the apply ladder's composer sees 3 sections of 600 characters, has
  its own prompt and a weaker fabrication screen. A number further into a story
  never reaches either.
- 355 gap groups (1,777 asks) are mostly non-essay noise and never drive mining.

Success (each is a check, run before the plan is called done):

1. **Eval set.** The essay questions the real runs left for the owner (about a
   hundred candidate texts in `apply_runs` so far, to be labelled; some are
   short questions, not essays) are a frozen corpus under `internal/kb/testdata`,
   each labelled with the essay type it asks for, or "outside the owner's
   experience" (an Android feature, payroll tax, an enterprise sales deal). A
   test reports, per type, how many the confirmed records can answer.
2. **Coverage.** After the owner's capture sessions, at least 80% of the corpus
   questions labelled inside the owner's experience get a draft, and every
   question labelled outside it is declined with that reason, never answered.
3. **Grounding.** Every name and figure in a composed essay traces to a
   confirmed field of the record it cites (an automated check on the draft, not
   a prompt rule); an unconfirmed fact is never used.
4. **Owner effort.** The first capture takes the owner no more than about three
   hours in total; afterwards a new kind of essay costs one short interview
   (5-10 minutes) the system asks for on its own.

### Design

#### Experience records (what is stored)

A record is one piece of work the owner did, kept as a Markdown file under
`knowledge/records/` (the files stay the truth, as for every snippet), with
structured frontmatter the composer can rely on:

- `org`, `period` (start, end), `role`, `team` (size, who else), `ownership`
  (what was the owner's own part).
- `context`: internal tool, external customer, enterprise customer, consumer
  product, research, open source, course; `customer` (named or described, with
  whether it may be named).
- `domains` (ML/AI, infrastructure, security, growth, data, frontend, mobile,
  ...) and `stack`.
- `challenge` (what made it hard), `actions` (the owner's own steps),
  `results`, `learnings`, `hindsight` (what the owner would do differently).
- `metrics`: each `{name, before, after, unit, period, source}`, where `source`
  is the owner's statement, a document or a measurement.
- `links` (repository, document, demo) and `confidentiality` (which names and
  numbers may appear in an application).
- Every fact carries its origin and whether the owner confirmed it, as draft
  facts already do (`DraftFact`); only confirmed facts are published.

A story snippet is a view of a record, not a second copy: the record is the one
place a fact lives.

#### Capture (how the material is gathered, cheapest first)

1. **Bulk import of what already exists.** Résumé versions, a LinkedIn export,
   project and design documents, performance reviews, theses and papers, past
   essays and cover letters the owner wrote, repositories (the existing repo
   read). The model extracts record drafts with a citation per fact (the
   document and passage); every fact is pending until the owner confirms it on
   one card per record.
2. **Gap-driven interviews.** The system computes, per essay type, which record
   fields are missing (a "customer" essay needs a record with an external
   customer context and a result; a "metric owned" essay needs a metric with
   before and after). It asks one targeted question at a time, about one
   record, for the gaps the stuck questions actually hit. The owner answers in
   Chinese or English, typed or spoken, through the Ask Agent chat or Telegram;
   the answer is kept verbatim as the source and written into English facts
   the owner confirms. This replaces the fixed five-prompt mining script and
   the unused gap table as the driver of what to ask.
3. **Answers flow back.** Every essay the owner edits or approves is stored with
   the question and the record it drew on; a later question of the same type
   starts from it.

#### Composition (how an essay is written)

One composer for both the autofill path and the apply ladder:

1. Classify the question into an essay type (proudest work, hardest problem,
   biggest accomplishment, metric owned, external customer, cross-team
   partnership, security finding, remote work, why this company, product used,
   link to work) or "outside the record".
2. Choose one or two records by type, domain and the posting, and pass the
   whole confirmed record, not clipped sections.
3. Write the essay in the owner's voice (`voice.md`) to the requested length.
4. Check every sentence's names and figures against the cited record's
   confirmed fields; a sentence that fails is cut, and an essay that loses its
   point is declined with the missing field named, which becomes the next
   interview question.

"Why this company" essays draw on the company brief (`knowledge/companies/`)
and the owner's recorded motivations, never on invented enthusiasm.

#### What is reused, replaced and removed

- Reused: drafts, confirmation and publish; the repo read; the Ask Agent's
  approval card; the fact check; the story quality gates.
- Replaced: the five-prompt mining script (by field-driven questions), the two
  composers (by one), retrieval of 400-character clips (by whole records).
- Removed: legacy snippets once their facts are in records; test-persona
  material is moved out of the owner's knowledge base.

### Layers (each ends working and checked)

1. Eval set and coverage report over today's knowledge base (the baseline).
2. Record format, its validation and the record view on the Wiki page.
3. Import of documents into record drafts, with the confirmation card.
4. Gap-driven interview in the Ask Agent (then Telegram).
5. The single composer with the record-level fact check, wired into autofill
   and the ladder; the coverage report reruns on the eval set.
6. Answers flowing back; legacy snippets retired.

### Owner's answers (2026-11-06)

1. Records are built from the owner's real material; the test persona's
   résumé (`cv.md`, `resumes/jordan-avery.pdf`) and its four stories are never
   a source. The real material in the workspace today is
   `knowledge/behavioral`, `knowledge/experience` and `knowledge/projects`
   (the course and side projects, the research internship, the job-search
   agent, and the rest), less the persona's pages.
2. Employers, customers, projects and figures may all be named.
3. Capture is an agent that interviews the owner step by step, in the Ask
   Agent chat first; the material it cannot find, it asks for.

### The interview, step by step

The existing mining session (above) is the engine; what changes is what it
asks. A session gets a **target**: the stuck question, its company, and its
essay type. The essay type, not a fixed five-prompt script, decides the fields
to fill, and every reply is read for all of them at once.

Essay types and the fields each needs (the closed table lives in code beside
the classifier, so a new type is one row and one test):

| Type | Example (real stuck question) | Fields it needs |
|---|---|---|
| external customer | Databricks: "a time you worked directly with an external enterprise customer on an ML/AI problem" | context (who the customer was), problem, own actions, result |
| metric owned | Firecrawl: "a conversion, activation or funnel metric you personally owned ... the real numbers" | metric, before, after, unit, period, what was built or changed, own share |
| hardest / proudest / highest impact | Aven, Kinetic, Nomic | challenge, own actions, result, scale |
| partnership | Dave: "partnered closely with Marketing, Growth, CRM or Lifecycle" | stakeholders, goal translated, conflict, result |
| security finding | Apollo.io: "a vulnerability you found ... how you validated the fix" | class, discovery, validation, variant tested, own patch |
| remote work | Close: "your experience working remotely" | arrangement, period, practices |
| product used | ElevenLabs: "what did you build or explore" | what was built, how the product was used |
| AI in the work | Kinetic: "How did you use AI to ship this project?" | tools, where they helped, own judgement |
| outside the record | fal: payroll tax; Wonderschool: a small-business sale | none: the owner is asked once whether it applies; a No marks the type declined for every later run |

One turn:

1. **Open.** The agent names the stuck question and offers the two or three
   records most likely to answer it (ranked by type, domain and the posting),
   plus "another experience" and "I have none". "None" closes the session and
   marks the type outside the owner's record.
2. **Tell.** The owner answers freely, in Chinese or English, typed or pasted.
   The reply is kept verbatim as the source.
3. **Read.** The model reads the reply for every field of the type; a value is
   kept only with a passage of the reply it rests on (the notes rule above),
   and is written as an English fact. The agent echoes what it recorded in one
   short list, so a misreading is corrected at once.
4. **Ask one thing.** The next question is the most important missing field,
   asked concretely ("What was the activation rate before and after, over what
   period, and where was it measured?"). A field that needs a number and got
   none gets one follow-up ("A range or an estimate is fine; say 'no number' to
   skip"); the owner may skip any field. At most two follow-ups per field.
5. **Confirm.** When the type's fields are filled, the agent shows the record
   as one card of facts, each with its source passage; the owner approves it
   or edits a fact. Only confirmed facts are published.
6. **Write.** The composer drafts the stuck essay from the confirmed record and
   shows it; the owner approves or edits it, and the approved text is kept with
   the question and the record, so the next question of that type starts from
   it. The stuck run can then be filled again.

A record takes five to eight questions, about five to ten minutes. The agent
proposes the next session itself from the stuck questions, the most frequent
type first, so the owner's time goes where the runs stop.

### Layers, revised with the answers

1. Eval set: the stuck essay questions from `apply_runs`, labelled by type.
   Done as the table of `TestClassifyEssay_TheStuckQuestionsOfRealRuns` (the
   repository is at its file budget, so no new testdata file); the coverage
   report over today's knowledge base is still to come.
2. Essay types: the type table and a deterministic classifier, tested on the
   eval set. Done: `essayTypes`, `essayPrompts` and `ClassifyEssay` in
   `internal/kb/mining.go`; 41 of the 43 questions have a type, and the two
   without one (a reflection on how one's work shifted, payroll tax) stay
   untyped.
3. Targeted sessions: the target, the type-driven next question, the reading
   of each reply for every field, the follow-ups and the skip, in the session
   API, the CLI and the Ask Agent's mining tools. Done in
   `internal/kb/mining.go`:
   - `MiningStart.Target` takes the application question. Its type must be a
     story type, one with `Sections` (security finding, external customer,
     partnership, metric owned, model built, proud work); it sets the
     quadrant, and the fields asked are the type's. The question is kept as
     the transcript's first entry (role `target`), so no schema change.
     Motivation, links, AI use and the other one-part types are not stories
     and are refused.
   - "skip" (or "pass", "not sure", "no number", and the same in Chinese)
     leaves a part unanswered (source `skipped`). It is refused when it
     would leave a whole part of the story (Situation, Task, Action, Result)
     empty. "No" and "none" are answers, not skips, and so is a reply that
     only mentions not knowing. A quadrant session takes no skips.
   - A reply to a field that wants a figure (`Numbers`) without a digit is
     kept as `partial` and asked once: "Do you have the figure...". The
     answer to that follow-up is appended, and a skip keeps the first reply.
   - With the model configured, each reply is read for the other open
     fields; a passage copied word for word from it fills them (source
     `reply`, transcript role `quote`). A figure field takes a passage only
     with a number. Without the model, or when it fails, the questions are
     asked as usual.
   - Saving writes each story part from its section's fields; skipped fields
     write nothing and give no fact.
   - Entry points: `POST /api/kb/stories/mining` with `target`;
     `applysmith kb mine start|run --target "<question>"`; the Ask Agent's
     `story_mining` start with `target` (an instruction-shaped target is
     refused). The wiki's Stories page shows the question and the story's
     own parts.
4. Records: the structured frontmatter, its validation and the record card on
   the Wiki page; the existing real pages are imported as record drafts.
5. One composer over confirmed records with the record-level fact check, wired
   into autofill and the ladder; the coverage report reruns.
6. Approved essays flow back; legacy snippets retire once their facts live in
   records.
