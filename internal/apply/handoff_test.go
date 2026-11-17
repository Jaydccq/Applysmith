package apply

import (
	"context"
	"net/url"
	"slices"
	"strings"
	"testing"
	"time"

	"applysmith/internal/browser"
	"applysmith/internal/discovery"
	"applysmith/internal/engine"
	"applysmith/internal/gates"
)

const (
	ghEmbed    = "https://job-boards.greenhouse.io/embed/job_app?for=example-corp&token=1234567"
	listingURL = "https://careers.example.test/jobs"
)

func shell(url string, frames ...FrameInfo) *PageState {
	return &PageState{URL: url, Title: "Careers at Example Corp", Text: "Careers", Lines: []string{"Careers"}, Headings: []string{"Careers at Example Corp"}, Frames: frames}
}

func visibleFrame(src string) FrameInfo { return FrameInfo{Src: src, Visible: true, W: 600, H: 500} }

func resolveLines(run *Run) []string {
	var out []string
	for _, l := range run.Stages {
		if l.Stage == StageResolve {
			out = append(out, l.Detail)
		}
	}
	return out
}

// handoffWalk walks a page with no form whose destination page is dest.
func handoffWalk(t *testing.T, in Input, first, dest *PageState) (*wenv, *walkResult, *stop) {
	t.Helper()
	w := newWalk(t, in, first)
	w.tab.onNav = func(tab *scriptTab, url string) { tab.set(dest) }
	res, st := w.walk()
	return w, res, st
}

func TestApply_Handoff_EmbeddedFrameWalkedAsTopLevelPage(t *testing.T) {
	dest := appPage()
	dest.URL = ghEmbed
	w, res, st := handoffWalk(t, Input{URL: listingURL, Caller: gates.CallerCLI}, shell(listingURL, visibleFrame(ghEmbed)), dest)
	if st != nil || res.End != EndSubmit {
		t.Fatalf("end %+v stop %+v", res, st)
	}
	if len(w.tab.navs) != 1 || w.tab.navs[0] != ghEmbed {
		t.Fatalf("navigations %v", w.tab.navs)
	}
	if lines := resolveLines(w.e.Run); len(lines) != 1 || !strings.Contains(lines[0], "embedded greenhouse application frame") {
		t.Fatalf("resolve lines %q", lines)
	}
	if w.e.Run.ATS != ATSGreenhouse || w.e.Run.Tenant != "example-corp" || w.page.nFills != 1 {
		t.Fatalf("ats %s tenant %s fills %d: the page is walked once, from the frame's address", w.e.Run.ATS, w.e.Run.Tenant, w.page.nFills)
	}

	t.Run("a frame of no named system is left alone", func(t *testing.T) {
		for _, src := range []string{"https://player.example.test/embed/1", "https://www.google.com/recaptcha/api2/anchor?k=x", "https://job-boards.greenhouse.io/embed/job_board?for=example-corp"} {
			w, res, st := handoffWalk(t, Input{URL: listingURL, Caller: gates.CallerCLI}, shell(listingURL, visibleFrame(src)), appPage())
			if st != nil || res.End != EndNoForm || len(w.tab.navs) != 0 {
				t.Errorf("%s: end %+v stop %+v navs %v", src, res, st, w.tab.navs)
			}
		}
	})
	t.Run("a frame that is not shown is left alone", func(t *testing.T) {
		hidden := visibleFrame(ghEmbed)
		hidden.Visible, hidden.W, hidden.H = false, 0, 0
		w, _, _ := handoffWalk(t, Input{URL: listingURL, Caller: gates.CallerCLI}, shell(listingURL, hidden), appPage())
		if len(w.tab.navs) != 0 {
			t.Fatalf("navigations %v", w.tab.navs)
		}
	})
	t.Run("a hand-off that comes back is not repeated", func(t *testing.T) {
		loop := shell(ghEmbed, visibleFrame(ghEmbed))
		w, res, st := handoffWalk(t, Input{URL: listingURL, Caller: gates.CallerCLI}, shell(listingURL, visibleFrame(ghEmbed)), loop)
		if len(w.tab.navs) != 1 {
			t.Fatalf("navigations %v, end %+v stop %+v", w.tab.navs, res, st)
		}
		if st == nil || st.class != ClassDefect {
			t.Fatalf("stop %+v", st)
		}
	})
}

func srcdocFrame(script string) FrameInfo {
	return FrameInfo{Srcdoc: true, Visible: true, W: 600, H: 500, SrcdocText: `<html><body><div id="grnhse_app"></div>` + script + `</body></html>`}
}

func boardScript(slug, tail string) string {
	return `<script src="https://boards.greenhouse.io/embed/job_board/js?for=` + slug + tail + `></script>`
}

func TestApply_Handoff_SrcdocGreenhouseEmbedNeedsSameJobId(t *testing.T) {
	page := "https://careers.example.test/jobs?gh_jid=1234567"
	nav := func(runURL string, f FrameInfo, pageURL string) []string {
		dest := appPage()
		dest.URL = ghEmbed
		w, _, _ := handoffWalk(t, Input{URL: runURL, Caller: gates.CallerCLI}, shell(pageURL, f), dest)
		return w.tab.navs
	}
	good := srcdocFrame(boardScript("example-corp", `"`))
	if got := nav(page, good, page); len(got) != 1 || got[0] != ghEmbed {
		t.Fatalf("the embed of the run's own job: %v", got)
	}
	for name, c := range map[string]struct {
		runURL, pageURL string
		f               FrameInfo
	}{
		"another job's embed":       {"https://careers.example.test/jobs?gh_jid=7654321", page, good},
		"the run's URL names no id": {"https://careers.example.test/jobs", page, good},
		"the page names no id":      {page, "https://careers.example.test/jobs", good},
		"the slug runs on":          {page, page, srcdocFrame(boardScript("example-corp/../x", `"`))},
		"no board script":           {page, page, srcdocFrame(`<script>var for="example-corp";</script>`)},
		"not a script source":       {page, page, srcdocFrame(`<p>https://boards.greenhouse.io/embed/job_board/js?for=example-corp</p>`)},
	} {
		if got := nav(c.runURL, c.f, c.pageURL); len(got) != 0 {
			t.Errorf("%s: followed %v", name, got)
		}
	}
	// A slug that ends where the attribute does, and one that ends at a query
	// separator, are both closed.
	for _, tail := range []string{"", `&x=1"`, ` async`} {
		if got := nav(page, srcdocFrame(boardScript("example-corp", tail)), page); len(got) != 1 {
			t.Errorf("tail %q: %v", tail, got)
		}
	}
}

// Datadog's careers page loads its Greenhouse board script in the document,
// and the form frame the script draws never appeared.
func TestApply_Handoff_PageGreenhouseBoardScriptNeedsSameJobId(t *testing.T) {
	page := "https://careers.example.test/detail/1234567/?gh_jid=1234567"
	nav := func(runURL, board string) []string {
		st := shell(page)
		st.GreenhouseBoard = board
		dest := appPage()
		dest.URL = ghEmbed
		w, _, _ := handoffWalk(t, Input{URL: runURL, Caller: gates.CallerCLI}, st, dest)
		return w.tab.navs
	}
	if got := nav(page, "example-corp"); len(got) != 1 || got[0] != ghEmbed {
		t.Fatalf("the board of the run's own job: %v", got)
	}
	for name, c := range map[string]struct{ runURL, board string }{
		"another job":               {"https://careers.example.test/detail/7654321/?gh_jid=7654321", "example-corp"},
		"the run's URL names no id": {"https://careers.example.test/detail/1234567/", "example-corp"},
		"no board script":           {page, ""},
		"the board runs on":         {page, "example-corp/../x"},
	} {
		if got := nav(c.runURL, c.board); len(got) != 0 {
			t.Errorf("%s: followed %v", name, got)
		}
	}
}

func listing(links ...LinkInfo) *PageState {
	st := shell(listingURL)
	st.Title, st.Headings = "Careers", []string{"Open roles"}
	st.Links = links
	return st
}

func TestApply_Handoff_LinkedInDestinationAndExactTitleLink(t *testing.T) {
	posting := "https://careers.example.test/jobs/platform-engineer-42"
	dest := wizardStep(1, 1)
	dest.URL = posting
	dest.Title, dest.Headings = "Platform Engineer", []string{"Platform Engineer"}
	in := Input{URL: listingURL, Role: "Platform Engineer", Company: "Example Corp", Caller: gates.CallerCLI}

	t.Run("the listing links the evaluated role by its exact title", func(t *testing.T) {
		first := listing(LinkInfo{"Principal Platform Engineer", "https://careers.example.test/jobs/principal-1"}, LinkInfo{" platform  engineer ", posting}, LinkInfo{"Platform Engineer", posting})
		w, res, st := handoffWalk(t, in, first, dest)
		if st != nil || res.End != EndSubmit || len(w.tab.navs) != 1 || w.tab.navs[0] != posting {
			t.Fatalf("end %+v stop %+v navs %v", res, st, w.tab.navs)
		}
		if lines := resolveLines(w.e.Run); len(lines) != 1 || !strings.Contains(lines[0], "the evaluated role is linked from it") {
			t.Fatalf("resolve lines %q", lines)
		}
	})
	t.Run("the role guard reads the page the link led to", func(t *testing.T) {
		wrong := wizardStep(1, 1)
		wrong.Title, wrong.Headings = "Marketing Manager", []string{"Marketing Manager"}
		_, _, st := handoffWalk(t, in, listing(LinkInfo{"Platform Engineer", posting}), wrong)
		if st == nil || st.class != ClassPosting || !strings.Contains(st.reason, "stale link") {
			t.Fatalf("stop %+v", st)
		}
	})
	t.Run("two destinations, a neighbour, or a page that names a role", func(t *testing.T) {
		for name, first := range map[string]*PageState{
			"two destinations": listing(LinkInfo{"Platform Engineer", posting}, LinkInfo{"Platform Engineer", posting + "-b"}),
			"only a neighbour": listing(LinkInfo{"Principal Platform Engineer", posting}),
			"the page itself":  listing(LinkInfo{"Platform Engineer", listingURL}),
		} {
			w, _, _ := handoffWalk(t, in, first, dest)
			if len(w.tab.navs) != 0 {
				t.Errorf("%s: followed %v", name, w.tab.navs)
			}
		}
		named := listing(LinkInfo{"Platform Engineer", posting})
		named.Headings = []string{"Staff Engineer"}
		if w, _, _ := handoffWalk(t, in, named, dest); len(w.tab.navs) != 0 {
			t.Errorf("a page that names a role was left: %v", w.tab.navs)
		}
	})
	t.Run("employerUrl on a job URL that is not an aggregator is ignored", func(t *testing.T) {
		e := &Exec{Run: &Run{Input: Input{URL: "https://jobs.lever.co/example-corp/0a1b2c3d", EmployerURL: "https://jobs.ashbyhq.com/other/1"}}}
		if got := e.target(); got != "https://jobs.lever.co/example-corp/0a1b2c3d" {
			t.Fatalf("target %s", got)
		}
		e.Run.Input.URL = "https://www.ziprecruiter.com/jobs/abc"
		if got := e.target(); got != "https://jobs.ashbyhq.com/other/1" {
			t.Fatalf("target %s", got)
		}
	})
	t.Run("a LinkedIn posting that applies on the employer's site", func(t *testing.T) {
		ashby := "https://jobs.ashbyhq.com/example-corp/0a1b2c3d-1111-2222-3333-444455556666"
		for name, c := range map[string]struct {
			href    string
			ats     string
			reason  string
			handoff bool
		}{
			"followed": {ashby, ATSAshby, "", true},
			// Capital One (live): the Apply link is LinkedIn's own outbound redirect.
			"through LinkedIn's safety redirect": {"https://www.linkedin.com/safety/go/?url=" + url.QueryEscape(ashby) + "&urlhash=6YHX&mt=x", ATSAshby, "", true},
			"back to LinkedIn":                   {"https://www.linkedin.com/jobs/view/4000000002", ATSLinkedIn, "does not offer Easy Apply", false},
			"a mirror":                           {"https://www.ziprecruiter.com/jobs/abc", ATSLinkedIn, "does not offer Easy Apply", false},
			"no destination in payload":          {"", ATSLinkedIn, "does not offer Easy Apply; it applies through \"Apply\"", false},
		} {
			run, tab, g := linkedInRun(t)
			tab.lk["affordance"] = map[string]any{"label": "Apply", "href": c.href, "offsite": c.href != "" && !strings.Contains(c.href, "linkedin.com")}
			e := &Exec{r: g.r, Run: run, Ctx: context.Background(), Cls: mustClassify(t, run.Input.URL)}
			s := &session{r: g.r, e: e, tab: tab, page: tab.page}
			stp := g.r.openLinkedIn(context.Background(), e, s)
			switch {
			case c.handoff && (stp != nil || run.ATS != c.ats || len(resolveLines(run)) != 1 || tab.navs[len(tab.navs)-1] != ashby+"/application"):
				t.Errorf("%s: stop %+v ats %s lines %q navs %v", name, stp, run.ATS, resolveLines(run), tab.navs)
			case !c.handoff && (stp == nil || stp.class != ClassPosting || !strings.Contains(stp.reason, c.reason) || run.ATS != ATSLinkedIn):
				t.Errorf("%s: stop %+v ats %s", name, stp, run.ATS)
			}
		}
	})
}

func mustClassify(t *testing.T, raw string) Classification {
	t.Helper()
	c, s := Classify(raw)
	if s != nil {
		t.Fatalf("%s: %+v", raw, s)
	}
	return c
}

// linkedInRun is a LinkedIn run on a scripted tab whose page offers no Easy
// Apply button and no modal.
func linkedInRun(t *testing.T) (*Run, *scriptTab, *rig) {
	t.Helper()
	g := newRig(t, func(d *Deps) {
		d.Page.Timing = WalkTiming{First: 20 * time.Millisecond, Poll: 5 * time.Millisecond, PressWait: 20 * time.Millisecond, OpenWait: 40 * time.Millisecond, TerminalWait: 40 * time.Millisecond, ConfirmWait: 40 * time.Millisecond}
	})
	post := &PageState{URL: "https://www.linkedin.com/jobs/view/4000000001/", Title: "Platform Engineer | Example Corp | LinkedIn", Text: "Platform Engineer", Lines: []string{"Platform Engineer"}}
	tab := newScriptTab(post)
	tab.page = &fakePage{}
	tab.lk = map[string]any{"modal": false, "notFound": false, "mainText": "Platform Engineer"}
	run := &Run{ID: "run_lk", StartedAt: t0, Input: Input{URL: "https://www.linkedin.com/jobs/view/4000000001", Caller: gates.CallerCLI}, ATS: ATSLinkedIn, Tenant: "linkedin"}
	return run, tab, g
}

func TestApply_Walk_BuiltURL404AndLinkedInDomContentLoaded(t *testing.T) {
	posting := "https://example.wd5.myworkdayjobs.com/en-US/Careers/job/Durham-NC/Engineer_R100"
	built := posting + "/apply/applyManually"
	gone := &PageState{URL: built, Title: "Workday", Text: "The page you are looking for doesn't exist.", Lines: []string{"The page you are looking for doesn't exist."}}
	live := &PageState{URL: posting, Title: "Engineer", Text: "Engineer Apply", Lines: []string{"Engineer"}}
	open := func(t *testing.T, mod func(*scriptTab)) (*scriptTab, *stop, *Run) {
		run, tab, g := linkedInRun(t)
		run.Input.URL, run.ATS = posting, ATSWorkday
		tab.set(live)
		tab.onNav = func(tab *scriptTab, url string) {
			if url == built {
				tab.set(gone)
			} else {
				tab.set(live)
			}
		}
		mod(tab)
		e := &Exec{r: g.r, Run: run, Ctx: context.Background(), Cls: mustClassify(t, posting)}
		s := &session{r: g.r, e: e, tab: tab, page: tab.page}
		return tab, g.r.openApplication(context.Background(), e, s), run
	}
	t.Run("a built address the site does not serve sends the run back to the posting", func(t *testing.T) {
		tab, stp, run := open(t, func(*scriptTab) {})
		if stp != nil || len(tab.navs) != 2 || tab.navs[0] != built || tab.navs[1] != posting || len(run.PostingFacts) != 0 {
			t.Fatalf("stop %+v navs %v facts %v: the posting is not closed because our address was wrong", stp, tab.navs, run.PostingFacts)
		}
	})
	t.Run("a page the walk cannot read is not a 404", func(t *testing.T) {
		tab, stp, _ := open(t, func(tab *scriptTab) { tab.evalErr = context.DeadlineExceeded })
		if stp != nil || len(tab.navs) != 1 {
			t.Fatalf("stop %+v navs %v", stp, tab.navs)
		}
	})
	// The closure is the posting's liveness result too (Clera, Oklo: closed
	// postings stayed in the inbox and were offered again).
	t.Run("the posting itself answering 404 is a posting fact", func(t *testing.T) {
		run, tab, g := linkedInRun(t)
		var closed []string
		g.r.d.PostingClosed = func(_ context.Context, url string, l discovery.Liveness) {
			closed = append(closed, url+" "+l.Verdict+" "+l.Reason)
		}
		tab.set(live)
		e := &Exec{r: g.r, Run: run, Ctx: context.Background(), Cls: Classification{ATS: ATSGeneric, ApplyURL: posting, Posting: posting}}
		s := &session{r: g.r, e: e, tab: &statusTab{scriptTab: tab, status: 404}, page: tab.page}
		stp := g.r.openApplication(context.Background(), e, s)
		if stp == nil || stp.class != ClassPosting || len(run.PostingFacts) != 1 {
			t.Fatalf("stop %+v facts %v", stp, run.PostingFacts)
		}
		if want := posting + " expired http-404"; len(closed) != 1 || closed[0] != want {
			t.Fatalf("liveness told %q, want %q", closed, want)
		}
	})
	// PostHog (live): its own careers page embeds the Ashby form, so Ashby's
	// posting and application pages answer 404 while the form itself, at
	// ?embed=js, is served; a closed posting's form answers 404 too.
	t.Run("an Ashby posting whose hosted page is off opens its embedded form", func(t *testing.T) {
		const ashby = "https://jobs.ashbyhq.com/posthog/20ab9628-20ff-4ae3-bd6a-46ae7e9dc6b8"
		for name, embed := range map[string]int{"listed": 200, "closed": 404} {
			run, tab, g := linkedInRun(t)
			run.Input.URL, run.ATS = ashby, ATSAshby
			tab.set(live)
			e := &Exec{r: g.r, Run: run, Ctx: context.Background(), Cls: mustClassify(t, ashby)}
			s := &session{r: g.r, e: e, tab: &urlStatusTab{scriptTab: tab, status: func(u string) int {
				if strings.HasSuffix(u, "/application?embed=js") {
					return embed
				}
				return 404
			}}, page: tab.page}
			stp := g.r.openApplication(context.Background(), e, s)
			last := tab.navs[len(tab.navs)-1]
			switch {
			case embed == 200 && (stp != nil || last != ashby+"/application?embed=js"):
				t.Errorf("%s: stop %+v navs %v", name, stp, tab.navs)
			case embed == 404 && (stp == nil || stp.class != ClassPosting):
				t.Errorf("%s: stop %+v navs %v", name, stp, tab.navs)
			}
		}
	})
	// Dropbox (live): its Greenhouse board address redirects to its own
	// careers site, whose apply page opens a ZIP-code step before any of the
	// application; Greenhouse's own form for the same job holds all of it, and
	// sends a job it no longer lists to its board with error=true.
	t.Run("a Greenhouse board address that leaves greenhouse.io opens Greenhouse's own form", func(t *testing.T) {
		const board = "https://job-boards.greenhouse.io/dropbox/jobs/7172701"
		const embed = "https://job-boards.greenhouse.io/embed/job_app?for=dropbox&token=7172701"
		const closed = "https://job-boards.greenhouse.io/embed/job_board?for=dropbox&error=true"
		for name, final := range map[string]string{"redirected": "https://dropbox.jobs/job/7172701", "closed": "https://dropbox.jobs/job/7172701",
			"kept": board, "kept with a query": board + "?gh_src=abc"} {
			run, tab, g := linkedInRun(t)
			run.Input.URL, run.ATS = board, ATSGreenhouse
			tab.set(live)
			tab.redirects = map[string]string{board: final}
			if name == "closed" {
				tab.redirects[embed] = closed
			}
			e := &Exec{r: g.r, Run: run, Ctx: context.Background(), Cls: mustClassify(t, board)}
			s := &session{r: g.r, e: e, tab: tab, page: tab.page}
			stp := g.r.openApplication(context.Background(), e, s)
			want, wantApply := []string{board}, board
			if name == "redirected" || name == "closed" {
				want, wantApply = []string{board, embed}, embed
			}
			if (stp != nil) != (name == "closed") || stp != nil && stp.class != ClassPosting || !slices.Equal(tab.navs, want) || e.Cls.ApplyURL != wantApply {
				t.Errorf("%s: stop %+v navs %v apply %s, want navs %v", name, stp, tab.navs, e.Cls.ApplyURL, want)
			}
		}
	})
	t.Run("the LinkedIn flow URL waits for the document and not the load event", func(t *testing.T) {
		run, tab, g := linkedInRun(t)
		tab.lk["modal"] = true
		e := &Exec{r: g.r, Run: run, Ctx: context.Background(), Cls: mustClassify(t, run.Input.URL)}
		s := &session{r: g.r, e: e, tab: tab, page: tab.page}
		if stp := g.r.openLinkedIn(context.Background(), e, s); stp != nil {
			t.Fatal(stp)
		}
		flow := "https://www.linkedin.com/jobs/view/4000000001/apply/?openSDUIApplyFlow=true"
		if len(tab.domNavs) != 1 || tab.domNavs[0] != flow || len(tab.loadNavs) != 0 {
			t.Fatalf("document-only navigations %v, load navigations %v", tab.domNavs, tab.loadNavs)
		}
	})
}

// statusTab answers every navigation with one HTTP status.
type statusTab struct {
	*scriptTab
	status int
}

func (s *statusTab) Navigate(ctx context.Context, url string) (r browser.NavResult, err error) {
	r, err = s.scriptTab.Navigate(ctx, url)
	r.Status = s.status
	return r, err
}

func (s *statusTab) NavigateDOM(ctx context.Context, url string) (r browser.NavResult, err error) {
	r, err = s.scriptTab.NavigateDOM(ctx, url)
	r.Status = s.status
	return r, err
}

// urlStatusTab answers each navigation with the HTTP status its address has.
type urlStatusTab struct {
	*scriptTab
	status func(url string) int
}

func (s *urlStatusTab) NavigateDOM(ctx context.Context, url string) (r browser.NavResult, err error) {
	r, err = s.scriptTab.NavigateDOM(ctx, url)
	r.Status = s.status(url)
	return r, err
}

// Node's open-application test (jobright's "Apply with Autofill"): an Apply
// control that opens its form in a new window is followed in the run's own
// tab, and the new window is not left to the walk.
func TestApply_Walk_ApplyThatOpensANewWindowIsFollowedInThisTab(t *testing.T) {
	const dest = "https://jobs.lever.co/example-corp/1234/apply"
	posting := &PageState{
		URL: listingURL, Title: "Platform Engineer", Text: "Platform Engineer at Example Corp", Lines: []string{"Platform Engineer"}, Headings: []string{"Platform Engineer"},
		Controls: []Control{{I: 1, Tag: "button", Labels: []string{"Apply with Autofill"}, Form: -1}},
	}
	form := appPage()
	form.URL = dest
	w := newWalk(t, Input{URL: listingURL, Caller: gates.CallerCLI}, posting)
	w.tab.popups = map[string]string{"Apply with Autofill": dest}
	w.tab.onNav = func(tab *scriptTab, url string) { tab.set(form) }
	res, st := w.walk()
	if st != nil || res.End != EndSubmit {
		t.Fatalf("end %+v stop %+v", res, st)
	}
	if len(w.tab.navs) != 1 || w.tab.navs[0] != dest || w.e.Run.ATS != ATSLever {
		t.Fatalf("navigations %v ats %s: the window's address is opened in the run's tab", w.tab.navs, w.e.Run.ATS)
	}
}

// RTX (Phenom): the Apply link opens Workday in a new window while the press
// also redraws the posting page (its chat panel answers), so the page reads as
// moved; the window is still the destination.
func TestApply_Walk_NewWindowLinkThatAlsoRedrawsThePageIsFollowed(t *testing.T) {
	const dest = "https://globalhr.wd5.myworkdayjobs.com/REC_RTX_Ext_Gateway/job/US-TX-REMOTE/Developer_01857907/apply"
	posting := &PageState{
		URL: listingURL, Title: "Platform Engineer", Text: "Platform Engineer at Example Corp", Lines: []string{"Platform Engineer"}, Headings: []string{"Platform Engineer"},
		Controls: []Control{{I: 1, Tag: "a", Labels: []string{"Apply"}, Form: -1, Href: dest, Target: "_blank"}},
	}
	form := appPage()
	form.URL = dest
	w := newWalk(t, Input{URL: listingURL, Caller: gates.CallerCLI}, posting)
	w.tab.popups = map[string]string{"Apply": dest}
	w.tab.popupLate = true
	w.tab.onClick = func(tab *scriptTab, c Control) {
		redrawn := *posting
		redrawn.Text += " Hi there! Are you interested in this job?"
		redrawn.Lines = append(append([]string(nil), posting.Lines...), "Hi there! Are you interested in this job?")
		redrawn.Controls = append(append([]Control(nil), posting.Controls...), Control{I: 2, Tag: "button", Labels: []string{"Similar Jobs"}, Form: -1})
		tab.set(&redrawn)
	}
	w.tab.onNav = func(tab *scriptTab, url string) { tab.set(form) }
	res, st := w.walk()
	if st != nil || res.End != EndSubmit {
		t.Fatalf("end %+v stop %+v", res, st)
	}
	if len(w.tab.navs) != 1 || !strings.HasPrefix(w.tab.navs[0], dest) || w.e.Run.ATS != ATSWorkday {
		t.Fatalf("navigations %v ats %s: the new window's address is opened in the run's tab", w.tab.navs, w.e.Run.ATS)
	}
}

// Node's arrivedFlow (careers.gevernova.com -> gevernova.wd5.myworkdayjobs.com):
// an Apply that lands on another named system's job page switches to that
// system, and Workday's application address skips the "Autofill with Resume /
// Apply Manually" chooser.
func TestApply_Walk_ApplyThatLandsOnWorkdayOpensItsApplicationAddress(t *testing.T) {
	const job = "https://gevernova.wd5.myworkdayjobs.com/Careers/job/Remote/Engineer_R123"
	posting := &PageState{
		URL: listingURL, Title: "Platform Engineer", Text: "Platform Engineer at Example Corp", Lines: []string{"Platform Engineer"}, Headings: []string{"Platform Engineer"},
		Controls: []Control{{I: 1, Tag: "a", Labels: []string{"Apply now"}, Form: -1}},
	}
	chooser := &PageState{URL: job, Title: "Engineer", Text: "Start Your Application", Lines: []string{"Start Your Application"},
		Controls: []Control{{I: 3, Tag: "a", Labels: []string{"Autofill with Resume"}, Form: -1}, {I: 4, Tag: "a", Labels: []string{"Apply Manually"}, Form: -1}}}
	form := appPage()
	form.URL = job + "/apply/applyManually"
	w := newWalk(t, Input{URL: listingURL, Caller: gates.CallerCLI}, posting)
	w.tab.onClick = func(tab *scriptTab, c Control) {
		if c.Labels[0] == "Apply now" {
			tab.set(chooser)
		}
	}
	w.tab.onNav = func(tab *scriptTab, url string) {
		if url == form.URL {
			tab.set(form)
		}
	}
	res, st := w.walk()
	if st != nil || res.End != EndSubmit {
		t.Fatalf("end %+v stop %+v", res, st)
	}
	if len(w.tab.navs) != 1 || w.tab.navs[0] != form.URL || w.e.Run.ATS != ATSWorkday || w.e.Run.Tenant != "gevernova" {
		t.Fatalf("navigations %v ats %s tenant %s", w.tab.navs, w.e.Run.ATS, w.e.Run.Tenant)
	}
	if w.tab.clicksOf("Autofill with Resume") != 0 {
		t.Fatalf("clicks %v: the chooser is skipped, never answered", w.tab.clicks)
	}
}

// A stop of the account stage reached through a hand-off ends the run at the
// account stage, where its stage line was recorded, not at fill.
func TestApply_Handoff_AnAccountStopEndsTheRunAtTheAccountStage(t *testing.T) {
	const job = "https://gevernova.wd5.myworkdayjobs.com/Careers/job/Remote/Engineer_R123"
	posting := &PageState{
		URL: listingURL, Title: "Platform Engineer", Text: "Platform Engineer at Example Corp", Lines: []string{"Platform Engineer"}, Headings: []string{"Platform Engineer"},
		Controls: []Control{{I: 1, Tag: "a", Labels: []string{"Apply now"}, Form: -1}},
	}
	chooser := &PageState{URL: job, Title: "Engineer", Text: "Start Your Application", Lines: []string{"Start Your Application"},
		Controls: []Control{{I: 4, Tag: "a", Labels: []string{"Apply Manually"}, Form: -1}}}
	w := newWalk(t, Input{URL: listingURL, Caller: gates.CallerCLI}, posting)
	w.tab.onClick = func(tab *scriptTab, c Control) { tab.set(chooser) }
	w.tab.onNav = func(tab *scriptTab, url string) {
		tab.set(&PageState{URL: "https://community.workday.com/maintenance-page", Title: "Maintenance", Text: "Workday is down"})
	}
	_, st := w.walk()
	if st == nil || st.class != ClassUnavailable || st.at != StageAccount {
		t.Fatalf("stop %+v: an account stage stop keeps the account stage", st)
	}
	last := w.e.Run.Stages[len(w.e.Run.Stages)-1]
	if last.Stage != StageAccount || last.Status != StatusFailed {
		t.Fatalf("last stage line %+v", last)
	}
}

// Procore's name-and-email step posts and the site redirects into its Workday
// tenant while the next fill runs: the fill the tab moved away from hands the
// run to Workday's application address, past its "Autofill with Resume / Apply
// Manually" chooser, as an Apply that lands there does.
func TestApply_Walk_AStepThatMovesIntoWorkdayMidFillOpensItsApplicationAddress(t *testing.T) {
	const job = "https://procore.wd12.myworkdayjobs.com/en-US/Procore_External_Careers/job/Staff-Engineer_R0018086"
	step := formPage(Control{I: 1, Tag: "button", Type: "submit", Labels: []string{"Apply"}, Form: 0, Name: "next_step"})
	chooser := &PageState{URL: job, Title: "Staff Engineer", Text: "Start Your Application", Lines: []string{"Start Your Application"},
		Controls: []Control{{I: 3, Tag: "a", Labels: []string{"Autofill with Resume"}, Form: -1}, {I: 4, Tag: "a", Labels: []string{"Apply Manually"}, Form: -1}}}
	form := appPage()
	form.URL = job + "/apply/applyManually"
	w := newWalk(t, Input{URL: "https://careers.example.test/jobs/7", Caller: gates.CallerCLI}, step)
	w.page.fillErr, w.page.fillErrOnce = &engine.Error{Code: engine.CodeTopOriginChanged, Message: "the tab moved to another origin during the fill"}, true
	w.page.onFill = func(n int) {
		if n == 1 {
			w.tab.set(chooser)
		}
	}
	w.tab.onNav = func(tab *scriptTab, url string) {
		if url == form.URL {
			tab.set(form)
		}
	}
	res, st := w.walk()
	if st != nil || res.End != EndSubmit {
		t.Fatalf("end %+v stop %+v", res, st)
	}
	if len(w.tab.navs) != 1 || w.tab.navs[0] != form.URL || w.e.Run.ATS != ATSWorkday {
		t.Fatalf("navigations %v ats %s", w.tab.navs, w.e.Run.ATS)
	}
	if w.tab.clicksOf("Autofill with Resume") != 0 || w.tab.clicksOf("Apply Manually") != 0 {
		t.Fatalf("clicks %v: the chooser is skipped, never answered", w.tab.clicks)
	}
}

// Node's run-multistep exiger case: a careers page carries HubSpot lead forms
// beside Greenhouse's iframe, which the embed script inserts after the page
// settled. The frame wins over the host's own form on the first page.
func TestApply_Handoff_EmbeddedFrameWinsOverTheHostsLeadForm(t *testing.T) {
	lead := func(frames ...FrameInfo) *PageState {
		st := shell(listingURL, frames...)
		st.Forms = []FormInfo{{Fields: 4}}
		st.Inputs = []InputInfo{{I: 0, Tag: "input", Type: "text", Label: "Company", Form: 0}, {I: 1, Tag: "input", Type: "email", Label: "Email", Form: 0}}
		st.Controls = []Control{{I: 2, Tag: "button", Labels: []string{"Submit"}, Form: 0}}
		return st
	}
	dest := appPage()
	dest.URL = ghEmbed
	w := newWalk(t, Input{URL: listingURL, Caller: gates.CallerCLI}, lead())
	w.tab.script(lead(), lead(), lead(visibleFrame(ghEmbed)))
	w.tab.onNav = func(tab *scriptTab, url string) { tab.set(dest) }
	res, st := w.walk()
	if st != nil || res.End != EndSubmit {
		t.Fatalf("end %+v stop %+v", res, st)
	}
	if len(w.tab.navs) != 1 || w.tab.navs[0] != ghEmbed || w.e.Run.ATS != ATSGreenhouse || w.page.nFills != 1 {
		t.Fatalf("navigations %v ats %s fills %d: the lead form is never filled", w.tab.navs, w.e.Run.ATS, w.page.nFills)
	}
}
