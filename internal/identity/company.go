package identity

import (
	"slices"
	"strings"
)

// placeholderCompanies name no employer. Entries are compared in compact form.
// "Unknown company" is what an evaluation files for a posting that names none.
var placeholderCompanies = wordSet([]string{
	"unknown", "unknowncompany", "na", "notapplicable", "tbd", "none", "null", "confidential", "companyconfidential",
	"stealth", "stealthstartup", "stealthmode", "stealthmodestartup", "undisclosed", "unspecified",
	"notspecified", "anonymous",
})

// legalSuffixes are dropped from the end of a company name by same-opening.
var legalSuffixes = wordSet([]string{
	"inc", "incorporated", "llc", "llp", "lp", "ltd", "limited", "corp", "corporation", "co", "company",
	"plc", "gmbh", "ag", "sa", "srl", "bv", "nv", "pty", "pte",
})

// genericCompanyWords are ignored by same-candidacy-strict.
var genericCompanyWords = wordSet([]string{
	"uk", "us", "usa", "inc", "llc", "ltd", "corp", "co", "the", "group", "company", "gmbh", "plc",
})

// companyNames returns the names a company field carries: its text without
// parenthetical annotations, then each parenthetical as an alias.
func companyNames(raw string) []string {
	var primary strings.Builder
	var aliases []string
	depth, start := 0, 0
	for i, r := range raw {
		switch {
		case r == '(':
			if depth == 0 {
				start = i + 1
			}
			depth++
		case r == ')' && depth > 0:
			depth--
			if depth == 0 {
				aliases = append(aliases, raw[start:i])
			}
		case depth == 0:
			primary.WriteRune(r)
		}
	}
	var out []string
	for _, n := range append([]string{primary.String()}, aliases...) {
		if n = strings.Join(strings.Fields(n), " "); n != "" {
			out = append(out, n)
		}
	}
	return out
}

func placeholderName(name string) bool {
	c := compact(name)
	return c == "" || placeholderCompanies[c]
}

// companyPunct reads "&" as "and" and drops apostrophes and dots.
var companyPunct = strings.NewReplacer("&", " and ", "'", "", "’", "", ".", "")

// openingCompanyNames returns the normalized names of a company for
// same-opening: case and accents folded, "&" read as "and", punctuation
// dropped, a leading "the" and trailing legal suffixes removed. Placeholder
// names are left out, so a placeholder company yields none.
func openingCompanyNames(raw string) []string {
	var out []string
	for _, n := range companyNames(raw) {
		if placeholderName(n) {
			continue
		}
		f := companyPunct.Replace(fold(n))
		ws := words(f)
		if len(ws) > 1 && ws[0] == "the" {
			ws = ws[1:]
		}
		for len(ws) > 1 && legalSuffixes[ws[len(ws)-1]] {
			ws = ws[:len(ws)-1]
		}
		if name := strings.Join(ws, " "); name != "" && !slices.Contains(out, name) {
			out = append(out, name)
		}
	}
	return out
}

// strictCompanySets returns, per name, the word set same-candidacy-strict
// compares: generic words removed. Empty sets are left out.
func strictCompanySets(raw string) []map[string]bool {
	var out []map[string]bool
	for _, n := range companyNames(raw) {
		if placeholderName(n) {
			continue
		}
		set := map[string]bool{}
		for _, w := range words(fold(n)) {
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

func subset(small, big map[string]bool) bool {
	for w := range small {
		if !big[w] {
			return false
		}
	}
	return true
}

func companySetsMatch(a, b []map[string]bool) bool {
	for _, x := range a {
		for _, y := range b {
			if len(x) <= len(y) && subset(x, y) || len(y) < len(x) && subset(y, x) {
				return true
			}
		}
	}
	return false
}

func sharesName(a, b []string) bool {
	for _, x := range a {
		if slices.Contains(b, x) {
			return true
		}
	}
	return false
}
