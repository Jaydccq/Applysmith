package match

import (
	"fmt"
	"slices"
	"strings"
)

// Bank is the loaded answer bank.
type Bank struct {
	Version   int
	Employers []Employer
	Entries   []Entry
	// Overrides maps a lowercased host to its overrides in file order.
	Overrides map[string][]Override
	// HostSpelling maps a key of Overrides to the host as the file spells it,
	// so a save writes the case the user wrote. A key without an entry is
	// written as it is.
	HostSpelling map[string]string
}

// HostName returns the spelling to write for an override key.
func (b Bank) HostName(key string) string {
	if s, ok := b.HostSpelling[key]; ok {
		return s
	}
	return key
}

// Employer is a canonical employer name and its aliases.
type Employer struct {
	Name    string   `yaml:"name" json:"name"`
	Aliases []string `yaml:"aliases" json:"aliases"`
}

// Override patches the answer fields of one entry for pages on a host. Fields
// names the answer-field keys the override states; only those are applied.
type Override struct {
	ID     string   `json:"id"`
	Kind   Kind     `json:"kind,omitempty"` // empty keeps the base kind
	Body   Body     `json:"body"`
	Fields []string `json:"fields"`
}

// Find returns the first entry with the id.
func (b Bank) Find(id string) (Entry, bool) {
	for _, e := range b.Entries {
		if e.ID == id {
			return e, true
		}
	}
	return Entry{}, false
}

// OwnAnswer reports whether r applies an answer the user wrote into this bank
// for the question, as written: a non-model fill from a stored entry whose body
// the user wrote (Entry.UserWritten).
func (b Bank) OwnAnswer(r Result) bool {
	if r.Source != SourceEntry || (r.Outcome != OutcomeFilled && r.Outcome != OutcomeFilledReview) {
		return false
	}
	e, ok := b.Find(r.EntryID)
	return ok && r.EntryID != "" && e.UserWritten()
}

// UserWritten reports an entry whose body is an answer the user wrote: not an
// acknowledgement entry, which the legal-declaration setting governs, and not a
// derived or per-run entry, a body stamped at load time or a shipped default.
func (e Entry) UserWritten() bool {
	return e.Kind != KindAutoAcknowledge && !consentAffirmativeIDs[e.ID] && !e.derived() && !e.RunContext && !e.StampedBody && !e.Shipped
}

// HostKey returns the override key that applies to host: an exact match wins,
// otherwise the longest key that is a dot-suffix of the host. Comparison is
// case-insensitive; keys of Overrides are lowercase.
func (b Bank) HostKey(host string) (string, bool) {
	host = strings.ToLower(strings.TrimSpace(host))
	if _, ok := b.Overrides[host]; ok {
		return host, true
	}
	best := ""
	for key := range b.Overrides {
		if strings.HasSuffix(host, "."+key) && len(key) > len(best) {
			best = key
		}
	}
	return best, best != ""
}

// EntriesForHost returns the entries a request from host is matched against:
// host overrides applied, archived entries removed. The bank is not modified.
func (b Bank) EntriesForHost(host string) []Entry {
	patches := map[string][]Override{}
	if key, ok := b.HostKey(host); ok {
		for _, o := range b.Overrides[key] {
			patches[o.ID] = append(patches[o.ID], o)
		}
	}
	out := make([]Entry, 0, len(b.Entries))
	for _, e := range b.Entries {
		for _, o := range patches[e.ID] {
			if merged, err := o.Apply(e); err == nil {
				e = merged
			}
		}
		if !e.Archived() {
			out = append(out, e)
		}
	}
	return out
}

// Apply returns the base entry patched by the override. A kind change keeps
// every metadata field of the base and must state every field the new kind
// requires; otherwise the override is refused and the base stays.
func (o Override) Apply(base Entry) (Entry, error) {
	if o.ID != base.ID {
		return base, fmt.Errorf("it targets another entry")
	}
	out := base
	if o.Kind != "" && o.Kind != base.Kind {
		if !o.Kind.Valid() {
			return base, fmt.Errorf("it names an unknown kind")
		}
		for _, req := range kindRequired[o.Kind] {
			if !slices.Contains(o.Fields, req) {
				return base, fmt.Errorf("it changes the kind to %s without stating %s", o.Kind, req)
			}
		}
		out.Kind = o.Kind
		out.Body = Body{}
		out.StampedBody = false
	}
	for _, key := range o.Fields {
		if slices.Contains(kindFields[out.Kind], key) {
			copyField(&out.Body, o.Body, key)
		}
	}
	return out, nil
}
