package kb

import (
	"context"
	"database/sql"
	"fmt"
	"maps"
	"slices"
	"sort"
	"strings"
	"unicode"

	"applysmith/internal/lazyregexp"
)

// gapCap bounds the stored retrieval gaps; the oldest are dropped first.
const gapCap = 5000

// Search is the one retrieval service. The API, the CLI, the agent tools and
// the internal consumers all call it, so they see one ranking.
func (s *Service) Search(ctx context.Context, q Query) (Result, error) {
	q.Query = strings.TrimSpace(q.Query)
	if q.Query == "" {
		return Result{}, fmt.Errorf("%w: the query is empty", ErrInvalid)
	}
	if q.K <= 0 {
		q.K = defaultK
	}
	if q.K > maxK {
		q.K = maxK
	}
	rep, docs, err := s.snapshot(ctx)
	if err != nil {
		return Result{}, err
	}
	written, err := s.writtenHashes(ctx)
	if err != nil {
		return Result{}, fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	res := Result{Failed: rep.Failures}

	hits := s.lexical(q, docs, s.briefUsable(written, s.now()))
	clearing := false
	for _, h := range hits {
		clearing = clearing || h.clearing
	}
	if len(hits) > q.K {
		hits = hits[:q.K]
	}
	if s.opts.Embedder != nil && (s.opts.SemanticEnabled == nil || s.opts.SemanticEnabled(ctx)) && !clearing && questionShaped(q.Query) && len(hits) < q.K {
		sem, diag := s.semantic(ctx, q, docs, hits, q.K-len(hits))
		hits = append(hits, sem...)
		res.Diagnostics = append(res.Diagnostics, diag...)
	}

	var facts []Fact
	for i := range hits {
		h := hits[i]
		facts = append(facts, factFromSection(h.doc, h.sec, h.score, h.stage, h.reasons)...)
	}
	read := sourcesRead(rep)
	for _, src := range s.opts.Sources {
		fs, err := src.Search(ctx, q)
		if err != nil {
			res.Failed = append(res.Failed, SourceFailure{Source: src.Kind(), Error: err.Error()})
			continue
		}
		read++
		for i := range fs {
			if fs[i].Stage == "" {
				fs[i].Stage = StageLexical
			}
			if fs[i].Trust == "" {
				fs[i].Trust = trustOf(fs[i].SourceKind)
			}
			if len(fs[i].Provenance) == 0 {
				fs[i].Provenance = []string{fs[i].SourceKind + ":" + fs[i].SourceRef}
			}
		}
		facts = append(facts, fs...)
	}
	res.Facts = mergeFacts(facts)
	if len(res.Facts) > q.K {
		res.Facts = res.Facts[:q.K]
	}
	if res.Facts == nil {
		res.Facts = []Fact{}
	}
	switch {
	case len(res.Failed) > 0 && read == 0:
		res.Status = StatusSourcesUnavailable
	case len(res.Failed) > 0:
		res.Status = StatusPartial
	case len(res.Facts) > 0:
		res.Status = StatusGrounded
	default:
		res.Status = StatusNoMatch
		switch {
		case len(ScanSecrets(q.Query)) > 0:
			res.Diagnostics = append(res.Diagnostics, "the query looks like it holds a credential; no knowledge gap was recorded")
		default:
			if err := s.recordGap(ctx, q.Query); err != nil {
				res.Diagnostics = append(res.Diagnostics, "knowledge gap not recorded: "+err.Error())
			}
		}
	}
	return res, nil
}

// OwnSection is one section of the user's own documents: its ref
// ("knowledge/projects/a.md#Detail") and its heading and text, or, under the
// document's own path, what its frontmatter says of it.
type OwnSection struct{ Ref, Text string }

// OwnSections are the sections of the user's own documents, read whole: the
// knowledge snippets, the résumé and the narrative, never a company brief
// (F-10), each document's title, description, tags, triggers, dimensions,
// principles and other frontmatter values first. A source that could not be
// read, or one of the user's documents the index holds out (broken,
// oversized, holding a secret), leaves them incomplete: ErrUnavailable.
func (s *Service) OwnSections(ctx context.Context) ([]OwnSection, error) {
	rep, docs, err := s.snapshot(ctx)
	if err != nil {
		return nil, err
	}
	if len(rep.Failures) > 0 {
		return nil, fmt.Errorf("%w: the %s source could not be read", ErrUnavailable, rep.Failures[0].Source)
	}
	var held int
	if err := s.st.Read().QueryRowContext(ctx, `SELECT COUNT(*) FROM kb_docs WHERE state <> 'ok' AND kind IN (?, ?, ?)`,
		KindSnippet, KindCV, KindNarrative).Scan(&held); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	if held > 0 {
		return nil, fmt.Errorf("%w: %d of the user's documents are held out of the index", ErrUnavailable, held)
	}
	own := newKindsFilter([]string{KindSnippet, KindCV, KindNarrative})
	var out []OwnSection
	for _, d := range docs {
		if !own.allows(d) {
			continue
		}
		front := append([]string{d.Title, d.Description}, slices.Concat(d.Tags, d.Triggers, d.Dimensions, d.Principles)...)
		for _, k := range slices.Sorted(maps.Keys(d.Extra)) {
			front = append(front, d.Extra[k])
		}
		if t := strings.TrimSpace(strings.Join(slices.DeleteFunc(front, func(s string) bool { return strings.TrimSpace(s) == "" }), "\n")); t != "" {
			out = append(out, OwnSection{Ref: d.Path, Text: t})
		}
		for i := range d.secs {
			out = append(out, OwnSection{Ref: sectionRef(d, &d.secs[i]), Text: strings.TrimSpace(d.secs[i].Heading + "\n" + d.secs[i].Text)})
		}
	}
	return out, nil
}

// sourcesRead counts the sources that were read without failing, including the
// ones that held no matching document.
func sourcesRead(rep SyncReport) int {
	failed := failedSources(rep.Failures)
	n := 0
	for _, name := range rep.Read {
		if !failed[name] {
			n++
		}
	}
	return n
}

var properWordRE = lazyregexp.New(`[\p{L}\p{N}][\p{L}\p{N}'’-]*`)

// gapKey folds a question to its stemmed content terms. Capitalised words that
// do not start the sentence (employer and product names) are left out, so the
// same question asked about different employers shares a key.
func gapKey(question string) string {
	var kept []string
	for i, loc := range properWordRE.FindAllStringIndex(question, -1) {
		word := question[loc[0]:loc[1]]
		first := []rune(word)[0]
		if i > 0 && unicode.IsUpper(first) {
			continue
		}
		kept = append(kept, word)
	}
	terms := Terms(strings.Join(kept, " "))
	if len(terms) == 0 {
		terms = Terms(question)
	}
	return strings.Join(terms, " ")
}

func (s *Service) recordGap(ctx context.Context, question string) error {
	key := gapKey(question)
	now := s.stamp()
	return s.st.Write(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `INSERT INTO kb_gaps (question, terms_key, asked_at) VALUES (?, ?, ?)`, question, key, now); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `DELETE FROM kb_gaps WHERE id <= (SELECT max(id) FROM kb_gaps) - ?`, gapCap)
		return err
	})
}

// Gap is one row of the knowledge-gap list: questions folded by their terms.
type Gap struct {
	Terms    string `json:"terms"`
	Count    int    `json:"count"`
	Example  string `json:"example"`
	LastSeen string `json:"lastSeen"`
}

// Gaps lists the recorded gaps, most asked first.
func (s *Service) Gaps(ctx context.Context) ([]Gap, error) {
	rows, err := s.st.Read().QueryContext(ctx, `SELECT terms_key, count(*), max(asked_at),
		(SELECT question FROM kb_gaps g2 WHERE g2.terms_key = g.terms_key ORDER BY g2.id DESC LIMIT 1)
		FROM kb_gaps g GROUP BY terms_key`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Gap{}
	for rows.Next() {
		var g Gap
		if err := rows.Scan(&g.Terms, &g.Count, &g.LastSeen, &g.Example); err != nil {
			return nil, err
		}
		out = append(out, g)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Count != out[j].Count {
			return out[i].Count > out[j].Count
		}
		if out[i].LastSeen != out[j].LastSeen {
			return out[i].LastSeen > out[j].LastSeen
		}
		return out[i].Terms < out[j].Terms
	})
	return out, rows.Err()
}

// ClearGaps deletes every recorded gap.
func (s *Service) ClearGaps(ctx context.Context) error {
	return s.st.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `DELETE FROM kb_gaps`)
		return err
	})
}
