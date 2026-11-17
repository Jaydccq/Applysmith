package identity

// SameOpening reports whether two postings are one opening, for de-duplication
// in discovery and the tracker. Equal identity keys are the same posting; two
// unequal ATS-native keys are never the same opening, because one employer can
// hold several requisitions with one title. Otherwise the companies must
// match, the roles must match and the places must not conflict.
func SameOpening(a, b Posting) bool {
	ka, kb := a.IdentityKey(), b.IdentityKey()
	if ka != "" && ka == kb {
		return true
	}
	if IsATSNative(ka) && IsATSNative(kb) {
		return false
	}
	if !sharesName(openingCompanyNames(a.Company), openingCompanyNames(b.Company)) {
		return false
	}
	if !rolesMatch(reduceRole(a.Role), reduceRole(b.Role)) {
		return false
	}
	return !placesConflict(a.Location, b.Location)
}

// SameCandidacyStrict reports whether two applications are the same, for the
// apply duplicate guard. It ignores location, so it matches more often, which
// is the safe direction for a guard against applying twice.
func SameCandidacyStrict(a, b Posting) bool { return StrictOf(a).Same(StrictOf(b)) }

// Strict is a posting reduced for SameCandidacyStrict, so a caller that tests
// one posting against many reduces each posting once.
type Strict struct {
	key, role string
	companies []map[string]bool
}

func StrictOf(p Posting) Strict {
	return Strict{key: p.IdentityKey(), role: strictRole(p.Role), companies: strictCompanySets(p.Company)}
}

// Same is SameCandidacyStrict of the two postings.
func (a Strict) Same(b Strict) bool {
	if a.key != "" && a.key == b.key {
		return true
	}
	return companySetsMatch(a.companies, b.companies) && a.role != "" && a.role == b.role
}

// dedupFiller are the grammatical words the tracker's dedup command ignores in
// a role. Seniority and employment-type words are not among them.
var dedupFiller = wordSet([]string{"and", "the", "of", "for", "a", "an", "in", "at", "to", "with"})

// levelWords identify a rung, not a subject.
var levelWords = wordSet([]string{
	"i", "ii", "iii", "iv", "v", "vi", "vii", "viii", "ix", "x", "jr", "sr", "junior", "senior", "intern",
	"internship", "staff", "principal", "lead", "associate", "entry", "mid", "graduate", "grad", "trainee",
})

// SameForDedup is the test the tracker's dedup command applies before it
// deletes a row. Unlike SameOpening it has no overlap threshold: roles match
// only when their titles are equal or their words, minus grammatical filler,
// are the same set. Seniority and employment-type words count, two-letter
// words count, and a role made only of level markers matches only by exact
// title. Differing requisition numbers or places, unequal ATS-native keys and
// placeholder companies never match.
func SameForDedup(a, b Posting) bool {
	ka, kb := a.IdentityKey(), b.IdentityKey()
	if ka != "" && ka == kb {
		return true
	}
	if IsATSNative(ka) && IsATSNative(kb) {
		return false
	}
	if !sharesName(openingCompanyNames(a.Company), openingCompanyNames(b.Company)) {
		return false
	}
	if placesConflict(a.Location, b.Location) {
		return false
	}
	ra, rb := strictRole(a.Role), strictRole(b.Role)
	if ra == "" || rb == "" {
		return false
	}
	if ra == rb {
		return true
	}
	sa, sb := dedupWords(a.Role), dedupWords(b.Role)
	return len(sa) == len(sb) && subset(sa, sb) && !onlyLevels(sa)
}

func dedupWords(role string) map[string]bool {
	set := map[string]bool{}
	for _, w := range roleWords(role) {
		if !dedupFiller[w] {
			set[w] = true
		}
	}
	return set
}

func onlyLevels(set map[string]bool) bool {
	for w := range set {
		if !levelWords[w] && !(isDigits(w) && len(w) <= 2) {
			return false
		}
	}
	return true
}

// CompanyKeys returns the normalized names a company field answers to: its
// name and any parenthetical aliases, without legal suffixes. A placeholder
// yields none. Callers use it to bucket rows before comparing pairs.
func CompanyKeys(company string) []string { return openingCompanyNames(company) }
