package api

import (
	"context"
	"errors"
	"fmt"

	"applysmith/internal/agent"
	"applysmith/internal/api/apikit"
	"applysmith/internal/api/applyapi"
	"applysmith/internal/api/wire"
	"applysmith/internal/apply"
	"applysmith/internal/notify"
)

// agentApply serves the Ask Agent's apply_* tools from a stopped run's kept
// tab. A call takes the apply lane for as long as it works the tab, as a
// resumed run does, and keeps the tab again when it ends. The run stays where
// the board has it, waiting for the user, until apply_continue goes on with it
// by the same door a Done press uses.
type agentApply struct{ s *Server }

var _ agent.ApplyRun = agentApply{}

// stateRefusal turns a refusal of the run's own state (no such run, not waiting,
// lane busy) into the answer the model reads; any other error is a fault.
func stateRefusal(err error) (string, bool) {
	var e *apikit.Error
	if errors.As(err, &e) && (e.Code == wire.CodeConflict || e.Code == wire.CodeNotFound) {
		return "Not done: " + e.Message + ".", true
	}
	return "", false
}

// operate takes the run's kept tab over for one call of fn.
func (a agentApply) operate(ctx context.Context, runID string, fn func(*apply.Operation) (string, error)) (string, error) {
	d, err := a.s.dom()
	if err != nil {
		return "", err
	}
	st := d.apply
	entry, err := d.notify.board.Get(ctx, runID)
	switch {
	case errors.Is(err, notify.ErrNotFound):
		return "Not done: there is no such run.", nil
	case err != nil:
		return "", err
	case !notify.CanResume(entry) || !st.keptTab(runID):
		return "Not done: the run does not wait for the user in a tab it kept.", nil
	}
	in, err := st.requestOf(ctx, entry, runID, applyapi.DoneFromAgent)
	if err != nil {
		if text, ok := stateRefusal(err); ok {
			return text, nil
		}
		return "", err
	}
	in.TabID = st.tabOf(ctx, runID)
	if e := st.takeLane(runID); e != nil {
		return "Not done: " + e.Message + "; try again in a moment.", nil
	}
	defer st.busy.Store(false)
	defer st.lend(runID) // before the window is freed: the turn's next call finds it
	o, err := st.runner.Operate(ctx, in)
	if err != nil {
		return "", err
	}
	defer o.Close(ctx)
	return fn(o)
}

func (a agentApply) Page(ctx context.Context, runID string, evidence bool) (string, error) {
	return a.operate(ctx, runID, func(o *apply.Operation) (string, error) { return o.Page(ctx, evidence) })
}

func (a agentApply) Fill(ctx context.Context, runID string) (string, error) {
	return a.operate(ctx, runID, func(o *apply.Operation) (string, error) { return o.Fill(ctx) })
}

func (a agentApply) Answer(ctx context.Context, runID string, rows []agent.RunAnswer) (string, error) {
	in := make([]apply.AnswerRow, len(rows))
	for i, r := range rows {
		in[i] = apply.AnswerRow{ID: r.ID, Value: r.Value, Cite: r.Cite, Change: r.Change}
	}
	return a.operate(ctx, runID, func(o *apply.Operation) (string, error) { return o.Answer(ctx, in) })
}

func (a agentApply) Press(ctx context.Context, runID, label string) (string, error) {
	return a.operate(ctx, runID, func(o *apply.Operation) (string, error) { return o.Press(ctx, label) })
}

func (a agentApply) Review(ctx context.Context, runID string) (string, error) {
	return a.operate(ctx, runID, func(o *apply.Operation) (string, error) { return o.Review(ctx) })
}

// Leave records the agent's reason on the run's board entry. The run already
// waits for the user in its kept tab, so nothing else moves.
func (a agentApply) Leave(ctx context.Context, runID, reason string) (string, error) {
	d, err := a.s.dom()
	if err != nil {
		return "", err
	}
	entry, err := d.notify.board.Get(ctx, runID)
	switch {
	case errors.Is(err, notify.ErrNotFound):
		return "Not done: there is no such run.", nil
	case err != nil:
		return "", err
	case !notify.CanResume(entry):
		return "Not done: the run does not wait for the user.", nil
	}
	detail := "The Ask Agent left this for you: " + reason
	if _, err := d.notify.board.Update(ctx, runID, notify.Patch{Detail: &detail}); err != nil {
		return "", err
	}
	d.apply.runner.ForgetWork(runID)
	d.apply.unlend(runID)
	return "The run is handed back to the user, its tab kept as it is.", nil
}

// Continue is the Done press by the agent: the run walks on from the page in
// its kept tab, checks it as it checks any page, and stops at Submit, which the
// user confirms in the dashboard.
func (a agentApply) Continue(ctx context.Context, runID string) (string, error) {
	d, err := a.s.dom()
	if err != nil {
		return "", err
	}
	if _, err := d.apply.goOn(ctx, runID, applyapi.DoneFromAgent, "Resumed with the page the Ask Agent filled; Submit waits for your confirmation.", true); err != nil {
		if text, ok := stateRefusal(err); ok {
			return text, nil
		}
		return "", err
	}
	d.apply.runner.ForgetWork(runID)
	return fmt.Sprintf("Run %s goes on in its kept tab: it fills what is left, checks each page and stops at Submit for the user's confirmation. Tell the user what you filled.", runID), nil
}
