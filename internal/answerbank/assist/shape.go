package assist

import (
	"regexp"
	"strings"

	"applysmith/internal/answerbank/match"
	"applysmith/internal/lazyregexp"
)

var (
	interestCueRE    = lazyregexp.New(`(?i)\b(?:why|interest\w*|excit\w*|attract\w*|appeal\w*|motivat\w*|passion\w*|resonat\w*|align\w*|contribut\w*|fit|draws?|drawn)\b`)
	strongCompanyREs = []*lazyregexp.Regexp{
		lazyregexp.New(`(?i)\b(?:why|what)\b[^?]{0,80}\b(?:interest\w*|excit\w*|attract\w*|appeal\w*|motivat\w*|want\w*|apply\w*|join\w*|work\w*|draws?|drawn|drew)\b[^?]{0,80}\b(?:this\s+(?:role|position|job|opportunity|company|team|organi[sz]ation)|our\s+(?:company|team|mission)|us|here)\b`),
		lazyregexp.New(`(?i)\b(?:what|how|tell|describe|share)\b[^?]{0,60}\b(?:know|learn|understand|heard|contribute|add|bring|impact|fit|align|help|support)\b[^?]{0,60}\b(?:this\s+(?:company|role|team|organi[sz]ation)|our\s+(?:team|company)|us)\b`),
	}
	bareWhyRE      = lazyregexp.New(`(?i)^\s*why\b[^?]{0,60}\?*[\s*]*$`)
	bareWhyAuxRE   = lazyregexp.New(`(?i)\b(?:did|do|does|are|is|was|were|would|should|will|can|you|your|we|they|not|the)\b`)
	weakPossessive = lazyregexp.New(`(?i)\b(?:our|the|this)\s+(?:\w+\s+)?(?:company|team|mission|product|platform|values|culture)\b`)
	placeholderCo  = setOf("new", "manual", "unknown", "none", "company", "default", "test", "main", "job", "jobs", "embed", "careers", "companies", "apply", "hiring", "work")
	legalSuffixRE  = lazyregexp.New(`(?i)[,.\s]+(?:inc|llc|ltd|corp|corporation|co|plc|gmbh|pbc|sa|ag|nv)\.?$`)

	applicantRE = lazyregexp.New(`(?i)\b(?:you|your|yours|yourself|applicant|candidate|cover\s+letter)\b`)

	locationVerbRE = lazyregexp.New(`(?i)\b(?:interest\w*|excit\w*|attract\w*|appeal\w*|motivat\w*|want\w*|drawn|draws?|drew)\b`)
	locationPrepRE = lazyregexp.New(`(?i)\b(?:in|to|about|for|near)\s+`)
	placePhraseRE  = lazyregexp.New(`(?:\b(?:in|to|about|for|near)\s+)([A-Z][\p{L}.'-]+(?:\s+[A-Z][\p{L}.'-]+){0,2}),\s*([A-Z][\p{L}.'-]+(?:\s+[A-Z][\p{L}.'-]+){0,2})`)
	legalOnlyRE    = lazyregexp.New(`(?i)^(?:inc|llc|ltd|corp|co|plc|gmbh|pbc|sa|ag|nv)\.?$`)

	liveFactWHRE   = lazyregexp.New(`(?i)\b(?:what|which|who|whom|whose|when|where|how\s+(?:much|many|long|old|far))\b`)
	liveFactNowRE  = lazyregexp.New(`(?i)\b(?:today'?s?|current(?:ly)?|latest|most\s+recent|right\s+now|as\s+of\s+today|last\s+(?:night|week)'?s?|this\s+(?:week|month|year)'?s?)\b`)
	livePronounRE  = lazyregexp.New(`(?i)\b(?:you|your|we|our|us|my|me|i|they|them|their|he|him|his|she|her)\b`)
	liveRejectRE   = lazyregexp.New(`(?i)\bwhy\b|^\W*if\s+(?:yes|no|so|not|any|applicable|other)\b`)
	announceCheckR = lazyregexp.New(`(?i)\b(?:anti[- ]?spam|spam\s+(?:check|filter|prevention)|captcha|human\s+verification|verification\s+question|(?:prove|verify|confirm)\s+(?:that\s+)?you(?:'re|\s+are)\s+(?:a\s+)?(?:human|not\s+a\s+(?:robot|bot))|security\s+(?:question|check))\b|\bare\s+you\s+a\s+human(?:\s+being)?\s*[?.!:]*\s*(?:\([^)]*\))?\s*$`)
	impersonalRE   = lazyregexp.New(`(?i)\b(?:what\s+colou?r\s+is|opposite\s+of|spell\s+the\s+word|what\s+sound\s+does|which\s+animal|which\s+of\s+these)\b`)
)

// companyName reduces a company string to the name a question would use.
func companyName(c string) string {
	c = strings.TrimSpace(c)
	if placeholderCo[strings.ToLower(c)] {
		return ""
	}
	return legalSuffixRE.ReplaceAllString(c, "")
}

// questionTargetsCompany recognizes a question about the employer, recall first:
// a missed company question yields the generic self-pitch, a false positive only
// asks an essay to name the product.
func questionTargetsCompany(q, company string) bool {
	for _, re := range strongCompanyREs {
		if re.MatchString(q) {
			return true
		}
	}
	if bareWhyRE.MatchString(q) && !bareWhyAuxRE.MatchString(strings.TrimPrefix(strings.TrimSpace(strings.ToLower(q)), "why")) {
		return true
	}
	name := companyName(company)
	if name != "" && len(name) >= 3 {
		named := regexp.MustCompile(`\b` + regexp.QuoteMeta(name) + `\b`)
		if named.MatchString(q) && interestCueRE.MatchString(q) {
			return true
		}
		prep := regexp.MustCompile(`(?i)\b(?:at|for|with|join|to|in)\s+` + regexp.QuoteMeta(name) + `\b`)
		if prep.MatchString(q) && interestCueRE.MatchString(q) {
			return true
		}
	}
	return weakPossessive.MatchString(q) && interestCueRE.MatchString(q)
}

var whyWantRE = lazyregexp.New(`(?i)\bwhy\b[^?]{0,40}\b(?:want|interested|excited|apply|join|work|drawn)\b`)

// isCompanyMotivation is the narrow shape that gets the 90-word budget: "why do
// you want to work at ...".
func isCompanyMotivation(q string) bool {
	return whyWantRE.MatchString(q) ||
		bareWhyRE.MatchString(q) && !bareWhyAuxRE.MatchString(strings.TrimPrefix(strings.TrimSpace(strings.ToLower(q)), "why"))
}

// locationQuestion extracts the place names of a location-motivation question.
func locationQuestion(q string) (places []string, ok bool) {
	if !locationVerbRE.MatchString(q) || !locationPrepRE.MatchString(q) {
		return nil, false
	}
	for _, m := range placePhraseRE.FindAllStringSubmatch(q, -1) {
		if legalOnlyRE.MatchString(strings.TrimSpace(m[2])) {
			continue
		}
		places = append(places, m[1]+", "+m[2], m[1], m[2])
	}
	return places, len(places) > 0
}

// excludedPlaces reports whether the question names places and all of those the
// profile lists at all are excluded. An excluded place is excluded even when it
// is also ranked; a question that also names one acceptable place is not refused.
// Places compare as places (match.ListNamesPlace): a code the list leaves open
// excludes both its readings and makes neither acceptable.
func excludedPlaces(places []string, rel *match.RelocationPolicy) bool {
	if rel == nil || len(places) == 0 {
		return false
	}
	excluded, acceptable := false, false
	for _, p := range places {
		if named, _ := match.ListNamesPlace(rel.Excluded, p); named {
			excluded = true
		} else if named, open := match.ListNamesPlace(rel.Places, p); named && !open {
			acceptable = true
		}
	}
	return excluded && !acceptable
}

func addressesApplicant(q string) bool { return applicantRE.MatchString(q) }

var (
	countWordRE  = lazyregexp.New(`(?i)\b(two|three|four|five|2|3|4|5)\s+(?:[a-z-]+\s+){0,2}?(examples|reasons|projects|accomplishments|achievements|strengths|weaknesses|skills|ways|items|stories|instances|bullet points|paragraphs)\b`)
	countNumbers = map[string]int{"two": 2, "three": 3, "four": 4, "five": 5, "2": 2, "3": 3, "4": 4, "5": 5}
	bulletLineRE = lazyregexp.New(`(?m)^\s*(?:[-*\x{2022}]|\d+[.)])\s+\S`)
	ordinalRE    = lazyregexp.New(`(?i)\b(first|second|third|fourth|fifth)\b`)
	// A count that only caps the answer: "up to three examples", "2
	// paragraphs or less".
	countCapBeforeRE = lazyregexp.New(`(?i)\b(?:up\s+to|at\s+most|no\s+more\s+than|not\s+more\s+than|max(?:imum)?(?:\s+of)?|fewer\s+than|less\s+than|under)\s+$`)
	countCapAfterRE  = lazyregexp.New(`(?i)^\s*(?:,\s*)?(?:or\s+(?:less|fewer)|at\s+most|max(?:imum)?)\b`)
)

// askedItemCount reads "three examples" and the like; years of experience are
// not a count, and neither is a cap. A number of sentences is a length
// (explicitLength), not items: Daymark's "In 3 sentences or less, what makes
// you a good fit ...?" was rewritten into three paragraphs.
func askedItemCount(q string) (int, bool) {
	m := countWordRE.FindStringSubmatchIndex(q)
	if m == nil || countCapBeforeRE.MatchString(q[:m[0]]) || countCapAfterRE.MatchString(q[m[1]:]) {
		return 0, false
	}
	return countNumbers[strings.ToLower(q[m[2]:m[3]])], true
}

var blankLineRE = lazyregexp.New(`\n\s*\n`)

// deliveredItems counts the items an answer delivers: paragraph blocks beat
// bullet or numbered lines beat distinct ordinals; a flowing answer counts one.
func deliveredItems(a string) int {
	if paras := blankLineRE.Split(strings.TrimSpace(a), -1); len(paras) > 1 {
		return len(paras)
	}
	if n := len(bulletLineRE.FindAllString(a, -1)); n > 1 {
		return n
	}
	seen := map[string]bool{}
	for _, m := range ordinalRE.FindAllString(a, -1) {
		seen[strings.ToLower(m)] = true
	}
	return max(1, len(seen))
}

var (
	proseVerbRE = lazyregexp.New(`(?i)\b(?:describe|explain|elaborate|overview|walk\s+(?:me\s+|us\s+)?through|tell\s+(?:me|us)\s+about|detail)\b`)
)

// looksLikeProseAsk reports a question that wants prose: a question mark, a
// describe-style verb or at least six words.
func looksLikeProseAsk(q string) bool {
	return strings.Contains(q, "?") || proseVerbRE.MatchString(q) || wordCount(q) >= 6
}

// isLiveFactQuestion reports an impersonal lookup about the world: no personal
// pronoun (a "their" points at a person the form asks about, such as a
// referrer), at most twelve words, not a motivation or conditional question.
func isLiveFactQuestion(q string) bool {
	if livePronounRE.MatchString(q) || liveRejectRE.MatchString(q) || wordCount(q) > 12 {
		return false
	}
	if !liveFactWHRE.MatchString(q) && !liveFactNowRE.MatchString(q) {
		return false
	}
	return liveFactWHRE.MatchString(q) && (liveFactNowRE.MatchString(q) || strings.Contains(q, "?"))
}

// isVerificationAnnouncement reports a question that announces itself as an
// anti-spam check, or an impersonal knowledge shape with no personal pronoun.
func isVerificationAnnouncement(q string) bool {
	if announceCheckR.MatchString(q) {
		return true
	}
	return impersonalRE.MatchString(q) && !livePronounRE.MatchString(q)
}

var durationRE = lazyregexp.New(`(?i)\b(?:\d+|one|two|three|four|five|six|seven|eight|nine|ten|eleven|twelve|a|an)\s+(?:months?|years?|weeks?)\b`)

// archetypeRules classify a behavioral question into the story axis its answer
// must be about; the first match wins. "develop" counts only with a person
// object and a bare "customer-facing" is not a customer story.
var archetypeRules = []struct {
	tag string
	re  *lazyregexp.Regexp
}{
	{"mentoring", lazyregexp.New(`(?i)\b(?:mentor\w*|coach\w*|develop(?:ed|ing)?\s+(?:a\s+|an\s+|your\s+|the\s+)?(?:junior|team\s*member|colleague|engineer|direct\s+report|person|people)\b)`)},
	{"feedback", lazyregexp.New(`(?i)\b(?:received|receiving|got|get)\s+(?:\w+\s+)?(?:critical\s+|constructive\s+|negative\s+|tough\s+)?feedback\b|\bconstructive\s+criticism\b`)},
	{"failure", lazyregexp.New(`(?i)\b(?:fail\w*|mistake|setback|went\s+wrong|made\s+an\s+error)\b`)},
	{"learning", lazyregexp.New(`(?i)\blearn(?:ed)?\s+(?:a\s+)?(?:new|something|quickly)\b|\bnew\s+(?:skill|technology|tool|language)\b|\bsteep\s+learning\b`)},
	{"conflict", lazyregexp.New(`(?i)\b(?:disagree\w*|conflict\w*|difficult\s+(?:coworker|colleague|teammate|stakeholder)|pushback|clash)\b`)},
	{"deadline", lazyregexp.New(`(?i)\b(?:deadline|tight\s+timeline|under\s+pressure|time\s+pressure)\b`)},
	{"prioritization", lazyregexp.New(`(?i)\bprioriti[sz]\w*|\bcompeting\s+(?:priorities|demands)\b|\bmultiple\s+(?:projects|tasks)\b`)},
	{"debugging", lazyregexp.New(`(?i)\b(?:debug\w*|troubleshoot\w*|root\s+cause|production\s+incident|outage)\b`)},
	{"customer", lazyregexp.New(`(?i)\bcustomer\s+(?:obsess\w*|focus\w*|centric|satisfaction|feedback|problem|need)\b|\babove\s+and\s+beyond\s+for\s+(?:a\s+)?customer\b`)},
}

// behavioralArchetype names the story axis of a behavioral question, or "".
func behavioralArchetype(q string) string {
	if !match.IsNarrativeText(q) && !isStarQuestion(q) {
		return ""
	}
	for _, r := range archetypeRules {
		if r.re.MatchString(q) {
			return r.tag
		}
	}
	return ""
}
