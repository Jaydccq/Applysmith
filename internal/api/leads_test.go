package api

import (
	"slices"
	"strings"
	"testing"

	"applysmith/internal/api/wire"
	"applysmith/internal/discovery"
)

func TestLeadsListFilterAndDismiss(t *testing.T) {
	h := newDomainHarness(t)
	repo := h.domainOf().repo
	ev := discovery.Evidence{URL: "https://news.example.com/a", Text: "Example Corp is hiring."}
	if _, err := repo.LeadAdd(t.Context(), discovery.Lead{Source: "signals", Company: "Example Corp", Category: discovery.LeadNoATS, Evidence: ev}); err != nil {
		t.Fatal(err)
	}
	if err := repo.AddCareersPage(t.Context(), "https://example.com/careers", "Example Corp", "exa"); err != nil {
		t.Fatal(err)
	}
	var page wire.Success[wire.LeadPage]
	r := h.get("/api/leads?pageSize=1&page=2")
	strictInto(t, r.body, &page)
	if r.StatusCode != 200 || page.Data.Total != 2 || len(page.Data.Rows) != 1 || page.Data.Counts[discovery.LeadCareersPage] != 1 {
		t.Fatalf("list: %d %s", r.StatusCode, r.body)
	}
	only := h.get("/api/leads?category=no-ats")
	if only.data()["total"].(float64) != 1 {
		t.Fatalf("category filter: %s", only.body)
	}
	for _, bad := range []string{"?category=gone"} {
		if r := h.get("/api/leads" + bad); r.StatusCode != 422 {
			t.Errorf("%s: %d", bad, r.StatusCode)
		}
	}
	id := itoa(int(page.Data.Rows[0].ID))
	if r := h.post("/api/leads/"+id+"/dismiss", nil); r.StatusCode != 200 || r.data()["state"] != discovery.LeadDismissed {
		t.Fatalf("dismiss: %d %s", r.StatusCode, r.body)
	}
	// A category's count is what its chip lists: the leads in the state shown.
	for _, st := range []string{discovery.LeadNew, discovery.LeadDismissed} {
		var p wire.Success[wire.LeadPage]
		strictInto(t, h.get("/api/leads?state="+st).body, &p)
		for c, n := range p.Data.Counts {
			if got := h.get("/api/leads?state=" + st + "&category=" + c).data()["total"]; got != float64(n) {
				t.Errorf("%s %s: counted %d, lists %v", st, c, n, got)
			}
		}
	}
	if r := h.post("/api/leads/9999/dismiss", nil); r.StatusCode != 404 {
		t.Errorf("unknown lead: %d", r.StatusCode)
	}
	// Restore is the undo of a dismissal: back to new, the reason cleared.
	if r := h.post("/api/leads/"+id+"/restore", nil); r.StatusCode != 200 || r.data()["state"] != discovery.LeadNew || r.data()["reason"] != nil {
		t.Fatalf("restore: %d %s", r.StatusCode, r.body)
	}
	if r := h.post("/api/leads/"+id+"/restore", nil); r.StatusCode != 409 {
		t.Errorf("restoring a new lead: %d %s", r.StatusCode, r.body)
	}
	if r := h.post("/api/leads/9999/restore", nil); r.StatusCode != 404 {
		t.Errorf("unknown lead: %d", r.StatusCode)
	}
	// Sorting by confidence puts the surest lead first, whatever its age.
	for i, c := range []float64{0.9, 0.5} {
		l := discovery.Lead{Source: "signals", Company: "Sure Corp " + itoa(i), Category: discovery.LeadNoATS, Confidence: c, Evidence: discovery.Evidence{URL: "https://news.example.com/s" + itoa(i), Text: "Hiring."}}
		if _, err := repo.LeadAdd(t.Context(), l); err != nil {
			t.Fatal(err)
		}
	}
	first := func(query string) string {
		var p wire.Success[wire.LeadPage]
		strictInto(t, h.get("/api/leads"+query).body, &p)
		return p.Data.Rows[0].Company
	}
	if got := first(""); got != "Sure Corp 1" {
		t.Errorf("newest first: %q", got)
	}
	if got := first("?sort=confidence"); got != "Sure Corp 0" {
		t.Errorf("by confidence: %q", got)
	}
	if r := h.get("/api/leads?sort=score"); r.StatusCode != 422 {
		t.Errorf("unknown sort: %d", r.StatusCode)
	}
}

// The search keeps a lead only when every word is in its company, poster,
// roles or evidence title, in any case and any order; body text is not
// searched, and the total counts the matches.
func TestLeadsSearchEveryWordOverCompanyPosterRolesAndTitle(t *testing.T) {
	h := newDomainHarness(t)
	repo := h.domainOf().repo
	for i, l := range []discovery.Lead{
		{Company: "Northwind Traders", Author: "Dana Poster", Roles: []string{"Staff Engineer"}, Evidence: discovery.Evidence{Text: "secret-body-word"}},
		{Company: "Contoso", Author: "Ravi Lee", Roles: []string{"Data Analyst"}, Evidence: discovery.Evidence{Title: "Hiring wave", Text: "Hiring."}},
		{Company: "Northwind Labs", Roles: []string{"Designer"}, Evidence: discovery.Evidence{Text: "Hiring."}},
	} {
		l.Source, l.Category = "signals", discovery.LeadNoATS
		l.Evidence.URL = "https://news.example.com/s" + itoa(i)
		if _, err := repo.LeadAdd(t.Context(), l); err != nil {
			t.Fatal(err)
		}
	}
	for _, tt := range []struct {
		q    string
		want []string
	}{
		{"NORTHWIND%20engineer", []string{"Northwind Traders"}},
		{"dana", []string{"Northwind Traders"}},
		{"WAVE", []string{"Contoso"}},
		{"northwind%20analyst", nil},
		{"secret-body-word", nil},
		{"north_ind", nil},
		{"", []string{"Contoso", "Northwind Labs", "Northwind Traders"}},
	} {
		var page wire.Success[wire.LeadPage]
		r := h.get("/api/leads?q=" + tt.q)
		strictInto(t, r.body, &page)
		var got []string
		for _, l := range page.Data.Rows {
			got = append(got, l.Company)
		}
		slices.Sort(got)
		if !slices.Equal(got, tt.want) || int(page.Data.Total) != len(tt.want) {
			t.Errorf("q=%q: rows %v total %d, want %v", tt.q, got, page.Data.Total, tt.want)
		}
	}
	// A search too long to be typed is refused, not run as a thousand-term AND
	// that SQLite's expression depth limit turns into a 500.
	if r := h.get("/api/leads?q=" + strings.Repeat("a%20", 2000)); r.StatusCode != 422 {
		t.Errorf("an over-long q: %d %.120s", r.StatusCode, r.body)
	}
}

func TestScheduledScanThatStopsProducingRaisesOneNeedsYouItemPerDay(t *testing.T) {
	h := newDomainHarness(t)
	d := h.domainOf()
	run := func(status string, inserted int) discovery.Record {
		rec, err := d.repo.ScanStart(t.Context(), "indeed", map[string]any{})
		if err != nil {
			t.Fatal(err)
		}
		rec.Status, rec.Counts.Inserted, rec.Counts.Discovered = status, inserted, inserted
		if rec, err = d.repo.ScanFinish(t.Context(), rec); err != nil {
			t.Fatal(err)
		}
		return rec
	}
	run(discovery.RunCompleted, 2)
	for i := 0; i < 2; i++ {
		h.srv.afterScheduledScan(t.Context(), d, run(discovery.RunNeedsSignIn, 0))
	}
	var inbox wire.Success[wire.NeedsYouInbox]
	strictInto(t, h.get("/api/needs-you").body, &inbox)
	n := 0
	for _, it := range inbox.Data.Items {
		// The source is named as the user knows it.
		if it.Entry.Title == "Scan Indeed stopped producing" {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("%d source alarms, want one per source per day", n)
	}
}
