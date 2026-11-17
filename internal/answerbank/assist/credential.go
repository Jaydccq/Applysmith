package assist

import (
	"regexp"
	"strings"

	"applysmith/internal/lazyregexp"
)

// A degree or a professional credential is a claim a reader takes at its word,
// so a composed answer may name one only when the applicant's own facts do.
// Company text never grounds one: "our analysts are CFA charterholders" makes
// no applicant a holder.

// credentialForms are the spellings that name each credential. A lower-case
// form matches the applicant's facts case-insensitively at word boundaries; an
// upper-case form only as written, so "40 ms" is no Master of Science. MASTER
// and BACHELOR are the degree levels a "master's" or "bachelor's" claim names;
// an MS or a BS is also held by a recorded degree of its level ("Master's in
// Computer Science").
var credentialForms = func() map[string][]string {
	m := map[string][]string{
		"PHD":  {"phd", "ph.d", "dphil", "doctor of philosophy", "doctorate", "doctoral"},
		"MBA":  {"mba", "m.b.a", "master of business administration"},
		"MS":   {"MS", "m.s", "msc", "m.sc", "master of science"},
		"BS":   {"BS", "b.s", "bsc", "b.sc", "bachelor of science"},
		"MD":   {"MD", "m.d", "doctor of medicine"},
		"JD":   {"JD", "j.d", "juris doctor"},
		"CPA":  {"cpa", "certified public accountant"},
		"CFA":  {"cfa", "chartered financial analyst"},
		"FRM":  {"frm", "financial risk manager"},
		"CAIA": {"caia", "chartered alternative investment analyst"},
		"CFP":  {"cfp", "certified financial planner"},
		"PMP":  {"pmp", "project management professional"},
	}
	m["MASTER"] = append(append([]string{"master's", "master’s", "masters", "master of", "MA", "m.a", "meng", "m.eng", "mphil", "mfin", "mph", "m.tech", "mtech", "M.E", "m.com", "mcom", "llm"}, m["MS"]...), m["MBA"]...)
	m["BACHELOR"] = append([]string{"bachelor's", "bachelor’s", "bachelors", "bachelor of", "BA", "b.a", "bba", "beng", "b.eng", "b.tech", "btech", "B.E", "b.com", "bcom", "llb"}, m["BS"]...)
	return m
}()

// degreeWords are the words after "I hold a" that name a degree.
var degreeWords = map[string]string{
	"phd": "PHD", "dphil": "PHD", "doctorate": "PHD", "mba": "MBA", "ms": "MS", "msc": "MS", "bs": "BS", "bsc": "BS",
	"md": "MD", "jd": "JD", "master": "MASTER", "masters": "MASTER", "bachelor": "BACHELOR", "bachelors": "BACHELOR",
}

var (
	// credentialClaimRE is a first-person credential statement ("I am a
	// licensed ...", "As a certified ..."); the rest of the clause names the
	// credential.
	credentialClaimRE = lazyregexp.New(`(?i)\b(?:i\s+am|i['\x{2019}]m|i\s+became|i['\x{2019}]ve\s+been|i\s+have\s+been|i\s+was|as)\s+(?:an?\s+)?(?:(?:fully|currently|also|now|newly|recently)\s+)?(board[- ]certified|licensed|certified|chartered|registered)\b([^.!?;:,\n]*)`)
	charterholderRE   = lazyregexp.New(`(?i)\bcharterholders?\b`)
	degreeClaimRE     = lazyregexp.New(`(?i)\bi\s+(?:also\s+)?(?:hold|have|earned|completed|received|obtained)\s+(?:an?|my|the)\s+([a-z][a-z.'\x{2019}]*)`)
	// msProductRE is Microsoft's "MS Excel", not a degree.
	msProductRE = lazyregexp.New(`\bMS\s+(?:Office|Excel|Word|PowerPoint|Outlook|Teams|Access|Project|Visio|SQL|Azure|Dynamics)\b`)
)

// claimStems are the words of the applicant's facts that hold a credential
// claim naming no credential ("I am licensed to practice").
var claimStems = map[string]string{"licensed": "licens", "certified": "certif", "chartered": "charter", "registered": "regist", "board certified": "certif"}

// claimStop ends the credential a claim names.
var claimStop = setOf("and", "or", "with", "at", "for", "since", "who", "which", "that", "from", "by", "in", "to", "of", "where", "when", "while",
	"through", "under", "on", "but", "so", "because", "i", "my", "we")

// credentialKey reads a span as a credential acronym: "CFA", "PhD", "M.B.A.".
func credentialKey(text string) (string, bool) {
	bare := strings.ReplaceAll(text, ".", "")
	k := strings.ToUpper(bare)
	if _, ok := credentialForms[k]; !ok || k == "MASTER" || k == "BACHELOR" {
		return "", false
	}
	// An acronym is written in capitals, PhD in any case; "Ms" is a title.
	if bare != k && k != "PHD" {
		return "", false
	}
	return k, true
}

// levelOf is the degree level an MS or a BS falls back to.
var levelOf = map[string]string{"MS": "MASTER", "BS": "BACHELOR"}

// holds reports whether the applicant's own facts name the credential. A
// recorded credential that names the level alone ("Master Finance") holds
// that level; elsewhere a bare "master" may be a Scrum Master.
func (g *grounding) holds(key string) bool {
	if l, ok := levelOf[key]; ok && g.holds(l) {
		return true
	}
	if key == "MASTER" && wordBoundaryContains(g.creds, "master") || key == "BACHELOR" && wordBoundaryContains(g.creds, "bachelor") {
		return true
	}
	for _, f := range credentialForms[key] {
		if f != strings.ToLower(f) {
			if wordBoundaryContains(g.own, f) {
				return true
			}
		} else if wordBoundaryContains(g.ownLower, f) {
			return true
		}
	}
	return false
}

// masked blanks the names inside which a credential acronym is no claim.
func (g *grounding) masked(text string) string {
	text = msProductRE.ReplaceAllString(text, " ")
	for _, n := range g.masks {
		if n = strings.TrimSpace(n); n != "" {
			text = regexp.MustCompile(`(?i)\b`+regexp.QuoteMeta(n)+`\b`).ReplaceAllString(text, " ")
		}
	}
	return text
}

// unheldClaims are the credential claims in words ("I am a licensed
// professional engineer", "charterholder", "I hold a doctorate") the
// applicant's own facts do not hold. Each is one claim, which salvage may
// strip with its sentence.
func (g *grounding) unheldClaims(text string) []span {
	var out []span
	for _, m := range credentialClaimRE.FindAllStringSubmatch(text, -1) {
		if name, ok := g.claimHeld(m[1], m[2]); !ok {
			out = append(out, span{Text: name, Kind: spanName, Words: 1})
		}
	}
	for _, w := range charterholderRE.FindAllString(text, -1) {
		if !strings.Contains(g.ownLower, "charter") && !g.holds("CFA") && !g.holds("CAIA") {
			out = append(out, span{Text: w, Kind: spanName, Words: 1})
		}
	}
	for _, m := range degreeClaimRE.FindAllStringSubmatch(text, -1) {
		w := strings.TrimRight(m[1], ".")
		norm := strings.ToLower(strings.ReplaceAll(w, ".", ""))
		norm = strings.TrimSuffix(strings.TrimSuffix(norm, "'s"), "’s")
		if key, ok := degreeWords[norm]; ok && !g.holds(key) {
			out = append(out, span{Text: w, Kind: spanName, Words: 1})
		}
	}
	return out
}

// claimHeld reads the credential a claim word and the rest of its clause name
// ("certified" + " public accountant at ...") and reports whether the
// applicant's own facts hold it: by a known credential's spellings, by one
// recorded credential, or one fact that also uses a claim word, that holds
// every word of it (a "Financial Analyst" title is no certified financial
// analyst), or, when it names none, by a fact that uses the claim word.
func (g *grounding) claimHeld(claim, rest string) (string, bool) {
	var words []string
	for _, w := range strings.Fields(rest) {
		low := strings.ToLower(w)
		if len(words) == 0 && (low == "in" || low == "as" || low == "a" || low == "an" || low == "the") {
			continue
		}
		if claimStop[low] || len(words) == 4 {
			break
		}
		words = append(words, w)
	}
	name := strings.TrimSpace(claim + " " + strings.Join(words, " "))
	phrase := strings.ToLower(strings.Join(words, " "))
	whole := strings.ToLower(name)
	for key, forms := range credentialForms {
		for _, f := range forms {
			if strings.Contains(f, " ") && (f == whole || f == phrase) {
				return name, g.holds(key)
			}
		}
	}
	if len(words) == 0 {
		return name, strings.Contains(g.ownLower, claimStems[strings.ToLower(strings.ReplaceAll(claim, "-", " "))])
	}
	holdsAll := func(line string) bool {
		for _, w := range strings.Fields(phrase) {
			if !wordBoundaryContains(line, w) {
				return false
			}
		}
		return true
	}
	for _, f := range strings.Split(g.creds, "\n") {
		if holdsAll(f) {
			return name, true
		}
	}
	for _, f := range strings.Split(g.ownLower, "\n") {
		if holdsAll(f) && usesClaimWord(f) {
			return name, true
		}
	}
	return name, false
}

// usesClaimWord reports a fact that states a credential in words.
func usesClaimWord(f string) bool {
	for _, stem := range claimStems {
		if strings.Contains(f, stem) {
			return true
		}
	}
	return false
}
