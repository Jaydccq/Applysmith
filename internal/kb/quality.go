package kb

import (
	"fmt"
	"strings"
	"unicode/utf8"

	"applysmith/internal/lazyregexp"
)

// Finding severities.
const (
	SevBlock = "block"
	SevWarn  = "warn"
)

// Story finding codes.
const (
	FindDuplicateEvent     = "duplicate_event"
	FindNoTension          = "no_tension"
	FindWeakTension        = "weak_tension"
	FindWeOveruse          = "we_overuse"
	FindUnquantifiedResult = "unquantified_result"
	FindMissingImpact      = "missing_impact"
	FindSituationBloat     = "situation_bloat"
	FindJargonHeavy        = "jargon_heavy"
	FindUnsupportedTrigger = "unsupported_triggers"

	GatePrincipleMinimum = "principle_minimum"
	GateMissingSection   = "missing_section"
	GateDuplicateSection = "duplicate_section"
	GateSourceType       = "invalid_source_type"
	GateDimension        = "invalid_dimension"
	GatePlaceholder      = "placeholder_todo"
	GateProjectReserved  = "project_type_reserved"
)

// StoryFinding is one result of the story checks.
type StoryFinding struct {
	Code     string `json:"code"`
	Severity string `json:"severity"`
	Message  string `json:"message"`
	Other    string `json:"other,omitempty"`
}

var storyParts = []string{"Situation", "Task", "Action", "Result"}

// storyPart returns the text of a labeled section.
func storyPart(sn *Snippet, name string) (string, bool) {
	for _, sec := range sn.Sections {
		if strings.EqualFold(strings.TrimSpace(sec.Heading), name) {
			return sec.Text, true
		}
	}
	return "", false
}

var (
	todoRE      = lazyregexp.New(`\bTODO\b`)
	tensionRE   = lazyregexp.New(`(?i)\b(disagree\w*|conflict\w*|risk\w*|trade-?offs?|deadline\w*|pressure|urgent\w*|tension|pushed? back|objection\w*|concern\w*|blocker\w*|constraint\w*|outage|incident|escalat\w*|dilemma)\b`)
	decisionRE  = lazyregexp.New(`(?i)\b(decided|chose|choose|agreed|resolved|settled|switched|picked|opted|proposed|implemented|because|so that|as a result|which led|resulting|therefore|ended up|instead|cut|reduced|rolled)\b`)
	weRE        = lazyregexp.New(`(?i)\bwe\b`)
	iRE         = lazyregexp.New(`\bI\b`)
	digitRE     = lazyregexp.New(`\p{Nd}`)
	impactRE    = lazyregexp.New(`(?i)\b(customers?|users?|clients?)\b[^.]{0,60}\b(impact\w*|benefit\w*|improv\w*|happier|satisf\w*)\b|\b(impact\w*|benefit\w*|improv\w*)\b[^.]{0,60}\b(customers?|users?|clients?)\b`)
	changeRE    = lazyregexp.New(`(?i)\bfrom\b[^.]{0,40}\bto\b|\d`)
	fileNameRE  = lazyregexp.New(`\b[\w./-]+\.(?:go|ts|tsx|js|jsx|py|java|rb|rs|yml|yaml|json|sql|md|sh|toml|proto)\b`)
	annotateRE  = lazyregexp.New("@[A-Za-z_]\\w+|`[^`\\n]+`")
	sentenceEnd = lazyregexp.New(`[.!?]+(?:\s+|$)|[\x{3002}\x{FF01}\x{FF1F}]+\s*`)
)

func sentenceCount(s string) int {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0
	}
	n := len(sentenceEnd.FindAllStringIndex(s, -1))
	_, size := utf8.DecodeLastRuneInString(s)
	if !sentenceEnd.MatchString(s[len(s)-size:]) {
		n++
	}
	return n
}

// gateFindings are the structural checks every story must pass to publish.
func gateFindings(sn *Snippet, origin string) []StoryFinding {
	var out []StoryFinding
	block := func(code, msg string) { out = append(out, StoryFinding{Code: code, Severity: SevBlock, Message: msg}) }
	if len(distinct(sn.Principles)) < 2 {
		block(GatePrincipleMinimum, "a story needs at least two principles; list a second one it can honestly evidence")
	}
	if !contains(SourceTypes, sn.StorySourceType) {
		block(GateSourceType, fmt.Sprintf("story_source_type must be one of %s", strings.Join(SourceTypes, ", ")))
	}
	if sn.StorySourceType == "project" && origin == "mining" {
		block(GateProjectReserved, "the project source type is reserved for stories derived from a repository; use work or another type")
	}
	for _, d := range sn.Dimensions {
		if !contains(Dimensions, d) {
			block(GateDimension, fmt.Sprintf("dimension %q is not one of %s", d, strings.Join(Dimensions, ", ")))
		}
	}
	for _, p := range storyParts {
		n := 0
		for _, sec := range sn.Sections {
			if strings.EqualFold(strings.TrimSpace(sec.Heading), p) {
				n++
			}
		}
		switch {
		case n == 0:
			block(GateMissingSection, fmt.Sprintf("the story has no %s section", p))
		case n > 1:
			block(GateDuplicateSection, fmt.Sprintf("the story has %d %s sections; keep one", n, p))
		}
	}
	if todoRE.MatchString(sn.body) {
		block(GatePlaceholder, "the story still holds a TODO marker; replace it with the real figure before publishing")
	}
	return out
}

func distinct(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range in {
		if s = strings.TrimSpace(s); s != "" && !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

// qualityFindings are the deterministic draft-time checks. bank holds the
// published stories, which never include the story itself.
func qualityFindings(sn *Snippet, facts []string, bank []*Snippet) []StoryFinding {
	var out []StoryFinding
	add := func(code, sev, msg, other string) {
		out = append(out, StoryFinding{Code: code, Severity: sev, Message: msg, Other: other})
	}
	body := sn.body
	fp := fingerprintOf(facts)
	for _, b := range bank {
		if b.ID == sn.ID {
			continue
		}
		if sameEvent(fp, fingerprintOf(b.Facts())) {
			add(FindDuplicateEvent, SevBlock, fmt.Sprintf("describes the same event as the published story %q", b.ID), b.ID)
			break
		}
	}
	situation, _ := storyPart(sn, "Situation")
	action, _ := storyPart(sn, "Action")
	result, hasResult := storyPart(sn, "Result")
	hasTension := tensionRE.MatchString(body)
	if !hasTension {
		add(FindNoTension, SevWarn, "no disagreement, risk, trade-off, deadline or pressure is present", "")
	} else if !decisionRE.MatchString(action + " " + result) {
		add(FindWeakTension, SevWarn, "a tension is named but no decision or consequence follows", "")
	}
	we, i := len(weRE.FindAllString(body, -1)), len(iRE.FindAllString(body, -1))
	if we >= 3 && (i == 0 || float64(we) > 1.5*float64(i)) {
		add(FindWeOveruse, SevWarn, fmt.Sprintf("the story says \"we\" %d times and \"I\" %d times; say what you did", we, i), "")
	}
	if hasResult && !digitRE.MatchString(result) && !todoRE.MatchString(result) {
		add(FindUnquantifiedResult, SevWarn, "the Result has no number and no TODO marker", "")
	}
	if impactRE.MatchString(body) && !changeRE.MatchString(sentenceWith(body, impactRE)) {
		add(FindMissingImpact, SevWarn, "the story claims customer impact without saying who or what changed", "")
	}
	if actionN := sentenceCount(action); sentenceCount(situation) > actionN || len(sn.factsForHeading("Situation")) > len(sn.factsForHeading("Action")) {
		add(FindSituationBloat, SevWarn, "the Situation is longer than the Action", "")
	}
	if len(fileNameRE.FindAllString(body, -1))+len(annotateRE.FindAllString(body, -1)) >= 2 {
		add(FindJargonHeavy, SevWarn, "two or more file names or code annotations appear in the spoken body", "")
	}
	return out
}

// sentenceWith returns the first sentence of text that matches re.
func sentenceWith(text string, re *lazyregexp.Regexp) string {
	for _, s := range sentenceEnd.Split(text, -1) {
		if re.MatchString(s) {
			return s
		}
	}
	return ""
}

// experienceKinds are the kinds of experience a trigger phrase can claim.
// claim matches the phrase; evidence matches text that supports the claim.
var experienceKinds = []struct {
	name            string
	claim, evidence *lazyregexp.Regexp
}{
	{"mentoring",
		lazyregexp.New(`(?i)\b(mentor\w*|coach\w*|teach\w*|taught|tutor\w*)\b`),
		lazyregexp.New(`(?i)\b(mentor\w*|coach\w*|teach\w*|taught|tutor\w*|onboard\w*|pair(?:ed|ing) with)\b`)},
	{"feedback",
		lazyregexp.New(`(?i)\b(feedback|critique\w*)\b`),
		lazyregexp.New(`(?i)\b(feedback|critique\w*|review comments?|pointed out|told me)\b`)},
	{"conflict",
		lazyregexp.New(`(?i)\b(conflict\w*|disagree\w*|clash\w*|friction|push(?:ed)? back)\b`),
		lazyregexp.New(`(?i)\b(conflict\w*|disagree\w*|clash\w*|friction|push(?:ed)? back|argu\w+|objection\w*|opposed)\b`)},
	{"failure",
		lazyregexp.New(`(?i)\b(fail\w*|mistake\w*|setback\w*|went wrong)\b`),
		lazyregexp.New(`(?i)\b(fail\w*|mistake\w*|setback\w*|went wrong|outage|regress\w*|broke|error)\b`)},
}

// honestTriggers keeps each trigger that claims no kind of experience or whose
// claim the evidence supports, and returns the ones it dropped.
func honestTriggers(triggers []string, evidence string) (kept, dropped []string) {
	for _, t := range triggers {
		supported := true
		for _, k := range experienceKinds {
			if k.claim.MatchString(t) && !k.evidence.MatchString(evidence) {
				supported = false
				break
			}
		}
		if supported {
			kept = append(kept, t)
		} else {
			dropped = append(dropped, t)
		}
	}
	return kept, dropped
}

func hasBlock(fs []StoryFinding) bool {
	for _, f := range fs {
		if f.Severity == SevBlock {
			return true
		}
	}
	return false
}
