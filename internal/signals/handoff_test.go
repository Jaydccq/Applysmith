package signals

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"applysmith/internal/discovery"
)

func (e *env) readyCompany(name, slug string, jobs ...ghJob) Company {
	e.profile()
	c := e.hot(name)
	e.identify(c, discovery.Greenhouse, slug)
	e.greenhouse(slug, jobs...)
	return c
}

func TestHandoff_PostingInsertedOnceMarkedHandedOff(t *testing.T) {
	e := newEnv(t)
	c := e.readyCompany("Example Corp", "examplecorp", ghJob{1, "Software Engineer", "Remote - US", "Build."})
	first := e.tick()
	if first.Handoff.Inserted != 1 {
		t.Fatalf("handoff = %+v", first.Handoff)
	}
	e.clk.advance(25 * time.Hour)
	second := e.tick()
	if second.Check.Scanned != 1 || second.Handoff.Inserted != 0 {
		t.Fatalf("second = %+v %+v", second.Check, second.Handoff)
	}
	if n := e.count(`SELECT count(*) FROM inbox WHERE source = 'signals'`); n != 1 {
		t.Fatalf("inbox rows = %d", n)
	}
	if n := len(e.leads.byCategory(LeadPosting)); n != 1 {
		t.Fatalf("posting leads = %d", n)
	}
	var detail, company string
	e.st.Read().QueryRowContext(e.ctx, `SELECT detail, company FROM inbox WHERE source = 'signals'`).Scan(&detail, &company)
	if !strings.Contains(detail, "Example Corp") || !strings.Contains(detail, "https://techcrunch.com/2026/10/example") || company != "Example Corp" {
		t.Fatalf("detail %q company %q", detail, company)
	}
	posts, _ := e.svc.repo.Postings(e.ctx, c.ID, true)
	if len(posts) != 1 || posts[0].HandedOff.IsZero() || posts[0].InboxID == 0 || posts[0].Handoff != "inbox" {
		t.Fatalf("posting = %+v", posts)
	}
	// The user's evaluation path: at most 10 per call, deep mode.
	queued, err := e.svc.EvaluatePostings(e.ctx, c.ID)
	if err != nil || len(queued) != 1 {
		t.Fatalf("queued %v, %v", queued, err)
	}
	if len(e.enq.reqs) != 1 || e.enq.reqs[0].Mode != "deep" || e.enq.reqs[0].Source != "signals" {
		t.Fatalf("evaluation requests = %+v", e.enq.reqs)
	}
	if again, _ := e.svc.EvaluatePostings(e.ctx, c.ID); len(again) != 0 {
		t.Fatalf("a queued row was queued again: %v", again)
	}
}

func TestHandoff_BlockedAndDuplicatePostingsAreNotInserted(t *testing.T) {
	e := newEnv(t)
	e.readyCompany("Example Corp", "examplecorp", ghJob{1, "Software Engineer", "Remote - US", ""})
	if _, err := e.disc.BlockCompany(e.ctx, "Example Corp", "test", "user"); err != nil {
		t.Fatal(err)
	}
	sum := e.tick()
	if sum.Handoff.Blocked != 1 || sum.Handoff.Inserted != 0 || e.count(`SELECT count(*) FROM inbox`) != 0 || len(e.leads.leads) != 0 {
		t.Fatalf("blocked handoff = %+v", sum.Handoff)
	}
	e2 := newEnv(t)
	e2.readyCompany("Example Corp", "examplecorp", ghJob{1, "Software Engineer", "Remote - US", ""})
	if _, err := e2.disc.InboxAddURL(e2.ctx, "https://boards.greenhouse.io/examplecorp/jobs/1"); err != nil {
		t.Fatal(err)
	}
	if sum := e2.tick(); sum.Handoff.Duplicates != 1 || e2.count(`SELECT count(*) FROM inbox`) != 1 {
		t.Fatalf("duplicate handoff = %+v", sum.Handoff)
	}
}

func TestHandoff_LeadEvidenceAlwaysWrittenOrReasonGiven(t *testing.T) {
	e := newEnv(t)
	e.profile()
	c, _, _ := e.company("Example Corp", strings.Repeat("evidence ", 1000), epoch.Add(-24*time.Hour), adapterAlerts)
	ev, err := e.svc.evidenceOf(e.ctx, c.ID)
	if err != nil {
		t.Fatal(err)
	}
	if ev.URL == "" || ev.Title == "" || ev.Adapter != adapterAlerts || ev.Date.IsZero() || len([]rune(ev.Body)) != 4000 || ev.AbsentReason != "" {
		t.Fatalf("evidence = %+v", ev)
	}
	lone, _ := e.svc.repo.UpsertCompany(e.ctx, "Lone Co", "lone")
	ev, _ = e.svc.evidenceOf(e.ctx, lone.ID)
	if ev.AbsentReason == "" || ev.Body != "" {
		t.Fatalf("absent evidence = %+v", ev)
	}
	e.exec(`UPDATE signal_documents SET body_dropped = 1, body = ''`)
	if ev, _ := e.svc.evidenceOf(e.ctx, c.ID); ev.AbsentReason == "" || ev.URL == "" {
		t.Fatalf("dropped body = %+v", ev)
	}
}

func TestHandoff_NoATSCompanyOneReviewLeadNoApplyLink(t *testing.T) {
	e := newEnv(t)
	e.profile()
	e.hot("Example Corp") // no domain, no board anywhere: every probe answers 404
	first := e.tick()
	if first.Check.NoATS != 1 {
		t.Fatalf("scan = %+v", first.Check)
	}
	e.clk.advance(25 * time.Hour)
	e.tick()
	leads := e.leads.byCategory(LeadNoATS)
	if len(leads) != 1 || leads[0].ApplyLink != "" || leads[0].Evidence.URL != "https://techcrunch.com/2026/10/example" || leads[0].Company != "Example Corp" {
		t.Fatalf("leads = %+v", e.leads.leads)
	}
	if e.count(`SELECT count(*) FROM inbox`) != 0 {
		t.Fatal("a no-ats company produced an inbox row")
	}
}

func TestHandoff_LeadFailureRetriesWithoutLosingThePosting(t *testing.T) {
	e := newEnv(t)
	e.readyCompany("Example Corp", "examplecorp", ghJob{1, "Software Engineer", "Remote - US", ""})
	e.leads.err = errors.New("leads store busy")
	sum := e.tick()
	if sum.Handoff.Failed != 1 {
		t.Fatalf("handoff = %+v", sum.Handoff)
	}
	e.leads.err = nil
	e.clk.advance(25 * time.Hour)
	sum = e.tick()
	if len(e.leads.byCategory(LeadPosting)) != 1 || e.count(`SELECT count(*) FROM inbox WHERE source = 'signals'`) != 1 {
		t.Fatalf("after the retry: leads %d, handoff %+v", len(e.leads.leads), sum.Handoff)
	}
}

func TestEvaluatePostings_AtMostTenPerCall(t *testing.T) {
	e := newEnv(t)
	var jobs []ghJob
	for i := 1; i <= 14; i++ {
		jobs = append(jobs, ghJob{i, fmt.Sprintf("Software Engineer %d", i), "Remote - US", ""})
	}
	c := e.readyCompany("Example Corp", "examplecorp", jobs...)
	e.tick()
	queued, err := e.svc.EvaluatePostings(e.ctx, c.ID)
	if err != nil || len(queued) != 10 {
		t.Fatalf("queued %d, %v", len(queued), err)
	}
	if rest, _ := e.svc.EvaluatePostings(e.ctx, c.ID); len(rest) != 4 {
		t.Fatalf("second call queued %d", len(rest))
	}
}

func TestHandoff_WritesLeadsToTheDiscoveryStoreAndItsDedupApplies(t *testing.T) {
	e := newEnv(t)
	e.svc.d.Leads = DiscoveryLeads{Repo: e.disc}
	c := e.readyCompany("Example Corp", "examplecorp", ghJob{1, "Software Engineer", "Remote - US", "Build."})
	e.tick()
	leads, err := e.disc.LeadList(e.ctx, discovery.LeadFilter{Source: "signals"})
	if err != nil || len(leads) != 1 {
		t.Fatalf("leads = %v, %v", leads, err)
	}
	l := leads[0]
	if l.Category != discovery.LeadPosting || l.Company != "Example Corp" || len(l.Roles) != 1 || l.Roles[0] != "Software Engineer" || l.InboxID == 0 ||
		len(l.ApplyLinks) != 1 || l.Evidence.URL != "https://techcrunch.com/2026/10/example" || l.Evidence.Text == "" || l.Evidence.Adapter != adapterAlerts {
		t.Fatalf("lead = %+v", l)
	}
	// The same posting handed over again adds no second lead.
	if err := e.svc.d.Leads.AddLead(e.ctx, Lead{Source: "signals", Category: LeadPosting, Company: "Example Corp", Role: "Software Engineer", ApplyLink: l.ApplyLinks[0],
		Evidence: Evidence{URL: l.Evidence.URL, Body: "x"}}); err != nil {
		t.Fatalf("a duplicate lead is an error: %v", err)
	}
	if again, _ := e.disc.LeadList(e.ctx, discovery.LeadFilter{Source: "signals"}); len(again) != 1 {
		t.Fatalf("leads after the duplicate = %d", len(again))
	}

	// A review lead is written once per company and evidence link.
	e2 := newEnv(t)
	e2.svc.d.Leads = DiscoveryLeads{Repo: e2.disc}
	e2.profile()
	e2.hot("Beta Corp")
	e2.tick()
	e2.clk.advance(25 * time.Hour)
	e2.tick()
	rows, _ := e2.disc.LeadList(e2.ctx, discovery.LeadFilter{Category: discovery.LeadNoATS})
	if len(rows) != 1 || rows[0].Company != "Beta Corp" || len(rows[0].ApplyLinks) != 0 || rows[0].Evidence.URL == "" {
		t.Fatalf("review leads = %+v", rows)
	}
	_ = c
}

func TestHandoff_EmployerPostingURLReachesScheduledLeadEvaluation(t *testing.T) {
	e := newEnv(t)
	e.svc.d.Leads = DiscoveryLeads{Repo: e.disc}
	e.readyCompany("Example Corp", "examplecorp", ghJob{1, "Software Engineer", "Remote - US", "Build."})
	const link = "https://careers.example.org/positions/1001"
	e.web.body("boards-api.greenhouse.io", "/v1/boards/examplecorp/jobs", "application/json", `{"jobs":[{"id":1001,"title":"Software Engineer","absolute_url":"`+link+`","location":{"name":"Remote - US"},"company_name":"Example Corp","content":"Build services. Responsibilities include shipping."}]}`)
	if got := e.tick(); got.Handoff.Inserted != 1 {
		t.Fatalf("handoff = %+v", got.Handoff)
	}
	leads, err := e.disc.LeadList(e.ctx, discovery.LeadFilter{Source: "signals"})
	if err != nil || len(leads) != 1 || len(leads[0].ApplyLinks) != 1 || leads[0].ApplyLinks[0] != link || leads[0].InboxID == 0 {
		t.Fatalf("leads = %+v, %v", leads, err)
	}
	client := discovery.NewClient()
	client.HTTP = e.web.client()
	scanner := discovery.Scanner{Repo: e.disc, Client: client, Enqueuer: e.enq}
	res, err := scanner.EvaluateLeads(e.ctx, discovery.EvaluateRequest{Portals: e.sets.Portals, Profile: e.sets.Profile})
	if err != nil || res.Enqueued != 1 || res.Unknown != 0 || len(e.enq.reqs) != 1 || e.enq.reqs[0].InboxID != leads[0].InboxID || e.enq.reqs[0].URL != link || e.enq.reqs[0].Mode != "deep" {
		t.Fatalf("evaluation = %+v, %v; requests %+v", res, err, e.enq.reqs)
	}
	if again, err := scanner.EvaluateLeads(e.ctx, discovery.EvaluateRequest{Portals: e.sets.Portals}); err != nil || again.Enqueued != 0 {
		t.Fatalf("repeat = %+v, %v", again, err)
	}
}

type interruptedLeadSink struct{ cancel context.CancelFunc }

func (s interruptedLeadSink) AddLead(ctx context.Context, _ Lead) error { s.cancel(); return ctx.Err() }

func TestHandoff_InterruptedReviewLeadRemainsRetryable(t *testing.T) {
	for _, careers := range []string{"", "https://example.test/careers"} {
		t.Run("careers="+careers, func(t *testing.T) {
			e := newEnv(t)
			e.profile()
			e.disable(SourceHNHiring)
			c := e.hot("Example Corp")
			if err := e.svc.repo.PutIdentity(e.ctx, Identity{CompanyID: c.ID, Status: IdentityNoATS, ResolverVersion: ResolverVersion, CareersURL: careers}); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(e.ctx)
			defer cancel()
			e.svc.d.Leads = interruptedLeadSink{cancel: cancel}
			sum, err := e.svc.Tick(ctx, TickRequest{})
			if !errors.Is(err, context.Canceled) || sum.Status != TickFailed {
				t.Fatalf("cancelled=%+v %v", sum, err)
			}
			if n := e.count(`SELECT count(*) FROM signal_lead_keys`); n != 0 {
				t.Errorf("unfinished lead owns %d completion keys", n)
			}
			e.svc.d.Leads = e.leads
			next := e.tick()
			category := LeadNoATS
			if careers != "" {
				category = LeadCareersPage
			}
			if next.Check.Scanned != 1 || len(e.leads.byCategory(category)) != 1 || e.count(`SELECT count(*) FROM signal_lead_keys`) != 1 {
				t.Fatalf("retry=%+v leads=%v", next, e.leads.leads)
			}
			e.clk.advance(25 * time.Hour)
			e.tick()
			if len(e.leads.byCategory(category)) != 1 {
				t.Fatal("completed review lead repeated")
			}
		})
	}
}

func TestHandoff_LeadCompletionWriteRetriesWithoutDuplicate(t *testing.T) {
	e := newEnv(t)
	c := e.hot("Example Corp")
	id := Identity{CompanyID: c.ID, Status: IdentityNoATS}
	e.svc.d.Leads = DiscoveryLeads{Repo: e.disc}
	e.exec(`CREATE TRIGGER refuse_lead_completion BEFORE INSERT ON signal_lead_keys BEGIN SELECT RAISE(FAIL, 'owned lead completion refusal'); END`)
	if err := e.svc.noATSLead(e.ctx, c, id); err == nil || !strings.Contains(err.Error(), "owned lead completion refusal") {
		t.Fatalf("completion failure=%v", err)
	}
	if e.count(`SELECT count(*) FROM leads`) != 1 || e.count(`SELECT count(*) FROM signal_lead_keys`) != 0 {
		t.Fatal("lead was not durable before completion")
	}
	e.exec(`DROP TRIGGER refuse_lead_completion`)
	for range 2 {
		if err := e.svc.noATSLead(e.ctx, c, id); err != nil {
			t.Fatal(err)
		}
	}
	if e.count(`SELECT count(*) FROM leads`) != 1 || e.count(`SELECT count(*) FROM signal_lead_keys`) != 1 {
		t.Fatal("completion retry duplicated or lost the lead")
	}
}
