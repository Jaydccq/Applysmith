package discovery

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"
)

func amazonJobs(n int) obj {
	var jobs []obj
	for i := 0; i < n; i++ {
		jobs = append(jobs, obj{"id_icims": strconv.Itoa(2000000 + i), "title": fmt.Sprintf("Software Engineer %d", i), "company_name": "Amazon",
			"job_path": fmt.Sprintf("/en/jobs/%d/software-engineer-%d", 2000000+i, i), "normalized_location": "Seattle, Washington, USA",
			"posted_date": "October  8, 2026", "description": "<p>Build services.</p>", "basic_qualifications": "BS in CS"})
	}
	return obj{"hits": n, "jobs": jobs}
}

func TestAmazon_SearchCappedAtOneHundredPerQuery(t *testing.T) {
	e, fs, _ := newFeedEnv(t)
	e.sc.Endpoints["amazon"] = "https://www.amazon.jobs"
	fs.handle = func(w http.ResponseWriter, r *http.Request, _ string) { writeJSON(w, amazonJobs(130)) }
	p := portalsWith(t, eng(), "amazon", "software engineer")
	rec := e.run(ScanRequest{Source: "amazon", Portals: p})
	reqs := fs.requests()
	if len(reqs) != 1 || reqs[0].query().Get("result_limit") != "100" || reqs[0].query().Get("base_query") != "software engineer" {
		t.Fatalf("requests = %+v", reqs)
	}
	if rec.Status != RunCompleted || rec.Counts.Discovered != 100 {
		t.Fatalf("record = %+v", rec)
	}
	found := false
	for _, row := range e.inbox(InboxQuery{Limit: 200}) {
		if row.URL == "https://www.amazon.jobs/jobs/2000000" && row.Company == "Amazon" {
			found = true
		}
	}
	if !found {
		t.Fatalf("no row for posting 2000000 among %d", len(e.inbox(InboxQuery{Limit: 200})))
	}
	// A result-size flag lowers the cap and replaces no saved search.
	e2, fs2, _ := newFeedEnv(t)
	e2.sc.Endpoints["amazon"] = "https://www.amazon.jobs"
	fs2.handle = func(w http.ResponseWriter, r *http.Request, _ string) { writeJSON(w, amazonJobs(130)) }
	p = portalsWith(t, eng(), "amazon", "software engineer", "data engineer")
	e2.run(ScanRequest{Source: "amazon", Portals: p, Limit: 10})
	if got := fs2.requests(); len(got) != 2 || got[0].query().Get("result_limit") != "10" {
		t.Fatalf("requests = %+v", got)
	}
}

func a16zFixture(page int, done bool, rows ...obj) obj {
	return obj{"jobs": rows, "total": 2, "page": page, "perPage": 25, "isDone": done}
}
func a16zJob(id string) obj {
	return obj{"id": id, "title": "Software Engineer " + id, "apply_url": "https://jobs.lever.co/example-co/5d1c0a9e-0000-4000-8000-00000000000" + id, "company_name": "Example Co", "location": "Austin, TX", "posted_at": ago(2 * time.Hour).Format(time.RFC3339)}
}
func a16zHTML(doc obj) string {
	data, _ := json.Marshal(doc)
	payload, _ := json.Marshal(`1:["$","Component",null,{"initialData":` + string(data) + `}]` + "\n")
	return `<script src="/_next/static/chunks/jobs.js"></script><script>self.__next_f.push([1,` + string(payload) + `])</script>`
}
func TestA16Z_TenSecondTimeoutPerRequest(t *testing.T) {
	e, fs, rt := newFeedEnv(t)
	e.sc.Endpoints["a16z"] = "https://jobs.a16z.example.test"
	action := strings.Repeat("a", 42)
	fs.handle = func(w http.ResponseWriter, r *http.Request, body string) {
		switch {
		case strings.Contains(r.URL.Path, "api-boards"):
			t.Errorf("used removed API")
			http.Error(w, "removed API", http.StatusInternalServerError)
		case r.URL.Path == "/_next/static/chunks/jobs.js":
			fmt.Fprintf(w, `let d=(0,c.createServerReference)("%s",c.callServer,void 0,c.findSourceMapURL,"loadMorePublicJobs");`, action)
		case r.Method == "POST":
			if r.Header.Get("Next-Action") != action || !strings.Contains(body, `"page":1`) {
				t.Errorf("pagination request: %v %s", r.Header, body)
			}
			data, _ := json.Marshal(a16zFixture(1, true, a16zJob("1"), a16zJob("2")))
			fmt.Fprintf(w, "0:{}\n1:%s\n", data)
		default:
			fmt.Fprint(w, a16zHTML(a16zFixture(0, false, a16zJob("1"))))
		}
	}
	rec := e.run(ScanRequest{Source: "a16z", Portals: eng(), Pages: 2})
	if rec.Status != RunCompleted || rec.Counts.Inserted != 2 || rec.Counts.Discovered != 2 {
		t.Fatalf("%+v", rec)
	}
	if left := rt.deadline("/"); left <= 9*time.Second || left > 10*time.Second {
		t.Fatalf("deadline %s", left)
	}
	if len(fs.requests()) != 3 {
		t.Fatalf("requests %+v", fs.requests())
	}
	if got := e.inbox(InboxQuery{})[0]; !strings.HasPrefix(got.IdentityKey, "lever:example-co/") {
		t.Fatalf("identity %s", got.IdentityKey)
	}
}
func TestA16Z_LayoutErrorsAndEmptyResults(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		bad        bool
	}{
		{"changed page", "<html>new layout</html>", true},
		{"missing rows", `1:{"jobs":[],"total":20,"isDone":false}`, true},
		{"empty", `1:{"jobs":[],"total":0,"isDone":true}`, false},
		{"old API", `{"jobs":[{"jobId":"1"}],"meta":{"sequence":"x"}}`, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := parseA16Z(Response{Status: 200, Body: []byte(tc.body)})
			if (err != nil) != tc.bad {
				t.Fatalf("error %v", err)
			}
		})
	}
}

func TestHTTPSources_OneQueryFailureDoesNotStopOthers(t *testing.T) {
	e, fs, _ := newFeedEnv(t)
	e.sc.Endpoints["amazon"] = "https://www.amazon.jobs"
	fs.handle = func(w http.ResponseWriter, r *http.Request, _ string) {
		if r.URL.Query().Get("base_query") == "broken" {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		writeJSON(w, amazonJobs(3))
	}
	p := portalsWith(t, eng(), "amazon", "broken", "software engineer")
	rec := e.run(ScanRequest{Source: "amazon", Portals: p})
	if rec.Status != RunCompleted || rec.Counts.Inserted != 3 || rec.Counts.BoardErrors != 1 || len(rec.Errors) != 1 {
		t.Fatalf("record = %+v", rec)
	}
	// Every query failing is a failed run.
	p = portalsWith(t, eng(), "amazon", "broken")
	rec = e.run(ScanRequest{Source: "amazon", Portals: p})
	if rec.Status != RunFailed {
		t.Fatalf("status %s", rec.Status)
	}
}

func TestHTTPSources_ResponseWithoutRowsIsLayoutChanged(t *testing.T) {
	e, fs, _ := newFeedEnv(t)
	e.sc.Endpoints["amazon"] = "https://www.amazon.jobs"
	fs.handle = func(w http.ResponseWriter, r *http.Request, _ string) { writeJSON(w, obj{"hits": 40, "jobs": []obj{}}) }
	p := portalsWith(t, eng(), "amazon", "software engineer")
	rec := e.run(ScanRequest{Source: "amazon", Portals: p})
	if rec.Status != RunFailed || rec.Reason != ReasonLayoutChanged {
		t.Fatalf("status %s reason %q", rec.Status, rec.Reason)
	}
	// A valid answer with no hits is a quiet market, not a broken source.
	fs.handle = func(w http.ResponseWriter, r *http.Request, _ string) { writeJSON(w, obj{"hits": 0, "jobs": []obj{}}) }
	if rec = e.run(ScanRequest{Source: "amazon", Portals: p}); rec.Status != RunCompleted {
		t.Fatalf("status %s", rec.Status)
	}
}

// The response has already arrived before its close hook ends the caller.
// This distinguishes an observed query failure from a request cancelled before
// a response, without depending on timer ordering in an HTTP handler.
type scanCloseTransport func(*http.Request) (*http.Response, error)

func (f scanCloseTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type scanCloseBody struct {
	*strings.Reader
	end func()
}

func (b scanCloseBody) Close() error { b.end(); return nil }

func TestAmazon_RecordedFailureSurvivesCancellation(t *testing.T) {
	for _, phase := range []string{"user", "budget"} {
		for _, response := range []string{"400", "layout"} {
			t.Run(phase+"/"+response, func(t *testing.T) {
				e := newEnv(t)
				ctx, cancel := context.WithCancelCause(e.ctx)
				defer cancel(nil)
				fire := e.budgetOnCue()
				e.sc.Client.HTTP = &http.Client{Transport: scanCloseTransport(func(req *http.Request) (*http.Response, error) {
					end := func() { cancel(ErrCancelled) }
					if phase == "budget" {
						end = func() { fire(); <-req.Context().Done() }
					}
					status, body := http.StatusBadRequest, "bad query"
					if response == "layout" {
						status, body = http.StatusOK, `{"hits":40,"jobs":[]}`
					}
					return &http.Response{StatusCode: status, Header: http.Header{}, Request: req, Body: scanCloseBody{strings.NewReader(body), end}}, nil
				})}
				rec, err := e.sc.Run(ctx, ScanRequest{Source: "amazon", Portals: portalsWith(t, eng(), "amazon", "engineer")})
				if err != nil || rec.Status != RunFailed {
					t.Fatalf("observed source failure hidden: rec=%+v err=%v", rec, err)
				}
				if response == "400" && !strings.Contains(rec.Error, "400") {
					t.Fatalf("query failure lost: %+v", rec)
				}
				if response == "layout" && rec.Reason != ReasonLayoutChanged {
					t.Fatalf("layout failure lost: %+v", rec)
				}
			})
		}
	}
}

func TestA16Z_LocationRestatementsCollapse(t *testing.T) {
	for in, want := range map[string]string{
		"San Francisco · San Francisco, California, United States":                       "San Francisco, California, United States",
		"Los Angeles, CA · Torrance, CA, United States":                                  "Los Angeles, CA; Torrance, CA, United States",
		"Washington, DC · Washington, District of Columbia, United States · US - Remote": "Washington, District of Columbia, United States; US - Remote",
		"Portland, OR · Portland, Maine, United States":                                  "Portland, OR; Portland, Maine, United States",
		"New York City · New York, New York, United States":                              "New York, New York, United States",
	} {
		job := a16zJob("1")
		job["location"] = in
		page, err := parseA16Z(Response{Status: 200, Body: []byte(a16zHTML(a16zFixture(0, true, job)))})
		if err != nil {
			t.Fatal(err)
		}
		if got := page.postings()[0].Location; got != want {
			t.Errorf("location %q = %q, want %q", in, got, want)
		}
	}
}
