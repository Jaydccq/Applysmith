package api

import (
	"errors"

	"applysmith/internal/agent"
	"applysmith/internal/agent/approvals"
	"applysmith/internal/api/apikit"
	"applysmith/internal/api/wire"
	"applysmith/internal/store"
)

// confirmationRoutes serve the confirmation cards of held Ask Agent calls and
// the standing approvals made from them. The specification lets a remote device
// approve once and revoke, so those two routes are remote-allowed and the
// handlers treat a remote principal that way. A wider approval is the dashboard
// session on the loopback listener alone, and so is the approval of a held
// submit.
var confirmationRoutes = []Route{
	route("GET", "/api/agent/confirmations", reads("ConfirmationList")),
	route("POST", "/api/agent/confirmations/{id}/approve", remoteReads("ConfirmationOutcome")),
	route("POST", "/api/agent/confirmations/{id}/approve-standing", func(r *Route) {
		sends("ApproveStanding", "ConfirmationOutcome")(r)
		r.SessionOnly = true
	}),
	route("POST", "/api/agent/confirmations/{id}/decline", reads("ConfirmationOutcome")),
	route("GET", "/api/agent/approvals", reads("StandingApprovalList")),
	route("DELETE", "/api/agent/approvals/{id}", remoteReads("StandingApprovalRevoked")),
}

func (s *Server) confirmations() (*agent.Confirmations, error) {
	d, err := s.dom()
	if err != nil {
		return nil, err
	}
	return d.confirmations, nil
}

// actorOf maps the credential of a request to who is deciding.
func actorOf(p apikit.Principal) (approvals.Actor, bool) {
	switch p.Kind {
	case apikit.PrincipalDashboard:
		return approvals.ActorDashboard, true
	case apikit.PrincipalLocalToken:
		return approvals.ActorLocalToken, true
	case apikit.PrincipalRemoteDevice:
		return approvals.ActorRemoteDevice, true
	}
	return "", false
}

// approvalError maps the store's refusals onto the API's answers.
func approvalError(err error) error {
	switch {
	case errors.Is(err, approvals.ErrNotFound), errors.Is(err, store.ErrNotFound):
		return apikit.NewErr(wire.CodeNotFound, "no such card or approval")
	case errors.Is(err, approvals.ErrConflict):
		return apikit.NewErr(wire.CodeConflict, "%s", err.Error())
	case errors.Is(err, approvals.ErrLimit):
		return apikit.NewErr(wire.CodeConflict, "%s", err.Error())
	case errors.Is(err, approvals.ErrNotPermitted):
		return apikit.NewErr(wire.CodeForbidden, "this credential may not make this decision; a held submit is confirmed in the dashboard")
	case errors.Is(err, approvals.ErrIneligible):
		return apikit.InvalidField("scope", "this call can only be approved once")
	}
	return err
}

func toCard(c approvals.Card) wire.ConfirmationCard {
	out := wire.ConfirmationCard{ID: c.ID, SessionID: c.SessionID, TurnID: c.TurnID, Channel: c.Channel, Tool: c.Tool, Summary: c.Summary,
		Company: c.Company, Role: c.Role, Host: c.Host, Tier: apikit.Safe(c.Tier), Code: c.Code, Reason: c.Reason, Args: c.ArgsShown, Purpose: c.Purpose,
		State: string(c.State), CreatedAt: wire.Time{Time: c.CreatedAt}, ExpiresAt: wire.Time{Time: c.ExpiresAt}}
	for _, sc := range c.Scopes() {
		out.Scopes = append(out.Scopes, string(sc))
	}
	if c.DecidedAt != nil {
		out.DecidedAt = &wire.Time{Time: *c.DecidedAt}
	}
	return out
}

func toApproval(a approvals.Approval) wire.StandingApproval {
	out := wire.StandingApproval{ID: a.ID, Tool: a.Tool, Target: a.Target, Scope: string(a.Scope), SessionID: a.SessionID,
		CreatedAt: wire.Time{Time: a.CreatedAt}, ExpiresAt: wire.Time{Time: a.ExpiresAt}, Uses: apikit.Safe(a.Uses)}
	if a.LastUsedAt != nil {
		out.LastUsedAt = &wire.Time{Time: *a.LastUsedAt}
	}
	return out
}

func (s *Server) listConfirmations(c *apikit.Call) (apikit.Reply, error) {
	conf, err := s.confirmations()
	if err != nil {
		return apikit.Reply{}, err
	}
	cards, err := conf.Pending(c.R.Context())
	if err != nil {
		return apikit.Reply{}, err
	}
	out := wire.ConfirmationList{Cards: make([]wire.ConfirmationCard, len(cards))}
	for i, card := range cards {
		out.Cards[i] = toCard(card)
	}
	return apikit.Reply{Data: out}, nil
}

func (s *Server) approveConfirmation(c *apikit.Call) (apikit.Reply, error) {
	return s.decideConfirmation(c, approvals.Once)
}

func (s *Server) approveConfirmationStanding(c *apikit.Call) (apikit.Reply, error) {
	var req wire.ApproveStanding
	if e := apikit.Decode(c.Body, &req); e != nil {
		return apikit.Reply{}, e
	}
	scope := approvals.Scope(req.Scope)
	if scope != approvals.Conversation && scope != approvals.Today {
		return apikit.Reply{}, apikit.InvalidField("scope", "scope must be conversation or today")
	}
	return s.decideConfirmation(c, scope)
}

func (s *Server) decideConfirmation(c *apikit.Call, scope approvals.Scope) (apikit.Reply, error) {
	conf, err := s.confirmations()
	if err != nil {
		return apikit.Reply{}, err
	}
	by, ok := actorOf(c.P)
	if !ok {
		return apikit.Reply{}, errUnauthenticated()
	}
	res, err := conf.Approve(c.R.Context(), c.R.PathValue("id"), by, scope)
	if err != nil {
		return apikit.Reply{}, approvalError(err)
	}
	out := wire.ConfirmationOutcome{Card: toCard(res.Card), Result: res.Result}
	if res.Approval != nil {
		a := toApproval(*res.Approval)
		out.Approval = &a
	}
	return apikit.Reply{Data: out}, nil
}

func (s *Server) declineConfirmation(c *apikit.Call) (apikit.Reply, error) {
	conf, err := s.confirmations()
	if err != nil {
		return apikit.Reply{}, err
	}
	by, ok := actorOf(c.P)
	if !ok {
		return apikit.Reply{}, errUnauthenticated()
	}
	card, err := conf.Decline(c.R.Context(), c.R.PathValue("id"), by)
	if err != nil {
		return apikit.Reply{}, approvalError(err)
	}
	return apikit.Reply{Data: wire.ConfirmationOutcome{Card: toCard(card)}}, nil
}

func (s *Server) listApprovals(c *apikit.Call) (apikit.Reply, error) {
	conf, err := s.confirmations()
	if err != nil {
		return apikit.Reply{}, err
	}
	list, err := conf.Active(c.R.Context())
	if err != nil {
		return apikit.Reply{}, err
	}
	out := wire.StandingApprovalList{Approvals: make([]wire.StandingApproval, len(list))}
	for i, a := range list {
		out.Approvals[i] = toApproval(a)
	}
	return apikit.Reply{Data: out}, nil
}

func (s *Server) revokeApproval(c *apikit.Call) (apikit.Reply, error) {
	conf, err := s.confirmations()
	if err != nil {
		return apikit.Reply{}, err
	}
	by, ok := actorOf(c.P)
	if !ok {
		return apikit.Reply{}, errUnauthenticated()
	}
	a, err := conf.Revoke(c.R.Context(), c.R.PathValue("id"), by)
	if err != nil {
		return apikit.Reply{}, approvalError(err)
	}
	return apikit.Reply{Data: wire.StandingApprovalRevoked{ID: a.ID}}, nil
}
