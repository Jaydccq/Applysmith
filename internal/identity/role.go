package identity

import (
	"slices"
	"strings"

	"applysmith/internal/lazyregexp"
)

// boilerplate words carry no information about which role is meant.
var boilerplate = wordSet(strings.Fields(
	"and the of for a an in at to with remote hybrid onsite full time part contract temporary permanent new hiring urgent opportunity position role job us usa"))

var (
	yearRE   = lazyregexp.New(`^(19|20)\d\d$`)
	numberRE = lazyregexp.New(`^\d{3,}$`)
	// langReplacer keeps "C++", "C#" and ".NET" apart from plain "C" and "NET".
	langReplacer = strings.NewReplacer("c++", "cpp", "c#", "csharp", "f#", "fsharp", ".net", "dotnet")
)

// roleWords returns the folded words of a role, in order. "Unknown role", what
// an evaluation files for a posting that names none, has no words, so it is no
// role that two postings could share.
func roleWords(role string) []string {
	if compact(role) == "unknownrole" {
		return nil
	}
	return words(langReplacer.Replace(fold(role)))
}

// companyTailRE splits a page title's employer tail off a role: "Full Stack
// Engineer @ expensify" (an Ashby page title), "Software Engineer at Chime" (a
// Greenhouse one).
var companyTailRE = lazyregexp.New(`(?i)^(.*\S)\s+(?:@|at)\s+(\S.*?)\s*$`)

// RoleWithoutCompany drops a tail that names the company, " @ Company" or
// " at Company", from a role; a feed may write the company short, as the
// leading words of its name ("Product Designer - NY @ Scope" for Scope Labs),
// or long, its name and more ("... @ Rainmaker Technology Corporation" for
// Rainmaker).
// A tail naming anything else stays: "Engineer at Scale" is a role of Example
// Corp.
func RoleWithoutCompany(role, company string) string {
	m := companyTailRE.FindStringSubmatch(role)
	if m == nil {
		return role
	}
	names := func(s string) []string {
		out := []string{compact(s)}
		for _, k := range CompanyKeys(s) {
			out = append(out, strings.ReplaceAll(k, " ", ""))
		}
		return out
	}
	own := names(company)
	for _, t := range names(m[2]) {
		if t != "" && slices.Contains(own, t) {
			return m[1]
		}
	}
	for _, t := range CompanyKeys(m[2]) {
		for _, k := range CompanyKeys(company) {
			if strings.HasPrefix(k, t+" ") || strings.HasPrefix(t, k+" ") {
				return m[1]
			}
		}
	}
	return role
}

// strictRole is the same-candidacy-strict form: lowercase, every run of
// non-alphanumeric characters one space, trimmed. Qualifiers stay.
func strictRole(role string) string { return strings.Join(roleWords(role), " ") }

// openingRole is the same-opening reduction of a role: its words without
// boilerplate, years and requisition numbers, plus the requisition numbers.
type openingRole struct {
	words []string
	reqs  []string
}

func reduceRole(role string) openingRole {
	var r openingRole
	for _, w := range roleWords(role) {
		switch {
		case boilerplate[w] || yearRE.MatchString(w):
		case numberRE.MatchString(w):
			if !slices.Contains(r.reqs, w) {
				r.reqs = append(r.reqs, w)
			}
		default:
			r.words = append(r.words, w)
		}
	}
	return r
}

// rolesMatch implements the same-opening role test: differing requisition
// numbers end the comparison; otherwise the word lists are identical or both
// hold at least two words with Jaccard overlap of at least 0.8.
func rolesMatch(a, b openingRole) bool {
	if len(a.words) == 0 || len(b.words) == 0 {
		return false
	}
	if len(a.reqs) > 0 && len(b.reqs) > 0 && !sameStrings(a.reqs, b.reqs) {
		return false
	}
	if slices.Equal(a.words, b.words) {
		return true
	}
	if len(a.words) < 2 || len(b.words) < 2 {
		return false
	}
	sa, sb := wordSet(a.words), wordSet(b.words)
	inter := 0
	for w := range sa {
		if sb[w] {
			inter++
		}
	}
	union := len(sa) + len(sb) - inter
	return inter*5 >= union*4
}

func sameStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for _, x := range a {
		if !slices.Contains(b, x) {
			return false
		}
	}
	return true
}

// placeTokens are the normalized tokens of a location text. Work-mode words
// carry no place, so "Remote" alone has no tokens.
func placeTokens(location string) []string {
	var out []string
	for _, w := range words(fold(location)) {
		switch w {
		case "remote", "hybrid", "onsite", "on", "site", "in", "and", "or", "the", "of":
			continue
		}
		if !slices.Contains(out, w) {
			out = append(out, w)
		}
	}
	slices.Sort(out)
	return out
}

// placesConflict holds when both locations name a place and share no token.
func placesConflict(a, b string) bool {
	ta, tb := placeTokens(a), placeTokens(b)
	if len(ta) == 0 || len(tb) == 0 {
		return false
	}
	for _, t := range ta {
		if slices.Contains(tb, t) {
			return false
		}
	}
	return true
}
