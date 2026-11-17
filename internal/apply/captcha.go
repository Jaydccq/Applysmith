package apply

import (
	"context"
	"errors"
	"slices"
	"time"

	"applysmith/internal/captcha"
	"applysmith/internal/gates"
)

// Detected is a challenge the page shows.
type Detected struct {
	// Kind is a gates.Challenge* value.
	Kind    string
	SiteKey string
	// ChallengeOnly is true when the page is nothing but the widget and a plain
	// button.
	ChallengeOnly bool
}

// fullPage reports whether the page is only a challenge: no fillable field, at
// most two controls and a short text.
func fullPage(st *PageState) bool {
	fields, files := st.FormFields()
	return fields == 0 && files == 0 && len(st.Controls) <= 2 && len(st.Text) < 500
}

// DetectChallenge reads the top document's widgets with the same visibility
// bars as the unsolved-widget probe: a dormant or invisible widget is not a
// challenge, an invisible one whose puzzle is open is. A Turnstile challenge
// that is the whole page is Cloudflare's own full-page interstitial and is
// reported as that kind, never as an embedded Turnstile widget; the same goes
// for a page that speaks the interstitial's copy, except on a listed bot
// manager's validation host, whose copy names the bot manager while its widget
// is a supported one.
func DetectChallenge(st *PageState) *Detected {
	whole := fullPage(st)
	host, _ := parseHost(st.URL)
	if n := BotInterstitial(st.Title, st.Lines); n != "" && (whole || hasKind(st, gates.ChallengeTurnstileWidget)) && !gates.BotManagerHost(host) {
		return &Detected{Kind: gates.ChallengeInterstitial, ChallengeOnly: true}
	}
	for _, w := range st.Captcha {
		if !standing(w) {
			continue
		}
		if w.Kind == gates.ChallengeTurnstileWidget && whole {
			return &Detected{Kind: gates.ChallengeInterstitial, ChallengeOnly: true}
		}
		return &Detected{Kind: w.Kind, SiteKey: w.SiteKey, ChallengeOnly: whole}
	}
	return nil
}

// standing reports whether a widget is a challenge now: a dormant or invisible
// one is not, an invisible one whose puzzle is open is, and reCAPTCHA v3 never
// is.
func standing(w CaptchaW) bool {
	if w.Kind == gates.ChallengeRecaptchaV3 {
		return false
	}
	if !w.Visible && !w.BframeOpen {
		return false
	}
	return !w.Invisible || w.BframeOpen
}

func hasKind(st *PageState, kind string) bool {
	for _, w := range st.Captcha {
		if w.Kind == kind {
			return true
		}
	}
	return false
}

// Unsolved reports whether the detected challenge stands with an empty
// response field.
func unsolved(st *PageState, d *Detected) bool {
	if d == nil {
		return false
	}
	if len(st.Resp) == 0 {
		return true
	}
	for _, f := range st.Resp {
		if f.Empty {
			return true
		}
	}
	return false
}

// delivered is what a token delivery reached.
type delivered struct {
	Fields   int    `json:"fields"`
	Created  bool   `json:"created"`
	Callback string `json:"callback"`
}

// reached reports a delivery that put the token somewhere the page reads it: a
// response field or the widget's callback.
func (d delivered) reached() bool { return d.Fields > 0 || d.Callback != "" }

// deliverToken puts the token into every response field of the widget's kind,
// creating one in the widget's form when the page has none, and calls the
// widget's data-callback when it names a global function.
func deliverToken(ctx context.Context, t Tab, kind, token string) (delivered, error) {
	var d delivered
	err := t.Eval(ctx, probeExpr("deliver", map[string]string{"kind": kind, "token": token}), &d)
	return d, err
}

// blankFields empties the response fields of a kind, as after a token the page
// refused.
func blankFields(ctx context.Context, t Tab, kind string) {
	_ = t.Eval(ctx, probeExpr("blank", kind), nil)
}

// solver returns the run's solver, built on first use.
func (r *Runner) solver(e *Exec) captcha.Solver {
	if e.solver != nil || r.d.Page.Solver == nil {
		return e.solver
	}
	e.solver = r.d.Page.Solver(e, func(d gates.Decision) { _ = r.audit(e, d, e.Run.Input.URL, false) })
	return e.solver
}

func (r *Runner) captchaLines(e *Exec) {
	if e.solver != nil {
		e.Run.Captcha = append(slices.Clip(e.solved), e.solver.Detail()...)
	}
}

// tokenSink is where a token goes and how a refused one is taken back out.
type tokenSink struct {
	put   func(ctx context.Context, t Tab, kind, token string) (delivered, error)
	blank func(ctx context.Context, t Tab, kind string)
}

// pageSink serves the top document.
var pageSink = tokenSink{put: deliverToken, blank: blankFields}

// solveAt asks the run's solver for a token for a challenge at an evidence
// point and delivers it. The answer is false when the challenge stays the
// user's: no solver, a refusal by the gate, a failure of the service, a token
// that reached no response field and no callback.
func (r *Runner) solveAt(ctx context.Context, e *Exec, s *session, d *Detected, point string) (captcha.Solution, bool) {
	return r.solveVia(ctx, e, s, d, point, pageSink)
}

func (r *Runner) solveVia(ctx context.Context, e *Exec, s *session, d *Detected, point string, sink tokenSink) (captcha.Solution, bool) {
	sv := r.solver(e)
	if sv == nil {
		return captcha.Solution{}, false
	}
	defer r.captchaLines(e)
	url, combined := s.pageFacts(ctx)
	sol, err := sv.Solve(ctx, captcha.Challenge{Kind: d.Kind, Point: point, SiteKey: d.SiteKey, PageURL: url, CombinedAccountAndApplication: combined})
	if err != nil {
		var ref *captcha.Refusal
		// A refusal and a failure both leave the challenge to the user.
		if errors.As(err, &ref) && ref.Decision.Code == gates.CodeDemoWorkspace {
			e.demoRefusal = ref.Decision.Code + ": " + ref.Decision.Reason
		}
		return captcha.Solution{}, false
	}
	if got, derr := sink.put(ctx, s.tab, d.Kind, sol.Token()); derr != nil || !got.reached() {
		return captcha.Solution{}, false
	}
	return sol, true
}

// retry handles a token the page did not accept: the fields are blanked and
// at most one more solve is asked inside the run's cap.
func (r *Runner) retry(ctx context.Context, e *Exec, s *session, d *Detected, sol captcha.Solution) (captcha.Solution, bool) {
	return r.retryVia(ctx, e, s, d, sol, pageSink)
}

func (r *Runner) retryVia(ctx context.Context, e *Exec, s *session, d *Detected, sol captcha.Solution, sink tokenSink) (captcha.Solution, bool) {
	sink.blank(ctx, s.tab, d.Kind)
	sv := r.solver(e)
	if sv == nil {
		return captcha.Solution{}, false
	}
	defer r.captchaLines(e)
	next, err := sv.TokenRefused(ctx, sol)
	if err != nil {
		return captcha.Solution{}, false
	}
	if got, derr := sink.put(ctx, s.tab, d.Kind, next.Token()); derr != nil || !got.reached() {
		return captcha.Solution{}, false
	}
	return next, true
}

// pageFacts reads the address of the page the challenge stands on and whether
// that page is a combined account and application form, whose captcha is left
// to the user.
func (s *session) pageFacts(ctx context.Context) (url string, combined bool) {
	if st, err := Probe(ctx, s.tab); err == nil {
		return st.URL, combinedAccountForm(st)
	}
	if s.last != nil {
		return s.last.URL, combinedAccountForm(s.last)
	}
	return "", false
}

// combinedAccountForm reads a page that asks for a new password beside the
// application itself (SuccessFactors and Wellfound quick apply): a password box
// that is not a sign-in wall. The solver is not wired there, so nothing is
// typed or solved and the page is the user's.
func combinedAccountForm(st *PageState) bool {
	for _, in := range st.Inputs {
		if !in.Furniture && in.Type == "password" {
			return findWall(st) == nil
		}
	}
	return false
}

// beforePress clears an unsolved widget that stands on the form before the
// press (evidence point 1): the token goes into the response field and the
// same probe is re-read, then the caller presses.
func (r *Runner) beforePress(ctx context.Context, e *Exec, s *session, st *PageState) (*PageState, bool) {
	d := DetectChallenge(st)
	if d == nil || !unsolved(st, d) {
		return st, true
	}
	if _, ok := r.solveAt(ctx, e, s, d, gates.PointBeforePress); !ok {
		return st, false
	}
	again, err := Probe(ctx, s.tab)
	if err != nil {
		return st, false
	}
	return again, !unsolved(again, DetectChallenge(again))
}

// afterPress clears a puzzle that stands where the confirmation should be
// (evidence point 2): the token is set and the same control is pressed again.
// The press claim is not made twice, and a callback that sent the form itself
// suppresses the second press.
func (r *Runner) afterPress(ctx context.Context, e *Exec, s *session, watch ResponseWatch, press func(context.Context) *stop) bool {
	st, err := Probe(ctx, s.tab)
	if err != nil {
		return false
	}
	d := DetectChallenge(st)
	if d == nil {
		return false
	}
	writes := countWrites(watch)
	sol, ok := r.solveAt(ctx, e, s, d, gates.PointAfterPress)
	if !ok {
		return false
	}
	pressed := false
	// A callback that sends the form itself does so within this wait; a run
	// cancelled meanwhile presses nothing more.
	if !sleepCtx(ctx, 1500*time.Millisecond) {
		return false
	}
	if sentByCallback := countWrites(watch) > writes; !sentByCallback {
		if press(ctx) != nil {
			return false
		}
		pressed = true
	}
	if !sleepCtx(ctx, 800*time.Millisecond) {
		return true
	}
	st2, err := Probe(ctx, s.tab)
	if err == nil && DetectChallenge(st2) != nil {
		if _, ok := r.retry(ctx, e, s, d, sol); ok && pressed {
			_ = press(ctx)
		}
	}
	return true
}

func countWrites(w ResponseWatch) int {
	if w == nil {
		return 0
	}
	n := 0
	for _, r := range w.Responses() {
		if isWrite(r.Method) && !ignoredWrite(r.URL) {
			n++
		}
	}
	return n
}

// nextRaised handles a Next that raised a puzzle and left the page where it was
// (evidence point 3): the token is set and Next is pressed once more; the
// page's own movement is the verdict. A puzzle the run may not or could not
// clear stops the run as a bot check the person clears in the kept tab.
func (r *Runner) nextRaised(ctx context.Context, w *walker, st *PageState, c Control) (bool, *stop) {
	d := DetectChallenge(st)
	if d == nil || !unsolved(st, d) || d.ChallengeOnly {
		return false, nil
	}
	if _, ok := r.solveAt(ctx, w.e, w.s, d, gates.PointNextUnchanged); !ok {
		why := "bot check: a " + d.Kind + " puzzle opened when the page's own control was pressed; it is yours to clear in the kept tab"
		if w.e.demoRefusal != "" {
			why += "; " + w.e.demoRefusal
		}
		return false, &stop{class: ClassVerification, board: "challenge", reason: why}
	}
	moved, stp := w.press(ctx, st, c)
	return moved, stp
}

// challengeOnly clears a page that is a supported widget plus a plain button
// and no field (evidence point 7), then the walk continues. The walk calls it
// only before its first form page. A full-page Cloudflare interstitial is never
// touched: the gate refuses it and the walk ends as a bot check. A button that
// reads as Submit is the walk's to press only when it is the top-document
// button of a listed bot manager's own validation page (gates.BotManagerHosts),
// which cannot be an application; anywhere else no solve is bought for it.
func (r *Runner) challengeOnly(ctx context.Context, w *walker, st *PageState) (bool, *stop) {
	d := DetectChallenge(st)
	if d == nil || !d.ChallengeOnly {
		return false, nil
	}
	var btn *Control
	for i := range st.Controls {
		c := &st.Controls[i]
		if !c.Disabled && !c.Chat {
			btn = c
			break
		}
	}
	if btn == nil {
		return false, nil
	}
	// The exception covers only the bot manager's own button: one in the top
	// document, not one merged in from an embedded frame.
	if host, _ := parseHost(st.URL); gates.ClassifyControl(btn.Labels...) == gates.ControlSubmit && (btn.I >= frameBase || !gates.BotManagerHost(host)) {
		return false, nil
	}
	if _, ok := r.solveAt(ctx, w.e, w.s, d, gates.PointChallengeOnly); !ok {
		return false, nil
	}
	moved, stp := w.pressAs(ctx, st, *btn, gates.StageChallenge)
	return moved, stp
}
