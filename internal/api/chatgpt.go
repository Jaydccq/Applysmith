package api

import (
	"errors"

	"applysmith/internal/api/apikit"
	"applysmith/internal/api/wire"
	"applysmith/internal/llm"
)

// The chatgpt provider signs in with the user's ChatGPT plan. The server owns
// the token family (it alone refreshes it), so every sign-in and sign-out goes
// through these routes, from the dashboard or from the command line.

func chatgptStatusView(st llm.ChatGPTStatus) wire.ChatGPTStatus {
	return wire.ChatGPTStatus{SignedIn: st.SignedIn, PlanUsage: st.PlanUsage, Account: st.Account, Pending: st.Pending, Message: st.Message}
}

func (s *Server) chatgptStatus(c *apikit.Call) (apikit.Reply, error) {
	d, err := s.dom()
	if err != nil {
		return apikit.Reply{}, err
	}
	st, err := d.router.ChatGPTStatus(c.R.Context())
	if err != nil {
		return apikit.Reply{}, err
	}
	return apikit.Reply{Data: chatgptStatusView(st)}, nil
}

// chatgptSignIn starts the loopback sign-in and answers with the address the
// user opens in the system browser. The server finishes it when the browser
// comes back; the status route reports the outcome.
func (s *Server) chatgptSignIn(c *apikit.Call) (apikit.Reply, error) {
	d, err := s.dom()
	if err != nil {
		return apikit.Reply{}, err
	}
	u, err := d.router.BeginChatGPTSignIn(c.R.Context())
	if err != nil {
		return apikit.Reply{}, apikit.Unavailable(err.Error())
	}
	return apikit.Reply{Data: wire.ChatGPTSignInStarted{URL: u}}, nil
}

func (s *Server) chatgptSignOut(c *apikit.Call) (apikit.Reply, error) {
	var req wire.ChatGPTSignOutRequest
	if len(c.Body) > 0 {
		if e := apikit.Decode(c.Body, &req); e != nil {
			return apikit.Reply{}, e
		}
	}
	d, err := s.dom()
	if err != nil {
		return apikit.Reply{}, err
	}
	revoked, err := d.router.SignOutChatGPT(c.R.Context(), req.Forget)
	if err != nil {
		return apikit.Reply{}, vaultError(err)
	}
	return apikit.Reply{Data: wire.ChatGPTSignOutResult{Revoked: revoked}}, nil
}

func (s *Server) chatgptModels(c *apikit.Call) (apikit.Reply, error) {
	d, err := s.dom()
	if err != nil {
		return apikit.Reply{}, err
	}
	models, err := d.router.ChatGPTModels(c.R.Context())
	var le *llm.Error
	switch {
	case errors.As(err, &le) && le.Kind == llm.KindUnconfigured:
		return apikit.Reply{}, apikit.NewErr(wire.CodeConflict, "%s", le.Message)
	case err != nil:
		return apikit.Reply{}, modelError(err)
	}
	out := wire.ChatGPTModels{Models: make([]wire.ChatGPTModel, len(models))}
	for i, m := range models {
		out.Models[i] = wire.ChatGPTModel{Slug: m.Slug}
	}
	return apikit.Reply{Data: out}, nil
}
