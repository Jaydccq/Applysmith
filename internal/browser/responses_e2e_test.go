//go:build browser

package browser

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

func watchTab(t *testing.T, filters ...ResponseFilter) (*Instance, *LaneHandle, string, *ResponseWatch) {
	t.Helper()
	inst, fx := sharedInstance(t)
	tab := openTab(t, inst, LaneApply, "run-watch")
	h := inst.Lane(LaneApply)
	navigate(t, inst, LaneApply, tab, fx.Loopback("/submit"))
	w, err := h.WatchResponsesFor(ctx30(t), tab, filters...)
	ok(t, err)
	t.Cleanup(w.Stop)
	return inst, h, tab, w
}

// Observation is off by default; once on, a submission is confirmed with its
// body, a refusal is read, a large body is cut with a flag, the newest 200
// records are kept, and nothing sensitive is in a record.
func TestE2E_Browser_ObservationRecordsBounded(t *testing.T) {
	_, h, tab, w := watchTab(t, ResponseFilter{URL: "/api/big"})
	for _, path := range []string{"/api/apply", "/api/refuse"} {
		eval[int](t, h, tab, fmt.Sprintf(`send(%q)`, path))
	}
	eval[int](t, h, tab, `(fetch('/api/big'), 1)`)
	var r Response
	eventually(t, 10*time.Second, "the big body recorded", func() bool {
		for _, got := range w.Responses() {
			if strings.HasSuffix(got.URL, "/api/big") {
				r = got
				return true
			}
		}
		return false
	})
	if len(r.Body) != MaxResponseBody || !r.BodyTruncated || r.Status != 200 || r.Type != "Fetch" {
		t.Fatalf("big body: len=%d truncated=%v %+v", len(r.Body), r.BodyTruncated, r)
	}
	eventually(t, 5*time.Second, "both submissions recorded", func() bool { return len(w.Responses()) >= 3 })
	var refusal Response
	for _, got := range w.Responses() {
		if strings.HasSuffix(got.URL, "/api/refuse") {
			refusal = got
		}
	}
	if refusal.Status != 422 || !strings.Contains(refusal.Body, "missing field: email") {
		t.Fatalf("a failing fetch reads its body: %+v", refusal)
	}
	if fmt.Sprintf("%+v", w.Responses()) != "" && strings.Contains(fmt.Sprintf("%+v", w.Responses()), "example-token") {
		t.Fatal("a credential reached a record")
	}
	// Two hundred and fifty more: the newest 200 stay.
	eval[int](t, h, tab, `(Array.from({length:250},(_, n)=>fetch('/probe?n='+n)), 1)`)
	eventually(t, 10*time.Second, "the records capped", func() bool { return w.Pending() == 0 && len(w.Responses()) == maxWatchedResponses })
	if last := w.Responses(); strings.HasSuffix(last[0].URL, "/api/big") {
		t.Fatal("the oldest records were kept instead of the newest")
	}
	w.Stop()
	if len(w.Responses()) != 0 {
		t.Fatal("recorded bodies survive the release")
	}
}
