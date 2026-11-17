package discovery

import (
	"errors"
	"strings"
	"testing"
	"time"

	"applysmith/internal/workspace"
)

// ldFeedRun is a run shaped like a linkedin-feed scan, over the test env.
func ldFeedRun(t *testing.T, e *env, positive, negative []string) *run {
	t.Helper()
	r, err := e.sc.leadRun(e.ctx, portals(positive, negative), workspace.Profile{})
	if err != nil {
		t.Fatal(err)
	}
	r.req.Source = "linkedin-feed"
	return r
}

func ldPost(permalink, body string, roles ...string) FeedLeadInput {
	return FeedLeadInput{Permalink: permalink, Author: "Alex Example", Body: body, PosterType: "company", Company: "Example Corp",
		Roles: roles, Locations: []string{"Exampleton"}, ContactMethod: "link", Sponsorship: "unknown", Confidence: 0.8}
}

func TestLeads_EvidenceAlwaysPresentOrReasonGiven(t *testing.T) {
	e := newEnv(t)
	if _, err := e.rp.LeadAdd(e.ctx, Lead{Source: "signals", Company: "Example Corp", Category: LeadNoATS}); err == nil {
		t.Fatal("a lead with neither evidence text nor an absent reason was stored")
	}
	long := strings.Repeat("x", 6000)
	id, err := e.rp.LeadAdd(e.ctx, Lead{Source: "signals", Company: "Example Corp", Category: LeadNoATS, Evidence: Evidence{URL: "https://news.example.com/a", Text: long}})
	if err != nil {
		t.Fatal(err)
	}
	got, _ := e.rp.LeadGet(e.ctx, id)
	if n := len([]rune(got.Evidence.Text)); n != 4000 {
		t.Fatalf("evidence text is %d characters, want the 4,000 cap", n)
	}
	id2, err := e.rp.LeadAdd(e.ctx, Lead{Source: "exa", Company: "Other Corp", Category: LeadNoATS, Evidence: Evidence{URL: "https://news.example.com/b", AbsentReason: "the article could not be read"}})
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := e.rp.LeadGet(e.ctx, id2); got.Evidence.AbsentReason == "" {
		t.Fatal("the absent reason was not kept")
	}
}

func TestLeads_NoATSLeadDedupedByCompanyAndEvidenceURL(t *testing.T) {
	e := newEnv(t)
	ev := Evidence{URL: "https://news.example.com/a", Text: "Example Corp raised money."}
	first, err := e.rp.AddNoATSLead(e.ctx, "signals", "Example Corp", ev)
	if err != nil {
		t.Fatal(err)
	}
	second, err := e.rp.AddNoATSLead(e.ctx, "signals", "Example Corp, Inc.", ev)
	if !errors.Is(err, ErrLeadDuplicate) || second != first {
		t.Fatalf("second no-ats lead = %d, %v; want the first lead %d and ErrLeadDuplicate", second, err, first)
	}
	if _, err := e.rp.AddNoATSLead(e.ctx, "signals", "Example Corp", Evidence{URL: "https://news.example.com/other", Text: "More news."}); err != nil {
		t.Fatalf("a new evidence URL is a new lead: %v", err)
	}
}

func TestLeads_AddCareersPageStoresOncePerPage(t *testing.T) {
	e := newEnv(t)
	for i := 0; i < 2; i++ {
		if err := e.rp.AddCareersPage(e.ctx, "https://example.com/careers", "Example Corp", "exa"); err != nil {
			t.Fatal(err)
		}
	}
	if n, _ := e.rp.LeadCount(e.ctx, LeadFilter{Category: LeadCareersPage}); n != 1 {
		t.Fatalf("%d careers-page leads, want 1", n)
	}
}

func TestLeads_FeedDedup_PermalinkBodyHashAndRepostWindow(t *testing.T) {
	e := newEnv(t)
	r := ldFeedRun(t, e, []string{"engineer"}, nil)
	body := "We are hiring a Backend Engineer. Apply at https://jobs.lever.co/example-lever/abc123?utm_source=feed"

	// Same permalink: one lead.
	a := ldPost("https://www.example.com/feed/update/1/?trk=a", body, "Backend Engineer")
	id1, _, err := r.addFeedLead(e.ctx, a)
	if err != nil {
		t.Fatal(err)
	}
	b := ldPost("https://example.com/feed/update/1", "different text but the same post", "Data Engineer")
	id2, promoted, err := r.addFeedLead(e.ctx, b)
	if err != nil || promoted || id2 != id1 {
		t.Fatalf("same permalink: id %d promoted %v err %v; want lead %d", id2, promoted, err, id1)
	}

	// No permalink: the same author and body, whitespace, case and tracking aside, is one lead.
	n1 := ldPost("", "Hiring  a Platform Engineer at Example Corp. See https://example.com/x?utm_medium=z", "Platform Engineer")
	n1.Company = "Platform Corp"
	n2 := n1
	n2.Body = "hiring a platform engineer at example corp. see https://example.com/x"
	idA, _, _ := r.addFeedLead(e.ctx, n1)
	idB, _, _ := r.addFeedLead(e.ctx, n2)
	if idA == 0 || idA != idB {
		t.Fatalf("no permalink: leads %d and %d, want one", idA, idB)
	}

	// Repost: a new permalink with the same company and role.
	e.clk.advance(9 * 24 * time.Hour)
	rp := ldPost("https://example.com/feed/update/2", "Fresh wording of the same opening.", "Backend Engineer")
	if _, _, err := r.addFeedLead(e.ctx, rp); err != nil {
		t.Fatal(err)
	}
	if got := r.rec.Counts.Duplicates["repost"]; got != 1 {
		t.Fatalf("repost duplicates = %d, want 1 (%v)", got, r.rec.Counts.Duplicates)
	}
	if r.rec.Detail["duplicate:repost"] != 1 {
		t.Fatalf("detail = %v", r.rec.Detail)
	}
	// 15 days after the first it is a new lead.
	e.clk.advance(6 * 24 * time.Hour)
	late := ldPost("https://example.com/feed/update/3", "Yet another wording.", "Backend Engineer")
	idLate, _, err := r.addFeedLead(e.ctx, late)
	if err != nil || idLate == 0 || idLate == id1 {
		t.Fatalf("a repost after the window = %d, %v; want a new lead", idLate, err)
	}
	if err := r.rec.Counts.Validate(); err != nil {
		t.Fatalf("the run counts do not reconcile: %v", err)
	}
}

func TestLinkedInFeed_ConfidenceBoundaryAndLinkKinds(t *testing.T) {
	e := newEnv(t)
	r := ldFeedRun(t, e, []string{"engineer"}, nil)
	link := "https://jobs.lever.co/example-lever/abc123"
	low := ldPost("https://example.com/p/1", "Hiring a Backend Engineer", "Backend Engineer")
	low.ApplyLinks, low.Confidence = []string{link}, 0.49
	idLow, promoted, err := r.addFeedLead(e.ctx, low)
	if err != nil || promoted {
		t.Fatalf("confidence 0.49: promoted %v, %v", promoted, err)
	}
	if l, _ := e.rp.LeadGet(e.ctx, idLow); l.Category != LeadLowConfidence {
		t.Fatalf("category = %s, want low-confidence", l.Category)
	}
	high := ldPost("https://example.com/p/2", "Hiring a Backend Engineer too", "Backend Engineer")
	high.Company, high.ApplyLinks, high.Confidence = "Lever Corp", []string{"https://example.com/careers", link}, 0.5
	id, promoted, err := r.addFeedLead(e.ctx, high)
	if err != nil || !promoted {
		t.Fatalf("confidence 0.5: promoted %v, %v", promoted, err)
	}
	l, _ := e.rp.LeadGet(e.ctx, id)
	if l.Category != LeadPosting || l.ApplyLinks[0] != link || l.InboxID == 0 {
		t.Fatalf("lead = %+v; want a posting lead on the Lever link with an inbox row", l)
	}
}

func TestLeads_PostingLeadIsNotFiledAsCareersPage(t *testing.T) {
	e := newEnv(t)
	r := ldFeedRun(t, e, []string{"engineer"}, nil)
	cases := []struct {
		name  string
		links []string
		want  string
	}{
		{"single posting", []string{"https://jobs.lever.co/example-lever/abc123"}, LeadPosting},
		{"careers index only", []string{"https://example.com/careers"}, LeadCareersPage},
		{"company home only", []string{"https://example.com/"}, LeadGenericPage},
	}
	for i, c := range cases {
		in := ldPost("https://example.com/post/"+itoa(i), "Hiring an engineer: "+c.name, "Backend Engineer")
		in.Company = "Case Corp " + itoa(i)
		in.ApplyLinks = c.links
		id, _, err := r.addFeedLead(e.ctx, in)
		if err != nil {
			t.Fatal(err)
		}
		if l, _ := e.rp.LeadGet(e.ctx, id); l.Category != c.want {
			t.Errorf("%s: category %s, want %s", c.name, l.Category, c.want)
		}
	}
}

func TestLinkedInFeed_DMPostIsStoredAndNothingIsSent(t *testing.T) {
	e := newEnv(t)
	r := ldFeedRun(t, e, []string{"engineer"}, nil)
	in := ldPost("https://example.com/post/dm", "Hiring a Backend Engineer, DM me", "Backend Engineer")
	in.ContactMethod = "dm"
	id, promoted, err := r.addFeedLead(e.ctx, in)
	if err != nil || promoted {
		t.Fatal(promoted, err)
	}
	if l, _ := e.rp.LeadGet(e.ctx, id); l.Category != LeadDM || l.ContactMethod != "dm" {
		t.Fatalf("lead = %+v", l)
	}
	if r.rec.Counts.Filtered["lead:dm"] != 1 {
		t.Fatalf("filtered = %v", r.rec.Counts.Filtered)
	}
}

func TestLeads_GateRejectionAuditedWithAllRules_MultiRoleNarrowed(t *testing.T) {
	e := newEnv(t)
	e.sc.Gate = func(row InboxRow) string {
		if strings.Contains(row.Role, "Principal") {
			return "seniority"
		}
		return ""
	}
	r := ldFeedRun(t, e, []string{"engineer"}, []string{"principal"})

	// Fails the title filter and the gate: one audit entry lists both.
	bad := ldPost("https://example.com/post/b", "Hiring a Principal Engineer", "Principal Engineer")
	if _, _, err := r.addFeedLead(e.ctx, bad); err != nil {
		t.Fatal(err)
	}
	audit, _ := e.rp.GateAuditList(e.ctx, IngressFeed, 10)
	if len(audit) != 1 || len(audit[0].Rules) != 2 || audit[0].Rules[0] != FilterTitle || audit[0].Rules[1] != "seniority" {
		t.Fatalf("audit = %+v; want one feed entry with the title and seniority rules", audit)
	}
	if n, _ := e.rp.LeadCount(e.ctx, LeadFilter{}); n != 0 {
		t.Fatalf("%d leads stored for a rejected post", n)
	}

	// A lead naming an over-senior and a passing role is narrowed.
	mixed := ldPost("https://example.com/post/m", "Hiring engineers", "Principal Engineer", "Junior Engineer")
	mixed.Company = "Mixed Corp"
	id, _, err := r.addFeedLead(e.ctx, mixed)
	if err != nil {
		t.Fatal(err)
	}
	l, _ := e.rp.LeadGet(e.ctx, id)
	if len(l.Roles) != 1 || l.Roles[0] != "Junior Engineer" {
		t.Fatalf("roles = %v, want only the junior one", l.Roles)
	}
	if audit, _ := e.rp.GateAuditList(e.ctx, IngressFeed, 10); len(audit) != 2 {
		t.Fatalf("%d audit entries, want 2 (the rejected role is recorded)", len(audit))
	}
	if err := r.rec.Counts.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestLeads_MultiLocationRejectedOnlyWhenEveryLocationFails(t *testing.T) {
	c := LeadChecker{Gate: func(row InboxRow) string {
		if row.Location == "Nowhere" {
			return "geography"
		}
		return ""
	}}
	base := Posting{Company: "Example Corp"}
	kept, rej := c.Narrow(base, []string{"Engineer"}, []string{"Nowhere", "Exampleton"})
	if len(kept) != 1 || len(rej) != 0 {
		t.Fatalf("one passing location: kept %v rejected %v", kept, rej)
	}
	kept, rej = c.Narrow(base, []string{"Engineer"}, []string{"Nowhere"})
	if len(kept) != 0 || len(rej) != 1 || rej[0].Rules[0] != "geography" {
		t.Fatalf("every location failing: kept %v rejected %v", kept, rej)
	}
}

func TestLeads_DescriptionCapNotClippingRealPostings(t *testing.T) {
	e := newEnv(t)
	real := strings.Repeat("a", 10417) // the largest description measured on a real posting
	id, err := e.rp.LeadAdd(e.ctx, Lead{Source: "signals", Company: "Example Corp", Category: LeadPosting, ApplyLinks: []string{"https://jobs.lever.co/example-lever/abc123"},
		Description: real, Evidence: Evidence{AbsentReason: "the posting is its own evidence"}})
	if err != nil {
		t.Fatal(err)
	}
	if l, _ := e.rp.LeadGet(e.ctx, id); len([]rune(l.Description)) != 10417 {
		t.Fatalf("a real description was clipped to %d", len([]rune(l.Description)))
	}
	over := strings.Repeat("b", 25000)
	d := over
	if err := e.rp.LeadUpdate(e.ctx, id, LeadPatch{Description: &d}); err != nil {
		t.Fatal(err)
	}
	if l, _ := e.rp.LeadGet(e.ctx, id); len([]rune(l.Description)) != 20000 {
		t.Fatalf("description = %d characters, want the 20,000 cap", len([]rune(l.Description)))
	}
}
