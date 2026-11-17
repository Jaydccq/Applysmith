// Clean-room: docs/provenance.md#CR-LIVENESS

package discovery

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"applysmith/internal/identity"
	"applysmith/internal/tracker"
)

func longText(extra string) string {
	return strings.Repeat("We build reliable services for customers across the world. ", 6) + extra
}

func page(mod func(*PageRead)) PageRead {
	p := PageRead{Status: 200, FinalURL: "https://jobs.example.test/o/4001", Title: "Data Engineer - Example Corp", Heading: "Data Engineer",
		Text: longText("Example Corp is hiring."), HasApply: true}
	if mod != nil {
		mod(&p)
	}
	return p
}

func TestLiveness_RuleOrder(t *testing.T) {
	const req = "https://jobs.example.test/o/4001"
	exp := Expect{Company: "Example Corp", Role: "Data Engineer"}
	cases := []struct {
		name         string
		pg           PageRead
		exp          Expect
		verdict, why string
	}{
		{"404", page(func(p *PageRead) { p.Status = 404 }), exp, LiveExpired, "http-404"},
		{"410 beats a wall", page(func(p *PageRead) { p.Status = 410; p.PasswordForm = true }), exp, LiveExpired, "http-410"},
		{"redirect to the board index", page(func(p *PageRead) { p.FinalURL = "https://job-boards.greenhouse.io/example-co" }), exp, LiveExpired, "redirect-lost-job"},
		{"redirect to the localized careers home", page(func(p *PageRead) { p.FinalURL = "https://jobs.example.test/us/en" }), exp, LiveExpired, "redirect-lost-job"},
		{"login redirect is never expired", page(func(p *PageRead) { p.FinalURL = "https://jobs.example.test/login?next=o/4001"; p.HasApply = false }), exp, LiveUncertain, "login"},
		{"sign-in form only", page(func(p *PageRead) { p.PasswordForm = true; p.HasApply = false }), exp, LiveUncertain, "login"},
		{"challenge wall", page(func(p *PageRead) { p.Title = "Just a moment..."; p.Text = "Verify you are human"; p.HasApply = false }), exp, LiveUncertain, "challenge"},
		{"closure wording without apply", page(func(p *PageRead) { p.Text = longText("This job is no longer available."); p.HasApply = false }), exp, LiveExpired, "closure-wording"},
		{"workday requisition gone", page(func(p *PageRead) {
			p.Title = "Careers"
			p.Text = "CareersEnglishSign InThe page you are looking for doesn't exist.Privacy Policy" // text nodes joined as the reader joins them
			p.HasApply = false
		}), exp, LiveExpired, "closure-wording"},
		{"closure in a banner", page(func(p *PageRead) { p.Banner = "Applications are closed"; p.HasApply = false }), exp, LiveExpired, "closure-wording"},
		// A banner speaks for the site: only a sentence about a posting that
		// is not about cookies states this posting closed.
		{"a site banner about something else", page(func(p *PageRead) { p.Banner = "New look! Support for this browser has been removed." }), exp, LiveActive, "apply-control"},
		{"a cookie sentence in a banner", page(func(p *PageRead) { p.Banner = "This page uses cookies, and your earlier choice has been removed." }), exp, LiveActive, "apply-control"},
		{"open until filled is no closure", page(func(p *PageRead) { p.Text = longText("This posting will remain open until the position is filled.") }), exp, LiveActive, "apply-control"},
		{"pay at the level the role is filled is no closure", page(func(p *PageRead) {
			p.Text = longText("Compensation will be based primarily on the job level at which the role is filled.")
		}), exp, LiveActive, "apply-control"},
		{"closure wording with apply", page(func(p *PageRead) { p.Text = longText("This job is no longer available.") }), exp, LiveUncertain, "closure-with-apply"},
		{"wrong posting", page(func(p *PageRead) {
			p.Title = "Chef de Partie - Other Restaurant"
			p.Heading = "Chef de Partie"
			p.Text = longText("Other Restaurant")
		}), exp, LiveMismatch, "mismatch"},
		{"active", page(nil), exp, LiveActive, "apply-control"},
		{"long page without apply or wording", page(func(p *PageRead) { p.HasApply = false }), exp, LiveUncertain, "no-apply-control"},
	}
	for _, c := range cases {
		got := ClassifyLiveness(req, c.pg, c.exp)
		if got.Verdict != c.verdict || got.Reason != c.why {
			t.Errorf("%s: %+v, want %s/%s", c.name, got, c.verdict, c.why)
		}
	}
}

// A Workday posting that now redirects to the company's own careers page is
// gone, and a Workday shell that never rendered its posting says nothing; the
// careers tagline is not a posting title.
func TestLiveness_WorkdayRedirectAndUnrenderedShell(t *testing.T) {
	const req = "https://example.wd5.myworkdayjobs.com/en-US/ExampleSite/job/US-CA-Santa-Clara/Senior-Platform-Engineer--Enterprise-Products_JR2026916"
	cases := []struct {
		name         string
		pg           PageRead
		verdict, why string
	}{
		{"redirect to the company careers page", PageRead{Status: 200, FinalURL: "https://www.example.com/en-us/about/careers/", Title: "Jobs at Example | Example Careers",
			Heading: "Like No Place You've Ever Worked", Text: longText("Find your next job.")}, LiveExpired, "redirect-lost-job"},
		{"redirect to a page that keeps the job id", PageRead{Status: 200, FinalURL: "https://jobs.example.com/careers/JR2026916", Title: "Senior Platform Engineer",
			Text: longText("Example is hiring."), HasApply: true}, LiveActive, "apply-control"},
		{"shell with no posting data", PageRead{Status: 200, FinalURL: req, Title: "Example - Like No Place You've Ever Worked", Text: longText("Search for jobs."), NoPosting: true}, LiveUncertain, "posting-not-rendered"},
		{"expired posting wording wins over the shell", PageRead{Status: 200, FinalURL: req, Text: longText("The page you are looking for no longer exists. This job is no longer available."), NoPosting: true}, LiveExpired, "closure-wording"},
		{"rendered posting", PageRead{Status: 200, FinalURL: req, Title: "Example Careers", Heading: "Senior Platform Engineer", PostingTitle: "Senior Platform Engineer", Text: longText("Example is hiring."), HasApply: true}, LiveActive, "apply-control"},
	}
	for _, c := range cases {
		got := ClassifyLiveness(req, c.pg, Expect{})
		if got.Verdict != c.verdict || got.Reason != c.why {
			t.Errorf("%s: %+v, want %s/%s", c.name, got, c.verdict, c.why)
		}
	}
}

// A career site a tracking link lands on names its posting by its own
// canonical path, without the parameters the link added; a canonical link
// that leaves the site or drops the posting's id names something else.
func TestPostingAddress_ACanonicalLinkThatKeepsThePostingsID(t *testing.T) {
	const final = "https://careers.example.test/job/-/-/27600/98734009616?p_sid=aWt&p_uid=9f2"
	const canon = "https://careers.example.test/job/united-states/software-engineer/27600/98734009616"
	for link, want := range map[string]string{
		canon: canon,
		"https://other.example.test/job/united-states/software-engineer/27600/98734009616": final,
		"https://careers.example.test/jobs":                                                final,
		"":                                                                                 final,
	} {
		if got := PostingAddress(PageRead{FinalURL: final, Canonical: link}); got != want {
			t.Errorf("canonical %q: %q, want %q", link, got, want)
		}
	}
}

// An employer page that embeds Greenhouse loads the form from gh_jid, which
// its canonical link leaves out.
func TestPostingAddress_ACanonicalLinkThatDropsThePostingIDParameter(t *testing.T) {
	const final = "https://careers.example.test/detail/8052118/?gh_jid=8052118"
	if got := PostingAddress(PageRead{FinalURL: final, Canonical: "https://careers.example.test/detail/8052118/"}); got != final {
		t.Errorf("got %q, want %q", got, final)
	}
}

func TestLiveness_MismatchNeedsLowTitleOverlapAndAbsentCompany(t *testing.T) {
	exp := Expect{Company: "Example Corp", Role: "Data Engineer"}
	other := func(p *PageRead) {
		p.Title = "Chef de Partie"
		p.Heading = "Chef de Partie"
		p.Text = longText("Warehouse work.")
	}
	if got := ClassifyLiveness("https://jobs.example.test/o/1", page(other), exp); got.Verdict != LiveMismatch {
		t.Errorf("different role and company: %+v", got)
	}
	// Another role but the company is named: not a mismatch.
	named := func(p *PageRead) { other(p); p.Text = longText("Example Corp warehouse work.") }
	if got := ClassifyLiveness("https://jobs.example.test/o/1", page(named), exp); got.Verdict == LiveMismatch {
		t.Errorf("company named: %+v", got)
	}
	// The role is named but not the company: not a mismatch.
	roleOnly := func(p *PageRead) {
		p.Title = "Data Engineer"
		p.Heading = "Data Engineer"
		p.Text = longText("Some other firm.")
	}
	if got := ClassifyLiveness("https://jobs.example.test/o/1", page(roleOnly), exp); got.Verdict != LiveActive {
		t.Errorf("role named only: %+v", got)
	}
	// A 404 is expired whatever the expectation.
	if got := ClassifyLiveness("https://jobs.example.test/o/1", page(func(p *PageRead) { other(p); p.Status = 404 }), exp); got.Verdict != LiveExpired {
		t.Errorf("404 with a mismatch: %+v", got)
	}
}

func TestLiveness_UnreadableOrTimedOutIsUncertain(t *testing.T) {
	const req = "https://jobs.example.test/o/4001"
	for name, pg := range map[string]PageRead{
		"too little text": page(func(p *PageRead) { p.Text = "Loading" }),
		"timeout":         page(func(p *PageRead) { p.TimedOut = true }),
		"load failure":    page(func(p *PageRead) { p.LoadFailed = true }),
		"server error":    page(func(p *PageRead) { p.Status = 503; p.Text = longText("") }),
	} {
		if got := ClassifyLiveness(req, pg, Expect{}); got.Verdict != LiveUncertain {
			t.Errorf("%s: %+v", name, got)
		}
	}
}

// fakeReader serves pages by URL and counts what it was asked.
type fakeReader struct {
	mu       sync.Mutex
	pages    map[string]PageRead
	asked    []string
	cur, max int
	hold     chan struct{}
	entered  chan struct{} // when set, receives one value per Read as it starts
	fail     bool
}

func (f *fakeReader) Read(ctx context.Context, u string) (PageRead, error) {
	f.mu.Lock()
	f.asked = append(f.asked, u)
	f.cur++
	f.max = max(f.max, f.cur)
	fail := f.fail
	pg, ok := f.pages[u]
	f.mu.Unlock()
	if f.entered != nil {
		f.entered <- struct{}{}
	}
	if f.hold != nil {
		select {
		case <-f.hold:
		case <-ctx.Done():
		}
	}
	f.mu.Lock()
	f.cur--
	f.mu.Unlock()
	if fail {
		return PageRead{}, errors.New("the browser is not available")
	}
	if !ok {
		pg = page(func(p *PageRead) { p.FinalURL = u })
	}
	return pg, nil
}

func (e *env) checker(r PageReader) *Checker {
	return &Checker{Repo: e.rp, Reader: r, Tracker: e.tracker(), Sleep: func(context.Context, time.Duration) error { return nil }}
}

func (e *env) addInbox(url string, ageDays int, status string) {
	e.t.Helper()
	at := e.clk.now().Add(-time.Duration(ageDays) * day).UTC().Format(timeLayout)
	key := identity.Posting{URL: url}.IdentityKey()
	e.exec(`INSERT INTO inbox (url, original_url, identity_key, company, role, source, status, first_seen_at, updated_at) VALUES (?, ?, ?, 'Example Corp', 'Data Engineer', 'test', ?, ?, ?)`,
		url, url, key, status, at, at)
}

func (e *env) inboxByURL(url string) InboxRow {
	e.t.Helper()
	for _, r := range e.inbox(InboxQuery{Limit: 1000}) {
		if r.URL == url {
			return r
		}
	}
	e.t.Fatalf("no inbox row for %s", url)
	return InboxRow{}
}

func TestLiveness_ReadsRenderedPageNotStaticHTML(t *testing.T) {
	// The page is an empty shell until script runs; only the browser's read
	// shows the apply control. The check makes no request of its own.
	e, fs, _ := newFeedEnv(t)
	fs.handle = func(w http.ResponseWriter, r *http.Request, _ string) {
		writeHTML(w, `<html><body><div id="root"></div></body></html>`)
	}
	reader := &fakeReader{pages: map[string]PageRead{}}
	c := e.checker(reader)
	l, err := c.Check(e.ctx, "https://jobs.example.test/o/4001", Expect{Company: "Example Corp", Role: "Data Engineer"})
	if err != nil || l.Verdict != LiveActive {
		t.Fatalf("verdict %+v, %v", l, err)
	}
	if len(fs.requests()) != 0 || len(reader.asked) != 1 {
		t.Fatalf("static requests %d, browser reads %d", len(fs.requests()), len(reader.asked))
	}
	rec, ok, _ := e.rp.LivenessGet(e.ctx, identity.Posting{URL: "https://jobs.example.test/o/4001"}.IdentityKey())
	if !ok || rec.Method != LivenessMethod || rec.Verdict != LiveActive || rec.CheckedAt.IsZero() {
		t.Fatalf("stored = %+v", rec)
	}
}

// PostHog (live): Ashby's posting page answers 404 for an employer whose own
// careers page embeds the form, and the form is still served; only a posting
// whose form answers 404 too is expired.
func TestLiveness_AnAshbyPostingWithItsHostedPageOffIsReadAtItsForm(t *testing.T) {
	e := newEnv(t)
	const listed, closed = "https://jobs.ashbyhq.com/posthog/20ab9628", "https://jobs.ashbyhq.com/rebar/ead8df0c"
	gone := page(func(p *PageRead) { p.Status = 404 })
	reader := &fakeReader{pages: map[string]PageRead{listed: gone, closed: gone, closed + "/application?embed=js": gone}}
	c := e.checker(reader)
	if l, err := c.Check(e.ctx, listed, Expect{}); err != nil || l.Verdict == LiveExpired {
		t.Fatalf("listed: %+v %v", l, err)
	}
	if l, err := c.Check(e.ctx, closed, Expect{}); err != nil || l.Verdict != LiveExpired {
		t.Fatalf("closed: %+v %v", l, err)
	}
	if AshbyEmbedURL("https://jobs.ashbyhq.com/posthog/20ab9628/application") != listed+"/application?embed=js" || AshbyEmbedURL("https://jobs.lever.co/a/b") != "" {
		t.Fatal("embed address")
	}
}

func TestLivenessAudit_SkipsRowsCheckedWithinWindow_RowLimitBounds(t *testing.T) {
	e := newEnv(t)
	reader := &fakeReader{}
	c := e.checker(reader)
	for i := 0; i < 300; i++ {
		u := fmt.Sprintf("https://jobs.example.test/o/%d", 1000+i)
		e.addInbox(u, 9, InboxPending)
		if i < 100 {
			key := identity.Posting{URL: u}.IdentityKey()
			e.exec(`INSERT INTO liveness_checks (identity_key, url, verdict, reason, method, checked_at) VALUES (?, ?, 'active', 'apply-control', 'browser', ?)`,
				key, u, e.clk.now().Add(-10*day).UTC().Format(timeLayout))
		}
	}
	res, err := c.Audit(e.ctx)
	if err != nil {
		t.Fatal(err)
	}
	if res.Skipped != 100 || res.Checked != 200 || len(reader.asked) != 200 {
		t.Fatalf("result = %+v, %d reads", res, len(reader.asked))
	}
	for _, u := range reader.asked {
		var n int
		fmt.Sscanf(u, "https://jobs.example.test/o/%d", &n)
		if n < 1100 {
			t.Fatalf("a row checked 10 days ago was requested: %s", u)
		}
	}
	// A re-run costs only the unchecked rows: nothing is left.
	reader.asked = nil
	res, _ = c.Audit(e.ctx)
	if res.Checked != 0 || len(reader.asked) != 0 {
		// Rows 1100..1299 were just checked; the other 100 are inside the window.
		t.Fatalf("re-run = %+v, %d reads", res, len(reader.asked))
	}
}

// A row keyed by its board's own job id has a key its address does not give;
// its check is stored under the address's key and still counts for the row.
func TestLivenessAudit_ARowKeyedApartFromItsAddressIsNotReadAgain(t *testing.T) {
	e := newEnv(t)
	reader := &fakeReader{}
	c := e.checker(reader)
	const u = "https://jobs.example.test/o/7001"
	e.addInbox(u, 9, InboxPending)
	e.exec(`UPDATE inbox SET identity_key = 'test:7001' WHERE url = ?`, u)
	if res, err := c.Audit(e.ctx); err != nil || res.Checked != 1 {
		t.Fatalf("first audit = %+v, %v", res, err)
	}
	reader.asked = nil
	res, err := c.Audit(e.ctx)
	if err != nil || res.Checked != 0 || res.Skipped != 1 || len(reader.asked) != 0 {
		t.Fatalf("second audit = %+v, %v, %d reads", res, err, len(reader.asked))
	}
}

func TestLivenessAudit_ScopeRows(t *testing.T) {
	e := newEnv(t)
	reader := &fakeReader{}
	c := e.checker(reader)
	e.addInbox("https://jobs.example.test/o/1", 8, InboxPending)
	e.addInbox("https://jobs.example.test/o/2", 3, InboxPending)
	e.addInbox("https://jobs.example.test/o/3", 9, InboxDismissed)
	e.addInbox("https://jobs.example.test/o/4", 9, InboxQueued)
	e.trackedRow("Example Corp", "Evaluated Role", "https://jobs.example.test/o/5")
	applied := e.trackedRow("Example Corp", "Applied Role", "https://jobs.example.test/o/6")
	e.exec(`UPDATE tracker_entries SET status = 'applied' WHERE id = ?`, applied.ID)
	if _, err := c.Audit(e.ctx); err != nil {
		t.Fatal(err)
	}
	got := strings.Join(reader.asked, " ")
	for _, want := range []string{"/o/1", "/o/4", "/o/5"} {
		if !strings.Contains(got, want) {
			t.Errorf("%s was not checked: %s", want, got)
		}
	}
	for _, not := range []string{"/o/2", "/o/3", "/o/6"} {
		if strings.Contains(got, not) {
			t.Errorf("%s was checked: %s", not, got)
		}
	}
}

func TestLiveness_ExpiredSetsInboxRowNotTrackerStatus(t *testing.T) {
	e := newEnv(t)
	gone, wrong := "https://jobs.example.test/o/10", "https://jobs.example.test/o/11"
	e.addInbox(gone, 9, InboxPending)
	e.addInbox(wrong, 9, InboxPending)
	en := e.trackedRow("Example Corp", "Data Engineer", gone)
	reader := &fakeReader{pages: map[string]PageRead{
		gone: page(func(p *PageRead) { p.Status = 404; p.FinalURL = gone }),
		wrong: page(func(p *PageRead) {
			p.FinalURL = wrong
			p.Title = "Chef"
			p.Heading = "Chef"
			p.Text = longText("A warehouse.")
		}),
	}}
	res, err := e.checker(reader).Audit(e.ctx)
	if err != nil || res.Expired != 1 || res.Mismatch != 1 {
		t.Fatalf("result = %+v, %v", res, err)
	}
	if got := e.inboxByURL(gone); got.Status != InboxExpired {
		t.Fatalf("expired row status = %s", got.Status)
	}
	if got := e.inboxByURL(wrong); got.Status != InboxPending || len(got.Flags) != 1 || got.Flags[0] != FlagNeedsReview {
		t.Fatalf("mismatch row = %+v", got)
	}
	d, err := e.tracker().Get(e.ctx, en.ID)
	if err != nil {
		t.Fatal(err)
	}
	if d.Entry.Status != tracker.Evaluated || !strings.Contains(d.Entry.Notes, "liveness: expired (http-404)") {
		t.Fatalf("tracker row = %+v", d.Entry)
	}
	noted := false
	for _, ev := range d.Events {
		if ev.Kind == tracker.EventNoteAdded {
			noted = true
		}
	}
	if !noted {
		t.Fatal("no event was recorded")
	}
}

func TestLiveness_PreEvaluationProbeBestEffort(t *testing.T) {
	e := newEnv(t)
	reader := &fakeReader{fail: true}
	c := e.checker(reader)
	const u = "https://jobs.example.test/o/20"
	if l := c.BeforeEvaluation(e.ctx, u, Expect{Company: "Example Corp", Role: "Data Engineer"}); l.Verdict != LiveUncertain || l.Reason != "unknown" {
		t.Fatalf("a failed probe = %+v", l)
	}
	if l := c.BeforeEvaluation(e.ctx, "javascript:alert(1)", Expect{}); l.Reason != "unknown" {
		t.Fatalf("unusable URL = %+v", l)
	}
	reader.fail = false
	if l := c.BeforeEvaluation(e.ctx, u, Expect{Company: "Example Corp", Role: "Data Engineer"}); l.Verdict != LiveActive {
		t.Fatalf("probe = %+v", l)
	}
	// A stored result answers without another read.
	n := len(reader.asked)
	c.BeforeEvaluation(e.ctx, u, Expect{})
	if len(reader.asked) != n {
		t.Fatal("a URL with a stored result was read again")
	}
}

func TestLiveness_ConcurrencyTwoAndPerHostSpacing(t *testing.T) {
	e := newEnv(t)
	release := make(chan struct{})
	reader := &fakeReader{hold: release, entered: make(chan struct{}, 6)}
	var sleeps []time.Duration
	var mu sync.Mutex
	c := e.checker(reader)
	c.Sleep = func(_ context.Context, d time.Duration) error {
		mu.Lock()
		sleeps = append(sleeps, d)
		mu.Unlock()
		return nil
	}
	for i := 0; i < 6; i++ {
		e.addInbox(fmt.Sprintf("https://jobs.example.test/o/%d", 30+i), 9, InboxPending)
	}
	done := make(chan error, 1)
	go func() {
		_, err := c.Audit(e.ctx)
		done <- err
	}()
	// Two reads start and hold; with the cap at two, the rest wait for them.
	for i := 0; i < 2; i++ {
		select {
		case <-reader.entered:
		case <-time.After(30 * time.Second):
			t.Fatalf("only %d reads started", i)
		}
	}
	// A third read would start without the release. The wait only bounds how
	// long the test looks; it cannot fail a correct cap.
	select {
	case <-reader.entered:
		t.Fatal("a third read started while two were held")
	case <-time.After(100 * time.Millisecond):
	}
	close(release)
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("the audit did not finish")
	}
	reader.mu.Lock()
	peak, asked := reader.max, len(reader.asked)
	reader.mu.Unlock()
	if peak != 2 || asked != 6 {
		t.Fatalf("peak %d reads in flight over %d reads, want 2 over 6", peak, asked)
	}
	// Six pages on one host: after the first, each waits two more seconds.
	mu.Lock()
	defer mu.Unlock()
	var longest time.Duration
	for _, d := range sleeps {
		longest = max(longest, d)
	}
	if longest != 10*time.Second {
		t.Fatalf("sleeps = %v, want the last wait to be 10s", sleeps)
	}
}

// An evaluation's own page read that found the posting closed is kept like an
// audit result: it is stored under the URL, and the row the job was for
// expires even when the scan stored it under another identity key.
func TestLiveness_RecordAppliesAReadOutsideTheAudit(t *testing.T) {
	e := newEnv(t)
	const u = "https://careers.example.test/r/7001"
	e.exec(`INSERT INTO inbox (url, original_url, identity_key, company, role, source, status, first_seen_at, updated_at)
		VALUES (?, ?, 'greenhouse:example/7001', 'Example Corp', 'Data Engineer', 'registry', 'queued', ?, ?)`, u, u, e.rp.stamp(), e.rp.stamp())
	own := e.inboxByURL(u)
	if err := e.checker(nil).Record(e.ctx, u, Liveness{Verdict: LiveExpired, Reason: "closure-wording"}, own.ID); err != nil {
		t.Fatal(err)
	}
	if row, _ := e.rp.InboxGet(e.ctx, own.ID); row.Status != InboxExpired {
		t.Errorf("the job's row = %s", row.Status)
	}
	if rec, ok, err := e.rp.LivenessGet(e.ctx, identity.Posting{URL: u}.IdentityKey()); err != nil || !ok || rec.Verdict != LiveExpired {
		t.Fatalf("stored = %+v ok %v err %v", rec, ok, err)
	}
}
