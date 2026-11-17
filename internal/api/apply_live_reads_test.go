package api

import (
	"context"
	"database/sql"
	"errors"
	"sync"
	"testing"
	"time"

	"applysmith/internal/agent"
	"applysmith/internal/api/wire"
	"applysmith/internal/apply"
	"applysmith/internal/gates"
)

// readBound is how long a status route may take while a run works: a read
// answers from committed rows and memory, while a read that waits on the
// browser or the held connections would never answer before the test releases
// them, so a bound this loose still tells the two apart on a loaded machine.
const readBound = 5 * time.Second

// gateLane is an apply lane whose tab listing waits until the gate opens, like
// a browser that is busy loading a page.
type gateLane struct {
	showLane
	gate  chan struct{}
	asked chan struct{}
	once  sync.Once
}

func (l *gateLane) LiveTabs(ctx context.Context) ([]string, error) {
	l.once.Do(func() { close(l.asked) })
	<-l.gate
	return l.showLane.LiveTabs(ctx)
}

type gateTab struct {
	apply.Tab
	id   string
	lane *gateLane
}

func (t gateTab) ID() string                  { return t.id }
func (t gateTab) Handle() apply.TabLane       { return t.lane }
func (t gateTab) Close(context.Context) error { return nil }

// getWithin reads path and fails when the answer takes longer than readBound.
// On a timeout it calls release and waits for the read, so no request outlives
// the test.
func getWithin(t *testing.T, h *harness, path string, release func()) (result, bool) {
	t.Helper()
	got := make(chan result, 1)
	go func() { got <- h.get(path) }()
	select {
	case r := <-got:
		return r, true
	case <-time.After(readBound):
		t.Errorf("GET %s did not answer within %v while a run worked", path, readBound)
		release()
		<-got
		return result{}, false
	}
}

func TestApplyRunView_AnswersWhileTheRunAndItsKeeperWaitOnTheBrowser(t *testing.T) {
	hold := make(chan struct{})
	h := newDomainHarness(t, func(c *Config) {
		c.ApplyWindow = func(ctx context.Context) (apply.Window, error) {
			select {
			case <-hold:
			case <-ctx.Done():
			}
			return nil, errors.New("the test browser is down")
		}
	})
	lane := &gateLane{gate: make(chan struct{}), asked: make(chan struct{})}
	var once sync.Once
	release := func() { once.Do(func() { close(hold); close(lane.gate) }) }
	t.Cleanup(release)

	a := h.domainOf().apply
	id, err := a.StartApply(context.Background(), agent.ApplyRequest{Tool: gates.ToolAutofillApplication, Caller: gates.CallerAskAgent, Rows: []agent.ApplyRow{
		{URL: "https://jobs.example.test/a/1", Company: "Example Corp", Role: "Engineer"}}})
	if err != nil {
		t.Fatal(err)
	}
	if !a.busy.Load() {
		t.Fatal("the run is not holding the apply window")
	}
	run := decode[wire.ApplyRunView](t, h.get("/api/apply/runs/"+id))
	if run.Outcome.Kind != "running" {
		t.Fatalf("the run is not mid-stage: %+v", run)
	}
	const batch = "list_20260102T030405.000"
	err = h.srv.store.Load().Write(t.Context(), func(tx *sql.Tx) error {
		if _, err := tx.Exec(`INSERT INTO apply_batches (batch_id, created_at, deadline_at, caller, tool, submit, explicit) VALUES (?, '2026-01-02T03:04:05.000Z', '2026-01-02T04:04:05.000Z', 'ask_agent', 'apply_from_list', 0, 1)`, batch); err != nil {
			return err
		}
		_, err := tx.Exec(`INSERT INTO apply_batch_rows (batch_id, idx, url, company, role, state, run_id, updated_at) VALUES (?, 0, 'https://jobs.example.test/a/1', 'Example Corp', 'Engineer', 'started', ?, '2026-01-02T03:04:05.000Z')`, batch, id)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}

	// The run's tab was kept and the keeper is listing the lane, which waits.
	go a.runner.Keeper().Admit(context.WithoutCancel(t.Context()), gateTab{id: "tab-x", lane: lane}, id)
	select {
	case <-lane.asked:
	case <-time.After(5 * time.Second):
		t.Fatal("the keeper never listed the lane")
	}

	for _, path := range []string{"/api/apply/runs/" + id, "/api/apply/batches/" + batch, "/api/apply/list", "/api/runs", "/api/runs/" + id, "/api/runs/summary"} {
		r, ok := getWithin(t, h, path, release)
		if ok && r.StatusCode != 200 {
			t.Errorf("GET %s: %d %s", path, r.StatusCode, r.body)
		}
	}
	if r, ok := getWithin(t, h, "/api/apply/runs/"+id, release); ok {
		if v := decode[wire.ApplyRunView](t, r); !v.KeptTab {
			t.Errorf("the kept tab is not offered while the lane is listed: %+v", v)
		}
	}
	release()
}
