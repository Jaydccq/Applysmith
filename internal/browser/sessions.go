package browser

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"applysmith/internal/api/wire"
)

const (
	snapshotFile = "session-snapshot.json"
	snapshotTTL  = 7 * 24 * time.Hour
	// sessionCookieLife is the expiry a session cookie receives, so the
	// imported sign-in survives a restart of the browser.
	sessionCookieLife = 30 * 24 * time.Hour
	// replaceBudget bounds the destructive phase of an import or undo. It runs
	// detached from the request so a caller that gives up cannot leave the
	// profile half replaced.
	replaceBudget = 2 * time.Minute
)

// StoredAccount is a stored sign-in account the sync lists. A tenant of a
// named system has no host until the profile's cookies reveal it.
type StoredAccount struct {
	Host   string
	System string
	Tenant string
}

// SessionsConfig wires Sessions to a browser.
type SessionsConfig struct {
	// Dir holds the undo snapshot; it is created with mode 0700.
	Dir string
	// Open returns the profile's cookie store, starting the browser if needed.
	Open func(ctx context.Context) (CookieJar, error)
	// Running returns the store of a browser that is already running, or nil.
	// The sites list uses it so a check-in never starts Chrome.
	Running func() CookieJar
	// Accounts lists the stored sign-in accounts; nil means none.
	Accounts func(ctx context.Context) ([]StoredAccount, error)
	// Probe loads a site's account page and reads it; nil disables the sign-in
	// probe.
	Probe func(ctx context.Context, site SignInSite) (PageReading, error)
	// States keeps the probe's last result per site; nil reports every site as
	// unknown.
	States SignInStore
}

// Sessions copies sign-in sessions into the managed profile. It satisfies the
// API's SessionBrowser.
type Sessions struct {
	cfg SessionsConfig
	now func() time.Time
	mu  sync.Mutex // one import or undo at a time
}

// NewSessions returns the sign-in sync over cfg.
func NewSessions(cfg SessionsConfig) *Sessions { return &Sessions{cfg: cfg, now: time.Now} }

// scoped reports whether host is site or lies below it.
func scoped(host, site string) bool { return host == site || strings.HasSuffix(host, "."+site) }

func withinAny(host string, domains []string) bool {
	return slices.ContainsFunc(domains, func(d string) bool { return scoped(host, d) })
}

// SessionSite is one site the sign-in sync may be asked about. Domain is empty
// for a stored tenant whose host the managed profile has not revealed yet.
type SessionSite struct {
	Domain string
	Label  string
	// Cookies counts the cookies the managed profile holds for the site.
	Cookies wire.SafeInt
	// SignIn is the sign-in probe's state for the site: signed_in, signed_out,
	// checkpoint, captcha, rate_limited or unknown.
	SignIn string
	// SignInCheckedAt is when the probe last read the site, nil before the
	// first check.
	SignInCheckedAt *wire.Time
	// SignInURL is where the site asks for a sign-in, empty for a site the
	// probe does not know.
	SignInURL string
}

// Sites lists the hosts the sync may be asked about: the sign-in probe's
// sites, hosts with a stored sign-in account and hosts holding cookies in the
// managed profile, each with its cookie count in the profile. An unreachable
// browser counts as an empty profile.
func (s *Sessions) Sites(ctx context.Context) ([]SessionSite, error) {
	var cookies []Cookie
	if s.cfg.Running != nil {
		if jar := s.cfg.Running(); jar != nil {
			cookies, _ = jar.Cookies(ctx)
		}
	}
	var accounts []StoredAccount
	if s.cfg.Accounts != nil {
		var err error
		if accounts, err = s.cfg.Accounts(ctx); err != nil {
			return nil, err
		}
	}
	var states map[string]SignInRecord
	if s.cfg.States != nil {
		states, _ = s.cfg.States.Last(ctx) // an unreadable record reads as unknown
	}
	hosts := map[string]bool{}
	for _, c := range cookies {
		hosts[c.host()] = true
	}
	var out []SessionSite
	seen := map[string]bool{}
	add := func(domain, label string) {
		if domain != "" {
			if seen[domain] {
				return
			}
			seen[domain] = true
		}
		n := 0
		for _, c := range cookies {
			if domain != "" && scoped(c.host(), domain) {
				n++
			}
		}
		state := StateUnknown
		site := SessionSite{Domain: domain, Label: label, Cookies: wire.SafeInt(n)}
		if rec, ok := states[domain]; ok {
			state = rec.State
			if !rec.CheckedAt.IsZero() {
				site.SignInCheckedAt = &wire.Time{Time: rec.CheckedAt.UTC()}
			}
		}
		site.SignIn = string(state)
		if i := slices.IndexFunc(SignInSites, func(p SignInSite) bool { return p.Domain == domain }); i >= 0 {
			site.SignInURL = SignInSites[i].SignInURL
		}
		out = append(out, site)
	}
	for _, p := range SignInSites {
		add(p.Domain, p.Label)
	}
	for _, a := range accounts {
		switch {
		case a.Host != "":
			add(strings.ToLower(a.Host), strings.ToLower(a.Host))
		case a.System == "workday" && a.Tenant != "":
			add(workdayHost(hosts, a.Tenant), "Workday "+a.Tenant)
		}
	}
	for _, h := range slices.Sorted(maps.Keys(hosts)) {
		add(h, h)
	}
	return out, nil
}

// workdayHost finds the host of a Workday tenant among the profile's cookie
// hosts, or "" when none shows it yet.
func workdayHost(hosts map[string]bool, tenant string) string {
	tenant = strings.ToLower(tenant)
	var found []string
	for h := range hosts {
		if strings.HasPrefix(h, tenant+".wd") && strings.HasSuffix(h, ".myworkdayjobs.com") {
			found = append(found, h)
		}
	}
	if len(found) == 0 {
		return ""
	}
	slices.Sort(found)
	return found[0]
}

func fromWire(c wire.SessionCookie, now time.Time) Cookie {
	host := strings.TrimPrefix(strings.ToLower(c.Domain), ".")
	out := Cookie{Name: c.Name, Value: c.Value, Domain: host, Path: c.Path, Secure: c.Secure, HTTPOnly: c.HTTPOnly}
	if !c.HostOnly {
		out.Domain = "." + host
	}
	switch c.SameSite {
	case "lax":
		out.SameSite = "Lax"
	case "strict":
		out.SameSite = "Strict"
	case "no_restriction":
		out.SameSite = "None"
	}
	if c.Session || c.ExpirationDate == nil {
		out.Expires = float64(now.Add(sessionCookieLife).Unix())
	} else {
		out.Expires = *c.ExpirationDate
	}
	return out
}

// inSource reports whether the incoming cookies for host show a sign-in: any
// cookie at all, and when the site defines the cookies that prove a sign-in,
// at least one of them.
func inSource(host string, incoming []Cookie) bool {
	proof := proofCookies(host)
	for _, c := range incoming {
		if scoped(c.host(), host) && (len(proof) == 0 || slices.Contains(proof, c.Name)) {
			return true
		}
	}
	return false
}

// Import replaces the managed profile's cookies for the domains with the
// incoming ones. A domain the incoming cookies do not show a sign-in for is
// left untouched and counted as not in source. For the rest it writes the undo
// snapshot first, deletes the in-scope cookies, sets the incoming ones and
// reads the profile back, because the browser acknowledges cookies it then
// drops. It reports counts only.
func (s *Sessions) Import(ctx context.Context, domains []string, cookies []wire.SessionCookie) (wire.SessionImportResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	reqs := make([]string, len(domains))
	for i, d := range domains {
		reqs[i] = strings.TrimPrefix(strings.ToLower(d), ".")
	}
	byKey := map[string]Cookie{}
	var incoming []Cookie
	for _, w := range cookies {
		c := fromWire(w, s.now())
		if !withinAny(c.host(), reqs) {
			return wire.SessionImportResult{}, newError(CodeCookieOutOfScope, "a cookie lies outside the requested domains")
		}
		if _, dup := byKey[c.key()]; !dup {
			incoming = append(incoming, c)
		}
		byKey[c.key()] = c
	}
	for i, c := range incoming {
		incoming[i] = byKey[c.key()]
	}
	var active []string
	for _, d := range reqs {
		if inSource(d, incoming) {
			active = append(active, d)
		}
	}
	res := wire.SessionImportResult{NotInSource: wire.SafeInt(len(reqs) - len(active))}
	if len(active) == 0 {
		return res, nil
	}
	incoming = slices.DeleteFunc(incoming, func(c Cookie) bool { return !withinAny(c.host(), active) })
	jar, err := s.cfg.Open(ctx)
	if err != nil {
		return wire.SessionImportResult{}, err
	}
	end, note := busy(jar)
	defer end()
	rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), replaceBudget)
	defer cancel()
	existing, err := inScope(rctx, jar, active)
	if err != nil {
		return wire.SessionImportResult{}, err
	}
	s.dropExpiredSnapshot()
	if err := s.writeSnapshot(active, existing); err != nil {
		return wire.SessionImportResult{}, err
	}
	if err := deleteAll(rctx, jar, existing); err != nil {
		for _, c := range existing {
			_ = jar.SetCookie(rctx, c)
		}
		return wire.SessionImportResult{}, newError(CodeReplaceFailed, "the browser refused to delete a cookie being replaced; the profile was restored")
	}
	for _, c := range incoming {
		_ = jar.SetCookie(rctx, c)
	}
	present, err := presentKeys(rctx, jar)
	if err != nil {
		return wire.SessionImportResult{}, err
	}
	for _, c := range incoming {
		if present[c.key()] {
			res.Imported++
		} else {
			res.Failed++
		}
	}
	res.Replaced = wire.SafeInt(len(existing))
	// Counts and domains only; never a cookie name or value.
	note(EventSessionImport, fmt.Sprintf("%d imported, %d failed, %d replaced; domains: %s", res.Imported, res.Failed, res.Replaced, strings.Join(active, ", ")))
	if res.Imported > 0 {
		s.recordImported(active)
	}
	return res, nil
}

// busy marks the import in flight on a jar that can say so (the browser
// instance, which then does not stop itself meanwhile) and returns a recorder
// for the event log. Any other jar gets no-ops.
func busy(jar CookieJar) (end func(), note func(kind, detail string)) {
	if b, ok := jar.(interface {
		Begin() func()
		Note(kind, detail string)
	}); ok {
		return b.Begin(), b.Note
	}
	return func() {}, func(string, string) {}
}

// Undo deletes the in-scope cookies of the last import and restores the
// snapshot. The snapshot is deleted only when every cookie came back, so a
// partial undo can be repeated.
func (s *Sessions) Undo(ctx context.Context) (wire.SessionUndoResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.dropExpiredSnapshot()
	snap, err := s.readSnapshot()
	if err != nil {
		return wire.SessionUndoResult{}, err
	}
	jar, err := s.cfg.Open(ctx)
	if err != nil {
		return wire.SessionUndoResult{}, err
	}
	end, _ := busy(jar)
	defer end()
	rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), replaceBudget)
	defer cancel()
	current, err := inScope(rctx, jar, snap.Domains)
	if err != nil {
		return wire.SessionUndoResult{}, err
	}
	var res wire.SessionUndoResult
	for _, c := range current {
		if jar.DeleteCookie(rctx, c) != nil {
			res.Failed++
		}
	}
	for _, c := range snap.Cookies {
		_ = jar.SetCookie(rctx, c)
	}
	present, err := presentKeys(rctx, jar)
	if err != nil {
		return wire.SessionUndoResult{}, err
	}
	for _, c := range snap.Cookies {
		if present[c.key()] {
			res.Restored++
		} else {
			res.Failed++
		}
	}
	if res.Failed == 0 {
		_ = os.Remove(s.snapshotPath())
	}
	return res, nil
}

// UndoAvailable reports whether a snapshot waits to be restored.
func (s *Sessions) UndoAvailable() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.dropExpiredSnapshot()
	_, err := os.Stat(s.snapshotPath())
	return err == nil
}

func inScope(ctx context.Context, jar CookieJar, domains []string) ([]Cookie, error) {
	all, err := jar.Cookies(ctx)
	if err != nil {
		return nil, err
	}
	return slices.DeleteFunc(all, func(c Cookie) bool { return !withinAny(c.host(), domains) }), nil
}

func deleteAll(ctx context.Context, jar CookieJar, cookies []Cookie) error {
	for _, c := range cookies {
		if err := jar.DeleteCookie(ctx, c); err != nil {
			return err
		}
	}
	return nil
}

func presentKeys(ctx context.Context, jar CookieJar) (map[string]bool, error) {
	all, err := jar.Cookies(ctx)
	if err != nil {
		return nil, err
	}
	m := make(map[string]bool, len(all))
	for _, c := range all {
		m[c.key()] = true
	}
	return m, nil
}

type snapshot struct {
	Domains []string `json:"domains"`
	Cookies []Cookie `json:"cookies"`
}

func (s *Sessions) snapshotPath() string { return filepath.Join(s.cfg.Dir, snapshotFile) }

// writeSnapshot replaces the snapshot with a file of mode 0600, published by
// rename so a failed write leaves the earlier snapshot intact.
func (s *Sessions) writeSnapshot(domains []string, cookies []Cookie) error {
	fail := func(err error) error { return wrapError(CodeSnapshotFailed, err, "cannot write the undo snapshot") }
	data, err := json.Marshal(snapshot{Domains: domains, Cookies: cookies})
	if err != nil {
		return fail(err)
	}
	if err := os.MkdirAll(s.cfg.Dir, 0o700); err != nil {
		return fail(err)
	}
	tmp, err := os.CreateTemp(s.cfg.Dir, ".session-snapshot-*")
	if err != nil {
		return fail(err)
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return fail(err)
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fail(err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fail(err)
	}
	if err := tmp.Close(); err != nil {
		return fail(err)
	}
	if err := os.Rename(tmp.Name(), s.snapshotPath()); err != nil {
		return fail(err)
	}
	return nil
}

func (s *Sessions) readSnapshot() (snapshot, error) {
	data, err := os.ReadFile(s.snapshotPath())
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return snapshot{}, newError(CodeNoSnapshot, "there is no import to undo")
		}
		return snapshot{}, wrapError(CodeSnapshotFailed, err, "cannot read the undo snapshot")
	}
	var snap snapshot
	if err := json.Unmarshal(data, &snap); err != nil {
		return snapshot{}, newError(CodeSnapshotFailed, "the undo snapshot is unreadable")
	}
	return snap, nil
}

// dropExpiredSnapshot deletes a snapshot older than seven days.
func (s *Sessions) dropExpiredSnapshot() {
	if st, err := os.Stat(s.snapshotPath()); err == nil && s.now().Sub(st.ModTime()) > snapshotTTL {
		_ = os.Remove(s.snapshotPath())
	}
}
