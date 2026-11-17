package apply

import (
	"context"
	"fmt"
	"net/url"
	"strings"
	"time"

	"applysmith/internal/lazyregexp"
)

// maxHops bounds how often one run moves to another page of its application.
const maxHops = 3

var (
	notFoundRE = []*lazyregexp.Regexp{
		lazyregexp.New(`(?i)\bthe page you are looking for (?:doesn't|does not) exist\b`),
		lazyregexp.New(`(?i)\bpage not found\b`),
		lazyregexp.New(`(?i)\bwe (?:can'?t|cannot) find the page\b`),
	}
	// The board script of a Greenhouse board embedded through srcdoc. The slug
	// is a closed alphabet and must end where the attribute value does.
	srcdocBoardRE = lazyregexp.New(`(?i)<script[^>]+src=["']?https://(?:boards|job-boards)[.]greenhouse[.]io/embed/job_board/js[?]for=([A-Za-z0-9_-]+)(?:["'&\s>]|$)`)
	jobIDRE       = lazyregexp.New(`^[0-9]+$`)
	slugRE        = lazyregexp.New(`^[A-Za-z0-9_-]+$`)
)

// readsNotFound reports whether the page says it has no such page. A URL the
// chain built that the site does not serve is a fact about the construction,
// never about the posting. A page that cannot be read is not a 404.
func readsNotFound(ctx context.Context, s *session) bool {
	st, err := Probe(ctx, s.tab)
	if err != nil {
		return false
	}
	text := st.Text
	if len(text) > 4000 {
		text = text[:4000]
	}
	for _, re := range notFoundRE {
		if re.MatchString(text) {
			return true
		}
	}
	return false
}

// handoff moves the run to the page where its application lives, keeping the
// posting URL as the run's identity: the destination is classified, the hop is
// recorded as a resolve line, the page is opened, and the account stage runs
// there when it is asked to (a hop made before the account stage leaves it to
// the stage that follows).
func (r *Runner) handoff(ctx context.Context, e *Exec, s *session, dest, why string, account bool) *stop {
	start := r.d.Now()
	cls, st := Classify(dest)
	if st != nil || cls.ATS == ATSLinkedIn {
		return &stop{class: ClassPosting, reason: "the hand-off destination is not an application URL: " + redactURL(dest), at: StageResolve}
	}
	if e.hops >= maxHops || e.visited[cls.ApplyURL] {
		return &stop{class: ClassDefect, reason: "the application moved between pages without reaching a form: " + redactURL(cls.ApplyURL), at: StageResolve}
	}
	if e.visited == nil {
		e.visited = map[string]bool{}
	}
	e.visited[cls.ApplyURL] = true
	e.hops++
	e.Cls = cls
	e.Run.ATS, e.Run.Tenant = cls.ATS, cls.Tenant
	r.record(e, StageResolve, StatusOK, fmt.Sprintf("ats=%s tenant=%s (%s)", cls.ATS, cls.Tenant, why), start)
	if stp := r.openApplication(ctx, e, s); stp != nil {
		return stp
	}
	if !account {
		return nil
	}
	as := r.d.Now()
	detail, stp := r.account(ctx, e, s)
	if stp != nil {
		r.record(e, StageAccount, StatusFailed, stp.reason, as)
		if stp.at == "" {
			// The stop is the account stage's own, as the stage's first run
			// records it; the walk would end it at fill.
			stp.at = StageAccount
		}
		return stp
	}
	if !strings.HasPrefix(detail, "no sign-in wall") {
		r.record(e, StageAccount, StatusOK, detail, as)
	}
	return nil
}

// target is the URL the run was asked to apply to, or the employer's
// posting a list resolved it to.
func (e *Exec) target() string {
	in := e.Run.Input
	if in.EmployerURL != "" && IsAggregatorURL(in.URL) {
		return in.EmployerURL
	}
	return in.URL
}

// handoffTarget reads a page with no form for the place its application lives:
// an application frame of a named system (by address, or through a Greenhouse
// board's inline document that the run's own URL names the job of), or, on the
// first page, the one link whose text is exactly the evaluated role.
func (w *walker) handoffTarget(st *PageState) (dest, why string) {
	if w.e.hops >= maxHops {
		return "", ""
	}
	if dest, why = embeddedApplication(st, w.e.target()); dest != "" {
		return dest, why
	}
	if w.pages == 0 && w.opens == 0 {
		return w.listedPosting(st)
	}
	return "", ""
}

// awaitEmbedded looks for an embedded application frame on the first page,
// polling for the first-look interval: an embed script inserts its frame
// after the page settles.
func (w *walker) awaitEmbedded(ctx context.Context, st *PageState) (dest, why string) {
	deadline := time.Now().Add(w.tm.First)
	for {
		if dest, why = embeddedApplication(st, w.e.target()); dest != "" || !time.Now().Before(deadline) || !w.sleep(ctx, w.tm.Poll) {
			return dest, why
		}
		next, err := Probe(ctx, w.s.tab)
		if err != nil {
			return "", ""
		}
		st = next
	}
}

func embeddedApplication(st *PageState, runURL string) (dest, why string) {
	for _, f := range st.Frames {
		if !f.Visible || f.W <= 0 || f.H <= 0 {
			continue
		}
		if f.Src != "" && (strings.HasPrefix(f.Src, "http://") || strings.HasPrefix(f.Src, "https://")) {
			cls, stp := Classify(f.Src)
			if stp != nil || cls.ATS == ATSGeneric || cls.ATS == ATSLinkedIn {
				continue
			}
			return f.Src, "embedded " + cls.ATS + " application frame"
		}
		if !f.Srcdoc {
			continue
		}
		// The inline document names the board, the page's own address names the
		// job; together they are that application's embed address, and only for
		// the job this run was asked about.
		jid := jobIDParam(st.URL)
		m := srcdocBoardRE.FindStringSubmatch(f.SrcdocText)
		if m == nil || jid == "" || jid != jobIDParam(runURL) {
			continue
		}
		q := url.Values{"for": {m[1]}, "token": {jid}}
		return "https://job-boards.greenhouse.io/embed/job_app?" + q.Encode(), "embedded greenhouse application frame"
	}
	// The page's own embed script names the board even when the frame it
	// draws never appears; the page's address names the job, which must be
	// the run's.
	if jid := jobIDParam(st.URL); slugRE.MatchString(st.GreenhouseBoard) && jid != "" && jid == jobIDParam(runURL) {
		q := url.Values{"for": {st.GreenhouseBoard}, "token": {jid}}
		return "https://job-boards.greenhouse.io/embed/job_app?" + q.Encode(), "greenhouse board embedded in the page"
	}
	return "", ""
}

func jobIDParam(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	if v := u.Query().Get("gh_jid"); jobIDRE.MatchString(v) {
		return v
	}
	return ""
}

// listedPosting follows a careers listing to the posting it links by the
// evaluated role's exact title. The text is compared whole: a listing shows
// neighbouring roles, and following the wrong one is the incident the role
// guard exists for. The guard reads the new first page again.
func (w *walker) listedPosting(st *PageState) (dest, why string) {
	run := w.e.Run
	want := run.Input.Role
	if run.Report != nil {
		want = run.Report.Role
	}
	if strings.TrimSpace(want) == "" || PageRole(st.Title, st.Headings, "") != "" {
		return "", ""
	}
	hrefs := map[string]bool{}
	for _, l := range st.Links {
		if normText(l.Text) == normText(want) && l.Href != redactedSame(st.URL, l.Href) {
			hrefs[l.Href] = true
		}
	}
	if len(hrefs) != 1 {
		return "", ""
	}
	for h := range hrefs {
		return h, "the tracker link opened a careers listing; the evaluated role is linked from it at " + redactURL(h)
	}
	return "", ""
}

// redactedSame returns href when it is the page itself (a link to the page
// being read is not a hop), else "".
func redactedSame(page, href string) string {
	if redactURL(page) == redactURL(href) {
		return href
	}
	return ""
}

// popupCapturer is a tab that reports the address a press asked a new window for
// (window.open, a target=_blank link); the new window is closed.
type popupCapturer interface {
	CapturePopup(ctx context.Context, act func(context.Context) error) (string, error)
}

// pressOpener presses an Apply control and returns the address of a window
// it opened instead of moving this tab (jobright's "Apply with Autofill"): the
// run holds one tab, so the caller opens that address here. A press that
// moved this tab waits for no window, unless the control is a link that opens
// a new window: its press may also redraw this page (Phenom's chat panel
// answers it), and the window is still where it leads.
func (w *walker) pressOpener(ctx context.Context, st *PageState, c Control) (popup string, moved bool, s *stop) {
	pt, ok := w.s.tab.(popupCapturer)
	if !ok {
		moved, s = w.press(ctx, st, c)
		return "", moved, s
	}
	newWindow := c.Tag == "a" && strings.EqualFold(c.Target, "_blank")
	wait, done := context.WithCancel(ctx)
	defer done()
	url, err := pt.CapturePopup(wait, func(context.Context) error {
		moved, s = w.press(ctx, st, c)
		if s != nil || moved && !newWindow {
			done()
		}
		return nil
	})
	if s != nil || err != nil {
		return "", moved, s
	}
	return url, moved, nil
}

// arrived reads the page an Apply control led to: a page of another named
// system (a careers site's Apply that lands on a Workday job page) is handed
// off to that system's application address, so Workday's rewrite skips its
// "Autofill with Resume / Apply Manually" chooser. A page already at that
// address is only classified. It reports whether the run moved.
func (w *walker) arrived(ctx context.Context) (bool, *stop) {
	st, err := Probe(ctx, w.s.tab)
	if err != nil {
		return false, nil
	}
	cls, bad := Classify(st.URL)
	if bad != nil || cls.ATS == "" || cls.ATS == ATSGeneric || cls.ATS == ATSLinkedIn || cls.ATS == w.e.Run.ATS {
		return false, nil
	}
	if cls.ApplyURL == st.URL {
		w.e.Cls = cls
		w.e.Run.ATS, w.e.Run.Tenant = cls.ATS, cls.Tenant
		w.r.record(w.e, StageResolve, StatusOK, fmt.Sprintf("landed on ats=%s tenant=%s", cls.ATS, cls.Tenant), w.r.d.Now())
		return false, nil
	}
	return true, w.r.handoff(ctx, w.e, w.s, st.URL, "the Apply control led to a "+cls.ATS+" page", true)
}
