package kb

import (
	"cmp"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Entry is one row of the knowledge listing.
type Entry struct {
	ID          string    `json:"id"`
	Kind        string    `json:"kind"`
	Path        string    `json:"path"`
	Category    string    `json:"category"`
	Title       string    `json:"title"`
	Description string    `json:"description"`
	ModifiedAt  string    `json:"modifiedAt"`
	ContentHash string    `json:"contentHash"`
	FileHash    string    `json:"fileHash"`
	Lint        []Finding `json:"lint,omitempty"`
	Tags        []string  `json:"tags,omitempty"`
	Principles  []string  `json:"principles,omitempty"`
	SourceType  string    `json:"sourceType,omitempty"`
	// SourceURL is the page a company brief was built from.
	SourceURL string `json:"sourceUrl,omitempty"`
}

// Document is one snippet or brief with its Markdown.
type Document struct {
	Entry
	Markdown string `json:"markdown"`
	// Body is the Markdown without its frontmatter.
	Body        string    `json:"body"`
	Sections    []Section `json:"sections"`
	HasPrevious bool      `json:"hasPrevious"`
	// Links are the page's links to other pages, resolved to the page at the
	// path when there is one; LinkedFrom are the pages whose text links to it.
	Links      []PageLink `json:"links"`
	LinkedFrom []PageRef  `json:"linkedFrom"`
}

// PageRef names a page.
type PageRef struct {
	ID    string `json:"id"`
	Title string `json:"title"`
}

// PageLink is a link from one page to another. ID is empty when no page is at
// Path.
type PageLink struct {
	Label string `json:"label"`
	Path  string `json:"path"`
	ID    string `json:"id"`
}

// linkTarget is the workspace path a bundle-relative link such as
// "/projects/design.md" points at.
func linkTarget(link string) string { return "knowledge" + link }

// List returns every snippet and company brief with its lint findings.
func (s *Service) List(ctx context.Context) ([]Entry, error) {
	_, docs, err := s.snapshot(ctx)
	if err != nil {
		return nil, err
	}
	return entriesOf(docs), nil
}

// entriesOf is the listing of one loaded index.
func entriesOf(docs []*indexedDoc) []Entry {
	var snippets []*Snippet
	for _, d := range docs {
		if d.Kind == KindSnippet {
			sn := d.Snippet
			snippets = append(snippets, &sn)
		}
	}
	peers := lintPeers(snippets)
	out := []Entry{}
	for _, d := range docs {
		if d.Kind != KindSnippet && d.Kind != KindBrief {
			continue
		}
		e := entryOf(d)
		if d.Kind == KindSnippet {
			sn := d.Snippet
			e.Lint = lintSnippet(&sn, peers)
		}
		out = append(out, e)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out
}

// Find is the part of List whose title, description, tags, themes or text hold
// every word of q, each as the start of a word there, read by the search's own
// tokenizer: "kafk" finds "Kafka", "migrations" finds "migration", "cachi"
// finds "caching". A hyphenated word is held when all its parts are or when
// its parts written as one are ("open-source" finds "open source" and
// "opensource"). A query of stop words alone finds nothing. Nothing is recorded
// as a gap.
func (s *Service) Find(ctx context.Context, q string) ([]Entry, error) {
	_, docs, err := s.snapshot(ctx)
	if err != nil {
		return nil, err
	}
	hit := findHits(docs, []string{q})[0]
	return slices.DeleteFunc(entriesOf(docs), func(e Entry) bool { return !hit[e.Path] }), nil
}

// findHits applies Find's rule for each query to docs and returns, per query,
// the paths of the documents that hold it. Each document is read once.
func findHits(docs []*indexedDoc, qs []string) []map[string]bool {
	type alt struct{ term, raw string }
	wants := make([][][][]alt, len(qs)) // per query, per word: alternatives, each a list of parts that must all match
	for i, q := range qs {
		for _, w := range rawWords(q) {
			var parts []alt
			for _, p := range w.parts {
				if t, ok := term(p); ok {
					parts = append(parts, alt{t, p})
				}
			}
			alts := [][]alt{}
			if len(parts) > 0 {
				alts = append(alts, parts)
			}
			if t, ok := term(w.join); ok && w.join != "" {
				alts = append(alts, []alt{{t, w.join}})
			}
			if len(alts) > 0 {
				wants[i] = append(wants[i], alts)
			}
		}
	}
	hits := make([]map[string]bool, len(qs))
	for i := range hits {
		hits[i] = map[string]bool{}
	}
	for _, d := range docs {
		header := strings.Join(slices.Concat([]string{d.ID, d.Title, d.Description}, d.Tags, d.Principles), "\n")
		have, raw := Tokens(header), rawTokens(header)
		for _, sec := range d.secs {
			have = append(have, sec.Tokens...)
			raw = append(raw, rawTokens(sec.Heading+"\n"+sec.Text)...)
		}
		holds := func(a alt) bool {
			return slices.ContainsFunc(have, func(h string) bool { return strings.HasPrefix(h, a.term) }) ||
				slices.ContainsFunc(raw, func(h string) bool { return strings.HasPrefix(h, a.raw) })
		}
		for i, want := range wants {
			hits[i][d.Path] = len(want) > 0 && !slices.ContainsFunc(want, func(alts [][]alt) bool {
				return !slices.ContainsFunc(alts, func(parts []alt) bool { return !slices.ContainsFunc(parts, func(a alt) bool { return !holds(a) }) })
			})
		}
	}
	return hits
}

func entryOf(d *indexedDoc) Entry {
	id := d.ID
	if d.Kind == KindBrief {
		id = strings.TrimSuffix(filepath.Base(d.Path), ".md")
	}
	return Entry{ID: id, Kind: d.Kind, Path: d.Path, Category: d.Category, Title: d.Title, Description: d.Description,
		ModifiedAt: d.ModifiedAt, ContentHash: d.ContentHash, FileHash: d.FileHash,
		Tags: d.Tags, Principles: d.Principles, SourceType: d.StorySourceType, SourceURL: d.Extra["source_url"]}
}

// find locates a snippet by id, or else a brief by its domain.
func (s *Service) find(ctx context.Context, id string) (*indexedDoc, error) {
	_, docs, err := s.snapshot(ctx)
	if err != nil {
		return nil, err
	}
	return docByID(docs, id)
}

// docByID picks a snippet by id, or else a brief by its domain, from a loaded
// index.
func docByID(docs []*indexedDoc, id string) (*indexedDoc, error) {
	for _, kind := range []string{KindSnippet, KindBrief} {
		for _, d := range docs {
			if d.Kind == kind && entryOf(d).ID == id {
				return d, nil
			}
		}
	}
	return nil, fmt.Errorf("%w: no snippet or brief %q", ErrNotFound, id)
}

// Show returns one document with its Markdown, sections and lint findings,
// all from one read of the index.
func (s *Service) Show(ctx context.Context, id string) (Document, error) {
	_, docs, err := s.snapshot(ctx)
	if err != nil {
		return Document{}, err
	}
	return s.showIn(ctx, docs, nil, id)
}

// showIn is Show over a snapshot already loaded; list is entriesOf(docs) when
// the caller has it, else nil.
func (s *Service) showIn(ctx context.Context, docs []*indexedDoc, list []Entry, id string) (Document, error) {
	d, err := docByID(docs, id)
	if err != nil {
		return Document{}, err
	}
	data, err := readRegular(s.absPath(d.Path))
	if err != nil {
		return Document{}, fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	doc := Document{Entry: entryOf(d), Markdown: string(data), Sections: d.Sections}
	if _, body, _, err := splitFrontmatter(string(data)); err == nil {
		doc.Body = strings.TrimSpace(body)
	}
	doc.FileHash = fileHash(data)
	if d.Kind == KindSnippet {
		if list == nil {
			list = entriesOf(docs)
		}
		for _, e := range list {
			if e.Path == d.Path {
				doc.Lint = e.Lint
			}
		}
	}
	addLinks(&doc, d, docs)
	var n int
	if err := s.st.Read().QueryRowContext(ctx, `SELECT count(*) FROM kb_previous WHERE path = ?`, d.Path).Scan(&n); err != nil {
		return Document{}, err
	}
	doc.HasPrevious = n > 0
	return doc, nil
}

// addLinks fills the links of a snippet and the snippets that link to it. A
// brief has none.
func addLinks(doc *Document, d *indexedDoc, docs []*indexedDoc) {
	doc.Links, doc.LinkedFrom = []PageLink{}, []PageRef{}
	if d.Kind != KindSnippet {
		return
	}
	byPath := map[string]*indexedDoc{}
	for _, o := range docs {
		if o.Kind == KindSnippet {
			byPath[o.Path] = o
		}
	}
	for _, l := range d.Links {
		link := PageLink{Label: l.Label, Path: l.Path}
		if t := byPath[linkTarget(l.Path)]; t != nil {
			link.ID = t.ID
		}
		doc.Links = append(doc.Links, link)
	}
	for _, o := range docs {
		if o.Kind == KindSnippet && o.Path != d.Path && slices.ContainsFunc(o.Links, func(l Link) bool { return linkTarget(l.Path) == d.Path }) {
			doc.LinkedFrom = append(doc.LinkedFrom, PageRef{ID: o.ID, Title: o.Title})
		}
	}
}

// Previous is the text a document had before its last edit, split the way
// the editor shows it. SavedAt is when that edit replaced it.
type Previous struct {
	Markdown string
	SavedAt  string
	Title    string
	Tags     []string
	Body     string
}

// PreviousVersion returns what the snippet held before its last edit.
func (s *Service) PreviousVersion(ctx context.Context, id string) (Previous, error) {
	d, err := s.find(ctx, id)
	if err != nil {
		return Previous{}, err
	}
	var b []byte
	var p Previous
	err = s.st.Read().QueryRowContext(ctx, `SELECT content, saved_at FROM kb_previous WHERE path = ?`, d.Path).Scan(&b, &p.SavedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return Previous{}, fmt.Errorf("%w: %q has no previous version", ErrNotFound, id)
	}
	if err != nil {
		return Previous{}, err
	}
	p.Markdown = string(b)
	if sn, err := ParseSnippet(d.Path, b); err == nil {
		p.Title, p.Tags = sn.Title, sn.Tags
	}
	if _, body, _, err := splitFrontmatter(p.Markdown); err == nil {
		p.Body = strings.TrimSpace(body)
	}
	return p, nil
}

// Edit replaces the Markdown of a snippet or brief. The submitted text is
// parsed first (an unparseable body is refused and the file untouched), the id
// must not change, and expectedHash must be the file hash the editor was
// opened with (Document.FileHash, which also covers title and description); a
// stale hash is a conflict and nothing is written. The previous bytes stay recoverable until the next edit. A brief edited by hand becomes
// manual: it never expires and is never refetched over.
func (s *Service) Edit(ctx context.Context, id, markdown, expectedHash string) (Document, error) {
	if err := s.edit(ctx, id, markdown, expectedHash); err != nil {
		return Document{}, err
	}
	return s.Show(ctx, id)
}

func (s *Service) edit(ctx context.Context, id, markdown, expectedHash string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := s.syncLocked(ctx, false); err != nil {
		return err
	}
	d, err := s.findLocked(ctx, id)
	if err != nil {
		return err
	}
	if expectedHash == "" {
		return fmt.Errorf("%w: the file hash the editor was opened with is required", ErrInvalid)
	}
	if d.FileHash != expectedHash {
		return fmt.Errorf("%w: %s; reload it and apply the edit again", ErrStale, d.Path)
	}
	doc, err := parseMD(markdown)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrInvalid, err)
	}
	sn, err := ParseSnippet(d.Path, []byte(markdown))
	if err != nil {
		return fmt.Errorf("%w: %w", ErrInvalid, err)
	}
	if d.Kind == KindSnippet {
		if sn.ID != d.ID {
			return fmt.Errorf("%w: the id cannot change (%q to %q)", ErrConflict, d.ID, sn.ID)
		}
	} else {
		mapSet(doc.fm, "manual", boolNode(true))
	}
	data := doc.render()
	if d.Kind == KindSnippet {
		data = []byte(markdown)
	}
	if err := CheckSecrets(d.Path, string(data)); err != nil {
		return err
	}
	abs := s.absPath(d.Path)
	prev, err := readRegular(abs)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	now := s.stamp()
	if err := s.writeRecorded(ctx, abs, data, prev, true, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `INSERT INTO kb_previous (path, content, saved_at) VALUES (?, ?, ?)
			ON CONFLICT (path) DO UPDATE SET content = excluded.content, saved_at = excluded.saved_at`, d.Path, prev, now); err != nil {
			return err
		}
		if d.Kind == KindBrief {
			_, err := tx.ExecContext(ctx, `UPDATE kb_brief_state SET written_hash = '' WHERE domain = ?`, strings.TrimSuffix(filepath.Base(d.Path), ".md"))
			return err
		}
		return nil
	}); err != nil {
		return err
	}
	_, err = s.syncLocked(ctx, false)
	return err
}

// findLocked is find for a caller that already holds the lock and has synced.
func (s *Service) findLocked(ctx context.Context, id string) (*indexedDoc, error) {
	docs, err := s.loadDocs(ctx, nil)
	if err != nil {
		return nil, err
	}
	return docByID(docs, id)
}

// Delete removes a snippet or brief: the file, its previous version and every
// index record derived from it (sections, cached embeddings). It returns the
// token that undoes it within UndoWindow.
func (s *Service) Delete(ctx context.Context, id string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := s.syncLocked(ctx, false); err != nil {
		return "", err
	}
	d, err := s.findLocked(ctx, id)
	if err != nil {
		return "", err
	}
	file, err := readRegular(s.absPath(d.Path))
	if err != nil {
		return "", err
	}
	t := tombstone{kind: "snippet", name: id, path: d.Path, file: file}
	domain := strings.TrimSuffix(filepath.Base(d.Path), ".md")
	// The file goes as the last step of the transaction: a remove that fails
	// keeps the rows, and a commit that fails after it puts the file back.
	removed := false
	if err := s.st.Write(ctx, func(tx *sql.Tx) error {
		prev, err := copyRows(ctx, tx, "kb_previous", "path = ?", d.Path)
		if err != nil {
			return err
		}
		prev.replace = true
		t.rows = []rowSet{prev}
		if _, err := tx.ExecContext(ctx, `DELETE FROM kb_previous WHERE path = ?`, d.Path); err != nil {
			return err
		}
		if d.Kind == KindBrief {
			t.kind = "brief"
			state, err := copyRows(ctx, tx, "kb_brief_state", "domain = ?", domain)
			if err != nil {
				return err
			}
			state.replace = true
			t.rows = append(t.rows, state)
			if _, err := tx.ExecContext(ctx, `DELETE FROM kb_brief_state WHERE domain = ?`, domain); err != nil {
				return err
			}
		}
		if err := os.Remove(s.absPath(d.Path)); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		removed = true
		return nil
	}); err != nil {
		if removed {
			err = errors.Join(err, writeFileAtomic(s.absPath(d.Path), file))
		}
		return "", err
	}
	token := s.keep(t)
	_, err = s.syncLocked(ctx, false)
	return token, err
}

// UndoWindow is how long a knowledge delete can be taken back. What a delete
// removed is held in memory only: it is never written to disk and is gone when
// the window ends or the server stops.
const UndoWindow = 15 * time.Minute

// undoKeep bounds how many deletes are held at once; the oldest goes first.
const undoKeep = 20

// rowSet is a verbatim copy of the rows a delete removed from one table.
// replace marks rows that hang off a file whose absence restore checks first.
type rowSet struct {
	table   string
	cols    []string
	rows    [][]any
	replace bool
}

// copyRows reads the rows of table that match where, before they are deleted.
func copyRows(ctx context.Context, tx *sql.Tx, table, where string, args ...any) (rowSet, error) {
	rs, err := tx.QueryContext(ctx, `SELECT * FROM `+table+` WHERE `+where, args...)
	if err != nil {
		return rowSet{}, err
	}
	defer rs.Close()
	out := rowSet{table: table}
	if out.cols, err = rs.Columns(); err != nil {
		return out, err
	}
	for rs.Next() {
		row := make([]any, len(out.cols))
		ptrs := make([]any, len(row))
		for i := range row {
			ptrs[i] = &row[i]
		}
		if err := rs.Scan(ptrs...); err != nil {
			return out, err
		}
		out.rows = append(out.rows, row)
	}
	return out, rs.Err()
}

// put writes the rows back. A key taken again since the delete is a conflict,
// never an overwrite.
func (r rowSet) put(ctx context.Context, tx *sql.Tx) error {
	if len(r.rows) == 0 {
		return nil
	}
	q := map[bool]string{false: `INSERT`, true: `INSERT OR REPLACE`}[r.replace] + ` INTO ` + r.table + ` ("` + strings.Join(r.cols, `", "`) + `") VALUES (?` + strings.Repeat(", ?", len(r.cols)-1) + `)`
	for _, row := range r.rows {
		if _, err := tx.ExecContext(ctx, q, row...); err != nil {
			if strings.Contains(err.Error(), "constraint failed") {
				return ErrConflict
			}
			return err
		}
	}
	return nil
}

// tombstone is one delete, held for its undo. rows are in insert order,
// parents first; path and file are a deleted snippet's or brief's Markdown.
type tombstone struct {
	at         time.Time
	kind, name string
	rows       []rowSet
	path       string
	file       []byte
}

type trash struct {
	mu sync.Mutex
	m  map[string]tombstone
}

// keep holds t for UndoWindow and returns the token that restores it.
func (s *Service) keep(t tombstone) string {
	t.at = s.now()
	s.trash.mu.Lock()
	defer s.trash.mu.Unlock()
	if s.trash.m == nil {
		s.trash.m = map[string]tombstone{}
	}
	oldest := ""
	for k, v := range s.trash.m {
		if t.at.Sub(v.at) > UndoWindow {
			delete(s.trash.m, k)
		} else if oldest == "" || v.at.Before(s.trash.m[oldest].at) {
			oldest = k
		}
	}
	if len(s.trash.m) >= undoKeep {
		delete(s.trash.m, oldest)
	}
	token := randomID("undo")
	s.trash.m[token] = t
	return token
}

// Restored names what an undo brought back.
type Restored struct{ Kind, Name string }

// Restore puts back what the delete behind token removed. It is refused once
// the window has passed, and when something has taken the item's place since:
// nothing is overwritten. A snippet or brief is indexed again.
func (s *Service) Restore(ctx context.Context, token string) (Restored, error) {
	s.trash.mu.Lock()
	t, ok := s.trash.m[token]
	delete(s.trash.m, token)
	s.trash.mu.Unlock()
	if !ok || s.now().Sub(t.at) > UndoWindow {
		return Restored{}, fmt.Errorf("%w: this delete can no longer be undone; a delete is kept for %v and not across a restart", ErrNotFound, UndoWindow)
	}
	err := s.restore(ctx, t)
	if err != nil {
		s.trash.mu.Lock()
		s.trash.m[token] = t
		s.trash.mu.Unlock()
	}
	if errors.Is(err, ErrConflict) {
		return Restored{}, fmt.Errorf("%w: a %s named %s was made again since the delete, so the deleted one was not put back over it", ErrConflict, t.kind, t.name)
	}
	return Restored{Kind: t.kind, Name: t.name}, err
}

func (s *Service) restore(ctx context.Context, t tombstone) error {
	put := func(tx *sql.Tx) error {
		for _, r := range t.rows {
			if err := r.put(ctx, tx); err != nil {
				return err
			}
		}
		return nil
	}
	if t.path == "" {
		return s.st.Write(ctx, put)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := s.syncLocked(ctx, false); err != nil {
		return err
	}
	if _, err := s.findLocked(ctx, t.name); !errors.Is(err, ErrNotFound) {
		return cmp.Or(err, ErrConflict)
	}
	abs := s.absPath(t.path)
	if _, err := os.Lstat(abs); !errors.Is(err, os.ErrNotExist) {
		return cmp.Or(err, ErrConflict)
	}
	if err := s.st.Write(ctx, put); err != nil {
		return err
	}
	if err := writeFileAtomic(abs, t.file); err != nil {
		return err
	}
	_, err := s.syncLocked(ctx, false)
	return err
}

// ClearEmbeddings deletes every cached section vector.
func (s *Service) ClearEmbeddings(ctx context.Context) error {
	return s.st.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `DELETE FROM kb_embeddings`)
		return err
	})
}

// Export copies the Markdown of every published snippet and brief into dir,
// below their category or companies directory, and writes the drafts, the
// knowledge gaps, the repository sources, the mining sessions and the
// follow-up answers as JSON files beside them.
func (s *Service) Export(ctx context.Context, dir string) error {
	inv, err := s.Inventory(ctx)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, dirMode); err != nil {
		return err
	}
	for _, e := range inv.Documents {
		data, err := readRegular(s.absPath(e.Path))
		if err != nil {
			return err
		}
		dst := filepath.Join(dir, filepath.FromSlash(strings.TrimPrefix(e.Path, KnowledgeDir+"/")))
		if err := writeFileAtomic(dst, data); err != nil {
			return err
		}
	}
	for name, v := range map[string]any{"drafts.json": inv.Drafts, "gaps.json": inv.Gaps, "repos.json": inv.Repos,
		"sessions.json": inv.Sessions, "answers.json": inv.Answers} {
		b, err := json.MarshalIndent(v, "", "  ")
		if err != nil {
			return err
		}
		if err := writeFileAtomic(filepath.Join(dir, name), append(b, '\n')); err != nil {
			return err
		}
	}
	return nil
}

// Inventory is everything the knowledge base holds, for the listing: documents,
// drafts, repository sources, mining sessions, recorded gaps and the number of
// cached embeddings.
type Inventory struct {
	Documents  []Entry         `json:"documents"`
	Drafts     []Draft         `json:"drafts"`
	Repos      []Repo          `json:"repos"`
	Sessions   []MiningSession `json:"sessions"`
	Gaps       []Gap           `json:"gaps"`
	Embeddings int             `json:"embeddings"`
	Answers    []AnswerOfDraft `json:"answers"`
}

// AnswerOfDraft is a follow-up answer with the draft key it belongs to.
type AnswerOfDraft struct {
	DraftKey string `json:"draftKey"`
	Answer
}

// Inventory returns every kind of knowledge the base stores.
func (s *Service) Inventory(ctx context.Context) (Inventory, error) {
	var inv Inventory
	var err error
	if inv.Documents, err = s.List(ctx); err != nil {
		return inv, err
	}
	if inv.Drafts, err = s.ListDrafts(ctx, true); err != nil {
		return inv, err
	}
	if inv.Repos, err = s.ListRepos(ctx); err != nil {
		return inv, err
	}
	if inv.Sessions, err = s.ListMining(ctx); err != nil {
		return inv, err
	}
	if inv.Gaps, err = s.Gaps(ctx); err != nil {
		return inv, err
	}
	if err = s.st.Read().QueryRowContext(ctx, `SELECT count(*) FROM kb_embeddings`).Scan(&inv.Embeddings); err != nil {
		return inv, err
	}
	rows, err := s.st.Read().QueryContext(ctx, `SELECT draft_key, question_id, dimension, kind, text, updated_at FROM kb_story_answers ORDER BY draft_key, question_id`)
	if err != nil {
		return inv, err
	}
	defer rows.Close()
	inv.Answers = []AnswerOfDraft{}
	for rows.Next() {
		var a AnswerOfDraft
		if err := rows.Scan(&a.DraftKey, &a.QuestionID, &a.Dimension, &a.Kind, &a.Text, &a.UpdatedAt); err != nil {
			return inv, err
		}
		inv.Answers = append(inv.Answers, a)
	}
	return inv, rows.Err()
}

// Remove deletes one thing by reference: a snippet or brief id, or one of
// repo:<id>, session:<id>, draft:<id>, cache:gaps and cache:embeddings. A bare
// word is always a page id, so a page named "gaps" is deleted, not the list.
func (s *Service) Remove(ctx context.Context, ref string) error {
	kind, rest, typed := strings.Cut(ref, ":")
	if !typed {
		_, err := s.Delete(ctx, ref)
		return err
	}
	var err error
	switch kind {
	case "cache":
		switch rest {
		case "gaps":
			return s.ClearGaps(ctx)
		case "embeddings":
			return s.ClearEmbeddings(ctx)
		}
		return fmt.Errorf("%w: %q is not cache:gaps or cache:embeddings", ErrInvalid, ref)
	case "repo", "draft":
		n, err := strconv.ParseInt(rest, 10, 64)
		if err != nil || n < 1 {
			return fmt.Errorf("%w: %q is not a %s id", ErrInvalid, rest, kind)
		}
		if kind == "repo" {
			_, err = s.RemoveRepo(ctx, n)
		} else {
			_, err = s.DeleteDraft(ctx, n)
		}
		return err
	case "session":
		_, err = s.DeleteMining(ctx, rest)
		return err
	}
	// An id that happens to contain a colon is still a snippet id.
	_, err = s.Delete(ctx, ref)
	return err
}
