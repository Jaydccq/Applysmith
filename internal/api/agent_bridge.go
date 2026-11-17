package api

import (
	"errors"

	"applysmith/internal/agent"
	"applysmith/internal/api/apikit"
	"applysmith/internal/api/wire"
	"applysmith/internal/llm"
)

// The routes below are the bridge the extension inside a pi process calls. The
// credential was checked before the handler runs; each handler reads it again
// to find the turn it belongs to.

func (s *Server) bridge() (*agent.Bridge, error) {
	d, err := s.dom()
	if err != nil {
		return nil, err
	}
	return d.agentBridge, nil
}

func processToken(c *apikit.Call) string {
	tok, _ := bearerFrom(c.R)
	return tok
}

// bridgeError maps the bridge's failures onto the API errors.
func bridgeError(err error) error {
	switch {
	case errors.Is(err, agent.ErrBridgeAuth):
		return errUnauthenticated()
	case errors.Is(err, agent.ErrCatalogMismatch):
		return apikit.NewErr(wire.CodeConflict, "%s", err.Error())
	}
	return err
}

func (s *Server) agentCatalog(c *apikit.Call) (apikit.Reply, error) {
	b, err := s.bridge()
	if err != nil {
		return apikit.Reply{}, err
	}
	out, err := b.Catalog(processToken(c))
	return apikit.Reply{Data: out}, bridgeError(err)
}

func (s *Server) agentHello(c *apikit.Call) (apikit.Reply, error) {
	b, err := s.bridge()
	if err != nil {
		return apikit.Reply{}, err
	}
	var req wire.AgentHello
	if e := apikit.Decode(c.Body, &req); e != nil {
		return apikit.Reply{}, e
	}
	out, err := b.Hello(processToken(c), req)
	return apikit.Reply{Data: out}, bridgeError(err)
}

func (s *Server) agentTurnContext(c *apikit.Call) (apikit.Reply, error) {
	b, err := s.bridge()
	if err != nil {
		return apikit.Reply{}, err
	}
	out, err := b.TurnContext(c.R.Context(), processToken(c))
	return apikit.Reply{Data: out}, bridgeError(err)
}

func (s *Server) agentToolCall(c *apikit.Call) (apikit.Reply, error) {
	b, err := s.bridge()
	if err != nil {
		return apikit.Reply{}, err
	}
	var req wire.AgentToolCall
	if e := apikit.Decode(c.Body, &req); e != nil {
		return apikit.Reply{}, e
	}
	out, err := b.ToolCall(c.R.Context(), processToken(c), req)
	return apikit.Reply{Data: out}, bridgeError(err)
}

// agentModel relays a model call of the process's turn. Once the gateway has
// written a reply the handler is done; a call that failed before is answered
// as an error, whose message pi shows.
func (s *Server) agentModel(wireName string) apikit.Handler {
	return func(c *apikit.Call) (apikit.Reply, error) {
		b, err := s.bridge()
		if err != nil {
			return apikit.Reply{}, err
		}
		call := llm.AgentCall{Wire: wireName, Body: c.Body, Beta: c.R.Header.Get("anthropic-beta")}
		if err := b.ModelCall(c.R.Context(), processToken(c), call, c.W); err != nil {
			return apikit.Reply{}, modelError(bridgeError(err))
		}
		c.Handled = true
		return apikit.Reply{}, nil
	}
}

// modelError maps a gateway failure onto an API error.
func modelError(err error) error {
	var le *llm.Error
	if !errors.As(err, &le) {
		return err
	}
	text := llm.AgentErrorText(le)
	switch le.Kind {
	case llm.KindBadRequest:
		return apikit.NewErr(wire.CodeBadRequest, "%s", text)
	case llm.KindRateLimited, llm.KindQuota, llm.KindCostCap:
		return apikit.NewErr(wire.CodeRateLimited, "%s", text)
	}
	return apikit.NewErr(wire.CodeUnavailable, "%s", text)
}
