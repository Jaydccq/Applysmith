// Clean-room: docs/provenance.md#CR-EVAL-RUBRIC

package evaluate

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"unicode"

	"applysmith/internal/gates"
	"applysmith/internal/tracker"
)

// answersFrom is the score from which the application answers are drafted.
const answersFrom = StrongFrom

func (j *jobRun) deep(ctx context.Context) (Result, error) {
	if err := j.phase(ctx, PhaseEvaluating); err != nil {
		return Result{}, err
	}
	signals := Signals(j.p.Text, j.p.Company, j.p.URL, j.ageDays(), j.tier, j.atsNative())
	data := promptData{
		Posting: j.postingView(0), Profile: j.prof.view(escapeText), Sponsor: j.sponsorView(), Signals: signals.Texts(),
	}
	if j.e.d.Knowledge != nil {
		for _, s := range j.e.d.Knowledge(ctx, j.p.Text) {
			data.Knowledge = append(data.Knowledge, Snippet{ID: s.ID, Text: escapeText(s.Text)})
		}
	}
	prompt, err := j.render("deep-eval", data)
	if err != nil {
		return Result{}, err
	}
	var raw json.RawMessage
	if err := j.call(ctx, "deep-eval", prompt, DeepSchema(), &raw); err != nil {
		return Result{}, err
	}
	var out DeepOutput
	if err := json.Unmarshal(raw, &out); err != nil {
		return Result{}, j.e.fail(ReasonInvalid, err)
	}
	j.logIgnored(raw)

	company := displayCompany(j.p.Company, out.Posting.Company)
	if j.p.Company == "" {
		j.setTier(company)
	}
	role := firstNonEmpty(j.p.Title, out.Posting.Title)

	ck := Check(out, CheckContext{Stance: j.stance, Need: j.need, ProfileYears: j.prof.Years(j.e.d.Now())})
	rng := j.compRange(out)
	target := j.prof.CompTarget()
	comp := CompensationScore(rng, target)
	notes := append([]Cap(nil), ck.Demotions...)
	for _, id := range ck.Unsupported {
		notes = append(notes, Cap{CapUnsupportedHit, 5.0, id + ": the evidence names no profile source"})
	}
	in := ScoreInput{
		Must: Levels(ck.Must), Nice: Levels(ck.Nice), Hidden: Levels(ck.Hidden), Align: Levels(ck.Align),
		RedFlags: FlagLevels(ck.RedFlags), Comp: comp, MustMisses: ck.MustMisses, HonoredGate: ck.HonoredGate,
		WorkAuthGate: j.need == NeedNeeded && (j.stance == gates.StanceRestricted || j.stance == gates.StanceRefuses),
		TierDelta:    j.delta, Notes: notes,
	}
	sc := Compute(in)
	rec := Recommend(sc.Score)
	legit := ApplyLegitimacy(out.Legit.Level, signals)
	j.detail["score"] = sc.Score

	var answers []Answer
	answersNote := ""
	switch {
	case tenths(sc.Score) < tenths(answersFrom):
		answersNote = fmt.Sprintf("omitted (score below %.1f)", answersFrom)
	default:
		answers, err = j.answers(ctx, out, sc)
		if err != nil {
			if ctx.Err() != nil {
				return Result{}, err
			}
			j.e.d.Log.Warn("evaluate: the answers call failed", "run", j.runID, "err", err)
			answersNote, answers = "unavailable (the follow-up call failed)", nil
		}
	}

	if err := j.phase(ctx, PhaseWriting); err != nil {
		return Result{}, err
	}
	meta := j.meta(KindFinal, &sc.Score, string(tracker.ScoreFinal), rec, legit)
	meta.CompanySize, meta.RoleFamily, meta.AnswersNote = out.Posting.CompanySize, out.Posting.RoleFamily, answersNote
	meta.Caps = j.capsLine(sc)
	body := FinalBody{
		Out: out, Checked: ck, Input: in, Card: sc, Pay: payText(rng, target, comp), StatedPay: statedPay(out.Posting.Compensation.Text, rng), Sponsor: j.sponsorText(),
		Years: j.prof.Years(j.e.d.Now()), Targets: j.prof.view(line).Targets,
		Signals: append(out.Legit.Signals, signals.Texts()...), Answers: answers, Doubtful: legit == "doubtful",
	}
	outcome := tracker.OutcomeRecommended
	if rec == RecSkip {
		outcome = tracker.OutcomeSkip
	}
	score := sc.Score
	n, err := j.persist(ctx, reportOut{
		meta: meta, markdown: func(m ReportMeta) string { return FinalMarkdown(m, body) },
		company: company, role: role, score: &score, scoreKind: tracker.ScoreFinal, outcome: outcome,
		docs: DocumentsEligible(sc.Score, true),
	})
	if err != nil {
		return Result{}, err
	}
	return Result{Outcome: OutcomeReported, ReportNumber: n, Decision: "reported", Score: &score, Recommendation: rec}, nil
}

func (j *jobRun) answers(ctx context.Context, out DeepOutput, sc Scorecard) ([]Answer, error) {
	summary := fmt.Sprintf("score %.1f; must-haves: %s", sc.Score, labels(out.Must))
	data := promptData{Posting: j.postingView(0), Profile: j.prof.view(escapeText), Evaluation: escapeText(summary)}
	prompt, err := j.render("deep-answers", data)
	if err != nil {
		return nil, err
	}
	var ans AnswersOutput
	if err := j.call(ctx, "deep-eval", prompt, AnswersSchema(), &ans); err != nil {
		return nil, err
	}
	return ans.Answers, nil
}

func labels(items []Item) string {
	var l []string
	for _, it := range items {
		l = append(l, line(it.Label))
	}
	return strings.Join(l, "; ")
}

// displayCompany is the company name the report and the tracker show. A
// lower-case one-word name, as a board's slug is ("palantir", "1password"),
// gives way to the name the posting states when that name contains it.
func displayCompany(read, stated string) string {
	read, stated = strings.TrimSpace(read), strings.TrimSpace(stated)
	if read == "" || (stated != "" && read == strings.ToLower(read) && !strings.Contains(read, " ") &&
		squashName(read) != "" && strings.Contains(squashName(stated), squashName(read))) {
		return stated
	}
	return read
}

// squashName is a name's letters and digits, lower-cased.
func squashName(s string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			return unicode.ToLower(r)
		}
		return -1
	}, s)
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v = strings.TrimSpace(v); v != "" {
			return v
		}
	}
	return ""
}

// logIgnored notes the model-supplied fields that the application owns.
func (j *jobRun) logIgnored(raw json.RawMessage) {
	var top map[string]json.RawMessage
	if json.Unmarshal(raw, &top) != nil {
		return
	}
	var ignored []string
	for _, k := range modelOnlyKeys {
		if _, ok := top[k]; ok {
			ignored = append(ignored, k)
		}
	}
	if len(ignored) > 0 {
		j.detail["ignored_fields"] = ignored
		j.e.d.Log.Warn("evaluate: ignored model-supplied fields", "run", j.runID, "fields", ignored)
	}
}

func (j *jobRun) ageDays() int {
	if j.req.PostedAt.IsZero() {
		return 0
	}
	return int(j.e.d.Now().Sub(j.req.PostedAt).Hours() / 24)
}

func (j *jobRun) atsNative() bool {
	_, ok := identityATS(j.p.URL)
	return ok
}

// compRange prefers the range parsed from the stated text; the model's
// extracted figures are the fallback.
func (j *jobRun) compRange(out DeepOutput) *gates.Range {
	if r, ok := gates.ParseCompensation(j.p.Compensation + " " + j.p.Text); ok {
		return &r
	}
	c := out.Posting.Compensation
	if c.Maximum == nil || *c.Maximum <= 0 || (c.Minimum != nil && *c.Minimum > *c.Maximum) {
		return nil
	}
	// Preserve an explicit currency even when it differs from the target;
	// scoring refuses that comparison and the Pay section explains it.
	if strings.TrimSpace(c.Currency) == "" {
		return nil
	}
	mult := 1.0
	switch strings.ToLower(c.Period) {
	case "year", "":
	case "hour":
		mult = gates.HoursPerYear
	case "month":
		mult = gates.MonthsPerYear
	default:
		return nil
	}
	lo := *c.Maximum
	if c.Minimum != nil {
		lo = *c.Minimum
	}
	r := &gates.Range{Low: lo * mult, High: *c.Maximum * mult, Currency: strings.ToUpper(c.Currency)}
	if r.Placeholder() {
		return nil
	}
	return r
}

// statedPay is the posting's pay as extracted, else the scored range in
// words: a range read from the posting text is stated pay even when the
// extraction missed it.
func statedPay(extracted string, r *gates.Range) string {
	if strings.TrimSpace(extracted) != "" || r == nil {
		return extracted
	}
	return fmt.Sprintf("%s %.0f to %.0f a year", r.Currency, r.Low, r.High)
}

func payText(r *gates.Range, t CompTarget, comp float64) string {
	if r == nil {
		return fmt.Sprintf("No usable compensation range is stated. Compensation score: %.0f (neutral).", comp) + payAnchors(nil, t)
	}
	s := fmt.Sprintf("Stated range (%s), annualized: %.0f to %.0f.", r.Currency, r.Low, r.High)
	if t.Minimum > 0 {
		currency := t.Currency
		if currency == "" {
			currency = "USD"
		}
		s += fmt.Sprintf(" Profile minimum %.0f %s.", t.Minimum, currency)
		if !strings.EqualFold(r.Currency, currency) {
			s += " Different currencies are not compared or converted."
		}
	}
	return s + fmt.Sprintf(" Compensation score: %.0f.", comp) + payAnchors(r, t)
}

// payAnchors is the profile's negotiation anchors, empty without a minimum.
func payAnchors(r *gates.Range, t CompTarget) string {
	s := ""
	if t.Minimum > 0 {
		goal := t.Target
		if goal <= 0 {
			goal = 1.1 * t.Minimum
		}
		currency := t.Currency
		if currency == "" {
			currency = "USD"
		}
		same := r != nil && strings.EqualFold(r.Currency, currency)
		if same && r.Low >= goal {
			s += fmt.Sprintf("\n\nYour anchors (profile): the whole stated range clears your target of %.0f %s (its bottom is %.0f%% of it), so anchor inside the range, not at your target; %.0f %s, the minimum, stays the walk-away floor.",
				goal, currency, 100*r.Low/goal, t.Minimum, currency)
		} else {
			s += fmt.Sprintf("\n\nYour anchors (profile): ask for %.0f %s, the target; %.0f %s, the minimum, is the walk-away floor.", goal, currency, t.Minimum, currency)
			if same {
				s += fmt.Sprintf(" The stated top is %.0f%% of the target and the stated bottom %.0f%%.", 100*r.High/goal, 100*r.Low/goal)
			}
		}
	}
	return s
}

func (j *jobRun) capsLine(sc Scorecard) string {
	var parts []string
	if len(sc.Caps) > 0 {
		parts = append(parts, capsText(sc.Caps))
	}
	if j.delta > 0 {
		parts = append(parts, fmt.Sprintf("employer tier adjustment: -%.1f (%s)", j.delta, j.tier))
	}
	return strings.Join(parts, "; ")
}
