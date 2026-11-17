package match

import "fmt"

// Classification stages, in the names of the specification.
const (
	stageCuratedVerbatim = "C1"
	stageCuratedLong     = "C2"
	stageSignature       = "G1"
	stageRoute           = "G2"
	stageVerbatim        = "G3"
	stageFuzzy           = "G4"
	stageOptionOnly      = "G5"
	stageTopic           = "G6"
	// stagePinned is an entry the caller named (MatchPinned), not one the
	// classification stages found.
	stagePinned = "pinned"
)

// Starting confidence by stage.
const (
	startCuratedVerbatim = 1.00
	startPattern         = 0.95
	startTopicLane       = 0.80
)

// classification is the outcome of the classification stages: at most one
// entry, how it was reached and its starting confidence.
type classification struct {
	entry *Entry
	stage string
	via   string
	start float64
	// tag is the route's variant tag, for the resolver stages.
	tag string
	// abstain is set when no entry was chosen: ambiguous after a fuzzy race.
	abstain *Abstain
	// rejected means the entry matched but the control cannot accept it.
	rejected bool
	trace    []string
}

type classifier struct {
	ix     *classIndex
	q      Question
	routes []compiledRoute
	// vocab is the reworded form of the question for the second route sweep.
	vocab string
	// binds remembers whether an entry's ranked values bind a rendered option:
	// the answer cannot change within one question.
	binds map[*Entry]bool
	// shapes remembers whether the control's shape accepts an entry
	// (acceptsControl), which cannot change within one question either.
	shapes map[*Entry]bool
	// sigs are the form signatures that fire for the control, read once.
	sigs     []signature
	sigsRead bool
	// scorer holds the option profiles and scores of this control.
	scorer *optionScorer
}

func newClassifier(ix *classIndex, q Question, sc *optionScorer) *classifier {
	if sc == nil {
		sc = newOptionScorer(q.Text, nil)
	}
	c := &classifier{ix: ix, q: q, routes: routeCatalog(), scorer: sc}
	c.vocab, _ = routeVocabularyVariant(q.Text)
	return c
}

// accepts is the first-pass filter: the control accepts the entry's shape and
// the entry's preferred values bind a rendered option. Once the request's
// budget is spent it accepts nothing, so a control stops mid-walk and the
// request replaces its result with the budget abstain.
func (c *classifier) accepts(e *Entry) (bool, string) {
	if c.scorer.bud.over() {
		return false, "request budget spent"
	}
	shaped, seen := c.shapes[e]
	if !seen {
		shaped = acceptsControl(e, c.q).OK
		if c.shapes == nil {
			c.shapes = map[*Entry]bool{}
		}
		c.shapes[e] = shaped
	}
	if !shaped {
		return false, fmt.Sprintf("cannot accept inputType=%s", c.q.InputType)
	}
	bound, seen := c.binds[e]
	if !seen {
		bound = canBind(e, c.q, c.scorer)
		if c.binds == nil {
			c.binds = map[*Entry]bool{}
		}
		c.binds[e] = bound
	}
	if !bound {
		return false, "pick-option entry binds none of the rendered options"
	}
	return true, ""
}

// signatures lists the form signatures that fire for the control; both passes
// of the general walk share one reading of the options.
func (c *classifier) signatures() []signature {
	if !c.sigsRead {
		c.sigs = signatureCandidates(c.q, func(id string) bool { return c.lookup(id) != nil }, c.scorer)
		c.sigsRead = true
	}
	return c.sigs
}

func (c *classifier) lookup(id string) *Entry {
	i, ok := c.ix.byID[id]
	if !ok && id == idYearsExperienceTotal {
		// With no total-years entry of their own, the full-time years the user
		// wrote are their total (ZipRecruiter's "years of professional
		// experience in software engineering"), as totalYears reads them.
		if j, has := c.ix.byID[IDYearsFullTimeWork]; has && c.ix.entries[j].UserWritten() && !c.ix.entries[j].Placeholder && c.ix.entries[j].EffectiveState() == StateActive {
			i, ok = j, true
		}
	}
	if !ok || !c.ix.entries[i].classifiable() {
		return nil
	}
	return &c.ix.entries[i]
}

func (c *classifier) accepted(e *Entry) bool { ok, _ := c.accepts(e); return ok }

// curated runs the curated stages: C1 (verbatim, then heading-stripped) and C2
// (the long-example lane). Both skip an entry the control cannot accept.
func (c *classifier) curated() (classification, bool) {
	if i, heading, ok := verbatimMatch(c.ix, c.q.Text, true, true, c.accepted); ok {
		via := "curated example"
		if heading {
			via = "curated example after the heading was stripped"
		}
		return classification{entry: &c.ix.entries[i], stage: stageCuratedVerbatim, via: via, start: startCuratedVerbatim}, true
	}
	if i, cov, ok := longExampleMatch(c.ix, c.q.Text, c.accepted); ok {
		return classification{entry: &c.ix.entries[i], stage: stageCuratedLong, start: startPattern,
			via: fmt.Sprintf("curated long example coverage=%.2f", cov)}, true
	}
	return classification{}, false
}

// general runs the general stages with control-aware selection: a first pass
// that skips entries the control cannot take, and, only when that finds
// nothing, an unfiltered pass of the rule stages (no fuzzy resemblance) that
// prefers a matching form signature so an inapplicable entry ends in the
// input-mismatch abstain with the entry attached.
func (c *classifier) general() classification {
	first := c.pass(true)
	if first.entry != nil || first.abstain != nil {
		return first
	}
	second := c.pass(false)
	second.trace = append(first.trace, second.trace...)
	return second
}

func (c *classifier) pass(filtered bool) classification {
	var trace []string
	ok := func(e *Entry) bool { return !filtered || c.accepted(e) }
	finish := func(cl classification) classification {
		cl.trace = append(trace, cl.trace...)
		if cl.entry != nil {
			cl.rejected = !c.accepted(cl.entry)
		}
		return cl
	}
	skip := func(e *Entry, why string) {
		trace = append(trace, fmt.Sprintf("input-type-aware walk: skipped %q (%s)", e.ID, why))
	}

	// G1: form signature, whenever the control renders options. A signature
	// that owns its question keeps it on its entry even when the entry cannot
	// take the control.
	for _, sig := range c.signatures() {
		e := c.lookup(sig.Target)
		if good, why := c.accepts(e); filtered && !good {
			skip(e, why)
			if !sig.Owns {
				continue
			}
		}
		return finish(classification{entry: e, stage: stageSignature, via: "signature:" + sig.Target, start: startPattern})
	}

	// G2: pattern routes.
	walk := walkRoutes(c.routes, c.q.Text, c.vocab, routeEnv{
		hasOptions: hasRenderedOptions(c.q.Options),
		lookup:     c.lookup,
		accepts: func(e *Entry) (bool, string) {
			if !filtered {
				return true, ""
			}
			return c.accepts(e)
		},
	})
	for _, s := range walk.Skipped {
		trace = append(trace, fmt.Sprintf("input-type-aware walk: skipped %q (route %d: %s)", s.ID, s.Priority, s.Why))
	}
	if walk.Hit != nil {
		return finish(classification{entry: c.lookup(walk.Hit.Target), stage: stageRoute, tag: walk.Hit.Tag,
			via: fmt.Sprintf("route:%d", walk.Hit.Priority), start: startPattern})
	}

	// G3: verbatim match of a non-curated entry's example.
	if i, _, hit := verbatimMatch(c.ix, c.q.Text, false, false, ok); hit {
		return finish(classification{entry: &c.ix.entries[i], stage: stageVerbatim, via: "example-exact", start: startPattern})
	}

	// G4: fuzzy match. The unfiltered pass attaches an entry the control cannot
	// take only on a rule's evidence: a resemblance is too weak to hold the
	// question away from a cited answer (Tebra's production-operations Yes/No
	// took the data platforms list at a question coverage of 0.13).
	include := func(i int) bool {
		e := &c.ix.entries[i]
		return filtered && !walk.Blocked[e.ID] && ok(e)
	}
	fz := c.ix.fuzzyPick(c.q.Text, c.ix.scoreEntries(c.q.Text, walk.Released, include))
	// A required control whose one option acknowledges has no other reading,
	// whatever the statement above it resembles (TRM's "High Performance
	// Environment ... a truly global business" over its one "I have read and
	// understand ..." option took the work country at a question coverage of
	// 0.03); after an ambiguous race it stays ambiguous, as below.
	if !fz.tie && len(c.q.Options) <= 3 && len(realOptions(c.q.Options)) == 1 && ackByOption(c.q) {
		if e := c.lookup("mandatory-acknowledge"); e != nil && ok(e) {
			return finish(classification{entry: e, stage: stageOptionOnly, start: startPattern, via: "acknowledgement phrase in the lone option"})
		}
	}
	switch {
	case fz.found:
		start := fz.score
		if start > FuzzyConfidenceCap {
			start = FuzzyConfidenceCap
		}
		return finish(classification{entry: &c.ix.entries[fz.idx], stage: stageFuzzy,
			via: fmt.Sprintf("fuzzy score=%.2f qcov=%.2f", fz.score, float64(fz.inter)/float64(len(fuzzyTokens(c.q.Text)))), start: start})
	case fz.different:
		trace = append(trace, "fuzzy winner is about a different subject; dropped")
	}

	// G5: option-only routing, and G6 as the last stage and the tie rescue. A
	// lone box whose one option is the acknowledgement has no other reading,
	// whatever the race over the notice above it decided (Cirrus Logic's export
	// control notice over "By checking this box, I confirm ...").
	loneBox := c.q.Kind == KindCheckbox && len(realOptions(c.q.Options)) == 1
	if cl, hit := c.optionOnly(!fz.tie || loneBox, ok); hit {
		return finish(cl)
	}
	if i, hit := c.ix.topicLane(c.q.Text, func(e *Entry) bool { return !walk.Blocked[e.ID] && ok(e) }); hit {
		return finish(classification{entry: &c.ix.entries[i], stage: stageTopic, via: "curated topic lane", start: startTopicLane})
	}
	if fz.tie {
		return finish(classification{abstain: &Abstain{Reason: ReasonAmbiguous},
			trace: []string{"fuzzy classification remained within the margin of the runner-up"}})
	}
	return finish(classification{})
}

// optionOnly routes a question whose options form a single-axis disability or
// veteran self-identification set, and (except after an ambiguous race) a
// required question with an unnegated acknowledgement option.
func (c *classifier) optionOnly(allowAck bool, ok func(*Entry) bool) (classification, bool) {
	if id := axisEntryID(selfIDAxis(c.q.Options, c.scorer)); id != "" {
		if e := c.lookup(id); e != nil && ok(e) {
			return classification{entry: e, stage: stageOptionOnly, start: startPattern,
				via:   "options are a single-axis self-ID set",
				trace: []string{fmt.Sprintf("question text names no axis, but the options do: rescued to entry id=%s", id)}}, true
		}
	}
	if allowAck && ackByOption(c.q) {
		if e := c.lookup("mandatory-acknowledge"); e != nil && ok(e) {
			return classification{entry: e, stage: stageOptionOnly, start: startPattern,
				via: "acknowledgement phrase in an option"}, true
		}
	}
	return classification{}, false
}

// describe renders the classification trace line.
func (cl classification) describe(q Question) string {
	if cl.entry == nil {
		return "no entry classified"
	}
	return fmt.Sprintf("classified -> entry id=%s kind=%s via %s (stage %s) control=%s/%s",
		cl.entry.ID, cl.entry.Kind, cl.via, cl.stage, q.Kind, q.InputType)
}
