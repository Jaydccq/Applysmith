package onboarding

import (
	"context"
	"database/sql"
	"strings"
	"sync"
	"time"
)

// ReasonKind classifies why a step is not complete. Only persona and review
// reasons can be acknowledged; the others need a real value.
type ReasonKind string

const (
	ReasonBlank       ReasonKind = "blank"
	ReasonInvalid     ReasonKind = "invalid"
	ReasonSeed        ReasonKind = "seed"
	ReasonPlaceholder ReasonKind = "placeholder"
	ReasonReserved    ReasonKind = "reserved-email"
	ReasonFictional   ReasonKind = "fictional-phone"
	ReasonPersona     ReasonKind = "persona"
	ReasonReview      ReasonKind = "review"
)

// isTemplateKind says the reason makes a step `template` rather than `partial`.
func (k ReasonKind) isTemplateKind() bool {
	switch k {
	case ReasonSeed, ReasonPlaceholder, ReasonReserved, ReasonFictional, ReasonPersona:
		return true
	}
	return false
}

// needsValue says no acknowledgement can clear the reason.
func (k ReasonKind) needsValue() bool {
	return k.isTemplateKind() && k != ReasonPersona
}

// Reason names one outstanding field, file or value in plain text.
type Reason struct {
	Field string     `json:"field,omitempty"`
	Text  string     `json:"text"`
	Kind  ReasonKind `json:"-"`
}

// Detection is a detector's verdict. Every status other than complete and
// skipped carries at least one reason.
type Detection struct {
	Status  StepStatus `json:"status"`
	Reasons []Reason   `json:"reasons,omitempty"`
	// Warnings never change the status (low disk, advisory findings).
	Warnings []string `json:"warnings,omitempty"`
}

// Texts returns the reason lines.
func (d Detection) Texts() []string {
	out := make([]string, len(d.Reasons))
	for i, r := range d.Reasons {
		out[i] = r.Text
	}
	return out
}

func (d Detection) has(k ReasonKind) bool {
	for _, r := range d.Reasons {
		if r.Kind == k {
			return true
		}
	}
	return false
}

// verdict builds a Detection from reasons.
func verdict(reasons ...Reason) Detection {
	d := Detection{Reasons: reasons, Status: StepComplete}
	for _, r := range reasons {
		if r.Kind.isTemplateKind() {
			d.Status = StepTemplate
			return d
		}
		d.Status = StepPartial
	}
	return d
}

func missing(text string) Detection {
	return Detection{Status: StepMissing, Reasons: []Reason{{Text: text, Kind: ReasonBlank}}}
}

func reasonf(kind ReasonKind, field, text string) Reason {
	return Reason{Field: field, Text: text, Kind: kind}
}

// Decision keys, all in the workspace_meta table of the state database.
func ackKey(id string) string    { return "setup.ack." + id }
func reviewKey(id string) string { return "setup.review." + id }
func skipKey(id string) string   { return "setup.skip." + id }
func secretKey(n string) string  { return "setup.secret-saved." + n }
func (e *Env) now() time.Time {
	if e.Now != nil {
		return e.Now()
	}
	return time.Now()
}

// decision reads a stored decision; an unreadable database holds none.
func (e *Env) decision(ctx context.Context, key string) (string, bool) {
	if e.decisions != nil && strings.HasPrefix(key, "setup.") {
		v, ok := e.decisions[key]
		return v, ok
	}
	if e.Store == nil {
		return "", false
	}
	var v string
	if err := e.Store.Read().QueryRowContext(ctx, `SELECT value FROM workspace_meta WHERE key = ?`, key).Scan(&v); err != nil {
		return "", false
	}
	return v, true
}

// setupDecisions reads every "setup." decision in one query, or nil when it
// cannot, which leaves each lookup to its own query.
func (e *Env) setupDecisions(ctx context.Context) map[string]string {
	if e.Store == nil {
		return nil
	}
	rows, err := e.Store.Read().QueryContext(ctx, `SELECT key, value FROM workspace_meta WHERE key >= 'setup.' AND key < 'setup/'`)
	if err != nil {
		return nil
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var k, v string
		if rows.Scan(&k, &v) != nil {
			return nil
		}
		out[k] = v
	}
	if rows.Err() != nil {
		return nil
	}
	return out
}

func (e *Env) setDecision(ctx context.Context, key, value string) error {
	if e.Store == nil {
		return errNoDatabase
	}
	return e.Store.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `INSERT INTO workspace_meta (key, value) VALUES (?, ?)
			ON CONFLICT(key) DO UPDATE SET value = excluded.value`, key, value)
		return err
	})
}

func (e *Env) clearDecision(ctx context.Context, key string) error {
	if e.Store == nil {
		return nil
	}
	return e.Store.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `DELETE FROM workspace_meta WHERE key = ?`, key)
		return err
	})
}

// Evaluate is the status every lane shows: the step's detector, with the
// decisions the user stored applied. A skip counts only for an optional step;
// an acknowledgement or review counts only while the data it was bound to is
// unchanged.
func Evaluate(ctx context.Context, env *Env, s Step) Detection {
	if !s.Required {
		if _, ok := env.decision(ctx, skipKey(s.ID)); ok {
			return Detection{Status: StepSkipped}
		}
	}
	d := s.Detect(ctx, env)
	if env.Workspace != nil && env.Workspace.IsDemo() {
		d = demoPersona(d)
	}
	if s.AckScope == nil || len(d.Reasons) == 0 {
		return d
	}
	scope, err := s.AckScope(ctx, env)
	if err != nil {
		return d
	}
	valid := func(key string) bool { v, ok := env.decision(ctx, key); return ok && v == scope }
	ack, review := valid(ackKey(s.ID)), valid(reviewKey(s.ID))
	var kept []Reason
	for _, r := range d.Reasons {
		if (r.Kind == ReasonPersona && ack) || (r.Kind == ReasonReview && review) {
			continue
		}
		kept = append(kept, r)
	}
	if len(kept) == len(d.Reasons) || d.Status == StepMissing {
		return d
	}
	out := verdict(kept...)
	out.Warnings = d.Warnings
	return out
}

// demoPersona accepts the shipped example persona in a demo workspace: the
// reasons that only say a value is the persona's (its name, employer or
// school, its reserved-domain email, its fictional phone number) are dropped,
// and a step they alone kept from completion is complete with the reason
// "demo persona". Seed text, placeholders and every other reason still count.
func demoPersona(d Detection) Detection {
	if d.Status == StepMissing || d.Status == StepSkipped {
		return d
	}
	var kept []Reason
	dropped := false
	for _, r := range d.Reasons {
		switch r.Kind {
		case ReasonPersona, ReasonReserved, ReasonFictional:
			dropped = true
		default:
			kept = append(kept, r)
		}
	}
	if !dropped {
		return d
	}
	out := verdict(kept...)
	out.Warnings = d.Warnings
	if out.Status == StepComplete {
		out.Reasons = []Reason{{Text: demoPersonaReason, Kind: ReasonDemo}}
	}
	return out
}

// EvaluateAll evaluates the whole catalog in order.
//
// The detectors only read, and most wait on the keychain or a probe of the
// machine, so they run at once: the dashboard asks for this state on every
// load (about 200 ms one after another, docs/architecture/performance.md).
func EvaluateAll(ctx context.Context, env *Env) map[string]Detection {
	shared := *env
	shared.profile, shared.decisions = &profileRead{}, env.setupDecisions(ctx)
	env = &shared
	steps := Catalog()
	got := make([]Detection, len(steps))
	var wg sync.WaitGroup
	for i, s := range steps {
		wg.Go(func() { got[i] = Evaluate(ctx, env, s) })
	}
	wg.Wait()
	out := make(map[string]Detection, len(steps))
	for i, s := range steps {
		out[s.ID] = got[i]
	}
	return out
}

// FirstIncomplete is the first required step that is not complete, or ok
// false when every required step is.
func FirstIncomplete(st map[string]Detection) (Step, bool) {
	for _, s := range Catalog() {
		if s.Required && st[s.ID].Status != StepComplete {
			return s, true
		}
	}
	return Step{}, false
}

// summarize joins the first reasons for doctor output. The terminal has no
// detail line, so a reason names its config path in brackets unless its text
// already does.
func summarize(d Detection) string {
	t := make([]string, len(d.Reasons))
	for i, r := range d.Reasons {
		t[i] = r.Text
		if r.Field != "" && !strings.Contains(r.Text, r.Field) {
			t[i] += " (" + r.Field + ")"
		}
	}
	if len(t) > 8 {
		t = append(t[:8:8], "and more")
	}
	return strings.Join(t, "; ")
}
