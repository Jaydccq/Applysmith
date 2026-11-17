package match

import (
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	"applysmith/internal/gates"
	"applysmith/internal/lazyregexp"
)

// Skill screening ("Do you have experience with X?") answers Yes only from
// evidence the user recorded: a skill in the profile or a term on the entry. It
// answers No for a blacklisted term, when the entry declares a negative answer,
// and, for a question about the user's own experience, when none of the user's
// information mentions what it asks about (F-10); it abstains when that
// information names it, for the evidence lanes. A threshold the role sets is
// neither: whether the user meets it is not a matter of which skills they list.

var (
	symbolTokenSplitRE = lazyregexp.New(`[^a-z0-9+#]+`)
	// quantifiedYearsRE reads a years threshold ("3+ years", "at least 5 years",
	// "2 or more years", "3-5 years").
	quantifiedYearsRE = lazyregexp.New(`(?i)\b\d+\s*\+\s*(?:years?|yrs?)\b|\b(?:at\s+least|minimum\s+of|min\.?|more\s+than|over)\s+\d+\s*\+?\s*(?:years?|yrs?)\b|\b\d+\s*(?:or\s+more|and\s+(?:above|over|up))\s+(?:years?|yrs?)\b|\b\d+\s*(?:-|to|\x{2013})\s*\d+\s*(?:years?|yrs?)\b`)
)

// skillScreening resolves a fixed entry that declares a skill_mode.
func skillScreening(s *Step, e *Entry) (Draft, bool) {
	if e.SkillMode == "" {
		return Draft{}, false
	}
	q := s.Q.Text
	positive := strings.TrimSpace(e.Answer)
	if positive == "" {
		positive = "Yes"
	}
	negative := strings.TrimSpace(e.SkillNegativeAnswer)
	declared := negative != ""
	if !declared {
		negative = "No"
	}
	fixed := func(answer, why string) (Draft, bool) {
		return Draft{Answer: answer, Source: SourceEntry, Deterministic: true, Trace: []string{why}}, true
	}

	if blocked := matchingSkillTerms(q, cleanSkillTerms(e.SkillBlacklistTerms)); len(blocked) > 0 {
		return fixed(negative, "skill-screening blacklist matched ["+strings.Join(blocked, ", ")+"] -> "+negative)
	}
	if e.SkillMode == SkillManualYes {
		return fixed(positive, "skill-screening mode=manual_yes and no blacklist hit -> "+positive)
	}
	if quantifiedYearsRE.MatchString(q) || gates.RoleRequirement(q) {
		return abstainDraft(ReasonNoEntry, "",
			"skill-screening: a threshold the role sets is never answered from the presence or absence of a skill -> abstain"), true
	}
	profileTerms := make([]string, 0, len(s.Req.Facts.Skills))
	for _, sk := range s.Req.Facts.Skills {
		profileTerms = append(profileTerms, sk.Name)
	}
	supported := expandSkillTerms(append(append([]string(nil), e.SkillTerms...), profileTerms...))
	if missing, joins := uncoveredOperands(q, supported); joins && len(missing) > 0 {
		why := "skill-screening mode=profile_skill: the question joins skills and [" + strings.Join(missing, ", ") + "] has no recorded evidence"
		if declared {
			return fixed(negative, why+"; the entry declares a negative answer -> "+negative)
		}
		if ownAbsence(s, strings.Join(missing, ", ")).Absent() {
			return fixed(negative, why+"; none of the user's information mentions it -> "+negative+" (F-10)")
		}
		return abstainDraft(ReasonNoEntry, "", "%s -> abstain", why), true
	}
	if matched := matchingSkillTerms(q, supported); len(matched) > 0 {
		return fixed(positive, "skill-screening mode=profile_skill matched ["+strings.Join(matched, ", ")+"] -> "+positive)
	}
	if declared {
		return fixed(negative, "skill-screening mode=profile_skill found no supported term; the entry declares a negative answer -> "+negative)
	}
	if o := ownAbsence(s, q); o.Absent() {
		return fixed(negative, "skill-screening: none of the user's information mentions ["+strings.Join(o.Terms, ", ")+"] -> "+negative+" (F-10)")
	} else if len(o.Found) > 0 {
		s.Tracef("skill-screening: the user's information mentions [%s]: left to the evidence lanes", strings.Join(o.Found, ", "))
	}
	reason := ReasonNoEntry
	if len(supported) == 0 {
		reason = ReasonPlaceholderEmpty
	}
	return abstainDraft(reason, "", "skill-screening mode=profile_skill found no supported term in the question -> abstain"), true
}

func matchingSkillTerms(question string, terms []string) []string {
	var out []string
	seen := map[string]bool{}
	for _, term := range terms {
		if !skillTermMatchesQuestion(question, term) {
			continue
		}
		key := normalizeLabel(term)
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, term)
	}
	return out
}

// symbolBearingTokens are lowercase tokens that keep + and #, the only
// characters telling C, C++ and C# apart.
func symbolBearingTokens(text string) []string {
	var out []string
	for _, t := range symbolTokenSplitRE.Split(strings.ToLower(text), -1) {
		if t != "" {
			out = append(out, t)
		}
	}
	return out
}

// foldedTokens are the word tokens of a text with plurals folded.
func foldedTokens(text string) []string {
	toks := tokenize(normalizeLabel(text))
	for i, t := range toks {
		toks[i] = foldPlural(t)
	}
	return toks
}

// skillTermMatchesQuestion compares a term whose identity is its punctuation
// symbol-exactly, so blacklisting "C#" never answers "experience with C/C++";
// any other term matches as a contiguous run of folded tokens, or by its compact
// form (three or more characters) spelt by one to three question tokens in a
// row ("Node.js" is nodejs), never by letters inside or across other words.
func skillTermMatchesQuestion(question, term string) bool {
	if strings.ContainsAny(term, "+#") {
		needle := symbolBearingTokens(term)
		return len(needle) > 0 && optRunStart(symbolBearingTokens(question), needle) >= 0
	}
	nt := normalizeLabel(term)
	if nt == "" {
		return false
	}
	if containsAllTokens(foldedTokens(question), foldedTokens(nt)) {
		return true
	}
	compact := strings.Join(tokenize(nt), "")
	if len(compact) < 3 {
		return false
	}
	qt := tokenize(normalizeLabel(question))
	for i := range qt {
		for j := i + 1; j <= len(qt) && j <= i+3; j++ {
			if strings.Join(qt[i:j], "") == compact {
				return true
			}
		}
	}
	return false
}

// cleanSkillTerms collapses whitespace, drops blanks and de-duplicates by
// normalized form, keeping the first spelling.
func cleanSkillTerms(values []string) []string {
	var out []string
	seen := map[string]bool{}
	for _, raw := range values {
		term := strings.Join(strings.Fields(raw), " ")
		if term == "" {
			continue
		}
		key := normalizeLabel(term)
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, term)
	}
	return out
}

// skillSynonymGroups add related phrases once any trigger is among the user's
// skills; a group whose triggers the user lacks adds nothing.
var skillSynonymGroups = []struct{ triggers, adds []string }{
	{
		[]string{"JavaScript", "TypeScript", "React", "Next.js", "Vite", "HTML/CSS", "HTML", "CSS", "ElementUI"},
		[]string{"front end", "front-end", "frontend", "front end programming languages", "front-end development",
			"web front-end development", "web development", "responsive design", "adaptive design"},
	},
	{
		[]string{"Mobile App Development", "iOS", "Swift", "iOS SDK", "iOS/Swift"},
		[]string{"mobile app", "mobile app development", "mobile app front-end development", "mobile web"},
	},
	{
		[]string{"API", "REST", "HTTP", "OpenAPI", "FastAPI", "Node.js", "NodeJS", "NestJS", "Spring Boot", "Spring", "SpringMVC", "Spring WebFlux"},
		[]string{"api", "apis", "api framework", "api frameworks", "web frameworks", "NodeJS"},
	},
	{
		[]string{"AWS", "AWS EC2", "EC2"},
		[]string{"AWS", "AWS Services", "AWS services including EC2", "EC2"},
	},
	{
		[]string{"Git", "GitHub", "GitHub Actions"},
		[]string{"source control", "version control", "Git"},
	},
}

// expandSkillTerms returns the cleaned terms plus the phrases of every synonym
// group that contains one of them.
func expandSkillTerms(terms []string) []string {
	expanded := cleanSkillTerms(terms)
	has := func(key string) bool {
		for _, t := range expanded {
			if normalizeLabel(t) == key {
				return true
			}
		}
		return false
	}
	hasAny := func(needles []string) bool {
		for _, t := range expanded {
			for _, n := range needles {
				if skillTermsEquivalent(t, n) {
					return true
				}
			}
		}
		return false
	}
	for _, g := range skillSynonymGroups {
		if !hasAny(g.triggers) {
			continue
		}
		for _, add := range g.adds {
			if key := normalizeLabel(add); key != "" && !has(key) {
				expanded = append(expanded, add)
			}
		}
	}
	return expanded
}

func skillTermsEquivalent(a, b string) bool {
	na, nb := normalizeLabel(a), normalizeLabel(b)
	if na == "" || nb == "" {
		return false
	}
	if na == nb {
		return true
	}
	ca, cb := strings.Join(tokenize(na), ""), strings.Join(tokenize(nb), "")
	if len(ca) >= 3 && ca == cb {
		return true
	}
	ta, tb := tokenize(na), tokenize(nb)
	return containsAllTokens(ta, tb) || containsAllTokens(tb, ta)
}

// OwnSearch is what the user's information says about a question on their own
// experience (F-10): the terms it asks about (AskedAbout), each term some of
// the information names with where, and whether every source was searched.
type OwnSearch struct {
	Terms    []string
	Found    []string
	Complete bool
}

// Absent reports a search of everything that found nothing: the answer is No.
func (o OwnSearch) Absent() bool { return o.Complete && len(o.Terms) > 0 && len(o.Found) == 0 }

// OwnSources is the user's information as the own-experience search reads it:
// the profile's skills, education, employment history (its locations and
// summaries too) and the rest of what the user wrote there (ProfileText), the
// résumé and knowledge lines of Evidence, the sections of the knowledge base
// (Knowledge) and the entries the user wrote; a section the knowledge base
// ranks near the question by meaning (KnowledgeNear) is found too. A knowledge
// base not read whole leaves the search incomplete. Words compare in order, up
// to an inflection (wordRunIn); a term spelt with + or # compares its symbols
// exactly; a term question writes in capitals is also the initials of
// capitalised words in a row, and the reverse ("GCP", "Google Cloud
// Platform"). Build it once per request and search it per question.
type OwnSources struct {
	srcs []ownSource
	f    Facts
}

// NewOwnSources reads the facts and the entries the user wrote.
func NewOwnSources(f Facts, entries []Entry) OwnSources {
	return OwnSources{slices.Concat(ownFactSources(f), ownEntrySources(entries)), f}
}

// Mentions searches the sources for what about asks about in question.
func (o OwnSources) Mentions(question, about string) OwnSearch {
	return o.mentions(question, question, AskedAbout(about))
}

// mentions is Mentions over terms the caller lists, with capitals the text
// whose words in capitals and capitalised runs are compared as initials.
func (o OwnSources) mentions(question, capitals string, terms []string) OwnSearch {
	return ownMentionsIn(o.srcs, question, capitals, terms, o.f)
}

// ownSource is one piece of the user's information as the search reads it.
type ownSource struct {
	where       string
	words       []string
	text        string
	runs        [][]string
	capitalised map[string]bool
}

func newOwnSource(where string, parts ...string) ownSource {
	text := strings.Join(parts, "\n")
	return ownSource{where: where, words: tokenize(normalizeLabel(text)), text: text, runs: capitalRuns(text), capitalised: capitalWords(text)}
}

// ownFactSources are the sources the facts hold, in the order the search
// names them.
func ownFactSources(f Facts) []ownSource {
	srcs := []ownSource{newOwnSource("profile", f.ProfileText...)}
	for _, sk := range f.Skills {
		srcs = append(srcs, newOwnSource("profile skills", sk.Name))
	}
	for _, e := range f.Education {
		srcs = append(srcs, newOwnSource("education", e.School, e.Degree, e.Field))
	}
	for _, e := range f.Employment {
		srcs = append(srcs, newOwnSource("employment history", e.Employer, e.Title, e.Location, e.Summary))
	}
	for _, l := range f.Evidence {
		srcs = append(srcs, newOwnSource("résumé", l))
	}
	for _, k := range f.Knowledge {
		srcs = append(srcs, newOwnSource("kb:"+k.Ref, k.Text))
	}
	return srcs
}

// ownEntrySources are the sources the entries the user wrote hold.
func ownEntrySources(entries []Entry) []ownSource {
	srcs := []ownSource{}
	for _, e := range entries {
		// A consent says what the user allows, never what they did (Akuity: an
		// opt-in to marketing email is no marketing role).
		if e.UserWritten() && !e.Placeholder && e.EffectiveState() == StateActive && e.Category != CategoryConsent {
			if IsBlanketGate(e.ID) {
				srcs = append(srcs, newOwnSource("bank:"+e.ID, e.SkillTerms...))
			} else {
				srcs = append(srcs, newOwnSource("bank:"+e.ID, slices.Concat([]string{e.Description, e.Answer, e.FallbackText, e.Language}, e.Examples, e.Preferred, e.Selected, e.SkillTerms)...))
			}
		}
	}
	return srcs
}

// ownMentions is the search over the request's facts and, when entries is
// set, this question's entries. The sources are built once for the request
// and once for the question, however many terms and options are searched.
func (s *Step) ownMentions(question, capitals string, terms []string, entries bool) OwnSearch {
	if s.Req.ownFacts == nil {
		s.Req.ownFacts = ownFactSources(s.Req.Facts)
	}
	srcs := s.Req.ownFacts
	if entries {
		if s.ownEntries == nil {
			s.ownEntries = ownEntrySources(s.Entries)
		}
		srcs = slices.Concat(srcs, s.ownEntries)
	}
	return ownMentionsIn(srcs, question, capitals, terms, s.Req.Facts)
}

// ownMentionsIn searches sources already built.
func ownMentionsIn(srcs []ownSource, question, capitals string, terms []string, f Facts) OwnSearch {
	o := OwnSearch{Terms: terms, Complete: f.KnowledgeRead}
	asked := capitalWords(capitals)
	for _, t := range o.Terms {
		where := ""
		symbols, words := strings.ContainsAny(t, "+#"), tokenize(normalizeLabel(t))
		for i := 0; where == "" && i < len(srcs); i++ {
			if symbols && containsAllTokens(symbolBearingTokens(srcs[i].text), symbolBearingTokens(t)) ||
				!symbols && (wordRunIn(srcs[i].words, words) || asked[t] && spells(srcs[i].runs, t)) {
				where = srcs[i].where
			}
		}
		if where != "" {
			o.Found = append(o.Found, t+" ("+where+")")
		}
	}
	runs := capitalRuns(capitals)
	for _, s := range srcs {
		for w := range s.capitalised {
			if spells(runs, w) {
				o.Found = append(o.Found, w+" ("+s.where+")")
			}
		}
	}
	for _, ref := range f.KnowledgeNear[question] {
		o.Found = append(o.Found, "related notes (kb:"+ref+")")
	}
	return o
}

var (
	// capitalWordRE is a word in capitals of 2 to 5 letters, with a plural s.
	capitalWordRE = lazyregexp.New(`\b(\p{Lu}{2,5})s?\b`)
	// capitalRunRE is two or more capitalised words in a row, which a
	// connector may join ("National Society of Black Engineers").
	capitalRunRE = lazyregexp.New(`\p{Lu}[\p{L}\p{N}]*(?:[ \t]+(?:(?:of|and|for|the|&)[ \t]+)*\p{Lu}[\p{L}\p{N}]*)+`)
)

// capitalWords are the words text writes in capitals, lowercase.
func capitalWords(text string) map[string]bool {
	out := map[string]bool{}
	for _, m := range capitalWordRE.FindAllStringSubmatch(text, -1) {
		out[strings.ToLower(m[1])] = true
	}
	return out
}

// capitalRuns are the runs of capitalised words in a row in text, without
// their connectors, so NSBE is the initials of the National Society of Black
// Engineers. A word that is an acronym itself ("B2B", "SaaS", "AWS") is no
// initial and ends the run, so "B2B SaaS" spells no "BS".
func capitalRuns(text string) [][]string {
	var out [][]string
	for _, m := range capitalRunRE.FindAllString(text, -1) {
		var run []string
		for _, w := range strings.Fields(m) {
			r, _ := utf8.DecodeRuneInString(w)
			switch {
			case acronymWord(w):
				out, run = append(out, run), nil
			case unicode.IsUpper(r):
				run = append(run, w)
			}
		}
		out = append(out, run)
	}
	return out
}

// acronymWord reports a word that is an acronym rather than a capitalised
// word: one with a digit, or one ending in a capital.
func acronymWord(w string) bool {
	last, _ := utf8.DecodeLastRuneInString(w)
	return strings.IndexFunc(w, unicode.IsDigit) >= 0 || utf8.RuneCountInString(w) > 1 && unicode.IsUpper(last)
}

// spells reports some capitalised words in a row of runs whose initials are
// the letters of acronym (lowercase).
func spells(runs [][]string, acronym string) bool {
	for _, run := range runs {
		for i := 0; i+len(acronym) <= len(run); i++ {
			var b strings.Builder
			for _, w := range run[i : i+len(acronym)] {
				r, _ := utf8.DecodeRuneInString(w)
				b.WriteRune(unicode.ToLower(r))
			}
			if b.String() == acronym {
				return true
			}
		}
	}
	return false
}

// wordRunIn reports the words of needle in order among those of hay, each up
// to an inflection (sameWord) or, as a compound, written as two words up to an
// ending only ("cybersecurity", "cyber security"), so "MS, PhD" never spells
// MSP.
func wordRunIn(hay, needle []string) bool {
	for i := 0; len(needle) > 0 && i < len(hay); i++ {
		if runAt(hay[i:], needle) {
			return true
		}
	}
	return false
}

func runAt(hay, needle []string) bool {
	switch {
	case len(needle) == 0:
		return true
	case len(hay) == 0:
		return false
	case sameWord(hay[0], needle[0]) && runAt(hay[1:], needle[1:]):
		return true
	}
	return len(hay) > 1 && sameStem(hay[0]+hay[1], needle[0]) && runAt(hay[2:], needle[1:])
}

// sameWord compares a word of the user's information with one a question asks
// about: one word up to the mention stemmer ("libraries" and "library", "hire"
// and "hiring", "remote" and "remotely"), or the asked word with up to three
// letters more ("react" in "reactjs"). An asked word is never found in a
// shorter one: "servicenow" is not "service", nor "go" in "goals".
func sameWord(hay, asked string) bool {
	return len(asked) >= 3 && strings.HasPrefix(hay, asked) && len(hay)-len(asked) <= 3 || sameStem(hay, asked)
}

// ingNouns are nouns in -ing that name another thing than their stem: market
// data is no marketing.
var ingNouns = setOf("marketing", "accounting")

// sameStem compares two words up to the mention stemmer.
func sameStem(a, b string) bool { return mentionStem(a) == mentionStem(b) }

// mentionStem is the one stemmer both sides of a mention compare by: the
// matcher's, then an adverb's ly ("remotely") on a word of six letters or
// more, then a final e, then one derivational ending (mentionDerivations) and
// a doubled final letter ("modelled"), so a résumé's "analyzed", "optimized"
// or "built financial models" is a mention of analysis, optimization or
// financial modelling. Both sides take the same steps.
func mentionStem(w string) string {
	if ingNouns[w] {
		return w
	}
	w = stem(w)
	if len(w) >= 6 && strings.HasSuffix(w, "ly") {
		w = w[:len(w)-2]
	}
	w = strings.TrimSuffix(w, "e")
	for _, d := range mentionDerivations {
		if base, ok := strings.CutSuffix(w, d[0]); ok {
			if r := strings.TrimSuffix(base+d[1], "e"); len(r) >= 4 {
				w = r
			}
			break
		}
	}
	if n := len(w); n >= 5 && w[n-1] == w[n-2] && !strings.ContainsRune("aeiou", rune(w[n-1])) {
		w = w[:n-1]
	}
	return w
}

// mentionDerivations are the endings a noun or adjective adds to its verb or
// noun, each with the part it keeps, longest first: "optimization" and
// "optimized", "automation" and "automated", "management" and "managed",
// "biological" and "biology", "statistical" and "statistics", "analysis" and
// "analyzed".
var mentionDerivations = [][2]string{
	{"ization", "iz"}, {"isation", "iz"}, {"ological", "olog"}, {"ologist", "olog"}, {"ologic", "olog"}, {"ology", "olog"},
	{"ation", "at"}, {"atory", "at"}, {"ical", "ic"}, {"ment", ""}, {"ysi", "y"}, {"ys", "y"}, {"yz", "y"}, {"is", "iz"},
}

// OwnQuestion reports a question F-10 answers from the user's information: a
// yes/no question about the user's own experience (gates.OwnHistoryQuestion)
// that an affirmative default may answer.
func OwnQuestion(text string) bool {
	return gates.OwnHistoryQuestion(text) && !isAttestationDefaultUnsafe(text)
}

// ownAbsence is the skill gate's search for F-10: only an OwnQuestion is
// searched, for what about names; any other gives an incomplete search.
func ownAbsence(s *Step, about string) OwnSearch {
	if !OwnQuestion(s.Q.Text) {
		return OwnSearch{}
	}
	return s.ownMentions(s.Q.Text, s.Q.Text, AskedAbout(about), true)
}
