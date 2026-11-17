package apply

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"applysmith/internal/browser"
	"applysmith/internal/engine"
	"applysmith/internal/gates"
)

// peakCounter records the most calls in flight at once. With expect set,
// hold keeps a call waiting until that many are in flight together (or a
// generous bound passes), so the peak does not hang on scheduling.
type peakCounter struct {
	mu        sync.Mutex
	now, peak int
	total     int
	expect    int
	reached   chan struct{}
}

func (p *peakCounter) enter() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.now++
	p.total++
	p.peak = max(p.peak, p.now)
	if p.expect > 0 && p.peak >= p.expect && p.reached != nil {
		close(p.reached)
		p.reached = nil
	}
}

// await sets the in-flight count hold waits for.
func (p *peakCounter) await(n int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.expect, p.reached = n, make(chan struct{})
}

func (p *peakCounter) hold(ctx context.Context) {
	p.mu.Lock()
	ch := p.reached
	p.mu.Unlock()
	if ch == nil {
		return
	}
	select {
	case <-ch:
	case <-time.After(5 * time.Second):
	case <-ctx.Done():
	}
}

func (p *peakCounter) leave() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.now--
}

func (p *peakCounter) stats() (peak, total int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.peak, p.total
}

// recorder keeps the precheck stops in the order they were written.
type recorder struct {
	mu    sync.Mutex
	stops []Precheck
	err   error
}

func (r *recorder) RecordPrecheck(_ context.Context, p Precheck) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.stops = append(r.stops, p)
	return r.err
}

func (r *recorder) urls() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []string
	for _, s := range r.stops {
		out = append(out, s.URL)
	}
	return out
}

func mirrorRows(n int) []Candidate {
	var out []Candidate
	for i := 1; i <= n; i++ {
		out = append(out, lsCand(fmt.Sprintf("https://jobright.ai/jobs/info/%d", i), fmt.Sprintf("Company %c", 'A'+i), fmt.Sprintf("Role %c", 'A'+i)))
	}
	return out
}

func unresolved(c Candidate) Resolution {
	return Resolution{Reason: "aggregator-only: no employer posting found for " + c.URL}
}

func TestApplyList_Prefetch_ResolutionsAndProbePages(t *testing.T) {
	t.Run("only the first limit mirror rows are resolved, never a skip-listed company's", func(t *testing.T) {
		var mu sync.Mutex
		var asked []string
		sel := NewSelector(lsEvidence{})
		sel.Resolve = func(_ context.Context, c Candidate) (Resolution, error) {
			mu.Lock()
			asked = append(asked, c.Company+"/"+c.Role)
			mu.Unlock()
			return Resolution{URL: "https://boards.greenhouse.io/x/jobs/" + c.Company[len(c.Company)-1:]}, nil
		}
		rows := mirrorRows(6)
		res, err := sel.Select(context.Background(), rows, lsOpts(func(o *SelectOptions) { o.Limit = 2; o.SkipList = []string{rows[0].Company} }))
		if err != nil {
			t.Fatal(err)
		}
		mu.Lock()
		defer mu.Unlock()
		if len(asked) != 2 || strings.Contains(strings.Join(asked, ","), rows[0].Company) {
			t.Fatalf("resolved %v: two rows, none of the skip-listed company", asked)
		}
		if len(res.Rows) != 2 || res.Rows[0].EmployerURL == "" || res.Rows[0].URL != rows[1].URL {
			t.Fatalf("%+v", res.Rows)
		}
	})

	t.Run("a row an earlier rule skipped writes no precheck record, though it was fetched ahead", func(t *testing.T) {
		rows := mirrorRows(3)
		rec := &recorder{}
		sel := NewSelector(lsEvidence{f: Facts{Ledger: []LedgerFact{{ID: 1, URL: rows[1].URL, IdentityKey: postingKey(rows[1].URL), State: gates.StateSubmitted, At: t0}}}})
		sel.Record = rec
		sel.Resolve = func(_ context.Context, c Candidate) (Resolution, error) { return unresolved(c), nil }
		res, err := sel.Select(context.Background(), rows, lsOpts(nil))
		if err != nil {
			t.Fatal(err)
		}
		if got := rec.urls(); len(got) != 2 || got[0] != rows[0].URL || got[1] != rows[2].URL {
			t.Fatalf("records %v", got)
		}
		if rule := func() string { s, _ := skippedBy(res, rows[1].URL); return s.Rule }(); rule != RuleApplied {
			t.Fatalf("rule %q", rule)
		}
	})

	t.Run("the resolver gets the row's company and role", func(t *testing.T) {
		var mu sync.Mutex
		var got []string
		sel := NewSelector(lsEvidence{})
		sel.Resolve = func(_ context.Context, c Candidate) (Resolution, error) {
			mu.Lock()
			got = append(got, c.Company+"|"+c.Role)
			mu.Unlock()
			return Resolution{URL: c.URL}, nil
		}
		if _, err := sel.Select(context.Background(), mirrorRows(2), lsOpts(nil)); err != nil {
			t.Fatal(err)
		}
		mu.Lock()
		defer mu.Unlock()
		slices.Sort(got)
		if strings.Join(got, ",") != "Company B|Role B,Company C|Role C" {
			t.Fatalf("resolver got %v, want each row's company and role", got)
		}
	})

	t.Run("a failed resolution surfaces when its row is reached, and only then", func(t *testing.T) {
		boom := errors.New("resolver exploded")
		sel := NewSelector(lsEvidence{})
		sel.Resolve = func(_ context.Context, c Candidate) (Resolution, error) {
			if strings.HasSuffix(c.URL, "/2") {
				return Resolution{}, boom
			}
			return unresolved(c), nil
		}
		if _, err := sel.Select(context.Background(), mirrorRows(3), lsOpts(nil)); !errors.Is(err, boom) {
			t.Fatalf("err = %v", err)
		}
		// Two employer-side rows fill the limit before the walk reaches the
		// failing mirror, which was fetched ahead.
		rows := []Candidate{
			lsCand("https://boards.greenhouse.io/a/jobs/1", "Acme One", "R1"),
			lsCand("https://boards.greenhouse.io/b/jobs/2", "Acme Two", "R2"),
			lsCand("https://jobright.ai/jobs/info/3", "Acme Three", "R3"),
		}
		sel.Resolve = func(context.Context, Candidate) (Resolution, error) { return Resolution{}, boom }
		res, err := sel.Select(context.Background(), rows, lsOpts(func(o *SelectOptions) { o.Limit = 2 }))
		if err != nil || len(res.Rows) != 2 {
			t.Fatalf("%+v %v", res, err)
		}
	})

	t.Run("probe pages load three at a time", func(t *testing.T) {
		w := newProbeWindow()
		w.pc.await(3)
		var targets []ProbeTarget
		for i := 0; i < 9; i++ {
			u := fmt.Sprintf("https://careers.example.test/jobs/%d", i)
			w.pages[u] = probePage{state: listPostingPage("Data Engineer"), delay: 15 * time.Millisecond}
			targets = append(targets, ProbeTarget{URL: u, Company: "Example Corp", Role: "Data Engineer"})
		}
		verdicts, err := LivenessProber{Window: w, Settle: -1}.Probe(context.Background(), targets)
		if err != nil {
			t.Fatal(err)
		}
		if peak, total := w.pc.stats(); peak != 3 || total != 9 {
			t.Fatalf("peak %d pages, %d opened; want 3 at a time and 9", peak, total)
		}
		for i, v := range verdicts {
			if v.Result != ProbeLive {
				t.Fatalf("verdict %d = %+v", i, v)
			}
		}
		if w.openNow() != 0 {
			t.Fatalf("%d tabs left open", w.openNow())
		}
	})
}

// probePage scripts what one URL answers.
type probePage struct {
	status  int
	navErr  error
	state   *PageState
	evalErr error
	delay   time.Duration
	block   bool
}

type probeWindow struct {
	mu    sync.Mutex
	pages map[string]probePage
	pc    peakCounter
	open  int
}

func newProbeWindow() *probeWindow { return &probeWindow{pages: map[string]probePage{}} }

func (w *probeWindow) openNow() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.open
}

func (w *probeWindow) OpenTab(context.Context, string, engine.Host) (Tab, error) {
	w.pc.enter()
	w.mu.Lock()
	w.open++
	w.mu.Unlock()
	return &probeTab{w: w}, nil
}

type probeTab struct {
	Tab
	w    *probeWindow
	page probePage
}

func (t *probeTab) NavigateDOM(ctx context.Context, url string) (browser.NavResult, error) {
	t.w.mu.Lock()
	t.page = t.w.pages[url]
	t.w.mu.Unlock()
	if t.page.block {
		<-ctx.Done()
		return browser.NavResult{}, ctx.Err()
	}
	t.w.pc.hold(ctx)
	select {
	case <-time.After(t.page.delay):
	case <-ctx.Done():
		return browser.NavResult{}, ctx.Err()
	}
	if t.page.navErr != nil {
		return browser.NavResult{}, t.page.navErr
	}
	return browser.NavResult{Status: orInt(t.page.status, 200), URL: url}, nil
}

func (t *probeTab) Eval(_ context.Context, _ string, out any) error {
	if t.page.evalErr != nil {
		return t.page.evalErr
	}
	return setJSON(out, t.page.state)
}

func (t *probeTab) Close(context.Context) error {
	t.w.pc.leave()
	t.w.mu.Lock()
	t.w.open--
	t.w.mu.Unlock()
	return nil
}

// postingPage is a page that reads as the posting of a role.
func listPostingPage(role string) *PageState {
	return &PageState{Title: role + " - Example Corp", Headings: []string{role}, Lines: []string{role, "Apply now"}}
}

// Inventory is the engine's field read; the fake pages have no fields.
func (t *probeTab) Inventory(context.Context, string, []FormInfo) (Inventory, error) {
	return Inventory{}, nil
}
