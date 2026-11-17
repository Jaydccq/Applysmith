package match

import (
	"fmt"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	"applysmith/internal/lazyregexp"
)

// The owner lists every certification and license held in one entry,
// certifications-held, one per line. A question whether one is held is Yes
// when the list names it and No when no line names any word of it; a line
// that names part of it, and a question about a class of credentials (one it
// describes in common words, never names), leave it to the user. An answer the user gave a credential's own entry wins, and
// a blank list leaves the question to the user (Skydio's FAA Part 107).

const idCertificationsHeld = "certifications-held"

// credentialIDs are the entries the certifications list answers.
var credentialIDs = []string{idCertificationsHeld, "faa-part-107", "security-plus-certification"}

var (
	// heldQuestionRE asks whether a credential is held, not for the list.
	heldQuestionRE = lazyregexp.New(`(?i)^\W*(?:do|does|did)\s+you\b|^\W*are\s+you\b|^\W*have\s+you\b`)
	licenceAskedRE = lazyregexp.New(`(?i)\blicen[cs]e[sd]?\b`)
	// alternativeRE splits "an AWS or GCP certification" into what each
	// alternative names.
	alternativeRE  = lazyregexp.New(`(?i)\s+or\s+|/`)
	credentialWord = lazyregexp.New(`[a-z0-9]+(?:\+)?`)
	// writtenWord is a word as the question writes it, its case kept.
	writtenWord = lazyregexp.New(`[\p{L}\p{N}]+\+?`)
)

// credentialFiller are the words of a credential question that name no
// credential.
var credentialFiller = map[string]bool{
	"do": true, "does": true, "did": true, "you": true, "your": true, "are": true, "is": true, "have": true, "has": true,
	"hold": true, "holds": true, "held": true, "possess": true, "maintain": true, "currently": true, "current": true,
	"active": true, "valid": true, "a": true, "an": true, "the": true, "any": true, "of": true, "in": true, "on": true,
	"for": true, "or": true, "and": true, "to": true, "with": true, "certified": true, "certification": true,
	"certifications": true, "certificate": true, "certificates": true, "credential": true, "credentials": true,
	"license": true, "licenses": true, "licence": true, "licences": true, "licensed": true, "obtained": true,
	"earned": true, "professional": true, "which": true, "what": true, "following": true, "please": true, "list": true,
	"this": true, "role": true, "position": true, "job": true, "required": true, "now": true, "still": true,
	"registered": true, "registration": true, "chartered": true, "charter": true, "charterholder": true, "holder": true,
	"designation": true,
}

// credentialPlace are words that place or describe a credential without
// naming it ("a certification in this field", "a valid license for this
// state").
var credentialPlace = setOf("field", "state", "area", "industry", "domain", "discipline", "sector", "specialty",
	"jurisdiction", "country", "related", "relevant", "appropriate", "applicable", "equivalent", "similar")

// credentialWording reads the words of the asked sentence beside its filler.
// named: one names a credential by its own name, a word with a digit or a
// plus sign, or one written with a capital ("Series 7", "CFA", "Security+",
// "Google Cloud"); a place ("in California", "this State") names none, and in
// a sentence of capitalised words only a word in capitals does. described:
// some word is there at all. A question written only in common words ("a
// nursing license", "a certification in this field") describes a class of
// credentials, which the list cannot say is not held.
func credentialWording(question string) (named, described bool) {
	asked, _ := scanCountries(askedSentence(question), false)
	words := writtenWord.FindAllString(stateNameRE.ReplaceAllString(asked, " "), -1)
	titled := !slices.ContainsFunc(words, func(w string) bool { r, _ := utf8.DecodeRuneInString(w); return unicode.IsLower(r) })
	for _, w := range words {
		lw := strings.ToLower(strings.TrimSuffix(w, "+"))
		if credentialFiller[lw] {
			continue
		}
		described = true
		if credentialPlace[lw] {
			continue
		}
		capital := strings.IndexFunc(w, unicode.IsUpper) >= 0
		if titled {
			capital = utf8.RuneCountInString(w) > 1 && strings.ToUpper(w) == w
		}
		if capital || strings.HasSuffix(w, "+") || strings.IndexFunc(w, unicode.IsDigit) >= 0 {
			named = true
		}
	}
	return named, described
}

// stateNameRE is a US state's full name, longer names first so "West
// Virginia" is not read as Virginia.
var stateNameRE = lazyregexp.New(func() string {
	names := make([]string, len(usStateRows))
	for i, r := range usStateRows {
		names[i] = r.Name
	}
	slices.SortFunc(names, func(a, b string) int { return len(b) - len(a) })
	return `(?i)\b(?:` + strings.Join(names, "|") + `)\b`
}())

// spelledOut reports a credential one side writes as initials and the other
// in full ("CPA", "Certified Public Accountant").
func spelledOut(a, b string) bool {
	for _, pair := range [][2]string{{a, b}, {b, a}} {
		runs := capitalRuns(pair[1])
		for w := range capitalWords(pair[0]) {
			if spells(runs, w) {
				return true
			}
		}
	}
	return false
}

// credentialTokens are the words that name a credential, "+" spelled "plus".
func credentialTokens(text string) []string {
	var out []string
	for _, w := range credentialWord.FindAllString(strings.ToLower(strings.ReplaceAll(text, "+", " plus ")), -1) {
		if !credentialFiller[w] && !credentialPlace[w] {
			out = append(out, w)
		}
	}
	return out
}

// certificationsList is the user's list, one credential a line.
func certificationsList(entries []Entry) []string {
	for i := range entries {
		if entries[i].ID != idCertificationsHeld {
			continue
		}
		var out []string
		for _, l := range strings.Split(ownText(&entries[i]), "\n") {
			if l = strings.TrimSpace(strings.TrimLeft(l, "-*• \t")); l != "" {
				out = append(out, l)
			}
		}
		return out
	}
	return nil
}

// credentialClassRE asks about a class of credentials ("any cloud
// certifications", "industry related certifications"), which no line's words
// place a credential outside of.
var credentialClassRE = lazyregexp.New(`(?i)\bany\b|\b(?:certifications|certificates|credentials|licen[cs]es)\b`)

// listHolds reports the line of the list that names a credential a question
// asks about: one holding every word that names it, or that names one of its
// alternatives ("AWS or GCP"), or that spells out its initials ("Certified
// Public Accountant" for "CPA"). A question that names none ("Do you have any
// certifications?") is held by any line, a license by a line naming a license.
// known is false when no line holds it and the list cannot say it is not
// held: a line names part of what is asked ("AWS" of "cloud certifications
// (AWS, GCP, Azure)"), or the question asks about a class, which it may also
// describe in common words without naming any credential (credentialWording).
func listHolds(list []string, question string) (line string, held, known bool) {
	var names [][]string
	for _, part := range alternativeRE.Split(question, -1) {
		if name := credentialTokens(part); len(name) > 0 {
			names = append(names, name)
		}
	}
	named, described := credentialWording(question)
	class := described && !named
	known = !class && (len(names) == 0 || !credentialClassRE.MatchString(question))
	for _, l := range list {
		if spelledOut(question, l) {
			return l, true, true
		}
		if len(names) == 0 {
			if class {
				continue
			}
			if !licenceAskedRE.MatchString(question) || licenceAskedRE.MatchString(l) {
				return l, true, true
			}
			continue
		}
		item := credentialTokens(l)
		for _, name := range names {
			if !slices.ContainsFunc(name, func(w string) bool { return !slices.Contains(item, w) }) {
				return l, true, true
			}
			if slices.ContainsFunc(name, func(w string) bool { return slices.Contains(item, w) }) {
				known = false
			}
		}
	}
	return "", false, known
}

// deriveCredential answers a credential entry: the user's own answer to a
// credential's entry, else the list; a request for the list is the list.
func deriveCredential(s *Step, e *Entry) (Draft, bool) {
	if own := ownText(e); own != "" && e.ID != idCertificationsHeld {
		return bindEntryAnswer(s, e, own, "the user's own answer"), true
	}
	list := certificationsList(s.Entries)
	if len(list) == 0 {
		return abstainDraft(ReasonPlaceholderEmpty, "", "%s: blank, and so is the certifications list", e.ID), true
	}
	if !heldQuestionRE.MatchString(s.Q.Text) {
		return bindEntryAnswer(s, e, strings.Join(list, "\n"), "the certifications list"), true
	}
	l, held, known := listHolds(list, s.Q.Text)
	switch {
	case held:
		return bindEntryAnswer(s, e, "Yes", fmt.Sprintf("the certifications list holds %q", l)), true
	case !known:
		return abstainDraft(ReasonAmbiguous, "", "%s: the certifications list names part of what is asked, or a class is asked about -> left for you", e.ID), true
	}
	return bindEntryAnswer(s, e, "No", "the certifications list names no such credential"), true
}
