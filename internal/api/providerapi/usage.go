package providerapi

import (
	"errors"
	"strings"
	"time"

	"applysmith/internal/api/apikit"
	"applysmith/internal/api/wire"
	"applysmith/internal/providerusage"
	"applysmith/internal/vault"
)

func usageWire(u providerusage.Usage) wire.ProviderUsage {
	out := wire.ProviderUsage{Status: u.Status, Message: u.Message, CheckedAt: instant(u.CheckedAt), Windows: []wire.ProviderUsageWindow{}}
	for _, w := range u.Windows {
		out.Windows = append(out.Windows, wire.ProviderUsageWindow{Name: w.Name, Used: w.Used, Limit: w.Limit, Remaining: w.Remaining, UsedPercent: w.UsedPercent, ResetsAt: instant(w.ResetsAt)})
	}
	return out
}

// instant is t on the wire, nil when it is unknown.
func instant(t time.Time) *wire.Time {
	if t.IsZero() {
		return nil
	}
	return &wire.Time{Time: t}
}
func (s *Handlers) providerAccountView(d providerusage.Definition) wire.ProviderAccount {
	out := wire.ProviderAccount{ID: d.ID, Name: d.Name, Mode: d.Mode, LoginCommand: d.LoginCommand, ConsoleURL: d.ConsoleURL, Note: d.Note, CookieSupported: d.CookieSupported, CredentialStatus: "missing"}
	u := providerusage.Unknown("unconfigured", "Save a plan credential before refreshing.")
	if d.Mode == "native" {
		state := providerusage.Native(d)
		out.Installed = state.Installed
		out.AuthFilePresent = state.AuthFilePresent
		out.CredentialStatus = "unknown"
		out.Note += " Uses this server host's native login. Installation and auth-file presence do not verify sign-in. Managed account switching is not supported; sign in through the native CLI."
		u = providerusage.Unknown("unsupported", "Native subscription usage is available in the provider's app or console.")
	} else if s.Vault() != nil {
		credentialError := false
		check := func(kind string) bool {
			err := s.Vault().Check(providerusage.SecretName(d.ID, kind))
			if err != nil && !errors.Is(err, vault.ErrNotFound) {
				credentialError = true
			}
			return err == nil
		}
		out.KeyConfigured = check("key")
		if d.CookieSupported {
			out.CookieConfigured = check("cookie")
		}
		if d.ID == "opencode-go" {
			out.WorkspaceConfigured = check("workspace")
		}
		if out.KeyConfigured || out.CookieConfigured {
			out.CredentialStatus = "configured"
			u = providerusage.Unknown("unknown", "Refresh to retrieve subscription quota.")
		} else if credentialError {
			out.CredentialStatus = "unknown"
			u = providerusage.Unknown("error", "The credential vault is unavailable. Unlock the keychain and try again.")
		}

	}
	if cached, ok := s.Usage.Get(d.ID); ok {
		u = cached
	}
	out.Usage = usageWire(u)
	return out
}
func (s *Handlers) Accounts(c *apikit.Call) (apikit.Reply, error) {
	out := wire.ProviderAccounts{Accounts: []wire.ProviderAccount{}}
	for _, d := range providerusage.Definitions() {
		out.Accounts = append(out.Accounts, s.providerAccountView(d))
	}
	return apikit.Reply{Data: out}, nil
}
func accountDefinition(c *apikit.Call) (providerusage.Definition, error) {
	d, ok := providerusage.Lookup(c.R.PathValue("id"))
	if !ok {
		return d, apikit.NewErr(wire.CodeNotFound, "no such provider account")
	}
	return d, nil
}
func (s *Handlers) SetCredential(c *apikit.Call) (apikit.Reply, error) {
	d, err := accountDefinition(c)
	if err != nil {
		return apikit.Reply{}, err
	}
	var req wire.ProviderCredentialRequest
	if e := apikit.Decode(c.Body, &req); e != nil {
		return apikit.Reply{}, e
	}
	req.Value = strings.TrimSpace(req.Value)
	if err := providerusage.ValidateCredential(d, req.Kind, req.Value); err != nil {
		return apikit.Reply{}, apikit.InvalidField("value", "%s", err.Error())
	}
	if s.Vault() == nil {
		return apikit.Reply{}, apikit.Unavailable("the keychain is not available")
	}
	if _, err := s.Vault().Set(providerusage.SecretName(d.ID, req.Kind), req.Value); err != nil {
		return apikit.Reply{}, s.VaultError(err)
	}
	s.Usage.Clear(d.ID)
	s.Notify("configuration")
	return apikit.Reply{Data: s.providerAccountView(d)}, nil
}
func (s *Handlers) DeleteCredential(c *apikit.Call) (apikit.Reply, error) {
	d, err := accountDefinition(c)
	if err != nil {
		return apikit.Reply{}, err
	}
	kind := c.R.PathValue("kind")
	placeholder := "valid"
	if kind == "cookie" {
		placeholder = "auth=valid"
	}
	if kind == "workspace" {
		placeholder = "wrk_valid"
	}
	if err := providerusage.ValidateCredential(d, kind, placeholder); err != nil {
		return apikit.Reply{}, apikit.InvalidField("kind", "%s", err.Error())
	}
	if s.Vault() == nil {
		return apikit.Reply{}, apikit.Unavailable("the keychain is not available")
	}
	if err := s.Vault().Delete(providerusage.SecretName(d.ID, kind)); err != nil && !errors.Is(err, vault.ErrNotFound) {
		return apikit.Reply{}, s.VaultError(err)
	}
	s.Usage.Clear(d.ID)
	s.Notify("configuration")
	return apikit.Reply{Data: s.providerAccountView(d)}, nil
}
func (s *Handlers) Refresh(c *apikit.Call) (apikit.Reply, error) {
	d, err := accountDefinition(c)
	if err != nil {
		return apikit.Reply{}, err
	}
	if !s.Usage.Begin(d.ID) {
		return apikit.Reply{}, apikit.NewErr(wire.CodeConflict, "a usage refresh is already running for this provider")
	}
	creds := providerusage.Credentials{}
	if d.Mode == "subscription" {
		if s.Vault() == nil {
			s.Usage.End(d.ID, providerusage.Unknown("error", "The credential vault is unavailable."))
			return apikit.Reply{Data: s.providerAccountView(d)}, nil
		}
		get := func(kind string) (string, error) {
			v, err := s.Vault().Get(providerusage.SecretName(d.ID, kind))
			if errors.Is(err, vault.ErrNotFound) {
				return "", nil
			}
			return v, err
		}
		creds.Key, err = get("key")
		if err == nil && creds.Key == "" && d.CookieSupported {
			creds.Cookie, err = get("cookie")
		}
		if err == nil && creds.Key == "" && creds.Cookie != "" && d.ID == "opencode-go" {
			creds.Workspace, err = get("workspace")
		}
		if err != nil {
			s.Usage.End(d.ID, providerusage.Unknown("error", "The saved credential could not be read. Unlock the keychain and try again."))
			return apikit.Reply{Data: s.providerAccountView(d)}, nil
		}
	}
	u := providerusage.NewClient().Refresh(c.R.Context(), d, creds)
	s.Usage.End(d.ID, u)
	return apikit.Reply{Data: s.providerAccountView(d)}, nil
}
