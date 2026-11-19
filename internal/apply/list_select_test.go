package apply

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"applysmith/internal/gates"
)

// lsEvidence serves a fixed snapshot.
type lsEvidence struct {
	f   Facts
	err error
}

func (e lsEvidence) Facts(context.Context, time.Time) (Facts, error) { return e.f, e.err }

func lsOpts(mod func(*SelectOptions)) SelectOptions {
	o := SelectOptions{Now: t0, Build: "build-b", Limit: 10}
	if mod != nil {
		mod(&o)
	}
	return o
}

func lsSelect(t *testing.T, f Facts, cands []Candidate, mod func(*SelectOptions)) SelectResult {
	t.Helper()
	res, err := NewSelector(lsEvidence{f: f}).Select(context.Background(), cands, lsOpts(mod))
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func lsCand(url, company, role string) Candidate {
	return Candidate{URL: url, Company: company, Role: role}
}

// lsRun builds a stopped run at the check stage on an answer gap, started at
// the given time; mod edits it.
func lsRun(id, url string, started time.Time, mod func(*RunFact)) RunFact {
	r := RunFact{ID: id, URL: url, Started: started, Finished: started.Add(2 * time.Minute), Kind: OutcomeStopped, Stage: StageCheck,
		Class: ClassAnswerGap, Reason: "a required answer is missing", Build: "build-a"}
	if mod != nil {
		mod(&r)
	}
	return r
}

func skippedBy(res SelectResult, url string) (Skipped, bool) {
	for _, s := range res.Skipped {
		if s.URL == url {
			return s, true
		}
	}
	return Skipped{}, false
}

func ranRows(res SelectResult) []string {
	var out []string
	for _, r := range res.Rows {
		out = append(out, r.URL)
	}
	return out
}

func TestApplyList_MirrorGroups_MergeByJobIdOrExactTitle(t *testing.T) {
	role := "Software Engineer, Core Infrastructure (New Grad)"
	const co = "Example Corp"
	sixMirrors := []Candidate{
		lsCand("https://jobs.smartrecruiters.com/ExampleCorp/744000153279380-core-infrastructure", co, role),
		lsCand("https://careers.example.org/jobs/core-infrastructure", co, role),
		lsCand("https://board.example.net/careeronaut/core-infrastructure-744000153279380", co, role),
		lsCand("https://mirror-a.example.net/job/abc", co, "Software Engineer Core Infrastructure (New Grad)"),
		lsCand("https://mirror-b.example.net/job/xyz", co, role),
		lsCand("https://mirror-c.example.net/job/12", co, role),
	}
	cases := []struct {
		name   string
		rows   []Candidate
		groups []int // mirrors per group, in order
	}{
		{"six mirrors, one exact title and a shared job id", sixMirrors, []int{5}},
		{"a shared nine-digit id merges different titles", []Candidate{
			lsCand("https://jobs.example.org/a/123456789012", co, "Backend Engineer"),
			lsCand("https://board.example.net/x-123456789012", co, "Backend Engineer (Remote)"),
		}, []int{1}},
		{"a Workday requisition merges with its copy suffix", []Candidate{
			lsCand("https://acme.wd5.myworkdayjobs.com/en-US/External/job/Austin/Engineer_R51635", co, "Platform Engineer"),
			lsCand("https://acme.wd5.myworkdayjobs.com/en-US/Univ/job/Austin/Engineer_R51635-1", co, "Platform Engineer I"),
		}, []int{1}},
		{"two different job ids are two postings, titles alike or not", []Candidate{
			lsCand("https://jobs.example.org/a/111111111", co, "Backend Engineer"),
			lsCand("https://board.example.net/b/222222222", co, "Backend Engineer"),
		}, []int{0, 0}},
		{"two listings on one host are two postings", []Candidate{
			lsCand("https://jobs.example.org/a/1", co, "Backend Engineer"),
			lsCand("https://jobs.example.org/a/2", co, "Backend Engineer"),
		}, []int{0, 0}},
		{"a year or level in a title is another job", []Candidate{
			lsCand("https://jobs.example.org/a/1", co, "Software Developer"),
			lsCand("https://board.example.net/b/2", co, "Entry Level Software Developer 2027 - Durham"),
		}, []int{0, 0}},
		{"another company with the same id is another posting", []Candidate{
			lsCand("https://jobs.example.org/a/123456789012", co, "Backend Engineer"),
			lsCand("https://board.example.net/b/123456789012", "Sample Inc", "Backend Engineer"),
		}, []int{0, 0}},
		{"rows without a URL stay alone", []Candidate{lsCand("", co, "Backend Engineer"), lsCand("", co, "Backend Engineer")}, []int{0, 0}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var got []int
			for _, g := range GroupMirrors(c.rows) {
				got = append(got, len(g.Mirrors))
			}
			if !slices.Equal(got, c.groups) {
				t.Fatalf("mirrors per group %v, want %v", got, c.groups)
			}
		})
	}
}

func TestApplyList_MirrorGroups_PreferEmployerURLAndShowOnce(t *testing.T) {
	const co, role = "Northwind", "Data Engineer"
	agg := lsCand("https://jobright.ai/jobs/info/1", co, role)
	other := lsCand("https://board.example.net/job/1", co, role)
	own := lsCand("https://careers.northwind.example.org/jobs/1", co, role)
	ats := lsCand("https://boards.greenhouse.io/northwind/jobs/1", co, role)
	g := GroupMirrors([]Candidate{agg, other, own, ats})
	if len(g) != 1 || g[0].Primary.URL != ats.URL || len(g[0].Mirrors) != 3 {
		t.Fatalf("group %+v: the ATS URL should run it with three mirrors", g)
	}
	if g := GroupMirrors([]Candidate{agg, other, own}); g[0].Primary.URL != own.URL {
		t.Fatalf("without an ATS the employer's own site runs it, got %s", g[0].Primary.URL)
	}
	if g := GroupMirrors([]Candidate{agg, other}); g[0].Primary.URL != other.URL {
		t.Fatalf("an aggregator runs last, got %s", g[0].Primary.URL)
	}
	if g := GroupMirrors([]Candidate{lsCand("https://x.example.net/job/2", co, role), lsCand("https://y.example.net/job/2", co, role)}); g[0].Primary.URL != "https://x.example.net/job/2" {
		t.Fatal("ties keep lane order")
	}

	res := lsSelect(t, Facts{}, []Candidate{agg, other, own, ats}, nil)
	if len(res.Rows) != 1 || res.Rows[0].Mirrors != 3 || res.Rows[0].URL != ats.URL {
		t.Fatalf("rows %+v: the posting is listed once", res.Rows)
	}
	// The count travels with the row; the batch panel prints it beside the role
	// (web/src/pages/runs/BatchPanel.tsx, asserted in RunsApply.test.tsx).

	// A duplicate fact on a mirror's URL skips the whole group.
	f := Facts{Ledger: []LedgerFact{{ID: 1, URL: other.URL, IdentityKey: postingKey(other.URL), State: gates.StateSubmitted, Confirmed: true, At: t0}}}
	res = lsSelect(t, f, []Candidate{agg, other, own, ats}, nil)
	if len(res.Rows) != 0 || len(res.Skipped) != 1 || res.Skipped[0].Rule != RuleApplied {
		t.Fatalf("a submission on any URL of the group skips it: %+v", res)
	}
}

func TestApplyList_Skip_AppliedTrackerURLAndLedgerURL(t *testing.T) {
	const co, role = "Example Corp", "Data Engineer"
	url := "https://boards.greenhouse.io/examplecorp/jobs/777"
	tracked := func(status string) Facts {
		return Facts{Tracker: []TrackerFact{{ID: 5, Company: "Other Co", Role: "Other Role", Status: status, ReportURL: url + "?utm_source=feed"}}}
	}
	cases := []struct {
		name string
		f    Facts
		skip bool
	}{
		{"a ledger row on the URL", Facts{Ledger: []LedgerFact{{ID: 1, URL: url, IdentityKey: postingKey(url), State: gates.StateSubmitted, At: t0}}}, true},
		{"a may-have-submitted ledger row", Facts{Ledger: []LedgerFact{{ID: 1, URL: url, IdentityKey: postingKey(url), State: gates.StateMayHaveSubmitted, At: t0}}}, true},
		{"a failed ledger row is no application", Facts{Ledger: []LedgerFact{{ID: 1, URL: url, IdentityKey: postingKey(url), State: gates.StateFailed, At: t0}}}, false},
		{"an applied tracker row whose report carries the URL, tracking parameters aside", tracked("applied"), true},
		{"every status that follows an application counts", tracked("interview"), true},
		{"a decision not to apply does not", tracked("discarded"), false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			res := lsSelect(t, c.f, []Candidate{lsCand(url, co, role)}, nil)
			if skipped := len(res.Skipped) == 1 && res.Skipped[0].Rule == RuleApplied; skipped != c.skip {
				t.Fatalf("skipped=%v, want %v: %+v", skipped, c.skip, res)
			}
		})
	}

	t.Run("naming the company or retrying today does not lift a duplicate", func(t *testing.T) {
		res := lsSelect(t, cases[0].f, []Candidate{lsCand(url, co, role)}, func(o *SelectOptions) {
			o.RetryToday, o.Named, o.NamedCompanies = true, true, []string{co}
		})
		if len(res.Skipped) != 1 || res.Skipped[0].Rule != RuleApplied || len(res.Rows) != 0 {
			t.Fatalf("%+v", res)
		}
	})

	t.Run("unreadable evidence blocks a submitting list and only adds skips otherwise", func(t *testing.T) {
		f := Facts{Unreadable: []string{"tracker"}}
		_, err := NewSelector(lsEvidence{f: f}).Select(context.Background(), []Candidate{lsCand(url, co, role)}, lsOpts(func(o *SelectOptions) { o.Submit = true }))
		if !errors.Is(err, ErrEvidenceUnreadable) || !strings.Contains(err.Error(), "tracker") {
			t.Fatalf("err = %v", err)
		}
		if _, err := NewSelector(lsEvidence{f: f}).Check(context.Background(), []Candidate{lsCand(url, co, role)}, lsOpts(func(o *SelectOptions) { o.Submit = true })); !errors.Is(err, ErrEvidenceUnreadable) {
			t.Fatalf("Check err = %v", err)
		}
		res := lsSelect(t, f, []Candidate{lsCand(url, co, role)}, nil)
		if len(res.Rows) != 1 {
			t.Fatalf("a dry list proceeds with what it can read: %+v", res)
		}
	})
}

func TestApplyList_Skip_LiveSubmittingRowSkippedUpFront(t *testing.T) {
	const co, role = "Example Corp", "Data Engineer"
	url := "https://boards.greenhouse.io/examplecorp/jobs/778"
	ledger := func(state gates.LedgerState, at time.Time) Facts {
		return Facts{Ledger: []LedgerFact{{ID: 1, URL: url, IdentityKey: postingKey(url), State: state, At: at}}}
	}
	for _, c := range []struct {
		name string
		f    Facts
		skip bool
	}{
		{"a run pressing the posting now", ledger(gates.StateSubmitting, t0.Add(-time.Minute)), true},
		{"a press the process never settled", ledger(gates.StateSubmitting, t0.Add(-time.Hour)), true},
		{"a failed row is no application", ledger(gates.StateFailed, t0.Add(-time.Minute)), false},
	} {
		t.Run(c.name, func(t *testing.T) {
			res := lsSelect(t, c.f, []Candidate{lsCand(url, co, role)}, nil)
			if skipped := len(res.Skipped) == 1 && len(res.Rows) == 0; skipped != c.skip {
				t.Fatalf("skipped=%v, want %v: %+v", skipped, c.skip, res)
			}
		})
	}
	// A list that submits is refused the row too, whatever the user named.
	res := lsSelect(t, ledger(gates.StateSubmitting, t0.Add(-time.Minute)), []Candidate{lsCand(url, co, role)}, func(o *SelectOptions) {
		o.Submit, o.Named, o.NamedCompanies, o.RetryToday = true, true, []string{co}, true
	})
	if len(res.Rows) != 0 || len(res.Skipped) != 1 {
		t.Fatalf("%+v", res)
	}
}

func TestApplyList_Skip_CompanyRoleMatchesAppliedOrSubmittedTrackerRow(t *testing.T) {
	const co, role = "Example Corp", "Junior AI Engineer"
	cand := lsCand("https://careers.example.org/jobs/9", co, role)
	cases := []struct {
		name string
		f    Facts
		cand Candidate
		skip bool
	}{
		{"an applied row under another URL and a longer company name", Facts{Tracker: []TrackerFact{{ID: 2631, Company: "Example Corp UK", Role: role, Status: "applied"}}}, cand, true},
		{"a row the chain just submitted but still reads evaluated", Facts{
			Tracker: []TrackerFact{{ID: 8934, Company: co, Role: role, Status: "evaluated", Report: 88}},
			Ledger:  []LedgerFact{{ID: 1, URL: "https://other.example.net/p/1", Report: 88, State: gates.StateSubmitted, At: t0}},
		}, cand, true},
		{"an evaluated row nothing submitted is no evidence", Facts{Tracker: []TrackerFact{{ID: 3, Company: co, Role: role, Status: "evaluated", Report: 88}}}, cand, false},
		{"a different role at the same company is still offered", Facts{Tracker: []TrackerFact{{ID: 4, Company: co, Role: "Staff Platform Engineer", Status: "applied"}}}, cand, false},
		{"a company that only shares a prefix is another company", Facts{Tracker: []TrackerFact{{ID: 5, Company: "Rocket", Role: role, Status: "applied"}}}, lsCand("https://careers.example.org/jobs/10", "NewRocket", role), false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			res := lsSelect(t, c.f, []Candidate{c.cand}, nil)
			if skipped := len(res.Skipped) == 1 && res.Skipped[0].Rule == RuleCompanyRole; skipped != c.skip {
				t.Fatalf("skipped=%v, want %v: %+v", skipped, c.skip, res)
			}
		})
	}
}

func TestApplyList_Skip_LedgerCompanyRoleViaReportTitle(t *testing.T) {
	const co, role = "Example Corp", "Data Analyst"
	// The chain stored only the host; the report's title names the application.
	f := Facts{
		Ledger:  []LedgerFact{{ID: 9196, URL: "https://jobs.smartrecruiters.com/p/1", Company: "jobs.smartrecruiters.com", Report: 8088, State: gates.StateSubmitted, At: t0.Add(-9 * time.Hour)}},
		Reports: []ReportFact{{Number: 8088, Company: co, Role: role, URL: "https://jobs.smartrecruiters.com/p/1"}},
	}
	res := lsSelect(t, f, []Candidate{lsCand("https://careers.example.org/new-link", co, role)}, nil)
	if len(res.Skipped) != 1 || res.Skipped[0].Rule != RuleCompanyRole || !strings.Contains(res.Skipped[0].Reason, "ledger row 9196") {
		t.Fatalf("%+v", res)
	}
	// The host never stands for the company.
	res = lsSelect(t, f, []Candidate{lsCand("https://careers.example.org/other", "jobs.smartrecruiters.com", "Anything")}, nil)
	if len(res.Rows) != 1 {
		t.Fatalf("a host-only company name matched: %+v", res)
	}
	// A different role at the same company is offered.
	res = lsSelect(t, f, []Candidate{lsCand("https://careers.example.org/x", co, "Data Scientist")}, nil)
	if len(res.Rows) != 1 {
		t.Fatalf("%+v", res)
	}
	// A ledger row with its own company and role needs no report.
	own := Facts{Ledger: []LedgerFact{{ID: 3, URL: "https://x.example.net/1", Company: co, Role: role, State: gates.StateSubmitted, At: t0}}}
	if res := lsSelect(t, own, []Candidate{lsCand("https://careers.example.org/y", co, role)}, nil); len(res.Skipped) != 1 {
		t.Fatalf("%+v", res)
	}
}

func TestApplyList_Skip_SharedLinkAcrossCompaniesEvenUnderRetryToday(t *testing.T) {
	shared := "https://jobs.lever.co/sponsored/ad-1"
	f := Facts{Tracker: []TrackerFact{
		{ID: 1, Company: "Alpha Systems", Role: "Engineer", Status: "evaluated", Report: 1, ReportURL: shared},
		{ID: 2, Company: "Beta Labs", Role: "Engineer", Status: "evaluated", Report: 2, ReportURL: shared + "?ref=x"},
		{ID: 3, Company: "Alpha Systems Inc", Role: "Engineer II", Status: "evaluated", Report: 3, ReportURL: "https://jobs.lever.co/alpha/solo"},
	}}
	cands := []Candidate{lsCand(shared, "Alpha Systems", "Engineer"), lsCand("https://jobs.lever.co/alpha/solo", "Alpha Systems", "Engineer II")}
	res := lsSelect(t, f, cands, func(o *SelectOptions) {
		o.RetryToday, o.Named, o.NamedCompanies = true, false, []string{"Alpha Systems"}
	})
	if s, ok := skippedBy(res, shared); !ok || s.Rule != RuleSharedLink || !strings.Contains(s.Reason, "2 evaluations") {
		t.Fatalf("shared link not skipped: %+v", res)
	}
	if !slices.Equal(ranRows(res), []string{"https://jobs.lever.co/alpha/solo"}) {
		t.Fatalf("one company with two rows is not a shared link: %+v", res.Rows)
	}
}

func TestApplyList_Skip_InterruptedRowHoldsUnderRetryToday(t *testing.T) {
	url := "https://careers.example.org/jobs/31"
	at := t0.Add(-3 * time.Hour)
	f := Facts{Interrupted: []JournalFact{{Key: postingKey(url), StartedAt: at}}}
	c := lsCand(url, "Example Corp", "Data Engineer")
	for _, name := range []string{"plain", "retry today", "named"} {
		res := lsSelect(t, f, []Candidate{c}, func(o *SelectOptions) {
			o.RetryToday = name == "retry today"
			if name == "named" {
				o.NamedCompanies = []string{"Example Corp"}
			}
		})
		if len(res.Skipped) != 1 || res.Skipped[0].Rule != RuleInterrupted || !strings.Contains(res.Skipped[0].Reason, "may have been submitted") {
			t.Fatalf("%s: %+v", name, res)
		}
	}
	// A later run of the URL speaks for it.
	f.Runs = []RunFact{lsRun("r1", url, at.Add(time.Hour), func(r *RunFact) { r.Kind, r.Class = OutcomeFilled, "" })}
	if res := lsSelect(t, f, []Candidate{c}, nil); len(res.Rows) != 1 {
		t.Fatalf("a later run should clear the interrupted hold: %+v", res)
	}
	// A mirror of the posting carries the journal row too.
	mirror := lsCand("https://board.example.net/jobs/31", "Example Corp", "Data Engineer")
	g := Facts{Interrupted: []JournalFact{{Key: postingKey(mirror.URL), StartedAt: at}}}
	if res := lsSelect(t, g, []Candidate{c, mirror}, nil); len(res.Skipped) != 1 || res.Skipped[0].Rule != RuleInterrupted {
		t.Fatalf("%+v", res)
	}
}

// A list closes the tabs it fills, so a fill-only list that fills a row the
// running build filled today shows the user nothing new.
func TestApplyList_Skip_FillOnlyListLeavesARowThisBuildFilledToday(t *testing.T) {
	url := "https://careers.example.org/jobs/42"
	c := lsCand(url, "Example Corp", "Data Engineer")
	filled := []RunFact{lsRun("r1", url, t0.Add(-2*time.Hour), func(r *RunFact) {
		r.Kind, r.Class, r.Stage, r.Reason, r.Build = OutcomeFilled, "", "", "", "build-b"
	})}
	if res := lsSelect(t, Facts{Runs: filled}, []Candidate{c}, nil); holdRule(res) != RuleToday || len(res.Rows) != 0 {
		t.Fatalf("fill-only list: %+v", res)
	}
	saved := Facts{Runs: filled, DataSavedAt: t0.Add(-time.Hour)}
	for name, run := range map[string]struct {
		f   Facts
		mod func(*SelectOptions)
	}{
		"a submitting list":           {Facts{Runs: filled}, func(o *SelectOptions) { o.Submit = true }},
		"retry today":                 {Facts{Runs: filled}, func(o *SelectOptions) { o.RetryToday = true }},
		"the company named":           {Facts{Runs: filled}, func(o *SelectOptions) { o.Named, o.NamedCompanies = true, []string{"Example Corp"} }},
		"the row chosen":              {Facts{Runs: filled}, func(o *SelectOptions) { o.Chosen = true }},
		"the user's data saved since": {saved, nil},
	} {
		if res := lsSelect(t, run.f, []Candidate{c}, run.mod); len(res.Rows) != 1 {
			t.Fatalf("%s: %+v", name, res)
		}
	}
}

func TestApplyList_Skip_LastRunOfDayIsVerdictAndEnvironmentNeverSkips(t *testing.T) {
	url := "https://careers.example.org/jobs/41"
	c := lsCand(url, "Example Corp", "Data Engineer")
	morning, noon := t0.Add(-4*time.Hour), t0.Add(-2*time.Hour)
	cases := []struct {
		name string
		runs []RunFact
		skip string // rule, or empty when the row runs
	}{
		{"submitted today", []RunFact{lsRun("r1", url, noon, func(r *RunFact) { r.Kind, r.Class, r.Stage = OutcomeSubmitted, "", StageConfirm })}, RuleToday},
		{"stopped on an answer gap on the same build", []RunFact{lsRun("r1", url, noon, func(r *RunFact) { r.Build = "build-b" })}, RuleToday},
		{"a later filled dry run clears the earlier stop", []RunFact{
			lsRun("r1", url, morning, func(r *RunFact) { r.Build = "build-b" }),
			lsRun("r2", url, noon, func(r *RunFact) { r.Kind, r.Class, r.Stage, r.Reason = OutcomeFilled, "", "", "" }),
		}, ""},
		{"a filled dry run is not a skip", []RunFact{lsRun("r1", url, noon, func(r *RunFact) { r.Kind, r.Class, r.Stage, r.Reason = OutcomeFilled, "", "", "" })}, ""},
		{"an environment stop is not a skip", []RunFact{lsRun("r1", url, noon, func(r *RunFact) { r.Class, r.Stage, r.Build = ClassEnvironment, StagePreflight, "build-b" })}, ""},
		{"an unavailable site is not a skip", []RunFact{lsRun("r1", url, noon, func(r *RunFact) { r.Class, r.Build = ClassUnavailable, "build-b" })}, ""},
		{"a later stop replaces an earlier filled run", []RunFact{
			lsRun("r1", url, morning, func(r *RunFact) { r.Kind, r.Class, r.Stage, r.Reason = OutcomeFilled, "", "", "" }),
			lsRun("r2", url, noon, func(r *RunFact) { r.Class, r.Build = ClassDefect, "build-b" }),
		}, RuleToday},
		{"yesterday's stop is no verdict of today", []RunFact{lsRun("r1", url, t0.Add(-20*time.Hour), func(r *RunFact) { r.Build = "build-b" })}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res := lsSelect(t, Facts{Runs: tc.runs}, []Candidate{c}, nil)
			var got string
			if len(res.Skipped) == 1 {
				got = res.Skipped[0].Rule
			}
			if got != tc.skip {
				t.Fatalf("skipped by %q, want %q: %+v", got, tc.skip, res)
			}
		})
	}
	t.Run("retry today lifts a stop but never a submit", func(t *testing.T) {
		stop := []RunFact{lsRun("r1", url, noon, func(r *RunFact) { r.Build = "build-b" })}
		if res := lsSelect(t, Facts{Runs: stop}, []Candidate{c}, func(o *SelectOptions) { o.RetryToday = true }); len(res.Rows) != 1 {
			t.Fatalf("%+v", res)
		}
		sub := []RunFact{lsRun("r1", url, noon, func(r *RunFact) { r.Kind, r.Class, r.Stage = OutcomeSubmitted, "", StageConfirm })}
		if res := lsSelect(t, Facts{Runs: sub}, []Candidate{c}, func(o *SelectOptions) { o.RetryToday = true; o.NamedCompanies = []string{"Example Corp"} }); len(res.Skipped) != 1 {
			t.Fatalf("%+v", res)
		}
	})
	t.Run("a credential stop yields only to retry today and the user naming the company", func(t *testing.T) {
		cred := []RunFact{lsRun("r1", url, noon, func(r *RunFact) { r.Class, r.Stage, r.Build = ClassCredential, StageAccount, "build-b" })}
		retry := func(o *SelectOptions) { o.RetryToday = true }
		if res := lsSelect(t, Facts{Runs: cred}, []Candidate{c}, retry); len(res.Skipped) != 1 || !strings.Contains(res.Skipped[0].Reason, "name the company") {
			t.Fatalf("%+v", res)
		}
		res := lsSelect(t, Facts{Runs: cred}, []Candidate{c}, func(o *SelectOptions) { retry(o); o.NamedCompanies = []string{"example corp"} })
		if len(res.Rows) != 1 || !res.Rows[0].Named {
			t.Fatalf("%+v", res)
		}
	})
}

func TestApplyList_Skip_SameDayJudgementStopRetriesOnNewBuild(t *testing.T) {
	url := "https://careers.example.org/jobs/51"
	c := lsCand(url, "Example Corp", "Data Engineer")
	at := t0.Add(-2 * time.Hour)
	judgements := map[string]func(*RunFact){
		"answer gap": func(r *RunFact) {},
		"defect":     func(r *RunFact) { r.Class, r.Stage = ClassDefect, StageFill },
		"no form found (a judgement)": func(r *RunFact) {
			r.Class, r.Stage, r.Reason = ClassPosting, StageAccount, "the walk found no form or Submit control"
		},
	}
	for name, mod := range judgements {
		t.Run(name+" is attempted again on new code", func(t *testing.T) {
			f := Facts{Runs: []RunFact{lsRun("r1", url, at, func(r *RunFact) { mod(r); r.Build = "build-a" })}}
			if res := lsSelect(t, f, []Candidate{c}, nil); len(res.Rows) != 1 {
				t.Fatalf("new clean build should retry: %+v", res)
			}
			f = Facts{Runs: []RunFact{lsRun("r1", url, at, func(r *RunFact) { mod(r); r.Build = "build-b" })}}
			if res := lsSelect(t, f, []Candidate{c}, nil); len(res.Skipped) != 1 {
				t.Fatalf("the same clean build keeps the stop: %+v", res)
			}
			f = Facts{Runs: []RunFact{lsRun("r1", url, at, func(r *RunFact) { mod(r); r.Build = "build-b-dirty" })}}
			if res := lsSelect(t, f, []Candidate{c}, func(o *SelectOptions) { o.Build = "build-b-dirty" }); len(res.Rows) != 1 {
				t.Fatalf("a modified build never vouches for a stop: %+v", res)
			}
		})
	}
	t.Run("a fact about the posting, a challenge and a credential stop still hold on new code", func(t *testing.T) {
		for name, mod := range map[string]func(*RunFact){
			"closed": func(r *RunFact) { r.Class, r.Reason = ClassPosting, "posting closed: HTTP 404" },
			"challenge": func(r *RunFact) {
				r.Class, r.Reason = ClassDefect, "bot check: a challenge stands where the page should be"
			},
			"credential": func(r *RunFact) { r.Class, r.Reason = ClassCredential, "every stored password was refused" },
		} {
			f := Facts{Runs: []RunFact{lsRun("r1", url, at, func(r *RunFact) { mod(r); r.Build = "build-a" })}}
			if res := lsSelect(t, f, []Candidate{c}, nil); len(res.Skipped) != 1 {
				t.Fatalf("%s: %+v", name, res)
			}
		}
	})
}

func TestApplyList_StaleLinkRoleMismatchSkipButPunctuationVariantRuns(t *testing.T) {
	url := "https://careers.example.org/jobs/61"
	report := func(role string) Facts {
		return Facts{Reports: []ReportFact{{Number: 10, Company: "Example Corp", Role: role, URL: url}}}
	}
	cases := []struct {
		name, reportRole, rowRole string
		stale                     bool
	}{
		{"a different role", "Adobe 2026 University Graduate - Software Development Engineer", "Adobe 2027 University Graduate - AI Forward Deployed Engineer", true},
		{"punctuation only", "AI Engineer | EMEA", "AI Engineer - EMEA", false},
		{"a level suffix on the row", "Software Engineer", "Software Engineer I", false},
		{"a level suffix on the report", "Software Engineer II", "Software Engineer", false},
		{"the same role", "Data Engineer", "Data Engineer", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			res := lsSelect(t, report(c.reportRole), []Candidate{lsCand(url, "Example Corp", c.rowRole)}, nil)
			stale := len(res.Skipped) == 1 && res.Skipped[0].Rule == RuleStaleLink
			if stale != c.stale {
				t.Fatalf("stale=%v, want %v: %+v", stale, c.stale, res)
			}
			if c.stale && !strings.Contains(res.Skipped[0].Reason, "tracker link is stale") {
				t.Fatalf("reason %q", res.Skipped[0].Reason)
			}
		})
	}
	if res := lsSelect(t, Facts{}, []Candidate{lsCand(url, "Example Corp", "Data Engineer")}, nil); len(res.Rows) != 1 {
		t.Fatalf("a row with no evaluated report runs: %+v", res)
	}
}

func TestApplyList_Skip_SkipListHoldsWhenNamedAndDuplicateIsReportedFirst(t *testing.T) {
	url := "https://careers.example.org/jobs/71"
	c := lsCand(url, "Alphabet", "Data Engineer")
	opts := func(o *SelectOptions) {
		o.SkipList, o.NamedCompanies, o.RetryToday = gates.DefaultSkipList, []string{"Alphabet"}, true
	}
	res := lsSelect(t, Facts{}, []Candidate{c, lsCand("https://careers.example.org/jobs/72", "Example Corp", "Data Engineer")}, opts)
	if s, ok := skippedBy(res, url); !ok || s.Rule != RuleSkipList || s.Owner != "user" || len(res.Rows) != 1 {
		t.Fatalf("a named skip-listed company is still left to the user: %+v", res)
	}
	f := Facts{Ledger: []LedgerFact{{ID: 1, URL: url, IdentityKey: postingKey(url), State: gates.StateSubmitted, At: t0}}}
	if res := lsSelect(t, f, []Candidate{c}, opts); res.Skipped[0].Rule != RuleApplied {
		t.Fatalf("an applied row is reported as applied, not as skip-listed: %+v", res)
	}
}

func TestApplyList_Select_SkippedRowsDoNotCountTowardTheLimit(t *testing.T) {
	var cands []Candidate
	f := Facts{}
	for i := 1; i <= 6; i++ {
		u := "https://careers.example.org/jobs/" + string(rune('0'+i))
		cands = append(cands, lsCand(u, "Company "+string(rune('A'+i)), "Role "+string(rune('A'+i))))
		if i <= 2 {
			f.Ledger = append(f.Ledger, LedgerFact{ID: int64(i), URL: u, IdentityKey: postingKey(u), State: gates.StateSubmitted, At: t0})
		}
	}
	res := lsSelect(t, f, cands, func(o *SelectOptions) { o.Limit = 3 })
	if len(res.Rows) != 3 || len(res.Skipped) != 2 {
		t.Fatalf("rows %d skipped %d: %+v", len(res.Rows), len(res.Skipped), res)
	}
	got, err := NewSelector(lsEvidence{f: f}).Check(context.Background(), cands, lsOpts(nil))
	if err != nil || len(got) != len(cands) || got[0] == nil || got[0].Rule != RuleApplied {
		t.Fatalf("Check = %+v %v", got, err)
	}
	if got[5] != nil {
		t.Fatalf("a clean row is not skipped: %+v", got[5])
	}
}

func TestApplyList_NamedCompaniesOnlyWholeWordCaseInsensitive(t *testing.T) {
	// A name with a non-ASCII letter is matched as a substring.
	e := string(rune(0xe9))
	cases := []struct {
		names   []string
		company string
		want    bool
	}{
		{[]string{"example corp"}, "Example Corp", true},
		{[]string{"EXAMPLE CORP"}, "Example Corp, Inc.", true},
		{[]string{"Meta"}, "Meta Platforms", true},
		{[]string{"Meta"}, "Metalworks Inc", false},
		{[]string{"AI"}, "Rain Labs", false},
		{[]string{"Sample"}, "Resample Co", false},
		{[]string{"apply to Example Corp and Sample Inc please"}, "Sample Inc", true},
		{[]string{"Example Corp", "Sample Inc"}, "Third Co", false},
		{[]string{"Caf"}, "Caf" + e + "s Group", false},
		{[]string{"af" + e}, "Caf" + e + "s Group", true},
		{[]string{""}, "Example Corp", false},
		{nil, "Example Corp", false},
	}
	for _, c := range cases {
		if got := NamedCompany(c.names, c.company); got != c.want {
			t.Errorf("NamedCompany(%q, %q) = %v, want %v", c.names, c.company, got, c.want)
		}
	}

	var cands []Candidate
	for _, co := range []string{"Example Corp", "Sample Inc", "Third Co", "Fourth Co", "Fifth Co", "Sixth Co", "Seventh Co"} {
		cands = append(cands, lsCand("https://careers.example.org/"+strings.ReplaceAll(strings.ToLower(co), " ", "-"), co, "Engineer"))
	}
	res := lsSelect(t, Facts{}, cands, func(o *SelectOptions) {
		o.Named, o.NamedCompanies, o.Limit = true, []string{"example corp", "SAMPLE INC"}, 1
	})
	if len(res.Rows) != 2 || len(res.Skipped) != 0 || !res.Rows[0].Named || !res.Rows[1].Named {
		t.Fatalf("a request naming companies runs exactly those rows, ignoring the limit: %+v", res)
	}
	if res := lsSelect(t, Facts{}, cands, func(o *SelectOptions) { o.Named, o.NamedCompanies = true, []string{"Nowhere Ltd"} }); len(res.Rows) != 0 {
		t.Fatalf("none of them on the list runs nothing, not the top rows: %+v", res)
	}
}
