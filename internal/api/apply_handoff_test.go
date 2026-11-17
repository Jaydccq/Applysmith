package api

import (
	"context"
	"database/sql"
	"errors"
	"slices"
	"sync"
	"testing"

	"applysmith/internal/api/wire"
	"applysmith/internal/apply"
	"applysmith/internal/browser"
	"applysmith/internal/engine"
	"applysmith/internal/notify"
)

// showLane is the apply lane as the show-tab route reaches it.
type showLane struct {
	mu      sync.Mutex
	live    []string
	shown   []string
	closed  []string
	showErr error
	reads   int
}

func (l *showLane) LiveTabs(context.Context) ([]string, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.reads++
	return slices.Clone(l.live), nil
}
func (l *showLane) CloseTabID(_ context.Context, id string) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.closed = append(l.closed, id)
	return nil
}

func (l *showLane) wasClosed(id string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return slices.Contains(l.closed, id)
}
func (l *showLane) ShowTab(_ context.Context, id string) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.showErr != nil {
		return l.showErr
	}
	l.shown = append(l.shown, id)
	return nil
}

type showWindow struct{ lane *showLane }

func (w showWindow) OpenTab(context.Context, string, engine.Host) (apply.Tab, error) {
	return nil, errors.New("not used")
}
func (w showWindow) Handle() apply.TabLane { return w.lane }

func keptRow(t *testing.T, h *harness, id, tab string) {
	t.Helper()
	d := h.domainOf()
	if _, err := d.notify.board.Open(t.Context(), notify.OpenArgs{ID: id, Kind: notify.KindApply, Origin: notify.OriginAgent, Title: "Apply to Example Corp", Stage: "fill", FocusTab: tab}); err != nil {
		t.Fatal(err)
	}
	if _, err := d.notify.board.Move(t.Context(), id, notify.NeedsYou, notify.MoveArgs{Reason: "challenge", Detail: "a bot check", FocusTab: tab}); err != nil {
		t.Fatal(err)
	}
}

func TestApplyHandoff_ShowTabBringsTheKeptTabForward(t *testing.T) {
	lane := &showLane{live: []string{"tab-1"}}
	h := newDomainHarness(t, func(c *Config) {
		c.ApplyWindow = func(context.Context) (apply.Window, error) { return showWindow{lane}, nil }
	})
	keptRow(t, h, "run_a", "tab-1")

	var env wire.Success[wire.ApplyTabShown]
	r := h.post("/api/apply/runs/run_a/show-tab", nil)
	if r.StatusCode != 200 {
		t.Fatalf("%d %s", r.StatusCode, r.body)
	}
	strictInto(t, r.body, &env)
	if env.Data.RunID != "run_a" || !slices.Equal(lane.shown, []string{"tab-1"}) {
		t.Fatalf("%+v shown %v", env.Data, lane.shown)
	}

	t.Run("a run that kept no tab", func(t *testing.T) {
		if r := h.post("/api/apply/runs/run_none/show-tab", nil); r.StatusCode != 404 || r.errReason() != wire.ReasonNoTab {
			t.Fatalf("%d %s", r.StatusCode, r.body)
		}
	})
	t.Run("a tab the user closed stops being offered", func(t *testing.T) {
		keptRow(t, h, "run_b", "tab-gone")
		lane.mu.Lock()
		lane.showErr = &browser.Error{Code: browser.CodeTabClosed, Message: "no such tab"}
		lane.mu.Unlock()
		if r := h.post("/api/apply/runs/run_b/show-tab", nil); r.StatusCode != 404 || r.errReason() != wire.ReasonNoTab {
			t.Fatalf("%d %s", r.StatusCode, r.body)
		}
		e, err := h.domainOf().notify.board.Get(t.Context(), "run_b")
		if err != nil || e.FocusTab != "" {
			t.Fatalf("the row still offers the jump: %+v %v", e, err)
		}
	})
}

func TestApplyStack_FillsInstallTheMainWorldHelper(t *testing.T) {
	if !newDomainHarness(t).domainOf().apply.runner.MainWorld() {
		t.Fatal("the apply host never asks for the main-world helper, so component drivers end binding-unavailable")
	}
}

// keptFake is a tab a run kept, as the keeper sees it.
type keptFake struct {
	apply.Tab
	id string
}

func (k keptFake) ID() string                  { return k.id }
func (k keptFake) Close(context.Context) error { return nil }

func TestApplyHandoff_FinishPutsTheKeptTabOnTheBoardRow(t *testing.T) {
	h := newDomainHarness(t)
	d := h.domainOf()
	a := d.apply
	if got := a.focusTab("run_unknown"); got != "" {
		t.Fatalf("focus tab %q for a run that kept none", got)
	}
	open := func(id string) {
		if _, err := d.notify.board.Open(t.Context(), notify.OpenArgs{ID: id, Kind: notify.KindApply, Origin: notify.OriginAgent, Title: "Apply", Stage: "fill"}); err != nil {
			t.Fatal(err)
		}
	}
	stopped := apply.Outcome{Kind: apply.OutcomeStopped, Stage: apply.StageFill, Class: apply.ClassDefect, Reason: "a bot check", Board: "challenge"}

	open("run_kept")
	a.runner.Keeper().Admit(t.Context(), keptFake{id: "tab-9"}, "run_kept")
	a.finish(&apply.Run{ID: "run_kept", Outcome: stopped})
	e, err := d.notify.board.Get(t.Context(), "run_kept")
	if err != nil || e.State != notify.NeedsYou || e.FocusTab != "tab-9" {
		t.Fatalf("%+v %v", e, err)
	}
	if !a.keptTab("run_kept") {
		t.Fatal("the run's kept tab is not offered")
	}

	// A run that closed its tab leaves no jump on its row.
	open("run_closed")
	a.finish(&apply.Run{ID: "run_closed", Outcome: apply.Outcome{Kind: apply.OutcomeStopped, Stage: apply.StageAccount, Class: apply.ClassPosting, Reason: "posting closed"}})
	if e, err := d.notify.board.Get(t.Context(), "run_closed"); err != nil || e.FocusTab != "" {
		t.Fatalf("%+v %v", e, err)
	}
}

func TestApplyHandoff_ANewRunOfAPostingRetiresItsEarlierStopsAndClosesTheirTabs(t *testing.T) {
	lane := &showLane{live: []string{"tab-old", "tab-filled", "tab-refused", "tab-maybe", "tab-other"}}
	h := newDomainHarness(t, func(c *Config) {
		c.ApplyWindow = func(context.Context) (apply.Window, error) { return showWindow{lane}, nil }
	})
	const posting = "https://jobs.example.test/p/1"
	earlier := func(id, url string, to notify.State, reason, tab string) {
		t.Helper()
		key, stp := apply.IdentityKey(url)
		if stp != nil {
			t.Fatal(stp)
		}
		err := h.srv.store.Load().Write(t.Context(), func(tx *sql.Tx) error {
			_, err := tx.Exec(`INSERT INTO apply_runs (run_id, started_at, finished_at, caller, posting_url, identity_key, outcome_kind) VALUES
				(?, '2027-10-12T15:04:05.000Z', '2027-10-12T15:09:05.000Z', 'dashboard', ?, ?, 'stopped')`, id, url, key)
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
		board := h.domainOf().notify.board
		if _, err := board.Open(t.Context(), notify.OpenArgs{ID: id, Kind: notify.KindApply, Origin: notify.OriginUser, Title: "Apply to Example Corp", FocusTab: tab}); err != nil {
			t.Fatal(err)
		}
		if _, err := board.Move(t.Context(), id, to, notify.MoveArgs{Reason: reason, FocusTab: tab}); err != nil {
			t.Fatal(err)
		}
	}
	earlier("run_old", posting, notify.NeedsYou, "answer", "tab-old")
	earlier("run_filled", posting, notify.NeedsYou, "confirmation", "tab-filled")
	// A refused saved password keeps the sign-in page for the user.
	earlier("run_refused", posting, notify.Blocked, "credential-refused", "tab-refused")
	earlier("run_maybe", posting, notify.NeedsYou, "may-have-submitted", "tab-maybe")
	earlier("run_other", "https://jobs.example.test/p/2", notify.NeedsYou, "answer", "tab-other")
	if r := h.post("/api/apply/runs", wire.ApplyRunRequest{URL: posting}); r.StatusCode != 202 {
		t.Fatalf("%d %s", r.StatusCode, r.body)
	}
	waitLong(t, "the stale tabs to close", func() bool {
		return lane.wasClosed("tab-old") && lane.wasClosed("tab-filled") && lane.wasClosed("tab-refused")
	})
	for _, id := range []string{"run_old", "run_filled", "run_refused"} {
		waitEntry(t, h, id, func(e notify.Entry) bool { return e.Acked() && e.FocusTab == "" })
	}
	// A possible submission stays for the user to check; another posting is not touched.
	for _, c := range []struct{ id, tab string }{{"run_maybe", "tab-maybe"}, {"run_other", "tab-other"}} {
		if e := entryOf(t, h, c.id); e.Acked() || e.FocusTab != c.tab || lane.wasClosed(c.tab) {
			t.Errorf("%s: %+v", c.id, e)
		}
	}
}

func TestApplyHandoff_TheTabsOfABrowserThatDidNotSurviveLeaveTheBoard(t *testing.T) {
	ctl := &fakeControl{survives: true}
	h := newDomainHarness(t, func(c *Config) { c.BrowserControl = ctl })
	board := h.domainOf().notify.board
	wait := func(id, reason, tab string) {
		t.Helper()
		if _, err := board.Open(t.Context(), notify.OpenArgs{ID: id, Kind: notify.KindApply, Origin: notify.OriginUser, Title: "Apply to " + id, FocusTab: tab}); err != nil {
			t.Fatal(err)
		}
		if _, err := board.Move(t.Context(), id, notify.NeedsYou, notify.MoveArgs{Reason: reason, FocusTab: tab}); err != nil {
			t.Fatal(err)
		}
	}
	wait("run_filled", "confirmation", "tab-filled")
	wait("run_answer", "answer", "tab-answer")
	// An earlier process lost this filled form's tab and left it in the inbox.
	wait("run_lost", "confirmation", "")
	inbox := func() []string {
		var env wire.Success[wire.NeedsYouInbox]
		strictInto(t, h.get("/api/needs-you").body, &env)
		var ids []string
		for _, it := range env.Data.Items {
			ids = append(ids, it.Entry.ID+"/"+string(it.Action))
		}
		slices.Sort(ids)
		return ids
	}

	// A browser an abrupt stop left running is adopted with its tabs; a filled
	// form waiting in no tab leaves the inbox all the same.
	h.domainOf().apply.dropClosedTabs()
	if got := inbox(); !slices.Equal(got, []string{"run_answer/" + string(wire.ActionFocusTab), "run_filled/" + string(wire.ActionFocusTab)}) {
		t.Fatalf("a surviving browser's tabs left the board: %v", got)
	}

	// A graceful stop closed the browser: the filled form is gone with its tab,
	// and the question still waits, now on its run page.
	ctl.mu.Lock()
	ctl.survives = false
	ctl.mu.Unlock()
	h.domainOf().apply.dropClosedTabs()
	if got := inbox(); !slices.Equal(got, []string{"run_answer/" + string(wire.ActionOpenRun)}) {
		t.Fatalf("inbox %v", got)
	}
	if e := entryOf(t, h, "run_filled"); e.FocusTab != "" || e.State != notify.NeedsYou {
		t.Fatalf("the filled form's row: %+v", e)
	}
}
