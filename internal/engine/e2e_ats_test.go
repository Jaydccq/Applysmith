//go:build browser

package engine_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"applysmith/internal/answerbank"
	"applysmith/internal/answerbank/bind"
	"applysmith/internal/engine"
	"applysmith/internal/store"
	"applysmith/internal/workspace"
)

// The suites below fill one synthetic form per named application system, in
// the managed Chrome, through the Go binding: the matcher is the real one with
// the default answer bank and the fictional profile below, so what is filled
// is what the product would fill. The pages are small hand-written HTML with
// fictional content; each reproduces the markup and the timing of one system's
// widget, not its look.

const alexProfile = `version: 1
candidate:
  full_name: Alex Example
  email: alex@example.com
  phone: "+1 555 0100"
  address:
    line1: 1 Example Street
    city: Durham
    county: Durham County
    region: NC
    postal_code: "27701"
    country: US
work_authorization:
  authorized_countries: [US]
  requires_sponsorship_now: false
  requires_sponsorship_future: false
  visa_status: Citizen
history:
  employment:
    - employer: Example Labs
      title: Staff Engineer
      start: "2021-03"
      end: present
      location: Durham, NC
    - employer: Sample Works
      title: Engineer
      start: "2017-06"
      end: "2021-02"
  education:
    - school: Example University
      degree: BS
      field: Computer Science
      start: "2013-09"
      end: "2017-05"
`

// applicant is the snapshot the engine receives (GET /api/autofill/applicant).
const applicant = `{"candidate":{"fullName":"Alex Example","firstName":"Alex","lastName":"Example","email":"alex@example.com","phone":"+1 555 0100","phoneCountryCode":"+1","phoneNational":"5550100",
"address":{"line1":"1 Example Street","city":"Durham","state":"NC","postalCode":"27701","country":"United States"}},
"links":{"linkedin":"https://www.linkedin.com/in/alex-example"},
"history":{"employment":[{"employer":"Example Labs","title":"Staff Engineer","location":"Durham","country":"United States","startDate":"2021-03-01","current":true},
{"employer":"Sample Works","title":"Engineer","location":"Raleigh","startDate":"2017-06-01","endDate":"2021-02-01"}],
"education":[{"school":"Example University","degree":"Bachelor of Science","field":"Computer Science","startDate":"2013-09-01","endDate":"2017-05-01"}]}}`

// matcher builds the real binding over a temporary workspace holding the
// fictional profile; the default bank answers its identity entries from that
// profile at load.
func matcher(t *testing.T) *bind.Binding {
	t.Helper()
	ws := workspace.Open(t.TempDir())
	if err := os.MkdirAll(ws.StateDir(), 0o700); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(context.Background(), filepath.Join(ws.StateDir(), store.DBFile), store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	bank := answerbank.New(ws, st)
	if _, err := bank.Seed(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(ws.Path(workspace.FileProfile), []byte(alexProfile), 0o600); err != nil {
		t.Fatal(err)
	}
	return bind.New(bind.Deps{Bank: bank, Workspace: ws, Now: func() time.Time { return time.Date(2031, 3, 4, 10, 0, 0, 0, time.UTC) }})
}

type resumes struct{ payload engine.ResumePayload }

func newResumes(t *testing.T) *resumes {
	t.Helper()
	data, err := os.ReadFile("testdata/resume-alex-example.pdf")
	if err != nil {
		t.Fatal(err)
	}
	return &resumes{payload: engine.ResumePayload{FileName: "resume-alex-example.pdf", MediaType: "application/pdf", ContentBase64: base64.StdEncoding.EncodeToString(data)}}
}

func (r *resumes) Resume(_ context.Context, req engine.ResumeRequest) (*engine.ResumePayload, error) {
	if req.Document != "resume" {
		return nil, nil
	}
	p := r.payload
	return &p, nil
}

func applicantFn(context.Context) (json.RawMessage, error) { return json.RawMessage(applicant), nil }

// fastTiming keeps the protocol's clocks short; the page's own timing is real.
func fastTiming() engine.Timing {
	return engine.Timing{Poll: 150 * time.Millisecond, PollTimeout: 20 * time.Second, PageDeadline: 80 * time.Second, HungGrace: 5 * time.Second, DocumentWait: 2 * time.Second, ApplicantRetry: 100 * time.Millisecond}
}

// atsCase is one named system: the page, where it stands, and what must read back.
type atsCase struct {
	name string
	host string
	path string
	body string
	// helper installs the main-world helper for the fill.
	helper bool
	// want maps a row label to the value it must read back as, filled.
	want map[string]string
	// check runs after the fill, against the page.
	check func(t *testing.T, e *engine.E2E, res *engine.PageResult)
}

func runATS(t *testing.T, c atsCase) {
	t.Helper()
	e := engine.NewE2E(t)
	e.Serve(c.path, c.body)
	p := e.Page(engine.Host{Matcher: matcher(t), Resumes: newResumes(t)})
	e.Navigate(e.URL(c.host, c.path))
	res, err := e.Fill(p, engine.FillOptions{
		Applicant:   applicantFn,
		Posting:     engine.PostingContext{Company: "Example Corp", Title: "Engineer"},
		Timing:      fastTiming(),
		MainWorld:   c.helper,
		OnUnreached: func(d engine.FrameDecision) { t.Errorf("frame unreached: %+v", d) },
	})
	if err != nil {
		t.Fatalf("%s: %v", c.name, err)
	}
	if res.Top.State != engine.StateDone || res.Top.Summary == nil {
		t.Fatalf("%s: top %+v", c.name, res.Top)
	}
	rows := map[string]engine.Row{}
	for _, s := range summaries(res) {
		for _, r := range s.Rows {
			rows[r.Label] = r
		}
	}
	defer func() {
		if t.Failed() {
			for _, r := range rows {
				t.Logf("ROW %s | %s %s/%s %q lane=%s", r.Label, r.Outcome, r.Reason, r.MatcherReason, r.Value, r.Lane)
			}
			for _, l := range e.EngineLog() {
				t.Log(l)
			}
		}
	}()
	for label, want := range c.want {
		r, ok := rows[label]
		if !ok || r.Outcome != "filled" || r.Value != want {
			t.Errorf("%s: row %q = %+v, want filled %q", c.name, label, r, want)
		}
	}
	if c.check != nil {
		c.check(t, e, res)
	}
}

func summaries(res *engine.PageResult) []*engine.Summary {
	var out []*engine.Summary
	if res.Top.Summary != nil {
		out = append(out, res.Top.Summary)
	}
	for _, f := range res.Frames {
		if f.Status != nil && f.Status.Summary != nil {
			out = append(out, f.Status.Summary)
		}
	}
	return out
}

// val reads a control's value from the page.
func val(t *testing.T, e *engine.E2E, id string) string {
	t.Helper()
	var v string
	e.MainEval(`(()=>{const e=document.getElementById(`+jsq(id)+`);return e?e.value:null})()`, &v)
	return v
}

func jsq(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}
