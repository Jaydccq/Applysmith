package kb

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path"
	"strings"

	"applysmith/internal/lazyregexp"
)

// Draft statuses.
const (
	StatusDraft      = "draft"
	StatusReady      = "ready"
	StatusPublished  = "published"
	StatusSuperseded = "superseded"
)

// Draft kinds and origins.
const (
	DraftSnippet = "snippet"
	DraftStory   = "story"

	OriginUser   = "user"
	OriginImport = "import"
	OriginRepo   = "repo"
	OriginMining = "mining"
)

// maxImportBytes bounds one imported file. The text it yields must still fit
// in maxFileBytes, the bound of a draft and of a knowledge file.
const maxImportBytes = 5 << 20

// Fact origins of a draft fact.
const (
	FactUser     = "user"
	FactCited    = "cited"
	FactMeasured = "measured"
	FactInferred = "inferred"
)

// DraftFact is a statement a draft rests on, with where it came from. An
// inferred fact is promoted into allowed_facts only after the user edited it.
type DraftFact struct {
	Text      string `json:"text"`
	Origin    string `json:"origin"`
	Metric    bool   `json:"metric,omitempty"`
	Edited    bool   `json:"edited,omitempty"`
	Dimension string `json:"dimension,omitempty"`
	// Pending marks a statement the user has not confirmed yet. It is kept
	// with the draft but not promoted into allowed_facts.
	Pending bool `json:"pending,omitempty"`
}

func (f DraftFact) promotable() bool {
	return !f.Pending && (f.Origin != FactInferred || !f.Metric || f.Edited)
}

// Draft is content a model or the agent produced, kept out of retrieval until
// the user publishes it.
type Draft struct {
	ID            int64       `json:"id"`
	Key           string      `json:"key"`
	Revision      int         `json:"revision"`
	Status        string      `json:"status"`
	Kind          string      `json:"kind"`
	Origin        string      `json:"origin"`
	Category      string      `json:"category"`
	SnippetID     string      `json:"snippetId"`
	Markdown      string      `json:"markdown"`
	Facts         []DraftFact `json:"facts"`
	SourceRun     string      `json:"sourceRun,omitempty"`
	RepoID        int64       `json:"repoId,omitempty"`
	PublishedPath string      `json:"publishedPath,omitempty"`
	// BaseHash is the file hash of the published page this draft replaces,
	// read when the draft was made; empty for a draft that creates a page.
	BaseHash  string `json:"baseHash,omitempty"`
	CreatedAt string `json:"createdAt"`
	UpdatedAt string `json:"updatedAt"`
}

// NewDraft is the input of CreateDraft.
type NewDraft struct {
	Kind      string
	Origin    string
	Category  string
	SnippetID string
	Markdown  string
	Facts     []DraftFact
	SourceRun string
	RepoID    int64
	BaseHash  string
}

// SupersededError is returned when a publish names a revision that a newer one
// replaced.
type SupersededError struct {
	Revision, Newer int
	NewerID         int64
}

func (e *SupersededError) Error() string {
	return fmt.Sprintf("kb: revision %d was superseded by revision %d (draft %d); publish that one", e.Revision, e.Newer, e.NewerID)
}
func (e *SupersededError) Is(target error) bool { return target == ErrConflict }

const draftColumns = `id, key, revision, status, kind, origin, category, snippet_id, markdown, facts_json, source_run, repo_id, published_path, created_at, updated_at, base_hash`

func scanDraft(row interface{ Scan(...any) error }) (Draft, error) {
	var d Draft
	var facts string
	err := row.Scan(&d.ID, &d.Key, &d.Revision, &d.Status, &d.Kind, &d.Origin, &d.Category, &d.SnippetID, &d.Markdown, &facts, &d.SourceRun, &d.RepoID, &d.PublishedPath, &d.CreatedAt, &d.UpdatedAt, &d.BaseHash)
	if err != nil {
		return d, err
	}
	if err := json.Unmarshal([]byte(facts), &d.Facts); err != nil {
		return d, err
	}
	if d.Facts == nil {
		d.Facts = []DraftFact{}
	}
	return d, nil
}

// GetDraft returns one draft revision.
func (s *Service) GetDraft(ctx context.Context, id int64) (Draft, error) {
	d, err := scanDraft(s.st.Read().QueryRowContext(ctx, `SELECT `+draftColumns+` FROM kb_drafts WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return d, fmt.Errorf("%w: draft %d", ErrNotFound, id)
	}
	return d, err
}

// ListDrafts returns the drafts, newest revision of each key first by default.
func (s *Service) ListDrafts(ctx context.Context, includeSuperseded bool) ([]Draft, error) {
	q := `SELECT ` + draftColumns + ` FROM kb_drafts`
	if !includeSuperseded {
		q += ` WHERE status <> 'superseded'`
	}
	rows, err := s.st.Read().QueryContext(ctx, q+` ORDER BY updated_at DESC, id DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Draft{}
	for rows.Next() {
		d, err := scanDraft(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

func (s *Service) newest(ctx context.Context, q interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, key string) (Draft, bool, error) {
	d, err := scanDraft(q.QueryRowContext(ctx, `SELECT `+draftColumns+` FROM kb_drafts WHERE key = ? ORDER BY revision DESC LIMIT 1`, key))
	if errors.Is(err, sql.ErrNoRows) {
		return d, false, nil
	}
	return d, err == nil, err
}

// validateDraft checks a draft's text before it is stored: it must parse and
// hold no secret. It returns the parsed snippet.
func validateDraft(nd *NewDraft) (*Snippet, error) {
	if strings.TrimSpace(nd.Markdown) == "" {
		return nil, fmt.Errorf("%w: the draft is empty", ErrInvalid)
	}
	if len(nd.Markdown) > maxFileBytes {
		return nil, fmt.Errorf("%w: the draft is larger than 1 MiB, the most a knowledge file the index reads may hold", ErrInvalid)
	}
	if nd.Category != "" && !contains(Categories, nd.Category) {
		return nil, fmt.Errorf("%w: category %q is not one of %s", ErrInvalid, nd.Category, strings.Join(Categories, ", "))
	}
	if err := CheckSecrets("draft", nd.Markdown); err != nil {
		return nil, err
	}
	for _, f := range nd.Facts {
		if err := CheckSecrets("draft fact", f.Text); err != nil {
			return nil, err
		}
	}
	sn, err := ParseSnippet("draft.md", []byte(nd.Markdown))
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalid, err)
	}
	return sn, nil
}

// CreateDraft stores a new draft. The key is a safe slug of the snippet id. A
// key whose newest revision is still unpublished is refused; refine it instead.
func (s *Service) CreateDraft(ctx context.Context, nd NewDraft) (Draft, error) {
	return s.createDraft(ctx, nd, false)
}

// preparedDraft is a validated draft ready to insert.
type preparedDraft struct {
	nd    NewDraft
	key   string
	facts string
}

func prepareDraft(nd NewDraft) (preparedDraft, error) {
	sn, err := validateDraft(&nd)
	if err != nil {
		return preparedDraft{}, err
	}
	if nd.Kind == "" {
		nd.Kind = DraftSnippet
	}
	if nd.Origin == "" {
		nd.Origin = OriginUser
	}
	if nd.Kind == DraftStory {
		nd.Category = CatBehavioral
	}
	if nd.SnippetID == "" {
		nd.SnippetID = sn.ID
	}
	if err := validSnippetID(nd.SnippetID); err != nil {
		return preparedDraft{}, err
	}
	facts, _ := json.Marshal(nonNilFacts(nd.Facts))
	return preparedDraft{nd: nd, key: Slug(nd.SnippetID), facts: string(facts)}, nil
}

func (s *Service) createDraft(ctx context.Context, nd NewDraft, replace bool) (Draft, error) {
	p, err := prepareDraft(nd)
	if err != nil {
		return Draft{}, err
	}
	var out Draft
	err = s.st.Write(ctx, func(tx *sql.Tx) error {
		out, err = s.insertDraft(ctx, tx, p, replace)
		return err
	})
	return out, err
}

// insertDraft adds the draft as the next revision of its key inside the
// caller's transaction.
func (s *Service) insertDraft(ctx context.Context, tx *sql.Tx, p preparedDraft, replace bool) (Draft, error) {
	nd, key := p.nd, p.key
	cur, ok, err := s.newest(ctx, tx, key)
	if err != nil {
		return Draft{}, err
	}
	rev := 1
	if ok {
		rev = cur.Revision + 1
		if cur.Status != StatusPublished && !replace {
			return Draft{}, fmt.Errorf("%w: draft %q already exists (revision %d); refine it", ErrConflict, key, cur.Revision)
		}
		if cur.Status != StatusPublished {
			if _, err := tx.ExecContext(ctx, `UPDATE kb_drafts SET status = 'superseded', updated_at = ? WHERE id = ?`, s.stamp(), cur.ID); err != nil {
				return Draft{}, err
			}
		}
	}
	now := s.stamp()
	res, err := tx.ExecContext(ctx, `INSERT INTO kb_drafts (key, revision, status, kind, origin, category, snippet_id, markdown, facts_json, source_run, repo_id, base_hash, created_at, updated_at)
		VALUES (?, ?, 'draft', ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		key, rev, nd.Kind, nd.Origin, nd.Category, nd.SnippetID, nd.Markdown, p.facts, nd.SourceRun, nd.RepoID, nd.BaseHash, now, now)
	if err != nil {
		return Draft{}, err
	}
	id, _ := res.LastInsertId()
	return scanDraft(tx.QueryRowContext(ctx, `SELECT `+draftColumns+` FROM kb_drafts WHERE id = ?`, id))
}

func nonNilFacts(f []DraftFact) []DraftFact {
	if f == nil {
		return []DraftFact{}
	}
	return f
}

// UpsertSourceDraft stores the one snippet draft a source run keeps. Building it
// again updates it in place; building it after it was published is refused
// while the published page exists, and drafts it again once the user deleted
// that page.
func (s *Service) UpsertSourceDraft(ctx context.Context, nd NewDraft) (Draft, error) {
	if nd.SourceRun == "" || nd.Kind == DraftStory {
		return Draft{}, fmt.Errorf("%w: a source draft needs a run and is not a story", ErrInvalid)
	}
	sn, err := validateDraft(&nd)
	if err != nil {
		return Draft{}, err
	}
	if nd.SnippetID == "" {
		nd.SnippetID = sn.ID
	}
	if err := validSnippetID(nd.SnippetID); err != nil {
		return Draft{}, err
	}
	nd.Origin = firstNonEmpty(nd.Origin, OriginRepo)
	facts, _ := json.Marshal(nonNilFacts(nd.Facts))
	var out Draft
	err = s.st.Write(ctx, func(tx *sql.Tx) error {
		var id int64
		var key, status, published string
		err := tx.QueryRowContext(ctx, `SELECT id, key, status, published_path FROM kb_drafts WHERE source_run = ? AND kind = 'snippet' AND status <> 'superseded'`, nd.SourceRun).Scan(&id, &key, &status, &published)
		switch {
		case errors.Is(err, sql.ErrNoRows):
			return errNoSourceDraft
		case err != nil:
			return err
		case status == StatusPublished && s.pageGone(published):
			// The published row steps aside, as the run keeps one live
			// draft; under the same key the new draft is its next revision.
			if _, err := tx.ExecContext(ctx, `UPDATE kb_drafts SET status = 'superseded', updated_at = ? WHERE id = ?`, s.stamp(), id); err != nil {
				return err
			}
			nd.Kind = DraftSnippet
			p, err := prepareDraft(nd)
			if err != nil {
				return err
			}
			out, err = s.insertDraft(ctx, tx, p, p.key == key)
			return err
		case status == StatusPublished:
			return fmt.Errorf("%w: %w: the draft of this source run", ErrConflict, errAlreadyPublished)
		}
		if _, err := tx.ExecContext(ctx, `UPDATE kb_drafts SET markdown = ?, facts_json = ?, category = ?, snippet_id = ?, updated_at = ? WHERE id = ?`,
			nd.Markdown, string(facts), nd.Category, nd.SnippetID, s.stamp(), id); err != nil {
			return err
		}
		out, err = scanDraft(tx.QueryRowContext(ctx, `SELECT `+draftColumns+` FROM kb_drafts WHERE id = ?`, id))
		return err
	})
	if errors.Is(err, errNoSourceDraft) {
		nd.Kind = DraftSnippet
		return s.CreateDraft(ctx, nd)
	}
	return out, err
}

// pageGone reports whether a published draft's page is no longer on disk,
// because the user deleted it.
func (s *Service) pageGone(publishedPath string) bool {
	if publishedPath == "" {
		return false
	}
	_, err := os.Lstat(s.absPath(publishedPath))
	return errors.Is(err, fs.ErrNotExist)
}

var (
	errNoSourceDraft    = errors.New("kb: no draft for the run yet")
	errAlreadyPublished = errors.New("already published")
)

func firstNonEmpty(vs ...string) string {
	for _, v := range vs {
		if v != "" {
			return v
		}
	}
	return ""
}

// RefineDraft stores new text as a new revision and marks the revision it
// replaces superseded.
func (s *Service) RefineDraft(ctx context.Context, id int64, markdown string, facts []DraftFact) (Draft, error) {
	return s.refineDraft(ctx, id, markdown, facts, nil, nil)
}

// refineDraft is RefineDraft; a non-nil baseHash replaces the hash the draft
// was made from, else the new revision keeps it. A non-nil also runs in the
// same transaction, after the new revision is written.
func (s *Service) refineDraft(ctx context.Context, id int64, markdown string, facts []DraftFact, baseHash *string, also func(tx *sql.Tx) error) (Draft, error) {
	var out Draft
	err := s.st.Write(ctx, func(tx *sql.Tx) error {
		cur, err := scanDraft(tx.QueryRowContext(ctx, `SELECT `+draftColumns+` FROM kb_drafts WHERE id = ?`, id))
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("%w: draft %d", ErrNotFound, id)
		}
		if err != nil {
			return err
		}
		if err := s.checkEditable(ctx, tx, cur); err != nil {
			return err
		}
		nd := NewDraft{Markdown: markdown, Facts: facts, Category: cur.Category}
		if _, err := validateDraft(&nd); err != nil {
			return err
		}
		fj, _ := json.Marshal(nonNilFacts(facts))
		now := s.stamp()
		if baseHash == nil {
			baseHash = &cur.BaseHash
		}
		if _, err := tx.ExecContext(ctx, `UPDATE kb_drafts SET status = 'superseded', updated_at = ? WHERE id = ?`, now, cur.ID); err != nil {
			return err
		}
		res, err := tx.ExecContext(ctx, `INSERT INTO kb_drafts (key, revision, status, kind, origin, category, snippet_id, markdown, facts_json, source_run, repo_id, base_hash, created_at, updated_at)
			VALUES (?, ?, 'draft', ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			cur.Key, cur.Revision+1, cur.Kind, cur.Origin, cur.Category, cur.SnippetID, markdown, string(fj), cur.SourceRun, cur.RepoID, *baseHash, now, now)
		if err != nil {
			return err
		}
		nid, _ := res.LastInsertId()
		if out, err = scanDraft(tx.QueryRowContext(ctx, `SELECT `+draftColumns+` FROM kb_drafts WHERE id = ?`, nid)); err != nil || also == nil {
			return err
		}
		return also(tx)
	})
	return out, err
}

// checkEditable refuses a change to a draft that is published or has a newer
// revision.
func (s *Service) checkEditable(ctx context.Context, q interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, d Draft) error {
	newest, _, err := s.newest(ctx, q, d.Key)
	if err != nil {
		return err
	}
	if newest.Revision > d.Revision {
		return &SupersededError{Revision: d.Revision, Newer: newest.Revision, NewerID: newest.ID}
	}
	if d.Status == StatusPublished {
		return fmt.Errorf("%w: draft %d is published; edit the snippet instead", ErrConflict, d.ID)
	}
	return nil
}

// EditDraft replaces the text of an unpublished draft in place, as the user does
// before publishing.
func (s *Service) EditDraft(ctx context.Context, id int64, markdown string) (Draft, error) {
	var out Draft
	err := s.st.Write(ctx, func(tx *sql.Tx) error {
		cur, err := scanDraft(tx.QueryRowContext(ctx, `SELECT `+draftColumns+` FROM kb_drafts WHERE id = ?`, id))
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("%w: draft %d", ErrNotFound, id)
		}
		if err != nil {
			return err
		}
		if err := s.checkEditable(ctx, tx, cur); err != nil {
			return err
		}
		nd := NewDraft{Markdown: markdown, Category: cur.Category}
		if _, err := validateDraft(&nd); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE kb_drafts SET markdown = ?, updated_at = ? WHERE id = ?`, markdown, s.stamp(), id); err != nil {
			return err
		}
		out, err = scanDraft(tx.QueryRowContext(ctx, `SELECT `+draftColumns+` FROM kb_drafts WHERE id = ?`, id))
		return err
	})
	return out, err
}

// SetDraftStatus moves an unpublished draft between draft and ready.
func (s *Service) SetDraftStatus(ctx context.Context, id int64, status string) (Draft, error) {
	if status != StatusDraft && status != StatusReady {
		return Draft{}, fmt.Errorf("%w: a draft can be set to draft or ready", ErrInvalid)
	}
	var out Draft
	err := s.st.Write(ctx, func(tx *sql.Tx) error {
		cur, err := scanDraft(tx.QueryRowContext(ctx, `SELECT `+draftColumns+` FROM kb_drafts WHERE id = ?`, id))
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("%w: draft %d", ErrNotFound, id)
		}
		if err != nil {
			return err
		}
		if err := s.checkEditable(ctx, tx, cur); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE kb_drafts SET status = ?, updated_at = ? WHERE id = ?`, status, s.stamp(), id); err != nil {
			return err
		}
		out, err = scanDraft(tx.QueryRowContext(ctx, `SELECT `+draftColumns+` FROM kb_drafts WHERE id = ?`, id))
		return err
	})
	return out, err
}

// DeleteDraft removes every revision of the draft's key and its recorded
// answers, and returns the token that undoes it within UndoWindow. A published
// snippet made from it stays.
func (s *Service) DeleteDraft(ctx context.Context, id int64) (string, error) {
	t := tombstone{kind: "draft"}
	err := s.st.Write(ctx, func(tx *sql.Tx) error {
		var key string
		err := tx.QueryRowContext(ctx, `SELECT key, snippet_id FROM kb_drafts WHERE id = ?`, id).Scan(&key, &t.name)
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("%w: draft %d", ErrNotFound, id)
		}
		if err != nil {
			return err
		}
		drafts, err := copyRows(ctx, tx, "kb_drafts", "key = ?", key)
		if err != nil {
			return err
		}
		answers, err := copyRows(ctx, tx, "kb_story_answers", "draft_key = ?", key)
		if err != nil {
			return err
		}
		t.rows = []rowSet{drafts, answers}
		if _, err := tx.ExecContext(ctx, `DELETE FROM kb_drafts WHERE key = ?`, key); err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `DELETE FROM kb_story_answers WHERE draft_key = ?`, key)
		return err
	})
	if err != nil {
		return "", err
	}
	return s.keep(t), nil
}

// ImportInput is a file the user hands to the knowledge base.
type ImportInput struct {
	Name     string
	Data     []byte
	Category string
	// Replace lets the import take an id that already exists.
	Replace bool
}

// ImportFile turns a Markdown, text or PDF file into a draft. It never creates
// a published snippet: the id is a safe slug of the file name and allowed_facts
// starts empty. A PDF needs Options.PDFText.
func (s *Service) ImportFile(ctx context.Context, in ImportInput) (Draft, error) {
	if len(in.Data) > maxImportBytes {
		return Draft{}, fmt.Errorf("%w: the file is larger than 5 MiB", ErrInvalid)
	}
	ext := strings.ToLower(extOf(in.Name))
	id := Slug(strings.TrimSuffix(baseOf(in.Name), extOf(in.Name)))
	if id == "" {
		return Draft{}, fmt.Errorf("%w: the file name %q has no usable characters", ErrInvalid, in.Name)
	}
	var text string
	switch ext {
	case ".md", ".markdown", ".txt":
		text = string(in.Data)
	case ".pdf":
		if s.opts.PDFText == nil {
			return Draft{}, fmt.Errorf("%w: PDF text extraction is not configured", ErrUnavailable)
		}
		t, err := s.opts.PDFText(ctx, in.Data)
		if err != nil {
			if errors.Is(err, exec.ErrNotFound) {
				return Draft{}, fmt.Errorf("%w: %w", ErrUnavailable, err)
			}
			return Draft{}, fmt.Errorf("%w: reading the PDF: %w", ErrInvalid, err)
		}
		text = t
	default:
		return Draft{}, fmt.Errorf("%w: only Markdown, text and PDF files can be imported", ErrInvalid)
	}
	if err := CheckSecrets(in.Name, text); err != nil {
		return Draft{}, err
	}
	body := strings.TrimSpace(text)
	if _, b, has, err := splitFrontmatter(text); err == nil && has {
		body = strings.TrimSpace(b)
	}
	if body == "" {
		return Draft{}, fmt.Errorf("%w: the file has no text", ErrInvalid)
	}
	taken, err := s.idTaken(ctx, id)
	if err != nil {
		return Draft{}, err
	}
	if taken && !in.Replace {
		return Draft{}, fmt.Errorf("%w: a snippet or draft with id %q already exists; import again with replace to take it", ErrConflict, id)
	}
	doc := &mdDoc{fm: newMapping(), body: body}
	doc.setString("id", id)
	doc.setString("title", titleOf(in.Name, body))
	doc.setList("allowed_facts", nil, true)
	return s.createDraft(ctx, NewDraft{Kind: DraftSnippet, Origin: OriginImport, Category: in.Category, SnippetID: id, Markdown: string(doc.render())}, in.Replace)
}

// idTaken reports whether a published snippet or an unpublished draft holds the
// id.
func (s *Service) idTaken(ctx context.Context, id string) (bool, error) {
	if _, err := s.Sync(ctx); err != nil {
		return false, err
	}
	var n int
	err := s.st.Read().QueryRowContext(ctx, `SELECT
		(SELECT count(*) FROM kb_docs WHERE kind = 'snippet' AND id = ?) +
		(SELECT count(*) FROM kb_drafts WHERE key = ? AND status IN ('draft', 'ready'))`, id, Slug(id)).Scan(&n)
	return n > 0, err
}

func baseOf(name string) string { return path.Base(strings.ReplaceAll(name, "\\", "/")) }
func extOf(name string) string  { return path.Ext(baseOf(name)) }

var firstHeadingRE = lazyregexp.New(`(?m)^#{1,6}[ \t]+(.+?)(?:[ \t]+#+)?[ \t]*$`)

// titleOf names an imported file: its first heading, else its file name.
func titleOf(name, body string) string {
	if m := firstHeadingRE.FindStringSubmatch(body); m != nil {
		return strings.TrimSpace(m[1])
	}
	stem := strings.TrimSuffix(baseOf(name), extOf(name))
	return strings.TrimSpace(strings.NewReplacer("-", " ", "_", " ").Replace(stem))
}

// validSnippetID is the rule for any id that becomes a file name: no path
// separators or control characters, not "." or "..", bounded in length, and at
// least one letter or digit so its slug is not empty.
func validSnippetID(id string) error {
	bad := func(why string) error { return fmt.Errorf("%w: snippet id %q %s", ErrInvalid, id, why) }
	switch {
	case strings.TrimSpace(id) == "" || Slug(id) == "":
		return bad("has no usable characters")
	case len(id) > 120:
		return bad("is longer than 120 bytes")
	case id == "." || id == "..":
		return bad("is not a name")
	case strings.ContainsAny(id, "/\\"):
		return bad("holds a path separator")
	}
	for _, r := range id {
		if r < 0x20 || r == 0x7f {
			return bad("holds a control character")
		}
	}
	return nil
}
