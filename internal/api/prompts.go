// Clean-room: docs/provenance.md#CR-AGENT-TRIGGERS

package api

import (
	"context"
	"errors"
	"net/http"
	"time"

	"applysmith/internal/agent"
	"applysmith/internal/agent/goals"
	"applysmith/internal/agent/triggers"
	"applysmith/internal/api/apikit"
	"applysmith/internal/api/wire"
	"applysmith/internal/gmail"
	"applysmith/internal/notify"
	"applysmith/internal/scheduler"
)

// promptRoutes serve the scheduled prompts and the Gmail triggers. Confirming
// activates a proposal, so no tool and no Telegram callback reaches it and it
// stays off remote listeners.
var promptRoutes = []Route{
	route("GET", "/api/agent/prompts", reads("AgentPromptList")),
	route("POST", "/api/agent/prompts", sends("AgentPromptCreate", "AgentPrompt")),
	route("PATCH", "/api/agent/prompts/{id}", sends("AgentPromptPatch", "AgentPrompt")),
	route("DELETE", "/api/agent/prompts/{id}", reads("AgentPromptDeleted")),
	route("POST", "/api/agent/prompts/{id}/confirm", reads("AgentPrompt")),
}

// firingsShown is how many firings of a trigger a prompt carries.
const firingsShown = 5

func promptActor(p apikit.Principal) (triggers.Actor, bool) {
	switch p.Kind {
	case apikit.PrincipalDashboard:
		return triggers.ActorDashboard, true
	case apikit.PrincipalLocalToken:
		return triggers.ActorCLI, true
	}
	return "", false
}

func promptError(err error) error {
	switch {
	case errors.Is(err, triggers.ErrNotFound):
		return apikit.NewErr(wire.CodeNotFound, "no such scheduled prompt")
	case errors.Is(err, triggers.ErrLimit), errors.Is(err, triggers.ErrConflict):
		return apikit.NewErr(wire.CodeConflict, "%s", err.Error())
	case errors.Is(err, triggers.ErrInvalid), errors.Is(err, triggers.ErrUnsupported), errors.Is(err, goals.ErrInvalid):
		return apikit.NewErr(wire.CodeValidationFailed, "%s", err.Error())
	}
	return err
}

func (s *Server) promptStore() (*triggers.Store, error) {
	d, err := s.dom()
	if err != nil {
		return nil, err
	}
	return d.prompts, nil
}

func fromWireTrigger(t *wire.PromptTriggerSpec) triggers.Trigger {
	if t == nil {
		return triggers.Trigger{}
	}
	return triggers.Trigger{Types: t.Signals, Company: t.Company}
}

func (s *Server) toPrompt(ctx context.Context, st *triggers.Store, p triggers.Prompt) (wire.AgentPrompt, error) {
	out := wire.AgentPrompt{ID: p.ID, Kind: string(p.Kind), Text: p.Text, State: string(p.State), Origin: string(p.Origin), Firings: []wire.PromptFiring{},
		CreatedAt: wire.Time{Time: p.CreatedAt}, UpdatedAt: wire.Time{Time: p.UpdatedAt}}
	if p.Kind == triggers.KindCadence {
		c := toWireCadence(p.Cadence)
		out.Cadence = &c
		if next := st.NextSlot(p); !next.IsZero() {
			out.NextRun = &wire.Time{Time: next}
		}
	} else {
		out.Trigger = &wire.PromptTriggerSpec{Signals: p.Trigger.Types, Company: p.Trigger.Company}
		fs, err := st.Firings(ctx, p.ID, firingsShown)
		if err != nil {
			return out, err
		}
		for _, f := range fs {
			at := f.FinishedAt
			if at.IsZero() {
				at = f.ClaimedAt
			}
			out.Firings = append(out.Firings, wire.PromptFiring{SignalType: f.SignalType, Employer: f.Employer, State: f.State, Detail: f.Detail, SessionID: f.SessionID,
				Draft: f.Draft, ProposedStatus: f.ProposedStatus, ProposedReason: f.ProposedReason, At: wire.Time{Time: at}})
		}
	}
	if p.Last != nil {
		out.Last = &wire.PromptLast{At: wire.Time{Time: p.Last.At}, Status: p.Last.Status, SessionID: p.Last.SessionID, Summary: p.Last.Summary}
	}
	return out, nil
}

func (s *Server) listPrompts(c *apikit.Call) (apikit.Reply, error) {
	st, err := s.promptStore()
	if err != nil {
		return apikit.Reply{}, err
	}
	list, err := st.List(c.R.Context())
	if err != nil {
		return apikit.Reply{}, err
	}
	out := wire.AgentPromptList{Prompts: make([]wire.AgentPrompt, len(list)), Limit: apikit.Safe(triggers.MaxPrompts)}
	for i, p := range list {
		if out.Prompts[i], err = s.toPrompt(c.R.Context(), st, p); err != nil {
			return apikit.Reply{}, err
		}
	}
	return apikit.Reply{Data: out}, nil
}

func (s *Server) createPrompt(c *apikit.Call) (apikit.Reply, error) {
	var req wire.AgentPromptCreate
	if e := apikit.Decode(c.Body, &req); e != nil {
		return apikit.Reply{}, e
	}
	st, err := s.promptStore()
	if err != nil {
		return apikit.Reply{}, err
	}
	by, ok := promptActor(c.P)
	if !ok {
		return apikit.Reply{}, errUnauthenticated()
	}
	p, err := st.Create(c.R.Context(), triggers.NewPrompt{Text: req.Text, Cadence: fromWireCadence(req.Cadence), Trigger: fromWireTrigger(req.Trigger), Origin: triggers.OriginUser}, by)
	if err != nil {
		return apikit.Reply{}, promptError(err)
	}
	out, err := s.toPrompt(c.R.Context(), st, p)
	return apikit.Reply{Status: http.StatusCreated, Location: "/api/agent/prompts/" + p.ID, Data: out}, err
}

func (s *Server) patchPrompt(c *apikit.Call) (apikit.Reply, error) {
	var req wire.AgentPromptPatch
	if e := apikit.Decode(c.Body, &req); e != nil {
		return apikit.Reply{}, e
	}
	st, err := s.promptStore()
	if err != nil {
		return apikit.Reply{}, err
	}
	by, ok := promptActor(c.P)
	if !ok {
		return apikit.Reply{}, errUnauthenticated()
	}
	ctx, id := c.R.Context(), c.R.PathValue("id")
	if req.State != nil && *req.State != wire.PromptActive && *req.State != wire.PromptPaused {
		return apikit.Reply{}, apikit.NewErr(wire.CodeValidationFailed, "the state must be active or paused; use confirm for a proposal")
	}
	if req.State != nil {
		cur, err := st.Get(ctx, id)
		if err != nil {
			return apikit.Reply{}, promptError(err)
		}
		if cur.State != triggers.StateActive && cur.State != triggers.StatePaused {
			return apikit.Reply{}, apikit.NewErr(wire.CodeConflict, "the prompt is %s; confirm it first", cur.State)
		}
	}
	pt := triggers.Patch{Text: req.Text}
	if req.Cadence != nil {
		cad := fromWireCadence(req.Cadence)
		pt.Cadence = &cad
	}
	if req.Trigger != nil {
		tr := fromWireTrigger(req.Trigger)
		pt.Trigger = &tr
	}
	p, err := st.Update(ctx, id, pt, by)
	if err == nil && req.State != nil {
		switch {
		case *req.State == wire.PromptPaused && p.State == triggers.StateActive:
			p, err = st.Pause(ctx, id, by)
		case *req.State == wire.PromptActive && p.State == triggers.StatePaused:
			p, err = st.Resume(ctx, id, by)
		}
	}
	if err != nil {
		return apikit.Reply{}, promptError(err)
	}
	out, err := s.toPrompt(ctx, st, p)
	return apikit.Reply{Data: out}, err
}

func (s *Server) deletePrompt(c *apikit.Call) (apikit.Reply, error) {
	st, err := s.promptStore()
	if err != nil {
		return apikit.Reply{}, err
	}
	by, ok := promptActor(c.P)
	if !ok {
		return apikit.Reply{}, errUnauthenticated()
	}
	id := c.R.PathValue("id")
	if err := st.Delete(c.R.Context(), id, by); err != nil {
		return apikit.Reply{}, promptError(err)
	}
	return apikit.Reply{Data: wire.AgentPromptDeleted{ID: id}}, nil
}

func (s *Server) confirmPrompt(c *apikit.Call) (apikit.Reply, error) {
	st, err := s.promptStore()
	if err != nil {
		return apikit.Reply{}, err
	}
	by, ok := promptActor(c.P)
	if !ok {
		return apikit.Reply{}, errUnauthenticated()
	}
	p, err := st.Confirm(c.R.Context(), c.R.PathValue("id"), by)
	if err != nil {
		return apikit.Reply{}, promptError(err)
	}
	out, err := s.toPrompt(c.R.Context(), st, p)
	return apikit.Reply{Data: out}, err
}

// gmailSource reads the signal events and stored mail for the triggers.
type gmailSource struct{ repo *gmail.Repo }

func (g gmailSource) SignalEvents(ctx context.Context, after int64, limit int) ([]triggers.Event, error) {
	evs, err := g.repo.SignalEvents(ctx, after, limit)
	out := make([]triggers.Event, len(evs))
	for i, e := range evs {
		out[i] = triggers.Event{Seq: e.Seq, MessageID: e.MessageID, SignalType: e.SignalType, Tier: e.Tier, Direction: e.Direction, Employer: e.Employer, Received: e.Received}
	}
	return out, err
}

func (g gmailSource) Mail(ctx context.Context, id string) (triggers.Mail, error) {
	m, err := g.repo.StoredMail(ctx, id)
	return triggers.Mail{From: m.From, Subject: m.Subject, Body: m.Body}, err
}

// scheduledRunner runs a scheduled turn on the Ask Agent.
type scheduledRunner struct{ a *agent.Agent }

func (r scheduledRunner) RunScheduled(ctx context.Context, req triggers.Request) (triggers.Answer, error) {
	res, err := r.a.RunScheduled(ctx, agent.ScheduledRequest{Title: req.Title, Prompt: req.Prompt, Untrusted: req.Untrusted, Withhold: req.Withhold})
	return triggers.Answer{SessionID: res.SessionID, Status: res.Status, Detail: res.Detail, Text: res.Answer}, err
}

// schedulerLease holds the model lease for a firing batch.
type schedulerLease struct{ sc func() *scheduler.Scheduler }

func (l schedulerLease) Acquire(ctx context.Context) (func(), bool, error) {
	sc := l.sc()
	if sc == nil {
		return func() {}, true, nil
	}
	return sc.TryLeases(ctx, "ask-agent-triggers", scheduler.LeaseEvaluation)
}

// promptNotifier delivers a result as an announcement and a status proposal
// as an item of the needs-you inbox.
type promptNotifier struct{ ns *notifyStack }

func (n promptNotifier) Announce(ctx context.Context, a triggers.Announcement) error {
	ev := notify.Event{Key: a.Key, Kind: notify.EventPromptResult, Title: a.Title, Body: a.Body, Link: a.Link, NeverHold: true}
	if a.Trigger {
		ev.Kind, ev.NeverHold = notify.EventTriggerRes, false
	}
	return n.ns.policy.Announce(ctx, ev)
}

func (n promptNotifier) Propose(ctx context.Context, p triggers.StatusProposal) error {
	_, err := n.ns.board.Raise(ctx, notify.RaiseArgs{ID: p.Key, Kind: notify.KindAgent, Origin: notify.OriginAgent, Title: p.Title,
		State: notify.NeedsYou, Reason: "review", Detail: p.Detail, Link: p.Link})
	return err
}

// promptTimers are the two built-in timers over the engine.
func (s *Server) promptTimers(d *domain) []scheduler.Timer {
	return []scheduler.Timer{
		engineTimer{name: "scheduled-prompts", describe: "Scheduled Ask Agent prompts on a cadence, checked on each tick; the next run of each is on the Ask Agent page",
			tick: d.engine.TickPrompts},
		engineTimer{name: "signal-triggers", describe: "Gmail signal triggers, evaluated on each tick while Gmail is connected; at most five firings start per tick",
			tick: d.engine.TickTriggers, needsGmail: d.gmail.Service.Readable},
	}
}

type engineTimer struct {
	name, describe string
	tick           func(ctx context.Context, now time.Time) error
	needsGmail     func(ctx context.Context) bool
}

func (t engineTimer) Name() string     { return t.name }
func (t engineTimer) Describe() string { return t.describe }
func (t engineTimer) Status(ctx context.Context, _ time.Time) scheduler.TimerStatus {
	if t.needsGmail != nil && !t.needsGmail(ctx) {
		return scheduler.TimerStatus{Reason: gmailNotReadable}
	}
	return scheduler.TimerStatus{Active: true}
}
func (t engineTimer) Tick(ctx context.Context, now time.Time) error { return t.tick(ctx, now) }
