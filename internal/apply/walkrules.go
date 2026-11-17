package apply

import (
	"context"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"

	"applysmith/internal/lazyregexp"
)

var (
	signInToApplyRE = lazyregexp.New(`(?i)^(?:sign|log)\s*in\s+to\s+apply\b`)
	// accountTitleRE names an account page: sign-in, register, create-account.
	accountTitleRE = lazyregexp.New(`(?i)\b(?:sign\s*in|log\s*in|login|register|create\s+(?:an\s+)?account|sign\s*up)\b`)
	// bareGateRE is the heading of an identifier-first page: "Log in or create
	// account", or a sign-in heading alone.
	bareGateRE   = lazyregexp.New(`(?i)\b(?:log|sign)\s*in\s+(?:or|and|/)\s+(?:create|sign\s*up|register)\b|^(?:please\s+)?(?:log|sign)\s*in$`)
	locationRE   = lazyregexp.New(`(?i)\b(?:location|city|state|province|region|county|zip|postal|address|country)\b`)
	emailLabelRE = lazyregexp.New(`(?i)e-?mail|username`)
	// siteErrorRE is a site's own error page that asks for a reload (Workday:
	// "Something went wrong. Please refresh the page and then try again.").
	siteErrorRE = lazyregexp.New(`(?i)\bsomething went wrong\b[\s\S]{0,200}?\b(?:refresh|reload)(?:\s+the)?\s+page\b`)
	// clauseWordRE is a word of a sentence, not of a field name: a subject
	// pronoun, a form of "be", "do" or "have", a modal verb or a question word.
	clauseWordRE = lazyregexp.New(`(?i)\b(?:i|we|you|am|is|are|was|were|be|been|do|does|did|have|has|had|can|could|will|would|shall|should|may|might|must|what|which|where|when|whether|who|how|why)\b`)
)

// siteErrorPage reports whether the page is the site's own error page that asks
// for a reload.
func siteErrorPage(st *PageState) bool {
	return siteErrorRE.MatchString(strings.Join(st.Lines, "\n"))
}

// signInWall returns the label of the page's own "Sign in to apply" control
// when it is the page's wall: one such control, no Apply opener and no form.
// A list of other jobs' sign-in buttons (several of them) is not this
// posting's wall. The label is read as the page shows it.
func signInWall(st *PageState) string {
	var found []string
	for _, c := range st.Controls {
		if c.Chat || (c.Chrome && !c.Modal) {
			continue
		}
		for _, l := range c.Labels {
			if signInToApplyRE.MatchString(strings.TrimSpace(l)) {
				found = append(found, l)
				break
			}
		}
	}
	if len(found) != 1 || len(Categorize(st, false).Openers) > 0 {
		return ""
	}
	return found[0]
}

// isBoardURL reports a job board's own page, where a sign-in is the board's
// account and the chain never signs up: a recognized mirror, or any Indeed
// page.
func isBoardURL(raw string) bool {
	if IsAggregatorURL(raw) {
		return true
	}
	u, err := url.Parse(raw)
	return err == nil && hostIs(strings.ToLower(u.Hostname()), "indeed.com")
}

// bareSignInGate reads an identifier-first gate: one email or username box, no
// upload and no other field, under a heading that offers to log in or create
// an account. A real single-page application (name, email, résumé) on a
// login-looking host has more than one field and is not a gate.
func bareSignInGate(st *PageState) bool {
	fields, files := st.FormFields()
	if fields != 1 || files != 0 {
		return false
	}
	for _, in := range st.Inputs {
		if in.Furniture || in.Type == "hidden" {
			continue
		}
		if in.Type != "email" && !emailLabelRE.MatchString(in.Label+" "+in.Name+" "+in.Autocomplete) {
			return false
		}
	}
	for _, h := range append([]string{st.Title}, st.Headings...) {
		if bareGateRE.MatchString(strings.TrimSpace(h)) {
			return true
		}
	}
	return false
}

// accountPageFirst reads a first page that is an account page: a title or
// heading naming sign-in, register or create-account and no job noun.
func accountPageFirst(st *PageState) bool {
	// The site's own error page keeps the title of the step it replaced (P&G's
	// Workday: "Create Account"); it is reloaded, never an account page.
	if PageRole(st.Title, st.Headings, "") != "" || siteErrorPage(st) {
		return false
	}
	for _, h := range append([]string{st.Title}, st.Headings...) {
		h = strings.TrimSpace(h)
		if h != "" && accountTitleRE.MatchString(h) && !jobNounRE.MatchString(h) {
			return st.HasForm() || len(Categorize(st, false).Openers) == 0
		}
	}
	return false
}

// stallWords explains a stalled step: what the page marked as a refusal, else
// what it shows of one in a red message and the required boxes still empty.
// The hints only reach this text; the refusal reader never sees them.
func stallWords(st *PageState) string {
	if w := validationWords(st); w != "" {
		return w
	}
	var parts []string
	if len(st.Stall.Messages) > 0 {
		parts = append(parts, "the page says: "+strings.Join(st.Stall.Messages, " / "))
	}
	if n := len(st.Stall.Empty); n > 0 {
		q := make([]string, n)
		for i, l := range st.Stall.Empty {
			q[i] = strconv.Quote(l)
		}
		parts = append(parts, fmt.Sprintf("%d required field(s) still empty: %s", n, strings.Join(q, ", ")))
	}
	if len(parts) == 0 {
		return ""
	}
	return "; " + strings.Join(parts, "; ")
}

// Revalidation is what one FormValidation re-validation did; field names only.
type Revalidation struct {
	HiddenReleased []string `json:"hiddenReleased"`
	Revalidated    []string `json:"revalidated"`
	Invalid        []string `json:"invalid"`
}

// revalidateForm asks a FormValidation form once more about its fields in the
// page's main world. It returns nil when the page carries no such instance.
func revalidateForm(ctx context.Context, t Tab) (*Revalidation, error) {
	var rv *Revalidation
	if err := t.Eval(ctx, probeExpr("revalidate", nil), &rv); err != nil {
		return nil, err
	}
	if rv != nil {
		// A deferred validator settles on a later tick.
		sleepCtx(ctx, 300*time.Millisecond)
	}
	return rv, nil
}

// sameSite reports whether two hosts belong to one site: equal, or sharing
// their registrable domain.
func sameSite(a, b string) bool {
	a, b = strings.ToLower(a), strings.ToLower(b)
	return a == b || registrable(a) == registrable(b)
}

var twoLevelSLD = map[string]bool{"co": true, "com": true, "org": true, "net": true, "gov": true, "ac": true, "edu": true}

func registrable(host string) string {
	labels := strings.Split(strings.TrimSuffix(host, "."), ".")
	n := len(labels)
	switch {
	case n <= 2:
		return strings.Join(labels, ".")
	case len(labels[n-1]) == 2 && twoLevelSLD[labels[n-2]]:
		return strings.Join(labels[n-3:], ".")
	}
	return strings.Join(labels[n-2:], ".")
}

// pathDepth counts the non-empty segments of a URL path.
func pathDepth(raw string) int {
	u, err := url.Parse(raw)
	if err != nil {
		return 0
	}
	return len(segments(u.Path))
}

// careersHomeRE is the root of a careers site: the site's root, its careers or
// jobs index, with an optional locale.
var careersHomeRE = lazyregexp.New(`(?i)^/(?:[a-z]{2}(?:-[a-z]{2})?/)?(?:careers?|jobs?|join(?:-us)?|work-with-us|opportunities|openings|search(?:-jobs)?)?/?$`)

var siteErrorPathRE = lazyregexp.New(`(?i)/(?:404|not-?found|page-?not-?found|error|errors?/404)(?:[/.?]|$)`)

// redirectedAway reads where a posting URL ended up. A posting that answers by
// redirecting to the same site's error page ("Page not found"), or to the
// careers home from a deeper address, is a closed posting; the reason says
// which. An address that did not move says nothing.
func redirectedAway(requested, final string, notFoundText bool) string {
	if requested == "" || final == "" || redactURL(requested) == redactURL(final) {
		return ""
	}
	r, err1 := url.Parse(requested)
	f, err2 := url.Parse(final)
	if err1 != nil || err2 != nil || !sameSite(r.Hostname(), f.Hostname()) {
		return ""
	}
	switch {
	case siteErrorPathRE.MatchString(f.Path) && !siteErrorPathRE.MatchString(r.Path), notFoundText && r.Path != f.Path:
		return "redirected to the site's error page"
	// Greenhouse sends the form of a job it no longer lists to its board with
	// error=true.
	case f.Query().Get("error") == "true" && r.Query().Get("error") != "true":
		return "redirected to the site's error page"
	case pathDepth(requested) >= 2 && careersHomeRE.MatchString(f.Path):
		return "redirected to the careers home"
	}
	return ""
}

// finalStep reports a field-less step after a form page that offers the form's
// Submit: a widget there is cleared by the submit stage, after the
// authorization and the claim.
func (w *walker) finalStep(st *PageState) bool {
	return w.pages > 0 && len(Categorize(st, true).Submit) > 0
}

// sentAway names a walk that ended on the careers home of the posting's site,
// away from the posting (RWE's Apply opened its job portal); empty otherwise.
func (w *walker) sentAway(st *PageState) string {
	if why := redirectedAway(w.e.target(), st.URL, false); why != "" {
		return "the walk was " + why + " (" + redactURL(st.URL) + "); "
	}
	return ""
}

// noteHost names the first hop the application made to another site; a walk
// that stays on the posting's site or a subdomain of it names nothing.
func (w *walker) noteHost(pageURL string) {
	run := w.e.Run
	if run.LeftForAnotherHost != "" {
		return
	}
	from, to := hostOf(w.e.target()), hostOf(pageURL)
	if from != "" && to != "" && !sameSite(from, to) {
		run.LeftForAnotherHost = to
	}
}

func hostOf(raw string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return ""
	}
	return strings.ToLower(u.Hostname())
}

// revalidate re-validates a FormValidation form once per page before a
// disabled Submit ends the walk. It returns the page read again and, when the
// library still refuses fields, the sentence naming them. Nothing is returned
// for a page without such a form or one already tried.
func (w *walker) revalidate(ctx context.Context, st *PageState) (*PageState, string) {
	key := stateKey(st)
	if len(st.Validation) > 0 || w.revalidated[key] {
		return nil, ""
	}
	w.revalidated[key] = true
	rv, err := revalidateForm(ctx, w.s.tab)
	if err != nil || rv == nil {
		return nil, ""
	}
	again, err := Probe(ctx, w.s.tab)
	if err != nil {
		return nil, ""
	}
	still := ""
	if len(rv.Invalid) > 0 {
		still = "; the form library still refuses: " + strings.Join(rv.Invalid, ", ")
	}
	return again, still
}

// enabledLater re-reads a page whose Submit control is disabled with nothing
// said against the form, for up to PressWait: Workday draws its Review step
// with Submit disabled and enables it a moment later. It returns the page once
// Submit is enabled, nil when it stayed disabled.
func (w *walker) enabledLater(ctx context.Context) (*PageState, *stop) {
	deadline := time.Now().Add(w.tm.PressWait)
	for time.Now().Before(deadline) {
		if !w.sleep(ctx, w.tm.Poll) {
			return nil, cancelledStop(StageFill)
		}
		st, err := Probe(ctx, w.s.tab)
		if err != nil {
			return nil, w.browserStop(err)
		}
		if cats := Categorize(st, w.pages > 0); len(cats.Submit) > 0 && !cats.Submit[0].Disabled {
			return st, nil
		}
	}
	return nil, nil
}

// redirectClosed reads a navigation that ended on another address of the same
// site: the site's own error page or the careers home names the posting as
// gone. The page is read for its "not found" words only when the address
// moved.
func (r *Runner) redirectClosed(ctx context.Context, s *session, cls Classification, final string) string {
	requested := cls.ApplyURL
	if final == "" || redactURL(final) == redactURL(requested) {
		return ""
	}
	if why := redirectedAway(requested, final, false); why != "" {
		return why
	}
	if redactURL(final) == redactURL(cls.Posting) {
		return ""
	}
	return redirectedAway(requested, final, readsNotFound(ctx, s))
}

// readAnyControl reports whether the fill read at least one control on some
// page of the walk.
func (s *session) readAnyControl() bool {
	for _, ps := range s.pages {
		if len(ps.rows) > 0 {
			return true
		}
	}
	return false
}

// wroteApplication reports whether the walk left evidence of an application: a
// résumé, an answered question, a prefilled control or a timeline row. A walk
// that wrote only contact details (a name, an email, a location) is a newsletter
// or talent-community box and never presses Submit.
func (s *session) wroteApplication() bool {
	wrote := false
	for _, ps := range s.pages {
		if ps.timeline {
			return true
		}
		for _, r := range ps.rows {
			switch {
			case r.Outcome == "prefilled":
				return true
			case r.Outcome == "filled" && (r.Kind == "file" || !contactLabel(r.Label)):
				return true
			case r.Outcome == "filled":
				wrote = true
			}
		}
	}
	// Nothing written at all is no evidence either way; only contact details
	// written and nothing else is the box of a newsletter.
	return !wrote
}

// contactLabel reports whether a label names contact data: an identity field,
// or a place field such as "Country", "State / Province" or "Current location".
// A sentence is answered, not contact data, though it names a place: a label
// that asks a question ("Are you legally authorized to work in this
// country?"), or one that says a clause before the place it names, as a
// checkbox statement or a Yes/No without a question mark does ("I am
// authorized to work in this country"). A field name comes first, so help text
// after it ("Current Location We use this to ...") leaves it a place field.
func contactLabel(label string) bool {
	if _, ok := identityFieldOf(label); ok {
		return true
	}
	at := locationRE.FindStringIndex(label)
	return at != nil && !strings.Contains(label, "?") && !clauseWordRE.MatchString(label[:at[0]])
}

// maxGrowth bounds the fills one walk gives questions a page drew after an
// answer (ADP's Preliminary Questions asks up to 15, one after another).
const maxGrowth = 20

// moreOnThisPage looks for what a form page that offers nothing to press may
// still hold: a required question it drew after the fill answered the one
// before, which is filled again, or (after a press) ADP's code pane, which the
// site draws seconds after its Continue goes away while it mails the code.
func (w *walker) moreOnThisPage(ctx context.Context, st *PageState) (bool, *stop) {
	if w.grewAfterFill(st) && w.seen["grew"] < maxGrowth {
		w.seen["grew"]++
		return true, nil
	}
	if w.pages > 1 {
		return w.awaitCodeDialogs(ctx)
	}
	return false, nil
}

// grewAfterFill reports a required, empty box the last fill did not read, once
// per box: a box no refill reads is no growth of the page (Netflix).
func (w *walker) grewAfterFill(st *PageState) bool {
	if len(w.s.pages) == 0 {
		return false
	}
	rows := w.s.pages[len(w.s.pages)-1].rows
	for _, in := range st.Inputs {
		if !in.Required || in.Filled || in.Disabled || in.Furniture {
			continue
		}
		read := false
		for _, r := range rows {
			read = read || r.Selector == in.Selector || in.Name != "" && strings.Contains(r.Selector, `"`+in.Name+`"`)
		}
		if key := "grew|" + in.Selector; !read && w.seen[key] == 0 {
			w.seen[key]++
			return true
		}
	}
	return false
}
