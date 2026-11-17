package match

import (
	"slices"
	"strings"

	"applysmith/internal/lazyregexp"
)

// A proficiency ladder ("how good are you at X", rungs from "No experience" up
// to "I could teach others") is an ordered scale, not a set of Yes/No variants.
// The one rung chosen is the highest that names a recorded skill of the user's
// beyond what the question asks about; a rung of scale wording is chosen from
// the question's own subject and capped at the working rung, because presence
// on a resume does not evidence standing among colleagues.

type ladderRank int

const (
	rankNone ladderRank = iota
	rankAware
	rankLapsed
	rankWorking
	// rankAdvanced is a bare "Advanced" below an "Expert" (Dash0: Expert,
	// Advanced, Beginner); a label that names expert work is rankExpert.
	rankAdvanced
	rankExpert
)

// rankCues are tried in this order, a lapsed cue before a working one, so a
// label that states both ends of a contrast lands on what it claims: "used it
// daily in a previous job, not currently" is lapsed.
var rankCues = []struct {
	rank ladderRank
	cues []*lazyregexp.Regexp
}{
	{rankExpert, mustCues(`\bcould\s+teach\s+others?\b`, `\bteach\s+others?\b`, `\bmentor\b`, `\bexpert\b`, `^-?\s*mastery\b`,
		`\bextensive\s+experience\b`, `\barchitect(?:ing|ed)?\b`, `\bdeep\s+expertise\b`,
		// Taekus: "Professional, hands-on experience working on airline loyalty
		// programs" above "Advanced personal use" and "Basic consumer use".
		`^-?\s*professional\s*,?\s+(?:hands[-\s]on|experience|expertise|level)\b`)},
	{rankAdvanced, mustCues(`^-?\s*advanced\b`)},
	{rankLapsed, mustCues(`\bneed\s+a\s+refresher\b`, `\bnot\s+currently\b`, `\bpick\s+it\s+up\s+again\b`, `\bin\s+the\s+past\b`,
		`\bpreviously\s+used\b`, `\brusty\b`)},
	{rankWorking, mustCues(`\bday\s+to\s+day\b`, `\bday-to-day\b`, `\bdaily\b`, `\bin\s+my\s+current\s+job\b`, `\bam\s+using\s+it\b`,
		`^-?\s*intermediate\b`, `\bbuilt\s+and\s+deployed\b`, `\binto\s+production\b`, `\bproduction\s+environments?\b`,
		`\bproficient\b`, `\bprofessional(?:ly)?\s+use\b`)},
	{rankAware, mustCues(`^-?\s*basic\b`, `\bbeginner\b`, `\bnovice\b`, `\bknowledge\s+from\s+class(?:es)?\b`,
		`\bfrom\s+class(?:es)?\s+or\s+college\b`, `\bcoursework\b`, `\bnever\s+used\s+on\s+the\s+job\b`, `\bwilling\s+to\s+learn\b`,
		`\bsimple\s+scripts?\b`, `\bno[-\s]?code\b`, `\bfamiliar\b`)},
	{rankNone, mustCues(`^-?\s*no\s+experience\b`, `\bhave\s+not\s+(?:used|integrated|worked)\b`, `\bnever\s+used\b`, `\bnone\b`,
		`^-?\s*no$`, `^-?\s*not\s+familiar\b`)},
}

// explicitDenial marks a label that denies experience. Whatever hedge
// follows ("but willing to learn") must not lift it above the bottom rung.
var explicitDenial = mustCues(`^-?\s*no\s+experience\b`, `^-?\s*no$`, `^-?\s*none\b`, `\bnot\s+familiar\b`,
	`\bhave\s+not\s+(?:used|integrated|worked)\b`, `\bnever\s+used\s+(?:it|this)\b`)

func mustCues(patterns ...string) []*lazyregexp.Regexp {
	out := make([]*lazyregexp.Regexp, len(patterns))
	for i, p := range patterns {
		out[i] = lazyregexp.New(p)
	}
	return out
}

func anyCue(cues []*lazyregexp.Regexp, text string) bool {
	for _, re := range cues {
		if re.MatchString(text) {
			return true
		}
	}
	return false
}

// ladderRankOf is the rank a label claims.
func ladderRankOf(label string) (ladderRank, bool) {
	norm := normalizeLabel(label)
	if anyCue(explicitDenial, norm) {
		return rankNone, true
	}
	for _, r := range rankCues {
		if anyCue(r.cues, norm) {
			return r.rank, true
		}
	}
	return 0, false
}

// readProficiencyLadder reads the options as a ladder: at least three options,
// every one ranked, at least three distinct ranks, and ranks in page order that
// never decrease, or never increase (Dash0 lists Expert, Advanced, Beginner).
// Real ladders are rendered in scale order, which keeps a checklist or a
// self-identification set that happens to hold a cue word from being read as a
// scale. The result is index-aligned with opts.
func readProficiencyLadder(opts []Option) ([]ladderRank, bool) {
	if len(opts) < 3 {
		return nil, false
	}
	ranks := make([]ladderRank, len(opts))
	distinct := map[ladderRank]bool{}
	up, down := true, true
	for i, o := range opts {
		r, ok := ladderRankOf(o.Label)
		if !ok {
			return nil, false
		}
		ranks[i] = r
		distinct[r] = true
		if i > 0 {
			up = up && r >= ranks[i-1]
			down = down && r <= ranks[i-1]
		}
	}
	return ranks, (up || down) && len(distinct) >= 3
}

// leastLadderOption is a ladder's one lowest rung.
func leastLadderOption(opts []Option) (Option, bool) {
	ranks, ok := readProficiencyLadder(opts)
	if !ok {
		return Option{}, false
	}
	low := slices.Min(ranks)
	i := slices.Index(ranks, low)
	if slices.Contains(ranks[i+1:], low) {
		return Option{}, false
	}
	return opts[i], true
}

// scaleStopWords are scale phrasing and generic technology words: none names
// what a question asks about or a skill a rung claims.
var scaleStopWords = setOf(
	"no", "experience", "basic", "intermediate", "advanced", "expert", "none",
	"i", "have", "has", "not", "but", "willing", "to", "learn", "the", "a", "an",
	"and", "or", "of", "in", "on", "at", "for", "with", "using", "used", "use",
	"am", "is", "are", "was", "were", "be", "been", "my", "me", "it", "its",
	"day", "daily", "job", "jobs", "current", "currently", "could", "would",
	"teach", "others", "other", "simple", "scripts", "script", "built", "build",
	"building", "deployed", "deploy", "functional", "features", "feature",
	"production", "environments", "environment", "complex", "systems", "system",
	"extensive", "architecting", "architect", "into", "from", "class", "classes",
	"college", "never", "refresher", "need", "needs", "pick", "up", "again",
	"easily", "successful", "success", "knowledge", "level", "your", "you",
	"do", "does", "what", "which", "tools", "tool", "applications", "application",
	"automations", "automation", "automated", "workflows", "workflow", "apis",
	"api", "calls", "call", "eg", "e", "g", "etc", "years", "year", "work",
	"working", "please", "select", "all", "that", "apply", "pipelines", "pipeline",
	"data", "generative", "ai", "rate", "rating", "describe", "describes", "best",
	"ability", "programming", "language", "languages",
)

// ladderSubjectTerms are the words naming what a ladder's question asks about:
// what AskedAbout reads in its asked sentence, less scale wording. The words
// that frame the ask ("How proficient are you in") name nothing.
func ladderSubjectTerms(question string) []string {
	var out []string
	for _, t := range AskedAbout(askedSentence(question)) {
		if !scaleStopWords[t] {
			out = append(out, t)
		}
	}
	return out
}

var evidenceSplitRE = lazyregexp.New(`[^\p{L}\p{N}+#.]+`)

// evidenceHas reports a term the evidence holds as a whole word, up to a
// plural: a resume lists one and two letter skills ("R", "Go"), and Java is no
// JavaScript. A dotted name is also its parts ("Node.js" is node and js).
func evidenceHas(evidence []string, term string) bool {
	t := foldPlural(strings.ToLower(term))
	for _, e := range evidence {
		for _, tok := range evidenceSplitRE.Split(strings.ToLower(e), -1) {
			tok = strings.Trim(tok, ".")
			if foldPlural(tok) == t || slices.Contains(strings.Split(tok, "."), t) {
				return true
			}
		}
	}
	return false
}

// ladderSelection is the one rung picked; Why says what supports it.
type ladderSelection struct {
	Index int
	Why   string
}

// ladderWords are the whole words of a text, as evidenceHas compares them.
func ladderWords(text string) []string {
	var out []string
	for _, t := range evidenceSplitRE.Split(normalizeLabel(text), -1) {
		if t = strings.Trim(t, "."); t != "" {
			out = append(out, foldPlural(t))
		}
	}
	return out
}

// rungNamesSkill is a recorded skill the rung's label names whole, every word
// of its name, and the question does not: an ordinary word of the label ("led",
// "them") claims nothing, and a rung that only repeats the question's subject
// is a scale-only rung. A name of one or two letters is read only where the
// label writes it as a name (shortSkillWritten), so the verb "go" is not Go.
func rungNamesSkill(label string, skills []SkillFact, subject map[string]bool) (string, bool) {
	words := setOf(ladderWords(label)...)
	for _, sk := range skills {
		name := ladderWords(sk.Name)
		if len(name) == 0 || !hasEveryToken(words, name) || !slices.ContainsFunc(name, func(t string) bool { return !subject[t] && !scaleStopWords[t] }) {
			continue
		}
		if shortSkillWritten(label, sk.Name) {
			return sk.Name, true
		}
	}
	return "", false
}

// selectLadderRung picks the index of the single highest rung the user's
// information supports: first a rung that names a recorded skill
// (rungNamesSkill), highest rank first; then, for rungs of scale wording, the
// question's own subject held in the evidence, capped at the working rung. It
// returns false when nothing is supported; naming no rung is the honest result
// for a technology the evidence never mentions, not a lowest-rung guess.
func selectLadderRung(ranks []ladderRank, opts []Option, questionText string, evidence []string, skills []SkillFact) (ladderSelection, bool) {
	idx := make([]int, len(opts))
	for i := range idx {
		idx[i] = i
	}
	slices.SortStableFunc(idx, func(a, b int) int { return int(ranks[b]) - int(ranks[a]) })
	subject := ladderSubjectTerms(questionText)
	asked := setOf(subject...)
	for _, i := range idx {
		if ranks[i] <= rankAware {
			continue
		}
		if name, ok := rungNamesSkill(opts[i].Label, skills, asked); ok {
			return ladderSelection{i, "rung names the recorded skill " + name}, true
		}
	}
	for _, t := range subject {
		if !evidenceHas(evidence, t) {
			continue
		}
		for _, i := range idx {
			if ranks[i] <= rankWorking {
				return ladderSelection{i, "subject " + t + " is held in the user's evidence (capped at the working rung)"}, true
			}
		}
	}
	return ladderSelection{}, false
}

// userAnswerOwnsQuestion reports that the user already has an answer for this
// very question: a curated example names it, or the classified entry is a
// pick-option entry with preferred values.
func userAnswerOwnsQuestion(s *Step) bool {
	cx := newClassifier(s.Index, s.Q, s.scorer())
	cl, hit := cx.curated()
	if hit {
		return true
	}
	cl = cx.general()
	e := cl.entry
	return e != nil && e.Kind == KindPickOption && entryAnyText(e.Preferred) && !e.NeedsReview
}

// proficiencyLadder is the lane for a proficiency ladder. The user's own answer
// for the same question wins; with no evidence for any rung it abstains.
func proficiencyLadder(s *Step) (Draft, bool) {
	opts := realOptions(s.Q.Options)
	ranks, ok := readProficiencyLadder(opts)
	if !ok {
		return Draft{}, false
	}
	if userAnswerOwnsQuestion(s) {
		return Draft{}, false
	}
	evidence := append([]string(nil), s.Req.Facts.Evidence...)
	for _, sk := range s.Req.Facts.Skills {
		evidence = append(evidence, sk.Name)
	}
	sel, ok := selectLadderRung(ranks, opts, s.Q.Text, evidence, s.Req.Facts.Skills)
	if !ok {
		return abstainDraft(ReasonPlaceholderEmpty, "", "proficiency ladder: nothing in the user's evidence supports any rung -> abstain"), true
	}
	label := opts[sel.Index].Label
	d := derivedDraft(label, "proficiency_ladder", startProfileRow, "proficiency ladder: "+sel.Why+" -> "+label)
	d.ChosenLabel, d.HasOption, d.OptionScore = label, true, 1
	return d, true
}
