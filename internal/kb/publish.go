package kb

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
)

// GateError carries the findings that blocked a story publish.
type GateError struct{ Findings []StoryFinding }

func (e *GateError) Error() string {
	var msgs []string
	for _, f := range e.Findings {
		if f.Severity == SevBlock {
			msgs = append(msgs, f.Message)
		}
	}
	return "kb: the story cannot be published: " + strings.Join(msgs, "; ")
}
func (e *GateError) Is(target error) bool { return target == ErrConflict }

// Published reports what a publish wrote.
type Published struct {
	Path            string         `json:"path"`
	SnippetID       string         `json:"snippetId"`
	Lint            []Finding      `json:"lint,omitempty"`
	StoryFindings   []StoryFinding `json:"storyFindings,omitempty"`
	DroppedTriggers []string       `json:"droppedTriggers,omitempty"`
	NotPromoted     []string       `json:"notPromoted,omitempty"`
}

// PublishDraft writes a snippet draft into knowledge/. A story draft is refused
// here with a conflict that points to PublishStory; a superseded revision is
// refused with a conflict naming the newer one.
func (s *Service) PublishDraft(ctx context.Context, id int64) (Published, error) {
	d, err := s.publishable(ctx, id)
	if err != nil {
		return Published{}, err
	}
	if d.Kind == DraftStory {
		return Published{}, fmt.Errorf("%w: draft %d is a story; publish it with the story publish action, which runs the story gates", ErrConflict, id)
	}
	doc, err := parseMD(d.Markdown)
	if err != nil {
		return Published{}, fmt.Errorf("%w: %w", ErrInvalid, err)
	}
	var out Published
	if len(d.Facts) > 0 {
		sn, err := ParseSnippet("draft.md", []byte(d.Markdown))
		if err != nil {
			return Published{}, fmt.Errorf("%w: %w", ErrInvalid, err)
		}
		var facts []string
		facts, out.NotPromoted = promoteFacts(sn.Facts(), d)
		doc.setList("allowed_facts", facts, true)
	}
	return s.writePublished(ctx, d, doc, out)
}

// promoteFacts adds the draft's promotable statements to the facts a page has
// and names the ones it leaves out.
func promoteFacts(have []string, d Draft) (facts, notPromoted []string) {
	facts = append([]string{}, have...)
	for _, f := range d.Facts {
		if f.promotable() {
			facts = append(facts, f.Text)
		} else {
			notPromoted = append(notPromoted, f.Text)
		}
	}
	return distinct(facts), notPromoted
}

// publishable loads a draft and checks that it is the newest, unpublished
// revision.
func (s *Service) publishable(ctx context.Context, id int64) (Draft, error) {
	d, err := s.GetDraft(ctx, id)
	if err != nil {
		return d, err
	}
	if err := s.checkEditable(ctx, s.st.Read(), d); err != nil {
		return d, err
	}
	return d, nil
}

// PublishStory publishes a story draft after the story gates. A blocking
// finding refuses it with a *GateError and writes nothing. Trigger phrases the
// story cannot support are dropped and reported; inferred metrics the user did
// not edit are not promoted into allowed_facts and are named.
func (s *Service) PublishStory(ctx context.Context, id int64) (Published, error) {
	d, err := s.publishable(ctx, id)
	if err != nil {
		return Published{}, err
	}
	if d.Kind != DraftStory {
		return Published{}, fmt.Errorf("%w: draft %d is not a story", ErrInvalid, id)
	}
	doc, err := parseMD(d.Markdown)
	if err != nil {
		return Published{}, fmt.Errorf("%w: %w", ErrInvalid, err)
	}
	sn, err := ParseSnippet(KnowledgeDir+"/"+CatBehavioral+"/"+Slug(d.SnippetID)+".md", []byte(d.Markdown))
	if err != nil {
		return Published{}, fmt.Errorf("%w: %w", ErrInvalid, err)
	}
	sn.ID = d.SnippetID
	findings, err := s.storyReport(ctx, sn, d)
	if err != nil {
		return Published{}, err
	}

	comps, err := s.Competencies()
	if err != nil {
		return Published{}, err
	}
	triggers := append([]string{}, sn.Triggers...)
	for _, c := range comps {
		if contains(sn.Principles, c.ID) {
			triggers = append(triggers, c.QuestionPatterns...)
		}
	}
	evidence := sn.body + "\n" + strings.Join(draftFactTexts(d), "\n") + "\n" + strings.Join(sn.Facts(), "\n")
	kept, dropped := honestTriggers(distinct(triggers), evidence)
	if len(dropped) > 0 {
		findings = append(findings, StoryFinding{Code: FindUnsupportedTrigger, Severity: SevWarn,
			Message: "dropped trigger phrases the story does not support: " + strings.Join(dropped, "; ")})
	}
	if hasBlock(findings) {
		return Published{}, &GateError{Findings: findings}
	}

	facts, notPromoted := promoteFacts(sn.AllowedFacts, d)
	dims := append([]string{}, sn.Dimensions...)
	answers, err := s.answers(ctx, d.Key)
	if err != nil {
		return Published{}, err
	}
	for _, a := range answers {
		if a.Dimension != "" {
			dims = append(dims, a.Dimension)
		}
	}
	for _, f := range d.Facts {
		if f.Dimension != "" && f.promotable() {
			dims = append(dims, f.Dimension)
		}
	}
	doc.setString("id", d.SnippetID)
	doc.setList("triggers", kept, false)
	doc.setList("dimensions", sortedDistinct(dims), false)
	doc.setList("allowed_facts", facts, true)
	return s.writePublished(ctx, d, doc, Published{StoryFindings: findings, DroppedTriggers: dropped, NotPromoted: notPromoted})
}

func sortedDistinct(in []string) []string {
	out := distinct(in)
	sort.Strings(out)
	return out
}

func draftFactTexts(d Draft) []string {
	out := make([]string, 0, len(d.Facts))
	for _, f := range d.Facts {
		out = append(out, f.Text)
	}
	return out
}

// storyReport runs the structural gates and the quality checks on a story.
func (s *Service) storyReport(ctx context.Context, sn *Snippet, d Draft) ([]StoryFinding, error) {
	bank, err := s.publishedStories(ctx)
	if err != nil {
		return nil, err
	}
	facts := append(draftFactTexts(d), sn.Facts()...)
	out := gateFindings(sn, d.Origin)
	out = append(out, qualityFindings(sn, facts, bank)...)
	return out, nil
}

// writePublished writes the document to its place in knowledge/, keeps the
// bytes it replaced and marks the draft published, all or nothing. The lint and
// the secret scan run on the final text; lint only warns.
func (s *Service) writePublished(ctx context.Context, d Draft, doc *mdDoc, out Published) (Published, error) {
	cat := d.Category
	if cat == "" {
		cat = CatAnswerPatterns
	}
	slug := Slug(d.SnippetID)
	if slug == "" {
		return out, fmt.Errorf("%w: the snippet id %q has no usable characters", ErrInvalid, d.SnippetID)
	}
	rel := KnowledgeDir + "/" + cat + "/" + slug + ".md"
	doc.setString("id", d.SnippetID)
	data := doc.render()
	if len(data) > maxFileBytes {
		return out, fmt.Errorf("%w: %s would be larger than 1 MiB, which the index skips", ErrInvalid, rel)
	}
	if err := CheckSecrets(rel, string(data)); err != nil {
		return out, err
	}
	sn, err := ParseSnippet(rel, data)
	if err != nil {
		return out, fmt.Errorf("%w: %w", ErrInvalid, err)
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := s.syncLocked(ctx, false); err != nil {
		return out, err
	}
	if other, err := s.pathOfID(ctx, d.SnippetID); err != nil {
		return out, err
	} else if other != "" && other != rel {
		return out, fmt.Errorf("%w: snippet id %q is already used by %s", ErrConflict, d.SnippetID, other)
	}
	abs := s.absPath(rel)
	prev, rerr := readRegular(abs)
	if rerr != nil && !errors.Is(rerr, os.ErrNotExist) {
		return out, rerr
	}
	if rerr == nil {
		if existing, perr := ParseSnippet(rel, prev); perr == nil && existing.ID != d.SnippetID {
			return out, fmt.Errorf("%w: %s already holds snippet %q", ErrConflict, rel, existing.ID)
		}
	}
	if d.BaseHash != "" && (rerr != nil || fileHash(prev) != d.BaseHash) {
		return out, fmt.Errorf("%w: %s; the draft was made from an older version of the page, so discard it and read the source again", ErrStale, rel)
	}
	now := s.stamp()
	err = s.writeRecorded(ctx, abs, data, prev, rerr == nil, func(tx *sql.Tx) error {
		if rerr == nil {
			if _, err := tx.ExecContext(ctx, `INSERT INTO kb_previous (path, content, saved_at) VALUES (?, ?, ?)
				ON CONFLICT (path) DO UPDATE SET content = excluded.content, saved_at = excluded.saved_at`, rel, prev, now); err != nil {
				return err
			}
		}
		_, err := tx.ExecContext(ctx, `UPDATE kb_drafts SET status = 'published', published_path = ?, updated_at = ? WHERE id = ?`, rel, now, d.ID)
		return err
	})
	if err != nil {
		return out, err
	}
	if _, err := s.syncLocked(ctx, false); err != nil {
		return out, err
	}
	lint, err := s.lintFor(ctx, sn)
	if err != nil {
		return out, err
	}
	out.Path, out.SnippetID, out.Lint = rel, d.SnippetID, lint
	return out, nil
}

// pathOfID returns the path of the indexed snippet with the id, or "".
func (s *Service) pathOfID(ctx context.Context, id string) (string, error) {
	var p string
	err := s.st.Read().QueryRowContext(ctx, `SELECT path FROM kb_docs WHERE kind = 'snippet' AND id = ? AND state IN ('ok', 'duplicate') ORDER BY path LIMIT 1`, id).Scan(&p)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return p, err
}

// lintFor lints one snippet against the other published snippets.
func (s *Service) lintFor(ctx context.Context, sn *Snippet) ([]Finding, error) {
	docs, err := s.loadDocs(ctx, nil)
	if err != nil {
		return nil, err
	}
	var others []*Snippet
	for _, d := range docs {
		if d.Kind == KindSnippet && d.ID != sn.ID {
			o := d.Snippet
			others = append(others, &o)
		}
	}
	return lintSnippet(sn, lintPeers(others)), nil
}
