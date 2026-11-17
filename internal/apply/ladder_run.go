package apply

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"applysmith/internal/answerbank/assist"
	"applysmith/internal/answerbank/learn"
	"applysmith/internal/answerbank/match"
	"applysmith/internal/engine"
	"applysmith/internal/gates"
	"applysmith/internal/lazyregexp"
)

var (
	explainBoxRE = lazyregexp.New(`(?i)\b(?:if\s+yes|please\s+(?:explain|describe|provide|specify)|provide\s+details|explain)\b`)
	// otherChoiceRE is a gate answered with its "Other" choice, otherWordRE a
	// box that names it, and statusGateRE a citizenship or immigration status
	// question ("Citizenship Status": "Other / Eligible for required
	// authorization").
	otherChoiceRE = lazyregexp.New(`(?i)^\W*other\b`)
	otherWordRE   = lazyregexp.New(`(?i)\bother\b`)
	statusGateRE  = lazyregexp.New(`(?i)\b(?:citizenship|immigration|visa)\b`)
	// ifGateRE opens a label with the condition the gate above sets ("If yes,
	// what type of visa will you need?").
	ifGateRE = lazyregexp.New(`(?i)^\W*if\b`)
)

// asksItsOwn reports a box whose label asks a question of its own, so the row
// above it is no gate: iCapital's "What is your desired bonus expectation?
// Please specify the amount and currency." stood under a sponsorship Yes.
func asksItsOwn(label string) bool {
	return strings.Contains(label, "?") && !ifGateRE.MatchString(label)
}

// answerWrite is one answer the check chose for a row of the page.
type answerWrite struct {
	idx    int
	value  string
	option string
	// options are the options a checkbox group's answer ticks.
	options []string
	// rung names the rung or fact that answered: "rung1", "fact", "rung2",
	// "rung3", "fix"; "user" for a value the user's own words name.
	rung string
	// entry is the bank entry whose answer a rung 2 citation wrote, knowledge
	// the knowledge section a rung 2 answer cited first.
	entry, knowledge string
	// overwrite replaces a value this run wrote, which the engine otherwise
	// keeps: a flagged value's correction.
	overwrite bool
	// replace writes a value the user's own words name over whatever the box
	// holds: an answer the user changes.
	replace bool
}

// own reads the user's own facts once per run. A run without them has no model
// rung and no profile-grounded fact.
func (s *session) own(ctx context.Context) Own {
	if s.ownRead {
		return s.ownVal
	}
	s.ownRead = true
	if f := s.r.d.Page.Own; f != nil {
		if o, err := f(ctx); err == nil {
			s.ownVal = o
		}
	}
	return s.ownVal
}

// roleTitle is the evaluated role: the bound report's, else the list row's.
func (s *session) roleTitle() string {
	if s.e.Run.Report != nil {
		return s.e.Run.Report.Role
	}
	return s.e.Run.Input.Role
}

func (s *session) companyName() string {
	if s.e.Run.Report != nil {
		return s.e.Run.Report.Company
	}
	// A run given only an address (P&G's Workday, whose pages state no
	// employer) takes the company the posting's tracker row names.
	_, company := s.trackerRow()
	return firstNonEmpty(s.e.Run.Input.Company, company)
}

// placedLoc is where the posting's own records place it: the location its
// tracker row holds (the scan's or the evaluation's reading of its listing),
// else its board's, else the place the first page's JobPosting data states.
// A tracker place that settles no country gives way to a board place that
// settles one: a row stored with Greenhouse's site name "NIH-Bethesda" beside
// the board's office address (Black Canyon). The fill takes it as it is and
// leaves the text's location line to the matcher.
func (s *session) placedLoc() string {
	loc := firstNonEmpty(s.trackerLocation(), s.board.Location, s.e.Run.Stated.Location)
	if _, ok := match.LocationCountry(loc); !ok {
		if _, ok := match.LocationCountry(s.board.Location); ok {
			return s.board.Location
		}
	}
	return loc
}

// postingLoc is the posting's own location for every step after the fill:
// placedLoc, else the location line of the text held for the URL. Nothing else
// places a posting, neither its company nor its host.
func (s *session) postingLoc() string {
	return firstNonEmpty(s.placedLoc(), postingLocation(s.jd))
}

// ladderRowOf describes a row of the page for the rungs: the control kind,
// options and ceiling come from the matcher's own record of the question; a
// choice the matcher was never asked about carries the options of its row.
func (s *session) ladderRowOf(runID string, idx int, r engine.Row) *ladderRow {
	l := &ladderRow{idx: idx, sel: r.Selector, label: r.Label, kind: r.Kind, section: r.Section, long: r.Kind == "textarea", options: r.Options}
	if m, ok := s.r.d.Page.Matches.get(runID, r.Selector); ok {
		l.kind = string(m.Q.Kind)
		// A question asked without options (a Workday search prompt) keeps
		// what the writer found its menu holds.
		if len(m.Q.Options) > 0 {
			l.options = nil
		}
		for _, o := range m.Q.Options {
			l.options = append(l.options, o.Label)
		}
		l.long = l.long || m.Q.InputType == match.InputLongText
		l.number = m.Q.InputType == match.InputNumber
		l.multi = m.Q.InputType == match.InputMultiSelect && isOptionKind(l.kind)
		l.maxLen = m.Q.MaxLength
		if m.R.Outcome == match.OutcomeAbstained {
			l.entry = m.R.EntryID
		}
	}
	host, _ := parseHost(s.e.Run.Input.URL)
	l.groupKey = declineKey(host, r.Label, l.kind, l.options)
	return l
}

func isOptionKind(kind string) bool {
	switch match.ControlKind(kind) {
	case match.KindSelect, match.KindRadioGroup, match.KindCustomListbox:
		return true
	}
	return false
}

// open reports a required row nobody has answered: the one the ladder works
// on and the final verdict counts.
func open(r engine.Row) bool {
	if !r.Required || r.Outcome == "filled" || r.Outcome == "prefilled" {
		return false
	}
	if r.Reason == "hidden" {
		return false
	}
	return !(r.Review && r.Value != "")
}

// writeAnswers writes answers through the engine's writer and verifies each
// against the page, as the check verifies the fill's own writes: a value the
// page does not hold becomes a write-not-committed leftover and the opposite
// Yes/No a write-mismatch, and neither counts as answered. It returns the
// answers that took.
func (s *session) writeAnswers(ctx context.Context, runID string, ps *pageState, ws []answerWrite) []answerWrite {
	if len(ws) == 0 {
		return nil
	}
	res := make([]engine.Resolution, len(ws))
	for i, w := range ws {
		res[i] = engine.Resolution{Selector: ps.rows[w.idx].Selector, Outcome: "filled", Value: w.value, OptionLabel: w.option, OptionLabels: w.options, Overwrite: w.overwrite, Replace: w.replace}
	}
	got, err := s.answer(ctx, runID, res)
	for _, w := range ws {
		ps.dropNotHeld(ps.rows[w.idx].Selector)
	}
	if err != nil {
		for _, w := range ws {
			ps.setWhy(ps.rows[w.idx].Selector, "write-failed")
			ps.unwrite(w, "write-failed")
		}
		return nil
	}
	outcome, stage := map[string]string{}, map[string]string{}
	for _, g := range got {
		outcome[g.Selector], stage[g.Selector] = g.Outcome, g.Reason
	}
	held := map[string]engine.InspectRow{}
	if rows, ierr := s.inspect(ctx); ierr == nil {
		held = indexRows(rows)
	}
	// A write the first read finds uncommitted gets one more look, as the check
	// gives the fill's own writes: Workday's self-identification boxes commit a
	// moment after the click (Samsung).
	for _, w := range ws {
		if cur, ok := held[ps.rows[w.idx].Selector]; ok && verify(firstNonEmpty(w.option, w.value), cur.Value) == writeUncommitted {
			sleepCtx(ctx, s.r.d.Page.Check.withDefaults().Reread)
			if rows, ierr := s.inspect(ctx); ierr == nil {
				held = indexRows(rows)
			}
			break
		}
	}
	var kept []answerWrite
	for _, w := range ws {
		r := &ps.rows[w.idx]
		switch outcome[r.Selector] {
		case "filled", "held", "prefilled":
		default:
			// The stage the writer stopped at (listbox-not-opened, option-not-found, verify-mismatch) tells why it failed.
			why := "write-failed:" + firstNonEmpty(stage[r.Selector], outcome[r.Selector])
			ps.setWhy(r.Selector, why)
			ps.unwrite(w, why)
			continue
		}
		written := firstNonEmpty(w.option, w.value)
		if cur, ok := held[r.Selector]; ok {
			switch verify(written, cur.Value) {
			case writeUncommitted:
				ps.notHeld = append(ps.notHeld, Leftover{Selector: r.Selector, Label: r.Label, Reason: ReasonNotCommitted})
				ps.unwrite(w, ReasonNotCommitted)
				continue
			case writeWrongOption:
				ps.notHeld = append(ps.notHeld, Leftover{Selector: r.Selector, Label: r.Label, Reason: ReasonMismatch, WrongPolarity: true})
				ps.unwrite(w, ReasonMismatch)
				continue
			}
		}
		delete(ps.unwritten, r.Selector)
		r.Outcome, r.Value, r.OptionLabel = "filled", w.value, w.option
		ps.answeredBy(r.Selector, w.provenance())
		kept = append(kept, w)
	}
	return kept
}

// dropNotHeld forgets the value the page did not hold of an earlier write of
// the row: a later write replaces it.
func (ps *pageState) dropNotHeld(sel string) {
	ps.notHeld = slices.DeleteFunc(ps.notHeld, func(l Leftover) bool { return l.Selector == sel })
}

func (ps *pageState) setWhy(sel, why string) {
	if ps.why == nil {
		ps.why = map[string]string{}
	}
	ps.why[sel] = why
}

func (ps *pageState) answeredBy(sel, prov string) {
	if ps.answered == nil {
		ps.answered = map[string]string{}
	}
	ps.answered[sel] = prov
}

// ladder completes the required rows the fill left open, cheapest rung first.
// Rung 1 re-asks the bank for the rows the engine skipped or failed; the
// deterministic facts, the gated follow-ups and the No of a question about
// the user's own experience none of their information mentions come next; the
// model rungs, the grounded one and then the composed one, take what is left.
// The computer-use rung then writes by trusted input what the chain chose but
// the page did not take. Whatever remains is rung 4: the user's, with its
// reason.
func (s *session) ladder(ctx context.Context, res *engine.PageResult, ps *pageState) {
	lr := &ps.report.Ladder
	s.rung1(ctx, res, ps)
	s.deterministic(ctx, res, ps)
	s.consoleCodes(ctx, res, ps)
	s.followUps(ctx, res, ps)
	rows := s.absent(ctx, res, ps, s.modelRows(res, ps))
	callsBefore := s.e.budget.spent()
	if len(rows) > 0 && !s.e.noModel {
		for _, l := range rows {
			s.readKnowledge(ctx, l)
		}
		own := s.own(ctx)
		rows = s.rung2(ctx, res, ps, rows, own)
		s.rung3(ctx, res, ps, rows, own)
	}
	lr.Calls = s.e.budget.spent() - callsBefore
	s.computerUse(ctx, res, ps)
	unresolved := 0
	for i, r := range ps.rows {
		if open(r) && !ps.replaced(i) {
			unresolved++
		}
	}
	lr.Rung4 = unresolved
	lr.Covered = lr.Rung1 > 0 && lr.Calls == 0 && unresolved == 0
}

// wireOf is a row as the matcher is asked about it again.
func (s *session) wireOf(runID string, r engine.Row) engine.WireQuestion {
	w := engine.WireQuestion{Selector: r.Selector, Text: r.Label, Section: r.Section, Kind: r.Kind, Required: r.Required}
	if m, ok := s.r.d.Page.Matches.get(runID, r.Selector); ok {
		w.Text, w.Kind, w.InputType, w.MaxLength, w.Description = m.Q.Text, string(m.Q.Kind), string(m.Q.InputType), m.Q.MaxLength, m.Q.Description
		for _, o := range m.Q.Options {
			w.Options = append(w.Options, o.Label)
		}
		if len(w.Options) == 0 {
			w.Options = r.Options
		}
	} else {
		// A choice the matcher was never asked about carries the options the engine read; the matcher infers its input type.
		w.Options = r.Options
	}
	if w.Kind == "textarea" {
		w.Kind = string(match.KindText)
	}
	return w
}

func (s *session) matchRequest(runID string, qs []engine.WireQuestion) engine.MatchRequest {
	host, _ := parseHost(s.e.Run.Input.URL)
	return engine.MatchRequest{
		RunID: runID, RecordRun: s.e.Run.ID, Host: host, Questions: qs,
		Posting: engine.PostingContext{Company: firstNonEmpty(s.companyName(), s.statedCompany()), Title: s.roleTitle(), URL: redactURL(s.e.Run.Input.URL), JDText: s.jd, Location: s.postingLoc()},
	}
}

// rung1 re-sends, in one batch, the required rows the engine skipped or failed
// to the answer-bank matcher. A row the matcher already declined is not sent
// again: asking twice yields the same abstain or a fabrication. Nor is a row
// held as a composed draft: the matcher would hand back the same draft, and
// rung 3 composes it. A row the matcher cannot see is not sent either, nor
// one of a kind it does not take (a file upload), which would reject the
// whole batch.
func (s *session) rung1(ctx context.Context, res *engine.PageResult, ps *pageState) {
	if s.r.d.Page.Matcher == nil {
		return
	}
	var qs []engine.WireQuestion
	var idx []int
	for i, r := range ps.rows {
		if !open(r) || (r.Outcome != "skipped" && r.Outcome != "failed") || r.Reason == "awaiting-review" || isCodeBox(r.Label) || ps.replaced(i) {
			continue
		}
		w := s.wireOf(res.RunID, r)
		if !match.ControlKind(w.Kind).Valid() || isOptionKind(w.Kind) && len(w.Options) == 0 {
			continue
		}
		qs = append(qs, w)
		idx = append(idx, i)
	}
	if len(qs) == 0 {
		return
	}
	out, err := s.r.d.Page.Matcher.Match(ctx, s.matchRequest(res.RunID, qs))
	if err != nil || len(out.Results) != len(qs) {
		for _, i := range idx {
			ps.setWhy(ps.rows[i].Selector, "bank-unavailable")
		}
		return
	}
	var ws []answerWrite
	for k, got := range out.Results {
		if got.Outcome == "filled" && strings.TrimSpace(firstNonEmpty(got.OptionLabel, got.Value)) != "" {
			ws = append(ws, answerWrite{idx: idx[k], value: got.Value, option: got.OptionLabel, rung: "rung1"})
		} else {
			ps.setWhy(ps.rows[idx[k]].Selector, reasonBankDeclined+":"+firstNonEmpty(got.Reason, "no-answer"))
		}
	}
	ps.report.Ladder.Rung1 += len(s.writeAnswers(ctx, res.RunID, ps, ws))
}

// deterministic answers the shapes that need no model, each from the user's
// own facts or the posting's own words and null outside its shape.
func (s *session) deterministic(ctx context.Context, res *engine.PageResult, ps *pageState) {
	own := s.own(ctx)
	title, loc := s.roleTitle(), s.postingLoc()
	today := s.r.d.Now()
	var ws []answerWrite
	for i, r := range ps.rows {
		if r.Outcome == "filled" || r.Outcome == "prefilled" || isCodeBox(r.Label) || ps.replaced(i) {
			continue
		}
		l := s.ladderRowOf(res.RunID, i, r)
		if v, ok := factAnswer(r.Label, l.options, title, loc, own, today); ok {
			ws = append(ws, answerWrite{idx: i, value: v, option: v, rung: "fact"})
		}
	}
	ps.report.Ladder.Facts += len(s.writeAnswers(ctx, res.RunID, ps, ws))
}

// factAnswer applies the deterministic shapes in order. The internship answer
// comes first, before any filter that would drop its question.
func factAnswer(label string, options []string, role, location string, own Own, today time.Time) (string, bool) {
	if v, ok := InternshipAnswer(label, role, options); ok {
		return v, true
	}
	if v, ok := OfficeListAnswer(label, location, options); ok {
		return v, true
	}
	if v, ok := SponsorshipNowOnlyAnswer(label, own.SponsorshipNow, own.SponsorshipFuture, options); ok {
		return v, true
	}
	if v, ok := AvailabilityMonthYearAnswer(label, own.EarliestStart, own.Education, today); ok {
		return v, true
	}
	if v, ok := LicenseListAnswer(label, own.CV, options); ok {
		return v, true
	}
	if v, _, ok := BuiltSystemsAnswer(label, own.CV, options); ok {
		return v, true
	}
	if v, ok := LastInternshipAnswer(label, own.Employment); ok {
		return v, true
	}
	return "", false
}

// followUps answers a required "if yes, please explain" box whose Yes/No gate
// stands directly above it, after the earlier rungs have written: a Yes can
// open the box after every earlier read. Under a sponsorship Yes the answer is
// the user's sponsorship-type description; any other gate is re-asked of the
// bank together with the box. An "if other, please explain" box under an
// "Other" citizenship or immigration status takes the profile's visa status
// (Cursor). A box with no answer is reported for the user. A box that asks a
// question of its own is no follow-up.
func (s *session) followUps(ctx context.Context, res *engine.PageResult, ps *pageState) {
	var ws []answerWrite
	for i, r := range ps.rows {
		if i == 0 || !open(r) || !explainBoxRE.MatchString(r.Label) || asksItsOwn(r.Label) || ps.replaced(i) {
			continue
		}
		gate := ps.rows[i-1]
		if gate.Outcome != "filled" && gate.Outcome != "prefilled" {
			continue
		}
		if otherChoiceRE.MatchString(firstNonEmpty(gate.OptionLabel, gate.Value)) && otherWordRE.MatchString(r.Label) && statusGateRE.MatchString(gate.Label) {
			visa := strings.TrimSpace(s.own(ctx).VisaStatus)
			if visa == "" {
				ps.setWhy(r.Selector, "the profile's visa status is blank")
				continue
			}
			ws = append(ws, answerWrite{idx: i, value: visa, option: visa, rung: "fact"})
			continue
		}
		if polarity(firstNonEmpty(gate.OptionLabel, gate.Value)) != 1 {
			continue
		}
		if sponsorWordRE.MatchString(gate.Label) {
			desc := strings.TrimSpace(s.own(ctx).SponsorshipDescription)
			if desc == "" {
				ps.setWhy(r.Selector, "sponsorship-type-description is blank")
				continue
			}
			ws = append(ws, answerWrite{idx: i, value: desc, option: desc, rung: "fact"})
			continue
		}
		if s.r.d.Page.Matcher == nil {
			continue
		}
		out, err := s.r.d.Page.Matcher.Match(ctx, s.matchRequest(res.RunID, []engine.WireQuestion{s.wireOf(res.RunID, gate), s.wireOf(res.RunID, r)}))
		if err != nil || len(out.Results) != 2 || out.Results[1].Outcome != "filled" {
			continue
		}
		got := out.Results[1]
		ws = append(ws, answerWrite{idx: i, value: got.Value, option: got.OptionLabel, rung: "fact"})
	}
	ps.report.Ladder.Facts += len(s.writeAnswers(ctx, res.RunID, ps, ws))
}

// modelRows lists the open rows a model rung may take. Every other open row
// keeps the reason it stopped on.
func (s *session) modelRows(res *engine.PageResult, ps *pageState) []*ladderRow {
	var rows []*ladderRow
	legalOn := s.r.d.Page.Settings != nil && s.r.d.Page.Settings().AutoAcceptLegalDeclarations
	for i, r := range ps.rows {
		if !open(r) || isCodeBox(r.Label) || ps.replaced(i) {
			continue
		}
		if r.Outcome == "failed" || ps.why[r.Selector] != "" {
			continue
		}
		l, why, recorded := s.takeable(res.RunID, ps, i, legalOn, true)
		if recorded {
			ps.setWhy(r.Selector, why)
		}
		if l != nil {
			rows = append(rows, l)
		}
	}
	return rows
}

// takeable is the input filter in front of every answer a model or the Ask
// Agent gives to an open required row: the matcher's terminal abstains, the
// controls an answer cannot set and the exclusions of the apply-safety-gates
// capability (identity, self-identification, work authorisation, signatures,
// legal declarations, statements about the application). It returns the row
// as the rungs show it, or why it may not be taken; recorded says the reason
// belongs on the row, not just in the answer of this call. several admits a
// checkbox group, which only the grounded rung answers: as many of its options
// as its label asks for ("Select 2"), else every option the user's information
// supports ("Area of Expertise").
func (s *session) takeable(runID string, ps *pageState, i int, legalOn, several bool) (l *ladderRow, why string, recorded bool) {
	r := ps.rows[i]
	if match.Reason(r.MatcherReason).IsTerminal() || s.terminalMatch(runID, r.Selector) {
		return nil, reasonAbstainedByMatch + ":" + firstNonEmpty(r.MatcherReason, "terminal"), true
	}
	l = s.ladderRowOf(runID, i, r)
	if (match.ControlKind(l.kind) == match.KindCheckbox || l.multi) && several {
		if l.pick, l.upTo = pickCount(l.label, len(l.options)); l.pick == 0 && len(l.options) > 1 {
			l.pick, l.upTo = len(l.options), true
		}
	}
	if match.ControlKind(l.kind) == match.KindCheckbox && l.pick == 0 || (isOptionKind(l.kind) && len(l.options) == 0) {
		return nil, "a control a typed or chosen answer cannot set", false
	}
	if code, excluded := ladderExclusion(r, l.options, legalOn); excluded {
		return nil, code, true
	}
	return l, "", false
}

// absent answers No, before any model rung, a row about the user's own
// experience that no entry claims and that none of the user's information
// mentions (F-10, bind.Binding.OwnSearch as PageDeps.OwnSearch): the option
// meaning No, or "No" in a text box, and says so in the page's report and the
// fill stage's detail. A row whose information names some of what it asks
// about keeps what was found, for its detail if rung 2 cannot cite it. It
// returns the rows still open.
func (s *session) absent(ctx context.Context, res *engine.PageResult, ps *pageState, rows []*ladderRow) []*ladderRow {
	search := s.r.d.Page.OwnSearch
	if search == nil || len(rows) == 0 {
		return rows
	}
	qs := make([]engine.WireQuestion, len(rows))
	for i, l := range rows {
		qs[i] = s.wireOf(res.RunID, ps.rows[l.idx])
	}
	got, err := search(ctx, s.matchRequest(res.RunID, qs))
	if err != nil || len(got) != len(rows) {
		return rows
	}
	var ws []answerWrite
	terms := map[int][]string{}
	var left []*ladderRow
	for i, l := range rows {
		l.found = got[i].Found
		no, ok := AbsenceNo(l.options)
		if !got[i].Absent() || l.entry != "" || !ok || l.pick > 0 {
			left = append(left, l)
			continue
		}
		ws = append(ws, answerWrite{idx: l.idx, value: no, option: no, rung: "fact"})
		terms[l.idx] = got[i].Terms
	}
	written := s.writeAnswers(ctx, res.RunID, ps, ws)
	ps.report.Ladder.Facts += len(written)
	for _, w := range written {
		line := fmt.Sprintf("%q answered No: none of your information mentions %s", clip(ps.rows[w.idx].Label, 80), strings.Join(terms[w.idx], ", "))
		ps.report.Notes = append(ps.report.Notes, line)
		s.walkNotes = append(s.walkNotes, line)
	}
	return left
}

// AbsenceNo is the answer meaning No for a row with options: its one option
// that means No, or "No" for a text box with none; none when its options hold
// no single No. The ladder writes it for a row none of the user's information
// mentions (F-10), and the views of a run's questions read it to agree.
func AbsenceNo(options []string) (string, bool) {
	if len(options) == 0 {
		return "No", true
	}
	no := ""
	for _, o := range options {
		if polarity(o) == -1 {
			if no != "" {
				return "", false
			}
			no = o
		}
	}
	return no, no != ""
}

// readKnowledge shows the row the sections of the user's knowledge base for
// its question and options (PageDeps.Knowledge): at most 3, each cut to 600
// characters, none for a row the policies keep from a model. A knowledge base
// that fails shows none and the run goes on.
func (s *session) readKnowledge(ctx context.Context, l *ladderRow) {
	q := questionText(l.label)
	get := s.r.d.Page.Knowledge
	if get == nil || heldFromModels(q, l.kind, l.options) {
		return
	}
	for _, o := range l.options {
		if polarity(o) == 0 {
			q += "\n" + o
		}
	}
	secs, err := get(ctx, q)
	if err != nil {
		return
	}
	for _, k := range secs {
		if t := strings.TrimSpace(k.Text); t != "" && k.Ref != "" && len(l.knowledge) < maxRowKnowledge {
			l.knowledge = append(l.knowledge, match.KnowledgeSection{Ref: k.Ref, Text: clip(t, maxKnowledgeText)})
		}
	}
}

func (s *session) terminalMatch(runID, sel string) bool {
	m, ok := s.r.d.Page.Matches.get(runID, sel)
	return ok && m.R.Outcome == match.OutcomeAbstained && (m.R.Terminal || m.R.Reason.IsTerminal())
}

// excerpt is the posting's text without the questions of the form itself: what
// the page asks the applicant is not what the posting says, and the models see
// the questions only where they are meant to answer them.
func (s *session) excerpt(ps *pageState) string {
	t := s.jd
	for _, r := range ps.rows {
		if l := strings.Trim(strings.TrimSpace(match.Sanitize(r.Label, nil)), "*: "); len([]rune(l)) >= 8 {
			t = strings.ReplaceAll(t, l, " ")
		}
	}
	return strings.Join(strings.Fields(t), " ")
}

// rung2 asks the grounded question: the user's profile facts, the posting's
// excerpt and the options, one call per page for at most 25 rows, one answer
// per distinct question. It returns the rows still open.
func (s *session) rung2(ctx context.Context, res *engine.PageResult, ps *pageState, rows []*ladderRow, own Own) []*ladderRow {
	build, rev := s.r.d.Build, ""
	if s.r.d.Page.Revision != nil {
		rev = s.r.d.Page.Revision(ctx)
	}
	groups := map[string][]*ladderRow{}
	var ask []*ladderRow
	for _, l := range rows {
		if _, dup := groups[l.groupKey]; !dup {
			if s.r.d.Page.Declines.Held(l.groupKey, build, rev) {
				l.why = "declined-before"
				groups[l.groupKey] = nil
				continue
			}
			if len(ask) >= maxGroundedRows {
				l.why = "not-asked"
				continue
			}
			ask = append(ask, l)
		}
		groups[l.groupKey] = append(groups[l.groupKey], l)
	}
	if len(ask) > 0 {
		keys, facts := groundedFacts(own)
		questions, named := rowQuestions(ask)
		entries := groundedBank(own.Bank, questions, named)
		jd := s.excerpt(ps)
		system, user := groundedPrompt(ask, keys, facts, entries, jd, s.r.d.Now())
		text, why := s.e.ask(ctx, taskComplete, system, user, 2048, true)
		answers := []groundedAnswer(nil)
		if why != "" {
			for _, l := range ask {
				l.why = why
			}
		} else {
			answers = s.pinBank(ctx, res.RunID, ps, parseGrounded(text, ask, facts, entries, jd))
			if needsSupport(answers) {
				sys2, usr2 := supportPrompt(answers, facts, jd)
				text2, why2 := s.e.ask(ctx, taskComplete, sys2, usr2, 512, true)
				if why2 != "" {
					ps.report.Ladder.SupportCheck = "skipped (" + why2 + ")"
				} else {
					ps.report.Ladder.SupportCheck = "ok"
					answers = applySupport(text2, answers)
				}
			}
			for _, l := range ask {
				declined := l.why == reasonDeclined
				if !declined && !strings.HasPrefix(l.why, reasonSupportPrefix) {
					continue
				}
				if declined {
					repeat, err := s.r.d.Page.Declines.Record(ctx, l.groupKey, build, rev, s.e.Run.ID)
					if err != nil {
						s.e.Run.Warnings = append(s.e.Run.Warnings, "a decline could not be remembered: "+err.Error())
					}
					if repeat {
						s.e.Run.Repeats = append(s.e.Run.Repeats, clip(questionText(l.label), 120))
					}
				}
				// The owner's default for an experience nothing settles, declined
				// or answered without support: Yes over a plain Yes and No, else
				// the least-experience option, which the check then reviews.
				if opt, ok := assist.ExperienceDefaultChoice(questionText(l.label), l.options); ok {
					answers = append(answers, groundedAnswer{row: l, value: opt, option: opt, cites: []string{"owner-default:least-experience"}})
					l.why = ""
				}
			}
		}
		var ws []answerWrite
		for _, a := range answers {
			for _, member := range groups[a.row.groupKey] {
				ws = append(ws, answerWrite{idx: member.idx, value: a.value, option: a.option, options: a.options, rung: "rung2", entry: a.entry, knowledge: a.kb})
			}
		}
		ps.report.Ladder.Rung2 += len(s.writeAnswers(ctx, res.RunID, ps, ws))
	}
	var left []*ladderRow
	for _, l := range rows {
		if !open(ps.rows[l.idx]) {
			continue
		}
		if l.why != "" && len(l.found) > 0 {
			ps.setWhy(l.sel, l.why+"; your information mentions "+strings.Join(l.found, ", "))
		} else if l.why != "" {
			ps.setWhy(l.sel, l.why)
		}
		left = append(left, l)
	}
	return left
}

// rung3 composes answers for opinion, motivation and icebreaker questions that
// clear the deny-first list, in the user's own voice, in one call per page.
func (s *session) rung3(ctx context.Context, res *engine.PageResult, ps *pageState, rows []*ladderRow, own Own) {
	var ask []*ladderRow
	for _, l := range rows {
		if !open(ps.rows[l.idx]) {
			continue
		}
		if why := composedExclusionReason(l); why != "" {
			if ps.why[l.sel] == "" {
				ps.setWhy(l.sel, firstNonEmpty(l.why, why))
			}
			continue
		}
		if len(ask) < maxComposedRows {
			ask = append(ask, l)
		}
	}
	if len(ask) == 0 {
		return
	}
	fit := ""
	if rep := s.e.Run.Report; rep != nil && s.r.d.Page.ReportText != nil {
		if md, err := s.r.d.Page.ReportText(ctx, rep.Path); err == nil {
			fit = ReportFit(md)
		}
	}
	for _, l := range ask {
		l.why = ""
	}
	system, user := composedPrompt(ask, own, fit, s.roleTitle(), firstNonEmpty(s.companyName(), s.statedCompany()))
	text, why := s.e.ask(ctx, taskComplete, system, user, 2048, true)
	if why != "" {
		for _, l := range ask {
			ps.setWhy(l.sel, why)
		}
		return
	}
	var ws []answerWrite
	for _, a := range parseComposed(text, ask, own) {
		ws = append(ws, answerWrite{idx: a.row.idx, value: a.value, option: a.option, rung: "rung3"})
	}
	ps.report.Ladder.Rung3 += len(s.writeAnswers(ctx, res.RunID, ps, ws))
	// A row rung 3 answered keeps the reason its write did not take; a decline
	// adds to the reason an earlier rung gave.
	for _, l := range ask {
		switch w, wrote := ps.unwritten[l.sel]; {
		case !open(ps.rows[l.idx]):
		case wrote && w.rung == "rung3":
			ps.setWhy(l.sel, w.failure)
		default:
			ps.setWhy(l.sel, addWhy(ps.why[l.sel], firstNonEmpty(l.why, reasonDeclined)))
		}
	}
}

// addWhy adds why to an earlier reason that does not already open with it.
func addWhy(earlier, why string) string {
	if earlier == "" || strings.HasPrefix(earlier, why) {
		return firstNonEmpty(earlier, why)
	}
	return earlier + "; " + why
}

func (l ladderRow) String() string { return fmt.Sprintf("%s %q", l.sel, l.label) }

// Computer-use rung bounds (owner decision F-9).
const (
	maxComputerUseRows = 3
	computerUseBudget  = 30 * time.Second
	// cuOpenWait is how long an option is looked for after the list was
	// pressed open; the settles are how long the page has to hold the value.
	cuOpenWait   = 2 * time.Second
	cuSettle     = 1500 * time.Millisecond
	cuTextSettle = 3 * time.Second
	cuPoll       = 150 * time.Millisecond
	maxCUText    = 4000
	// maxCUTypeAhead bounds the label typed into a native select.
	maxCUTypeAhead = 64
	maxCUDocs      = 6
)

// cuWrite is a write the chain chose for a row that the page did not take:
// what the computer-use rung may write again by trusted input.
type cuWrite struct {
	idx           int
	value, option string
	// rung chose the value: "fill" for the fill's own write, else the rung or
	// fact of the ladder.
	rung string
	// failure is why the write did not take: write-failed:<stage>, or the
	// reason of a value the page did not hold.
	failure string
	// review marks a review-band answer, which the user is shown to review.
	review bool
	// entry is the answer-bank entry the abstain of a list the fill asked with
	// no options named: the rung reads the options once it opens the list, and
	// the matcher resolves this entry against them (CO-1). For a write of rung
	// 2 it is the entry the answer cited, and knowledge the section (F-9, F-10).
	entry, knowledge string
}

// unwrite records a write of the ladder that did not take.
func (ps *pageState) unwrite(w answerWrite, failure string) {
	if ps.unwritten == nil {
		ps.unwritten = map[string]cuWrite{}
	}
	ps.unwritten[ps.rows[w.idx].Selector] = cuWrite{idx: w.idx, value: w.value, option: w.option, rung: w.rung, failure: failure, entry: w.entry, knowledge: w.knowledge}
}

// cuFailures are the failures trusted input can cure: the page ignored the
// engine's own events, or took them and let the value go.
var cuFailures = map[string]bool{
	"write-failed": true, "write-failed:failed": true, "write-failed:listbox-not-opened": true, "write-failed:option-not-found": true,
	"write-failed:verify-mismatch": true, "write-failed:write-not-committed": true, "write-failed:control-detached": true,
	"write-failed:timeout": true, "write-failed:format-not-accepted": true, ReasonNotCommitted: true, ReasonMismatch: true,
}

// cuUnread prefixes the failure of an unread list: one the matcher was asked
// about with no options, as the prescan could not open or read it.
// cuUnreadReasons are the abstains the missing options cause: a fixed entry
// whose input types refuse the single select such a list is read as, a
// pick-option entry with no option to bind, and the assist lanes' rewrite of
// the second for a sensitive question (CO-1). cuUnreadNotFound is an answer
// the matcher gave without the options that the list's own search then did
// not find (n8n's state list holds no South Carolina): the entry is resolved
// again against what the list shows.
const (
	cuUnread         = "abstained:"
	cuUnreadNotFound = cuUnread + "option-not-found"
)

var cuUnreadReasons = map[string]bool{
	cuUnread + string(match.ReasonInputMismatch): true, cuUnread + string(match.ReasonLowConfidence): true, cuUnread + string(match.ReasonSensitiveCategory): true,
	cuUnreadNotFound: true,
}

// cuKinds are the controls the rung writes; cuTextTypes the input types it types into.
var (
	cuKinds     = map[string]bool{"text": true, "textarea": true, "select": true, "radio-group": true, "checkbox": true, "custom-listbox": true}
	cuTextTypes = map[string]bool{"": true, "text": true, "email": true, "tel": true, "url": true, "search": true, "number": true}
)

// cuEligible decides whether the computer-use rung may write w into r, and
// names the refusal. It writes a required row of a kind it knows, whose write
// failed in a way trusted input can cure, with the one value the chain chose:
// no control character, and for a choice one of its options. An unread list it
// opens only for the entry its abstain named, and writes once the matcher chose
// one of the options read (CO-1). Never a code
// box, a row gates.ComputerUseExclusion leaves to the owner, or a control whose
// values the learning service withholds, other than the profile's own identity.
func cuEligible(r engine.Row, w cuWrite, options []string, legalOn bool) (string, bool) {
	want := firstNonEmpty(w.option, w.value)
	unread := strings.HasPrefix(w.failure, cuUnread)
	switch {
	case !r.Required:
		return "optional", false
	case !cuKinds[r.Kind]:
		return "kind " + r.Kind, false
	case unread && w.entry == "":
		return "no entry", false
	case unread && !cuUnreadReasons[w.failure], !unread && !cuFailures[w.failure]:
		return "failure " + w.failure, false
	case isCodeBox(r.Label):
		return ReasonCodeBox, false
	case strings.TrimSpace(want) == "" && !unread:
		return "no value", false
	case strings.ContainsFunc(want, unicode.IsControl):
		return "control character", false
	case utf8.RuneCountInString(want) > maxCUText:
		return "too long", false
	}
	if code, excluded := gates.ComputerUseExclusion(gates.LadderRow{Label: r.Label, Options: options, LegalSettingOn: legalOn}); excluded {
		return code, false
	}
	if wh := learn.ClassifyEdit(r.Label, r.Kind, "", "", options, w.value, w.option); wh != learn.WithheldNone && wh != learn.WithheldProfileIdentity {
		return "withheld " + string(wh), false
	}
	switch {
	case r.Kind == "text" || r.Kind == "textarea":
	case r.Kind == "checkbox" && len(options) <= 1:
		if !cuTicks(want, options) {
			return "untick", false
		}
	case len(options) > 0 && !slices.ContainsFunc(options, func(o string) bool { return normValue(o) == normValue(want) }):
		return reasonNotAnOption, false
	case r.Kind == "select" && utf8.RuneCountInString(want) > maxCUTypeAhead:
		return "too long", false
	}
	return "", true
}

// cuTicks reports a lone box's answer that ticks it; the rung never clears one.
func cuTicks(want string, options []string) bool {
	n := normValue(want)
	return polarity(want) == 1 || n == "checked" || n == "on" || len(options) == 1 && n == normValue(options[0])
}

// cuWrites lists, in page order, each required row's last write that did not
// take: the ladder's from writeAnswers' record; a fill write the check found
// not held, and one that failed outright, with the fill's own choice; and each
// open list the matcher was asked about with no options, with the reason and
// entry of its answer, whose value is still to choose.
func (s *session) cuWrites(runID string, ps *pageState) []cuWrite {
	byRow := map[int]cuWrite{}
	for _, w := range ps.unwritten {
		byRow[w.idx] = w
	}
	for _, l := range ps.notHeld {
		for i, r := range ps.rows {
			if _, ok := byRow[i]; !ok && r.Selector == l.Selector && r.Outcome == "filled" {
				byRow[i] = cuWrite{idx: i, value: r.Value, option: r.OptionLabel, rung: "fill", failure: l.Reason}
			}
		}
	}
	for i, r := range ps.rows {
		if _, ok := byRow[i]; ok || r.Outcome != "failed" {
			continue
		}
		// A review-band answer is one the engine writes too, flagged for the user.
		if m, ok := s.r.d.Page.Matches.get(runID, r.Selector); ok && (m.R.Outcome == match.OutcomeFilled || m.R.Outcome == match.OutcomeFilledReview) && len(m.R.ChosenLabels) <= 1 {
			byRow[i] = cuWrite{idx: i, value: m.R.Answer, option: m.R.ChosenLabel, rung: "fill", failure: "write-failed:" + firstNonEmpty(r.Reason, r.Outcome), review: m.R.Outcome == match.OutcomeFilledReview}
		}
	}
	for i, r := range ps.rows {
		if _, ok := byRow[i]; ok || !open(r) || r.Kind != "custom-listbox" && r.Kind != "select" {
			continue
		}
		if m, ok := s.r.d.Page.Matches.get(runID, r.Selector); ok && len(m.Q.Options) == 0 {
			failure := cuUnread + string(m.R.Reason)
			if m.R.Reason == "" && r.Reason == "option-not-found" {
				failure = cuUnreadNotFound
			}
			byRow[i] = cuWrite{idx: i, rung: "fill", failure: failure, entry: m.R.EntryID}
		}
	}
	var out []cuWrite
	for _, i := range slices.Sorted(maps.Keys(byRow)) {
		out = append(out, byRow[i])
	}
	return out
}

// computerUse is the rung after the ladder's writes (owner decision F-9): a
// required row whose answer the chain chose but whose write did not take gets
// one more write by trusted input in the apply browser, as a person makes it,
// for at most maxComputerUseRows rows a page and computerUseBudget each. A list
// the fill asked with no options is opened and written with the option the
// matcher picks among those it shows, which the matcher's record of the
// question keeps (CO-1). A row the page then holds is this rung's; any other
// keeps its reason, which names the step that failed. Nothing is asked of
// anyone and no model chooses a value.
func (s *session) computerUse(ctx context.Context, res *engine.PageResult, ps *pageState) {
	ws := s.cuWrites(res.RunID, ps)
	if len(ws) == 0 || s.tab == nil {
		return
	}
	rows, err := s.inspect(ctx)
	if err != nil {
		return
	}
	held, live := indexHeld(rows, ps.rows)
	ws = landedLate(ps, ws, held)
	holder, ok := holderOf(s.page)
	if !ok {
		return
	}
	legalOn := s.r.d.Page.Settings != nil && s.r.d.Page.Settings().AutoAcceptLegalDeclarations
	var href string
	_ = s.tab.Eval(ctx, "location.href", &href)
	var wrote []string
	tried := 0
	for _, w := range ws {
		r := ps.rows[w.idx]
		l := s.ladderRowOf(res.RunID, w.idx, r)
		if _, ok := cuEligible(r, w, l.options, legalOn); !ok || ps.replaced(w.idx) {
			continue
		}
		if tried == maxComputerUseRows {
			break
		}
		tried++
		c := &cuRun{s: s, holder: holder, docs: s.cuDocs(ctx)}
		rctx, cancel := context.WithTimeout(ctx, computerUseBudget)
		sel, before := firstNonEmpty(live[r.Selector], r.Selector), held[r.Selector].Value
		var step string
		if strings.HasPrefix(w.failure, cuUnread) {
			var options []string
			w, options, step = c.choose(rctx, res.RunID, ps.rows, r, sel, w, legalOn, before)
			if len(options) > 0 {
				s.r.d.Page.Matches.list(res.RunID, r.Selector, options)
			}
		} else {
			step = c.write(rctx, ps.rows, r, sel, w, l.options, before)
		}
		cancel()
		if step == "" {
			ps.cuWrote(w)
			wrote = append(wrote, fmt.Sprintf("%q (%s)", clip(r.Label, 80), s.cuSource(res.RunID, r.Selector, w)))
		} else {
			ps.cuMissed(w, step)
		}
		var now string
		if href != "" && s.tab.Eval(ctx, "location.href", &now) == nil && now != href {
			ps.report.Notes = append(ps.report.Notes, "computer use stopped: the page moved to another address")
			break
		}
	}
	if len(wrote) > 0 {
		line := "computer use wrote " + strings.Join(wrote, ", ")
		ps.report.Notes = append(ps.report.Notes, line)
		s.walkNotes = append(s.walkNotes, line)
	}
}

// cuSource names where a value the rung wrote came from: the answer-bank entry
// that answered the row with it, else the rung that chose it.
func (s *session) cuSource(runID, sel string, w cuWrite) string {
	if w.entry != "" {
		return "entry " + w.entry
	}
	m, ok := s.r.d.Page.Matches.get(runID, sel)
	if ok && (learn.Routed{EntryID: m.R.EntryID, Outcome: m.R.Outcome}).Fills() && normValue(firstNonEmpty(w.option, w.value)) == normValue(firstNonEmpty(m.R.ChosenLabel, m.R.Answer)) {
		return "entry " + m.R.EntryID
	}
	return w.rung
}

// landedLate answers each row whose write ran past its deadline and which the
// page holds by now, as the rung's own re-read would judge it (Visa's
// Workday date box took the date after its write timed out), and returns the
// writes still to try. Nothing is pressed or typed for such a row.
func landedLate(ps *pageState, ws []cuWrite, held map[string]engine.InspectRow) []cuWrite {
	var late []string
	ws = slices.DeleteFunc(ws, func(w cuWrite) bool {
		r := ps.rows[w.idx]
		h, ok := held[r.Selector]
		if w.failure != "write-failed:timeout" || !ok || !cuHeld(r.Kind == "text" || r.Kind == "textarea", firstNonEmpty(w.option, w.value), r.Value, h.Value) {
			return false
		}
		ps.pageHolds(w)
		late = append(late, fmt.Sprintf("%q", clip(r.Label, 80)))
		return true
	})
	if len(late) > 0 {
		ps.report.Notes = append(ps.report.Notes, "the page holds what a write that ran past its deadline wrote: "+strings.Join(late, ", "))
	}
	return ws
}

// cuWrote records a write of the rung the page holds: the row is answered and
// is no longer a value the page lacks.
func (ps *pageState) cuWrote(w cuWrite) {
	ps.pageHolds(w)
	ps.report.Ladder.ComputerUse++
}

// pageHolds records a chosen value the page holds: the row is answered and is
// no longer a value the page lacks.
func (ps *pageState) pageHolds(w cuWrite) {
	r := &ps.rows[w.idx]
	r.Outcome, r.Value, r.OptionLabel = "filled", w.value, w.option
	r.Review = r.Review || w.review
	delete(ps.why, r.Selector)
	delete(ps.unwritten, r.Selector)
	ps.dropNotHeld(r.Selector)
	ps.report.Uncommitted = slices.DeleteFunc(ps.report.Uncommitted, func(l string) bool { return l == r.Label })
	if w.entry != "" || w.rung != "fill" {
		ps.answeredBy(r.Selector, answerWrite{entry: w.entry, knowledge: w.knowledge}.provenance())
	}
}

// cuMissed names, on the row's reason, the step a write of the rung failed at.
func (ps *pageState) cuMissed(w cuWrite, step string) {
	r := ps.rows[w.idx]
	why := w.failure + "; computer use: " + step
	if open(r) {
		ps.setWhy(r.Selector, why)
	}
	for i := range ps.notHeld {
		if ps.notHeld[i].Selector == r.Selector {
			ps.notHeld[i].Detail = why
		}
	}
	ps.report.Notes = append(ps.report.Notes, fmt.Sprintf("computer use did not write %q: %s", clip(r.Label, 80), step))
}

// cuResolve has the matcher resolve the entry w's abstain named against the
// options a list showed once the rung opened it (Deps.Page.Pin: the matcher's
// own guards and option binding, no model). It returns the write of the one
// option the matcher picks, judged again by the rung's refusals with those
// options, or why there is none, naming the options so that the owner's answer
// is one press.
func (s *session) cuResolve(ctx context.Context, runID string, r engine.Row, w cuWrite, options []string, legalOn bool) (cuWrite, string) {
	if len(options) == 0 {
		return w, "the list shows no options"
	}
	shown := "the list shows " + shownOptions(options)
	if s.r.d.Page.Pin == nil {
		return w, "no matcher resolves entry " + w.entry + "; " + shown
	}
	q := s.wireOf(runID, r)
	q.Options = options
	got, err := s.r.d.Page.Pin(ctx, s.matchRequest(runID, []engine.WireQuestion{q}), []string{w.entry})
	if err != nil || len(got) != 1 {
		return w, "the matcher could not resolve entry " + w.entry + "; " + shown
	}
	g, pick := got[0], w
	pick.value, pick.option, pick.review = g.Value, g.OptionLabel, g.Outcome == string(match.OutcomeFilledReview)
	switch {
	case len(g.OptionLabels) > 1:
		return w, fmt.Sprintf("entry %s picks several of them; %s", w.entry, shown)
	case g.Outcome != string(match.OutcomeFilled) && !pick.review:
		return w, fmt.Sprintf("entry %s picks none of them (%s); %s", w.entry, firstNonEmpty(g.Reason, g.Outcome), shown)
	}
	if why, ok := cuEligible(r, pick, options, legalOn); !ok {
		return w, fmt.Sprintf("the matcher's pick %q is refused (%s); %s", clip(pick.option, 80), why, shown)
	}
	return pick, ""
}

// shownOptions names the options a list showed, at most 8 of them.
func shownOptions(options []string) string {
	var b strings.Builder
	for i, o := range options {
		if i == 8 {
			fmt.Fprintf(&b, " and %d more", len(options)-i)
			break
		}
		if i > 0 {
			b.WriteString(", ")
		}
		fmt.Fprintf(&b, "%q", clip(o, 60))
	}
	return b.String()
}
