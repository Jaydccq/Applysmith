package learn

import (
	"context"
	"database/sql"
	"encoding/json"
	"slices"
	"strings"
	"time"

	"applysmith/internal/answerbank/match"
)

// DefaultWindowDays is how far back the evidence of a candidate may reach.
const DefaultWindowDays = 30

// GenerateOptions tunes one generation run.
type GenerateOptions struct {
	// SinceDays overrides the evidence window; zero means DefaultWindowDays.
	SinceDays int
	// DryRun computes the result and writes nothing.
	DryRun bool
}

// GenerateResult reports a run. Candidates are the proposals the run created,
// refreshed or brought back.
type GenerateResult struct {
	Created    int
	Refreshed  int
	Reproposed int
	// Corrections counts the manual fills that disagreed with the entry the
	// matcher routes the question to and were recorded as corrections on it.
	Corrections int
	Candidates  []Candidate
}

// row is one supporting event or fill.
type row struct {
	ref      evidenceRef
	at       time.Time
	host     string
	question string
	kind     string
	options  []string
	answer   string
	value    string
	label    string
	entryID  string
	posting  string
}

func (r row) pickShaped() bool {
	return len(r.options) > 0 || r.label != "" || r.kind == string(match.KindSelect) || r.kind == string(match.KindRadioGroup) || r.kind == string(match.KindCustomListbox)
}

type proposal struct {
	Candidate
	rows   []row
	seeded bool
}

// conflict holds the manual fills that disagree with the entry the matcher
// routes their question to; each is a correction on that entry.
type conflict struct {
	entry string
	rows  []row
}

type plan struct {
	proposals   []proposal
	corrections []conflict
}

// Generate turns the evidence of the last window into candidates. It only
// creates and refreshes pending candidates and brings a rejected one back when
// its evidence doubled; it never approves one and never deletes one.
func (s *Service) Generate(ctx context.Context, o GenerateOptions) (GenerateResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	days := o.SinceDays
	if days <= 0 {
		days = DefaultWindowDays
	}
	loaded, err := s.bank.Bank()
	if err != nil {
		return GenerateResult{}, err
	}
	since := stamp(s.now().AddDate(0, 0, -days))
	existing, err := s.existingByKey(ctx)
	if err != nil {
		return GenerateResult{}, err
	}
	pl, err := s.plan(ctx, since, loaded.Bank)
	if err != nil {
		return GenerateResult{}, err
	}
	var res GenerateResult
	now := s.now()
	apply := func(tx *sql.Tx) error {
		for _, p := range pl.proposals {
			ex, found := existing[p.Key]
			switch {
			case !found:
				p.CreatedAt, p.UpdatedAt = now, now
				if p.Type == TypeNewEntry {
					id, err := s.mintEntryID(p.SeedQuestion)
					if err != nil {
						return err
					}
					p.EntryID = id
				}
				if !o.DryRun {
					id, err := insertCandidate(ctx, tx, p.Candidate, refsOf(p.rows))
					if err != nil {
						return err
					}
					p.ID = id
				}
				p.Support = len(p.rows)
				res.Created++
			case ex.State == StateApproved:
				continue
			case ex.State == StateRejected:
				if p.Type == TypeExtendExamples || len(p.rows) < 2*ex.Support {
					continue
				}
				p = mergeInto(ex, p, now, true)
				p.Support = len(p.rows)
				if !o.DryRun {
					if err := reproposeRejected(ctx, tx, p.Candidate, refsOf(p.rows)); err != nil {
						return err
					}
				}
				res.Reproposed++
			default:
				// An unseeded cluster never touches a candidate that already
				// holds an answer. A cluster of another source replaces the
				// evidence; one of the same source adds to it.
				if !p.seeded && (ex.ProposedAnswer != "" || ex.LongForm) {
					continue
				}
				replace := p.Source != ex.Source
				p = mergeInto(ex, p, now, replace)
				if !o.DryRun {
					if err := updatePending(ctx, tx, p.Candidate, refsOf(p.rows), replace); err != nil {
						return err
					}
				}
				res.Refreshed++
			}
			res.Candidates = append(res.Candidates, p.Candidate)
		}
		for _, c := range pl.corrections {
			for _, r := range c.rows {
				if o.DryRun {
					// A fill that already has its event is one insertEvent
					// would ignore.
					recorded, err := s.fillRecorded(ctx, r.ref.id)
					if err != nil {
						return err
					}
					if !recorded {
						res.Corrections++
					}
					continue
				}
				e := eventRow{Kind: KindUserCorrected, At: r.at, Host: r.host, Question: r.question, ControlKind: r.kind, Options: r.options,
					EntryID: c.entry, Origin: OriginManualFill, Counted: true, Lane: LaneManual, FillID: ptr(r.ref.id)}
				e.Value, e.OptionLabel = nonEmpty(r.value), nonEmpty(r.label)
				inserted, err := insertEvent(ctx, tx, e)
				if err != nil {
					return err
				}
				if inserted {
					res.Corrections++
				}
			}
		}
		return nil
	}
	if o.DryRun {
		return res, apply(nil)
	}
	if err := s.st.Write(ctx, apply); err != nil {
		return GenerateResult{}, err
	}
	if s.generated != nil && res.Created+res.Reproposed+res.Refreshed > 0 {
		s.generated()
	}
	return res, nil
}

// fillRecorded reports a manual fill that already has its event.
func (s *Service) fillRecorded(ctx context.Context, fill int64) (bool, error) {
	var n int
	err := s.st.Read().QueryRowContext(ctx, `SELECT count(*) FROM answer_events WHERE fill_id = ?`, fill).Scan(&n)
	return n > 0, err
}

func refsOf(rows []row) []evidenceRef {
	out := make([]evidenceRef, len(rows))
	for i, r := range rows {
		out[i] = r.ref
	}
	return out
}

// mergeInto folds a fresh proposal into the stored candidate: the stored id,
// entry id and creation time stay, and the question and host lists only grow.
// The evidence window grows with the evidence a pending candidate keeps; a
// returning rejected candidate, or one whose evidence is replaced, starts a new
// window. A pending candidate keeps its review; a returning one is reviewed
// again.
func mergeInto(ex Candidate, p proposal, now time.Time, replace bool) proposal {
	out := p
	out.ID, out.EntryID, out.CreatedAt, out.UpdatedAt = ex.ID, ex.EntryID, ex.CreatedAt, now
	if ex.State == StatePending {
		out.Review, out.ReviewedAt = ex.Review, ex.ReviewedAt
	}
	out.Questions, out.Hosts = union(ex.Questions, p.Questions), union(ex.Hosts, p.Hosts)
	out.SeedQuestion = longest(out.Questions)
	if ex.State == StatePending && !replace {
		if ex.FirstSeenAt.Before(out.FirstSeenAt) {
			out.FirstSeenAt = ex.FirstSeenAt
		}
		if ex.LastSeenAt.After(out.LastSeenAt) {
			out.LastSeenAt = ex.LastSeenAt
		}
	}
	return out
}

func (s *Service) existingByKey(ctx context.Context) (map[string]Candidate, error) {
	all, err := s.stored(ctx, "")
	if err != nil {
		return nil, err
	}
	out := make(map[string]Candidate, len(all))
	for _, c := range all {
		out[c.Key] = c
	}
	return out, nil
}

// curatedQuestions are the normalized descriptions and examples of every entry
// that is not a placeholder: a question that equals one is already answered by
// something the user curated.
func curatedQuestions(bank match.Bank) map[string]bool {
	out := map[string]bool{}
	for _, e := range bank.Entries {
		if e.Placeholder {
			continue
		}
		out[Normalize(e.Description)] = true
		for _, ex := range e.Examples {
			out[Normalize(ex)] = true
		}
	}
	return out
}

// loadRows reads the evidence of one source from the window.
func (s *Service) loadRows(ctx context.Context, q string, since string, fill bool) ([]row, error) {
	rs, err := s.st.Read().QueryContext(ctx, q, since)
	if err != nil {
		return nil, err
	}
	defer rs.Close()
	var out []row
	for rs.Next() {
		var r row
		var at, opts string
		var value, label sql.NullString
		if err := rs.Scan(&r.ref.id, &at, &r.host, &r.question, &r.kind, &opts, &value, &label, &r.entryID, &r.posting); err != nil {
			return nil, err
		}
		r.ref.fill = fill
		r.at = parseStamp(at)
		_ = json.Unmarshal([]byte(opts), &r.options)
		r.label, r.value = strings.TrimSpace(label.String), strings.TrimSpace(value.String)
		r.answer = r.label
		if r.answer == "" {
			r.answer = r.value
		}
		out = append(out, r)
	}
	return out, rs.Err()
}

// postingSQL names the posting a row was seen on: the application run's posting
// identity, else its report, else only its host. A retried posting mints a new
// run each time, so the run id alone never counts as a posting.
func postingSQL(table string) string {
	return `COALESCE(NULLIF((SELECT a.identity_key FROM apply_runs a WHERE a.run_id = ` + table + `.run_id AND ` + table + `.run_id <> ''), ''),
		'report:' || ` + table + `.report_number, 'host:' || ` + table + `.host)`
}

var (
	abstainSQL = `SELECT id, at, host, question, control_kind, options_json, value, option_label, entry_id, ` + postingSQL("answer_events") + ` FROM answer_events
		WHERE kind = 'abstained' AND reason = 'no-entry' AND withheld = '' AND at >= ? ORDER BY at, id`
	composedSQL = `SELECT id, at, host, question, control_kind, options_json, value, option_label, entry_id, ` + postingSQL("answer_events") + ` FROM answer_events
		WHERE kind = 'match' AND lane = 'composed' AND entry_id = '' AND withheld = '' AND at >= ? ORDER BY at, id`
	modelFillSQL = `SELECT id, at, host, question, control_kind, options_json, value, option_label, entry_id, ` + postingSQL("answer_events") + ` FROM answer_events
		WHERE kind = 'match' AND lane IN ('model-option', 'composed') AND entry_id <> '' AND withheld = '' AND at >= ? ORDER BY at, id`
	// A manual fill that names the entry whose value it replaced is a
	// correction of that entry (insertFill), never a new answer.
	manualSQL = `SELECT id, at, host, question, control_kind, options_json, value, option_label, entry_id, ` + postingSQL("manual_fills") + ` FROM manual_fills
		WHERE ground_truth = 1 AND withheld = '' AND entry_id = '' AND at >= ? ORDER BY at, id`
)

// plan builds the proposals of the window: one per question key and source,
// collapsed to one per key, then the extend-examples proposals.
func (s *Service) plan(ctx context.Context, since string, bank match.Bank) (plan, error) {
	var pl plan
	curated := curatedQuestions(bank)
	byKey := map[string]proposal{}
	order := []string{}
	add := func(p proposal) {
		cur, ok := byKey[p.Key]
		if !ok {
			byKey[p.Key] = p
			order = append(order, p.Key)
			return
		}
		// A seeded proposal beats an unseeded one; among seeded ones the larger
		// support wins and the earlier source keeps a tie.
		if p.seeded && (!cur.seeded || len(p.rows) > len(cur.rows)) {
			byKey[p.Key] = p
		}
	}
	for _, src := range []struct {
		source, sql string
		fill        bool
	}{{SourceAbstain, abstainSQL, false}, {SourceComposed, composedSQL, false}, {SourceManual, manualSQL, true}} {
		rows, err := s.loadRows(ctx, src.sql, since, src.fill)
		if err != nil {
			return pl, err
		}
		for _, c := range clusters(rows) {
			p, ok, cf := s.newEntryProposal(src.source, c, curated, bank)
			if len(cf.rows) > 0 && src.source == SourceManual {
				pl.corrections = append(pl.corrections, cf)
			}
			if ok {
				add(p)
			}
		}
	}
	for _, k := range order {
		pl.proposals = append(pl.proposals, byKey[k])
	}
	extend, err := s.extendProposals(ctx, since, bank)
	if err != nil {
		return pl, err
	}
	pl.proposals = append(pl.proposals, extend...)
	return pl, nil
}

type cluster struct {
	norm string
	rows []row
}

// clusters groups rows by normalized question, keeping the order of first
// appearance.
func clusters(rows []row) []cluster {
	var out []cluster
	at := map[string]int{}
	for _, r := range rows {
		n := Normalize(r.question)
		if n == "" {
			continue
		}
		i, ok := at[n]
		if !ok {
			i = len(out)
			at[n] = i
			out = append(out, cluster{norm: n})
		}
		out[i].rows = append(out[i].rows, r)
	}
	return out
}

// learnableQuestion applies the question-level exclusions: not a question, a
// sensitive or disqualifying disclosure, a question the model writes on every
// application, a value-withheld control, a trap.
func learnableQuestion(r row) bool {
	q := r.question
	if IsNonQuestion(q) || match.IsSensitiveCategoryQuestion(q) || match.IsDisqualifyingFactDisclosure(q) || match.ModelWritten(q, r.pickShaped(), false) {
		return false
	}
	if Classify(q, r.kind, "", "", r.options) != WithheldNone {
		return false
	}
	_, trap := match.TrapAbstain(match.Question{Text: q, Kind: match.ControlKind(r.kind)})
	return !trap
}

// newEntryProposal builds the new-entry proposal of one cluster, or reports why
// there is none. conflicts are the rows that disagree with the entry the
// matcher routes the question to.
func (s *Service) newEntryProposal(source string, c cluster, curated map[string]bool, bank match.Bank) (proposal, bool, conflict) {
	if curated[c.norm] {
		return proposal{}, false, conflict{}
	}
	seeded := source != SourceAbstain
	var rows []row
	for _, r := range c.rows {
		if !learnableQuestion(r) {
			continue
		}
		if seeded && (r.answer == "" || IsNothingChosen(r.answer) || Normalize(r.answer) == "" || match.IsAskMe(r.answer)) {
			continue
		}
		rows = append(rows, r)
	}
	if postings(rows) < MinPostingSupport {
		return proposal{}, false, conflict{}
	}
	if seeded {
		for _, r := range rows[1:] {
			if Normalize(r.answer) != Normalize(rows[0].answer) {
				return proposal{}, false, conflict{}
			}
		}
		// A disagreement with the bank is checked before the value is judged
		// substantive, so a short answer such as "2" against an entry that says
		// "3" still reaches the entry as a correction.
		last := rows[len(rows)-1]
		if r := s.routeBank(bank, match.Facts{}, last.host, match.Posting{}, routeQuestion(last)); r.Fills() {
			if Normalize(r.Answer) == Normalize(rows[0].answer) {
				return proposal{}, false, conflict{}
			}
			return proposal{}, false, conflict{entry: r.EntryID, rows: rows}
		}
		rows = slices.DeleteFunc(rows, func(r row) bool { return !IsSubstantiveValue(r.question, r.answer) })
		if postings(rows) < MinPostingSupport {
			return proposal{}, false, conflict{}
		}
	}
	p := proposal{rows: rows, seeded: seeded}
	p.Key, p.Type, p.Source, p.State = NewEntryKey(rows[0].question), TypeNewEntry, source, StatePending
	fill(&p, rows)
	if seeded {
		answer := strings.TrimSpace(rows[0].answer)
		if IsLongFormProse(answer) {
			p.LongForm = true
			p.Flags = append(p.Flags, "REVIEW: the answer is long-form prose and is not proposed")
		} else {
			p.ProposedAnswer = answer
			p.Flags = append(p.Flags, ReviewFlags(answer)...)
		}
	}
	return p, true, conflict{}
}

// postings counts the distinct postings the rows were seen on.
func postings(rows []row) int {
	seen := map[string]bool{}
	for _, r := range rows {
		seen[r.posting] = true
	}
	return len(seen)
}

// fill sets the fields every proposal derives from its rows.
func fill(p *proposal, rows []row) {
	for _, r := range rows {
		p.Questions = union(p.Questions, []string{r.question})
		if r.host != "" {
			p.Hosts = union(p.Hosts, []string{r.host})
		}
	}
	p.SeedQuestion = longest(p.Questions)
	p.FirstSeenAt, p.LastSeenAt = rows[0].at, rows[0].at
	for _, r := range rows {
		if r.at.Before(p.FirstSeenAt) {
			p.FirstSeenAt = r.at
		}
		if r.at.After(p.LastSeenAt) {
			p.LastSeenAt = r.at
		}
	}
	p.Support = len(rows)
	p.ProposedKind = "fixed"
	if slices.ContainsFunc(rows, row.pickShaped) {
		p.ProposedKind = "pick-option"
	}
}

func routeQuestion(r row) match.Question {
	q := match.Question{Selector: "q", Text: r.question, Kind: match.ControlKind(r.kind), SectionPath: nil}
	if !q.Kind.Valid() {
		q.Kind = match.KindText
	}
	for _, o := range r.options {
		q.Options = append(q.Options, match.Option{Value: o, Label: o})
	}
	return q
}

// extendProposals builds the extend-examples proposals: one per entry and
// phrasing a model filled for a question the matcher had routed to that entry.
func (s *Service) extendProposals(ctx context.Context, since string, bank match.Bank) ([]proposal, error) {
	rows, err := s.loadRows(ctx, modelFillSQL, since, false)
	if err != nil {
		return nil, err
	}
	owner := map[string]string{}
	for _, e := range bank.Entries {
		for _, ex := range append([]string{e.Description}, e.Examples...) {
			if n := Normalize(ex); n != "" {
				if _, taken := owner[n]; !taken {
					owner[n] = e.ID
				}
			}
		}
	}
	type group struct {
		entry, norm string
		rows        []row
	}
	var groups []*group
	at := map[string]*group{}
	for _, r := range rows {
		n := Normalize(r.question)
		if n == "" || !learnableQuestion(r) {
			continue
		}
		target, ok := bank.Find(r.entryID)
		if !ok || target.Pinned {
			continue
		}
		if _, taken := owner[n]; taken {
			// Already an example or description of this entry, or a phrasing that
			// belongs to another one.
			continue
		}
		k := r.entryID + "\x00" + n
		g := at[k]
		if g == nil {
			g = &group{entry: r.entryID, norm: n}
			at[k] = g
			groups = append(groups, g)
		}
		g.rows = append(g.rows, r)
	}
	var out []proposal
	for _, g := range groups {
		p := proposal{rows: g.rows}
		p.Key, p.Type, p.Source, p.State, p.TargetEntryID = ExtendExamplesKey(g.entry, g.rows[0].question), TypeExtendExamples, SourceModelFill, StatePending, g.entry
		fill(&p, g.rows)
		p.ProposedKind = ""
		out = append(out, p)
	}
	return out, nil
}
