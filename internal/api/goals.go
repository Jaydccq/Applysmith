// Clean-room: docs/provenance.md#CR-AGENT-GOALS

package api

import (
	"context"
	"errors"
	"net/http"

	"applysmith/internal/agent/goals"
	"applysmith/internal/api/apikit"
	"applysmith/internal/api/wire"
	"applysmith/internal/notify"
	"applysmith/internal/scheduler"
)

// goalRoutes serve the Goals panel. The user creates, edits, pauses,
// completes and deletes goals here, and accepts or dismisses the plan changes
// the agent proposed. Accepting activates a proposal, so it stays off remote
// listeners; no tool and no Telegram callback reaches these routes.
var goalRoutes = []Route{
	route("GET", "/api/agent/goals", reads("GoalList")),
	route("POST", "/api/agent/goals", sends("GoalCreate", "Goal")),
	route("GET", "/api/agent/goals/{id}", reads("Goal")),
	route("PATCH", "/api/agent/goals/{id}", sends("GoalPatch", "Goal")),
	route("DELETE", "/api/agent/goals/{id}", reads("GoalDeleted")),
	route("POST", "/api/agent/goals/{id}/proposals/{proposalId}/accept", reads("Goal")),
	route("POST", "/api/agent/goals/{id}/proposals/{proposalId}/dismiss", reads("Goal")),
}

func (s *Server) goalStore() (*goals.Store, error) {
	d, err := s.dom()
	if err != nil {
		return nil, err
	}
	return d.goals, nil
}

// goalError maps the store's refusals onto the API's answers.
func goalError(err error) error {
	switch {
	case errors.Is(err, goals.ErrNotFound):
		return apikit.NewErr(wire.CodeNotFound, "no such goal or proposal")
	case errors.Is(err, goals.ErrTooManyActive), errors.Is(err, goals.ErrConflict):
		return apikit.NewErr(wire.CodeConflict, "%s", err.Error())
	case errors.Is(err, goals.ErrInvalid), errors.Is(err, goals.ErrRefused):
		return apikit.NewErr(wire.CodeValidationFailed, "%s", err.Error())
	}
	return err
}

func goalActor(p apikit.Principal) (goals.Actor, bool) {
	switch p.Kind {
	case apikit.PrincipalDashboard:
		return goals.ActorDashboard, true
	case apikit.PrincipalLocalToken:
		return goals.ActorCLI, true
	}
	return "", false
}

func fromWireSteps(in []wire.GoalStep) []goals.Step {
	out := make([]goals.Step, len(in))
	for i, s := range in {
		out[i] = goals.Step{Text: s.Text, Status: s.Status, Note: s.Note, ApplicationID: s.ApplicationID, Company: s.Company}
	}
	return out
}

func toWireSteps(in []goals.Step) []wire.GoalStep {
	out := make([]wire.GoalStep, len(in))
	for i, s := range in {
		out[i] = wire.GoalStep{Text: s.Text, Status: s.Status, Note: s.Note, ApplicationID: s.ApplicationID, Company: s.Company}
	}
	return out
}

func fromWireCadence(c *wire.GoalCadence) goals.Cadence {
	if c == nil {
		return goals.Cadence{}
	}
	return goals.Cadence{Days: c.Days, Time: c.Time}
}

func toWireCadence(c goals.Cadence) wire.GoalCadence {
	days := c.Days
	if days == nil {
		days = []string{}
	}
	return wire.GoalCadence{Days: days, Time: c.Time}
}

func (s *Server) toGoal(st *goals.Store, g goals.Goal) wire.Goal {
	out := wire.Goal{ID: g.ID, Title: g.Title, DueDate: g.DueDate, State: g.State, Version: apikit.Safe(g.Version), Cadence: toWireCadence(g.Cadence),
		Steps: toWireSteps(g.Steps), CreatedAt: wire.Time{Time: g.CreatedAt}, UpdatedAt: wire.Time{Time: g.UpdatedAt}}
	if next := st.NextCheckin(g); !next.IsZero() {
		out.NextCheckin = &wire.Time{Time: next}
	}
	if p := g.Proposal; p != nil {
		wp := wire.GoalProposal{ID: p.ID, Reason: p.Reason, Steps: toWireSteps(p.Change.Steps), DueDate: p.Change.DueDate, CreatedAt: wire.Time{Time: p.CreatedAt}}
		if p.Change.Cadence != nil {
			c := toWireCadence(*p.Change.Cadence)
			wp.Cadence = &c
		}
		out.Proposal = &wp
	}
	return out
}

func (s *Server) listGoals(c *apikit.Call) (apikit.Reply, error) {
	st, err := s.goalStore()
	if err != nil {
		return apikit.Reply{}, err
	}
	list, err := st.List(c.R.Context())
	if err != nil {
		return apikit.Reply{}, err
	}
	out := wire.GoalList{Goals: make([]wire.Goal, len(list))}
	for i, g := range list {
		out.Goals[i] = s.toGoal(st, g)
	}
	return apikit.Reply{Data: out}, nil
}

func (s *Server) createGoal(c *apikit.Call) (apikit.Reply, error) {
	var req wire.GoalCreate
	if e := apikit.Decode(c.Body, &req); e != nil {
		return apikit.Reply{}, e
	}
	st, err := s.goalStore()
	if err != nil {
		return apikit.Reply{}, err
	}
	g, err := st.Create(c.R.Context(), goals.NewGoal{Title: req.Title, DueDate: req.DueDate, Steps: fromWireSteps(req.Steps), Cadence: fromWireCadence(req.Cadence)})
	if err != nil {
		return apikit.Reply{}, goalError(err)
	}
	return apikit.Reply{Status: http.StatusCreated, Location: "/api/agent/goals/" + g.ID, Data: s.toGoal(st, g)}, nil
}

func (s *Server) getGoal(c *apikit.Call) (apikit.Reply, error) {
	st, err := s.goalStore()
	if err != nil {
		return apikit.Reply{}, err
	}
	g, err := st.Get(c.R.Context(), c.R.PathValue("id"))
	if err != nil {
		return apikit.Reply{}, goalError(err)
	}
	return apikit.Reply{Data: s.toGoal(st, g)}, nil
}

func (s *Server) patchGoal(c *apikit.Call) (apikit.Reply, error) {
	var req wire.GoalPatch
	if e := apikit.Decode(c.Body, &req); e != nil {
		return apikit.Reply{}, e
	}
	st, err := s.goalStore()
	if err != nil {
		return apikit.Reply{}, err
	}
	p := goals.Patch{Title: req.Title, DueDate: req.DueDate, State: req.State}
	if req.Cadence != nil {
		cad := fromWireCadence(req.Cadence)
		p.Cadence = &cad
	}
	if req.Steps != nil {
		steps := fromWireSteps(*req.Steps)
		p.Steps = &steps
	}
	g, err := st.Update(c.R.Context(), c.R.PathValue("id"), p)
	if err != nil {
		return apikit.Reply{}, goalError(err)
	}
	return apikit.Reply{Data: s.toGoal(st, g)}, nil
}

func (s *Server) deleteGoal(c *apikit.Call) (apikit.Reply, error) {
	st, err := s.goalStore()
	if err != nil {
		return apikit.Reply{}, err
	}
	id := c.R.PathValue("id")
	if err := st.Delete(c.R.Context(), id); err != nil {
		return apikit.Reply{}, goalError(err)
	}
	return apikit.Reply{Data: wire.GoalDeleted{ID: id}}, nil
}

func (s *Server) acceptGoalProposal(c *apikit.Call) (apikit.Reply, error) {
	return s.decideProposal(c, true)
}

func (s *Server) dismissGoalProposal(c *apikit.Call) (apikit.Reply, error) {
	return s.decideProposal(c, false)
}

func (s *Server) decideProposal(c *apikit.Call, accept bool) (apikit.Reply, error) {
	st, err := s.goalStore()
	if err != nil {
		return apikit.Reply{}, err
	}
	by, ok := goalActor(c.P)
	if !ok {
		return apikit.Reply{}, errUnauthenticated()
	}
	id, pid := c.R.PathValue("id"), c.R.PathValue("proposalId")
	var g goals.Goal
	if accept {
		g, err = st.Accept(c.R.Context(), id, pid, by)
	} else if err = st.Dismiss(c.R.Context(), id, pid, by); err == nil {
		g, err = st.Get(c.R.Context(), id)
	}
	if err != nil {
		return apikit.Reply{}, goalError(err)
	}
	return apikit.Reply{Data: s.toGoal(st, g)}, nil
}

// goalCheckinTimer is the built-in timer that announces the goal check-ins
// that came due. It builds each message from the goal record, calls no model
// and starts no run; the announcement policy holds it in quiet hours.
func (s *Server) goalCheckinTimer(d *domain) scheduler.Timer {
	return scheduler.EveryTick("goal-checkins", "Ask Agent goal check-ins, checked on each tick; the next slot of each goal is on the Goals panel",
		func(ctx context.Context) error {
			due, err := d.goals.DueCheckins(ctx, s.now())
			var errs []error
			if err != nil {
				errs = append(errs, err)
			}
			for _, ci := range due {
				if err := d.notify.policy.Announce(ctx, notify.Event{Key: ci.Key, Kind: notify.EventGoalCheckin, Title: ci.Title, Body: ci.Body, Link: "/ask#goals"}); err != nil {
					errs = append(errs, err)
				}
			}
			return errors.Join(errs...)
		})
}
