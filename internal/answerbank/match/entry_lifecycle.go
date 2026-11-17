package match

import "slices"

// StaleAfterCorrections is the number of counted corrections that moves an
// active entry to stale.
const StaleAfterCorrections = 3

// Stale is the one-way automatic transition. counts maps an entry id to its
// counted corrections. A pinned or archived entry never changes, and nothing
// here ever archives. It returns the ids that became stale; the bank is not
// modified. staleAt is the RFC 3339 instant stamped on the entries that
// change, so the package reads no clock.
func (b Bank) Stale(counts map[string]int, staleAt string) (Bank, []string) {
	out := b
	out.Entries = slices.Clone(b.Entries)
	var changed []string
	for i := range out.Entries {
		e := &out.Entries[i]
		n := counts[e.ID]
		if n < StaleAfterCorrections || e.Pinned || e.EffectiveState() != StateActive {
			continue
		}
		e.State = StateStale
		e.StaleAt = staleAt
		e.StaleCorrectionCount = n
		changed = append(changed, e.ID)
	}
	return out, changed
}
