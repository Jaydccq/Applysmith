package browser

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"applysmith/internal/api/wire"
)

// importedFile records which sites' sign-ins were brought into the managed
// profile, so the dashboard can list them and clear one at a time. It holds
// host names and times only, never a cookie.
const importedFile = "imported-sites.json"

// ImportedSite is one host whose sign-in was brought into the managed profile.
type ImportedSite struct {
	Host    string
	At      time.Time
	Cookies wire.SafeInt
}

type importedRecord struct {
	Host string    `json:"host"`
	At   time.Time `json:"at"`
}

func (s *Sessions) importedPath() string { return filepath.Join(s.cfg.Dir, importedFile) }

func (s *Sessions) readImported() []importedRecord {
	data, err := os.ReadFile(s.importedPath())
	if err != nil {
		return nil
	}
	var recs []importedRecord
	if json.Unmarshal(data, &recs) != nil {
		return nil
	}
	return recs
}

// writeImported publishes the ledger by rename, mode 0600.
func (s *Sessions) writeImported(recs []importedRecord) error {
	data, err := json.Marshal(recs)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(s.cfg.Dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(s.cfg.Dir, ".imported-sites-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), s.importedPath())
}

// recordImported adds or refreshes the ledger entry for each host. It never
// fails an import: a ledger it cannot write only loses the manage-list row.
func (s *Sessions) recordImported(hosts []string) {
	recs := s.readImported()
	now := s.now().UTC()
	for _, h := range hosts {
		h = strings.TrimPrefix(strings.ToLower(h), ".")
		if i := slices.IndexFunc(recs, func(r importedRecord) bool { return r.Host == h }); i >= 0 {
			recs[i].At = now
		} else {
			recs = append(recs, importedRecord{Host: h, At: now})
		}
	}
	_ = s.writeImported(recs)
}

// ImportedSites lists the sites brought into the managed profile, each with the
// cookie count the profile now holds for it. A site the profile no longer holds
// any cookie for is dropped from the ledger, so a sign-in cleared elsewhere
// does not linger in the list.
func (s *Sessions) ImportedSites(ctx context.Context) ([]ImportedSite, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	recs := s.readImported()
	if len(recs) == 0 {
		return nil, nil
	}
	var cookies []Cookie
	if s.cfg.Running != nil {
		if jar := s.cfg.Running(); jar != nil {
			cookies, _ = jar.Cookies(ctx)
		}
	}
	count := func(host string) int {
		n := 0
		for _, c := range cookies {
			if scoped(c.host(), host) {
				n++
			}
		}
		return n
	}
	var out []ImportedSite
	kept := recs[:0]
	for _, r := range recs {
		n := count(r.Host)
		if n == 0 {
			continue // cleared since; drop it
		}
		kept = append(kept, r)
		out = append(out, ImportedSite{Host: r.Host, At: r.At, Cookies: wire.SafeInt(n)})
	}
	if len(kept) != len(recs) {
		_ = s.writeImported(kept)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].At.After(out[j].At) })
	return out, nil
}

// Forget clears one imported site's sign-in from the managed profile: it
// deletes the in-scope cookies for the host and drops the ledger row. Unlike
// Undo it restores nothing, since the site's sign-in came from outside. It
// reports how many cookies it cleared.
func (s *Sessions) Forget(ctx context.Context, host string) (int, error) {
	host = strings.TrimPrefix(strings.ToLower(host), ".")
	if host == "" {
		return 0, newError(CodeCookieOutOfScope, "name the site to clear")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	jar, err := s.cfg.Open(ctx)
	if err != nil {
		return 0, err
	}
	end, note := busy(jar)
	defer end()
	rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), replaceBudget)
	defer cancel()
	existing, err := inScope(rctx, jar, []string{host})
	if err != nil {
		return 0, err
	}
	if err := deleteAll(rctx, jar, existing); err != nil {
		return 0, newError(CodeReplaceFailed, "the browser refused to delete a cookie")
	}
	recs := slices.DeleteFunc(s.readImported(), func(r importedRecord) bool { return r.Host == host })
	_ = s.writeImported(recs)
	note(EventSessionImport, "cleared "+strconv.Itoa(len(existing))+" cookies; site: "+host)
	return len(existing), nil
}
