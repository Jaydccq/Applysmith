package api

import (
	"errors"

	"applysmith/internal/agent/schedprop"
	"applysmith/internal/api/apikit"
	"applysmith/internal/api/schedulesapi"
	"applysmith/internal/api/wire"
	"applysmith/internal/scheduler"
)

// scheduleProposalRoutes serve the proposals the agent made to change a
// schedule. Accepting applies the change to the scheduler, so it stays off
// remote listeners; no tool and no Telegram callback reaches these routes.
var scheduleProposalRoutes = []Route{
	route("GET", "/api/agent/schedule-proposals", reads("ScheduleProposalList")),
	route("POST", "/api/agent/schedule-proposals/{id}/accept", reads("ScheduleProposalDecided")),
	route("POST", "/api/agent/schedule-proposals/{id}/dismiss", reads("ScheduleProposalDecided")),
	route("POST", "/api/agent/schedule-proposals/{id}/restore", reads("ScheduleProposal")),
}

func scheduleProposalActor(p apikit.Principal) (schedprop.Actor, bool) {
	switch p.Kind {
	case apikit.PrincipalDashboard:
		return schedprop.ActorDashboard, true
	case apikit.PrincipalLocalToken:
		return schedprop.ActorCLI, true
	}
	return "", false
}

func scheduleProposalError(err error) error {
	if errors.Is(err, schedprop.ErrNotFound) {
		return apikit.NewErr(wire.CodeNotFound, "no such schedule proposal")
	}
	return err
}

func toWireScheduleProposal(p schedprop.Proposal, name string) wire.ScheduleProposal {
	out := wire.ScheduleProposal{ID: p.ID, ScheduleID: apikit.Safe64(p.ScheduleID), ScheduleName: name, Reason: p.Reason,
		Change: wire.ScheduleProposalChange{Enabled: p.Change.Enabled}, CreatedAt: wire.Time{Time: p.CreatedAt}}
	if pre := p.Change.Preset; pre != nil {
		out.Change.Preset = &wire.SchedulePreset{Kind: pre.Kind, Time: pre.Time, Weekdays: pre.Weekdays, RRule: pre.RRule, Timezone: pre.Timezone}
	}
	return out
}

func (s *Server) listScheduleProposals(c *apikit.Call) (apikit.Reply, error) {
	d, err := s.dom()
	if err != nil {
		return apikit.Reply{}, err
	}
	list, err := d.schedProps.List(c.R.Context())
	if err != nil {
		return apikit.Reply{}, err
	}
	names := map[int64]string{}
	if sc := d.sched; sc != nil {
		if views, err := sc.Manager.List(c.R.Context()); err == nil {
			for _, v := range views {
				names[v.ID] = v.Name
			}
		}
	}
	out := wire.ScheduleProposalList{Proposals: make([]wire.ScheduleProposal, len(list))}
	for i, p := range list {
		out.Proposals[i] = toWireScheduleProposal(p, names[p.ScheduleID])
	}
	return apikit.Reply{Data: out}, nil
}

func (s *Server) acceptScheduleProposal(c *apikit.Call) (apikit.Reply, error) {
	return s.decideScheduleProposal(c, true)
}

func (s *Server) dismissScheduleProposal(c *apikit.Call) (apikit.Reply, error) {
	return s.decideScheduleProposal(c, false)
}

// restoreScheduleProposal is the undo of a dismissal: the proposal waits again
// as it was.
func (s *Server) restoreScheduleProposal(c *apikit.Call) (apikit.Reply, error) {
	d, err := s.dom()
	if err != nil {
		return apikit.Reply{}, err
	}
	by, ok := scheduleProposalActor(c.P)
	if !ok {
		return apikit.Reply{}, errUnauthenticated()
	}
	p, err := d.schedProps.Restore(c.R.Context(), c.R.PathValue("id"), by)
	switch {
	case errors.Is(err, schedprop.ErrNotFound):
		return apikit.Reply{}, apikit.NewErr(wire.CodeNotFound, "only the suggestion dismissed last since the app started can be brought back")
	case errors.Is(err, schedprop.ErrReplaced):
		return apikit.Reply{}, apikit.NewErr(wire.CodeConflict, "the Ask Agent has suggested a newer change to this schedule since")
	case errors.Is(err, schedprop.ErrScheduleGone):
		return apikit.Reply{}, apikit.NewErr(wire.CodeNotFound, "the schedule this suggestion was for has been deleted")
	case err != nil:
		return apikit.Reply{}, err
	}
	name := ""
	if sc := d.sched; sc != nil {
		if v, err := sc.Manager.View(c.R.Context(), p.ScheduleID); err == nil {
			name = v.Name
		}
	}
	return apikit.Reply{Data: toWireScheduleProposal(p, name)}, nil
}

// decideScheduleProposal ends a pending proposal. An accept first applies the
// change through the scheduler's own validation; a change the scheduler
// refuses leaves the proposal pending.
func (s *Server) decideScheduleProposal(c *apikit.Call, accept bool) (apikit.Reply, error) {
	d, err := s.dom()
	if err != nil {
		return apikit.Reply{}, err
	}
	by, ok := scheduleProposalActor(c.P)
	if !ok {
		return apikit.Reply{}, errUnauthenticated()
	}
	ctx, id := c.R.Context(), c.R.PathValue("id")
	prop, err := d.schedProps.Get(ctx, id)
	if err != nil {
		return apikit.Reply{}, scheduleProposalError(err)
	}
	out := wire.ScheduleProposalDecided{ID: id, Accepted: accept}
	if accept {
		sc, err := s.scheduler()
		if err != nil {
			return apikit.Reply{}, err
		}
		cur, err := sc.Manager.View(ctx, prop.ScheduleID)
		if err != nil {
			return apikit.Reply{}, schedulesapi.ScheduleError(err)
		}
		patch := scheduler.Patch{Enabled: prop.Change.Enabled}
		if pre := prop.Change.Preset; pre != nil {
			p := scheduler.Preset{Kind: pre.Kind, Time: pre.Time, Weekdays: pre.Weekdays, RRule: pre.RRule, Timezone: pre.Timezone}
			if p.Timezone == "" {
				p.Timezone = cur.Preset.Timezone
			}
			patch.Preset = &p
		}
		if _, err := sc.Manager.Update(ctx, prop.ScheduleID, patch); err != nil {
			return apikit.Reply{}, schedulesapi.ScheduleError(err)
		}
		view, err := sc.Manager.View(ctx, prop.ScheduleID)
		if err != nil {
			return apikit.Reply{}, schedulesapi.ScheduleError(err)
		}
		w := schedulesapi.WireSchedule(view)
		out.Schedule = &w
		s.notify("schedules")
	}
	if err := d.schedProps.Resolve(ctx, id, accept, by); err != nil {
		return apikit.Reply{}, scheduleProposalError(err)
	}
	return apikit.Reply{Data: out}, nil
}
