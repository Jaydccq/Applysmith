package match

import (
	"fmt"
	"net/url"
	"strings"

	"applysmith/internal/lazyregexp"
)

// narrativeGuard keeps stored scalar answers out of prompts that ask for prose.
// It applies to a text control without options; a select, the sponsorship
// details question, an AI-use statement (only the user's own answer reaches
// it) and the follow-up slots are left alone. Inside a repeated
// entity row the abstain is the terminal section-policy instead. A typed
// signature is the name box whatever the declaration after its ask mentions
// (GoFundMe: "By typing my name below, I confirm ... any additional information
// provided in my resume ..."); a prose ask before it still asks for prose.
func narrativeGuard(q Question) (Abstain, string, bool) {
	if q.Kind != KindText || len(q.Options) > 0 {
		return Abstain{}, "", false
	}
	t := q.Text
	if !isNarrativeText(t) && !isEssayQuestion(t) {
		return Abstain{}, "", false
	}
	if isSponsorshipDescription(t) || aiUseStatementRE.MatchString(t) || isParentReferential(t) || typedSignatureAsk(t) {
		return Abstain{}, "", false
	}
	if isRepeatedRow(q.SectionPath) {
		return Abstain{Reason: ReasonSectionPolicy},
			fmt.Sprintf("narrative question inside repeated %s row -> abstain", classifySection(q.SectionPath)), true
	}
	return Abstain{Reason: ReasonNarrativeDeferred}, "narrative-shaped question: no bank entry fills prose", true
}

// typedSignatureAsk reports a label whose typed-name ask follows no prose ask:
// "Please explain why you signed. By typing your name below, ..." asks for a
// story first.
func typedSignatureAsk(t string) bool {
	loc := typedNameAskRE.FindStringIndex(t)
	if loc == nil {
		return false
	}
	head := t[:loc[0]]
	return !isNarrativeText(head) && !isEssayQuestion(head)
}

var (
	sentenceBoundaryRE = lazyregexp.New(`[.!?]+\s+[A-Z(0-9]`)
	bareURLRE          = lazyregexp.New(`^https?://\S+$`)
	locatorRE          = lazyregexp.New(`(?i)\burls?\b|\blinks?\b|\bwebsites?\b|\brepos?\b|\brepositor(?:y|ies)\b|\bgithub\b|\blinked ?in\b|\bgitlab\b|\bbitbucket\b|\bportfolio\b|\bdemo video\b|\blive product\b|\bproject page\b|\btechnical write-?up\b|\bopen[- ]source\b`)
	linkedinNameRE     = lazyregexp.New(`(?i)linked ?in`)
	githubNameRE       = lazyregexp.New(`(?i)git ?hub`)
	openSiteRE         = lazyregexp.New(`(?i)\bwebsite\b|\bweb site\b|\bportfolio\b|\bblog\b|\bpersonal site\b|\bdeployed\b|\bdemo\b`)
)

const (
	longFormMinWords = 30
	longFormMinInter = minFuzzyInter + 1
)

// isLongFormProse reports an answer of 30 or more words with a sentence
// boundary; a long run-on clause such as an address is not prose.
func isLongFormProse(answer string) bool {
	a := strings.TrimSpace(answer)
	return len(strings.Fields(a)) >= longFormMinWords && sentenceBoundaryRE.MatchString(a)
}

// questionAsksForLocator reports a link, repository, demo or portfolio ask
// anywhere in the label.
func questionAsksForLocator(text string) bool { return locatorRE.MatchString(text) }

// urlHostMatches reports whether a bare URL's host is the network's host or a
// subdomain of it; a leading www is ignored.
func urlHostMatches(raw, network string) bool {
	u, err := url.Parse(raw)
	if err != nil {
		return false
	}
	host := strings.TrimPrefix(strings.ToLower(u.Hostname()), "www.")
	return host == network || strings.HasSuffix(host, "."+network)
}

// fillableTextShapes are the labels of a short-answer field; a fuzzy or topic
// classification fills a one-line text control only when the label has one.
var fillableTextShapes = []*lazyregexp.Regexp{
	lazyregexp.New(`(?i)\bwhat(?:'s|\s+is|\s+are|\s+were|\s+was)\s+(?:your|the|a|an)\b`),
	lazyregexp.New(`(?i)\b(?:please|kindly)\s+(?:enter|provide|specify|share|state|indicate|input|fill|complete|list)\b`),
	lazyregexp.New(`(?i)\b(?:enter|provide|specify|state|indicate|input|list)\s+your\b`),
	lazyregexp.New(`(?i)\byour\s+(?:(?:full|first|last|preferred)\s+)?(?:name|email|phone|address|city|state|zip|country|url|link|handle|website|portfolio|github|linkedin|twitter|resume|cv|location)\b`),
	lazyregexp.New(`(?i)\b(?:how many|number of)\s+years?\b`),
	lazyregexp.New(`(?i)\bhow many\b`),
	lazyregexp.New(`(?i)\byears?\s+of\s+(?:experience|professional|relevant|total)\b`),
	lazyregexp.New(`(?i)\b(?:earliest|expected|preferred|target|desired)\s+(?:start date|availability|start|date)\b`),
	lazyregexp.New(`(?i)\bstart date\b`),
	lazyregexp.New(`(?i)\bwhat month and year\b`),
	lazyregexp.New(`(?i)\bwhen\s+(?:can|are|do|will)\s+you\s+(?:start|begin|join|come)\b`),
	lazyregexp.New(`(?i)\b(?:salary|compensation|wage|comp|pay)\s+(?:expectations?|requirements?|range|history)\b`),
	lazyregexp.New(`(?i)\bdesired\s+(?:salary|compensation|comp|pay)\b`),
	lazyregexp.New(`(?i)\bcurrent\s+(?:salary|comp(?:ensation)?|wage|location|city|country|company|employer|residence)\b`),
	lazyregexp.New(`(?i)\bwhere\s+(?:are|do)\s+you\b`),
	lazyregexp.New(`(?i)\b(?:current|currently)\s+(?:live|living|reside|residing|located|based)\b`),
	lazyregexp.New(`(?i)^\s*(?:have|has|are|is|do|does|did|was|were|will|can|could|should|would)\s+you(?:r|'ll|'ve|'re|'d)?\b`),
}

func hasShortAnswerShape(text string) bool {
	for _, re := range fillableTextShapes {
		if re.MatchString(text) {
			return true
		}
	}
	return false
}

// namePartRE asks for one part of the name (ZoomInfo: "What is your Legal
// Last Name?"); wholeNameRE asks for all of it, parts named or not.
var (
	namePartRE  = lazyregexp.New(`(?i)\b(?:first|last|middle|given|family|maiden)\s+name\b|\bsurname\b|\bforename\b`)
	wholeNameRE = lazyregexp.New(`(?i)\bfirst\b\s*(?:name\b\s*)?,?\s*(?:middle(?:\s+(?:name|initial))?\s*,?\s*)?(?:and|&|/)\s+(?:last|family)\s+name\b|\bfull\s+(?:legal\s+)?name\b`)
)

// answerShapeGuard runs the guards on the shape of the answer an entry would
// give. Each is a non-terminal abstain. It reports the abstain, the trace line
// and whether a guard fired.
func answerShapeGuard(cl classification, q Question, ix *classIndex) (Abstain, string, bool) {
	e := cl.entry
	if e == nil {
		return Abstain{}, "", false
	}
	fixed := e.Kind == KindFixed && !e.derived() && !e.BodyEmpty()
	answer := strings.TrimSpace(e.Answer)
	// The AI-use statement lane has already decided the user's own disclosure
	// answers the question (Calendly: "Was this application prepared ... by an
	// AI system ...?"), however few words the two share.
	aiUse := e.ID == IDAIUseDisclosure && aiUseStatementRE.MatchString(q.Text)
	if fixed && q.Kind == KindText && cl.stage != stageCuratedVerbatim && !aiUse && isLongFormProse(answer) {
		inter, score := longFormOverlap(ix, e, q.Text)
		if inter < longFormMinInter || score < minFuzzyScore {
			return Abstain{Reason: ReasonNoEntry},
				fmt.Sprintf("long-form fixed answer: question shares %d stemmed tokens (score %.2f), need %d and %.2f", inter, score, longFormMinInter, minFuzzyScore), true
		}
	}
	if fixed && q.InputType == InputDate && !e.futureOnlyDate() && !isDateShaped(answer) {
		return Abstain{Reason: ReasonInputMismatch}, "date-shape guard: a date control is never answered by a non-date value", true
	}
	if fixed && bareURLRE.MatchString(answer) {
		if q.InputType == InputLongText && !questionAsksForLocator(q.Text) {
			return Abstain{Reason: ReasonInputMismatch}, "url-shape guard: a multi-line control is not answered by a bare URL unless the label asks for a link", true
		}
		li, gh := linkedinNameRE.MatchString(q.Text), githubNameRE.MatchString(q.Text)
		if li != gh && !openSiteRE.MatchString(q.Text) {
			network := "linkedin.com"
			if gh {
				network = "github.com"
			}
			if !urlHostMatches(answer, network) {
				return Abstain{Reason: ReasonNoEntry}, "named-network guard: the question names " + network + " and the entry holds another host", true
			}
		}
	}
	if e.ID == IDApplicantFullName && namePartRE.MatchString(q.Text) && !wholeNameRE.MatchString(q.Text) {
		return Abstain{Reason: ReasonNoEntry}, "name-part guard: a part of the name is never answered with the full name", true
	}
	if (cl.stage == stageFuzzy || cl.stage == stageTopic || cl.stage == stagePinned) && q.Kind == KindText && q.InputType != InputLongText && len(q.Options) == 0 && !hasShortAnswerShape(q.Text) {
		return Abstain{Reason: ReasonNoEntry}, "short-answer shape guard: a fuzzy classification fills a one-line control only for a short-answer label", true
	}
	return Abstain{}, "", false
}

// longFormOverlap is the question's overlap with the entry's own description and
// examples, scored the way the fuzzy stage scores.
func longFormOverlap(ix *classIndex, e *Entry, text string) (inter int, score float64) {
	idx, ok := ix.byID[e.ID]
	if !ok {
		return 0, 0
	}
	scored := ix.scoreEntries(text, nil, func(i int) bool { return i == idx })
	if len(scored) == 0 {
		return 0, 0
	}
	return scored[0].inter, scored[0].score
}
