package apply

import (
	"context"
	"fmt"

	"applysmith/internal/engine"
	"applysmith/internal/gates"
)

// signal records a detection signal once per run under its cooldown key, so
// the account creation gate's cooldown applies to later runs: the run's named
// system, or on a host the generic flow serves that host only. A
// failure to store it is a warning: the run's outcome does not depend on it.
func (r *Runner) signal(e *Exec, signal, host string) {
	key := gates.CooldownKey(signupKey(e.Run.ATS), host)
	if r.d.Page.Signals == nil || e.signalled[signal+"|"+key] {
		return
	}
	if e.signalled == nil {
		e.signalled = map[string]bool{}
	}
	e.signalled[signal+"|"+key] = true
	if _, err := r.d.Page.Signals.RecordSignal(context.WithoutCancel(e.Ctx), key, signal, e.Run.ID); err != nil {
		e.Run.Warnings = append(e.Run.Warnings, fmt.Sprintf("the %s signal on %s could not be recorded: %v", signal, key, err))
	}
}

// recordSignals turns the run's stop into the signals it stands for: a
// challenge the run could not clear, on the host it stood on, and definite
// lockout wording at a sign-in, on the host that showed it.
func (r *Runner) recordSignals(e *Exec) {
	if e.Run.Outcome.Board == "challenge" {
		r.signal(e, gates.SignalCaptcha, e.signalHost())
	}
	for _, s := range e.Run.Signins {
		if s.Lockout {
			r.signal(e, gates.SignalAccountLocked, s.Host)
			break
		}
	}
}

// signalHost is the host a stop's signal is about: the page the stopping stage
// read last (the account stage's page for a stop there, else the walk's), and
// the application address before any page was read.
func (e *Exec) signalHost() string {
	var pages []*PageState
	if s := e.sess; s != nil {
		pages = []*PageState{s.last, s.entry}
		if e.Run.Outcome.Stage == StageAccount {
			pages = []*PageState{s.entry, s.last}
		}
	}
	for _, p := range pages {
		if p == nil {
			continue
		}
		if h, _ := parseHost(p.URL); h != "" {
			return h
		}
	}
	h, _ := parseHost(firstNonEmpty(e.Cls.ApplyURL, e.target()))
	return h
}

// expectedStandardFields is how many of the applicant's own identity fields
// (first name, last name or full name, email, phone) every board of a system
// exposes on its first application page; phone is optional on some boards and
// so is not expected. A filler that finds fewer has been shown a tampered or
// degraded page. Systems with a multi-step or free-form first page expect none.
var expectedStandardFields = map[string]int{ATSGreenhouse: 3, ATSLever: 2, ATSAshby: 2}

// standardFields counts the distinct identity slots among a fill's rows.
func standardFields(rows []engine.Row) int {
	seen := map[gates.IdentityField]bool{}
	for _, r := range rows {
		switch f, ok := identityFieldOf(r.Label); {
		case !ok:
		case f == gates.FieldFirstName || f == gates.FieldLastName || f == gates.FieldFullName || f == gates.FieldEmail || f == gates.FieldPhone:
			seen[f] = true
		}
	}
	return len(seen)
}

// signalFirstFill records the silent-degradation signal when the first page
// the filler worked on held fewer standard fields than its system exposes.
func (r *Runner) signalFirstFill(e *Exec, rows []engine.Row) {
	want := expectedStandardFields[e.Run.ATS]
	if want == 0 {
		return
	}
	if sig := gates.DetectSignal(gates.PageCapture{StandardFields: standardFields(rows), ExpectedFields: want}); sig != "" {
		r.signal(e, sig, e.signalHost())
	}
}
