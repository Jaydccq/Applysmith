package signals

import (
	"strings"

	"applysmith/internal/lazyregexp"
)

// brandingWords are dropped from the end of a company name.
var brandingWords = map[string]bool{"inc": true, "llc": true, "corp": true, "co": true, "labs": true, "ai": true,
	"technologies": true, "tech": true}

var (
	nonWordRE   = lazyregexp.New(`[^\p{L}\p{N}\s]+`)
	trailParen  = lazyregexp.New(`\s*\([^()]*\)\s*$`)
	newsVerbRE  = lazyregexp.New(`(?i)\b(?:raises|launches|announces|unveils|closes|secures|lands|debuts|introduces|acquires)\b`)
	moneyRE     = lazyregexp.New(`(?i)(?:[$\x{20ac}\x{a3}]\s?\d|\b\d[\d.,]*\s?(?:million|billion|thousand)\b)`)
	roundRE     = lazyregexp.New(`(?i)\b(?:series\s+[a-z]\b|seed\s+round|pre-seed|funding\s+round|round\s+of)\b`)
	urlRE       = lazyregexp.New(`(?i)(?:https?://|\bwww\.|\b[a-z0-9-]+\.(?:com|io|ai|co|net|org|dev|app|xyz|so|tech)\b)`)
	legalSuffix = lazyregexp.New(`(?i)[,\s]+(?:inc|llc|ltd|corp|corporation|co|gmbh|plc)\.?$`)
)

// RuleCanon canonicalizes a company name by rules alone: lowercase, strip
// punctuation, drop trailing branding and legal words. It is empty when the
// rules leave nothing.
func RuleCanon(raw string) string {
	s := strings.ToLower(nonWordRE.ReplaceAllString(raw, " "))
	words := strings.Fields(s)
	for len(words) > 0 && brandingWords[words[len(words)-1]] {
		words = words[:len(words)-1]
	}
	return strings.Join(words, " ")
}

// NotAnEntity reports why a candidate cannot be a company name; the guard only
// rejects and never rewrites. Empty means the name passes.
func NotAnEntity(name string) string {
	name = strings.TrimSpace(name)
	switch {
	case name == "":
		return "empty"
	case len(name) > 60:
		return "longer than 60 characters"
	case len(strings.Fields(name)) > 8:
		return "more than 8 words"
	case newsVerbRE.MatchString(name):
		return "contains a news verb"
	case moneyRE.MatchString(name), roundRE.MatchString(name):
		return "contains a funding amount or round marker"
	case urlRE.MatchString(name):
		return "contains a URL or domain"
	}
	return ""
}

// stripLegal drops one trailing legal suffix from a display name.
func stripLegal(s string) string {
	return strings.TrimSpace(legalSuffix.ReplaceAllString(strings.TrimSpace(s), ""))
}

var (
	bulletRE     = lazyregexp.New(`^\s*(?:[-*\x{2022}]|\d{1,2}[.)])\s+(.+)$`)
	roundupSplit = lazyregexp.New(`^(.{1,80}?)(?::\s|\s[-\x{2013}\x{2014}]\s)`)
)

const (
	roundupMin = 3
	roundupMax = 25
)

// Roundup reads a document that lists many companies as bullet lines of
// "Name: blurb" and returns the distinct names that pass the entity guard.
// A colon may hug the name; a dash must be spaced. It yields nothing unless
// 3 to 25 names are found.
func Roundup(body string) []string {
	var names []string
	seen := map[string]bool{}
	for _, line := range strings.Split(body, "\n") {
		m := bulletRE.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		head := roundupSplit.FindStringSubmatch(strings.TrimSpace(m[1]))
		if head == nil {
			continue
		}
		name := stripLegal(strings.Trim(head[1], " *_\"'"))
		if NotAnEntity(name) != "" {
			continue
		}
		key := RuleCanon(name)
		if key == "" || seen[key] {
			continue
		}
		seen[key] = true
		names = append(names, name)
	}
	if len(names) < roundupMin || len(names) > roundupMax {
		return nil
	}
	return names
}

var locationWords = map[string]bool{
	"remote": true, "onsite": true, "hybrid": true, "wfh": true, "fulltime": true, "parttime": true, "full": true, "time": true,
	"part": true, "contract": true, "global": true, "worldwide": true, "anywhere": true, "us": true, "usa": true, "uk": true,
	"eu": true, "emea": true, "apac": true, "europe": true, "asia": true, "canada": true, "germany": true, "france": true,
	"india": true, "israel": true, "japan": true, "australia": true, "netherlands": true, "spain": true, "ireland": true,
	"brazil": true, "mexico": true, "singapore": true, "ny": true, "nyc": true, "sf": true, "la": true, "bay": true,
	"area": true, "san": true, "francisco": true, "new": true, "york": true, "london": true, "berlin": true, "paris": true,
	"toronto": true, "seattle": true, "boston": true, "austin": true, "chicago": true, "denver": true, "amsterdam": true,
	"dublin": true, "remoteus": true, "in": true, "or": true, "and": true, "visa": true, "sponsorship": true, "relocation": true,
}

// locationOnly reports whether a segment is made only of location and
// work-mode words, so it names no company.
func locationOnly(seg string) bool {
	words := strings.FieldsFunc(strings.ToLower(seg), func(r rune) bool {
		return !(r >= 'a' && r <= 'z') && !(r >= '0' && r <= '9')
	})
	if len(words) == 0 {
		return true
	}
	for _, w := range words {
		if !locationWords[w] {
			return false
		}
	}
	return true
}

// HiringTitleCompany reads the company from the title line of a hiring-thread
// comment by rule: the first segment before a pipe, with a trailing
// parenthetical (a funding stage, say) dropped. A first segment that is only a
// location or work-mode word names no company, so it returns false and the
// generic identification path takes over.
func HiringTitleCompany(title string) (string, bool) {
	if !strings.Contains(title, "|") {
		return "", false
	}
	seg, _, _ := strings.Cut(title, "|")
	seg = strings.TrimSpace(trailParen.ReplaceAllString(strings.TrimSpace(seg), ""))
	if seg == "" || locationOnly(seg) || NotAnEntity(seg) != "" {
		return "", false
	}
	return seg, true
}
