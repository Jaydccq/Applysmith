package discovery

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"applysmith/internal/workspace"
)

func ldPostingLead(t *testing.T, e *env, company, link string) int64 {
	t.Helper()
	id, err := e.rp.LeadAdd(e.ctx, Lead{Source: "signals", Company: company, Category: LeadPosting, ApplyLinks: []string{link}, Roles: []string{"Backend Engineer"},
		Evidence: Evidence{URL: "https://news.example.com/" + company, Text: "Hiring news for " + company}})
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func ldEvaluate(t *testing.T, e *env, limit int) EvaluateResult {
	t.Helper()
	res, err := e.sc.EvaluateLeads(e.ctx, EvaluateRequest{Portals: portals([]string{"engineer"}, nil), Limit: limit})
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func ghLink(slug string, id int) string {
	return fmt.Sprintf("https://job-boards.greenhouse.io/%s/jobs/%d", slug, id)
}

func TestLeadsEvaluate_RecoversJDAndEmployerFromBoard_ClosedAndUnknownNeverEvaluated(t *testing.T) {
	e := newEnv(t)
	ldBoard(e, "example-co", "Example Co Inc", ldJob(1001, "Backend Engineer"))
	e.srv.set("/v1/boards/down-co/jobs", route{status: 500})
	open := ldPostingLead(t, e, "Headline Fragment", ghLink("example-co", 1001))
	closed := ldPostingLead(t, e, "Gone Corp", ghLink("example-co", 9999))
	unknown := ldPostingLead(t, e, "Down Corp", ghLink("down-co", 5))

	res := ldEvaluate(t, e, 0)
	if res.Enqueued != 1 || res.Closed != 1 || res.Unknown != 1 {
		t.Fatalf("result = %+v", res)
	}
	l, _ := e.rp.LeadGet(e.ctx, open)
	if l.State != LeadEvaluated || l.Company != "Example Co Inc" || !strings.Contains(l.Description, "Build services") || l.InboxID == 0 {
		t.Fatalf("open lead = %+v; want the board's employer name and description and an inbox row", l)
	}
	if len(e.enq.reqs) != 1 || e.enq.reqs[0].Mode != "deep" || e.enq.reqs[0].InboxID != l.InboxID {
		t.Fatalf("enqueued = %+v; want one deep evaluation of the lead's row", e.enq.reqs)
	}
	row, _ := e.rp.InboxGet(e.ctx, l.InboxID)
	if row.Company != "Example Co Inc" || row.Description == "" {
		t.Fatalf("inbox row = %+v", row)
	}
	if c, _ := e.rp.LeadGet(e.ctx, closed); c.State != LeadDismissed || c.Reason != "posting-closed" {
		t.Fatalf("closed lead = %+v", c)
	}
	u, _ := e.rp.LeadGet(e.ctx, unknown)
	if u.State != LeadNew || !strings.Contains(u.Reason, "did not answer") {
		t.Fatalf("unknown lead = %+v; want it new with the reason, retried by a later run", u)
	}
}

func TestLeadsEvaluate_NoDescriptionFromTheBoardIsNeverEvaluatedFromTheTitle(t *testing.T) {
	e := newEnv(t)
	e.srv.json("/v1/boards/bare-co/jobs", ghJobs("bare-co", obj{"id": 7, "title": "Backend Engineer", "location": obj{"name": "Exampleton"}, "content": ""}))
	e.srv.set("/v1/boards/bare-co/jobs/7", route{status: 200, body: `{"id":7,"title":"Backend Engineer","content":""}`})
	id := ldPostingLead(t, e, "Bare Corp", ghLink("bare-co", 7))
	res := ldEvaluate(t, e, 0)
	if res.Enqueued != 0 || res.Unknown != 1 || len(e.enq.reqs) != 0 {
		t.Fatalf("result = %+v, enqueued %v", res, e.enq.reqs)
	}
	if l, _ := e.rp.LeadGet(e.ctx, id); l.State != LeadNew {
		t.Fatalf("state = %s", l.State)
	}
}

// SmartRecruiters lists its postings with no text; the posting's own record
// has it, so a lead on such a board is recovered like any other.
func TestLeadsEvaluate_ASmartRecruitersPostingIsRecoveredFromItsOwnRecord(t *testing.T) {
	e := newEnv(t)
	e.srv.json("/v1/companies/exampleco/postings?offset=0", srPage(1, 0, 1, "ExampleCo"))
	e.srv.json("/v1/companies/exampleco/postings/743900000000", obj{"id": "743900000000", "name": "Software Engineer 0", "active": true,
		"jobAd": obj{"sections": obj{
			"jobDescription": obj{"title": "Job Description", "text": "<p>Build services for Example Sr Co.</p>"},
			"qualifications": obj{"title": "Qualifications", "text": "<ul><li>Two years of Go</li></ul>"},
		}}})
	id := ldPostingLead(t, e, "Example Sr", "https://jobs.smartrecruiters.com/ExampleCo/743900000000")
	res := ldEvaluate(t, e, 0)
	l, _ := e.rp.LeadGet(e.ctx, id)
	if res.Enqueued != 1 || l.State != LeadEvaluated || !strings.Contains(l.Description, "Build services for Example Sr Co.") || !strings.Contains(l.Description, "Two years of Go") {
		t.Fatalf("result = %+v, lead = %+v", res, l)
	}
}

func TestLeadsRecovery_OneRequestPerBoard(t *testing.T) {
	e := newEnv(t)
	ldBoard(e, "big-co", "Big Co", ldJob(1, "Backend Engineer"), ldJob(2, "Data Engineer"), ldJob(3, "Platform Engineer"))
	var leads []Lead
	for i := 1; i <= 140; i++ {
		id := ldPostingLead(t, e, fmt.Sprintf("Fragment %d", i), ghLink("big-co", i))
		l, _ := e.rp.LeadGet(e.ctx, id)
		leads = append(leads, l)
	}
	rc := NewLeadRecoverer(e.sc.Client, e.sc.Endpoints, e.rp)
	got := map[int64]Recovery{}
	for _, l := range leads {
		got[l.ID] = rc.Recover(e.ctx, l)
	}
	open, closed := 0, 0
	for _, r := range got {
		switch r.Verdict {
		case RecoveryOpen:
			open++
		case RecoveryClosed:
			closed++
		}
	}
	if open != 3 || closed != 137 {
		t.Fatalf("open %d closed %d, want 3 and 137", open, closed)
	}
	if n := e.srv.hitCount("/v1/boards/big-co/jobs"); n != 1 || rc.Requests()["greenhouse:big-co"] != 1 {
		t.Fatalf("board listing requested %d times, want once", n)
	}
}

func TestLeadsEvaluate_CapTwentyFiveMarksEvalCap(t *testing.T) {
	e := newEnv(t)
	var jobs []obj
	for i := 1; i <= 30; i++ {
		jobs = append(jobs, ldJob(i, fmt.Sprintf("Backend Engineer %d", i)))
	}
	ldBoard(e, "cap-co", "Cap Co", jobs...)
	var ids []int64
	for i := 1; i <= 30; i++ {
		ids = append(ids, ldPostingLead(t, e, fmt.Sprintf("Fragment %d", i), ghLink("cap-co", i)))
	}
	res := ldEvaluate(t, e, 0)
	if res.Enqueued != 25 || res.Capped != 5 {
		t.Fatalf("result = %+v; want 25 sent and 5 beyond the cap", res)
	}
	capped := 0
	for _, id := range ids {
		if l, _ := e.rp.LeadGet(e.ctx, id); l.Category == LeadEvalCap {
			capped++
		}
	}
	if capped != 5 {
		t.Fatalf("%d leads marked eval-cap, want 5", capped)
	}
	// The next run takes the ones the cap held back.
	res = ldEvaluate(t, e, 0)
	if res.Enqueued != 5 {
		t.Fatalf("second run = %+v; want the 5 held back", res)
	}
}

func TestLeads_BoardIndexNeverEvaluatedAsPosting(t *testing.T) {
	e := newEnv(t)
	id := ldPostingLead(t, e, "Example Corp", "https://jobs.ashbyhq.com/example-corp")
	res := ldEvaluate(t, e, 0)
	if res.BoardIndex != 1 || res.Enqueued != 0 || len(e.enq.reqs) != 0 {
		t.Fatalf("result = %+v", res)
	}
	if l, _ := e.rp.LeadGet(e.ctx, id); l.Category != LeadCareersPage {
		t.Fatalf("category = %s, want careers-page", l.Category)
	}
}

func ldJDBody() string {
	return "Example Corp is hiring a Backend Engineer in Exampleton.\n\nResponsibilities\n- Build and run services.\n- Review code.\n\nRequirements\n- Three years of Go.\n- Comfort with SQL.\n\n" +
		strings.Repeat("We care about careful engineering and clear writing. ", 6)
}

func TestLinkedInFeed_DMLeadWithJDBodyIsEvaluable(t *testing.T) {
	e := newEnv(t)
	jd, err := e.rp.LeadAdd(e.ctx, Lead{Source: "linkedin-feed", Company: "Example Corp", Category: LeadDM, ContactMethod: "dm", Roles: []string{"Backend Engineer"},
		Description: ldJDBody(), Evidence: Evidence{URL: "https://example.com/feed/update/jd", Text: ldJDBody()}})
	if err != nil {
		t.Fatal(err)
	}
	short, _ := e.rp.LeadAdd(e.ctx, Lead{Source: "linkedin-feed", Company: "Short Corp", Category: LeadDM, ContactMethod: "dm", Roles: []string{"Backend Engineer"},
		Description: "Hiring an engineer.\nDM me.", Evidence: Evidence{URL: "https://example.com/feed/update/short", Text: "Hiring an engineer.\nDM me."}})
	res := ldEvaluate(t, e, 0)
	if res.Enqueued != 1 || len(e.enq.reqs) != 1 {
		t.Fatalf("result = %+v", res)
	}
	req := e.enq.reqs[0]
	if req.Mode != "deep" || !strings.Contains(req.Text, "Responsibilities") || req.Company != "Example Corp" {
		t.Fatalf("request = %+v; want deep mode on the lead's own text", req)
	}
	if l, _ := e.rp.LeadGet(e.ctx, jd); l.State != LeadEvaluated {
		t.Fatalf("jd lead state = %s", l.State)
	}
	if l, _ := e.rp.LeadGet(e.ctx, short); l.State != LeadNew {
		t.Fatalf("a two-line lead was taken (%s)", l.State)
	}
}

func TestLeadsEvaluate_RefusedRecoveredPostingIsAuditedAndDismissed(t *testing.T) {
	e := newEnv(t)
	e.sc.Gate = func(row InboxRow) string {
		if strings.Contains(row.Role, "Staff") {
			return "seniority"
		}
		return ""
	}
	ldBoard(e, "staff-co", "Staff Co", ldJob(1, "Staff Engineer"))
	id := ldPostingLead(t, e, "Fragment", ghLink("staff-co", 1))
	res := ldEvaluate(t, e, 0)
	if res.Refused != 1 || len(e.enq.reqs) != 0 {
		t.Fatalf("result = %+v", res)
	}
	if l, _ := e.rp.LeadGet(e.ctx, id); l.State != LeadDismissed || !strings.HasPrefix(l.Reason, "gate:") {
		t.Fatalf("lead = %+v", l)
	}
	if a, _ := e.rp.GateAuditList(e.ctx, IngressEvaluate, 5); len(a) != 1 {
		t.Fatalf("audit = %+v", a)
	}
}

// ---- silent-source alarm ----

func ldRun(t *testing.T, e *env, source, status string, inserted, discovered int, scheduled bool, errText string) Record {
	t.Helper()
	e.clk.advance(time.Hour)
	rec, err := e.rp.ScanStart(e.ctx, source, map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	rec.Status, rec.Error = status, errText
	rec.Counts.Inserted, rec.Counts.Discovered = inserted, discovered
	rec.Counts.Filtered["x"] = discovered - inserted
	if rec, err = e.rp.ScanFinish(e.ctx, rec); err != nil {
		t.Fatal(err)
	}
	if scheduled {
		if err := e.rp.MarkScheduled(e.ctx, rec.ID); err != nil {
			t.Fatal(err)
		}
	}
	return rec
}

func TestScanHealth_SilentSourceRaisesAlarm(t *testing.T) {
	e := newEnv(t)
	productive := ldRun(t, e, "indeed", RunCompleted, 3, 10, true, "")
	failed := ldRun(t, e, "indeed", RunNeedsSignIn, 0, 0, true, "the board asked for a sign-in")
	a, err := e.rp.SourceAlarmFor(e.ctx, failed)
	if err != nil || a == nil {
		t.Fatalf("alarm = %v, %v", a, err)
	}
	if a.Source != "indeed" || a.Action == "" || a.Retryable || a.LastProductive.IsZero() || !a.LastProductive.Equal(productive.Finished.Truncate(time.Second)) {
		t.Fatalf("alarm = %+v; want the source, a required action, retryable false and the last productive time", a)
	}
}

func TestScanHealth_NeverProductiveSkippedAndDisabledRaiseNothing(t *testing.T) {
	e := newEnv(t)
	never := ldRun(t, e, "glassdoor", RunFailed, 0, 0, true, "boom")
	if a, _ := e.rp.SourceAlarmFor(e.ctx, never); a != nil {
		t.Fatalf("a source that never produced raised %+v", a)
	}
	ldRun(t, e, "zip", RunCompleted, 2, 5, true, "")
	skipped := ldRun(t, e, "zip", RunSkipped, 0, 0, true, "")
	if a, _ := e.rp.SourceAlarmFor(e.ctx, skipped); a != nil {
		t.Fatalf("a skipped run raised %+v", a)
	}
}

func TestScanHealth_ThreeEmptyScheduledRunsRaiseAnAlarmTwoDoNot(t *testing.T) {
	e := newEnv(t)
	ldRun(t, e, "linkedin", RunCompleted, 4, 9, true, "")
	ldRun(t, e, "linkedin", RunCompleted, 0, 0, true, "")
	second := ldRun(t, e, "linkedin", RunCompleted, 0, 0, true, "")
	if a, _ := e.rp.SourceAlarmFor(e.ctx, second); a != nil {
		t.Fatalf("two empty runs raised %+v", a)
	}
	third := ldRun(t, e, "linkedin", RunCompleted, 0, 0, true, "")
	a, err := e.rp.SourceAlarmFor(e.ctx, third)
	if err != nil || a == nil || !a.Retryable {
		t.Fatalf("three empty scheduled runs: %+v, %v; want a retryable alarm", a, err)
	}
}

func TestLeadsEvaluate_ListedIDsRestrictTheRunToThoseLeads(t *testing.T) {
	e := newEnv(t)
	ldBoard(e, "example-co", "Example Co Inc", ldJob(1001, "Backend Engineer"), ldJob(1002, "Platform Engineer"))
	picked := ldPostingLead(t, e, "Picked Corp", ghLink("example-co", 1001))
	left := ldPostingLead(t, e, "Left Corp", ghLink("example-co", 1002))
	res, err := e.sc.EvaluateLeads(e.ctx, EvaluateRequest{Portals: portals([]string{"engineer"}, nil), IDs: []int64{picked}})
	if err != nil {
		t.Fatal(err)
	}
	if res.Enqueued != 1 || len(e.enq.reqs) != 1 {
		t.Fatalf("result = %+v, enqueued %v", res, e.enq.reqs)
	}
	if l, _ := e.rp.LeadGet(e.ctx, left); l.State != LeadNew {
		t.Fatalf("a lead that was not listed is %s, want new", l.State)
	}
}

// Evaluable is what the dashboard's Evaluate button promises, so it holds
// exactly for the leads that a run naming the one lead acts on.
func TestLeadsEvaluable_HoldsForTheLeadsARunActsOn(t *testing.T) {
	e := newEnv(t)
	e.srv.set("/v1/boards/down-co/jobs", route{status: 500})
	add := func(l Lead) int64 {
		l.Source, l.Roles = "linkedin-feed", []string{"Backend Engineer"}
		id, err := e.rp.LeadAdd(e.ctx, l)
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	dismissed := ldPostingLead(t, e, "Gone Corp", ghLink("down-co", 6))
	if err := e.rp.LeadDismiss(e.ctx, dismissed, "dismissed by the user"); err != nil {
		t.Fatal(err)
	}
	leads := map[string]int64{
		"posting":        ldPostingLead(t, e, "Down Corp", ghLink("down-co", 5)),
		"board index":    ldPostingLead(t, e, "Index Corp", "https://jobs.ashbyhq.com/index-corp"),
		"dismissed":      dismissed,
		"dm with a JD":   add(Lead{Company: "JD Corp", Category: LeadDM, Description: ldJDBody(), Evidence: Evidence{URL: "https://example.com/feed/jd", Text: ldJDBody()}}),
		"dm without":     add(Lead{Company: "Short Corp", Category: LeadDM, Description: "DM me.", Evidence: Evidence{URL: "https://example.com/feed/short", Text: "DM me."}}),
		"careers page":   add(Lead{Company: "Page Corp", Category: LeadCareersPage, ApplyLinks: []string{"https://page.example.com/careers"}, Evidence: Evidence{URL: "https://example.com/feed/page", Text: "We are hiring."}}),
		"low confidence": add(Lead{Company: "Vague Corp", Category: LeadLowConfidence, Description: ldJDBody(), Evidence: Evidence{URL: "https://example.com/feed/vague", Text: ldJDBody()}}),
		"unreachable":    add(Lead{Company: "Dark Corp", Category: LeadUnreachable, ApplyLinks: []string{"https://dark.example.com/jobs/1"}, Evidence: Evidence{URL: "https://example.com/feed/dark", Text: "Apply here."}}),
	}
	for name, id := range leads {
		l, err := e.rp.LeadGet(e.ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		promised := Evaluable(l)
		res, err := e.sc.EvaluateLeads(e.ctx, EvaluateRequest{Portals: portals([]string{"engineer"}, nil), IDs: []int64{id}})
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if acted := res != (EvaluateResult{}); promised != acted {
			t.Errorf("%s: Evaluable = %v, but the run acted %v (%+v)", name, promised, acted, res)
		}
	}
}

func TestLeadsEvaluate_EmployerURLUsesOnlyItsLinkedNativeInboxIdentity(t *testing.T) {
	for _, tc := range []struct {
		name                            string
		linked, sameURL, native, closed bool
	}{
		{"matched", true, true, true, false},
		{"closed", true, true, true, true},
		{"unlinked", false, true, true, false},
		{"different posting", true, false, true, false},
		{"no native identity", true, true, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t)
			link := "https://careers.example.org/positions/1001"
			p := Posting{Source: "signals", ATS: Greenhouse, Board: "example-co", JobID: "1001", Company: "Example Co", Title: "Backend Engineer", Location: "Exampleton", URL: link, Description: "Stale cached text"}
			if !tc.sameURL {
				p.URL = "https://careers.example.org/positions/2002"
			}
			if !tc.native {
				p.ATS, p.Board, p.JobID = "", "", ""
			}
			admitted, err := e.rp.AdmitPosting(e.ctx, portals(nil, nil), workspace.Profile{}, p, "signal handoff")
			if err != nil || admitted.Row == nil {
				t.Fatalf("admission = %+v, %v", admitted, err)
			}
			job := ldJob(1001, "Backend Engineer")
			job["absolute_url"] = link
			if tc.closed {
				job["id"] = 9999
			}
			ldBoard(e, "example-co", "Example Co", job)
			id := ldPostingLead(t, e, "Headline Fragment", link)
			if tc.linked {
				if err := e.rp.LeadUpdate(e.ctx, id, LeadPatch{InboxID: &admitted.Row.ID}); err != nil {
					t.Fatal(err)
				}
			}
			res := ldEvaluate(t, e, 0)
			l, _ := e.rp.LeadGet(e.ctx, id)
			switch {
			case tc.linked && tc.sameURL && tc.native && !tc.closed:
				if res.Enqueued != 1 || len(e.enq.reqs) != 1 || e.enq.reqs[0].InboxID != admitted.Row.ID || l.InboxID != admitted.Row.ID || l.State != LeadEvaluated || !strings.Contains(l.Description, "Build services") {
					t.Fatalf("result %+v, lead %+v, requests %+v", res, l, e.enq.reqs)
				}
				if again := ldEvaluate(t, e, 0); again.Enqueued != 0 {
					t.Fatalf("duplicate run = %+v", again)
				}
			case tc.linked && tc.sameURL && tc.native && tc.closed:
				if res.Closed != 1 || l.State != LeadDismissed || l.Reason != "posting-closed" {
					t.Fatalf("result %+v, lead %+v", res, l)
				}
			default:
				if res.Unknown != 1 || res.Enqueued != 0 || len(e.enq.reqs) != 0 || l.State != LeadNew {
					t.Fatalf("result %+v, lead %+v", res, l)
				}
				if hits := e.srv.hitCount("/v1/boards/example-co/jobs"); hits != 0 {
					t.Fatalf("unproven board was queried %d times", hits)
				}
			}
		})
	}
}

func TestLeadsEvaluate_FailedAdmissionIsReportedAndExplicitlyRetryable(t *testing.T) {
	for _, textOnly := range []bool{false, true} {
		t.Run(fmt.Sprintf("text=%t", textOnly), func(t *testing.T) {
			e := newEnv(t)
			var id int64
			if textOnly {
				var err error
				id, err = e.rp.LeadAdd(e.ctx, Lead{Source: "linkedin-feed", Company: "Example Corp", Category: LeadDM, Roles: []string{"Backend Engineer"}, Description: ldJDBody(), Evidence: Evidence{URL: "https://example.com/feed/retry", Text: ldJDBody()}})
				if err != nil {
					t.Fatal(err)
				}
			} else {
				ldBoard(e, "retry-co", "Retry Co", ldJob(1, "Backend Engineer"))
				id = ldPostingLead(t, e, "Fragment", ghLink("retry-co", 1))
			}
			e.sc.Enqueuer = failingPool{}
			res, err := e.sc.EvaluateLeads(e.ctx, EvaluateRequest{})
			if err == nil || !strings.Contains(err.Error(), "pool is full") || res.Failed != 1 || res.Enqueued != 0 {
				t.Errorf("failed admission result=%+v err=%v", res, err)
			}
			l, err := e.rp.LeadGet(e.ctx, id)
			if err != nil || l.Category != LeadEvalFailed || l.State != LeadNew {
				t.Fatalf("failed lead=%+v err=%v", l, err)
			}
			e.sc.Enqueuer = e.enq
			res, err = e.sc.EvaluateLeads(e.ctx, EvaluateRequest{})
			if err != nil || res.Enqueued != 0 || len(e.enq.reqs) != 0 {
				t.Fatalf("failed lead retried without explicit selection: %+v %v", res, err)
			}
			res, err = e.sc.EvaluateLeads(e.ctx, EvaluateRequest{IDs: []int64{id}})
			if err != nil || res.Enqueued != 1 || len(e.enq.reqs) != 1 {
				t.Fatalf("explicit retry=%+v err=%v requests=%v", res, err, e.enq.reqs)
			}
			req := e.enq.reqs[0]
			if (req.Text != "") != textOnly || req.Mode != "deep" {
				t.Fatalf("retry lost original path: %+v", req)
			}
			l, err = e.rp.LeadGet(e.ctx, id)
			if err != nil || l.State != LeadEvaluated || l.Category == LeadEvalFailed || l.InboxID == 0 {
				t.Fatalf("retried lead=%+v err=%v", l, err)
			}
			rows, err := e.rp.InboxList(e.ctx, InboxQuery{})
			if err != nil || len(rows) != 1 {
				t.Fatalf("retry rows=%+v err=%v", rows, err)
			}
		})
	}
}

func TestLeadsEvaluate_AcceptedWorkConsumesTheCapDespiteFinalizationFailure(t *testing.T) {
	e := newEnv(t)
	ldBoard(e, "persist-co", "Persist Co", ldJob(1, "Backend Engineer"), ldJob(2, "Data Engineer"))
	first := ldPostingLead(t, e, "First Fragment", ghLink("persist-co", 1))
	ldPostingLead(t, e, "Second Fragment", ghLink("persist-co", 2))
	if err := e.st.Write(e.ctx, func(tx *sql.Tx) error {
		_, err := tx.Exec(`CREATE TRIGGER owned_lead_completion_refusal BEFORE UPDATE ON leads WHEN NEW.state = 'evaluated' BEGIN SELECT RAISE(FAIL, 'owned completion refusal'); END`)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	res, err := e.sc.EvaluateLeads(e.ctx, EvaluateRequest{Limit: 1})
	if err == nil || res.Enqueued != 1 || res.Failed != 1 || res.Capped != 1 || len(e.enq.reqs) != 1 {
		t.Errorf("result=%+v err=%v requests=%d", res, err, len(e.enq.reqs))
	}
	if err := e.st.Write(e.ctx, func(tx *sql.Tx) error { _, err := tx.Exec("DROP TRIGGER owned_lead_completion_refusal"); return err }); err != nil {
		t.Fatal(err)
	}
	res, err = e.sc.EvaluateLeads(e.ctx, EvaluateRequest{IDs: []int64{first}})
	if err != nil || res.Known != 1 || res.Enqueued != 0 || len(e.enq.reqs) != 1 {
		t.Fatalf("accepted retry duplicated: result=%+v err=%v requests=%d", res, err, len(e.enq.reqs))
	}
}

type leadAdmissionFunc func(context.Context, EvalRequest) (string, error)

func (f leadAdmissionFunc) Enqueue(ctx context.Context, req EvalRequest) (string, error) {
	return f(ctx, req)
}

func TestLeadsEvaluate_CancelledAdmissionRetainsAcceptedCount(t *testing.T) {
	for _, accept := range []bool{false, true} {
		t.Run(fmt.Sprintf("accepted=%t", accept), func(t *testing.T) {
			e := newEnv(t)
			ldBoard(e, "cancel-co", "Cancel Co", ldJob(1, "Backend Engineer"))
			id := ldPostingLead(t, e, "Fragment", ghLink("cancel-co", 1))
			ctx, cancel := context.WithCancel(e.ctx)
			defer cancel()
			e.sc.Enqueuer = leadAdmissionFunc(func(context.Context, EvalRequest) (string, error) {
				cancel()
				if accept {
					return "accepted-job", nil
				}
				return "", context.Canceled
			})
			res, err := e.sc.EvaluateLeads(ctx, EvaluateRequest{})
			want := 0
			if accept {
				want = 1
			}
			if !errors.Is(err, context.Canceled) || res.Enqueued != want {
				t.Errorf("result=%+v err=%v", res, err)
			}
			l, err := e.rp.LeadGet(e.ctx, id)
			if err != nil {
				t.Fatal(err)
			}
			if accept && (l.State != LeadEvaluated || l.InboxID == 0) {
				t.Fatalf("accepted lead not finalized: %+v", l)
			}
			if !accept && l.State != LeadNew {
				t.Fatalf("refused lead settled: %+v", l)
			}
		})
	}
}

func TestLeadsEvaluate_OwnTextProvenanceSurvivesReviewCategory(t *testing.T) {
	for _, transition := range []string{"failed", "capped"} {
		for _, category := range []string{LeadDM} {
			for _, linkInEvidence := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/%s/evidence=%t", transition, category, linkInEvidence), func(t *testing.T) {
					e := newEnv(t)
					if transition == "capped" {
						ldBoard(e, "first-co", "First Co", ldJob(1, "Backend Engineer"))
						ldPostingLead(t, e, "First Fragment", ghLink("first-co", 1))
					}
					input := Lead{Source: "signals", Category: category, Company: "Owned Corp", Roles: []string{"Backend Engineer"}, Description: ldJDBody(), ApplyLinks: []string{"https://careers.example.com/jobs/owned-42"}, Evidence: Evidence{URL: "https://news.example.com/owned-text", Text: ldJDBody()}}
					if linkInEvidence {
						input.ApplyLinks = nil
						input.Evidence.URL = "https://careers.example.com/jobs/owned-42"
					}
					id, err := e.rp.LeadAdd(e.ctx, input)
					if err != nil {
						t.Fatal(err)
					}
					if transition == "failed" {
						e.sc.Enqueuer = failingPool{}
					}
					first, err := e.sc.EvaluateLeads(e.ctx, EvaluateRequest{Limit: 1})
					if transition == "failed" && (err == nil || first.Failed != 1) {
						t.Fatalf("failure setup %+v %v", first, err)
					}
					if transition == "capped" && (err != nil || first.Capped != 1) {
						t.Fatalf("cap setup %+v %v", first, err)
					}
					saved, err := e.rp.LeadGet(e.ctx, id)
					if err != nil || saved.EvaluationCategory != category || evalPath(saved) != pathText {
						t.Fatalf("provenance=%+v err=%v", saved, err)
					}
					e.sc.Enqueuer = e.enq
					before := len(e.enq.reqs)
					again, err := e.sc.EvaluateLeads(e.ctx, EvaluateRequest{IDs: []int64{id}})
					if err != nil || again.Enqueued != 1 || len(e.enq.reqs) != before+1 {
						t.Fatalf("own-text retry switched path: result=%+v err=%v requests before=%d after=%d", again, err, before, len(e.enq.reqs))
					}
					if got := e.enq.reqs[len(e.enq.reqs)-1].Text; got != ldJDBody() {
						t.Fatalf("own-text retry lost body; length=%d", len(got))
					}
					saved, err = e.rp.LeadGet(e.ctx, id)
					if err != nil || saved.Category != category || saved.State != LeadEvaluated {
						t.Fatalf("restored category=%+v err=%v", saved, err)
					}
				})
			}
		}
	}
}

func TestLeadsEvaluate_MissingReviewProvenanceIsAnActionableFailure(t *testing.T) {
	for _, category := range []string{LeadEvalCap, LeadEvalFailed} {
		t.Run(category, func(t *testing.T) {
			e := newEnv(t)
			id, err := e.rp.LeadAdd(e.ctx, Lead{Source: "signals", Category: category, Company: "Example Corp", Roles: []string{"Backend Engineer"}, Description: ldJDBody(), Evidence: Evidence{URL: "https://example.com/jobs/unknown", Text: ldJDBody()}})
			if err != nil {
				t.Fatal(err)
			}
			res, err := e.sc.EvaluateLeads(e.ctx, EvaluateRequest{IDs: []int64{id}})
			if err == nil || !strings.Contains(err.Error(), "original evaluation category is missing") || res.Failed != 1 || len(e.enq.reqs) != 0 {
				t.Fatalf("result=%+v err=%v requests=%v", res, err, e.enq.reqs)
			}
		})
	}
}

func TestLeadsEvaluate_HistoricalCapDoesNotStarveHealthyWork(t *testing.T) {
	e := newEnv(t)
	_, err := e.rp.LeadAdd(e.ctx, Lead{Source: "signals", Category: LeadEvalCap, Company: "Historical Corp", Description: ldJDBody(), Evidence: Evidence{URL: "https://example.com/historical", Text: ldJDBody()}})
	if err != nil {
		t.Fatal(err)
	}
	_, err = e.rp.LeadAdd(e.ctx, Lead{Source: "signals", Category: LeadNoATS, Company: "Healthy Corp", Description: ldJDBody(), Evidence: Evidence{URL: "https://example.com/healthy", Text: ldJDBody()}})
	if err != nil {
		t.Fatal(err)
	}
	res, err := e.sc.EvaluateLeads(e.ctx, EvaluateRequest{})
	if err == nil || res.Failed != 1 {
		t.Fatalf("missing provenance not reported: result=%+v err=%v", res, err)
	}
	if res.Enqueued != 1 || len(e.enq.reqs) != 1 {
		t.Fatalf("historical capped row starved healthy independent work: result=%+v err=%v requests=%d", res, err, len(e.enq.reqs))
	}
}

func TestLeadsEvaluate_KnownTrackerPostingRestoresOriginalCategory(t *testing.T) {
	for _, review := range []string{LeadEvalCap, LeadEvalFailed} {
		t.Run(review, func(t *testing.T) {
			e := newEnv(t)
			ldBoard(e, "known-co", "Known Co", ldJob(1, "Backend Engineer"))
			url := ghLink("known-co", 1)
			id := ldPostingLead(t, e, "Fragment", url)
			if err := e.rp.LeadUpdate(e.ctx, id, LeadPatch{Category: &review, EvaluationCategory: leadPtr(LeadPosting)}); err != nil {
				t.Fatal(err)
			}
			e.trackedRow("Known Co", "Backend Engineer", url)
			res, err := e.sc.EvaluateLeads(e.ctx, EvaluateRequest{IDs: []int64{id}})
			lead, readErr := e.rp.LeadGet(e.ctx, id)
			if err != nil || readErr != nil || res.Known != 1 || len(e.enq.reqs) != 0 || lead.Category != LeadPosting || lead.State != LeadEvaluated {
				t.Fatalf("result=%+v err=%v lead=%+v read=%v", res, err, lead, readErr)
			}
		})
	}
}
