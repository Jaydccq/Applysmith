package discovery

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"applysmith/internal/workspace"
)

func linkedInPortals(t *testing.T, q map[string]any) workspace.Portals {
	if q == nil {
		q = map[string]any{"search": "engineer", "location": "Austin"}
	}
	return portalsWith(t, eng(), "linkedin", q)
}

func inboxFlags(e *env) map[string][]string {
	out := map[string][]string{}
	for _, r := range e.inbox(InboxQuery{}) {
		out[r.URL] = r.Flags
	}
	return out
}

// pageCards is a LinkedIn-shaped page of n cards numbered from first.
func pageCards(first, n int) fakePage {
	var cards []rawCard
	for i := 0; i < n; i++ {
		c := jobCard(first+i, fmt.Sprintf("Backend Engineer %d", first+i), fmt.Sprintf("Company %d", first+i))
		c.Href = fmt.Sprintf("https://www.linkedin.com/jobs/view/%d", first+i)
		cards = append(cards, c)
	}
	p := cardsPage(cards...)
	p.detail = detailRead{Desc: postingText()}
	return p
}

func startOf(raw string) int {
	n := 0
	if m := pageNumRE.FindStringSubmatch(raw); m != nil {
		fmt.Sscan(m[1], &n)
	}
	return n
}

func TestLinkedInJobs_DefaultsAndRowCap(t *testing.T) {
	lists := 0
	fb := newFakeBrowser(t, func(raw string) fakePage {
		if strings.Contains(raw, "/jobs/view/") {
			return fakePage{detail: detailRead{Desc: postingText()}}
		}
		lists++
		return pageCards(startOf(raw)+1, 25)
	})
	e := browserEnv(t, fb)
	rec := e.run(ScanRequest{Source: "linkedin", Portals: linkedInPortals(t, nil), Pages: 12, TopN: new(int)})
	if lists != 10 || rec.Counts.Discovered != 250 {
		t.Fatalf("list pages %d, discovered %d: no page is requested after the 250th row", lists, rec.Counts.Discovered)
	}
}

func TestLinkedInJobs_SearchURLUsesPaginatedPath(t *testing.T) {
	saved := "https://www.linkedin.com/jobs/search/?keywords=engineer&f_TPR=r86400&f_WT=2&f_AL=true"
	u, fresh, err := linkedInURL(linkedInBase, Query{P: map[string]string{"url": saved}}, 1, 24*time.Hour)
	if err != nil || !fresh {
		t.Fatalf("%v fresh=%v", err, fresh)
	}
	if !strings.Contains(u, "/jobs/search-results/") {
		t.Fatalf("url = %s", u)
	}
	q := mustQuery(t, u)
	// The results page drops location and f_WT; they travel as words.
	if q.Get("start") != "25" || q.Get("f_TPR") != "r86400" || q.Has("f_WT") || q.Get("f_AL") != "true" || q.Get("keywords") != "remote engineer" {
		t.Fatalf("query = %v", q)
	}
	built, fresh, _ := linkedInURL(linkedInBase, Query{Search: "engineer", P: map[string]string{"location": "Austin, TX", "remote": "true"}}, 0, 3*day)
	if q := mustQuery(t, built); q.Get("f_TPR") != "r259200" || q.Get("start") != "0" || !fresh || q.Has("location") || q.Has("f_WT") || q.Get("keywords") != "remote engineer in Austin, TX" {
		t.Fatalf("built = %s", built)
	}
}

func TestLinkedInJobs_RemoteFlagNarrowsSavedAndAdHocSearches(t *testing.T) {
	for _, req := range []ScanRequest{
		{Source: "linkedin", Portals: linkedInPortals(t, nil), Pages: 1, Remote: true},
		{Source: "linkedin", Portals: linkedInPortals(t, nil), Pages: 1, Remote: true, Search: "go engineer"},
	} {
		var asked []string
		fb := newFakeBrowser(t, func(raw string) fakePage {
			asked = append(asked, raw)
			return pageCards(1, 3)
		})
		browserEnv(t, fb).run(req)
		if len(asked) == 0 || !strings.Contains(mustQuery(t, asked[0]).Get("keywords"), "remote ") {
			t.Fatalf("search %q asked for %v", req.Search, asked)
		}
	}
}

func TestLinkedInJobs_EmptyPageReReadOnce(t *testing.T) {
	reads := map[int]int{}
	var mu sync.Mutex
	fb := newFakeBrowser(t, func(raw string) fakePage {
		if strings.Contains(raw, "/jobs/view/") {
			return fakePage{detail: detailRead{Desc: postingText()}}
		}
		s := startOf(raw)
		mu.Lock()
		reads[s]++
		n := reads[s]
		mu.Unlock()
		if s == 25 && n == 1 {
			return fakePage{} // the page had not rendered yet
		}
		return pageCards(s+1, 6)
	})
	rec := browserEnv(t, fb).run(ScanRequest{Source: "linkedin", Portals: linkedInPortals(t, nil), Pages: 2})
	if reads[25] != 2 || rec.Counts.Discovered != 12 {
		t.Fatalf("page 2 read %d times, discovered %d", reads[25], rec.Counts.Discovered)
	}
	reads = map[int]int{}
	fb2 := newFakeBrowser(t, func(raw string) fakePage {
		reads[startOf(raw)]++
		if startOf(raw) == 0 {
			return pageCards(1, 6)
		}
		return fakePage{}
	})
	browserEnv(t, fb2).run(ScanRequest{Source: "linkedin", Portals: linkedInPortals(t, nil), Pages: 3})
	if reads[25] != 2 || reads[50] != 0 {
		t.Fatalf("a page empty twice ends the results: %v", reads)
	}
}

func TestLinkedInJobs_KnownPageFrontierStopsAfterTwoPages(t *testing.T) {
	route := func(raw string) fakePage {
		if strings.Contains(raw, "/jobs/view/") {
			return fakePage{detail: detailRead{Desc: postingText()}}
		}
		return pageCards(startOf(raw)/25*10+1, 10)
	}
	first := browserEnv(t, newFakeBrowser(t, route))
	first.run(ScanRequest{Source: "linkedin", Portals: linkedInPortals(t, nil), Pages: 6, TopN: new(int)})
	// A second scan over the same results finds pages 2 and 3 already known.
	fb := newFakeBrowser(t, route)
	first.sc.Browser.Window = fb
	rec := first.run(ScanRequest{Source: "linkedin", Portals: linkedInPortals(t, nil), Pages: 6, TopN: new(int)})
	lists := 0
	for _, u := range fb.visited() {
		if strings.Contains(u, "search-results") {
			lists++
		}
	}
	if lists != 3 || rec.Reason != "frontier-reached" {
		t.Fatalf("list pages %d, reason %q", lists, rec.Reason)
	}
	fb3 := newFakeBrowser(t, route)
	first.sc.Browser.Window = fb3
	first.run(ScanRequest{Source: "linkedin", Portals: linkedInPortals(t, nil), Pages: 6, RetryKnown: true, TopN: new(int)})
	n := 0
	for _, u := range fb3.visited() {
		if strings.Contains(u, "search-results") {
			n++
		}
	}
	if n != 6 {
		t.Fatalf("--retry-known read %d pages, want all 6", n)
	}
}

func TestLinkedInJobs_RowsFromCardsWithoutClicking_AllCardsKeepOwnID(t *testing.T) {
	fb := newFakeBrowser(t, func(raw string) fakePage {
		if startOf(raw) > 0 || strings.Contains(raw, "/jobs/view/") {
			return fakePage{detail: detailRead{Desc: postingText()}}
		}
		return pageCards(1, 25)
	})
	e := browserEnv(t, fb)
	rec := e.run(ScanRequest{Source: "linkedin", Portals: linkedInPortals(t, nil), TopN: new(int)})
	if rec.Counts.Inserted != 25 || fb.count("click") != 0 {
		t.Fatalf("inserted %d clicks %d", rec.Counts.Inserted, fb.count("click"))
	}
	ids := map[string]bool{}
	for _, r := range e.inbox(InboxQuery{}) {
		ids[r.URL] = true
	}
	if len(ids) != 25 {
		t.Fatalf("%d distinct postings from 25 cards", len(ids))
	}
}

func TestLinkedInJobs_EasyApplyKeptWithFlagUnlessOnlyRequested(t *testing.T) {
	route := func(raw string) fakePage {
		if startOf(raw) > 0 || strings.Contains(raw, "/jobs/view/") {
			return fakePage{detail: detailRead{Desc: postingText()}}
		}
		p := pageCards(1, 2)
		var cards []rawCard
		cards = p.cards()
		cards[0].Easy = true
		p.extract.Rows = rawRows(cards...)
		p.detail = detailRead{Desc: postingText()}
		return p
	}
	e := browserEnv(t, newFakeBrowser(t, route))
	rec := e.run(ScanRequest{Source: "linkedin", Portals: linkedInPortals(t, nil), TopN: new(int)})
	flagged := 0
	for _, f := range inboxFlags(e) {
		for _, x := range f {
			if x == FlagEasyApply {
				flagged++
			}
		}
	}
	if rec.Counts.Inserted != 2 || flagged != 1 {
		t.Fatalf("inserted %d flagged %d", rec.Counts.Inserted, flagged)
	}
	e2 := browserEnv(t, newFakeBrowser(t, route))
	rec = e2.run(ScanRequest{Source: "linkedin", Portals: linkedInPortals(t, nil), TopN: new(int), EasyApplyOnly: true})
	if rec.Counts.Inserted != 1 || rec.Counts.Filtered["not-easy-apply"] != 1 {
		t.Fatalf("easy-apply-only: %+v", rec.Counts)
	}
}

func (p fakePage) cards() []rawCard {
	return listRead{Rows: p.extract.Rows}.cards()
}

func TestLinkedInJobs_RowsResolvingToSameEmployerPostingCollapse(t *testing.T) {
	route := func(raw string) fakePage {
		if startOf(raw) > 0 {
			return fakePage{}
		}
		p := pageCards(1, 3)
		p.detail = detailRead{Desc: postingText(), Apply: &applyControl{Href: "https://careers.example.com/jobs/777", Text: "Apply"}}
		return p
	}
	e := browserEnv(t, newFakeBrowser(t, route))
	rec := e.run(ScanRequest{Source: "linkedin", Portals: linkedInPortals(t, nil), TopN: new(int)})
	if rec.Counts.Inserted != 1 || rec.Counts.Duplicates[StoreRun] != 2 {
		t.Fatalf("counts = %+v", rec.Counts)
	}
}

func TestLinkedInJobs_ShortDetailFallsBackToGuestEndpoint(t *testing.T) {
	fb := newFakeBrowser(t, func(raw string) fakePage {
		if startOf(raw) > 0 {
			return fakePage{}
		}
		p := pageCards(1, 1)
		p.detail = detailRead{Desc: "too short"}
		return p
	})
	e := browserEnv(t, fb)
	rec := e.run(ScanRequest{Source: "linkedin", Portals: linkedInPortals(t, nil), TopN: new(int)})
	for _, u := range fb.visited() {
		if strings.Contains(u, "jobs-guest") || strings.Contains(u, "seeMore") {
			t.Fatalf("the undocumented guest endpoint was read: %s", u)
		}
	}
	rows := e.inbox(InboxQuery{})
	if rec.Counts.Inserted != 1 || len(rows) != 1 || rows[0].Description != "" {
		t.Fatalf("the row keeps its list text: %+v", rows)
	}
}

func TestLinkedInJobs_ExternalApplyURL_RejectsPixelsAssetsAndRequests(t *testing.T) {
	board := linkedInHost
	for _, c := range []struct {
		raw  string
		want bool
	}{
		{"https://careers.example.com/jobs/123", true},
		{"https://www.linkedin.com/jobs/view/5", false},
		{"https://cdn.example.com/logo.png?v=3", false},
		{"https://ad.doubleclick.net/pixel?x=1", false},
		{"javascript:void(0)", false},
	} {
		if got := usefulApplyURL(c.raw, board); got != c.want {
			t.Errorf("%q: %v, want %v", c.raw, got, c.want)
		}
	}
	if got := unwrapRedirect("https://www.linkedin.com/safety/go/?url=https%3A%2F%2Fcareers.example.com%2Fjobs%2F9&trk=x"); got != "https://careers.example.com/jobs/9" {
		t.Fatalf("unwrapped = %q", got)
	}
}

// employerRoute is a LinkedIn board whose Apply control is a button: pressing
// it opens the employer page in a popup.
func employerRoute(cards int) func(string) fakePage {
	return func(raw string) fakePage {
		switch {
		case strings.Contains(raw, "careers.example.com"):
			id := raw[strings.LastIndex(raw, "/")+1:]
			return fakePage{nav: Nav{URL: raw}, text: pageText{URL: raw, Text: "Backend Engineer " + id + "\n" + postingText()}}
		case strings.Contains(raw, "/jobs/view/"):
			id := raw[strings.LastIndex(raw, "/")+1:]
			return fakePage{detail: detailRead{Desc: "short", Apply: &applyControl{Text: "Apply"}},
				click: clickResult{Clicked: true}, popups: []string{"https://careers.example.com/jobs/" + id}}
		case startOf(raw) > 0:
			return fakePage{}
		}
		return pageCards(1, cards)
	}
}

func TestLinkedInJobs_ExternalPageReadOnlyAndClosed(t *testing.T) {
	fb := newFakeBrowser(t, employerRoute(1))
	e := browserEnv(t, fb)
	rec := e.run(ScanRequest{Source: "linkedin", Portals: linkedInPortals(t, nil), TopN: new(int)})
	rows := e.inbox(InboxQuery{})
	if rec.Status != RunCompleted || len(rows) != 1 || rows[0].URL != "https://careers.example.com/jobs/1" || len([]rune(rows[0].Description)) < 200 {
		t.Fatalf("rows = %+v", rows)
	}
	if fb.count("click") != 1 || rec.Detail["external-ats-navigations"] != 1 {
		t.Fatalf("clicks %d detail %v", fb.count("click"), rec.Detail)
	}
	if fb.opened != fb.closed {
		t.Fatalf("%d tabs opened, %d closed", fb.opened, fb.closed)
	}
	// The board's own address stays as an alias, and the row is no mirror.
	if len(rows[0].Aliases) != 1 || !strings.Contains(rows[0].Aliases[0], "/jobs/view/1") || slices.Contains(rows[0].Flags, FlagMirror) {
		t.Fatalf("aliases %v flags %v", rows[0].Aliases, rows[0].Flags)
	}
}

// trackerRoute is a LinkedIn board whose Apply popup is a click tracker; the
// tracker's page is what landing answers on each read.
func trackerRoute(landing func(reads int) pageText) func(string) fakePage {
	reads := 0
	return func(raw string) fakePage {
		if strings.Contains(raw, "click.tracker.example") {
			return fakePage{nav: Nav{URL: raw}, eval: map[string]any{"page-text": func() any { reads++; return landing(reads) }}}
		}
		p := employerRoute(1)(raw)
		if strings.Contains(raw, "/jobs/view/") {
			p.popups = []string{"https://click.tracker.example/t/abc"}
		}
		return p
	}
}

func TestLinkedInJobs_ApplyLandingIsReadWhereTheScriptRedirectEnds(t *testing.T) {
	fb := newFakeBrowser(t, trackerRoute(func(reads int) pageText {
		if reads < 3 {
			return pageText{URL: "https://www.diversityjobs.example/candidate/redirect_apply"}
		}
		return pageText{URL: "https://careers.example.com/jobs/1", Title: "Backend Engineer 1 - Careers", Text: postingText()}
	}))
	e := browserEnv(t, fb)
	e.sc.Browser.ContentWait = time.Second
	e.run(ScanRequest{Source: "linkedin", Portals: linkedInPortals(t, nil), TopN: new(int)})
	rows := e.inbox(InboxQuery{})
	if len(rows) != 1 || rows[0].URL != "https://careers.example.com/jobs/1" {
		t.Fatalf("rows = %+v", rows)
	}
}

func TestLinkedInJobs_ApplyLandingOnAnotherPostingKeepsTheBoardURL(t *testing.T) {
	fb := newFakeBrowser(t, trackerRoute(func(int) pageText {
		return pageText{URL: "https://jobs.ads.example/jobs/634288602-qa-intern", Title: "QA Intern (Summer 2027)", Text: "QA Intern (Summer 2027)\n" + postingText()}
	}))
	e := browserEnv(t, fb)
	rec := e.run(ScanRequest{Source: "linkedin", Portals: linkedInPortals(t, nil), TopN: new(int)})
	rows := e.inbox(InboxQuery{})
	if len(rows) != 1 || rows[0].URL != "https://www.linkedin.com/jobs/view/1" || rec.Detail["apply-landing-other-posting"] != 1 {
		t.Fatalf("rows = %+v, detail %v", rows, rec.Detail)
	}
}

func TestBrowserScan_FollowThroughClickBounded(t *testing.T) {
	fb := newFakeBrowser(t, employerRoute(41))
	e := browserEnv(t, fb)
	rec := e.run(ScanRequest{Source: "linkedin", Portals: linkedInPortals(t, nil), TopN: new(int)})
	if fb.count("click") != 40 || rec.Counts.Inserted != 41 {
		t.Fatalf("clicks %d inserted %d", fb.count("click"), rec.Counts.Inserted)
	}
	gap := 0
	for _, f := range inboxFlags(e) {
		for _, x := range f {
			if x == FlagEmployerURLGap {
				gap++
			}
		}
	}
	if gap < 1 {
		t.Fatal("the row after the 40th click is not flagged employer-url-unresolved")
	}
}

func TestLinkedInJobs_ScanNeverEnqueuesApplyRun(t *testing.T) {
	fb := newFakeBrowser(t, func(raw string) fakePage {
		if startOf(raw) > 0 || strings.Contains(raw, "/jobs/view/") {
			return fakePage{detail: detailRead{Desc: postingText(), Easy: true}}
		}
		p := pageCards(1, 2)
		c := p.cards()
		c[0].Easy, c[1].Easy = true, true
		p.extract.Rows = rawRows(c...)
		return p
	})
	e := browserEnv(t, fb)
	rec := e.run(ScanRequest{Source: "linkedin", Portals: linkedInPortals(t, nil)})
	if fb.count("click") != 0 {
		t.Fatal("an Easy Apply control was pressed")
	}
	for _, r := range e.enq.reqs {
		if r.Mode != "auto" {
			t.Fatalf("evaluation mode %q", r.Mode)
		}
	}
	if rec.Counts.Queued != 2 || evalMode("linkedin", "") != "auto" || evalMode("jobright", "") != "auto" {
		t.Fatalf("queued %d", rec.Counts.Queued)
	}
}

func TestLinkedInJobs_ProfileRolesBuildTheSearchWhenNothingIsSaved(t *testing.T) {
	fb := newFakeBrowser(t, func(raw string) fakePage { return fakePage{} })
	e := browserEnv(t, fb)
	req := ScanRequest{Source: "linkedin", Portals: eng()}
	req.Profile.Targets.Roles = append(req.Profile.Targets.Roles, roleOf("Backend Engineer"))
	e.run(req)
	if v := fb.visited(); len(v) == 0 || !strings.Contains(v[0], "keywords=Backend+Engineer") || !strings.Contains(v[0], "f_TPR=r86400") {
		t.Fatalf("visited = %v", v)
	}
}

// ---- Indeed ----

func indeedPortals(t *testing.T, q map[string]any) workspace.Portals {
	if q == nil {
		q = map[string]any{"search": "engineer", "location": "Austin"}
	}
	return portalsWith(t, eng(), "indeed", q)
}

func TestIndeed_SearchURLFromSavedSearch(t *testing.T) {
	q := Query{Search: "backend engineer", P: map[string]string{"location": "Austin, TX", "job_type": "full-time", "experience_level": "entry"}}
	u, fresh, err := indeedURL(indeedBase, q, 1, 3*day)
	if err != nil || !fresh {
		t.Fatal(err)
	}
	v := mustQuery(t, u)
	if v.Get("q") != "backend engineer" || v.Get("l") != "Austin, TX" || v.Get("jt") != "fulltime" || v.Get("explvl") != "entry_level" || v.Get("fromage") != "3" || v.Get("start") != "10" {
		t.Fatalf("query = %v", v)
	}
	if _, fresh, _ := indeedURL(indeedBase, q, 0, 0); fresh {
		t.Fatal("no window, no parameter")
	}
	if _, fresh, _ := indeedURL(indeedBase, q, 0, 30*day); fresh {
		t.Fatal("a window wider than the board's menu has no parameter")
	}
}

func TestIndeed_SearchIdentity_DroppedFilterAcceptedOffsetStrict(t *testing.T) {
	req := "https://www.indeed.com/jobs?q=engineer&l=Austin&jt=fulltime&start=10"
	g := indeedGuard(req)
	for _, c := range []struct {
		live string
		want bool
	}{
		{"https://www.indeed.com/jobs?q=engineer&l=Austin&start=10", true},                     // filter dropped by the site
		{"https://www.indeed.com/jobs?q=Engineer&l=Austin&jt=fulltime&start=10&vjk=abc", true}, // tracking added
		{"https://www.indeed.com/jobs?q=engineer&l=Austin&jt=parttime&start=10", false},        // contradiction
		{"https://www.indeed.com/jobs?q=engineer&l=Austin&jt=fulltime&start=20", false},        // offset strict
	} {
		if got := g(c.live); got != c.want {
			t.Errorf("%s: %v, want %v", c.live, got, c.want)
		}
	}
}

func TestIndeed_WarmUpPrecedesDeepPagination(t *testing.T) {
	fb := newFakeBrowser(t, pagedRoute(nil))
	e := browserEnv(t, fb)
	p := indeedPortals(t, map[string]any{"url": "https://www.indeed.com/jobs?q=engineer&start=20&fromage=1"})
	e.run(ScanRequest{Source: "indeed", Portals: p, TopN: new(int)})
	v := fb.visited()
	if len(v) < 2 || v[0] != "https://www.indeed.com/" || !strings.Contains(v[1], "start=20") {
		t.Fatalf("visits = %v", v)
	}
	if len(fb.sleeps) == 0 {
		t.Fatal("no pacing delay between the home page and the deep request")
	}
	fb2 := newFakeBrowser(t, pagedRoute(nil))
	browserEnv(t, fb2).run(ScanRequest{Source: "indeed", Portals: indeedPortals(t, nil), TopN: new(int)})
	if v := fb2.visited(); strings.HasSuffix(v[0], "indeed.com/") {
		t.Fatalf("a first page request was warmed up: %v", v)
	}
}

func TestIndeed_TransientNavigationRetriedNotDetection(t *testing.T) {
	calls := 0
	fb := newFakeBrowser(t, func(raw string) fakePage {
		p := pagedRoute(nil)(raw)
		if strings.Contains(raw, "/jobs?") {
			calls++
			if calls == 1 {
				return fakePage{nav: Nav{Failed: "net::ERR_ABORTED navigation in progress"}}
			}
		}
		return p
	})
	e := browserEnv(t, fb)
	rec := e.run(ScanRequest{Source: "indeed", Portals: indeedPortals(t, nil), TopN: new(int)})
	if rec.Status != RunCompleted || rec.Detail["retries"] != 1 || rec.Detail["page-failed"] != 0 || rec.Counts.Inserted != 3 {
		t.Fatalf("status %s detail %v inserted %d", rec.Status, rec.Detail, rec.Counts.Inserted)
	}
	if again := e.run(ScanRequest{Source: "indeed", Portals: indeedPortals(t, nil), TopN: new(int)}); again.Status == RunSkipped {
		t.Fatal("a transient navigation set a cooldown")
	}
}

func TestIndeed_IdentityMismatchIsRetriedThenSkipped(t *testing.T) {
	fb := newFakeBrowser(t, func(raw string) fakePage {
		p := pagedRoute(nil)(raw)
		p.extract.URL = "https://www.indeed.com/jobs?q=engineer&l=Austin&start=999"
		return p
	})
	rec := browserEnv(t, fb).run(ScanRequest{Source: "indeed", Portals: indeedPortals(t, nil), TopN: new(int)})
	if rec.Detail["page-failed"] != 1 || len(fb.visited()) != 2 {
		t.Fatalf("detail %v visits %v", rec.Detail, fb.visited())
	}
}

func TestIndeed_ApplyProbeLandingOnOtherJobKeyIsRejected(t *testing.T) {
	for _, landing := range []string{"https://www.indeed.com/viewjob?jk=OTHER99", "https://ads.example.net/redirect?jk=OTHER99"} {
		fb := newFakeBrowser(t, func(raw string) fakePage {
			switch {
			case strings.Contains(raw, "OTHER99"):
				return fakePage{nav: Nav{URL: landing}, text: pageText{Text: postingText()}}
			case strings.Contains(raw, "viewjob?jk="):
				return fakePage{detail: detailRead{Desc: postingText(), Apply: &applyControl{Text: "Apply on company site"}},
					click: clickResult{Clicked: true}, popups: []string{landing}}
			}
			return cardsPage(rawCard{ID: "AAA111", Title: "Backend Engineer", Company: "Acme", Age: "1 day ago"})
		})
		e := browserEnv(t, fb)
		e.run(ScanRequest{Source: "indeed", Portals: indeedPortals(t, nil), TopN: new(int)})
		rows := e.inbox(InboxQuery{})
		if len(rows) != 1 || !strings.Contains(rows[0].URL, "viewjob?jk=AAA111") {
			t.Fatalf("%s: rows = %+v", landing, rows)
		}
	}
}

func TestIndeed_UnresolvedCardStoredAsMirror(t *testing.T) {
	fb := newFakeBrowser(t, func(raw string) fakePage {
		if strings.Contains(raw, "viewjob") {
			return fakePage{detail: detailRead{Desc: postingText()}}
		}
		return cardsPage(rawCard{ID: "AAA111", Title: "Backend Engineer", Company: "Acme", Age: "1 day ago"})
	})
	e := browserEnv(t, fb)
	e.sc.Mirror = fakeMirror{hosts: []string{"indeed.com"}, resolve: func(string, string, string) MirrorResult { return MirrorResult{Reason: "no board lists it"} }}
	e.run(ScanRequest{Source: "indeed", Portals: indeedPortals(t, nil), TopN: new(int)})
	rows := e.inbox(InboxQuery{})
	if len(rows) != 1 || !strings.Contains(rows[0].URL, "viewjob?jk=AAA111") {
		t.Fatalf("rows = %+v", rows)
	}
	flags := strings.Join(rows[0].Flags, ",")
	if !strings.Contains(flags, FlagMirror) || !strings.Contains(flags, FlagMirrorUnresolved) {
		t.Fatalf("flags = %s", flags)
	}
}

// ---- ZipRecruiter, Google Jobs, Glassdoor ----

func TestZipRecruiter_CardWithOnlyRedirectIsMirror(t *testing.T) {
	card := rawCard{ID: "z1", Title: "Backend Engineer", Company: "Acme", Location: "Austin, TX", Age: "2 hours ago",
		Href: "https://www.ziprecruiter.com/c/Acme/Job/Backend-Engineer/-in-Austin,TX?jid=z1"}
	fb := newFakeBrowser(t, func(raw string) fakePage {
		if strings.Contains(raw, "/c/Acme") {
			return fakePage{detail: detailRead{Desc: postingText()}}
		}
		return cardsPage(card)
	})
	e := browserEnv(t, fb)
	e.run(ScanRequest{Source: "ziprecruiter", Portals: zipPortals(t, nil), TopN: new(int)})
	rows := e.inbox(InboxQuery{})
	if len(rows) != 1 || !strings.Contains(rows[0].URL, "ziprecruiter.com/c/Acme") || !strings.Contains(strings.Join(rows[0].Flags, ","), FlagMirror) {
		t.Fatalf("rows = %+v", rows)
	}
}

func TestGoogleJobs_WindowMapsToHonoredPhrasing_LocalFilterEnforcesAsk(t *testing.T) {
	for _, c := range []struct {
		w    time.Duration
		want string
	}{{time.Hour, "date_posted:3days"}, {7 * day, "date_posted:week"}, {0, ""}} {
		if got := googleChip(c.w); got != c.want {
			t.Errorf("%v: %q, want %q", c.w, got, c.want)
		}
	}
	fb := newFakeBrowser(t, googleRoute(2, func(raw string, p fakePage) fakePage {
		cards := p.cards()
		cards[0].Age, cards[1].Age = "30 minutes ago", "2 days ago"
		p.extract.Rows = rawRows(cards...)
		return p
	}))
	e := browserEnv(t, fb)
	rec := e.run(ScanRequest{Source: "google-jobs", Portals: portalsWith(t, eng(), "google-jobs", map[string]any{"search": "engineer"}), Window: time.Hour, TopN: new(int)})
	if v := fb.visited(); !strings.Contains(v[0], "htichips=date_posted%3A3days") {
		t.Fatalf("visits = %v", v)
	}
	if rec.Counts.Filtered["stale"] != 1 || rec.Counts.Inserted != 1 {
		t.Fatalf("counts = %+v", rec.Counts)
	}
}

func TestGoogleJobs_EmployerURLResolvedDuringEnrichment(t *testing.T) {
	fb := newFakeBrowser(t, googleRoute(2, nil))
	e := browserEnv(t, fb)
	rec := e.run(ScanRequest{Source: "google-jobs", Portals: portalsWith(t, eng(), "google-jobs", map[string]any{"search": "engineer"}), TopN: new(int)})
	rows := e.inbox(InboxQuery{})
	if rec.Counts.Inserted != 2 || len(rows) != 2 || !strings.HasPrefix(rows[0].URL, "https://careers.example.com/jobs/") {
		t.Fatalf("counts %+v rows %+v", rec.Counts, rows)
	}
}

func TestAdmission_ListingWithoutEmployerURLReachesEnrichmentFirst(t *testing.T) {
	fb := newFakeBrowser(t, googleRoute(3, func(raw string, p fakePage) fakePage {
		if strings.HasSuffix(raw, "doc3") {
			p.detail.Apply = nil // this posting never shows an employer link
		}
		return p
	}))
	e := browserEnv(t, fb)
	rec := e.run(ScanRequest{Source: "google-jobs", Portals: portalsWith(t, eng(), "google-jobs", map[string]any{"search": "engineer"}), TopN: new(int)})
	if rec.Counts.Inserted != 2 || rec.Counts.Filtered[FilterNonPostingURL] != 1 || rec.Counts.Filtered[FilterInvalidURL] != 0 {
		t.Fatalf("counts = %+v: 2 resolved rows are admitted, the unresolved share URL is refused after enrichment", rec.Counts)
	}
}

func TestBrowserScan_GoogleDetailChallengeStopsRemainingReads(t *testing.T) {
	fb := newFakeBrowser(t, googleRoute(8, func(raw string, p fakePage) fakePage {
		p.detail = detailRead{Head: "Our systems have detected unusual traffic from your computer network."}
		return p
	}))
	e := browserEnv(t, fb)
	e.sc.Browser.Host = func(context.Context) HostSample { return HostSample{Verdict: PressureWarn, FreeMiB: 400, CPUs: 4} }
	rec := e.run(ScanRequest{Source: "google-jobs", Portals: portalsWith(t, eng(), "google-jobs", map[string]any{"search": "engineer"}), TopN: new(int)})
	if rec.Status != RunBlocked || rec.Detail["detail-opens"] != 1 || rec.Counts.EnrichmentSkipped != 8 || rec.Counts.Enriched != 0 {
		t.Fatalf("status %s detail %v counts %+v", rec.Status, rec.Detail, rec.Counts)
	}
	if err := rec.Counts.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestGlassdoor_SignedOutEndsNeedsSignIn(t *testing.T) {
	for _, pg := range []fakePage{
		{nav: Nav{URL: "https://www.glassdoor.com/profile/login_input.htm"}},
		{probe: pageProbe{Text: "Sign in to see all jobs near you and get matched."}},
	} {
		fb := newFakeBrowser(t, func(string) fakePage { return pg })
		rec := browserEnv(t, fb).run(ScanRequest{Source: "glassdoor", Portals: portalsWith(t, eng(), "glassdoor", map[string]any{"search": "engineer"})})
		if rec.Status != RunNeedsSignIn || rec.Counts.Inserted != 0 {
			t.Fatalf("status %s", rec.Status)
		}
	}
}

func TestGlassdoor_PostingsAreMirrorsFromTheBoardLink(t *testing.T) {
	card := rawCard{ID: "99", Title: "Backend Engineer", Company: "Acme", Location: "Austin, TX", Age: "24h", Href: "https://www.glassdoor.com/job-listing/backend-engineer-acme-JV_IC1.htm?jl=99"}
	fb := newFakeBrowser(t, func(raw string) fakePage {
		if strings.Contains(raw, "job-listing") {
			return fakePage{detail: detailRead{Desc: postingText()}}
		}
		return cardsPage(card)
	})
	e := browserEnv(t, fb)
	rec := e.run(ScanRequest{Source: "glassdoor", Portals: portalsWith(t, eng(), "glassdoor", map[string]any{"search": "engineer"}), TopN: new(int)})
	if rec.Counts.Inserted != 1 || !strings.Contains(strings.Join(e.inbox(InboxQuery{})[0].Flags, ","), FlagMirror) {
		t.Fatalf("counts %+v", rec.Counts)
	}
}

func roleOf(title string) workspace.Role { return workspace.Role{Title: title} }

func TestIndeed_CanonicalPostingIdentityKeepsOnlyTheJobKey(t *testing.T) {
	a, _ := indeedPosting(indeedBase, rawCard{ID: "abc123", Title: "Backend Engineer"}, time.Now())
	b := a
	b.URL = "https://www.indeed.com/viewjob?jk=abc123&tk=1hx&from=serp&vjs=3"
	ca, err1 := postingURL(a.URL)
	cb, err2 := postingURL(b.URL)
	if err1 != nil || err2 != nil || ca != cb {
		t.Fatalf("%q vs %q (%v %v)", ca, cb, err1, err2)
	}
	other, _ := indeedPosting(indeedBase, rawCard{ID: "zzz999", Title: "Backend Engineer"}, time.Now())
	if co, _ := postingURL(other.URL); co == ca {
		t.Fatal("two job keys share a posting identity")
	}
}

func TestBrowserBoards_AdHocCityAndStateSetTheSearchLocation(t *testing.T) {
	q := Query{Search: "engineer", P: map[string]string{"city": "Austin", "state": "TX"}}
	for name, build := range map[string]func(string, Query, int, time.Duration) (string, bool, error){"indeed": indeedURL, "ziprecruiter": zipURL} {
		got, _, err := build("https://board.example.test", q, 0, 0)
		if err != nil || !strings.Contains(got, "Austin%2C+TX") {
			t.Errorf("%s: url %q err %v", name, got, err)
		}
	}
	if got := (Query{P: map[string]string{"location": "Denver", "city": "Austin"}}).place(); got != "Denver" {
		t.Errorf("a saved search's location wins over the ad hoc city: %q", got)
	}
}

// glassdoorRoute answers Glassdoor's home page (with the location typeahead's
// answer) and its results and posting pages.
func glassdoorRoute(places any, card rawCard, posted string) func(string) fakePage {
	return func(raw string) fakePage {
		switch {
		case strings.Contains(raw, "/Job/index.htm"):
			return fakePage{eval: map[string]any{"glassdoor-locate": places}}
		case strings.Contains(raw, "job-listing"):
			return fakePage{detail: detailRead{Desc: postingText(), DatePosted: posted}}
		}
		return cardsPage(card)
	}
}

func TestGlassdoor_LocationIsResolvedToItsIdBeforeTheSearch(t *testing.T) {
	card := rawCard{ID: "99", Title: "Backend Engineer", Company: "Acme", Location: "Austin, TX", Href: "https://www.glassdoor.com/job-listing/backend-engineer-acme-JV_IC1.htm?jl=99"}
	// What the typeahead answers for "Austin, TX" (fields as the site sends them).
	// The first is the site's own ranking; the exact name wins over it.
	places := map[string]any{"status": 200, "places": []map[string]string{
		{"id": "3757246", "type": "C", "name": "Macedonia, Austin, TX", "label": "Macedonia, Austin, TX (US)"},
		{"id": "1139761", "type": "C", "name": "Austin, TX", "label": "Austin, TX (US)"},
		{"id": "1", "type": "S", "name": "Texas", "label": "Texas (US)"}}}
	fb := newFakeBrowser(t, glassdoorRoute(places, card, "2026-10-09"))
	e := browserEnv(t, fb)
	rec := e.run(ScanRequest{Source: "glassdoor", Portals: portalsWith(t, eng(), "glassdoor", map[string]any{"search": "engineer", "location": "Austin, TX"}), TopN: new(int)})
	if rec.Status != RunCompleted || rec.Counts.Inserted != 1 || rec.Detail["location-resolved"] != 1 {
		t.Fatalf("status %s counts %+v detail %v", rec.Status, rec.Counts, rec.Detail)
	}
	var results string
	for _, u := range fb.visited() {
		if strings.Contains(u, "-SRCH_") {
			results = u
		}
	}
	if want := "https://www.glassdoor.com/Job/austin-tx-engineer-jobs-SRCH_IL.0,9_IC1139761_KO10,18.htm?"; !strings.HasPrefix(results, want) {
		t.Errorf("results url %q, want %q", results, want)
	}
	// The card shows no age; the posting page's structured data dates it.
	if got := e.inbox(InboxQuery{})[0].PostedAt; got.IsZero() || got.Format("2006-01-02") != "2026-10-09" {
		t.Errorf("posted at %v, want the posting page's datePosted", got)
	}
}

// The results page is addressed by the site's own search path: jobs.htm
// redirects there and drops fromAge (live: 90,026 results of every age
// instead of 5,540 from the last week), and no address turns a page.
func TestGlassdoor_SearchPathCarriesTheWindowThatJobsHtmDropped(t *testing.T) {
	week := 7 * 24 * time.Hour
	for _, c := range []struct {
		q    Query
		want string
	}{
		{Query{Search: "software engineer"}, "https://www.glassdoor.com/Job/software-engineer-jobs-SRCH_KO0,17.htm?fromAge=7"},
		{Query{Search: "Software Engineer", P: map[string]string{"location": "Austin, TX", "locT": "C", "locId": "1139761"}},
			"https://www.glassdoor.com/Job/austin-tx-software-engineer-jobs-SRCH_IL.0,9_IC1139761_KO10,27.htm?fromAge=7"},
		{Query{Search: "software engineer", P: map[string]string{"location": "Texas", "locT": "S", "locId": "1347"}},
			"https://www.glassdoor.com/Job/texas-software-engineer-jobs-SRCH_IL.0,5_IS1347_KO6,23.htm?fromAge=7"},
	} {
		got, windowed, err := glassdoorURL(glassdoorBase, c.q, 0, week)
		if err != nil || got != c.want || !windowed {
			t.Errorf("%+v: %q %v %v, want %q", c.q, got, windowed, err, c.want)
		}
	}
	if _, _, err := glassdoorURL(glassdoorBase, Query{Search: " "}, 0, week); err == nil {
		t.Error("a search with no words built a results address")
	}
	if glassdoorSpec.maxPages != 1 {
		t.Errorf("maxPages %d: the site serves one page per address", glassdoorSpec.maxPages)
	}
}

func TestGlassdoor_UnresolvedLocationFailsTheSearchInsteadOfSearchingTheCountry(t *testing.T) {
	card := rawCard{ID: "99", Title: "Backend Engineer", Company: "Acme", Href: "https://www.glassdoor.com/job-listing/backend-engineer-acme-JV_IC1.htm?jl=99"}
	for name, places := range map[string]any{
		"unknown place":  map[string]any{"status": 200, "places": []map[string]string{}},
		"lookup refused": map[string]any{"status": 403, "places": nil},
	} {
		fb := newFakeBrowser(t, glassdoorRoute(places, card, ""))
		rec := browserEnv(t, fb).run(ScanRequest{Source: "glassdoor", Portals: portalsWith(t, eng(), "glassdoor", map[string]any{"search": "engineer", "location": "Nowhereville"}), TopN: new(int)})
		if rec.Status != RunFailed || rec.Counts.Inserted != 0 {
			t.Errorf("%s: status %s counts %+v", name, rec.Status, rec.Counts)
		}
		for _, u := range fb.visited() {
			if strings.Contains(u, "/Job/jobs.htm") {
				t.Errorf("%s: the country was searched: %s", name, u)
			}
		}
	}
}

func TestGlassdoor_ChallengeOnTheLocationLookupPageEndsTheRunBlocked(t *testing.T) {
	fb := newFakeBrowser(t, func(raw string) fakePage {
		return fakePage{nav: Nav{URL: raw, Status: 403}}
	})
	rec := browserEnv(t, fb).run(ScanRequest{Source: "glassdoor", Portals: portalsWith(t, eng(), "glassdoor", map[string]any{"search": "engineer", "location": "Austin, TX"}), TopN: new(int)})
	if rec.Status != RunBlocked {
		t.Fatalf("status %s (%s)", rec.Status, rec.Error)
	}
}

func TestEmployerApply_TheControlThatNamesTheCompany(t *testing.T) {
	ctl := func(texts ...string) []applyControl {
		out := make([]applyControl, len(texts))
		for i, s := range texts {
			out[i] = applyControl{Text: s}
		}
		return out
	}
	for _, c := range []struct {
		company string
		applies []applyControl
		want    int
	}{
		{"Delta Systems", ctl("Apply on LinkedIn", "Apply on BeBee", "Apply on Delta Careers"), 2},
		{"The Structures Company, LLC", ctl("Apply on Indeed", "Apply directly on The Structures Company, LLC"), 1},
		{"Delta", ctl("Apply on Deltaforce Jobs"), -1},
		{"Acme", ctl("Apply on LinkedIn", "Apply on Indeed"), -1},
	} {
		if got := employerApply(c.applies, c.company); got != c.want {
			t.Errorf("%q in %v = %d, want %d", c.company, c.applies, got, c.want)
		}
	}
}
