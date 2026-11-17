package onboarding

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"applysmith/internal/browser"
)

// WindowReport is what one browser window said about itself.
type WindowReport struct {
	Lane      string `json:"lane"`
	Attached  bool   `json:"attached"`
	Webdriver *bool  `json:"webdriver,omitempty"`
	UserAgent string `json:"user_agent,omitempty"`
	Error     string `json:"error,omitempty"`
}

// BrowserReport is the result of the fingerprint check of both windows. It is
// stored as a setup decision and holds no page content.
type BrowserReport struct {
	CheckedAt string         `json:"checked_at"`
	Windows   []WindowReport `json:"windows"`
}

const browserCheckKey = "setup.browser-check"

// browserProblems lists what is wrong with the report, one line per window and
// property. The step passes when it is empty.
func browserProblems(r BrowserReport) []string {
	var out []string
	for _, w := range r.Windows {
		switch {
		case !w.Attached:
			msg := "the " + w.Lane + " window could not be attached"
			if w.Error != "" {
				msg += ": " + w.Error
			}
			out = append(out, msg)
		case w.Webdriver == nil:
			out = append(out, "the "+w.Lane+" window did not report navigator.webdriver")
		case *w.Webdriver:
			out = append(out, "the "+w.Lane+" window reports navigator.webdriver true")
		}
		if strings.Contains(strings.ToLower(w.UserAgent), "headless") {
			out = append(out, "the "+w.Lane+" window's user agent contains a headless marker")
		}
	}
	return out
}

func (e *Env) browserReport(ctx context.Context) (BrowserReport, bool) {
	raw, ok := e.decision(ctx, browserCheckKey)
	if !ok {
		return BrowserReport{}, false
	}
	var r BrowserReport
	return r, json.Unmarshal([]byte(raw), &r) == nil
}

func detectBrowser(ctx context.Context, env *Env) Detection {
	r, ok := env.browserReport(ctx)
	if !ok {
		return missing("the apply and scan windows have not been checked")
	}
	var rs []Reason
	for _, p := range browserProblems(r) {
		rs = append(rs, reasonf(ReasonInvalid, "fingerprint", p))
	}
	if len(r.Windows) == 0 {
		rs = append(rs, reasonf(ReasonBlank, "fingerprint", "the check reported no window"))
	}
	return verdict(rs...)
}

func applyBrowser(ctx context.Context, env *Env, raw json.RawMessage, dry bool) (Applied, error) {
	if err := decodeInput(raw, nil, &struct{}{}); err != nil {
		return Applied{}, err
	}
	if dry {
		return Applied{Step: "browser", DryRun: true, Detection: Evaluate(ctx, env, mustStep("browser"))}, nil
	}
	if env.BrowserCheck == nil {
		return Applied{}, &HumanError{Instruction: mustStep("browser").Human}
	}
	r, err := env.BrowserCheck(ctx)
	if err != nil {
		return Applied{}, &EnvError{Message: "the browser windows could not be checked", Err: err}
	}
	r.CheckedAt = env.now().UTC().Format(time.RFC3339)
	b, _ := json.Marshal(r)
	if err := env.setDecision(ctx, browserCheckKey, string(b)); err != nil {
		return Applied{}, &EnvError{Message: "the check result could not be stored", Err: err}
	}
	notes := []string{}
	for _, w := range r.Windows {
		notes = append(notes, fmt.Sprintf("%s window: attached=%v", w.Lane, w.Attached))
	}
	return Applied{Step: "browser", Wrote: true, Detection: Evaluate(ctx, env, mustStep("browser")), Notes: notes}, nil
}

// SignInLine summarises the stored sign-in probe of each job site. Site
// sign-ins never affect the step.
func SignInLine(ctx context.Context, env *Env) (string, bool) {
	if env.Store == nil {
		return "", false
	}
	rows, err := env.Store.Read().QueryContext(ctx, `SELECT domain, state FROM signin_probes`)
	if err != nil {
		return "", false
	}
	defer rows.Close()
	states := map[string]string{}
	for rows.Next() {
		var d, s string
		if rows.Scan(&d, &s) == nil {
			states[d] = s
		}
	}
	var parts []string
	any := false
	for _, site := range browser.SignInSites {
		s, ok := states[site.Domain]
		if !ok {
			s = "unknown"
		}
		any = any || s == string(browser.StateSignedIn)
		parts = append(parts, site.Label+" "+strings.ReplaceAll(s, "_", " "))
	}
	return strings.Join(parts, ", "), any
}
