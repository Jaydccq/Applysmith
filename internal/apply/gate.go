package apply

import (
	"context"
	"fmt"
	"strings"

	"applysmith/internal/lazyregexp"
)

// MaxGateReentries bounds how often the account stage is entered again at a
// login gate the walk stopped on.
const MaxGateReentries = 2

// gateTab is the walker's tab at a login gate. The walk tags it instead of
// closing it, because the gate can exist only in the session that clicked
// through to it (a modal that never renders on a cold load, a create-account
// endpoint bound to the referring session); the account stage adopts the same
// tab, with no fresh navigation, and hands it back under the tag.
type gateTab struct {
	tab Tab
	// adopted counts the account stage's uses of the tab.
	adopted int
}

// alive reports whether the tab is still open. A tab that was closed under the
// run is left alone: nothing is reopened, kept or tagged in its place.
func (g *gateTab) alive() bool {
	c, ok := g.tab.(interface{ Closed() bool })
	return !ok || !c.Closed()
}

// tagGate records the walker's tab as the posting's gate tab.
func (e *Exec) tagGate(tab Tab) {
	if e.gate == nil || e.gate.tab != tab {
		e.gate = &gateTab{tab: tab}
	}
}

// confirmsLoginGate is the independent second look at a page the walk called a
// login gate: a password gate, or a page that offers a login control and no
// form. A walk end that this look does not confirm is not the user's
// credential to store.
func confirmsLoginGate(st *PageState) bool {
	if findWall(st) != nil || bareSignInGate(st) || accountPageFirst(st) {
		return true
	}
	if st.HasForm() {
		return false
	}
	for _, c := range st.Controls {
		if c.Chat || c.Chrome {
			continue
		}
		for _, l := range c.Labels {
			if signinCTAShape(l) {
				return true
			}
		}
	}
	return false
}

// signinCTAShape reads a login control: Sign in, Log in, or either with a
// provider ("Sign in with SSO").
func signinCTAShape(label string) bool {
	l := strings.ToLower(strings.TrimSpace(label))
	return signinBtnRE.MatchString(l) || strings.HasPrefix(l, "sign in with ") || strings.HasPrefix(l, "log in with ") || strings.HasPrefix(l, "sign in using ")
}

// reenterGate handles a walk that ended at a login gate. It takes the second
// look; a password gate or an email-first sign-in re-enters the account stage
// on the walker's own tab, at most MaxGateReentries times; a login control the
// stage's probe could not see, a job board's sign-in and LinkedIn's login ask
// the user's everyday Chrome once per host for the session. It reports whether
// the walk should run again, and the stop that ends the run when the gate is
// not the stage's to clear. A gate nothing confirms is a defect.
func (r *Runner) reenterGate(ctx context.Context, e *Exec, s *session, wr *walkResult) (rewalk bool, stp *stop) {
	if wr.NoAccount || e.Run.ATS == ATSLinkedIn {
		return r.boardSession(ctx, e, s, wr)
	}
	if wr.Last != nil {
		e.tagGate(s.tab)
	}
	if e.gate == nil || !e.gate.alive() {
		return false, nil
	}
	st, err := Probe(ctx, s.tab)
	if err != nil {
		return false, nil
	}
	if !confirmsLoginGate(st) {
		wr.End, wr.Detail = EndStalled, "the walk ended at a login gate that a second look does not confirm: "+wr.Detail
		return false, nil
	}
	host, _ := parseHost(st.URL)
	s.entry = st
	if findWall(st) == nil {
		// A login control the account stage's probe could not see: no password
		// is typed and no sign-up runs; the session is asked for once. An
		// email-first sign-in the session does not clear is the stage's to
		// work, below.
		a := s.newAcct(host)
		email := readEmailStep(st) != nil
		if a.rec.Succeeded == "session" && !email {
			return false, nil // the session the walker just refused
		}
		imported, stp := a.importSession(ctx, st.URL)
		if stp != nil {
			return false, stp
		}
		if imported {
			return true, nil
		}
		if !email {
			if staffGate(gatePages(ctx, s.tab, st)) {
				return false, &stop{class: ClassPosting, at: StageAccount,
					reason: fmt.Sprintf("the posting is for the employer's own staff: %s offers only an employee sign-in and no way to create an account", host)}
			}
			wr.Detail += a.syncNote()
			return false, nil
		}
	}
	if r.d.Page.Credentials == nil {
		return false, nil
	}
	if e.reentries >= MaxGateReentries {
		wr.Detail += fmt.Sprintf("; the account stage was re-entered %d times already", e.reentries)
		return false, nil
	}
	// A gate address a re-entry already worked at is not worked at again.
	key := redactURL(st.URL)
	if e.gateSeen == nil {
		e.gateSeen = map[string]bool{}
	}
	if e.gateSeen[key] {
		wr.Detail += "; the account stage already worked at this address"
		return false, nil
	}
	e.gateSeen[key] = true
	e.reentries++
	e.gate.adopted++
	r.landed(e, st.URL)
	start := r.d.Now()
	detail, stp := r.accountOn(ctx, e, s, st)
	if stp != nil {
		stp.at = StageAccount
		r.record(e, StageAccount, StatusFailed, "re-entry at the login gate: "+firstNonEmpty(detail, stp.reason), start)
		return false, stp
	}
	r.record(e, StageAccount, StatusOK, "re-entry at the login gate: "+detail, start)
	return true, nil
}

// employeeSigninRE reads a sign-in the page offers the employer's own staff.
var employeeSigninRE = lazyregexp.New(`(?i)\bemployees?\s+(?:log\s*-?\s*in|sign\s*-?\s*in)\b`)

// gatePages are a sign-in gate's page and the documents of its frames, which
// the page probe leaves out when they hold no form: iCIMS draws its whole
// portal, its sign-in page included, in a same-origin frame.
func gatePages(ctx context.Context, t Tab, st *PageState) []*PageState {
	pages := []*PageState{st}
	for _, f := range subFrames(ctx, t) {
		var fs PageState
		if t.EvalFrame(ctx, f.ID, probeExpr("probe", nil), &fs) == nil {
			pages = append(pages, &fs)
		}
	}
	return pages
}

// staffGate reports whether a sign-in gate with no email or password box is an
// employer's portal for its own staff: its words name an employee sign-in and
// no control on it creates an account ("Employee Login · Log back in!" on
// Publicis Groupe's employees- iCIMS portal, whose posting the candidates'
// portal answers 410). Such a posting is not the user's to sign in to.
func staffGate(pages []*PageState) bool {
	named := false
	for _, p := range pages {
		named = named || employeeSigninRE.MatchString(p.Text)
		for _, c := range p.Controls {
			for _, l := range c.Labels {
				if signupBtnRE.MatchString(strings.TrimSpace(l)) {
					return false
				}
			}
		}
	}
	return named
}

// boardSession works a job board's "sign in to apply" or LinkedIn's login: the
// sign-in there is the user's, so no password is typed and no account is
// created; the session from the user's everyday Chrome is asked for once per
// host. A session that landed walks the page again, LinkedIn from its Easy Apply
// flow.
func (r *Runner) boardSession(ctx context.Context, e *Exec, s *session, wr *walkResult) (bool, *stop) {
	if wr.Last == nil {
		return false, nil
	}
	host, _ := parseHost(wr.Last.URL)
	if host == "" {
		return false, nil
	}
	linkedIn := e.Run.ATS == ATSLinkedIn
	reload := wr.Last.URL
	if linkedIn {
		reload = ""
	}
	a := s.newAcct(host)
	imported, stp := a.importSession(ctx, reload)
	if stp != nil {
		return false, stp
	}
	if !imported {
		wr.Detail += a.syncNote()
		return false, nil
	}
	if linkedIn {
		if stp := r.openLinkedIn(ctx, e, s); stp != nil {
			return false, stp
		}
	}
	return true, nil
}

// signupATS are the systems with an account flow of their own: a walk that
// lands on one of them from another site resolves again and signs in there.
var signupATS = map[string]bool{ATSGreenhouse: true, ATSLever: true, ATSAshby: true, ATSWorkday: true}

// landed records the hop of a walk that stopped at a sign-in on another
// applicant-tracking system than the posting's: the run resolves again for the
// landing, keeping the posting URL as its identity.
func (r *Runner) landed(e *Exec, pageURL string) {
	cls, stp := Classify(pageURL)
	if stp != nil || cls.ATS == e.Run.ATS || !signupATS[cls.ATS] {
		return
	}
	e.Cls = cls
	e.Run.ATS, e.Run.Tenant = cls.ATS, cls.Tenant
	r.record(e, StageResolve, StatusOK, fmt.Sprintf("landed on ats=%s tenant=%s", cls.ATS, cls.Tenant), r.d.Now())
}
