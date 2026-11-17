package kb

import (
	"net/url"
	"sync"
	"time"
)

// memoTable remembers, for a while, the site a page declared, or that it
// declared none (a nil site).
type memoTable struct {
	mu      sync.Mutex
	entries map[string]memoEntry
}

type memoEntry struct {
	site  *url.URL
	until time.Time
}

// lookup returns the remembered site of key while its entry lives.
func (m *memoTable) lookup(key string, now time.Time) (site *url.URL, ok bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	e, ok := m.entries[key]
	if ok && now.Before(e.until) {
		return e.site, true
	}
	delete(m.entries, key)
	return nil, false
}

// remember keeps key's site until the given time and drops every entry whose
// time has passed, so the table holds only live entries.
func (m *memoTable) remember(key string, site *url.URL, now, until time.Time) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.entries == nil {
		m.entries = map[string]memoEntry{}
	}
	for k, e := range m.entries {
		if !now.Before(e.until) {
			delete(m.entries, k)
		}
	}
	m.entries[key] = memoEntry{site: site, until: until}
}
