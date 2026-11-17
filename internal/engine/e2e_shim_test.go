//go:build browser

package engine

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"
)

// atsPages are pages tests outside this package ask the fixture server to
// serve, by path.
var (
	atsMu    sync.Mutex
	atsPages = map[string]string{}
)

func atsPage(path string) (string, bool) {
	atsMu.Lock()
	defer atsMu.Unlock()
	body, ok := atsPages[path]
	return body, ok
}

// ATSHostRules maps the hosts of the named application systems to the fixture
// server, so a synthetic page can stand at a URL the site table knows.
const ATSHostRules = "MAP *.myworkdayjobs.com 127.0.0.1, MAP *.oraclecloud.com 127.0.0.1, MAP *.successfactors.com 127.0.0.1, MAP *.taleo.net 127.0.0.1, " +
	"MAP *.icims.com 127.0.0.1, MAP workforcenow.adp.com 127.0.0.1, MAP recruiting.adp.com 127.0.0.1, MAP jobs.dayforcehcm.com 127.0.0.1, " +
	"MAP jobs.ashbyhq.com 127.0.0.1, MAP job-boards.greenhouse.io 127.0.0.1, MAP jobs.lever.co 127.0.0.1, MAP ats.rippling.com 127.0.0.1, " +
	"MAP apply.workable.com 127.0.0.1, MAP *.breezy.hr 127.0.0.1, MAP *.applytojob.com 127.0.0.1, MAP *.bamboohr.com 127.0.0.1, " +
	"MAP jobs.jobvite.com 127.0.0.1, MAP jobs.smartrecruiters.com 127.0.0.1, MAP recruiting.paylocity.com 127.0.0.1, MAP www.linkedin.com 127.0.0.1"

// E2E is the browser harness for the suites that live outside this package.
type E2E struct {
	f *tabFixture
	t *testing.T
}

func NewE2E(t *testing.T) *E2E {
	return &E2E{f: openFixture(t), t: t}
}

// Serve registers a page body under a path on the fixture server.
func (e *E2E) Serve(path, body string) {
	atsMu.Lock()
	atsPages[path] = body
	atsMu.Unlock()
	e.t.Cleanup(func() {
		atsMu.Lock()
		delete(atsPages, path)
		atsMu.Unlock()
	})
}

// URL is a URL on the fixture server under the given host name.
func (e *E2E) URL(host, path string) string {
	return "https://" + host + ":" + itoa(port(e.f.fx.c)) + path
}

func (e *E2E) Navigate(url string) { e.f.navigate(url) }

func (e *E2E) Page(h Host) *Page { return e.f.page(h) }

// Fill runs one page fill under a bounded context.
func (e *E2E) Fill(p *Page, o FillOptions) (*PageResult, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	return p.Fill(ctx, o)
}

// MainEval evaluates an expression in the top frame's main world and decodes the JSON result.
func (e *E2E) MainEval(expr string, out any) {
	e.t.Helper()
	e.f.mainEval(e.f.frames()["top"].ID, expr, out)
}

// EngineLog is the last fill's diagnostic log of the top frame.
func (e *E2E) EngineLog() []string {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	raw, err := e.f.hooks.Eval(ctx, e.f.frames()["top"].ID, World, `__applysmithEngine.log()`)
	var out []string
	if err == nil {
		_ = json.Unmarshal(raw, &out)
	}
	return out
}

// Observe installs the submit observer and reads what it saw.
func (e *E2E) Observe(p *Page) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := p.ObserveSubmissions(ctx); err != nil {
		e.t.Fatal(err)
	}
}

func (e *E2E) Observations(p *Page) []SubmitObservation {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	out, err := p.SubmitObservations(ctx, 2*time.Minute)
	if err != nil {
		e.t.Fatal(err)
	}
	return out
}

func itoa(n int) string {
	b, _ := json.Marshal(n)
	return string(b)
}
