package api

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"strings"
	"sync/atomic"
	"testing"

	"applysmith/internal/agent"
	"applysmith/internal/api/wire"
	"applysmith/internal/apply"
	"applysmith/internal/discovery"
	"applysmith/internal/engine"
	"applysmith/internal/gates"
	"applysmith/internal/notify"
	"applysmith/internal/workspace"
)

func (h *harness) writeProfile(src string) {
	h.t.Helper()
	if err := os.WriteFile(h.ws.Path(workspace.FileProfile), []byte(src), 0o600); err != nil {
		h.t.Fatal(err)
	}
}

func TestAccountsAPI_Own_FactsAreProfileValuesTheLadderMayHand(t *testing.T) {
	h := newDomainHarness(t)
	h.writeProfile(`version: 1
candidate:
  full_name: Alex Example
  email: alex@example.com
  date_of_birth: 1990-04-05
work_authorization:
  visa_status: Example visa
  requires_sponsorship_now: false
targets:
  roles:
    - {title: Platform Engineer, seniority: senior, priority: primary}
  locations: [Lisbon]
  compensation: {currency: USD, period: year, minimum: 100000, target: 150000}
preferences:
  work_modes: [remote, hybrid]
  max_days_in_office: 2
  relocation: {default: open, places: [Oslo]}
history:
  education:
    - {school: Example University, degree: BSc, field: Computer Science, start: 2010-09, end: 2014-06}
skills:
  - {name: Go, years: 6, level: advanced}
`)
	o, err := h.domainOf().apply.own(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"skill Go": "6 years, advanced", "education 1": "BSc, Computer Science, Example University, 2010-09 to 2014-06", "preferred work modes": "remote, hybrid",
		"preferred max days in office": "2", "relocation": "open: willing to relocate to any place not excluded; preferred: Oslo", "target roles": "senior Platform Engineer", "target locations": "Lisbon",
	}
	for k, v := range want {
		if o.Facts[k] != v {
			t.Errorf("fact %q = %q, want %q", k, o.Facts[k], v)
		}
	}
	for k, v := range o.Facts {
		if !gates.LadderFactAllowed(k) {
			t.Errorf("fact %q is not one the ladder may hand a model", k)
		}
		for _, secret := range []string{"alex@example.com", "1990", "Example visa", "100000", "150000"} {
			if strings.Contains(v, secret) || strings.Contains(k, secret) {
				t.Errorf("fact %q=%q carries a withheld value", k, v)
			}
		}
	}
	if o.Relocation == nil || o.Relocation.Default != "open" || len(o.Relocation.Places) != 1 {
		t.Fatalf("relocation = %+v", o.Relocation)
	}
}

func TestAccountsAPI_Learning_ChainEventsKeepTheirRun(t *testing.T) {
	h := newDomainHarness(t)
	d := h.domainOf()
	runID := "run_20271012T150405.000_ab12cd"
	err := h.srv.store.Load().Write(context.Background(), func(tx *sql.Tx) error {
		_, err := tx.Exec(`INSERT INTO apply_runs (run_id, started_at, caller, posting_url, identity_key) VALUES (?, '2027-10-12T15:04:05.000Z', 'ask_agent', 'https://jobs.example.test/p/1', 'k')`, runID)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	ev := func(id, reason string) apply.LearnEvent {
		return apply.LearnEvent{RunID: runID, LearningEvent: engine.LearningEvent{EventID: id, Kind: "uncertainty", Host: "jobs.example.test", Question: "Example question",
			ControlKind: "text", Reason: reason, Time: h.srv.now().UnixMilli()}}
	}
	// Uncertainty reasons in the receiver's vocabulary are stored.
	events := []apply.LearnEvent{ev("0b1c2d3e-4f50-4a61-8b72-93a4b5c6d7e8", "no-entry"), ev("1b1c2d3e-4f50-4a61-8b72-93a4b5c6d7e8", "review-flagged"),
		ev("2b1c2d3e-4f50-4a61-8b72-93a4b5c6d7e8", "failed")}
	if err := (learnPoster{d.learn, h.srv.log}).PostEvents(context.Background(), events); err != nil {
		t.Fatal(err)
	}
	rows, err := h.srv.store.Load().Read().Query(`SELECT run_id FROM learning_uploads`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	n := 0
	for rows.Next() {
		var got string
		if err := rows.Scan(&got); err != nil || got != runID {
			t.Fatalf("stored run id %q, %v", got, err)
		}
		n++
	}
	if n != 3 {
		t.Fatalf("%d of 3 events stored", n)
	}
	// A run id of the chain's shape that the server never issued is refused.
	stray := ev("3b1c2d3e-4f50-4a61-8b72-93a4b5c6d7e8", "no-entry")
	stray.RunID = "run_20271012T150406.000_ffffff"
	if err := (learnPoster{d.learn, h.srv.log}).PostEvents(context.Background(), []apply.LearnEvent{stray}); err != nil {
		t.Fatal(err)
	}
	var total int
	if err := h.srv.store.Load().Read().QueryRow(`SELECT count(*) FROM learning_uploads`).Scan(&total); err != nil || total != 3 {
		t.Fatalf("%d uploads stored after a stray run id, %v", total, err)
	}
}

// The section captions the engine read around a control are stored with its
// event, so the inbox tells a "Title" of an employment block from a lone one;
// of a value-withheld control only the category survives.
func TestAccountsAPI_Learning_ChainEventsKeepTheirSection(t *testing.T) {
	h := newDomainHarness(t)
	d := h.domainOf()
	runID := "run_20271012T150405.000_ab12cd"
	err := h.srv.store.Load().Write(context.Background(), func(tx *sql.Tx) error {
		_, err := tx.Exec(`INSERT INTO apply_runs (run_id, started_at, caller, posting_url, identity_key) VALUES (?, '2027-10-12T15:04:05.000Z', 'ask_agent', 'https://jobs.example.test/p/1', 'k')`, runID)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		id, question, kind string
		options, section   []string
		withheld           bool
		want               string
	}{
		{id: "0b1c2d3e-4f50-4a61-8b72-93a4b5c6d7e8", question: "Title", kind: "text", section: []string{"Work Experience", "Entry #2"}, want: "Work Experience > Entry #2"},
		{id: "1b1c2d3e-4f50-4a61-8b72-93a4b5c6d7e8", question: "What is your gender?", kind: "select", options: []string{"Female", "Male"},
			section: []string{"Voluntary Self-Identification"}, withheld: true},
	} {
		ev := apply.LearnEvent{RunID: runID, LearningEvent: engine.LearningEvent{EventID: c.id, Kind: "uncertainty", Host: "jobs.example.test", Question: c.question,
			Section: c.section, ControlKind: c.kind, Options: c.options, Reason: "no-entry", Time: h.srv.now().UnixMilli()}}
		if err := (learnPoster{d.learn, h.srv.log}).PostEvents(context.Background(), []apply.LearnEvent{ev}); err != nil {
			t.Fatal(err)
		}
		var section, withheld string
		if err := h.srv.store.Load().Read().QueryRow(`SELECT section_path, withheld FROM answer_events ORDER BY id DESC LIMIT 1`).Scan(&section, &withheld); err != nil {
			t.Fatal(err)
		}
		if section != c.want || (withheld != "") != c.withheld {
			t.Errorf("%s: stored section %q (withheld %q), want %q", c.question, section, withheld, c.want)
		}
	}
}

func TestAccountsAPI_Cancel_AWorkingApplyRunEndsCancelledOnTheBoard(t *testing.T) {
	// The list's liveness probe opens the window first and waits its full
	// bound for a slow browser; it is refused at once so the test reaches the
	// run, whose own attach then holds until the cancel.
	var opens atomic.Int32
	h := newDomainHarness(t, func(c *Config) {
		c.ApplyWindow = func(ctx context.Context) (apply.Window, error) {
			if opens.Add(1) == 1 {
				return nil, errors.New("the probe finds no window")
			}
			<-ctx.Done()
			return nil, ctx.Err()
		}
	})
	a := h.domainOf().apply
	id, err := a.StartApply(context.Background(), agent.ApplyRequest{Tool: gates.ToolApplyFromList, Caller: gates.CallerAskAgent, Rows: []agent.ApplyRow{
		{URL: "https://jobs.example.test/a/1", Company: "Example Corp"}, {URL: "https://jobs.example.test/b/1", Company: "Sample Labs"}}})
	if err != nil {
		t.Fatal(err)
	}
	if r := h.post("/api/runs/run_unknown/cancel", nil); r.StatusCode != 404 {
		t.Fatalf("unknown run: %d %s", r.StatusCode, r.body)
	}
	r := h.post("/api/runs/"+id+"/cancel", nil)
	if r.StatusCode != 202 || r.data()["status"] != wire.RunCancelling {
		t.Fatalf("cancel: %d %s", r.StatusCode, r.body)
	}
	run := waitForApplyRun(t, h)
	if run.kind != "stopped" || !strings.HasPrefix(run.reason, "cancelled") {
		t.Fatalf("run = %+v", run)
	}
	waitLong(t, "the list to end", func() bool { return !a.busy.Load() })
	var entry notify.Entry
	waitLong(t, "the board entry to end", func() bool {
		e, err := a.d.notify.board.Get(context.Background(), id)
		entry = e
		return err == nil && entry.State == notify.Done
	})
	if entry.Outcome != notify.OutcomeCancelled {
		t.Fatalf("entry = %+v", entry)
	}
	if rows := applyRunRows(t, h); len(rows) != 1 {
		t.Fatalf("a cancel must stop the rest of the list: %+v", rows)
	}
	if r := h.post("/api/runs/"+id+"/cancel", nil); r.StatusCode != 409 {
		t.Fatalf("second cancel: %d %s", r.StatusCode, r.body)
	}
}

// The risk view reads the challenge baseline, the recent signals and both kinds
// of cooldown, and writes nothing.
func TestAccountsAPI_RiskView_BaselineSignalsAndCooldowns(t *testing.T) {
	h := newDomainHarness(t)
	ctx := context.Background()
	d, err := h.srv.dom()
	if err != nil {
		t.Fatal(err)
	}
	empty := h.get("/api/policy/risk")
	if empty.StatusCode != 200 || len(empty.data()["baseline"].([]any)) != 0 || len(empty.data()["signals"].([]any)) != 0 || len(empty.data()["cooldowns"].([]any)) != 0 {
		t.Fatalf("empty view: %d %s", empty.StatusCode, empty.body)
	}
	// The key the chain writes for a site the generic flow serves.
	if _, err := d.accounts.RecordSignal(ctx, "generic:careers.example.test", gates.SignalCaptcha, "run_g"); err != nil {
		t.Fatal(err)
	}
	if _, err := d.accounts.RecordSignal(ctx, "workday", gates.SignalCaptcha, "run_a"); err != nil {
		t.Fatal(err)
	}
	if _, err := d.accounts.RecordSignal(ctx, "lever", gates.SignalRateLimited, "run_b"); err != nil {
		t.Fatal(err)
	}
	rec, err := d.repo.ScanStart(ctx, "ziprecruiter", nil)
	if err != nil {
		t.Fatal(err)
	}
	rec.Status, rec.Reason, rec.Detail = discovery.RunBlocked, "challenge", map[string]int{"list-opens": 2, "detail-opens": 3}
	if _, err := d.repo.ScanFinish(ctx, rec); err != nil {
		t.Fatal(err)
	}

	res := h.get("/api/policy/risk")
	if res.StatusCode != 200 {
		t.Fatalf("%d %s", res.StatusCode, res.body)
	}
	v := res.data()
	if v["days"] != float64(30) {
		t.Fatalf("days = %v", v["days"])
	}
	base := v["baseline"].([]any)
	if len(base) != 1 || base[0].(map[string]any)["source"] != "ziprecruiter" || base[0].(map[string]any)["visits"] != float64(5) || base[0].(map[string]any)["challenges"] != float64(1) {
		t.Fatalf("baseline = %v", base)
	}
	sigs := v["signals"].([]any)
	if len(sigs) != 3 || sigs[0].(map[string]any)["system"] != "lever" || sigs[0].(map[string]any)["host"] != nil {
		t.Fatalf("signals = %v", sigs)
	}
	if g := sigs[2].(map[string]any); g["system"] != "generic" || g["host"] != "careers.example.test" {
		t.Fatalf("a generic host's signal reads %v", g)
	}
	kinds := map[string]string{}
	for _, c := range v["cooldowns"].([]any) {
		m := c.(map[string]any)
		host, _ := m["host"].(string)
		kinds[m["kind"].(string)+":"+m["system"].(string)+":"+host] = m["signal"].(string)
	}
	if len(kinds) != 4 || kinds["apply:workday:"] != "captcha" || kinds["apply:lever:"] != "rate_limited" || kinds["scan:ziprecruiter:"] != "blocked scan" ||
		kinds["apply:generic:careers.example.test"] != "captcha" {
		t.Fatalf("cooldowns = %v", kinds)
	}
	if post := h.send("POST", "/api/policy/risk", map[string]any{}); post.StatusCode != 405 && post.StatusCode != 404 {
		t.Fatalf("the view is read-only: %d", post.StatusCode)
	}
}
