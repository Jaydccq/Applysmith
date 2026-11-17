package signals

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestCompany_CanonicalizationRulesFirstModelCached(t *testing.T) {
	cases := map[string]string{"Example Labs, Inc.": "example", "ACME Technologies LLC": "acme", "Foo & Bar Co.": "foo bar", "AI Labs Inc": ""}
	for raw, want := range cases {
		if got := RuleCanon(raw); got != want {
			t.Errorf("RuleCanon(%q) = %q, want %q", raw, got, want)
		}
	}
	e := newEnv(t)
	e.brain.fn = func(task, prompt string) (string, bool) { return `{"name":"Aigent Systems"}`, true }
	for i := 0; i < 2; i++ {
		got, err := e.svc.canonical(e.ctx, "AI Labs Inc")
		if err != nil || got != "aigent systems" {
			t.Fatalf("canonical = %q, %v", got, err)
		}
	}
	if n := e.brain.count("feed-classify"); n != 1 {
		t.Fatalf("model calls = %d, want 1", n)
	}
	if _, err := e.svc.canonical(e.ctx, "Example Labs, Inc."); err != nil || e.brain.count("feed-classify") != 1 {
		t.Fatalf("a rule-canonical name cost a call")
	}
}

func TestEntityGuard_HeadlineShapesRejectedNeverRewritten(t *testing.T) {
	bad := []string{"Acme Co raises $20M Series B", "Example launches new product", "Beta Corp acquires Gamma", "Series A", "$5M seed round", "example.com", "https://example.test/x",
		"Example Corp announces", strings.Repeat("Long ", 13), "One Two Three Four Five Six Seven Eight Nine", "", "Q closes round of funding"}
	for _, n := range bad {
		if NotAnEntity(n) == "" {
			t.Errorf("%q passed the guard", n)
		}
	}
	for _, n := range []string{"Example Corp", "3M", "Acme Robotics", "Foo & Bar"} {
		if r := NotAnEntity(n); r != "" {
			t.Errorf("%q rejected: %s", n, r)
		}
	}
}

func TestEntityGuard_AppliesWhenExtractorAbsentOrFails(t *testing.T) {
	const headline = "Acme Co raises $20M Series B"
	// With the extractor absent or failing, the rule path's candidate is the
	// final one, and the guard still applies to it.
	for name, model := range map[string]func(Model) Model{
		"absent": func(Model) Model { return noRouteModel{} },
		"fails":  func(inner Model) Model { return flakyModel{inner: inner, fail: headline} },
	} {
		t.Run(name, func(t *testing.T) {
			e := newEnv(t)
			e.svc.model = model(e.svc.model)
			e.doc(Doc{SourceID: SourceHNHiring, Adapter: adapterHNHiring, URL: "https://news.ycombinator.com/item?id=1", Title: headline + " | Remote", Body: headline + " and is hiring."})
			e.tick(TickRequest{})
			if n := e.count(`SELECT count(*) FROM signal_companies`); n != 0 {
				t.Fatalf("%d companies were created from a headline", n)
			}
		})
	}
	// The extractor's own answer is guarded too.
	e := newEnv(t)
	e.brain.fn = func(task, prompt string) (string, bool) { return `{"company":"` + headline + `"}`, true }
	id := e.doc(Doc{SourceID: SourceAlerts, URL: "https://news.example.test/h", Title: headline, Body: headline})
	e.tick(TickRequest{})
	if n := e.count(`SELECT count(*) FROM signal_companies`); n != 0 {
		t.Fatalf("%d companies were created from a headline", n)
	}
	var identified int
	if err := e.st.Read().QueryRowContext(e.ctx, `SELECT count(identified_at) FROM signal_documents WHERE id = ?`, id).Scan(&identified); err != nil || identified != 1 {
		t.Fatalf("identified = %d, %v", identified, err)
	}
}

func TestEntityGuard_FilingIssuerNamesAreNotGuarded(t *testing.T) {
	e := newEnv(t)
	name := "Example Opportunity Fund Series 2026-A Limited Partnership Holdings Trust Raises"
	e.doc(Doc{SourceID: SourceEdgar, Adapter: adapterEdgar, URL: "https://www.sec.gov/f/1", PublishedAt: e.clk.now(), Title: "Form D", Body: "filing",
		Filing: &Filing{Issuer: name, Amount: 1e6, FirstSale: e.clk.now()}})
	e.tick()
	if n := e.count(`SELECT count(*) FROM signal_companies`); n != 1 {
		t.Fatalf("companies = %d", n)
	}
}

func roundupBody(n int) string {
	var b strings.Builder
	for i := 0; i < n; i++ {
		fmt.Fprintf(&b, "- Company%c Systems: builds widgets number %d\n", 'A'+i, i)
	}
	return b.String()
}

func TestRoundup_ThreeToTwentyFiveDistinctNames(t *testing.T) {
	if got := Roundup(roundupBody(6)); len(got) != 6 {
		t.Fatalf("6 lines yielded %v", got)
	}
	if got := Roundup(roundupBody(2)); got != nil {
		t.Fatalf("2 names yielded %v", got)
	}
	if got := Roundup(roundupBody(26)); got != nil {
		t.Fatalf("26 names yielded %d", len(got))
	}
	same := "- Alpha Inc: one\n- Alpha, Inc.: two\n- Beta: three\n"
	if got := Roundup(same); got != nil {
		t.Fatalf("2 distinct names yielded %v", got)
	}
	dash := "* Alpha Corp - a thing\n* Beta - another\n1. Gamma Labs: third\n- hyphen-name-without-space not a dash\n"
	got := Roundup(dash)
	if len(got) != 3 || got[0] != "Alpha" || got[2] != "Gamma Labs" {
		t.Fatalf("got %v", got)
	}
	li := htmlToText("<ul><li>Alpha: x</li><li>Beta: y</li><li>Gamma: z</li></ul>")
	if got := Roundup(li); len(got) != 3 {
		t.Fatalf("list tags: %v from %q", got, li)
	}
}

func TestRoundup_OnlyWhenTitleLaneDeclines(t *testing.T) {
	e := newEnv(t)
	body := "Ten startups to watch\n" + roundupBody(6)
	e.brain.fn = func(task, prompt string) (string, bool) { return `{"company":null}`, true }
	e.doc(Doc{SourceID: SourceAlerts, URL: "https://news.example.test/roundup", PublishedAt: e.clk.now(), Title: "Startups to watch", Body: body})
	e.tick()
	if n := e.count(`SELECT count(*) FROM signal_companies`); n != 6 {
		t.Fatalf("companies = %d", n)
	}
	// Six events from one document: each company has its own.
	if n := e.count(`SELECT count(DISTINCT event_id) FROM signal_event_docs`); n != 6 {
		t.Fatalf("events = %d", n)
	}
	if n := e.count(`SELECT count(DISTINCT document_id) FROM signal_event_docs`); n != 1 {
		t.Fatalf("documents = %d", n)
	}

	// An answer that names a company wins, and the roundup rule stays unused.
	e2 := newEnv(t)
	e2.brain.fn = func(task, prompt string) (string, bool) { return `{"company":"Solo Systems"}`, true }
	e2.doc(Doc{SourceID: SourceAlerts, URL: "https://news.example.test/roundup", PublishedAt: e2.clk.now(), Title: "Solo Systems", Body: body})
	e2.tick()
	if n := e2.count(`SELECT count(*) FROM signal_companies`); n != 1 {
		t.Fatalf("companies = %d", n)
	}
}

func TestEvents_MergeWithinSevenDaysEqualAmountPreferred(t *testing.T) {
	e := newEnv(t)
	c, err := e.svc.repo.UpsertCompany(e.ctx, "Example Corp", "example")
	if err != nil {
		t.Fatal(err)
	}
	day := 24 * time.Hour
	t0 := epoch.Add(-30 * day)
	d1 := e.doc(Doc{URL: "https://a.example.test/1", Title: "t", Body: "b"})
	d2 := e.doc(Doc{URL: "https://a.example.test/2", Title: "t", Body: "b"})
	d3 := e.doc(Doc{URL: "https://a.example.test/3", Title: "t", Body: "b"})
	d4 := e.doc(Doc{URL: "https://a.example.test/4", Title: "t", Body: "b"})
	five, six := 5e6, 6e6
	ev1, created, _ := e.svc.repo.AttachEvent(e.ctx, d1, c.ID, TypeFunding, t0, &five)
	if !created {
		t.Fatal("first event not created")
	}
	ev2, created, _ := e.svc.repo.AttachEvent(e.ctx, d2, c.ID, TypeFunding, t0.Add(10*day), &six)
	if !created || ev2 == ev1 {
		t.Fatal("an event 10 days later merged")
	}
	// 3 days after the first: merges, and its time does not move the event.
	got, created, _ := e.svc.repo.AttachEvent(e.ctx, d3, c.ID, TypeFunding, t0.Add(3*day), &five)
	if created || got != ev1 {
		t.Fatalf("corroboration: event %d created=%v", got, created)
	}
	ev, _ := e.svc.repo.Event(e.ctx, ev1)
	if !ev.Time.Equal(t0) {
		t.Fatalf("event time drifted to %v", ev.Time)
	}
	// Re-processing a document is a no-op.
	again, created, _ := e.svc.repo.AttachEvent(e.ctx, d3, c.ID, TypeFunding, t0.Add(3*day), &five)
	if created || again != ev1 {
		t.Fatalf("re-attach: %d %v", again, created)
	}
	if n := e.count(`SELECT count(*) FROM signal_event_docs WHERE event_id = ?`, ev1); n != 2 {
		t.Fatalf("documents on the event = %d", n)
	}
	// A different type never merges.
	other, created, _ := e.svc.repo.AttachEvent(e.ctx, d4, c.ID, TypeOther, t0, nil)
	if !created || other == ev1 {
		t.Fatal("a different type merged")
	}
}

func TestEvents_RefetchLeavesEventTimeAndScoresUnchanged(t *testing.T) {
	// run collects the feed, and two days later collects it again or leaves
	// it alone; it returns the event's time and the company's momentum.
	run := func(refetch bool) (string, float64) {
		e := newEnv(t)
		e.enable(SourceAlerts)
		e.alertFeed("https://" + feedHost + "/alerts/feeds/1/2")
		e.web.body(feedHost, "/alerts/feeds", "application/atom+xml", atomFeed)
		e.brain.fn = func(task, prompt string) (string, bool) {
			if task == taskAssess {
				return assessReply(prompt, "will hire 20 engineers", IntentStrong, 0.9, "true", TypeFunding), true
			}
			return `{"company":"Example Corp"}`, true
		}
		e.tick()
		before := e.count(`SELECT count(*) FROM signal_documents`)
		e.clk.advance(48 * time.Hour)
		if !refetch {
			e.disable(SourceAlerts)
		}
		e.tick()
		if e.count(`SELECT count(*) FROM signal_documents`) != before || e.count(`SELECT count(*) FROM signal_events`) != 1 {
			t.Fatal("a re-fetched document created rows")
		}
		var at string
		var momentum float64
		if err := e.st.Read().QueryRowContext(e.ctx, `SELECT e.event_time, t.momentum FROM signal_events e JOIN signal_tiers t ON t.company_id = e.company_id`).Scan(&at, &momentum); err != nil {
			t.Fatal(err)
		}
		return at, momentum
	}
	at, momentum := run(false)
	refetchedAt, refetchedMomentum := run(true)
	if at != "2026-10-10T08:00:00Z" || refetchedAt != at {
		t.Fatalf("event time %s, after a re-fetch %s", at, refetchedAt)
	}
	if momentum == 0 || refetchedMomentum != momentum {
		t.Fatalf("momentum %v, after a re-fetch %v", momentum, refetchedMomentum)
	}
}

func TestGeoFilter_OutsideOnlyDroppedInsideOrNoneKept(t *testing.T) {
	g := NewGeo([]string{"San Francisco, CA", "Remote"}, []string{"US"})
	cases := []struct {
		header string
		keep   bool
	}{
		{"Example GmbH | Berlin | EUR 90k", false},
		{"Example | London, UK | GBP 80k", false},
		{"Example | Remote", true},
		{"Example | San Francisco or Berlin", true},
		{"Example | Berlin or New York", true},
		{"Example | hiring engineers", true},
	}
	for _, c := range cases {
		if got := g.Keep(c.header); got != c.keep {
			t.Errorf("Keep(%q) = %v", c.header, got)
		}
	}
	if !NewGeo(nil, nil).Keep("Berlin | EUR") {
		t.Error("the filter is on without regions")
	}
	// End to end: counted and no company.
	e := newEnv(t)
	e.sets.Profile.Targets.Locations = []string{"New York"}
	e.sets.Profile.WorkAuthorization.AuthorizedCountries = []string{"US"}
	e.doc(Doc{SourceID: SourceHNHiring, Adapter: adapterHNHiring, URL: "https://news.ycombinator.com/item?id=1", PublishedAt: e.clk.now(),
		Title: "Example GmbH | Berlin | EUR 90k", Body: "Example GmbH | Berlin | EUR 90k\nWe hire."})
	sum := e.tick()
	if sum.GeoFiltered != 1 || e.count(`SELECT count(*) FROM signal_companies`) != 0 {
		t.Fatalf("geo-filtered %d", sum.GeoFiltered)
	}
}

func TestHNHiring_CompanyCleanedParentheticalAndLocationOnlyRejected(t *testing.T) {
	if got, ok := HiringTitleCompany("Example Corp (Series B) | Onsite | Exampleton"); !ok || got != "Example Corp" {
		t.Fatalf("got %q %v", got, ok)
	}
	for _, title := range []string{"Remote | Example Corp", "Hybrid (US) | Example", "Berlin, Germany | Example", "We are hiring engineers"} {
		if got, ok := HiringTitleCompany(title); ok {
			t.Errorf("%q named %q", title, got)
		}
	}
	e := newEnv(t)
	e.doc(Doc{SourceID: SourceHNHiring, Adapter: adapterHNHiring, URL: "https://news.ycombinator.com/item?id=2", PublishedAt: e.clk.now(),
		Title: "Example Corp (Series B) | Onsite | Exampleton", Body: "Example Corp (Series B) | Onsite | Exampleton\nHiring."})
	e.tick()
	if n := e.brain.count("feed-classify"); n != 0 {
		t.Fatalf("a rule-first company cost %d model calls", n)
	}
	if n := e.count(`SELECT count(*) FROM signal_companies WHERE canon = 'example'`); n != 1 {
		t.Fatalf("companies = %d", n)
	}
	// A location-first comment falls through to the model.
	e.brain.fn = func(task, prompt string) (string, bool) { return `{"company":"Other Corp"}`, true }
	e.doc(Doc{SourceID: SourceHNHiring, Adapter: adapterHNHiring, URL: "https://news.ycombinator.com/item?id=3", PublishedAt: e.clk.now(),
		Title: "Remote | Other Corp", Body: "Remote | Other Corp\nHiring."})
	e.tick()
	if e.brain.count("feed-classify") != 1 || e.count(`SELECT count(*) FROM signal_companies WHERE canon = 'other'`) != 1 {
		t.Fatalf("generic path calls = %d", e.brain.count("feed-classify"))
	}
}

func TestSignals_Ingest_CrossSourceDuplicateURLCollapses(t *testing.T) {
	e := newEnv(t)
	a, created, _ := e.svc.repo.InsertDocument(e.ctx, Doc{SourceID: SourceAlerts, Adapter: adapterAlerts, URL: "https://WWW.Example.test/news/a/", FetchedAt: e.clk.now(), Title: "a", Body: "b"})
	b, createdB, _ := e.svc.repo.InsertDocument(e.ctx, Doc{SourceID: SourceWeb, Adapter: adapterWeb, URL: "https://example.test/news/a", FetchedAt: e.clk.now(), Title: "a", Body: "b"})
	if !created || createdB || a != b {
		t.Fatalf("ids %d %d created %v %v", a, b, created, createdB)
	}
	for in, want := range map[string]string{"HTTP://Example.TEST:80/x/?q=1#frag": "http://example.test/x?q=1", "https://example.test:443/": "https://example.test", "https://www.example.test/a/b/": "https://example.test/a/b"} {
		if got, _ := CanonicalURL(in); got != want {
			t.Errorf("CanonicalURL(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestEvents_OneDocumentManyCompaniesEachGetsEvent(t *testing.T) {
	e := newEnv(t)
	d := e.doc(Doc{URL: "https://a.example.test/roundup", Title: "t", Body: "b"})
	var events []int64
	for _, n := range []string{"Alpha", "Beta", "Gamma"} {
		c, _ := e.svc.repo.UpsertCompany(e.ctx, n, RuleCanon(n))
		id, created, err := e.svc.repo.AttachEvent(e.ctx, d, c.ID, TypeOther, epoch, nil)
		if err != nil || !created {
			t.Fatalf("%s: event %d created=%v err=%v", n, id, created, err)
		}
		events = append(events, id)
	}
	if events[0] == events[1] || events[1] == events[2] {
		t.Fatalf("events = %v", events)
	}
	if n := e.count(`SELECT count(*) FROM signal_event_docs WHERE document_id = ?`, d); n != 3 {
		t.Fatalf("links = %d", n)
	}
}

// A full pass of documents that always fail does not keep the documents after
// them waiting: the next pass starts where this one stopped and comes back to
// the failures after the rest.
func TestIdentify_DocumentsThatAlwaysFailDoNotStarveTheOnesAfterThem(t *testing.T) {
	e := newEnv(t)
	for i := range maxIdentifyPerTick {
		e.doc(Doc{URL: fmt.Sprintf("https://news.example.test/broken/%d", i), Title: fmt.Sprintf("Broken %d", i), Body: "b"})
	}
	good := e.doc(Doc{URL: "https://news.example.test/good", Title: "Good Corp raises $5M", Body: "Good Corp raised $5M."})
	e.brain.fn = func(task, prompt string) (string, bool) { return `{"company":"Good Corp"}`, true }
	e.svc.model = flakyModel{inner: e.svc.model, fail: "Broken"}
	geo := NewGeo(nil, nil)
	st, err := e.svc.identifyPending(e.ctx, geo)
	if err != nil || st.Identified != 0 || st.Pending != maxIdentifyPerTick {
		t.Fatalf("first pass = %+v, %v", st, err)
	}
	if st, err = e.svc.identifyPending(e.ctx, geo); err != nil {
		t.Fatal(err)
	}
	if e.count(`SELECT count(*) FROM signal_documents WHERE id = ? AND identified_at IS NOT NULL`, good) != 1 {
		t.Fatalf("the document after %d failing ones was not reached by the second pass: %+v", maxIdentifyPerTick, st)
	}
	// The pass went on to the failures again once nothing newer was left.
	if st.Identified != 1 || st.Pending != maxIdentifyPerTick-1 {
		t.Fatalf("second pass = %+v", st)
	}
}
