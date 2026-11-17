package discovery

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestTheirStack_UnfilteredSearchRefusedBeforeNetwork(t *testing.T) {
	e, fs, _ := newFeedEnv(t)
	e.withKeys(map[string]string{"api-key-theirstack": "ts-test-key-0001"})
	rec := e.run(ScanRequest{Source: "theirstack", Portals: eng()})
	if rec.Status != RunFailed || len(fs.requests()) != 0 || !strings.Contains(rec.Error, "company_domain") {
		t.Fatalf("record = %+v, %d requests", rec, len(fs.requests()))
	}
}

func TestTheirStack_SearchCarriesAgeLimitAndTimeout(t *testing.T) {
	e, fs, rt := newFeedEnv(t)
	e.sc.Endpoints["theirstack"] = "https://api.theirstack.example.test"
	e.withKeys(map[string]string{"api-key-theirstack": "ts-test-key-0001"})
	fs.handle = func(w http.ResponseWriter, r *http.Request, _ string) {
		writeJSON(w, obj{"data": []obj{{"id": 11, "job_title": "Software Engineer", "url": "https://jobs.example.test/t/11", "date_posted": "2026-10-10",
			"company": "Example Co", "location": "Austin, TX", "description": "Build."}}})
	}
	rec := e.run(ScanRequest{Source: "theirstack", Portals: eng(), Window: 72 * time.Hour})
	var body map[string]any
	_ = json.Unmarshal([]byte(fs.requests()[0].Body), &body)
	if body["posted_at_max_age_days"] != float64(3) || body["limit"] != float64(25) || rec.Counts.Inserted != 1 {
		t.Fatalf("body = %v, record = %+v", body, rec)
	}
	if auth := fs.requests()[0].Header.Get("Authorization"); auth != "Bearer ts-test-key-0001" {
		t.Fatalf("authorization = %q", auth)
	}
	if left := rt.deadline("/v1/jobs/search"); left <= 9*time.Second || left > 10*time.Second {
		t.Fatalf("deadline = %s", left)
	}
}

func TestKeyedSources_KeyNeverInRunRecordOrError(t *testing.T) {
	for _, source := range []string{"theirstack", "exa"} {
		t.Run(source, func(t *testing.T) {
			e, fs, _ := newFeedEnv(t)
			key := "secret-key-" + source
			e.withKeys(map[string]string{"api-key-" + source: key})
			// The vendor refuses the call and echoes the credential back.
			fs.handle = func(w http.ResponseWriter, r *http.Request, body string) {
				w.WriteHeader(http.StatusUnauthorized)
				_, _ = w.Write([]byte("bad credential " + key + " " + r.Header.Get("Authorization") + r.Header.Get("X-Api-Key")))
			}
			p := portalsWith(t, eng(), source, map[string]any{"company_name": "Example Co"})
			p.Filters.Title.Positive = []string{"engineer"}
			rec := e.run(ScanRequest{Source: source, Portals: p, Window: 24 * time.Hour})
			saved, err := e.rp.ScanGet(e.ctx, rec.ID)
			if err != nil {
				t.Fatal(err)
			}
			b, _ := json.Marshal([]Record{rec, saved})
			if strings.Contains(string(b), key) || rec.Status != RunFailed {
				t.Fatalf("status %s, record = %s", rec.Status, b)
			}
		})
	}
}
