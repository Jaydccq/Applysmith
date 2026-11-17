package apply

import (
	"context"
	_ "embed"
	"encoding/json"
	"strings"
	"time"

	"applysmith/internal/captcha"
	"applysmith/internal/gates"
)

//go:embed icims.js
var icimsJS string

// icimsStepGate is what the page script reads of the step: whether it is
// there, whether it stands in a frame, and what Next reads.
type icimsStepGate struct {
	Found  bool     `json:"found"`
	Framed bool     `json:"framed"`
	Labels []string `json:"labels"`
}

// icimsState is the step's look: whether it still stands and the captcha
// widgets of every same-origin document.
type icimsState struct {
	Here    bool        `json:"here"`
	Widgets []CaptchaW  `json:"widgets"`
	Resp    []RespField `json:"respFields"`
}

func icimsExpr(cmd string, arg any) string {
	a, _ := json.Marshal(arg)
	c, _ := json.Marshal(cmd)
	return "(" + strings.TrimSpace(icimsJS) + ")(" + string(c) + "," + string(a) + ")"
}

func icimsFind(ctx context.Context, t Tab) (icimsStepGate, error) {
	var g icimsStepGate
	err := t.Eval(ctx, icimsExpr("find", nil), &g)
	return g, err
}

func icimsLook(ctx context.Context, t Tab) (icimsState, error) {
	var s icimsState
	err := t.Eval(ctx, icimsExpr("state", nil), &s)
	return s, err
}

// icimsSink puts a token into the step's form, which may stand in a frame the
// page probe does not reach.
var icimsSink = tokenSink{
	put: func(ctx context.Context, t Tab, kind, token string) (delivered, error) {
		var d delivered
		err := t.Eval(ctx, icimsExpr("deliver", map[string]string{"kind": kind, "token": token}), &d)
		return d, err
	},
	blank: func(ctx context.Context, t Tab, kind string) {
		_ = t.Eval(ctx, icimsExpr("blank", kind), nil)
	},
}

// euOrUK lists the countries whose residents tick iCIMS's EU box (its own text
// asks about the European Union or the United Kingdom).
var euOrUK = map[string]bool{}

func init() {
	for _, c := range strings.Split("austria,belgium,bulgaria,croatia,cyprus,czechia,czech republic,denmark,estonia,finland,france,germany,greece,hungary,ireland,italy,latvia,lithuania,luxembourg,malta,netherlands,the netherlands,poland,portugal,romania,slovakia,slovenia,spain,sweden,united kingdom,uk,great britain,england,scotland,wales,northern ireland,at,be,bg,hr,cy,cz,dk,ee,fi,fr,de,gr,hu,ie,it,lv,lt,lu,mt,nl,pl,pt,ro,sk,si,es,se,gb", ",") {
		euOrUK[c] = true
	}
}

// isEUOrUK reads the profile's country: no country written is not residence.
func isEUOrUK(country string) bool { return euOrUK[strings.ToLower(strings.TrimSpace(country))] }

// icimsApplicant is the profile's email and country, the only two facts the
// step uses.
func (s *session) icimsApplicant(ctx context.Context) (email, country string) {
	a, _ := s.r.applicant(ctx)
	return strings.TrimSpace(a.Candidate.Email), a.Candidate.Address.Country
}

// icimsChallenge is the puzzle standing on the step, read with the same bars
// as any page challenge. It is never the whole page: the step is a form.
func icimsChallenge(st icimsState) *Detected {
	for _, w := range st.Widgets {
		if standing(w) {
			return &Detected{Kind: w.Kind, SiteKey: w.SiteKey}
		}
	}
	return nil
}

// icimsStep submits iCIMS's email-first step once per walk, on the tab that
// clicked through to it (a fresh tab at the same address is refused by the
// tenant): the profile email, the EU box only for an EU or UK country, no
// password. The puzzle Next raises is for the run's solver (evidence point 4)
// or else for the user. acted is true when the step left the page.
func (w *walker) icimsStep(ctx context.Context, st *PageState) (res *walkResult, stp *stop, acted bool) {
	if w.icimsDone {
		return nil, nil, false
	}
	g, err := icimsFind(ctx, w.s.tab)
	if err != nil || !g.Found {
		return nil, nil, false
	}
	w.icimsDone = true
	if look, err := icimsLook(ctx, w.s.tab); err == nil {
		if d := icimsChallenge(look); d != nil {
			res, stp = w.endBot(st, "a captcha stands on iCIMS's email step ("+d.Kind+") and is yours")
			return res, stp, false
		}
	}
	email, country := w.s.icimsApplicant(ctx)
	if email == "" {
		return nil, &stop{class: ClassAnswerGap, reason: "iCIMS's email step asks for an email and the profile holds none"}, false
	}
	d := gates.PressControl(gates.ControlInput{Unattended: w.e.unattended(), Stage: gates.StageWalk, Found: true, Labels: g.Labels})
	if aerr := w.r.audit(w.e, d, st.URL, false); aerr != nil {
		return nil, &stop{class: ClassEnvironment, reason: aerr.Error()}, false
	}
	if !d.Allowed() {
		return nil, &stop{class: ClassDefect, reason: "control not pressed: " + d.Reason}, false
	}
	var pressed string
	if err := w.s.tab.Eval(ctx, icimsExpr("press", map[string]any{"email": email, "eu": isEUOrUK(country)}), &pressed); err != nil {
		pressed = "threw"
	}
	if pressed != "pressed" {
		why := map[string]string{"disabled": "it stayed disabled after the email was typed", "gone": "the step left the page"}[pressed]
		if why == "" {
			why = "the press failed"
		}
		return &walkResult{End: EndStalled, Detail: "iCIMS's email step: Next could not be pressed (" + why + ")", Last: st}, nil, false
	}
	return w.icimsSettle(ctx, st)
}

// icimsSettle watches the step after Next: the puzzle opens over it or the
// page moves on. A token is delivered into the step's form and the step is
// sent the way the tenant's own callback sends it; a puzzle standing again
// after that is the token refused.
func (w *walker) icimsSettle(ctx context.Context, st *PageState) (*walkResult, *stop, bool) {
	settle := w.tm.PressWait
	deadline := time.Now().Add(settle)
	var sentUntil time.Time
	var last *captcha.Solution
	for {
		if ctx.Err() != nil {
			return nil, cancelledStop(StageFill), false
		}
		look, err := icimsLook(ctx, w.s.tab)
		if err == nil && !look.Here {
			return nil, nil, true
		}
		if err == nil && !time.Now().Before(sentUntil) {
			if d := icimsChallenge(look); d != nil {
				sol, ok := w.icimsSolve(ctx, d, last)
				if !ok {
					res, stp := w.endBot(st, "a captcha stands on iCIMS's email step ("+d.Kind+") and is yours")
					return res, stp, false
				}
				var sent bool
				if err := w.s.tab.Eval(ctx, icimsExpr("send", nil), &sent); err != nil || !sent {
					res, stp := w.endBot(st, "a captcha stands on iCIMS's email step ("+d.Kind+") and is yours")
					return res, stp, false
				}
				last = &sol
				sentUntil = time.Now().Add(settle)
				deadline = sentUntil
				continue
			}
		}
		if !time.Now().Before(deadline) {
			return &walkResult{End: EndStalled, Detail: "iCIMS's email step was still showing after Next, with no captcha open", Last: st}, nil, false
		}
		if !w.sleep(ctx, w.tm.Poll) {
			return nil, cancelledStop(StageFill), false
		}
	}
}

// icimsSolve asks the run's solver for the step's puzzle: the first time at
// evidence point 4, and for a puzzle standing again after a token was sent as
// that token refused, inside the run's cap.
func (w *walker) icimsSolve(ctx context.Context, d *Detected, prev *captcha.Solution) (captcha.Solution, bool) {
	if prev != nil {
		return w.r.retryVia(ctx, w.e, w.s, d, *prev, icimsSink)
	}
	return w.r.solveVia(ctx, w.e, w.s, d, gates.PointICIMSEmail, icimsSink)
}
