package api

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"sync"

	"applysmith/internal/api/apikit"
	"applysmith/internal/api/wire"
	"applysmith/internal/apply"
	"applysmith/internal/browser"
	"applysmith/internal/store"
)

// maxFillResults bounds the results kept in memory; the oldest goes first.
const maxFillResults = 32

// activeFills tracks the fills of the apply window's active tab and keeps
// their results. One fill runs per tab, and none while an application run
// drives the window. After a fill, what the person changes in the tab is
// followed until the next fill of that tab.
type activeFills struct {
	mu      sync.Mutex
	tabs    map[string]bool
	follows map[string]*following
	results map[string]*wire.AutofillTabResult
	order   []string
}

// following is the follow of what the person changes in a tab after its fill.
type following struct {
	stop, done chan struct{}
	stopping   bool
}

// follow registers the follow of tab. stop closes when the next fill of the tab
// ends it; end, called once the follow has returned, drops it.
func (f *activeFills) follow(tab string) (stop <-chan struct{}, end func()) {
	fw := &following{stop: make(chan struct{}), done: make(chan struct{})}
	f.mu.Lock()
	if f.follows == nil {
		f.follows = map[string]*following{}
	}
	f.follows[tab] = fw
	f.mu.Unlock()
	return fw.stop, func() {
		f.mu.Lock()
		if f.follows[tab] == fw {
			delete(f.follows, tab)
		}
		f.mu.Unlock()
		close(fw.done)
	}
}

// endFollow ends the follow of tab, if one runs, and returns once it has
// posted what it last drained.
func (f *activeFills) endFollow(tab string) {
	f.mu.Lock()
	fw := f.follows[tab]
	if fw != nil && !fw.stopping {
		fw.stopping = true
		close(fw.stop)
	}
	f.mu.Unlock()
	if fw != nil {
		<-fw.done
	}
}

func (f *activeFills) reserve(tab string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.tabs[tab] {
		return false
	}
	if f.tabs == nil {
		f.tabs = map[string]bool{}
	}
	f.tabs[tab] = true
	return true
}

func (f *activeFills) release(tab string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.tabs, tab)
}

func (f *activeFills) any() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.tabs) > 0
}

func (f *activeFills) put(r wire.AutofillTabResult) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.results == nil {
		f.results = map[string]*wire.AutofillTabResult{}
	}
	if _, ok := f.results[r.RunID]; !ok {
		f.order = append(f.order, r.RunID)
		if len(f.order) > maxFillResults {
			delete(f.results, f.order[0])
			f.order = f.order[1:]
		}
	}
	f.results[r.RunID] = &r
}

func (f *activeFills) get(id string) (wire.AutofillTabResult, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	r, ok := f.results[id]
	if !ok {
		return wire.AutofillTabResult{}, false
	}
	return *r, true
}

// laneError maps a failure to reach the browser to the envelope: unavailable,
// with the browser's own code as the reason.
func laneError(err error) *apikit.Error {
	e := apikit.Unavailable("the apply browser cannot be used: " + err.Error())
	if code, ok := browser.CodeOf(err); ok {
		e = apikit.WithReason(e, string(code))
	}
	return e
}

// autofillActiveTab fills the tab the apply window shows, once: the page step
// and the page check of an apply run, with no walk and no submit. The refusals
// that need no page are answered before anything touches the browser.
func (s *Server) autofillActiveTab(c *apikit.Call) (apikit.Reply, error) {
	d, err := s.dom()
	if err != nil {
		return apikit.Reply{}, err
	}
	st, err := s.db()
	if err != nil {
		return apikit.Reply{}, err
	}
	a := d.apply
	if a.driving() {
		return apikit.Reply{}, apikit.WithReason(apikit.NewErr(wire.CodeConflict, "an application run is driving the apply window; wait for it to finish"), wire.ReasonLaneBusy)
	}
	if s.cfg.ApplyWindow == nil {
		return apikit.Reply{}, apikit.Unavailable("the application browser is not available in this build")
	}
	ctx := c.R.Context()
	win, err := s.cfg.ApplyWindow(ctx)
	if err != nil {
		return apikit.Reply{}, laneError(err)
	}
	aw, ok := win.(apply.ActiveWindow)
	if !ok {
		return apikit.Reply{}, apikit.Unavailable("the application browser cannot fill a tab in this build")
	}
	tab, err := aw.ActiveTab(ctx)
	if errors.Is(err, apply.ErrNoTab) {
		return apikit.Reply{}, apikit.WithReason(apikit.NewErr(wire.CodeNotFound, "the apply window has no tab to fill"), wire.ReasonNoTab)
	}
	if err != nil {
		return apikit.Reply{}, laneError(err)
	}
	if err := a.reserveFill(tab); err != nil {
		return apikit.Reply{}, err
	}
	// What the person changed since the tab's last fill is posted before this
	// fill writes into the page.
	a.fills.endFollow(tab)
	fill, err := a.runner.OpenFill(ctx, aw, tab)
	if err != nil {
		a.fills.release(tab)
		go a.next()
		return apikit.Reply{}, laneError(err)
	}
	if reply, err := s.checkFillTab(ctx, a, fill, tab); err != nil {
		return reply, err
	}
	ref, err := st.CreateRun(ctx, store.RunRequest{Principal: c.P.ID, Operation: "autofill.active-tab", Key: "tab-" + randomKey(), Kind: wire.RunKindAutofillTab,
		Input: []byte(`{}`), Created: func(tx *sql.Tx, runID string) error {
			_, err := tx.Exec(`UPDATE runs SET status = ?, stage = 'fill' WHERE id = ?`, wire.RunRunning, runID)
			return err
		}})
	if err != nil {
		a.endFill(tab, fill)
		return apikit.Reply{}, err
	}
	a.fills.put(wire.AutofillTabResult{RunID: ref.RunID, Status: wire.RunRunning})
	s.notifyTopics("browser")
	a.wg.Go(func() { s.runFill(a, st, fill, tab, ref.RunID) })
	return apikit.Reply{Status: http.StatusAccepted, Location: "/api/runs/" + ref.RunID, Data: wire.RunAccepted{RunID: ref.RunID}}, nil
}

// checkFillTab refuses a page that is not an application form. It lets go of
// the tab and the reservation when it refuses.
func (s *Server) checkFillTab(ctx context.Context, a *applyStack, fill *apply.Fill, tab string) (apikit.Reply, error) {
	pg, err := fill.Probe(ctx)
	if err != nil {
		a.endFill(tab, fill)
		return apikit.Reply{}, laneError(err)
	}
	if ok, why := apply.LooksLikeApplication(pg); !ok {
		a.endFill(tab, fill)
		return apikit.Reply{}, apikit.WithReason(apikit.NewErr(wire.CodeValidationFailed, "this page is not an application form: %s", why), wire.ReasonNotAnApp)
	}
	return apikit.Reply{}, nil
}

// runFill is the fill's background half: it runs the page step and the check,
// records how the run ended and then, on a form it filled, follows what the
// person changes in the tab.
func (s *Server) runFill(a *applyStack, st *store.Store, fill *apply.Fill, tab, runID string) {
	stop, end := s.fillOnce(a, st, fill, tab, runID)
	if end == nil {
		return
	}
	defer end()
	why, posted := fill.Follow(s.bg, stop)
	s.log.Info("the edits after a fill of the active tab were collected", "run", runID, "ended", why, "events", posted)
}

// fillOnce runs the fill and records how it ended. A fill that wrote into an
// application form registers the follow of the tab while the tab is still
// reserved, so the next fill of the tab finds it and ends it first.
func (s *Server) fillOnce(a *applyStack, st *store.Store, fill *apply.Fill, tab, runID string) (stop <-chan struct{}, end func()) {
	defer a.endFill(tab, fill)
	res, err := fill.Run(s.bg, runID)
	out := wire.AutofillTabResult{RunID: runID, Status: wire.RunSucceeded, Filled: []wire.AutofillControl{}, Review: []wire.AutofillControl{},
		Missing: []wire.AutofillControl{}, Skipped: []wire.AutofillControl{}}
	switch {
	case err != nil:
		out.Status, out.Reason = wire.RunFailed, err.Error()
		if s.bg.Err() != nil || errors.Is(err, context.Canceled) {
			out.Status = wire.RunCancelled
		}
	case res.NotAnApplication:
		out.Status, out.Reason = wire.RunFailed, wire.ReasonNotAnApp+": the page was read as not an application and nothing was written"
	default:
		out.URL, out.Site = res.URL, res.Site
		out.Filled, out.Review, out.Missing, out.Skipped = controlsOf(res.Filled), controlsOf(res.Review), controlsOf(res.Missing), controlsOf(res.Skipped)
		out.Drafts, out.Flagged, out.Events = apikit.Safe(res.Drafts), apikit.Safe(res.Flagged), apikit.Safe(res.Events)
	}
	ctx := context.WithoutCancel(s.bg)
	a.fills.put(out)
	if err := st.SetRun(ctx, runID, out.Status, "", out.Reason); err != nil {
		s.log.Warn("the fill of the active tab could not be recorded", "run", runID, "error", err.Error())
	}
	s.runs.publish(runID, "final", wire.RunFinal{RunID: runID, Status: out.Status, Reason: out.Reason})
	s.runs.retire(runID)
	s.notifyTopics("browser")
	if out.Status == wire.RunSucceeded {
		stop, end = a.fills.follow(tab)
	}
	return stop, end
}

func controlsOf(in []apply.FillControl) []wire.AutofillControl {
	out := make([]wire.AutofillControl, len(in))
	for i, c := range in {
		out[i] = wire.AutofillControl{Label: c.Label, Reason: c.Reason}
	}
	return out
}

// getAutofillTab answers the result of a fill: the groups once it ended, its
// status while it runs. A result older than the server's memory of it reads as
// its stored status with no groups.
func (s *Server) getAutofillTab(c *apikit.Call) (apikit.Reply, error) {
	d, err := s.dom()
	if err != nil {
		return apikit.Reply{}, err
	}
	id := c.R.PathValue("id")
	if r, ok := d.apply.fills.get(id); ok {
		return apikit.Reply{Data: r}, nil
	}
	st, err := s.db()
	if err != nil {
		return apikit.Reply{}, err
	}
	run, err := st.GetRun(c.R.Context(), id)
	if err != nil || run.Kind != wire.RunKindAutofillTab {
		return apikit.Reply{}, apikit.NewErr(wire.CodeNotFound, "no fill of the active tab has that id")
	}
	return apikit.Reply{Data: wire.AutofillTabResult{RunID: id, Status: run.Status, Reason: run.FailureReason, Filled: []wire.AutofillControl{},
		Review: []wire.AutofillControl{}, Missing: []wire.AutofillControl{}, Skipped: []wire.AutofillControl{}}}, nil
}
