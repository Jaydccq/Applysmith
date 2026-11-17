package discovery

import (
	"context"
	"encoding/json"
	"sync"

	"applysmith/internal/identity"
)

// The stores a posting can be a duplicate of, in the order they are checked.
const (
	StoreInbox   = "inbox"
	StoreTracker = "tracker"
	StoreReport  = "report"
	StoreArchive = "archive"
	StoreHistory = "history"
	StoreRun     = "run" // an earlier row of the same run
)

type known struct {
	id      int64
	key     string
	aliases []string
	p       identity.Posting
}

// storeIndex answers "is this posting one of these rows".
type storeIndex struct {
	byKey     map[string]*known
	byCompany map[string][]*known
}

func newStoreIndex() *storeIndex {
	return &storeIndex{byKey: map[string]*known{}, byCompany: map[string][]*known{}}
}

func (s *storeIndex) add(k *known) {
	s.byKey[k.key] = k
	for _, a := range k.aliases {
		s.byKey[a] = k
	}
	for _, c := range identity.CompanyKeys(k.p.Company) {
		s.byCompany[c] = append(s.byCompany[c], k)
	}
}

func (s *storeIndex) match(p identity.Posting) *known {
	if key := p.IdentityKey(); key != "" {
		if k, ok := s.byKey[key]; ok {
			return k
		}
	}
	seen := map[*known]bool{}
	for _, c := range identity.CompanyKeys(p.Company) {
		for _, k := range s.byCompany[c] {
			if !seen[k] {
				seen[k] = true
				if identity.SameOpening(p, k.p) {
					return k
				}
			}
		}
	}
	return nil
}

// Known indexes everything a new posting is compared with: the inbox, the
// tracker (split into live rows, rows that exist only as an evaluated or
// skipped report, and archived rows), and the scan history. It is safe for
// concurrent use.
type Known struct {
	mu       sync.Mutex
	stores   map[string]*storeIndex
	order    []string
	inboxCo  map[string]bool
	trackedC map[string]bool
}

// LoadKnown reads the stores once; a scan keeps the index current as it
// inserts.
func (r *Repo) LoadKnown(ctx context.Context) (*Known, error) {
	k := &Known{stores: map[string]*storeIndex{}, inboxCo: map[string]bool{}, trackedC: map[string]bool{}}
	k.order = []string{StoreInbox, StoreTracker, StoreReport, StoreArchive, StoreHistory}
	for _, s := range append([]string{StoreRun}, k.order...) {
		k.stores[s] = newStoreIndex()
	}
	if err := r.loadInbox(ctx, k); err != nil {
		return nil, err
	}
	if err := r.loadTracker(ctx, k); err != nil {
		return nil, err
	}
	if err := r.loadHistory(ctx, k); err != nil {
		return nil, err
	}
	return k, nil
}

func (r *Repo) loadInbox(ctx context.Context, k *Known) error {
	rows, err := r.s.Read().QueryContext(ctx, `SELECT id, identity_key, aliases_json, company, role, location FROM inbox`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var x known
		var aliases string
		if err := rows.Scan(&x.id, &x.key, &aliases, &x.p.Company, &x.p.Role, &x.p.Location); err != nil {
			return err
		}
		_ = json.Unmarshal([]byte(aliases), &x.aliases)
		x.p.Key = x.key
		k.stores[StoreInbox].add(&x)
		k.noteCompany(k.inboxCo, x.p.Company)
	}
	return rows.Err()
}

// loadTracker reads tracker rows. A row's identity is its key, its aliases,
// and an ATS-native alias stands for it when the primary key is not native.
func (r *Repo) loadTracker(ctx context.Context, k *Known) error {
	aliases := map[int64][]string{}
	arows, err := r.s.Read().QueryContext(ctx, `SELECT entry_id, identity_key FROM tracker_aliases`)
	if err != nil {
		return err
	}
	for arows.Next() {
		var id int64
		var key string
		if err := arows.Scan(&id, &key); err != nil {
			arows.Close()
			return err
		}
		aliases[id] = append(aliases[id], key)
	}
	arows.Close()
	rows, err := r.s.Read().QueryContext(ctx, `SELECT id, identity_key, company, role, location, status, report_number IS NOT NULL, archived_at IS NOT NULL FROM tracker_entries`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var x known
		var status string
		var hasReport, archived bool
		if err := rows.Scan(&x.id, &x.key, &x.p.Company, &x.p.Role, &x.p.Location, &status, &hasReport, &archived); err != nil {
			return err
		}
		x.aliases = aliases[x.id]
		x.p.Key = x.key
		if !identity.IsATSNative(x.key) {
			for _, a := range x.aliases {
				if identity.IsATSNative(a) {
					x.p.Key = a
					break
				}
			}
		}
		store := StoreTracker
		switch {
		case archived:
			store = StoreArchive
		case hasReport && (status == "evaluated" || status == "skipped"):
			store = StoreReport
		}
		k.stores[store].add(&x)
		k.noteCompany(k.trackedC, x.p.Company)
	}
	return rows.Err()
}

func (r *Repo) loadHistory(ctx context.Context, k *Known) error {
	rows, err := r.s.Read().QueryContext(ctx, `SELECT identity_key, company, title, location FROM scan_history`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var x known
		if err := rows.Scan(&x.key, &x.p.Company, &x.p.Role, &x.p.Location); err != nil {
			return err
		}
		x.p.Key = x.key
		k.stores[StoreHistory].add(&x)
	}
	return rows.Err()
}

func (k *Known) noteCompany(set map[string]bool, company string) {
	for _, c := range identity.CompanyKeys(company) {
		set[c] = true
	}
}

// Match returns the first store holding the posting. With retryKnown the scan
// history is skipped; the tracker, report and archive matches never are.
func (k *Known) Match(p Posting, retryKnown bool) (store string, matchID int64, ok bool) {
	id := p.Identity()
	id.Key = id.IdentityKey()
	k.mu.Lock()
	defer k.mu.Unlock()
	if m := k.stores[StoreRun].match(id); m != nil {
		return StoreRun, m.id, true
	}
	for _, s := range k.order {
		if s == StoreHistory && retryKnown {
			continue
		}
		if m := k.stores[s].match(id); m != nil {
			return s, m.id, true
		}
	}
	return "", 0, false
}

// Reserve records a row about to be inserted so a later posting of the same
// run sees it.
func (k *Known) Reserve(p Posting) {
	id := p.Identity()
	key := id.IdentityKey()
	id.Key = key
	k.mu.Lock()
	defer k.mu.Unlock()
	k.stores[StoreRun].add(&known{key: key, p: id})
	k.noteCompany(k.inboxCo, p.Company)
}

// addInbox records a row just inserted into the inbox, so a later posting of
// the same batch is found as its duplicate there.
func (k *Known) addInbox(p Posting, rowID int64) {
	id := p.Identity()
	key := id.IdentityKey()
	id.Key = key
	k.mu.Lock()
	defer k.mu.Unlock()
	k.stores[StoreInbox].add(&known{id: rowID, key: key, p: id})
	k.noteCompany(k.inboxCo, p.Company)
}

// CompanyNovelty is +1 for a company not yet in the inbox or tracker, -1 for
// one already tracked, else 0.
func (k *Known) CompanyNovelty(company string) int {
	k.mu.Lock()
	defer k.mu.Unlock()
	keys := identity.CompanyKeys(company)
	for _, c := range keys {
		if k.trackedC[c] {
			return -1
		}
	}
	for _, c := range keys {
		if k.inboxCo[c] {
			return 0
		}
	}
	return 1
}
