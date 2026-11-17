package api

import (
	"errors"

	"applysmith/internal/api/apikit"
	"applysmith/internal/api/wire"
	"applysmith/internal/captcha"
	"applysmith/internal/credentials"
	"applysmith/internal/gates"
	"applysmith/internal/llm"
	"applysmith/internal/vault"
)

// secretError maps a vault or management failure onto the API. No message
// carries a value: the vault builds its own texts and the weak-password text
// names the rules only.
func secretError(err error) error {
	var weak *credentials.WeakPasswordError
	var ve *vault.Error
	switch {
	case errors.As(err, &weak):
		return apikit.WithReason(apikit.NewErr(wire.CodeBadRequest, "%s", weak.Error()), "weak_password")
	case errors.Is(err, credentials.ErrEmailRequired):
		return apikit.WithReason(apikit.NewErr(wire.CodeBadRequest, "a registration email is needed first: set the shared default's email, or give one with this request"), "email_required")
	case errors.Is(err, credentials.ErrBadEmail), errors.Is(err, credentials.ErrNothingToSet), errors.Is(err, credentials.ErrLabelTooLong),
		errors.Is(err, credentials.ErrBadOrder), errors.Is(err, credentials.ErrBadSite), errors.Is(err, credentials.ErrUseAddAlternate):
		return apikit.NewErr(wire.CodeBadRequest, "%s", err.Error())
	case errors.As(err, &ve):
		switch ve.Code {
		case vault.CodeEmpty, vault.CodeTooLarge, vault.CodeNameInvalid:
			return apikit.WithReason(apikit.NewErr(wire.CodeBadRequest, "%s", ve.Message), ve.Code)
		case vault.CodeNotFound:
			return apikit.WithReason(apikit.NewErr(wire.CodeNotFound, "no secret is stored under this name"), ve.Code)
		case vault.CodeEnvManaged:
			return apikit.WithReason(apikit.NewErr(wire.CodeConflict, "%s", ve.Message), ve.Code)
		}
		return apikit.WithReason(apikit.NewErr(wire.CodeUnavailable, "%s", ve.Message), ve.Code)
	}
	return apikit.Unavailable("the credential index could not be read or written; retry")
}

func secretEntries(entries []credentials.Entry) []wire.SecretEntry {
	out := make([]wire.SecretEntry, len(entries))
	for i, e := range entries {
		out[i] = wire.SecretEntry{Name: e.Name, Kind: e.Kind, Label: e.Label, Email: e.Email, Host: e.Host, System: e.System, Tenant: e.Tenant,
			Position: e.Position, Present: e.Present, Source: string(e.Source), Problem: e.Problem,
			UpdatedAt: apikit.StampPtr(e.UpdatedAt), LastSigninAt: apikit.StampPtr(e.LastSigninAt), LastOutcome: e.LastOutcome}
	}
	return out
}

func (s *Server) secretList(c *apikit.Call) (wire.SecretList, error) {
	d, err := s.dom()
	if err != nil {
		return wire.SecretList{}, err
	}
	entries, err := d.creds.Entries(c.R.Context())
	if err != nil {
		return wire.SecretList{}, secretError(err)
	}
	list := wire.SecretList{Registry: []wire.SecretRegistryRow{}, Entries: secretEntries(entries)}
	for _, r := range vault.Registry() {
		list.Registry = append(list.Registry, wire.SecretRegistryRow{Kind: r.Kind, Name: r.NamePattern, Description: r.Description,
			Env: r.EnvPattern, SetupStep: r.SetupStep, Required: r.Required})
	}
	return list, nil
}

// listSecrets lists the registry and the stored entries: presence and source,
// never a value.
func (s *Server) listSecrets(c *apikit.Call) (apikit.Reply, error) {
	list, err := s.secretList(c)
	return apikit.Reply{Data: list}, err
}

func (s *Server) secretStatus(name string, shadowedBy string) wire.SecretStatus {
	src, err := s.cfg.Vault.Test(name)
	return wire.SecretStatus{Name: name, Present: err == nil, Source: string(src), ShadowedBy: shadowedBy}
}

// putSecret sets one secret. A shared default password must satisfy the five
// rules; an override and any other kind accept any value the vault accepts.
func (s *Server) putSecret(c *apikit.Call) (apikit.Reply, error) {
	d, err := s.dom()
	if err != nil {
		return apikit.Reply{}, err
	}
	name := c.R.PathValue("name")
	var req wire.SecretWrite
	if e := apikit.Decode(c.Body, &req); e != nil {
		return apikit.Reply{}, e
	}
	row, err := vault.Lookup(name)
	if err != nil {
		return apikit.Reply{}, secretError(err)
	}
	if row.Kind == "api_key" {
		if err := llm.ValidateAPIKey(req.Value); err != nil {
			return apikit.Reply{}, apikit.WithReason(apikit.NewErr(wire.CodeBadRequest, "%s", err.Error()), "key_invalid")
		}
	}
	res, err := d.creds.Set(c.R.Context(), name, req.Value, req.Email, req.Label)
	if err != nil {
		return apikit.Reply{}, secretError(err)
	}
	return apikit.Reply{Data: s.secretStatus(name, res.ShadowedBy)}, nil
}

// deleteSecret removes one secret. A secret that only the environment provides
// answers `conflict` with the variable to unset.
func (s *Server) deleteSecret(c *apikit.Call) (apikit.Reply, error) {
	d, err := s.dom()
	if err != nil {
		return apikit.Reply{}, err
	}
	name := c.R.PathValue("name")
	if err := d.creds.Delete(c.R.Context(), name); err != nil {
		return apikit.Reply{}, secretError(err)
	}
	return apikit.Reply{Data: s.secretStatus(name, "")}, nil
}

// testSecret confirms the secret can be read and from where. The captcha key
// alone has a live check; any other readable secret reports `unsupported`.
func (s *Server) testSecret(c *apikit.Call) (apikit.Reply, error) {
	d, err := s.dom()
	if err != nil {
		return apikit.Reply{}, err
	}
	name := c.R.PathValue("name")
	src, err := d.creds.Test(name)
	switch {
	case err == nil && name == vault.CaptchaKey:
		return s.testCaptchaKey(c, wire.SecretTest{Name: name, Present: true, Source: string(src)})
	case err == nil:
		return apikit.Reply{Data: wire.SecretTest{Name: name, Present: true, Source: string(src), Live: "unsupported"}}, nil
	case vault.IsCode(err, vault.CodeNotFound):
		return apikit.Reply{Data: wire.SecretTest{Name: name, Live: "not_found"}}, nil
	}
	return apikit.Reply{}, secretError(err)
}

// testCaptchaKey reads the solver account's balance: one request, on the user's press.
// The key goes to the service only; no reply text comes back, just the code.
func (s *Server) testCaptchaKey(c *apikit.Call, t wire.SecretTest) (apikit.Reply, error) {
	key, err := s.cfg.Vault.Get(vault.CaptchaKey)
	if err != nil {
		return apikit.Reply{}, secretError(err)
	}
	micro, err := captcha.Balance(c.R.Context(), captcha.Config{Key: func() string { return key }, BaseURL: s.cfg.Domain.CaptchaAPI})
	switch {
	case err == nil:
		cents := wire.SafeInt(micro / 10_000)
		t.Live, t.BalanceCents, t.BelowFloor = "ok", &cents, micro < gates.CaptchaBalanceFloorMicroUSD
	case captcha.KeyRejected(err):
		t.Live = "rejected"
	case c.R.Context().Err() != nil:
		return apikit.Reply{}, err
	default:
		t.Live, t.Detail = "failed", err.Error()
	}
	return apikit.Reply{Data: t}, nil
}

// addAlternate appends an alternate shared password and returns its id.
func (s *Server) addAlternate(c *apikit.Call) (apikit.Reply, error) {
	d, err := s.dom()
	if err != nil {
		return apikit.Reply{}, err
	}
	var req wire.AlternateWrite
	if e := apikit.Decode(c.Body, &req); e != nil {
		return apikit.Reply{}, e
	}
	id, err := d.creds.AddAlternate(c.R.Context(), req.Label, req.Value)
	if err != nil {
		return apikit.Reply{}, secretError(err)
	}
	return apikit.Reply{Data: wire.AlternateAdded{ID: id, Name: "signin-alternate-" + id}}, nil
}

// orderAlternates stores a new try order.
func (s *Server) orderAlternates(c *apikit.Call) (apikit.Reply, error) {
	d, err := s.dom()
	if err != nil {
		return apikit.Reply{}, err
	}
	var req wire.AlternateOrder
	if e := apikit.Decode(c.Body, &req); e != nil {
		return apikit.Reply{}, e
	}
	if err := d.creds.ReorderAlternates(c.R.Context(), req.IDs); err != nil {
		return apikit.Reply{}, secretError(err)
	}
	list, err := s.secretList(c)
	return apikit.Reply{Data: list}, err
}

// removeAlternate deletes one alternate by id.
func (s *Server) removeAlternate(c *apikit.Call) (apikit.Reply, error) {
	d, err := s.dom()
	if err != nil {
		return apikit.Reply{}, err
	}
	if err := d.creds.Delete(c.R.Context(), "signin-alternate-"+c.R.PathValue("id")); err != nil {
		return apikit.Reply{}, secretError(err)
	}
	list, err := s.secretList(c)
	return apikit.Reply{Data: list}, err
}
