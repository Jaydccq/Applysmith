package apply

import (
	"context"
	"database/sql"
	"testing"

	"applysmith/internal/gates"
	"applysmith/internal/ledger"
)

func TestApply_Resolve_TenantExtractionPerATS(t *testing.T) {
	for _, c := range []struct {
		name, url, ats, tenant, applyURL string
	}{
		{"generic host", "https://careers.example.test/jobs/7", ATSGeneric, "careers.example.test", "https://careers.example.test/jobs/7"},
		{"generic www dropped", "https://www.example.test/careers/7", ATSGeneric, "example.test", "https://www.example.test/careers/7"},
		{"legacy greenhouse", "https://boards.greenhouse.io/example-corp/jobs/1234567", ATSGreenhouse, "example-corp", "https://job-boards.greenhouse.io/embed/job_app?for=example-corp&token=1234567"},
		{"job-boards greenhouse as is", "https://job-boards.greenhouse.io/example-corp/jobs/1234567", ATSGreenhouse, "example-corp", "https://job-boards.greenhouse.io/example-corp/jobs/1234567"},
		{"greenhouse embed", "https://job-boards.greenhouse.io/embed/job_app?for=example-corp&token=1234567", ATSGreenhouse, "example-corp", "https://job-boards.greenhouse.io/embed/job_app?for=example-corp&token=1234567"},
		{"lever", "https://jobs.lever.co/example-corp/0a1b2c3d-1111-2222-3333-444455556666?lever-source=x", ATSLever, "example-corp", "https://jobs.lever.co/example-corp/0a1b2c3d-1111-2222-3333-444455556666/apply"},
		{"ashby with embed parameter", "https://jobs.ashbyhq.com/example-corp/0a1b2c3d-1111-2222-3333-444455556666?embed=js", ATSAshby, "example-corp", "https://jobs.ashbyhq.com/example-corp/0a1b2c3d-1111-2222-3333-444455556666/application"},
		{"workday", "https://example.wd5.myworkdayjobs.com/en-US/Careers/job/Durham-NC/Engineer_R100/apply", ATSWorkday, "example", "https://example.wd5.myworkdayjobs.com/en-US/Careers/job/Durham-NC/Engineer_R100/apply/applyManually"},
		// Every employer on myworkdaysite.com shares the host: the tenant is in the path, so one employer's account is never another's.
		{"workday site", "https://wd5.myworkdaysite.com/en-US/recruiting/ExampleHR/External/job/Durham-NC/Engineer_R100", ATSWorkday, "examplehr", "https://wd5.myworkdaysite.com/en-US/recruiting/ExampleHR/External/job/Durham-NC/Engineer_R100/apply/applyManually"},
		{"linkedin view", "https://www.linkedin.com/jobs/view/4000000001", ATSLinkedIn, "linkedin", "https://www.linkedin.com/jobs/view/4000000001/apply/?openSDUIApplyFlow=true"},
		{"smartrecruiters api", "https://api.smartrecruiters.com/v1/companies/ExampleCorp/postings/7440000000001", ATSGeneric, "jobs.smartrecruiters.com", "https://jobs.smartrecruiters.com/ExampleCorp/7440000000001"},
	} {
		t.Run(c.name, func(t *testing.T) {
			got, s := Classify(c.url)
			if s != nil {
				t.Fatalf("stop %+v", s)
			}
			if got.ATS != c.ats || got.Tenant != c.tenant || got.ApplyURL != c.applyURL {
				t.Fatalf("got %+v", got)
			}
		})
	}
}

func TestApply_Resolve_NonApplicationURLsStopBeforeBrowser(t *testing.T) {
	for _, u := range []string{
		"ftp://careers.example.test/jobs/7", "mailto:hr@example.test", "not a url", "https://user:pw@careers.example.test/x",
		"https://boards.greenhouse.io/example-corp", "https://jobs.lever.co/example-corp", "https://jobs.ashbyhq.com/example-corp",
		"https://example.wd5.myworkdayjobs.com/en-US/Careers", "https://www.linkedin.com/feed/", "https://www.linkedin.com/in/alex-example",
		"https://www.linkedin.com/jobs/search/?keywords=go", "https://www.prnewswire.com/news-releases/example-hires.html",
		"https://googleadservices.com/pagead/aclk?x=1", "https://api.smartrecruiters.com/v1/companies/x",
	} {
		_, s := Classify(u)
		if s == nil || s.class != ClassPosting || s.reason != "not an application URL" {
			t.Errorf("%s: stop %+v", u, s)
		}
	}
}

func TestApply_Resolve_PressWireIsNotAnApplication(t *testing.T) {
	g := newRig(t, nil)
	run := g.r.RunOne(context.Background(), Input{URL: "https://www.businesswire.com/news/2027/example-hires", Caller: gates.CallerCLI})
	mustStop(t, run, StageResolve, ClassPosting)
	if run.Outcome.Reason != "not an application URL" {
		t.Fatalf("reason %q", run.Outcome.Reason)
	}
}

func TestApply_Resolve_PostingAPIRewrittenButIdentityStaysTheOriginal(t *testing.T) {
	g := newRig(t, nil)
	var applyURL string
	g.r.d.Drive = func(ctx context.Context, e *Exec) *stop { applyURL = e.Cls.ApplyURL; return nil }
	in := Input{URL: "https://api.smartrecruiters.com/v1/companies/ExampleCorp/postings/7440000000001", Caller: gates.CallerCLI}
	run := g.r.RunOne(context.Background(), in)
	if applyURL != "https://jobs.smartrecruiters.com/ExampleCorp/7440000000001" {
		t.Fatalf("walk opens %q", applyURL)
	}
	if run.Identity != "smartrecruiters:examplecorp/7440000000001" {
		t.Fatalf("identity %q", run.Identity)
	}
}

func TestApply_Resolve_BindsReportByIdentityNeverUsesHostAsCompany(t *testing.T) {
	g := newRig(t, nil)
	key := "https://careers.example.test/jobs/7"
	seedReport(t, g, 11, key, "Old Name", "Engineer", "reports/011-old.md")
	seedReport(t, g, 12, key, "Example Corp", "Platform Engineer", "reports/012-new.md")
	// Only the newer report is named by a live tracker row.
	seedTracker(t, g, key, "Example Corp", "Platform Engineer", 12)
	b, err := SQLReports{DB: g.st.Read()}.Bind(context.Background(), key)
	if err != nil || b == nil {
		t.Fatalf("binding %+v err %v", b, err)
	}
	if b.Number != 12 || b.Path != "reports/012-new.md" || b.Company != "Example Corp" || b.Role != "Platform Engineer" {
		t.Fatalf("binding %+v", b)
	}
	if none, _ := (SQLReports{DB: g.st.Read()}).Bind(context.Background(), "https://other.example.test/x"); none != nil {
		t.Fatalf("a posting with no report bound to %+v", none)
	}
}

func TestApply_Guard_DryRunNeverBlockedSubmitBlockedByConfirmedRow(t *testing.T) {
	g := newRig(t, func(d *Deps) { d.Drive = func(context.Context, *Exec) *stop { return nil } })
	if _, err := g.ledger.RecordObserved(context.Background(), ledger.Observation{EntryID: "e1", URL: pageURL, Host: "careers.example.test", Company: "Example Corp", Role: "Engineer", ObservedAt: t0, Confirmed: true}); err != nil {
		t.Fatal(err)
	}
	dry := g.r.RunOne(context.Background(), Input{URL: pageURL, Caller: gates.CallerCLI})
	if dry.Outcome.Kind != OutcomeFilled {
		t.Fatalf("a dry run is never blocked by duplicate evidence: %+v", dry.Outcome)
	}
	sub := g.r.RunOne(context.Background(), Input{URL: pageURL, Caller: gates.CallerCLI, Submit: true, ExplicitRequest: true})
	mustStop(t, sub, StageResolve, ClassPosting)
	if g.audits(gates.GateDuplicateGuard) != 2 {
		t.Fatalf("guard rows %d", g.audits(gates.GateDuplicateGuard))
	}
}

func TestApply_Guard_UnreadableTrackerBlocksASubmittingRun(t *testing.T) {
	g := newRig(t, func(d *Deps) { d.Drive = func(context.Context, *Exec) *stop { return nil } })
	g.breakTable("tracker_entries")
	run := g.r.RunOne(context.Background(), Input{URL: pageURL, Caller: gates.CallerCLI, Submit: true, ExplicitRequest: true})
	mustStop(t, run, StageResolve, ClassEnvironment)
}

func (g *rig) breakTable(name string) {
	g.t.Helper()
	err := g.st.Write(context.Background(), func(tx *sql.Tx) error {
		_, err := tx.Exec(`ALTER TABLE ` + name + ` RENAME TO ` + name + `_broken`)
		return err
	})
	if err != nil {
		g.t.Fatal(err)
	}
}

func seedReport(t *testing.T, g *rig, n int64, key, company, role, path string) {
	t.Helper()
	err := g.st.Write(context.Background(), func(tx *sql.Tx) error {
		if _, err := tx.Exec(`INSERT INTO report_artifacts (report_number, kind, path, status, content_hash, created_at, updated_at)
			VALUES (?, 'final', ?, 'ready', 'h', '2027-03-01T00:00:00.000Z', '2027-03-01T00:00:00.000Z')`, n, path); err != nil {
			return err
		}
		_, err := tx.Exec(`INSERT INTO report_index (report_number, kind, company, role, posting_url, identity_key, score, score_kind, recommendation, legitimacy, verification, markdown_path, html_path, job_id, created_at)
			VALUES (?, 'final', ?, ?, ?, ?, 4.2, 'final', 'apply', 'credible', 'x', ?, '', 'j', '2027-03-01T00:00:00.000Z')`, n, company, role, key, key, path)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
}

func seedTracker(t *testing.T, g *rig, key, company, role string, report int64) {
	t.Helper()
	err := g.st.Write(context.Background(), func(tx *sql.Tx) error {
		_, err := tx.Exec(`INSERT INTO tracker_entries (date, company, role, score, score_kind, status, report_number, posting_url, identity_key, created_at, updated_at)
			VALUES ('2027-03-01', ?, ?, 4.2, 'final', 'evaluated', ?, ?, ?, '2027-03-01T00:00:00.000Z', '2027-03-01T00:00:00.000Z')`, company, role, report, key, key)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestApply_Adapters_OwnershipAndSuccessHints(t *testing.T) {
	for ats, line := range map[string]string{
		ATSGreenhouse: "Thank you for applying to Example Corp.", ATSLever: "Application submitted!", ATSAshby: "Your application was successfully submitted.",
		ATSWorkday: "Application Submitted", ATSLinkedIn: "Application sent", ATSGeneric: "We've received your application.",
	} {
		if SuccessText(ats, []string{"Header", line}) == "" {
			t.Errorf("%s: %q not read as the success text", ats, line)
		}
	}
	// Node's Ashby manifest test: the success sentence interpolates the employer.
	if SuccessText(ATSAshby, []string{"Your application to Juniper Square was successfully submitted."}) == "" {
		t.Error("Ashby's success sentence with the employer's name is not read")
	}
	for _, line := range []string{"Have you submitted an application before?", "Submit your application below"} {
		if SuccessText(ATSGeneric, []string{line}) != "" {
			t.Errorf("%q read as a success text", line)
		}
	}
	for _, u := range []struct{ url, ats string }{
		{"https://boards.greenhouse.io/x/jobs/1234567", ATSGreenhouse}, {"https://jobs.lever.co/x/0a1b2c3d-1111-2222-3333-444455556666", ATSLever},
		{"https://jobs.ashbyhq.com/x/0a1b2c3d-1111-2222-3333-444455556666", ATSAshby}, {"https://x.wd1.myworkdayjobs.com/en-US/S/job/L/T_R1", ATSWorkday},
		{"https://www.linkedin.com/jobs/view/4000000001", ATSLinkedIn}, {"https://careers.example.test/jobs/7", ATSGeneric},
	} {
		if got, s := Classify(u.url); s != nil || got.ATS != u.ats {
			t.Errorf("%s: %+v %+v", u.url, got, s)
		}
	}
}
