// Clean-room: docs/provenance.md#CR-EVAL-RUBRIC

package gates

import (
	"slices"
	"strings"
	"unicode"
)

// Gate names and reason codes of the batch skip list.
const (
	GateSkipListConfig Gate = "batch_skip_list_config"
	GateSkipList       Gate = "batch_skip_list"

	CodeSkipListMalformed   = "skip_list_malformed"
	CodeSkipListTooLong     = "skip_list_too_long"
	CodeSkipListEntryLong   = "skip_list_entry_too_long"
	CodeSkipListed          = "skip_listed"
	CodeSkipSingleURL       = "skip_single_url"
	CodeDuplicateBeforeSkip = "duplicate_before_skip"
)

// Skip list bounds.
const (
	MaxSkipEntries     = 500
	MaxSkipEntryLength = 120
)

// DefaultSkipList is the list when the profile key is absent. A present key is
// the whole list, so a default the user removed stays removed.
var DefaultSkipList = []string{"Apple", "Microsoft", "Alphabet", "Amazon", "Meta", "NVIDIA", "Tesla"}

// aliasGroups are the names that cover one another.
var aliasGroups = [][]string{
	{"alphabet", "google", "deepmind", "youtube"},
	{"meta", "facebook", "instagram", "whatsapp"},
	{"amazon", "aws"},
}

// SkipListConfig is the profile's batch_skip_companies key as read.
type SkipListConfig struct {
	Present bool
	// NotAStringList is true when the key is present but is not a list of
	// strings.
	NotAStringList bool
	Entries        []string
}

// ResolveSkipList returns the effective list. A malformed or oversize key
// stops the batch before any row runs.
func ResolveSkipList(c SkipListConfig) ([]string, Decision) {
	const g = GateSkipListConfig
	if !c.Present {
		return slices.Clone(DefaultSkipList), decide(g, Allow, CodeOK, "key absent; the default list applies")
	}
	switch {
	case c.NotAStringList:
		return nil, decide(g, Deny, CodeSkipListMalformed, "batch_skip_companies is not a list of strings; the batch does not start").withClass(ClassEnvironment)
	case len(c.Entries) > MaxSkipEntries:
		return nil, decide(g, Deny, CodeSkipListTooLong, "batch_skip_companies holds more than %d entries; the batch does not start", MaxSkipEntries).withClass(ClassEnvironment)
	}
	for _, e := range c.Entries {
		if len([]rune(e)) > MaxSkipEntryLength {
			return nil, decide(g, Deny, CodeSkipListEntryLong, "a batch_skip_companies entry is longer than %d characters; the batch does not start", MaxSkipEntryLength).withClass(ClassEnvironment)
		}
	}
	return slices.Clone(c.Entries), decide(g, Allow, CodeOK, "%d entries", len(c.Entries))
}

// SkipInput holds the facts for one batch row.
type SkipInput struct {
	Company string
	Entries []string
	// SingleExplicitURL is true for an explicit single-URL application, which
	// the list never blocks.
	SingleExplicitURL bool
	// DuplicateFact is true when the row is already applied; the fact is
	// reported in preference to the skip.
	DuplicateFact bool
}

// SkipDecision adds the entry that covered the company.
type SkipDecision struct {
	Decision
	Entry string
}

// SkipCompany decides whether a batch row is left to the user. A row on the
// list is skipped whatever site it uses and even when the user names it.
func SkipCompany(in SkipInput) SkipDecision {
	const g = GateSkipList
	if in.SingleExplicitURL {
		return SkipDecision{Decision: decide(g, Allow, CodeSkipSingleURL, "an explicit single-URL application is not blocked by the skip list")}
	}
	for _, e := range in.Entries {
		if !CoversCompany(e, in.Company) {
			continue
		}
		if in.DuplicateFact {
			return SkipDecision{decide(g, Deny, CodeDuplicateBeforeSkip, "%s is already applied; reported as that fact", in.Company), e}
		}
		return SkipDecision{decide(g, Deny, CodeSkipListed, "%s is on your batch skip list (as %q) — apply there yourself", in.Company, e), e}
	}
	return SkipDecision{Decision: decide(g, Allow, CodeOK, "%s is not on the skip list", in.Company)}
}

// genericCompanyWords are ignored by the word-set comparison, as in
// same-candidacy-strict.
var genericCompanyWords = map[string]bool{
	"uk": true, "us": true, "usa": true, "inc": true, "llc": true, "ltd": true, "corp": true,
	"co": true, "the": true, "group": true, "company": true, "gmbh": true, "plc": true,
}

// CoversCompany reports whether a skip-list name covers a company: the
// company word-set comparison of same-candidacy-strict matches, or both names
// belong to one alias group. Matching ignores case and is never a substring
// match.
func CoversCompany(entry, company string) bool {
	if wordSetsMatch(nameSets(entry), nameSets(company)) {
		return true
	}
	for _, grp := range aliasGroups {
		if inGroup(entry, grp) && inGroup(company, grp) {
			return true
		}
	}
	return false
}

func wordsOf(s string) []string {
	return strings.FieldsFunc(foldText(s), func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) })
}

func inGroup(name string, group []string) bool {
	for _, w := range wordsOf(name) {
		if slices.Contains(group, w) {
			return true
		}
	}
	return false
}

// nameSets returns one word set per name a company field carries: its text
// outside parentheses, then each parenthetical as an alias.
func nameSets(raw string) []map[string]bool {
	var names []string
	var primary strings.Builder
	depth, start := 0, 0
	for i, r := range raw {
		switch {
		case r == '(':
			if depth == 0 {
				start = i + 1
			}
			depth++
		case r == ')' && depth > 0:
			if depth--; depth == 0 {
				names = append(names, raw[start:i])
			}
		case depth == 0:
			primary.WriteRune(r)
		}
	}
	var out []map[string]bool
	for _, n := range append([]string{primary.String()}, names...) {
		set := map[string]bool{}
		for _, w := range wordsOf(n) {
			if !genericCompanyWords[w] {
				set[w] = true
			}
		}
		if len(set) > 0 {
			out = append(out, set)
		}
	}
	return out
}

func wordSetsMatch(a, b []map[string]bool) bool {
	subset := func(small, big map[string]bool) bool {
		for w := range small {
			if !big[w] {
				return false
			}
		}
		return true
	}
	for _, x := range a {
		for _, y := range b {
			if len(x) <= len(y) && subset(x, y) || len(y) < len(x) && subset(y, x) {
				return true
			}
		}
	}
	return false
}
