// Clean-room: docs/provenance.md#CR-EVAL-RUBRIC

package evaluate

import (
	"context"
	"fmt"
	"strings"

	"applysmith/internal/tracker"
)

// screen runs the local prechecks and, when none fires, the quick-screen call.
// more is true when the job goes on to the deep evaluation.
func (j *jobRun) screen(ctx context.Context) (res Result, more bool, err error) {
	if err := j.phase(ctx, PhaseScreening); err != nil {
		return Result{}, false, err
	}
	ct := j.prof.CompTarget()
	minimum := ct.Minimum
	if ct.Currency != "" && ct.Currency != "USD" {
		minimum = 0
	}
	pre := Prechecks(PrecheckInput{
		Stance: j.stance, Need: j.need, Title: j.p.Title, Label: j.p.Seniority,
		LimitsSeniority: j.prof.limitsSeniority(), Compensation: j.p.Compensation, Text: j.p.Text, Minimum: minimum,
	})
	if len(pre) > 0 {
		body := ScreenBody{Decision: DecisionDrop, Blockers: pre, Score: PrecheckScore(pre), Local: true,
			Summary: "Dropped by a local check before any model was called.", Facts: j.facts()}
		res, err = j.writeScreening(ctx, body, LegitUnassessed)
		return res, false, err
	}

	data := promptData{Posting: j.postingView(screenWindow), Profile: j.prof.view(escapeText), Sponsor: j.sponsorView()}
	prompt, err := j.render("quick-screen", data)
	if err != nil {
		return Result{}, false, err
	}
	var out ScreenOutput
	if err := j.call(ctx, "quick-screen", prompt, ScreenSchema(), &out); err != nil {
		return Result{}, false, err
	}
	hard, concerns := SplitBlockers(out.Blockers, j.need)
	decision := Decide(out.Score, out.Decision, out.ReviewReason, hard)
	j.detail["screen"] = map[string]any{"score": out.Score, "decision": decision, "proposed": out.Decision}
	if decision == DecisionAdvance && j.req.Mode == ModeAuto {
		return Result{}, true, nil
	}
	if decision == DecisionReview {
		j.detail["review_reason"] = out.ReviewReason
		// A review asks nobody: the deep evaluation answers it in this job, in
		// every mode, unless the day's spend cap holds it back (F-8).
		if !j.e.d.CapReached(ctx) {
			return Result{}, true, nil
		}
	}
	body := ScreenBody{
		Decision: decision, Summary: out.Summary, Blockers: hard, Reasons: out.Reasons,
		Concerns: append(concerns, out.Concerns...), Score: round1(clamp(out.Score, 0, 5)), Facts: j.facts(),
	}
	res, err = j.writeScreening(ctx, body, out.Legitimacy)
	if res.Review {
		// The reason is what the user decides on, and the cap is why they are
		// asked: both are kept with the outcome.
		res.ReviewReason, res.Explanation = out.ReviewReason, CapHeldReview
		j.detail["explanation"] = CapHeldReview
	}
	return res, false, err
}

// CapHeldReview says why a review waits for the user: the day's model spend
// limit holds its deep evaluation back, and the inbox drain takes it the next
// day (F-8).
const CapHeldReview = "The daily model spend cap or an evaluation task's budget is reached; the inbox evaluation evaluates this posting in depth after midnight."

func (j *jobRun) facts() string {
	var parts []string
	for _, kv := range [][2]string{{"Title", j.p.Title}, {"Company", j.p.Company}, {"Location", j.p.Location}, {"Compensation", j.p.Compensation}} {
		if strings.TrimSpace(kv[1]) != "" {
			parts = append(parts, fmt.Sprintf("- %s: %s", kv[0], line(kv[1])))
		}
	}
	parts = append(parts, j.sponsorText())
	return strings.Join(parts, "\n")
}

func (j *jobRun) writeScreening(ctx context.Context, body ScreenBody, legit string) (Result, error) {
	if err := j.phase(ctx, PhaseWriting); err != nil {
		return Result{}, err
	}
	rec := Recommend(body.Score)
	if body.Decision == DecisionDrop {
		rec = RecSkip
	}
	outcome := tracker.OutcomeRecommended
	switch {
	case body.Decision == DecisionDrop:
		outcome = tracker.OutcomeDrop
	case body.Decision == DecisionReview:
		outcome = tracker.OutcomeReview
	case rec == RecSkip:
		outcome = tracker.OutcomeSkip
	}
	score := body.Score
	note := BlockerNote(body.Blockers)
	n, err := j.persist(ctx, reportOut{
		meta:     j.meta(KindScreening, &score, string(tracker.ScoreScreening), rec, legit),
		markdown: func(m ReportMeta) string { return ScreeningMarkdown(m, body) },
		company:  j.p.Company, role: j.p.Title, score: &score, scoreKind: tracker.ScoreScreening, outcome: outcome, note: note,
	})
	if err != nil {
		return Result{}, err
	}
	return Result{Outcome: OutcomeScreened, ReportNumber: n, Decision: body.Decision, Score: &score, Recommendation: rec,
		Review: body.Decision == DecisionReview}, nil
}
