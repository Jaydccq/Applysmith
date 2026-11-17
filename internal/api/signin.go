package api

import (
	"errors"
	"net"
	"net/url"
	"strings"

	"applysmith/internal/api/apikit"
	"applysmith/internal/api/wire"
	"applysmith/internal/credentials"
	"applysmith/internal/vault"
)

// resolveSignin is the one route that returns a password. It serves the paired
// extension's token on the loopback listener and nothing else, answers for
// exactly the host of the page the extension asked about, and refuses a host
// whose passwords are on hold before the vault is read.
func (s *Server) resolveSignin(c *apikit.Call) (apikit.Reply, error) {
	if c.P.Kind != apikit.PrincipalLocalToken {
		return apikit.Reply{}, apikit.NewErr(wire.CodeForbidden, "only the paired extension may resolve a sign-in")
	}
	if !fromLoopback(c) {
		return apikit.Reply{}, apikit.NewErr(wire.CodeForbidden, "a sign-in is resolved only on this computer")
	}
	var req wire.SigninResolveRequest
	if e := apikit.Decode(c.Body, &req); e != nil {
		return apikit.Reply{}, e
	}
	u, err := url.Parse(strings.TrimSpace(req.URL))
	if err != nil || req.URL == "" || u.Hostname() == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || len(req.URL) > apikit.MaxPostingURL {
		return apikit.Reply{}, apikit.NewErr(wire.CodeBadRequest, "a page address is required")
	}
	host := strings.ToLower(u.Hostname())
	d, err := s.dom()
	if err != nil {
		return apikit.Reply{}, err
	}
	hold, err := d.creds.Held(c.R.Context(), host)
	switch {
	case err != nil:
		e := apikit.WithReason(apikit.NewErr(wire.CodeUnavailable, "the record of refused passwords on this site cannot be read, so no password was released"), wire.ReasonSigninHoldUnreadable)
		return apikit.Reply{}, e
	case hold.Held:
		why := "the stored passwords for this site ran out earlier today"
		if hold.Lockout {
			why = "the site reported that the account is locked"
		}
		return apikit.Reply{}, apikit.WithReason(apikit.NewErr(wire.CodeConflict, "Signing in here is on hold for today: %s. Another try could lock the account. The hold ends at midnight, or sooner when you change a stored password.", why), wire.ReasonSigninHeld)
	}
	cred, err := d.creds.Resolve(c.R.Context(), host)
	switch {
	case errors.Is(err, credentials.ErrNotFound), vault.IsCode(err, vault.CodeNotFound):
		return apikit.Reply{}, apikit.WithReason(apikit.NewErr(wire.CodeNotFound, "no sign-in credential is stored for this site"), vault.CodeNotFound)
	case vault.IsCode(err, vault.CodeKeychainDenied):
		return apikit.Reply{}, apikit.WithReason(apikit.NewErr(wire.CodeUnavailable, "the OS keychain refused access; unlock it or allow access, then retry"), vault.CodeKeychainDenied)
	case vault.IsCode(err, vault.CodeKeychainUnavailable):
		return apikit.Reply{}, apikit.WithReason(apikit.NewErr(wire.CodeUnavailable, "no OS keychain is available on this computer"), vault.CodeKeychainUnavailable)
	case err != nil:
		return apikit.Reply{}, apikit.Unavailable("the credential could not be read; retry")
	}
	return apikit.Reply{Data: wire.SigninCredential{Email: cred.Email, Password: cred.Password, Source: cred.Source}}, nil
}

// fromLoopback reports whether the request came to the loopback listener from
// this computer.
func fromLoopback(c *apikit.Call) bool {
	if requestIsRemote(c.R) {
		return false // the tunnel's peer is loopback; the listener says the request is remote
	}
	host, _, err := net.SplitHostPort(c.R.RemoteAddr)
	if err != nil {
		return false
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
