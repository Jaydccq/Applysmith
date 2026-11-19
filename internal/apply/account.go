package apply

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"slices"
	"strings"
	"time"

	"applysmith/internal/browser"
	"applysmith/internal/credentials"
	"applysmith/internal/gates"
	"applysmith/internal/gmail"
	"applysmith/internal/lazyregexp"
)

// SessionSync asks the user's everyday Chrome, through the paired extension,
// for the sign-in of exactly one host. The implementation waits at most 45
// seconds, withdraws its request when the wait ends and does not wait when the
// extension has been silent or reported no cookies for the host.
type SessionSync interface {
	SyncOwnHost(ctx context.Context, host string) (OwnHostResult, error)
}

// OwnHostResult is how a run's request for its host's session ended.
type OwnHostResult struct {
	// Imported is true when cookies for the host landed in the profile.
	Imported bool
	// Reason says why not: extension-silent, no-cookies, timeout, replaced.
	Reason string
}

// Credentials is the credential service as the account stage uses it.
type Credentials interface {
	Candidates(ctx context.Context, host, system, tenant string) ([]credentials.Candidate, error)
	Keep(ctx context.Context, host, system, tenant string, c credentials.Candidate) error
	Held(ctx context.Context, host string) (credentials.Hold, error)
	RecordExhausted(ctx context.Context, tx *sql.Tx, host string, lockout bool, runID string) error
	GenerateFor(name string, maxLen int) (string, bool, error)
	Created(ctx context.Context, host, system, tenant, email, name string) error
	Remove(ctx context.Context, name string) error
	// SignedInBefore reports whether a stored password ever signed in anywhere.
	SignedInBefore(ctx context.Context, name string) (bool, error)
	// ResetFailedAt, RecordResetFailed and ClearHold keep the password reset's
	// 24-hour hold and clear a host's holds after a reset that signed in.
	ResetFailedAt(ctx context.Context, host string) (time.Time, error)
	RecordResetFailed(ctx context.Context, host string) error
	ClearHold(ctx context.Context, host string) error
}

// Mail is the bounded mail lookups the account stage may make.
type Mail interface {
	ActivationLink(ctx context.Context, q gmail.LinkQuery) (gmail.LinkResult, error)
	ResetLink(ctx context.Context, q gmail.LinkQuery) (gmail.LinkResult, error)
}

// SignupPolicy supplies the cooldowns of the account creation gate.
type SignupPolicy interface {
	Cooldowns(ctx context.Context) ([]gates.Cooldown, error)
}

// Signals stores a detection signal for a system and starts the cooldown the
// policy gives it. It returns when that cooldown ends, the zero time when none
// started.
type Signals interface {
	RecordSignal(ctx context.Context, system, signal, runID string) (time.Time, error)
}

// AccountTiming holds the account stage's clocks; a zero field takes the
// specified value.
type AccountTiming struct {
	// Stable is how long the sign-in form must stay gone on a loaded page.
	Stable time.Duration
	// Google bounds the complete existing-session OAuth flow.
	Google time.Duration
	// Answer is how long a pressed sign-in is given to answer.
	Answer time.Duration
	Poll   time.Duration
	// MailWait and MailPoll bound the wait for a mail the run asked for.
	MailWait, MailPoll time.Duration
	// Settle is the wait after opening an activation link.
	Settle time.Duration
	// Boxes is how long a reopened page is given to show its code boxes;
	// CodePoll is the poll of the code lookup.
	Boxes, CodePoll time.Duration
	// ResetWait bounds the wait for a password-reset mail or code.
	ResetWait time.Duration
	// Dwell is how long a Workday account form is on screen before its submit
	// is pressed (see dwell).
	Dwell time.Duration
}

func (t AccountTiming) withDefaults() AccountTiming {
	set := func(d *time.Duration, v time.Duration) {
		if *d == 0 {
			*d = v
		}
	}
	set(&t.Stable, 1500*time.Millisecond)
	set(&t.Google, 45*time.Second)
	set(&t.Answer, 8*time.Second)
	set(&t.Poll, 300*time.Millisecond)
	set(&t.MailWait, time.Duration(gates.ActivationResendWait)*time.Millisecond)
	set(&t.MailPoll, time.Duration(gates.MailPollInterval)*time.Millisecond)
	set(&t.Settle, time.Duration(gates.ActivationSettleWait)*time.Millisecond)
	set(&t.Boxes, 8*time.Second)
	set(&t.CodePoll, 2*time.Second)
	set(&t.ResetWait, time.Duration(gates.ResetMailWait)*time.Millisecond)
	set(&t.Dwell, 6*time.Second)
	return t
}

var (
	verifyRE     = lazyregexp.New(`(?i)\b(?:verify|confirm|activate)\s+your\s+(?:e-?mail|account)\b|\bverification\s+(?:e-?mail|link)\b|\bactivation\s+e-?mail\b|\bemail\s+(?:address\s+)?(?:has\s+)?not\s+been\s+verified\b`)
	sentPageRE   = lazyregexp.New(`(?i)\bactivation\s+e-?mail\s+sent\b|\bwe(?:'ve|\s+have)\s+sent\s+(?:you\s+)?(?:an?\s+)?(?:activation|verification)`)
	signupBtnRE  = lazyregexp.New(`(?i)^(?:create\s+(?:(?:an?|your|new)\s+)?(?:[\w.-]+\s*)?(?:account|profile|id)|sign\s*up|register)\b`)
	signinBtnRE  = lazyregexp.New(`(?i)^(?:sign\s*in|log\s*in|login)$`)
	badLoginRE   = lazyregexp.New(`(?i)\b(?:invalid|incorrect|wrong|does\s+not\s+match|not\s+recogni[sz]ed|unable\s+to\s+(?:sign|log)\s*in|failed)\b`)
	neverSyncFor = []string{"google.com", "youtube.com"}
)

// wall is a sign-in wall read from a probe: the credential inputs and the
// controls that sign in or offer to create an account.
type wall struct {
	email, password *InputInfo
	signin          *Control
	create          *Control
	google          *Control
	// registration is true when the page creates an account: the password box
	// carries the new-password autocomplete token, a second password box
	// retypes it, or the form's own submit reads Create Account while sign-in
	// is only a link beside it. No stored password is ever typed into it.
	registration bool
}

// signinRank orders the Sign In controls of a page: the one in the password
// box's form first, then one in its dialog, then any outside the page header
// (Workday's header Sign In stays behind its Sign In dialog).
func signinRank(c *Control, pw *InputInfo) int {
	switch {
	case pw != nil && pw.Form >= 0 && c.Form == pw.Form:
		return 0
	case pw != nil && pw.Modal && c.Modal:
		return 1
	case !c.Chrome:
		return 2
	}
	return 3
}

// findWall reads a sign-in wall: a password box with no application field
// beside the credential boxes. On a page that registers, the name, phone and
// username boxes the profile answers are the account's, not the
// application's.
func findWall(st *PageState) *wall {
	w := &wall{}
	other, profile, passwords := 0, 0, 0
	newPassword := false
	for i := range st.Inputs {
		in := &st.Inputs[i]
		if in.Furniture || in.Type == "hidden" {
			continue
		}
		switch {
		case in.Type == "password":
			passwords++
			if w.password == nil {
				w.password = in
			}
			newPassword = newPassword || strings.Contains(strings.ToLower(in.Autocomplete), "new-password")
		case in.Type == "email" || strings.Contains(strings.ToLower(in.Label+in.Name+in.Autocomplete), "mail") || strings.Contains(strings.ToLower(in.Label+in.Name+in.Autocomplete), "user"):
			if w.email == nil {
				w.email = in
			}
		case in.Type == "checkbox" || in.Type == "radio":
		case profileField(in) != "":
			profile++
		default:
			other++
		}
	}
	for i := range st.Controls {
		c := &st.Controls[i]
		if c.Chat {
			continue
		}
		for _, l := range c.Labels {
			switch {
			case gates.GoogleSignInControl(c.Labels) && w.google == nil && !c.Disabled:
				w.google = c
			case ssoRE.MatchString(l):
			case signinBtnRE.MatchString(l) && (w.signin == nil || signinRank(c, w.password) < signinRank(w.signin, w.password)):
				w.signin = c
			case signupBtnRE.MatchString(l) && w.create == nil:
				w.create = c
			}
		}
	}
	w.registration = newPassword && !offersForgot(st)
	// RedBalloon's create-account form: one password box and a Create Account
	// submit in its form, with "Log in" only a link outside it or a toggle
	// button that submits nothing (YC's sign-up view).
	if w.password != nil && w.create != nil && w.password.Form >= 0 && w.create.Form == w.password.Form && submitsForm(w.create) &&
		(w.signin == nil || w.signin.Form != w.password.Form || !submitsForm(w.signin)) {
		w.registration = true
	}
	w.registration = w.registration || passwords > 1
	if !w.registration {
		other += profile
	}
	if other > 1 {
		return nil
	}
	if w.password == nil && (w.google == nil || other > 0 || !googleWallContext(st)) {
		return nil
	}
	return w
}

type acct struct {
	r       *Runner
	e       *Exec
	s       *session
	host    string
	system  string
	tenant  string
	tm      AccountTiming
	rec     *SigninRecord
	refused int
	// lockout is true once lockout wording appeared.
	lockout bool
	// linkRefused is true once the activation mail came with no link the run may open.
	linkRefused bool
	// resetUnsaved is true once a reset was allowed but no saved password was
	// there for it to set.
	resetUnsaved bool
	cands        []credentials.Candidate
	// gateIn is the account creation gate's input of this attempt, kept so the
	// form's own facts can be decided against the same policy.
	gateIn gates.AccountInput
}

// account is the stage on the apply window before any form is filled: it signs
// in first and signs up only when the sign-in attempt says an account is
// needed. A session from the user's own Chrome comes before any password; a
// session that takes means no password is submitted.
func (r *Runner) account(ctx context.Context, e *Exec, s *session) (string, *stop) {
	st, err := Probe(ctx, s.tab)
	if err != nil {
		return "", &stop{class: ClassEnvironment, reason: "the entry page could not be read: " + err.Error()}
	}
	s.entry = st
	if n := WorkdayUnavailable(e.Run.ATS, st.URL); n != "" {
		return "", &stop{class: ClassUnavailable, reason: n}
	}
	expected, _ := parseHost(e.Cls.ApplyURL)
	if sig := gates.DetectSignal(gates.PageCapture{HTTPStatus: s.navStatus, Text: st.Text, FinalURL: st.URL, ApplyPageExpected: expected != "", ExpectedHost: expected}); sig != "" {
		host, _ := parseHost(st.URL)
		r.signal(e, sig, host)
	}
	if g, err := icimsFind(ctx, s.tab); err == nil && g.Found {
		return "no login needed: iCIMS's email step is the walk's", nil
	}
	return r.accountOn(ctx, e, s, st)
}

// accountOn runs the stage on a page that was already read: the entry page, or
// the gate page the walk stopped on, in the same tab.
func (r *Runner) accountOn(ctx context.Context, e *Exec, s *session, st *PageState) (string, *stop) {
	host, _ := parseHost(st.URL)
	a := &acct{r: r, e: e, s: s, host: host, system: e.Run.ATS, tenant: e.Run.Tenant, tm: r.d.Page.Account.withDefaults()}
	idx := e.signinIndex(host)
	a.rec = &e.Run.Signins[idx]
	a.refused, a.lockout = a.rec.Refusals, a.rec.Lockout
	defer func() { a.rec.Refusals = a.refused }()
	w := findWall(st)
	if w == nil && readEmailStep(st) != nil {
		return a.atEmailGate(ctx, st)
	}
	if w == nil {
		if d, handled, stp := a.detailsStep(ctx, st); handled {
			if stp != nil {
				stp.at = StageAccount
			}
			return d, stp
		}
		return "no sign-in wall at the entry page", nil
	}
	// A session from the user's everyday Chrome comes before any password, any
	// Google press and any stop at the wall, Google-only walls included.
	imported, stp := a.importSession(ctx, st.URL)
	if stp != nil {
		return "", stp
	}
	if imported {
		if ok, _ := a.signedIn(ctx); ok {
			a.rec.Succeeded = "session"
			return "signed in by the imported session; no password was submitted", nil
		}
		// The session did not carry the sign-in: the reloaded page's wall is
		// the one worked on.
		if p, err := Probe(ctx, a.s.tab); err == nil {
			if nw := findWall(p); nw != nil {
				st, w = p, nw
			}
		}
	}
	if w.google != nil && r.d.Page.Settings != nil && r.d.Page.Settings().AllowGoogleSignIn {
		return a.googleSignIn(ctx, w)
	}
	if st, w, stp = a.openEmailForm(ctx, st, w); stp != nil {
		return "", stp
	}
	if w.password == nil && googleRegistration(st, w) {
		return "", a.fail(ClassCredential, "sign-in", "this page creates an account with Google; finish registration in the browser, then resume%s", a.syncNote())
	}
	if w.password == nil {
		return "", a.fail(ClassCredential, "sign-in", "%s offers only Google sign-in and Google sign-in is off; sign in in the browser or enable it in Settings%s", a.host, a.syncNote())
	}
	detail, stp := a.signIn(ctx, w)
	if stp != nil {
		stp.at = StageAccount
	}
	return detail, stp
}

var googleWallHeadingRE = lazyregexp.New(`(?i)^(?:(?:candidate|account)\s+)?(?:sign\s*in|log\s*in|login|sign\s*up|create\s+(?:an?\s+)?account|register)\b`)

func googleWallContext(st *PageState) bool {
	for _, heading := range append([]string{st.Title}, st.Headings...) {
		if googleWallHeadingRE.MatchString(strings.TrimSpace(heading)) {
			return true
		}
	}
	return false
}

var googleApplicationFieldRE = lazyregexp.New(`(?i)\b(?:(?:first|last|full)\s*name|resume|curriculum vitae|cover letter|phone)\b`)

func googleRegistration(st *PageState, w *wall) bool {
	if w.registration {
		return true
	}
	if w.google != nil {
		for _, label := range w.google.Labels {
			if signupBtnRE.MatchString(strings.TrimSpace(label)) {
				return true
			}
		}
	}
	for _, heading := range append([]string{st.Title}, st.Headings...) {
		if signupBtnRE.MatchString(strings.TrimSpace(heading)) {
			return true
		}
	}
	return false
}

// googleSignIn follows the target or its new run-owned popup through a verified
// Google account choice and basic identity grant. Authentication is established
// only when the target application form returns.
func (a *acct) googleSignIn(ctx context.Context, w *wall) (string, *stop) {
	if a.rec.Google != "" {
		return "", a.fail(ClassCredential, "sign-in", "Google sign-in was already tried on this host; finish it in the browser")
	}
	current, err := Probe(ctx, a.s.tab)
	if err != nil {
		return "", a.fail(ClassEnvironment, "", "the Google sign-in page could not be read")
	}
	host, _ := parseHost(current.URL)
	fresh := findWall(current)
	if host != a.host || fresh == nil || fresh.google == nil {
		return "", a.fail(ClassCredential, "sign-in", "the Google sign-in wall changed; nothing was pressed")
	}
	if googleRegistration(current, fresh) {
		if st := a.signupDecision(ctx, "Google registration"); st != nil {
			return "", st
		}
		if err := a.recordAttempt(ctx); err != nil {
			return "", a.fail(ClassEnvironment, "", "the Google registration attempt could not be recorded")
		}
	}
	a.rec.Google = "attempted"
	started := time.Now()
	if st := a.pressAs(ctx, fresh.google, gates.StageGoogleSignIn); st != nil {
		return "", st
	}
	var since time.Time
	provider := newGoogleFlow(a)
	defer provider.close()
	deadline := time.Now().Add(a.tm.Google)
	for time.Now().Before(deadline) {
		page, err := Probe(ctx, a.s.tab)
		if err == nil {
			h, _ := parseHost(page.URL)
			if h != a.host {
				_, st := provider.step(ctx, a.s.tab, page)
				if st != nil {
					return "", st
				}
				since = time.Time{}
				if !sleepCtx(ctx, a.tm.Poll) {
					return "", cancelledStop(StageAccount)
				}
				continue
			}
			// A vanished login button or a blank callback is not authentication.
			// Require the site's actual application fields on a loaded document.
			application := false
			for _, in := range page.Inputs {
				if !in.Furniture && (in.Type == "file" || googleApplicationFieldRE.MatchString(in.Label)) {
					application = true
				}
			}
			controls := Categorize(page, false)
			application = application && (len(controls.Nav) > 0 || len(controls.Submit) > 0)
			// The probe also lists ordinary four-to-eight-field forms as
			// dialog candidates. Only actual dialogs and code steps block return.
			dialog := false
			for _, d := range page.Dialogs {
				dialog = dialog || !d.Form
			}
			_, codeEntry := classifyCodeEntry(page.Dialogs)
			_, codeDelivery := classifyDelivery(page.Dialogs)
			if application && !page.Loading && findWall(page) == nil && !awaitsCode(page) && !dialog && !codeEntry && !codeDelivery {
				if since.IsZero() {
					since = time.Now()
				}
				if time.Since(since) >= a.tm.Stable {
					a.rec.Google, a.rec.Succeeded = "signed-in", "google"
					return "signed in with the existing Google session; the target application form was observed", nil
				}
			} else {
				since = time.Time{}
			}
		} else {
			since = time.Time{}
		}
		if popups, ok := a.s.tab.(googlePopupSource); ok {
			ids := popups.GooglePopups(started)
			if len(ids) > 1 {
				break
			}
			for _, id := range ids {
				popup, err := provider.popup(ctx, popups, id)
				if err != nil {
					continue
				}
				state, err := Probe(ctx, popup)
				if err != nil {
					continue
				}
				h, _ := parseHost(state.URL)
				if h == a.host {
					continue
				}
				_, stopped := provider.step(ctx, popup, state)
				if stopped != nil {
					return "", stopped
				}
			}
		}
		if !sleepCtx(ctx, a.tm.Poll) {
			return "", cancelledStop(StageAccount)
		}
	}
	a.rec.Google = "needs-user"
	return "", a.fail(ClassCredential, "sign-in", "Google sign-in needs your action (unknown account, additional permission, password or verification); finish it in the browser, then resume")
}

type googlePopupSource interface {
	GooglePopups(since time.Time) []string
	GooglePopup(ctx context.Context, id string) (Tab, func(), error)
}

type googleFlow struct {
	a        *acct
	tabs     map[string]Tab
	cleanup  []func()
	pressed  map[string]bool
	selected bool
}

func newGoogleFlow(a *acct) *googleFlow {
	return &googleFlow{a: a, tabs: map[string]Tab{}, pressed: map[string]bool{}}
}
func (g *googleFlow) close() {
	for _, f := range g.cleanup {
		f()
	}
}
func (g *googleFlow) popup(ctx context.Context, src googlePopupSource, id string) (Tab, error) {
	if t := g.tabs[id]; t != nil {
		return t, nil
	}
	t, done, err := src.GooglePopup(ctx, id)
	if err == nil {
		g.tabs[id] = t
		g.cleanup = append(g.cleanup, done)
	}
	return t, err
}

var googleAccountEmailRE = lazyregexp.New(`[a-zA-Z0-9.!#$%&'*+/=?^_` + "`" + `{|}~-]+@[a-zA-Z0-9.-]+\.[a-zA-Z]{2,}`)
var googleBasicScopes = map[string]bool{"openid": true, "email": true, "profile": true, "https://www.googleapis.com/auth/userinfo.email": true, "https://www.googleapis.com/auth/userinfo.profile": true}

// Inspect only an actual authorization URL (including Google's nested continue
// URL). A missing scope list never authorizes a consent press.
func googleScopeProof(raw, targetHost string) (present, allowed bool) {
	for depth := 0; depth < 3; depth++ {
		u, err := url.Parse(raw)
		if err != nil || u.Scheme != "https" || u.Hostname() != "accounts.google.com" || u.User != nil {
			return false, false
		}
		if scopes, ok := u.Query()["scope"]; ok {
			redirect, err := url.Parse(u.Query().Get("redirect_uri"))
			if err != nil || redirect.Scheme != "https" || redirect.Hostname() != targetHost {
				return true, false
			}
			count := 0
			for _, s := range scopes {
				for _, scope := range strings.Fields(s) {
					count++
					if !googleBasicScopes[scope] {
						return true, false
					}
				}
			}
			return true, count > 0
		}
		raw = u.Query().Get("continue")
		if raw == "" {
			break
		}
	}
	return false, false
}

func (g *googleFlow) step(ctx context.Context, tab Tab, page *PageState) (bool, *stop) {
	a := g.a
	stop := func(reason string) (bool, *stop) {
		a.rec.Google = "needs-user"
		return false, a.fail(ClassCredential, "sign-in", "Google sign-in needs your action: %s", reason)
	}
	u, err := url.Parse(page.URL)
	if err != nil || u.Scheme != "https" || u.Hostname() != "accounts.google.com" || u.User != nil {
		return stop("the provider host is not Google's HTTPS account service")
	}
	if a.r.d.Page.Settings == nil || !a.r.d.Page.Settings().AllowGoogleSignIn {
		return stop("the Google setting is off")
	}
	for _, in := range page.Inputs {
		if !in.Furniture && in.Type != "hidden" {
			return stop("a Google login, password, verification or other input is required")
		}
	}
	present, allowed := googleScopeProof(page.URL, a.host)
	if present {
		if !allowed {
			return stop("the request includes additional or unverified permissions")
		}
	}
	// The account picker must identify exactly the user's curated email. Never
	// choose an arbitrary first account or use another account's permissions.
	email := a.accountEmail()
	var chosen *Control
	for i := range page.Controls {
		c := &page.Controls[i]
		if c.Disabled || c.Chat {
			continue
		}
		if email != "" && strings.EqualFold(c.GoogleAccount, email) {
			if chosen != nil && chosen.I != c.I {
				return stop("the account choice is ambiguous")
			}
			chosen = c
		}
	}
	action := "consent"
	if chosen != nil && g.selected {
		return true, nil
	}
	if chosen != nil {
		action = "account"
	} else {
		chosen = nil
		if !googleIdentityMatches(page.Text, email) {
			return stop("the signed-in Google account does not match the profile email")
		}
		if !present || !allowed {
			return stop("the current basic email/profile permissions could not be verified")
		}
		for i := range page.Controls {
			c := &page.Controls[i]
			if c.Disabled || c.Chat {
				continue
			}
			if googleConsentLabels(c.Labels) {
				if chosen != nil {
					return stop("the permission control is ambiguous")
				}
				chosen = c
			}
		}
	}
	if chosen == nil && g.selected {
		return true, nil
	}
	if chosen == nil {
		return stop("no matching account or supported permission control was found")
	}
	key := tab.ID() + ":" + action
	if g.pressed[key] {
		return true, nil
	}
	loc, err := Locate(ctx, tab, chosen.I)
	if err != nil || !loc.Found || !loc.Visible || !loc.Hit {
		return stop("the provider control changed or is covered")
	}
	valid := googleConsentLabels(loc.Labels)
	if action == "account" {
		valid = false
		for _, label := range loc.Labels {
			for _, found := range googleAccountEmailRE.FindAllString(label, -1) {
				if strings.EqualFold(found, email) {
					valid = true
				}
			}
		}
	}
	// Re-read the origin and permission request immediately before the press.
	fresh, err := Probe(ctx, tab)
	if err != nil || fresh.URL != page.URL || !valid {
		return stop("the provider page changed before the press")
	}
	if action == "account" {
		valid = false
		for _, c := range fresh.Controls {
			if c.I == chosen.I && !c.Disabled && !c.Chat && strings.EqualFold(c.GoogleAccount, email) {
				valid = true
			}
		}
		if !valid {
			return stop("the provider account choice changed before the press")
		}
	}
	if action == "consent" {
		proof, basic := googleScopeProof(fresh.URL, a.host)
		if !proof || !basic || !googleIdentityMatches(fresh.Text, email) {
			return stop("the account or permission proof changed before the press")
		}
	}
	d := gates.PressControl(gates.ControlInput{Unattended: a.e.unattended(), Stage: gates.StageGoogleProvider, Found: true, Labels: loc.Labels, TopHost: u.Hostname(), AllowGoogleSignIn: a.r.d.Page.Settings != nil && a.r.d.Page.Settings().AllowGoogleSignIn, GoogleProviderControl: valid})
	if st := a.audit(d); st != nil {
		return false, st
	}
	if !d.Allowed() {
		return stop(d.Reason)
	}
	g.pressed[key] = true
	if err := tab.Click(ctx, loc.X, loc.Y); err != nil {
		return stop("the provider did not accept the press")
	}
	if action == "account" {
		g.selected = true
	}
	return true, nil
}
func googleIdentityMatches(text, email string) bool {
	seen := false
	for _, found := range googleAccountEmailRE.FindAllString(text, -1) {
		if email == "" || !strings.EqualFold(found, email) {
			return false
		}
		seen = true
	}
	return seen
}

func googleConsentLabels(labels []string) bool {
	seen := false
	for _, l := range labels {
		l = strings.ToLower(strings.TrimSpace(l))
		if l == "" {
			continue
		}
		if l != "continue" && l != "allow" {
			return false
		}
		seen = true
	}
	return seen
}

// signinIndex is the run's record of a host, created on first use. A host the
// stage meets again (a re-entry at a gate) keeps one record and one budget.
func (e *Exec) signinIndex(host string) int {
	for i := range e.Run.Signins {
		if e.Run.Signins[i].Host == host {
			return i
		}
	}
	e.Run.Signins = append(e.Run.Signins, SigninRecord{Host: host})
	return len(e.Run.Signins) - 1
}

func (a *acct) fail(class Class, board, format string, args ...any) *stop {
	return &stop{class: class, board: board, reason: fmt.Sprintf(format, args...), at: StageAccount}
}

// audit records a decision and refuses the action when the audit cannot be
// written.
func (a *acct) audit(d gates.Decision) *stop {
	if err := a.r.audit(a.e, d, a.s.entry.URL, false); err != nil {
		return a.fail(ClassEnvironment, "", "%s", err.Error())
	}
	return nil
}

func (a *acct) signIn(ctx context.Context, w *wall) (string, *stop) {
	creds := a.r.d.Page.Credentials
	if creds == nil {
		return "", a.fail(ClassCredential, "sign-in", "a sign-in wall stands at %s and no credential store is configured", a.host)
	}
	cands, err := creds.Candidates(ctx, a.host, a.system, a.tenant)
	if err != nil {
		return "", a.fail(ClassEnvironment, "", "the stored credentials could not be read: %v", err)
	}
	a.cands = cands
	known, hadSite := false, false
	for _, c := range cands {
		hadSite = hadSite || c.Kind == credentials.KindSite
		if c.Kind == credentials.KindTenant || (!credentials.IsNamed(a.system) && c.Kind == credentials.KindSite) {
			known = true
		}
	}
	hold, err := creds.Held(ctx, a.host)
	if err != nil {
		return "", a.fail(ClassEnvironment, "", "the host's hold record could not be read: %v", err)
	}
	if hold.Held {
		a.rec.Held, a.rec.Lockout = true, hold.Lockout
		board := "credential-refused"
		if hold.Lockout {
			board = "lockout"
		}
		d := gates.PasswordSubmission(hold.Facts)
		if st := a.audit(d); st != nil {
			return "", st
		}
		// A reset or a sign-up submits no guess, so a hold earlier today blocks
		// neither; a locked account exists and gets no sign-up.
		if detail, stp, ran := a.tryReset(ctx, false); ran {
			return detail, stp
		}
		if !hold.Lockout && !known && !hadSite {
			return a.signUpOrStop(ctx, w, "the shared passwords for "+a.host+" ran out earlier today and no account is known")
		}
		return "", a.fail(ClassCredential, board, "the passwords for %s ran out earlier today (a hold); no password is submitted", a.host)
	}
	if w.registration && known && w.signin != nil && !submitsForm(w.signin) {
		// A sign-up view with its own Log in toggle (YC), for a host the store
		// holds a sign-in for: the toggle leads to the sign-in form.
		p, stp := a.toggleSignin(ctx, w.signin)
		if stp != nil {
			return "", stp
		}
		w = p
	}
	if w.registration {
		// A page that registers an account is never a place to type a stored
		// password; the sign-up decision is the only thing left to make.
		return a.signUpOrStop(ctx, w, a.host+" asks for a new account (its password box asks for a new password)")
	}
	if len(cands) == 0 {
		return a.signUpOrStop(ctx, w, "no sign-in is saved for "+a.host)
	}
	tried := map[string]bool{}
	for _, c := range cands {
		if slices.Contains(a.rec.Tried, c.Label) {
			continue // this run already tried it on this host (a re-entry at the gate)
		}
		key := strings.ToLower(c.Email) + "\x00" + c.Password
		d := gates.PasswordSubmission(gates.PasswordInput{Refused: a.refused, AlreadyRefused: tried[key], AccountKnown: known, LockoutSeen: a.lockout, NamedRetry: a.e.Run.Input.NamedRetry})
		if st := a.audit(d); st != nil {
			return "", st
		}
		if !d.Allowed() {
			if d.Code == gates.CodeGuessHeadroom {
				break // the kept submission belongs to the sign-in after a sign-up
			}
			if detail, stp, ran := a.tryReset(ctx, false); ran {
				return detail, stp
			}
			return "", a.exhausted(ctx, d.Reason)
		}
		tried[key] = true
		a.rec.Tried = append(a.rec.Tried, c.Label)
		res, stp := a.attempt(ctx, w, c)
		if stp != nil {
			return "", stp
		}
		switch res {
		case attemptSignedIn:
			a.rec.Succeeded = c.Label
			if err := creds.Keep(ctx, a.host, a.system, a.tenant, c); err != nil {
				a.e.Run.Warnings = append(a.e.Run.Warnings, "the working credential could not be kept: "+err.Error())
			}
			return "signed in with " + c.Label, nil
		case attemptVerify:
			a.rec.Succeeded = c.Label
			_ = creds.Keep(ctx, a.host, a.system, a.tenant, c) // "verify your email" proves the password
			return a.verify(ctx, w, c, gates.PreconditionBanner, time.Time{})
		case attemptLockout:
			a.lockout = true
			a.rec.Lockout = true
			return "", a.exhausted(ctx, "the host answered with lockout wording")
		}
		a.refused++
		a.rec.Refusals = a.refused
		w = refreshWall(ctx, a.s, w)
	}
	// Every guess was refused. An account the store does not know (no tenant
	// key, no site override) may not exist yet: the host gets a sign-up, whose
	// own "already exists" verdict leads to a reset.
	if !known && !hadSite {
		return a.signUpOrStop(ctx, w, "the shared passwords were refused and no account is known on "+a.host)
	}
	if detail, stp, ran := a.tryReset(ctx, false); ran {
		return detail, stp
	}
	return "", a.exhausted(ctx, "every stored password was refused")
}

func refreshWall(ctx context.Context, s *session, old *wall) *wall {
	if st, err := Probe(ctx, s.tab); err == nil {
		if w := findWall(st); w != nil {
			return w
		}
	}
	return old
}

// exhausted records the host's passwords as run out for the day and stops the
// run as a credential stop.
func (a *acct) exhausted(ctx context.Context, why string) *stop {
	a.rec.Exhausted = true
	if cr := a.r.d.Page.Credentials; cr != nil {
		wctx := context.WithoutCancel(ctx)
		err := a.r.d.Store.Write(wctx, func(tx *sql.Tx) error {
			return cr.RecordExhausted(wctx, tx, a.host, a.lockout, a.e.Run.ID)
		})
		if err != nil {
			a.e.Run.Warnings = append(a.e.Run.Warnings, "the hold could not be recorded: "+err.Error())
		}
	}
	board := "credential-refused"
	if a.lockout {
		board = "lockout"
	}
	return a.fail(ClassCredential, board, "no stored password signed in on %s (%s); the host is held for the rest of the day%s%s", a.host, why, a.unprovenOverride(ctx), a.resetNote())
}

// resetNote names what a reset the gate allowed lacked: a reset sets only a
// password the user saved, never one the run made up.
func (a *acct) resetNote() string {
	if !a.resetUnsaved {
		return ""
	}
	return fmt.Sprintf("; a password reset may run on %s but sets only a saved password - save one for this site or a shared default in Settings, then run again", a.host)
}

// unprovenOverride names the Settings action when the host's own saved
// password was refused and has never signed in anywhere: the user typed it and
// it may simply be wrong. The run never deletes it, because the index cannot
// say who wrote it.
func (a *acct) unprovenOverride(ctx context.Context) string {
	cr := a.r.d.Page.Credentials
	if cr == nil || credentials.IsNamed(a.system) {
		return ""
	}
	for _, c := range a.cands {
		if c.Kind != credentials.KindSite || !slices.Contains(a.rec.Tried, c.Label) {
			continue
		}
		if ok, err := cr.SignedInBefore(ctx, c.Name); err == nil && !ok {
			return fmt.Sprintf("; the password saved for %s has never signed in there - delete or correct it in Settings", a.host)
		}
	}
	return ""
}

// importSession asks the user's everyday Chrome for exactly this host's
// session, never google.com or youtube.com, once per host per run, and loads
// reload once the cookies landed (a caller that reopens the page itself passes
// ""). It reports whether this call imported them; the sync keeps its
// snapshot, so the import can be undone from the Browser page.
func (a *acct) importSession(ctx context.Context, reload string) (bool, *stop) {
	sy := a.r.d.Page.Sessions
	if sy == nil {
		return false, nil
	}
	for _, never := range neverSyncFor {
		if hostIs(a.host, never) {
			a.rec.Session = "never"
			return false, nil
		}
	}
	if a.e.sessionAsked == nil {
		a.e.sessionAsked = map[string]bool{}
	}
	if a.e.sessionAsked[a.host] {
		return false, nil
	}
	a.e.sessionAsked[a.host] = true
	res, err := sy.SyncOwnHost(ctx, a.host)
	switch {
	case err != nil:
		a.rec.Session = "failed"
	case res.Imported:
		a.rec.Session = "imported"
		if reload == "" {
			return true, nil
		}
		if _, nerr := a.s.tab.Navigate(ctx, reload); nerr != nil {
			return true, a.fail(ClassEnvironment, "", "the page could not be reloaded after the session import: %v", nerr)
		}
		return true, nil
	default:
		a.rec.Session = "not available: " + firstNonEmpty(res.Reason, "no-cookies")
	}
	return false, nil
}

// syncNote says, for a stop's reason, what the session sync from the user's
// everyday Chrome did for the host: "" when none was asked.
func (a *acct) syncNote() string {
	switch a.rec.Session {
	case "", "never":
		return ""
	case "imported":
		return "; the session imported from your Chrome did not sign in to " + a.host
	case "not available: no-cookies":
		return "; your Chrome holds no sign-in for " + a.host
	case "not available: extension-silent":
		return "; no Applysmith extension in your Chrome answered (pair it under Settings, Browser extension), so no sign-in for " + a.host + " was synced"
	case "not available: timeout":
		return "; your Chrome did not answer the sign-in sync for " + a.host + " in time"
	}
	return "; the sign-in sync for " + a.host + " from your Chrome failed"
}

type attemptResult int

const (
	attemptSignedIn attemptResult = iota
	// attemptNoAnswer is a press the page never answered: neither a refusal nor
	// a sign-in, so it spends no refusal and no further password is submitted.
	attemptNoAnswer
	attemptRefused
	attemptVerify
	attemptLockout
)

// typeInto focuses a probed input and types text with trusted key events.
func (a *acct) typeInto(ctx context.Context, in *InputInfo, text string) *stop {
	ok, err := Focus(ctx, a.s.tab, in.I)
	if err != nil || !ok {
		return a.fail(ClassDefect, "", "the %q box could not be focused", in.Label)
	}
	if err := a.s.tab.TypeText(ctx, text); err != nil {
		return a.fail(ClassEnvironment, "", "typing failed: %v", err)
	}
	return nil
}

// typeSecret types a password, and only into a password box: a handle that no
// longer names one types nothing.
func (a *acct) typeSecret(ctx context.Context, in *InputInfo, text string) *stop {
	ok, err := FocusSecret(ctx, a.s.tab, in.I)
	if err != nil || !ok {
		return a.fail(ClassDefect, "", "the %q box is not a password box at the moment of typing; no password was typed", in.Label)
	}
	if err := a.s.tab.TypeText(ctx, text); err != nil {
		return a.fail(ClassEnvironment, "", "typing failed: %v", err)
	}
	return nil
}

// press presses a control of the wall through gates.PressControl and reports
// whether the click landed.
func (a *acct) press(ctx context.Context, c *Control) *stop {
	return a.pressAs(ctx, c, gates.StageAccount)
}

// pressFlow presses a control of an account flow beside the wall: the terms
// link, the Accept of its dialog, the tenant's request-verification control.
func (a *acct) pressFlow(ctx context.Context, c *Control) *stop {
	return a.pressAs(ctx, c, gates.StageAccountFlow)
}

// maxSettles is how often an account press reads a covered control again.
const maxSettles = 4

func (a *acct) pressAs(ctx context.Context, c *Control, stage gates.ControlStage) *stop {
	loc, err := Locate(ctx, a.s.tab, c.I)
	// A dialog still drawing itself (Workday's) covers a control for a moment.
	for try := 0; err == nil && try < maxSettles && loc.Found && loc.Visible && !loc.Hit; try++ {
		if !sleepCtx(ctx, a.tm.Poll) {
			return cancelledStop(StageAccount)
		}
		loc, err = Locate(ctx, a.s.tab, c.I)
	}
	if err != nil {
		return a.fail(ClassEnvironment, "", "the control could not be located: %v", err)
	}
	d := gates.PressControl(gates.ControlInput{Unattended: a.e.unattended(), Stage: stage, Found: loc.Found && loc.Visible, Labels: loc.Labels, InDialog: c.Modal, AllowGoogleSignIn: stage == gates.StageGoogleSignIn && a.r.d.Page.Settings != nil && a.r.d.Page.Settings().AllowGoogleSignIn})
	if st := a.audit(d); st != nil {
		return st
	}
	if !d.Allowed() {
		return a.fail(ClassCredential, "sign-in", "the control %q was not pressed: %s", strings.Join(loc.Labels, " / "), d.Reason)
	}
	if !loc.Hit {
		return a.fail(ClassDefect, "", "the control is covered by %s; nothing was pressed", loc.Blocker)
	}
	if err := a.s.tab.Click(ctx, loc.X, loc.Y); err != nil && !lateClick(err) {
		return a.fail(ClassEnvironment, "", "the press failed: %v", err)
	}
	return nil
}

// lateClick reports a click whose input dispatch outlived its bound: a busy
// page (Workday drawing its next view) holds the dispatch after the press
// landed. The press is never repeated; what the page shows next is the verdict.
func lateClick(err error) bool {
	code, ok := browser.CodeOf(err)
	return ok && code == browser.CodeOperationTimeout
}

// attempt submits one candidate and reads what the page answers. A
// registration view with its own sign-in toggle (Workday draws Create Account
// a moment before its Sign In view, so a re-read can land on it) is toggled to
// the sign-in form the stored password belongs on.
func (a *acct) attempt(ctx context.Context, w *wall, c credentials.Candidate) (attemptResult, *stop) {
	if w.registration && w.signin != nil && !submitsForm(w.signin) {
		p, stp := a.toggleSignin(ctx, w.signin)
		if stp != nil {
			return attemptRefused, stp
		}
		w = p
	}
	if w.registration {
		return attemptRefused, a.fail(ClassDefect, "", "the page registers an account; a stored password is never typed into it")
	}
	if w.password == nil {
		// A wall re-read after a sign-up or a refusal can be the social-first
		// view (UT Austin's Workday); its email form holds the password box.
		st, err := Probe(ctx, a.s.tab)
		if err != nil {
			return attemptRefused, a.fail(ClassEnvironment, "", "the sign-in page could not be read: %v", err)
		}
		_, nw, stp := a.openEmailForm(ctx, st, w)
		if stp != nil {
			return attemptRefused, stp
		}
		w = nw
		if w.password == nil {
			return attemptRefused, a.fail(ClassCredential, "sign-in", "the sign-in page on %s shows no password box; no password was typed%s", a.host, a.syncNote())
		}
	}
	start := time.Now()
	// Evidence point 5: a checkbox captcha on a generic sign-in is solved before
	// the credential is typed; when it is not, nothing is typed.
	if st, err := Probe(ctx, a.s.tab); err == nil {
		if d := DetectChallenge(st); d != nil && unsolved(st, d) {
			if _, ok := a.r.solveAt(ctx, a.e, a.s, d, gates.PointGenericSignin); !ok {
				return attemptRefused, a.fail(ClassCredential, "challenge", "a captcha stands on the sign-in page and is yours; no password was typed")
			}
		}
	}
	if st := a.signinAgreement(ctx); st != nil {
		return attemptRefused, st
	}
	if w.email != nil {
		if st := a.typeInto(ctx, w.email, c.Email); st != nil {
			return attemptRefused, st
		}
	}
	if st := a.typeSecret(ctx, w.password, c.Password); st != nil {
		return attemptRefused, st
	}
	if w.signin == nil {
		return attemptRefused, a.fail(ClassCredential, "sign-in", "the page has no sign-in control the stage may press")
	}
	signin := w.signin
	// Typing can redraw the Sign In (Workday), detaching the handle read before.
	if nw := refreshWall(ctx, a.s, w); nw.password != nil && nw.password.I == w.password.I && nw.signin != nil {
		signin = nw.signin
	}
	if !a.dwell(ctx, start) {
		return attemptRefused, cancelledStop(StageAccount)
	}
	pre := a.signature(ctx)
	watch, _ := a.s.tab.WatchResponses(ctx)
	if watch != nil {
		defer watch.Stop()
	}
	if st := a.press(ctx, signin); st != nil {
		return attemptRefused, st
	}
	res, stp := a.read(ctx, pre)
	if res == attemptNoAnswer {
		return res, a.noAnswer(ctx, watch)
	}
	return res, stp
}

// signedIn re-checks, without submitting anything, whether the sign-in wall,
// an email-first sign-in included, is gone from a loaded page for the stable
// interval. A signed-in reading that began before the deadline is followed to
// the end of its interval, so a slow read does not cut it short.
func (a *acct) signedIn(ctx context.Context) (bool, error) {
	var since time.Time
	deadline := time.Now().Add(a.tm.Stable * 3)
	for time.Now().Before(deadline) || !since.IsZero() {
		// A page between documents cannot be read (Eightfold reloads after the
		// code): it is no answer yet.
		if st, err := Probe(ctx, a.s.tab); err == nil && findWall(st) == nil && readEmailStep(st) == nil && !st.Loading && !awaitsCode(st) && st.Account != accountSignedOut {
			if since.IsZero() {
				since = time.Now()
			}
			if time.Since(since) >= a.tm.Stable {
				return true, nil
			}
		} else {
			since = time.Time{}
		}
		if !sleepCtx(ctx, a.tm.Poll) {
			return false, ctx.Err()
		}
	}
	return false, nil
}

// awaitsCode reports a page that still asks for an emailed code: the account
// exists but is not usable yet.
func awaitsCode(st *PageState) bool {
	boxes, _ := codeBoxes(st)
	return len(boxes) > 0 && verifyRE.MatchString(st.Text+" "+strings.Join(st.Headings, " "))
}

// dwell holds a Workday account submit until the stage has worked on the form
// for tm.Dwell. Workday's spam-bot filter fails a submit made within five
// seconds of the form mounting and drops it without a word: no request, no
// message (GM, CVS, Rockwell, live). since is when the stage began on the form.
func (a *acct) dwell(ctx context.Context, since time.Time) bool {
	if d := time.Until(since.Add(a.tm.Dwell)); a.system == ATSWorkday && d > 0 {
		return sleepCtx(ctx, d)
	}
	return true
}

func sleepCtx(ctx context.Context, d time.Duration) bool {
	select {
	case <-time.After(d):
		return true
	case <-ctx.Done():
		return false
	}
}

// signature reads the page fingerprint, retrying once; "" means it could not
// be read.
func (a *acct) signature(ctx context.Context) string {
	for i := 0; i < 2; i++ {
		if st, err := Probe(ctx, a.s.tab); err == nil {
			return st.Fingerprint()
		}
		sleepCtx(ctx, a.tm.Poll)
	}
	return ""
}

// toggleSignin presses a sign-up view's own Log in toggle and returns the
// sign-in wall it shows. A view that asks for the email first is not finished
// by the stage.
func (a *acct) toggleSignin(ctx context.Context, toggle *Control) (*wall, *stop) {
	if st := a.press(ctx, toggle); st != nil {
		return nil, st
	}
	p := a.waitFor(ctx, func(p *PageState) bool { w := findWall(p); return w != nil && w.password != nil && !w.registration })
	if p == nil {
		return nil, a.fail(ClassCredential, "sign-in", "the Log in toggle on %s led to no sign-in form with a password box (it may ask for the email first); sign in in the browser, then resume%s", a.host, a.syncNote())
	}
	return findWall(p), nil
}
