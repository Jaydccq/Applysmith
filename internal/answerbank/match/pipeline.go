package match

import (
	"context"
	"fmt"
	"sync"
)

// Draft is what a stage proposes for a question. The pipeline turns it into a
// Result: it applies the confidence and outcome mapping, the review lift and the
// entry's own floor. A draft with an Abstain ends the walk with that abstain.
type Draft struct {
	// Entry is the entry the draft answers from; the classified entry when nil.
	Entry        *Entry
	Answer       string
	ChosenLabel  string
	ChosenLabels []string
	Source       Source
	// Start is the starting confidence. Zero means the stage default: the
	// classification's for an entry resolver, 0.95 for the walk-ending steps.
	Start float64
	// HasOption marks that an option was bound; OptionScore is its score and
	// OtherValueScore the best score among options with a different value.
	HasOption       bool
	OptionScore     float64
	OtherValueScore float64
	// Deterministic marks an answer that does not depend on phrasing: a fixed
	// entry, or a pick-option entry with exactly one preferred value.
	Deterministic bool
	// Alternatives are the entry's other ranked values, in order, on a pick
	// deferred to the apply step for want of options: a search that holds
	// none of the Answer is searched for the next.
	Alternatives []string
	Abstain      *Abstain
	Trace        []string
}

// Step is the context every stage receives. A stage reads it and never mutates
// the request.
type Step struct {
	Req *Request
	// Raw is the question as the caller sent it; Q is the prepared question with
	// the sanitized text, normalized options and the input type.
	Raw Question
	Q   Question
	// Entries is the effective entry set for this question: run-context entries
	// appended and entries whose captured scope does not admit it removed.
	Entries []Entry
	// Entry and Class are set once classification has chosen an entry.
	Entry *Entry
	Class classification
	Index *classIndex
	// Employers are the bank's canonical employers and their aliases.
	Employers []Employer
	trace     []string
	// kind is the resolver of the chosen entry's kind, for the self-identification
	// check, which must know whether the entry resolves on its own.
	kind StageFunc
	// out is the draft the output guards are looking at.
	out Draft
	sc  *optionScorer
	// bare scores options read without their question text (bindOption).
	bare *optionScorer
	// reviewOnly caps the answer at the review band: a guard answered on an
	// assumption the posting does not confirm.
	reviewOnly bool
	// presumedUS marks an answer the posting-country guard gave as for a
	// posting in the United States, which nothing placed; the result carries
	// DetailPostingCountryPresumed.
	presumedUS bool
	// otherCountries are the countries of a posting_country block whose every
	// country the profile does not record, for otherCountryAnswer.
	otherCountries []string
	// otherAuth is the user's other-country work-authorization answer, which
	// storedWorkAuth reads instead of the U.S. one while otherCountryAnswer
	// answers a question about an unrecorded country.
	otherAuth *bool
	// rematch matches a sub-question of this request on its own.
	rematch func(Question) Result
	// ownEntries are Entries as the own-experience search reads them, built by
	// the question's first search (ownMentions).
	ownEntries []ownSource
}

// scorer is the shared option scorer of this question.
func (s *Step) scorer() *optionScorer {
	if s.sc == nil || s.sc.text != s.Q.Text {
		s.sc = newOptionScorer(s.Q.Text, s.Req.bud)
		s.sc.shared = s.Req.labels
	}
	return s.sc
}

// bareScorer is the question's option scorer for options read without their
// question text.
func (s *Step) bareScorer() *optionScorer {
	if s.bare == nil {
		s.bare = newOptionScorer("", s.Req.bud)
		s.bare.shared = s.Req.labels
	}
	return s.bare
}

// Rematch matches another question of the same request on its own, with the
// same entry set and stages, and returns its final result. A stage that decides
// a question from a sub-question (the primary predicate of a combined
// disclosure) uses it, so the sub-question gets every guard and cap.
func (s *Step) Rematch(q Question) Result { return s.rematch(q) }

// Output is the draft the output guards are looking at.
func (s *Step) Output() Draft { return s.out }

// Tracef appends a decision line to the question's trace.
func (s *Step) Tracef(format string, args ...any) {
	s.trace = append(s.trace, fmt.Sprintf(format, args...))
}

// ClassTag is the variant tag of the route that classified the entry.
func (s *Step) ClassTag() string { return s.Class.tag }

// StageFunc is one resolver stage. It returns a draft and true when it handled
// the question, or false for "not handled" so the walk continues.
type StageFunc func(*Step) (Draft, bool)

// PostPass is a request-wide pass that runs after every question has been
// matched. It may rewrite results that are not terminal abstains.
type PostPass func(req *Request, questions []Question, results []Result)

// Stages are the slots of the resolver-order pipeline that other packages of
// work fill. A nil slot is "not handled". The numbers are the steps of the
// resolver order.
type Stages struct {
	// Step 3: a control of an identified education or work-history row.
	RowControls StageFunc
	// Step 4, in this order.
	HumanVerification    StageFunc
	ContactComposite     StageFunc
	ApplicationStatement StageFunc
	RelocationLiteral    StageFunc
	IfNoIndicateNo       StageFunc
	// Relationship is the company-relationship lane, the last of step 4. Its
	// unresolved verdicts are never terminal.
	Relationship StageFunc
	// Step 6: combined and narrowed disclosures, only when no curated entry
	// matched.
	CombinedDisclosures StageFunc
	// Step 9: the self-identification set check, whatever entry was chosen.
	SelfIDCheck StageFunc
	// Step 10: country, currency, held-compensation and employer-detail
	// policies on the chosen entry.
	ContextPolicies StageFunc
	// Step 11: resolution of the chosen entry by its kind. Derived is tried
	// first for an entry marked Derived.
	Derived             StageFunc
	Fixed               StageFunc
	PickOption          StageFunc
	AutoAcknowledge     StageFunc
	MultiSelect         StageFunc
	EmployerHistory     StageFunc
	CompensationRange   StageFunc
	LanguageProficiency StageFunc
	// Step 12: guards on the produced answer, for example the option constraint.
	OutputGuards StageFunc
	// PostPasses run in order after every question has passed step 12.
	PostPasses []PostPass
}

func (st Stages) forKind(e *Entry) StageFunc {
	if e.derived() && st.Derived != nil {
		return st.Derived
	}
	switch e.Kind {
	case KindFixed:
		return st.Fixed
	case KindPickOption:
		return st.PickOption
	case KindAutoAcknowledge:
		return st.AutoAcknowledge
	case KindMultiSelect:
		return st.MultiSelect
	case KindEmployerHistory:
		return st.EmployerHistory
	case KindCompensationRange:
		return st.CompensationRange
	case KindLanguageProficiency:
		return st.LanguageProficiency
	}
	return nil
}

// Matcher runs the resolver order over a bank. It is safe for concurrent use:
// it holds no mutable state.
type Matcher struct {
	bank   Bank
	stages Stages

	// bases keeps, per host override key and run context, the entries a
	// request reads and their class index, which depend on nothing else: a
	// matcher asked many single questions (learn.Router) builds them once, as
	// one request of many questions already shares them. Every host no
	// override names reads the same entries.
	mu    sync.Mutex
	bases map[string]hostBase
}

// hostBase is the bank as a request on one host reads it.
type hostBase struct {
	entries []Entry
	index   *classIndex
	scoped  bool
}

// New returns a matcher over a bank with the given stage slots.
func New(bank Bank, stages Stages) *Matcher { return &Matcher{bank: bank, stages: stages} }

// Bank is the bank the matcher reads.
func (m *Matcher) Bank() Bank { return m.bank }

// baseFor is the entries and class index a request reads: the bank's entries
// for its host and the run-context entries its attachments make.
func (m *Matcher) baseFor(req Request) hostBase {
	ctx := runContextEntries(req)
	key, _ := m.bank.HostKey(req.Host)
	for _, e := range ctx {
		key += "\x00" + e.Answer
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if b, ok := m.bases[key]; ok {
		return b
	}
	b := hostBase{entries: append(append([]Entry(nil), m.bank.EntriesForHost(req.Host)...), ctx...)}
	b.index = newClassIndex(b.entries)
	for i := range b.entries {
		if b.entries[i].Scoped() {
			b.scoped = true
			break
		}
	}
	if m.bases == nil {
		m.bases = map[string]hostBase{}
	}
	m.bases[key] = b
	return b
}

// Response is the result of one match call, one result per question in order.
type Response struct {
	Results []Result
	// Prepared are the questions as the matcher read them: sanitized text,
	// normalized options and the inferred input type. The model-assisted lanes
	// build their prompts from these, never from the raw text.
	Prepared []Question
}

// Match matches every question of the request independently, in order, then
// runs the request-wide post-passes.
func (m *Matcher) Match(req Request) (Response, error) {
	return m.MatchContext(context.Background(), req)
}

// MatchContext is Match under a context. The matcher reads no clock: when the
// context is done it stops scoring and every control it had not finished
// abstains with budget-exhausted, so a page never overruns its request budget.
func (m *Matcher) MatchContext(ctx context.Context, req Request) (Response, error) {
	req.bud = newBudget(ctx)
	req.labels = map[labelKey]*labelProfile{}
	resp, qs, err := m.matchQuestions(req)
	if err != nil {
		return Response{}, err
	}
	resp.Prepared = qs
	req.matcher = m
	for _, pass := range m.stages.PostPasses {
		pass(&req, qs, resp.Results)
	}
	return resp, nil
}

// matchQuestions runs the resolver order over every question of the request and
// returns the prepared questions beside the results.
func (m *Matcher) matchQuestions(req Request) (Response, []Question, error) {
	if err := req.Validate(); err != nil {
		return Response{}, nil, err
	}
	hb := m.baseFor(req)
	base, shared, scoped := hb.entries, hb.index, hb.scoped
	results := make([]Result, len(req.Questions))
	qs := make([]Question, len(req.Questions))
	memo := map[string]hostBase{}
	for i, raw := range req.Questions {
		p := prepareQuestion(raw)
		qs[i] = p.Q
		if req.bud.over() {
			results[i] = budgetResult(raw.Selector)
			continue
		}
		entries, ix := base, shared
		if _, named := programNames(p.Q.Text); scoped || named {
			entries, ix = hb.scopedTo(req.Host, p.Q, memo)
		}
		results[i] = m.matchOne(&req, p, entries, ix)
		if req.bud.over() {
			results[i] = budgetResult(raw.Selector)
		}
	}
	return Response{Results: results}, qs, nil
}

// runContextEntries are the synthetic per-run facts: never stored, reached only
// by the statement rule, answered only from an observation.
func runContextEntries(req Request) []Entry {
	mk := func(id string, observed *bool) Entry {
		e := Entry{ID: id, Kind: KindFixed, Runtime: Runtime{RunContext: true},
			Meta: Meta{InputTypes: []string{string(InputYesNo), string(InputSingleSelect), string(InputCheckbox)}}}
		if observed != nil && *observed {
			e.Answer = "Yes"
		}
		return e
	}
	return []Entry{mk(IDCoverLetterSubmitted, req.CoverLetterAttached), mk(IDResumeSubmitted, req.ResumeAttached)}
}

// scopedTo is the entries and class index one question reads: the base's
// entries whose captured scope admits this host, question text and option set,
// and for a named programme those naming it. The base itself serves a question
// that admits every entry; memo keeps one index per admitted set, so the
// questions of a request that admit the same entries share it.
func (b hostBase) scopedTo(host string, q Question, memo map[string]hostBase) ([]Entry, *classIndex) {
	labels := make([]string, len(q.Options))
	for i, o := range q.Options {
		labels[i] = o.Label
	}
	prog, named := programNames(q.Text)
	out := make([]Entry, 0, len(b.entries))
	kept := make([]byte, len(b.entries))
	for i, e := range b.entries {
		if e.Admits(host, q.Text, labels) && (!named || e.RunContext || e.namesProgram(prog)) {
			out = append(out, e)
			kept[i] = 1
		}
	}
	if len(out) == len(b.entries) {
		return b.entries, b.index
	}
	s, ok := memo[string(kept)]
	if !ok {
		s = hostBase{entries: out, index: newClassIndex(out)}
		memo[string(kept)] = s
	}
	return s.entries, s.index
}

// abstainResult builds a final abstain. A blank entry only the user's answer
// fills is terminal: no later lane may answer for the user.
func (s *Step) abstainResult(a Abstain, e *Entry) Result {
	r := Result{Selector: s.Raw.Selector, Outcome: OutcomeAbstained, Reason: a.Reason, Detail: a.Detail,
		Terminal: a.Terminal() || e != nil && a.Reason == ReasonPlaceholderEmpty && OwnAnswerOnly(e.ID), Trace: s.trace}
	if e != nil {
		r.EntryID, r.AnswerKey = e.ID, e.EffectiveAnswerKey()
	}
	s.Tracef("abstain-reason: %s", a.Reason)
	r.Trace = s.trace
	return r
}

// newStep is the context of one question of a request.
func (m *Matcher) newStep(req *Request, p prepared, entries []Entry, ix *classIndex) *Step {
	// A company named by its board slug is read as the question writes it.
	if w := writtenCompany(p.Q.Text, req.Posting.Company); w != "" {
		own := *req
		own.Posting.Company = w
		req = &own
	}
	s := &Step{Req: req, Raw: p.Raw, Q: p.Q, Entries: entries, Index: ix, Employers: m.bank.Employers, trace: append([]string(nil), p.trace...)}
	s.rematch = func(q Question) Result {
		sub := *req
		sub.Questions = []Question{q}
		resp, _, err := m.matchQuestions(sub)
		if err != nil {
			return Result{Selector: q.Selector, Outcome: OutcomeAbstained, Reason: ReasonNoEntry}
		}
		return resp.Results[0]
	}
	s.Tracef("question=%q kind=%s inputType=%s required=%t", p.Q.Text, p.Q.Kind, p.Q.InputType, p.Q.IsRequired())
	return s
}

// restored puts the page's own spelling back on the chosen labels.
func (p prepared) restored(r Result) Result {
	r.ChosenLabel = p.restoreLabel(r.ChosenLabel)
	for i, l := range r.ChosenLabels {
		r.ChosenLabels[i] = p.restoreLabel(l)
	}
	return r
}

// intakeAbstain is steps 1 and 2: a prompt-only text and a trap abstain before
// anything is classified.
func (s *Step) intakeAbstain() (Result, bool) {
	if IsContentless(s.Q.Text) {
		s.Tracef("question text is the control's own prompt or format, not a question -> abstain")
		return s.abstainResult(Abstain{Reason: ReasonNoEntry}, nil), true
	}
	if line, trap := trapAbstain(s.Q); trap {
		s.Tracef("trap: %s", line)
		return s.abstainResult(Abstain{Reason: ReasonAIInstruction}, nil), true
	}
	if place, other := otherCountryField(s); other {
		s.Tracef("the field is for %s only and the applicant lives elsewhere -> left blank", place)
		return s.abstainResult(Abstain{Reason: ReasonContextPolicy}, nil), true
	}
	return Result{}, false
}

// matchOne runs the resolver order for one question.
func (m *Matcher) matchOne(req *Request, p prepared, entries []Entry, ix *classIndex) Result {
	s := m.newStep(req, p, entries, ix)
	q := s.Q
	done := p.restored
	fill := func(d Draft, e *Entry) Result { return done(s.finalize(d, e, startWalkEnd)) }

	// 1. Intake: a prompt-only text abstains no-entry. 2. Traps.
	if r, ok := s.intakeAbstain(); ok {
		return done(r)
	}
	// 3. Row controls, then 4. form-text answers in their fixed order.
	for _, stage := range []StageFunc{
		m.stages.RowControls, m.stages.HumanVerification, m.stages.ContactComposite,
		m.stages.ApplicationStatement, m.stages.RelocationLiteral, m.stages.IfNoIndicateNo,
		m.stages.Relationship,
	} {
		if stage == nil {
			continue
		}
		if d, ok := stage(s); ok {
			return fill(d, nil)
		}
	}

	// 5. Curated classification, 6 and 7 only when it found nothing.
	cx := newClassifier(ix, q, s.scorer())
	cl, curated := cx.curated()
	if !curated {
		if m.stages.CombinedDisclosures != nil {
			if d, ok := m.stages.CombinedDisclosures(s); ok {
				return fill(d, nil)
			}
		}
		// 8. General classification. A question whose only prose is the
		// explanation of a No goes to the entry that claims it.
		if a, line, hit := narrativeGuard(q); hit {
			if cl = cx.general(); cl.entry == nil || !wantsWordsOnlyOfANo(q.Text) {
				s.Tracef("%s", line)
				return done(s.abstainResult(a, nil))
			}
			s.Tracef("the question wants words only of a No: its entry answers")
		} else {
			cl = cx.general()
		}
	}
	s.trace = append(s.trace, cl.trace...)
	if req.bud.over() {
		return budgetResult(q.Selector)
	}
	if cl.entry == nil {
		a := Abstain{Reason: ReasonNoEntry}
		if cl.abstain != nil {
			a = *cl.abstain
		}
		s.Tracef("no entry matched the question")
		// An option list that names protected characteristics is never left to a
		// later lane, whether or not an entry was chosen.
		if m.stages.SelfIDCheck != nil {
			if d, ok := m.stages.SelfIDCheck(s); ok {
				return fill(d, nil)
			}
		}
		return done(s.abstainResult(a, nil))
	}
	return done(m.resolveChosen(s, cl))
}

// resolveChosen runs steps 9 to 12 for the entry classification chose: the
// self-identification check, the policy guards, the entry's resolver, the
// output guards and the confidence mapping.
func (m *Matcher) resolveChosen(s *Step, cl classification) Result {
	q := s.Q
	s.Entry, s.Class = cl.entry, cl
	s.kind = m.stages.forKind(cl.entry)
	s.Tracef("%s", cl.describe(q))
	e := cl.entry

	// 9. Self-identification set check, whatever entry was chosen.
	if m.stages.SelfIDCheck != nil {
		if d, ok := m.stages.SelfIDCheck(s); ok {
			return s.finalize(d, e, startWalkEnd)
		}
	}
	// 10. Policy guards on the chosen entry, in order.
	if a, line, ok := m.policyGuards(s); ok {
		if line != "" {
			s.Tracef("%s", line)
		}
		if r, ok := m.otherCountryAnswer(s, cl, a); ok {
			return r
		}
		if d, ok := saysNone(s, a); ok {
			return s.finalize(d, e, cl.start)
		}
		return s.abstainResult(a, e)
	}
	// 11. Resolution of the entry by its kind.
	stage := m.stages.forKind(e)
	if stage == nil {
		s.Tracef("no resolver for entry kind %q", e.Kind)
		return s.abstainResult(Abstain{Reason: ReasonNoEntry}, e)
	}
	d, ok := stage(s)
	if !ok {
		s.Tracef("resolver for entry kind %q did not handle the question", e.Kind)
		return s.abstainResult(Abstain{Reason: ReasonNoEntry}, e)
	}
	// 12. Output guards, then the confidence and outcome mapping.
	if m.stages.OutputGuards != nil && d.Abstain == nil {
		s.out = d
		if g, hit := m.stages.OutputGuards(s); hit {
			d = g
		}
	}
	return s.finalize(d, e, cl.start)
}

// MatchPinned matches one question against the one entry the caller names, as
// if classification had chosen it: the intake and trap checks, then steps 9 to
// 12 with every guard, policy and option binding of the resolver order, at the
// curated topic lane's starting confidence. Classification is not run, so the
// matcher itself never decides that the entry is the question's; the caller
// (the model-assisted recognition lane) does, and this decides only whether
// and how the entry answers. An entry the question's effective entry set does
// not hold abstains no-entry. Post-passes do not run.
func (m *Matcher) MatchPinned(ctx context.Context, req Request, q Question, entryID string) (Result, error) {
	req.Questions = []Question{q}
	if err := req.Validate(); err != nil {
		return Result{}, err
	}
	req.bud = newBudget(ctx)
	req.labels = map[labelKey]*labelProfile{}
	p := prepareQuestion(q)
	entries, ix := m.baseFor(req).scopedTo(req.Host, p.Q, map[string]hostBase{})
	s := m.newStep(&req, p, entries, ix)
	if r, ok := s.intakeAbstain(); ok {
		return p.restored(r), nil
	}
	for i := range entries {
		if e := &entries[i]; e.ID == entryID && !e.RunContext {
			cl := classification{entry: e, stage: stagePinned, via: "pinned entry", start: startTopicLane}
			return p.restored(m.resolveChosen(s, cl)), nil
		}
	}
	s.Tracef("pinned entry %q is not in the question's entry set", entryID)
	return p.restored(s.abstainResult(Abstain{Reason: ReasonNoEntry}, nil)), nil
}

// policyGuards runs step 10: the context policies of the resolvers, the section
// policy, the ask-me marker, control shape acceptance, the empty-body abstain
// and the answer-shape guards.
func (m *Matcher) policyGuards(s *Step) (Abstain, string, bool) {
	e, q := s.Entry, s.Q
	if m.stages.ContextPolicies != nil {
		if d, ok := m.stages.ContextPolicies(s); ok && d.Abstain != nil {
			s.trace = append(s.trace, d.Trace...)
			return *d.Abstain, "", true
		}
	}
	if a, line, ok := sectionPolicy(e, q); ok {
		return a, line, true
	}
	if e.AsksMe() {
		return Abstain{Reason: ReasonAskMe}, fmt.Sprintf("entry %q holds the ask-me marker", e.ID), true
	}
	if v := acceptsControl(e, q); !v.OK {
		// Widening (b) judges a fixed entry by its answer: a blank one lacks that.
		if e.Kind == KindFixed && e.BodyEmpty() && !e.derived() && !e.futureOnlyDate() && (q.InputType == InputText && len(q.Options) == 0 || linkBox(e, q)) {
			return Abstain{Reason: ReasonPlaceholderEmpty}, fmt.Sprintf("entry %q has an empty body", e.ID), true
		}
		if e.Category == CategorySelfIdentification && q.InputType == InputLongText {
			return Abstain{Reason: ReasonSelfIDPolicy}, "a self-identification answer is never typed into a multi-line box", true
		}
		return Abstain{Reason: ReasonInputMismatch},
			fmt.Sprintf("entry input_types reject inputType=%s (answerKey=%s) -> abstain", q.InputType, e.EffectiveAnswerKey()), true
	} else if v.Note != "" {
		s.Tracef("%s", v.Note)
	}
	if e.BodyEmpty() && !e.derived() && !e.futureOnlyDate() && !e.policyResolved() && !e.thresholdExceeded(q.Text) && !statusNamesOneOption(s) && !relocationStatusAnswers(s) && !referralNotApplies(s, e) {
		return Abstain{Reason: ReasonPlaceholderEmpty}, fmt.Sprintf("entry %q has an empty body", e.ID), true
	}
	return answerShapeGuard(s.Class, q, s.Index)
}

// noneTokenRE is a label that names its own answer for an applicant without
// the link it asks for ("write na if you don't have one").
var noneTokenRE = regexpMust(`(?i)\b(?:write|enter|put|type)\s+["']?(n/?a|none)["']?\s+if\s+(?:you\s+)?(?:do\s+not|don't|have\s+no)\b`)

// saysNone answers a link the bank holds blank with the token the label itself
// asks for when there is no link, spelled as the label spells it.
func saysNone(s *Step, a Abstain) (Draft, bool) {
	e, q := s.Entry, s.Q
	if a.Reason != ReasonPlaceholderEmpty || e.Category != CategoryLinks || len(q.Options) > 0 ||
		q.InputType != InputText && q.InputType != InputLongText {
		return Draft{}, false
	}
	m := noneTokenRE.FindStringSubmatch(q.Text)
	if m == nil {
		return Draft{}, false
	}
	d := derivedDraft(m[1], e.EffectiveAnswerKey(), startWalkEnd,
		fmt.Sprintf("entry %q is blank and the label asks for %q without one", e.ID, m[1]))
	d.Deterministic = true
	return d, true
}

const startWalkEnd = 0.95

// finalize maps a draft to a result: the reported confidence is the smallest of
// the starting confidence, the bound option's score, the entry's own cap and
// 0.89 for an unconfirmed or stale entry; then the bands, the review lift and
// the entry's floor decide the outcome.
func (s *Step) finalize(d Draft, classified *Entry, defaultStart float64) Result {
	e := d.Entry
	if e == nil {
		e = classified
	}
	s.trace = append(s.trace, d.Trace...)
	if d.Abstain != nil {
		return s.abstainResult(*d.Abstain, e)
	}
	r := Result{Selector: s.Raw.Selector, Answer: d.Answer, ChosenLabel: d.ChosenLabel, ChosenLabels: d.ChosenLabels, Alternatives: d.Alternatives, Source: d.Source}
	if r.Source == "" {
		r.Source = SourceEntry
	}
	if e != nil {
		r.EntryID, r.AnswerKey = e.ID, e.EffectiveAnswerKey()
	}
	conf := d.Start
	if conf == 0 {
		conf = defaultStart
	}
	if d.HasOption && d.OptionScore < conf {
		conf = d.OptionScore
	}
	floor := ReviewFillConfidence
	if e != nil {
		if e.Confidence != nil && *e.Confidence < conf {
			conf = ClampConfidence(*e.Confidence)
		}
		if (e.NeedsReview || e.stale() || s.reviewOnly) && conf > FuzzyConfidenceCap {
			conf = FuzzyConfidenceCap
		}
		if f, ok := e.Floor(); ok {
			floor = f
		}
	}
	conf = ClampConfidence(conf)
	outcome, ok := OutcomeForConfidence(conf)
	if !ok {
		lifted := false
		textish := s.Q.Kind == KindText || !hasRenderedOptions(s.Q.Options)
		weakStage := s.Class.stage == stageFuzzy || s.Class.stage == stageTopic
		switch {
		case d.Deterministic && textish && weakStage && bareYesNoFill.MatchString(d.Answer):
			// A bare Yes or No typed into a box says no more than a Yes/No
			// pair: AssemblyAI's "Have you worked with financial or
			// operational data before?" took the prior-interaction "No".
			s.Tracef("a bare Yes or No adds no evidence to a weak match (%.2f): not lifted", conf)
		case d.Deterministic && textish:
			lifted = true
			s.Tracef("the answer is certain while the phrasing match is weak (%.2f): fill for review", conf)
		case d.HasOption && d.OptionScore >= ClearWinnerFloor && leadsBy(d.OptionScore-d.OtherValueScore, ClearWinnerLead):
			if s.Q.InputType == InputYesNo && (s.Class.stage == stageFuzzy || s.Class.stage == stageTopic) {
				// On a Yes/No pair the winning option only restates the entry's
				// own Yes or No: it says nothing about whether a weakly matched
				// question is the entry's.
				s.Tracef("a Yes/No pair adds no evidence to a weak match (%.2f): not lifted", conf)
				break
			}
			lifted = true
			s.Tracef("clear winner among the options (%.2f) while the match is weak: fill for review", d.OptionScore)
		}
		if lifted {
			conf, outcome = ReviewFillConfidence, OutcomeFilledReview
		}
		ok = lifted
	}
	if ok && conf < floor {
		ok = false
		s.Tracef("confidence %.2f is below the entry's floor %.2f", conf, floor)
	}
	if !ok {
		return s.abstainResult(Abstain{Reason: ReasonLowConfidence}, e)
	}
	r.Outcome, r.Confidence = outcome, conf
	if s.presumedUS {
		r.Detail = DetailPostingCountryPresumed
	}
	r.Trace = s.trace
	return r
}

// budgetResult is the abstain of a control the request ran out of budget for.
func budgetResult(selector string) Result {
	return Result{Selector: selector, Outcome: OutcomeAbstained, Reason: ReasonBudgetExhausted,
		Trace: []string{"request budget exhausted before this control was matched -> abstain"}}
}
