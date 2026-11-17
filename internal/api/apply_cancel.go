package api

import (
	"context"
	"net/http"

	"applysmith/internal/api/apikit"
	"applysmith/internal/api/wire"
	"applysmith/internal/notify"
)

// track makes a working run cancellable by its id. A cancel ends the list the
// run belongs to: the row working now stops where the chain checks, and the
// rows after it never start.
func (a *applyStack) track(id string, cancel context.CancelFunc) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.cancels == nil {
		a.cancels = map[string]context.CancelFunc{}
	}
	a.cancels[id] = cancel
}

func (a *applyStack) untrack(id string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	delete(a.cancels, id)
}

// cancel asks the run to stop. It reports false for a run this server is not
// working on. A stop is requested of the chain, never forced: before the press
// claim the run ends cancelled, and once a press went out the run still reaches
// its verdict and record.
func (a *applyStack) cancel(id string) bool {
	a.mu.Lock()
	cancel, ok := a.cancels[id]
	a.mu.Unlock()
	if ok {
		cancel()
	}
	return ok
}

// cancelApplyRun handles the cancel route for a run on the board that has no
// run record of its own. A start that waits for the apply window is taken off
// the queue and ends at once.
func (s *Server) cancelApplyRun(c *apikit.Call, d *domain, entry notify.Entry) (apikit.Reply, error) {
	if entry.State != notify.Working {
		return apikit.Reply{}, apikit.NewErr(wire.CodeConflict, "the run has already ended")
	}
	if d.apply.dequeue(c.R.Context(), entry.ID) {
		if e, err := d.notify.board.Get(c.R.Context(), entry.ID); err == nil {
			entry = d.notify.board.Shown(c.R.Context(), e)
		}
		return apikit.Reply{Data: viewOfEntry(entry)}, nil
	}
	if !d.apply.cancel(entry.ID) {
		return apikit.Reply{}, apikit.NewErr(wire.CodeConflict, "the run is not active in this server")
	}
	detail := "cancelling"
	if _, err := d.notify.board.Update(context.WithoutCancel(c.R.Context()), entry.ID, notify.Patch{Detail: &detail}); err != nil {
		s.log.Warn("a cancelling apply run could not be marked on the board", "run", entry.ID, "error", err.Error())
	}
	view := viewOfEntry(entry)
	view.Status = wire.RunCancelling
	return apikit.Reply{Status: http.StatusAccepted, Location: "/api/runs/" + entry.ID, Data: view}, nil
}
