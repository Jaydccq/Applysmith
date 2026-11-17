package api

import (
	"context"
	"errors"
	"runtime"
	"strings"

	"applysmith/internal/api/apikit"
	"applysmith/internal/api/wire"
	"applysmith/internal/browser/browserimport"
)

// maxImportHosts bounds the sites one import call reads.
const maxImportHosts = 200

// reasons a cookie import names.
const (
	reasonImportUnsupported = "import-unsupported"
	reasonImportNoKey       = "import-no-key"
	reasonImportKeyLocked   = "import-key-locked"
	reasonImportFailed      = "import-failed"
)

// CookieImporter reads a user's own sign-in cookies from an installed browser
// on this computer. Reading a source's cookie values prompts the user for that
// browser's keychain key; listing sources and sites does not.
type CookieImporter interface {
	Sources() ([]browserimport.Source, error)
	Sites(ctx context.Context, id string) ([]browserimport.Site, error)
	Read(ctx context.Context, id string, hosts []string) ([]wire.SessionCookie, error)
}

// importSupported reports whether this server can import from an installed
// browser, and why not when it cannot.
func (s *Server) importSupported() (bool, string) {
	if runtime.GOOS != "darwin" {
		return false, "Importing cookies from an installed browser is supported on macOS only. On this platform, sign in through the extension's session sync."
	}
	if s.cfg.CookieImport == nil {
		return false, "Importing cookies from an installed browser is not available here."
	}
	return true, ""
}

// importSources lists the installed-browser profiles the user can import from.
// It reads the filesystem only: no keychain, no cookie values.
func (s *Server) importSources(c *apikit.Call) (apikit.Reply, error) {
	ok, reason := s.importSupported()
	if !ok {
		return apikit.Reply{Data: wire.ImportSources{Supported: false, Reason: reason}}, nil
	}
	srcs, err := s.cfg.CookieImport.Sources()
	if err != nil {
		return apikit.Reply{}, importError(err)
	}
	out := wire.ImportSources{Supported: true, Sources: make([]wire.ImportSource, 0, len(srcs))}
	for _, src := range srcs {
		out.Sources = append(out.Sources, wire.ImportSource{ID: src.ID, BrowserID: src.BrowserID, BrowserLabel: src.BrowserLabel, Profile: src.Profile})
	}
	return apikit.Reply{Data: out}, nil
}

// importSites lists the hosts a source holds cookies for. No keychain, no values.
func (s *Server) importSites(c *apikit.Call) (apikit.Reply, error) {
	if ok, reason := s.importSupported(); !ok {
		return apikit.Reply{}, apikit.WithReason(apikit.NewErr(wire.CodeUnavailable, "%s", reason), reasonImportUnsupported)
	}
	sites, err := s.cfg.CookieImport.Sites(c.R.Context(), c.R.PathValue("id"))
	if err != nil {
		return apikit.Reply{}, importError(err)
	}
	out := wire.ImportSites{Sites: make([]wire.ImportSite, 0, len(sites))}
	for _, site := range sites {
		out.Sites = append(out.Sites, wire.ImportSite{Host: site.Host, Cookies: site.Cookies})
	}
	return apikit.Reply{Data: out}, nil
}

// importCookies brings the chosen sites' sign-ins from a source into the apply
// window. Reading the source prompts the user for the browser's keychain key;
// the cookies then go through the same replace-and-read-back import as the
// extension's sync, so the result can be undone and the sites appear in the
// manage list.
func (s *Server) importCookies(c *apikit.Call) (apikit.Reply, error) {
	if ok, reason := s.importSupported(); !ok {
		return apikit.Reply{}, apikit.WithReason(apikit.NewErr(wire.CodeUnavailable, "%s", reason), reasonImportUnsupported)
	}
	var req wire.ImportRequest
	if e := apikit.Decode(c.Body, &req); e != nil {
		return apikit.Reply{}, e
	}
	if strings.TrimSpace(req.SourceID) == "" {
		return apikit.Reply{}, apikit.InvalidField("sourceId", "name the browser profile to import from")
	}
	hosts, e := cleanHosts(req.Hosts)
	if e != nil {
		return apikit.Reply{}, e
	}
	if s.applyRunLive() {
		return apikit.Reply{}, errApplyRunLive()
	}
	if e := s.signin.begin(); e != nil {
		return apikit.Reply{}, e
	}
	defer s.signin.end()
	cookies, err := s.cfg.CookieImport.Read(c.R.Context(), req.SourceID, hosts)
	if err != nil {
		s.signin.record(outcomeFailed, errorText(importError(err)), nil, nil)
		return apikit.Reply{}, importError(err)
	}
	res, err := s.copyCookies(c, hosts, cookies, false)
	return replyOr(res, err)
}

// importedView lists the sites brought into the apply window, for the manage
// list.
func (s *Server) importedView(c *apikit.Call) (apikit.Reply, error) {
	sites, err := s.sessionBrowser().ImportedSites(c.R.Context())
	if err != nil {
		return apikit.Reply{}, sessionError(err)
	}
	out := wire.ImportedView{Sites: make([]wire.ImportedSite, 0, len(sites))}
	for _, site := range sites {
		out.Sites = append(out.Sites, wire.ImportedSite{Host: site.Host, At: wire.Time{Time: site.At.UTC()}, Cookies: site.Cookies})
	}
	return apikit.Reply{Data: out}, nil
}

// importForget clears one imported site's sign-in from the apply window.
func (s *Server) importForget(c *apikit.Call) (apikit.Reply, error) {
	var req wire.ForgetRequest
	if e := apikit.Decode(c.Body, &req); e != nil {
		return apikit.Reply{}, e
	}
	host := strings.TrimPrefix(strings.ToLower(strings.TrimSpace(req.Host)), ".")
	if len(host) > 253 || !hostPattern.MatchString(host) {
		return apikit.Reply{}, apikit.InvalidField("host", "name a site to clear")
	}
	if s.applyRunLive() {
		return apikit.Reply{}, errApplyRunLive()
	}
	if e := s.signin.begin(); e != nil {
		return apikit.Reply{}, e
	}
	defer s.signin.end()
	n, err := s.sessionBrowser().Forget(c.R.Context(), host)
	if err != nil {
		return apikit.Reply{}, sessionError(err)
	}
	return apikit.Reply{Data: wire.ForgetResult{Cleared: wire.SafeInt(n)}}, nil
}

// cleanHosts normalises and validates import host names.
func cleanHosts(in []string) ([]string, *apikit.Error) {
	if len(in) == 0 {
		return nil, apikit.InvalidField("hosts", "choose at least one site")
	}
	if len(in) > maxImportHosts {
		return nil, apikit.InvalidField("hosts", "at most %d sites at once", maxImportHosts)
	}
	seen := map[string]bool{}
	out := make([]string, 0, len(in))
	for _, h := range in {
		n := strings.TrimPrefix(strings.ToLower(strings.TrimSpace(h)), ".")
		if len(n) > 253 || !hostPattern.MatchString(n) {
			return nil, apikit.InvalidField("hosts", "hosts must be plain domain names")
		}
		if !seen[n] {
			seen[n] = true
			out = append(out, n)
		}
	}
	return out, nil
}

// importError maps a browserimport error to the envelope. A key error names
// what the user must do; everything else is an unavailable read.
func importError(err error) error {
	switch {
	case errors.Is(err, browserimport.ErrUnsupportedOS):
		return apikit.WithReason(apikit.NewErr(wire.CodeUnavailable, "%s", err.Error()), reasonImportUnsupported)
	case errors.Is(err, browserimport.ErrNoKey):
		return apikit.WithReason(apikit.NewErr(wire.CodeUnavailable, "%s", err.Error()), reasonImportNoKey)
	case errors.Is(err, browserimport.ErrKeyLocked):
		e := apikit.WithReason(apikit.NewErr(wire.CodeUnavailable, "%s", err.Error()), reasonImportKeyLocked)
		e.Retryable = true
		return e
	}
	return apikit.WithReason(apikit.NewErr(wire.CodeUnavailable, "the browser's cookies could not be read"), reasonImportFailed)
}
