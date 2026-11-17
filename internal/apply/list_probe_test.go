package apply

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestApplyList_Precheck_ExpiredWrongJobChallengeRecordedUnknownLeftToChain(t *testing.T) {
	const co, role = "Example Corp", "Data Engineer"
	url := func(name string) string { return "https://careers.example.test/jobs/" + name }
	w := newProbeWindow()
	w.pages[url("live")] = probePage{state: listPostingPage(role)}
	w.pages[url("gone")] = probePage{status: 404}
	w.pages[url("closed")] = probePage{state: &PageState{Title: role, Headings: []string{role}, Lines: []string{"Sorry, we are no longer accepting applications."}}}
	w.pages[url("wrong")] = probePage{state: listPostingPage("Warehouse Associate")}
	w.pages[url("wall")] = probePage{state: &PageState{Title: "Just a moment...", Lines: []string{"Checking your browser before accessing the site"}}}
	w.pages[url("down")] = probePage{status: 503}
	w.pages[url("navfail")] = probePage{navErr: errors.New("net::ERR_CONNECTION_RESET")}
	w.pages[url("hang")] = probePage{block: true}
	w.pages[url("unreadable")] = probePage{evalErr: errors.New("the page is gone")}

	names := []string{"live", "gone", "closed", "wrong", "wall", "down", "navfail", "hang", "unreadable"}
	var cands []Candidate
	for _, n := range names {
		cands = append(cands, lsCand(url(n), co+" "+n, role))
	}
	rec := &recorder{}
	sel := NewSelector(lsEvidence{})
	sel.Record = rec
	sel.Probe = LivenessProber{Window: w, Settle: -1, Timeout: 150 * time.Millisecond}
	res, err := sel.Select(context.Background(), cands, lsOpts(func(o *SelectOptions) { o.Limit = 20; o.Build = "build-b" }))
	if err != nil {
		t.Fatal(err)
	}
	// Unknown stays unknown: a timeout, an unreadable page and a site that is
	// down are left for the chain to diagnose.
	wantRan := []string{url("live"), url("down"), url("navfail"), url("hang"), url("unreadable")}
	if got := ranRows(res); strings.Join(got, ",") != strings.Join(wantRan, ",") {
		t.Fatalf("rows %v, want %v", got, wantRan)
	}
	wantRecorded := []string{url("gone"), url("closed"), url("wrong"), url("wall")}
	if got := rec.urls(); strings.Join(got, ",") != strings.Join(wantRecorded, ",") {
		t.Fatalf("precheck records %v, want %v in list order", got, wantRecorded)
	}
	byURL := map[string]Precheck{}
	for _, s := range rec.stops {
		byURL[s.URL] = s
		if s.Build != "build-b" || s.At != t0 {
			t.Fatalf("a precheck stop carries the build and time: %+v", s)
		}
	}
	for _, n := range []string{"gone", "closed"} {
		if p := byURL[url(n)]; p.Class != ClassPosting || !strings.HasPrefix(p.Reason, "liveness expired") || !postingVerdictIsFact(p.Reason) {
			t.Errorf("%s: %+v", n, p)
		}
	}
	if p := byURL[url("wrong")]; p.Class != ClassPosting || !strings.HasPrefix(p.Reason, "liveness mismatch") || postingVerdictIsFact(p.Reason) {
		t.Errorf("wrong job is the runtime's judgement: %+v", p)
	}
	if p := byURL[url("wall")]; p.Class != ClassDefect || !challengeRE.MatchString(p.Reason) {
		t.Errorf("a challenge reads as one: %+v", p)
	}
	if len(res.Skipped) != 4 {
		t.Fatalf("%+v", res.Skipped)
	}
	for _, s := range res.Skipped {
		if s.Rule != RulePrecheck {
			t.Errorf("%+v", s)
		}
	}
	if w.openNow() != 0 {
		t.Fatalf("%d tabs left open", w.openNow())
	}

	t.Run("a failed write is a warning, the row is skipped either way", func(t *testing.T) {
		bad := &recorder{err: errors.New("database is locked")}
		s2 := NewSelector(lsEvidence{})
		s2.Record, s2.Probe = bad, LivenessProber{Window: w, Settle: -1}
		out, err := s2.Select(context.Background(), cands[1:2], lsOpts(nil))
		if err != nil || len(out.Skipped) != 1 || len(out.Warnings) != 1 {
			t.Fatalf("%+v %v", out, err)
		}
	})
}

func TestApplyList_Precheck_StoredStopsHoldTheRowOnTheNextList(t *testing.T) {
	st := lsStore(t)
	const co, role = "Example Corp", "Data Engineer"
	gone, wrong, wall, live := "https://careers.example.test/jobs/gone", "https://careers.example.test/jobs/wrong", "https://careers.example.test/jobs/wall", "https://careers.example.test/jobs/live"
	w := newProbeWindow()
	w.pages[gone] = probePage{status: 410}
	w.pages[wrong] = probePage{state: listPostingPage("Warehouse Associate")}
	w.pages[wall] = probePage{state: &PageState{Title: "Just a moment...", Lines: []string{"Checking your browser before accessing the site"}}}
	w.pages[live] = probePage{state: listPostingPage(role)}
	sel := NewSelector(SQLEvidence{Store: st})
	sel.Record = StorePrecheck{Store: st}
	sel.Probe = LivenessProber{Window: w, Settle: -1}
	cands := []Candidate{lsCand(gone, "Gone Co", role), lsCand(wrong, "Wrong Co", role), lsCand(wall, "Wall Co", role), lsCand(live, "Live Co", role)}
	first, err := sel.Select(context.Background(), cands, lsOpts(nil))
	if err != nil || len(first.Skipped) != 3 || len(first.Rows) != 1 {
		t.Fatalf("%+v %v", first, err)
	}

	// The same build reads the records back as holds. A fact about the posting
	// and a challenge stay held under retry-today; the runtime's own mismatch
	// is a verdict of the day, which retry-today lifts to the hold behind it.
	for _, retry := range []bool{false, true} {
		second, err := sel.Select(context.Background(), cands, lsOpts(func(o *SelectOptions) { o.RetryToday = retry }))
		if err != nil {
			t.Fatal(err)
		}
		for _, u := range []string{gone, wall} {
			if s, ok := skippedBy(second, u); !ok || s.Rule != RuleHold {
				t.Fatalf("retry=%v: %s: %+v", retry, u, second)
			}
		}
		wantWrong := RuleToday
		if retry {
			wantWrong = RuleHold
		}
		if s, ok := skippedBy(second, wrong); !ok || s.Rule != wantWrong {
			t.Fatalf("retry=%v: a mismatch on the same clean build: %+v", retry, second)
		}
	}
	// A new clean build retries the judgement (wrong job) only; the closed
	// posting and the challenge keep holding.
	next, err := sel.Select(context.Background(), cands, lsOpts(func(o *SelectOptions) { o.Build = "build-c" }))
	if err != nil {
		t.Fatal(err)
	}
	if s, ok := skippedBy(next, wrong); !ok || s.Rule != RulePrecheck {
		t.Fatalf("a mismatch is a judgement and expires: the row is probed again: %+v", next)
	}
	for _, u := range []string{gone, wall} {
		if s, ok := skippedBy(next, u); !ok || s.Rule == RulePrecheck {
			t.Fatalf("%s: %+v", u, next)
		}
	}
	// Naming the company runs a held row (here the posting is back, so the probe
	// finds it live).
	w.pages[gone] = probePage{state: listPostingPage(role)}
	named, err := sel.Select(context.Background(), cands[:1], lsOpts(func(o *SelectOptions) { o.NamedCompanies = []string{"Gone Co"} }))
	if err != nil || len(named.Rows) != 1 {
		t.Fatalf("%+v %v", named, err)
	}
	var n int
	if err := st.Read().QueryRow(`SELECT count(*) FROM apply_runs WHERE run_id LIKE 'precheck-%' AND outcome_stage = 'resolve'`).Scan(&n); err != nil || n != 4 {
		t.Fatalf("%d precheck rows (%v): three from the first list and the wrong job again on build-c", n, err)
	}
}

// PostHog (live): Ashby's posting page answers 404 for an employer whose own
// careers page embeds the form, which Ashby still serves; only a posting whose
// form answers 404 too is expired.
func TestApplyList_Precheck_AnAshbyPostingWithItsHostedPageOffIsLive(t *testing.T) {
	const listed, closed = "https://jobs.ashbyhq.com/posthog/20ab9628", "https://jobs.ashbyhq.com/rebar/ead8df0c"
	w := newProbeWindow()
	w.pages[listed] = probePage{status: 404}
	w.pages[listed+"/application?embed=js"] = probePage{state: listPostingPage("Product Engineer")}
	w.pages[closed] = probePage{status: 404}
	w.pages[closed+"/application?embed=js"] = probePage{status: 404}
	got, err := LivenessProber{Window: w, Settle: -1, Timeout: 150 * time.Millisecond}.Probe(context.Background(), []ProbeTarget{
		{URL: listed, Company: "PostHog", Role: "Product Engineer"}, {URL: closed, Company: "Rebar", Role: "Product Engineer"}})
	if err != nil || len(got) != 2 || got[0].Result != ProbeLive || got[1].Result != ProbeExpired {
		t.Fatalf("verdicts %+v %v", got, err)
	}
}
