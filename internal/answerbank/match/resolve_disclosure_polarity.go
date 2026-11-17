package match

import (
	"slices"
	"strings"
	"unicode"

	"applysmith/internal/lazyregexp"
)

// Polarity of a sponsorship question. The recorded facts say whether the user
// needs sponsorship; a plain question ("Do you require sponsorship?") is
// answered with the fact and an explicitly negated one ("Are you able to work
// without sponsorship?") with its complement.
//
// A sponsorship answer is a statement the applicant signs, so the reading is
// closed instead of a search for negations: a question is read only when it is
// ONE sentence whose every word is either inert (a country, an article, "work",
// "employer", ...) or a structural word, and whose structural words, in order,
// spell one of the recognised shapes. Anything else (an unknown predicate such
// as "unnecessary" or "independent", a second clause, an "or" between clauses, a
// comparison, a hypothetical, an instruction about which button to press) is
// not read and is left to the user. An unlisted wording costs a fill, never a
// wrong answer. The work-authorization reader (resolve_disclosure_workauth.go)
// shares this skeleton and vocabulary and differs only in its shapes.

// The structural symbols of a skeleton.
const (
	symDo      = "DO"
	symWill    = "WILL"
	symCan     = "CAN"
	symBe      = "BE"
	symYou     = "YOU"
	symAble    = "ABLE"
	symNeg     = "NEG"
	symNo      = "NO"
	symWithout = "WITHOUT"
	symReq     = "REQ"
	symSponsor = "SP"
	symHave    = "HAVE"
	symHold    = "HOLD"
	symThat    = "THAT"
	symAid     = "AID"
	symReqment = "REQMT"
	symTime    = "T"
	symNeedFor = "NEEDFOR"
	symForYou  = "FORYOU"
	symLead    = "LEAD"
	symRight   = "RIGHT"
)

var (
	// structuralWords maps each word that carries polarity or shape to its
	// symbol. A word in neither this table nor inertWords is unknown and ends
	// the reading. The upper-case keys are the placeholders the phrase
	// collapses below leave in the lower-cased text.
	structuralWords = map[string]string{
		"do": symDo, "does": symDo,
		"will": symWill, "would": symWill,
		"can": symCan, "could": symCan,
		"is": symBe, "are": symBe, "am": symBe,
		"you": symYou, "i": symYou,
		"able": symAble, "eligible": symAble, "entitled": symAble, "allowed": symAble,
		"permitted": symAble, "authorized": symAble, "authorised": symAble,
		"not": symNeg, "never": symNeg,
		"no":      symNo,
		"without": symWithout,
		"require": symReq, "requires": symReq, "requiring": symReq, "required": symReq,
		"need": symReq, "needs": symReq, "needing": symReq, "needed": symReq,
		"sponsorship": symSponsor, "sponsor": symSponsor, "sponsors": symSponsor,
		"sponsoring": symSponsor, "sponsored": symSponsor,
		"visa": symSponsor, "visas": symSponsor, "permit": symSponsor, "h1b": symSponsor,
		"authorization": symAid, "authorisation": symAid, "support": symAid, "assistance": symAid, "help": symAid,
		"have": symHave, "has": symHave,
		"hold":        symHold,
		"that":        symThat,
		"requirement": symReqment, "requirements": symReqment,
		"T": symTime, "NEEDFOR": symNeedFor, "FORYOU": symForYou, "LEAD": symLead, "RIGHT": symRight,
	}
	// inertWords carry no polarity: they can neither negate nor condition the
	// requirement, so they are dropped from the skeleton.
	inertWords = setOf(
		"a", "an", "the", "any", "all", "this", "these", "our", "your", "my", "their", "its",
		"for", "to", "in", "of", "at", "on", "within", "from", "by", "with", "as", "be", "being",
		// Synthesia: "ongoing employer support" is the continuing one.
		"work", "working", "legal", "legally", "lawfully", "obtain", "maintain", "continue", "continuing", "continued", "ongoing",
		// GitLab: "sponsorship for a visa to remain in your current location".
		// Riskified: "authorized to work ... indefinitely", no time limit.
		"remain", "indefinitely",
		// Delinea: "in order to continue employment", a purpose.
		"order",
		// Gremlin: "require employee sponsorship". Aquatic: "the firm's sponsorship".
		"employment", "employed", "employee", "based", "employer", "employers", "company", "firm", "organization", "role", "position", "job",
		"hybrid", "remote", "onsite", "office", "immigration", "status",
		"nonimmigrant", "eg", "etc", "form", "kind", "type", "types", "sort", "right", "us",
		// "an immigration-related employment benefit" is the sponsorship itself.
		"benefit", "benefits",
		// Dust: "the appropriate work authorization for this role".
		"appropriate", "necessary", "proper",
	)

	// timeRE is a time qualifier, collapsed to one symbol before the words are
	// read. "Now or in the future" is a single qualifier, so a negation never
	// has to be split across two tenses.
	timeRE = lazyregexp.New(`\b(?:` +
		// Applied Intuition: "now or anytime in the future", "at" and the space
		// dropped; Mercor: "now and/or in the future", the slash a space by now.
		// BetterUp: "now or at a future date"; Qventus: "in the near future";
		// Fermat: "now or ever in the future".
		`(?:either )?(?:now|currently|presently) (?:or|and(?: or)?) (?:in the (?:(?:near|foreseeable) )?future|in future|later|ever(?: in the future)?|eventually|(?:at )?(?:any|some) ?(?:time|point)(?: in the future)?|at (?:a|any|some) (?:future|later) date)|` +
		`(?:in the (?:(?:near|foreseeable) )?future|in future|later) (?:or|and(?: or)?) now|` +
		// Avride: "current or future visa sponsorship", the qualifier as adjectives.
		`current (?:or|and) future|` +
		`before you (?:could|can|would) (?:begin|start)(?: working| work)?|` +
		`(?:at )?(?:any|some) ?(?:time|point) in the future|at (?:any|some) (?:time|point)|in the (?:(?:near|foreseeable) )?future|in future|` +
		`right now|at this time|at present|going forward|now|currently|presently|today|later|eventually|ever|future)\b`)

	edgePunctRE     = lazyregexp.New(`^[\s*"“”]+|[\s?.!*:"“”]+$`)
	innerSentenceRE = lazyregexp.New(`[.?!;:]`)
	doOrWillRE      = lazyregexp.New(`\bdo you or will you\b`)
	// doNowOrWillRE is the now-or-future qualifier split across "do you now, or
	// will you ever|in the future" (ERCOT's Workday) or "will you now or will
	// you in the future" (Harvey).
	// Peloton: "do you now or could you in the future"; Dandy: "are you now
	// or will you in the future require"; Mindbody: "will you ever in the
	// future"; Kaseya: "do you now or will you at any time in the future".
	doNowOrWillRE = lazyregexp.New(`\b(?:do|will|are) you (?:now|currently) or (?:will|would|could|might|may) you (?:(?:at )?any ?(?:time|point) )?(?:in (?:the )?future|ever(?: in (?:the )?future)?)\b`)
	// sponsorCaseRE is "to sponsor an immigration case": the company is asked to
	// sponsor, which is the sponsorship noun. Julius: "to sponsor or support a
	// visa or other employment authorization"; Kobie: "to sponsor an
	// employment-based immigration case"; Qrypt: "to sponsor, file, or
	// support any immigration paperwork on your behalf" (commas gone);
	// CharterUP: "to sponsor or petition for an employment-based visa".
	sponsorCaseRE = lazyregexp.New(`\bto (?:commence (?:or )?)?sponsor(?: file)?(?: or (?:support|file|petition for))? (?:an? |your |any )?(?:employment based )?(?:immigration|visa|work visa|work permit)` +
		`(?: or (?:an? )?(?:other )?(?:immigration|visa|work visa|work permit|(?:employment|work) authori[sz]ation))?(?: case| petition| application| paperwork)?` +
		`(?: (?:to (?:employ|hire) you|on your behalf|for you))?(?: for example h1b(?: visa)?(?: case| petition)?)?|` +
		// Solace: "require Solace to sponsor you for a work permit"; Lightspark:
		// "require Lightspark to serve as a sponsor for employment".
		`\bto (?:sponsor you|(?:serve|act) as (?:an? |your )?sponsor) for (?:an? )?(?:visa|work visa|work permit|employment visa|employment)\b|` +
		// SmarterDx: "require SmarterDx to file a petition or application for
		// employment-based visa status on your behalf".
		`\bto (?:file|submit) (?:an? )?(?:petition|application)(?: or (?:an? )?(?:petition|application))? for (?:an? )?(?:employment based )?(?:visa|immigration|work visa)(?: status)?(?: on your behalf| for you)?\b`)
	// provideAidRE is the company asked to provide the aid (Plaid: "require
	// Plaid to provide immigration-related support or sponsorship"): the aid
	// noun itself.
	provideAidRE = lazyregexp.New(`\bto (?:provide|offer|give)(?: you)?(?: with)? ((?:an? |any |the )?(?:(?:immigration(?: related)?|visa|employment|work) )*(?:support|sponsorship|assistance))\b`)
	// withoutEmployerSupportRE is the employer's support done without
	// (Eventual: "authorized to work lawfully in the US, without employer
	// support"): the employer's sponsorship.
	withoutEmployerSupportRE = lazyregexp.New(`\bwithout (?:any )?(?:employer|company)(?:'s)? support\b`)
	// rightToWorkAidRE is the aid named for the right it gives (Zscaler:
	// "a work permit, visa, or additional right to work support"): the aid noun.
	rightToWorkAidRE = lazyregexp.New(`\b(?:(?:additional|other|any)\s+)?right to work (?:support|assistance)\b`)
	// resideAndWorkRE is residing beside working (Cribl: "authorized to reside
	// and work in the country where this role is based"): the work.
	resideAndWorkRE = lazyregexp.New(`\b(?:live|reside) and work\b`)
	// orNounRE joins two nouns of one requirement ("a work permit or visa
	// sponsorship") by keeping the later one. Baton's "a Work Visa, Transfer, or
	// Sponsorship" (commas gone) is the same list: a visa's transfer is one.
	// Airwallex's "employment visa status or work authorization": the status a
	// visa gives is the visa. Immuta's "an employment visa or other
	// employer-sponsored immigration benefit": the other benefit is the visa's
	// kind.
	orNounRE = lazyregexp.New(`\b(?:work |employment )?(?:permit|visa(?: transfer| status)?) or (?:an? )?(?:other (?:(?:employer|company) sponsored )?)?`)
	// benefitOrVisaRE is YipitData's "employment benefits or visa status": the
	// visa named after the benefit is the benefit.
	benefitOrVisaRE = lazyregexp.New(`\b(benefits?) or (?:an? )?(?:work |employment |immigration )?(?:visa(?: status)?|immigration status)\b`)
	// visaPermitListRE is Corelight's "a visa, work permit, or other employment
	// authorization" (commas gone): a visa and a permit listed side by side are
	// one noun before the "or".
	visaPermitListRE = lazyregexp.New(`\b((?:work |employment )?(?:visa|permit)) (?:work |employment )?(?:visa|permit) or\b`)
	// unrestrictedWithoutRE is Corelight's "unrestricted authorization to work
	// ..., without requiring ... sponsorship": the "without" clause says what
	// the restriction is, so the adjective adds nothing. It is dropped only
	// when such a clause follows; "unrestricted authorization" alone is
	// another question.
	unrestrictedWithoutRE = lazyregexp.New(`\bunrestricted ((?:work )?authori[sz]ation|right)\b(.*\bwithout\b.*\bsponsor)`)
	// authTypoRE is a misspelt authorization (LanceDB: "employment
	// auhorization").
	authTypoRE = lazyregexp.New(`\bau[th]ori([sz])(ation|ed|e)\b`)
	// andOrRE is "and/or" with its slash gone (Lighthouse: "an employment visa
	// and/or working permit"): the "or" of a list.
	andOrRE = lazyregexp.New(`\band or\b`)
	// ableAndAbleRE is two words of the one authorization joined (Cohere
	// Health: "eligible and legally able to work").
	ableAndAbleRE = lazyregexp.New(`\b(eligible|authori[sz]ed|permitted|able) and (?:(?:legally|lawfully) )?(?:eligible|authori[sz]ed|permitted|able)\b`)
	// sponsorTransferRE is ClickHouse's "sponsorship or transfer of
	// sponsorship": a sponsorship carried over from another employer is
	// sponsorship.
	// sponsorAidWithRE, visaListRE and workOrContinueRE are New Relic's
	// "sponsorship for or assistance with an employment visa, authorization, or
	// permit to work for or continue to work for": the aid beside the
	// sponsorship, three nouns of one requirement, and the work continued.
	sponsorAidWithRE  = lazyregexp.New(`\bsponsorship for or (?:assistance|support|help) with\b`)
	visaListRE        = lazyregexp.New(`\b((?:work |employment )?visa) (?:work |employment )?authori[sz]ation or (?:an? )?(?:work |employment )?permit\b`)
	workOrContinueRE  = lazyregexp.New(`\bwork for or continue to work for\b`)
	sponsorTransferRE = lazyregexp.New(`\bsponsorship or (?:an? )?transfer of (?:(?:your|an?|the) )?(?:(?:visa|work visa|h1b) )?sponsorship\b`)
	// orOtherAidRE is "sponsorship or other employer assistance", "support or
	// sponsorship" or Chime's "immigration support or visa sponsorship": one
	// requirement.
	// Lime's "visa sponsorship, work authorization, or any other assistance" (commas gone) is the same list.
	// Snorkel AI's "sponsorship or other assistance or support" is three nouns
	// of the one requirement, joined pairwise until one is left. Aledade's
	// "require the company's sponsorship or need the company's assistance"
	// repeats the verb before the second noun. OpenGov's "sponsorship or
	// employment authorization" names the authorization the need is for.
	orOtherAidRE = lazyregexp.New(`\b(sponsorship|assistance|support|authori[sz]ation) or (?:(?:need|require)s? )?(?:any )?(?:other )?(?:the )?(?:employer |company )?(?:visa |immigration |work |employment )?(?:assistance|support|sponsorship|authori[sz]ation)\b`)
	// orOtherwiseWorkRE is Snorkel AI's "to be authorized to, or otherwise,
	// work" (commas gone): the work itself.
	orOtherwiseWorkRE = lazyregexp.New(`\bto or otherwise work\b`)
	// sponsorOrOtherwiseRE is Gumloop's "require Gumloop to sponsor or
	// otherwise support your employment authorization": the support beside
	// the sponsorship is one requirement. Keeper Security's "to sponsor, file
	// for or support an immigration-related employment benefit" (commas gone)
	// lists the filing between them.
	sponsorOrOtherwiseRE = lazyregexp.New(`\bsponsor (?:file for )?or (?:otherwise )?(?:support|assist|help)\b`)
	// soThatPurposeRE is the purpose "so that you may begin or continue
	// working" (Keeper Security), "begin or continue" read by then: the work
	// itself.
	soThatPurposeRE = lazyregexp.New(`\bso (?:that )?you (?:may|can|could|might) (continue|work|begin|start)\b`)
	// immigrationRelatedRE is "immigration related support": the inert
	// "immigration" before the aid noun.
	immigrationRelatedRE = lazyregexp.New(`\bimmigration related\b`)
	// beginOrContinueRE is the purpose "to begin or continue employment" (ERCOT:
	// "to initially accept, or to subsequently continue in, permanent
	// employment"), which names no second requirement.
	beginOrContinueRE = lazyregexp.New(`\b(?:initially )?(?:begin|start|commence|accept) (?:or|and) (?:to )?(?:subsequently )?continue(?: in permanent employment\b)?\b|` +
		// Campbell's: "to begin employment" alone.
		`\b(?:begin|start|commence) employment\b`)
	// beginWorkRE is the purpose "to begin working" (IMC): the work itself.
	// "before you could begin working" is a time and stays.
	beginWorkRE = lazyregexp.New(`\bto (?:begin|start|commence) (work(?:ing)?)\b`)
	// affiliatesRE is "(or one of its affiliates)" after the employer's name.
	affiliatesRE = lazyregexp.New(`\bor (?:one of )?(?:its|their) (?:affiliates|subsidiaries)\b`)
	// eligibilityPurposeRE is the purpose "to attain or maintain your
	// employment eligibility" (DoorDash), or "to ensure your continued
	// eligibility to work lawfully for the company" (EnergyHub), or Dandy's
	// "to secure or maintain authorization to work", Aquatic's "to obtain,
	// maintain, or extend your employment authorization" or Belvedere's "to
	// maintain lawful employment", which names no second requirement.
	eligibilityPurposeRE = lazyregexp.New(`\bto (?:obtain|attain|maintain|keep|retain|secure|ensure)(?:(?: or| and)? (?:to )?(?:obtain|attain|maintain|keep|retain|secure|ensure|extend))* (?:your )?(?:continued )?(?:(?:employment|work) (?:eligibility|authori[sz]ation|status)|(?:eligibility|authori[sz]ation) to work(?: lawfully| legally)?(?: for (?:the company|us))?|(?:lawful|legal) employment)\b`)
	// employYouRE is the purpose "to employ you", which names no requirement.
	employYouRE = lazyregexp.New(`\bto (?:employ|hire) you\b`)
	// fullTimeHoursRE is the hours "full-time" (Datadog: "authorised to work
	// full-time in the country where this job is based"; Mosaic: "on a
	// full-time basis"): the job's hours, not a requirement or a tense.
	fullTimeHoursRE = lazyregexp.New(`\b(?:on an? )?full time(?: basis)?\b`)
	// permanentJobRE is a job's tenure (Backblaze: "sponsorship ... for
	// permanent employment"); permanent residence is another fact and stays.
	permanentJobRE = lazyregexp.New(`\bpermanent (employment|position|role|job)\b`)
	// splitTenseRE is "do you (currently) require, or will (might) you need in
	// the future" (commas gone): the one now-or-future qualifier said in two clauses.
	// Torc's "do you require or will you require" says the future in the "will" alone.
	splitTenseRE = lazyregexp.New(`\bdo you (?:(?:now|currently|presently) )?(?:require|need) or (?:will|might|may|would) you (?:require|need)(?: in (?:the )?future)?\b`)
	// repeatedTenseRE is the qualifier split with the noun said in both
	// clauses (Zoox: "do you require sponsorship or will you require
	// sponsorship in the future"); it is one qualifier only when the two nouns
	// are the same (sameNounTense).
	repeatedTenseRE = lazyregexp.New(`\bdo you (?:(?:now|currently|presently) )?(?:require|need) ((?:(?:visa|immigration|employment|work) )?sponsorship) or (?:will|would|could|might|may) you (?:require|need) ((?:(?:visa|immigration|employment|work) )?sponsorship)(?: in (?:the )?future)?\b`)
	needForRE       = lazyregexp.New(`\bthe need (?:of|for)\b`)
	forYouRE        = lazyregexp.New(`\bfor you\b`)
	obtainRE        = lazyregexp.New(`\b(?:obtain|extend|maintain|renew|retain|continue)(?:(?: or| and)? (?:obtain|extend|maintain|renew|retain|continue))+\b`)
	inOrderRE       = lazyregexp.New(`\bin order (?:for you )?to\b`)
	leadRE          = lazyregexp.New(`^is it true that\b`)
	h1bRE           = lazyregexp.New(`\bh-?1[-\s]?b\b`)
	nonImmRE        = lazyregexp.New(`\bnon-immigrant\b`)
	egRE            = lazyregexp.New(`\be\.g\.`)
	etcRE           = lazyregexp.New(`\betc\.`)
	// possessiveRE is a possessive "'s": "the company's sponsorship" reads as
	// "the company sponsorship".
	possessiveRE = lazyregexp.New(`(\pL)'s\b`)
	// pluralMarkRE is a "(s)" plural mark (YipitData: "employment benefit(s)").
	pluralMarkRE = lazyregexp.New(`(\pL)\(s\)`)
	// rightRE is the noun phrase "the (legal|unrestricted) right to work", one
	// symbol like the other fixed phrases; "the ability to work" is the same
	// (Shield AI: "Do you have the ability to work in Australia without the
	// need for sponsorship?").
	rightRE = lazyregexp.New(`\b(?:the )?(?:(?:legal|unrestricted|legal and unrestricted) )?(?:right|ability) to work\b`)
	// demonstrativeRE is "that" or "this" pointing back at the authorization a
	// prior question named (ngrok: "require the support of ngrok to maintain
	// that authorization"): a determiner like "the", not the relative "that",
	// which a verb follows. "Your current authorization" (Sysdig) is the one
	// the applicant holds: the same determiner.
	demonstrativeRE = lazyregexp.New(`\b(?:that|this|your current) ((?:work |employment )?authori[sz]ation)\b`)
)

// sponsorShapes is the closed set of recognised question shapes over the
// skeleton. Each holds at most one negation word (NEG, NO or WITHOUT); a plain
// shape holds none.
var sponsorShapes = func() []*lazyregexp.Regexp {
	const (
		ask = `(?:(?:DO|WILL) YOU|YOU (?:DO|WILL))`
		// Ivo: "Are you currently authorized ... without employer sponsorship?"
		// Corelight: "Do you have ... authorization to work ..., without
		// requiring ... sponsorship?" is the right to work without it.
		ableAux = `(?:BE YOU(?: T)? ABLE|CAN YOU(?: ABLE)?|WILL YOU ABLE|DO YOU HAVE(?: RIGHT| AID)?)`
	)
	patterns := []string{
		// "Will you now or in the future require sponsorship?", "Do you not need
		// sponsorship?", "Will you require sponsorship later?"
		// "At any point in the future, will you require the company to sponsor
		// an employment visa?" leads with the qualifier. What the sponsorship
		// obtains may follow: work authorization or the right to work.
		`^(?:T )?` + ask + `(?: NEG(?: T)?| T(?: NEG)?)? REQ(?: T)? (?:SP|AID)(?: T)?(?: ABLE| RIGHT)?$`,
		// "Is it true that you will never require visa sponsorship?"
		`^LEAD YOU (?:DO|WILL)(?: NEG(?: T)?| T(?: NEG)?)? REQ(?: T)? (?:SP|AID)(?: T)?$`,
		// "Do you currently hold a visa that will require sponsorship?"
		`^DO YOU(?: T)? HOLD SP THAT (?:WILL )?REQ SP$`,
		// "Do you have a sponsorship requirement for this role?"
		`^DO YOU HAVE SP REQMT$`,
		// Sysdig: "Does your current authorization require sponsorship in the
		// future?" asks the need through the authorization the applicant holds.
		`^DO AID REQ(?: T)? SP(?: T)?$`,
		// "Is sponsorship needed now or in the future?", "Is no visa sponsorship needed?"
		`^BE (?:NO )?SP REQ(?: FORYOU)?(?: T)?$`,
		// "Is it true that no sponsorship is needed for you?"
		`^LEAD NO SP BE REQ(?: FORYOU)?$`,
		// "Are you eligible to work in the US with no sponsorship required?"
		`^BE YOU ABLE NO SP REQ$`,
		// "Are you able to work in the US without sponsorship?", "Can you work
		// without the need for visa sponsorship?", project44's "... without the
		// need for current or future visa sponsorship?"
		`^` + ableAux + `(?: T)? WITHOUT(?: NEEDFOR| REQ)?(?: T)? SP(?: T)?$`,
		// A bare label ("Visa sponsorship", "Sponsorship requirements").
		`^SP(?: REQMT)?$`,
	}
	out := make([]*lazyregexp.Regexp, len(patterns))
	for i, p := range patterns {
		out[i] = lazyregexp.New(p)
	}
	return out
}()

// sponsorSkeleton reduces the text to its structural symbols, or reports false
// when the text is more than one sentence or holds a word that is neither
// structural nor inert. company is the posting's company: its words are inert
// ("to work for Example Corp"), except those that are structural words.
// sponsorFromRE and employmentWithRE read the employer a sponsorship question
// names as the sponsor and as the party to the employment.
var (
	sponsorFromRE    = lazyregexp.New(`\bsponsorship\s+from\s+([A-Z][\w&.'-]*(?:\s+[A-Z][\w&.'-]*){0,3})`)
	employmentWithRE = lazyregexp.New(`\bemployment\s+with\s+([A-Z][\w&.'-]*(?:\s+[A-Z][\w&.'-]*){0,3})`)
)

// namedEmployer is the name a sponsorship question gives both slots, which is
// the employer it asks about whatever the posting calls the company (Twitch:
// "support or sponsorship from Amazon in order to begin or continue employment
// with Amazon"); "" when the slots are not both filled by one name.
func namedEmployer(text string) string {
	a, b := sponsorFromRE.FindStringSubmatch(text), employmentWithRE.FindStringSubmatch(text)
	if a == nil || b == nil || a[1] != b[1] {
		return ""
	}
	return a[1]
}

// companyAsWritten is the company's name as the question writes it: the name
// and up to three capitalized words that follow it (Flock: "require Flock
// Safety to commence ... an immigration case"); "" when the text does not
// write the name or adds nothing to it.
func companyAsWritten(text, company string) string {
	c := strings.TrimSpace(company)
	i := strings.Index(text, c)
	if c == "" || i < 0 || i > 0 && isWordByte(text[i-1]) {
		return ""
	}
	rest := text[i+len(c):]
	if rest == "" || rest[0] != ' ' {
		return ""
	}
	var tail []string
	for _, w := range strings.Fields(rest) {
		if len(tail) == 3 || w[0] < 'A' || w[0] > 'Z' {
			break
		}
		tail = append(tail, w)
	}
	return strings.Join(tail, " ")
}

// companyJoined lists the capitalized words that write a one-word company's
// name with more joined to it, the legal name (Oura: "require Ouraring
// sponsorship ... to work for Ouraring", the board's "ŌURA"), or a part of it,
// when the name is a board slug that joins more to it (AHEAD: "require AHEAD to
// sponsor you" under the board "thinkahead"); a part is four letters or more.
// Words are compared without case or accents; "" when there are none.
func companyJoined(text, company string) string {
	c := foldPlace(company)
	if c == "" || strings.Contains(c, " ") {
		return ""
	}
	var out []string
	for _, w := range strings.FieldsFunc(text, func(r rune) bool { return !unicode.IsLetter(r) }) {
		f := foldPlace(w)
		joined := len(f) > len(c) && strings.HasPrefix(f, c)
		part := len(f) >= 4 && len(f) < len(c) && strings.Contains(c, f)
		if (joined || part) && unicode.IsUpper([]rune(w)[0]) {
			out = append(out, w)
		}
	}
	return strings.Join(out, " ")
}

// sameNounTense collapses a repeatedTenseRE match whose two nouns are the
// same into the one now-or-future qualifier; any other is left as it is.
func sameNounTense(m string) string {
	g := repeatedTenseRE.FindStringSubmatch(m)
	if g == nil || g[1] != g[2] {
		return m
	}
	return "will you now or in the future require " + g[1]
}

// writtenCompany is the posting's company as the question writes it when the
// name is a board slug that joins several capitalized words into one (US
// Mobile's Lever board "USMobile" under "Are you authorized to work lawfully
// in the US for US Mobile Inc?"): the first run of two to four words, apart
// only by spaces, whose letters are the slug's. "" when the name holds a
// space or the text writes no such run. Words are compared without case or
// accents.
func writtenCompany(text, company string) string {
	c := foldPlace(company)
	if c == "" || strings.Contains(c, " ") {
		return ""
	}
	words := companyWordRE.FindAllStringIndex(text, -1)
	for i := range words {
		joined := ""
		for j := i; j < len(words) && j < i+4; j++ {
			w := []rune(text[words[j][0]:words[j][1]])
			apart := j > i && strings.TrimSpace(text[words[j-1][1]:words[j][0]]) != ""
			if apart || !unicode.IsUpper(w[0]) && !unicode.IsDigit(w[0]) {
				break
			}
			joined += foldPlace(string(w))
			if !strings.HasPrefix(c, joined) {
				break
			}
			if j > i && joined == c {
				return text[words[i][0]:words[j][1]]
			}
		}
	}
	return ""
}

var companyWordRE = lazyregexp.New(`[\p{L}\p{N}]+`)

// answerHintRE is a bracketed note of the answer's format (Nooks: "Are you
// legally authorized to work in the United States? (Yes/No)"); its "no" is
// no negation of the question.
var answerHintRE = lazyregexp.New(`(?i)[(\[]\s*(?:yes|y)\s*(?:/|\bor\b)\s*(?:no|n)\s*[)\]]`)

// legalFormRE is the legal form written after a company's name (Extreme
// Networks: "to work lawfully in the United States for Extreme Networks,
// Inc.?"): part of the name, never a sentence's end.
var legalFormRE = lazyregexp.New(`(?:,\s*|\s+)(?:Inc|LLC|Ltd|Corp|PBC)\b\.?`)

func sponsorSkeleton(text, company string) ([]string, bool) {
	text = legalFormRE.ReplaceAllString(text, "")
	t, _ := stripScope(apostrophes.Replace(withoutStrayClose(withoutExampleList(useNoteRE.ReplaceAllString(answerHintRE.ReplaceAllString(text, " "), " ")))), company)
	t = possessiveRE.ReplaceAllString(strings.ToLower(expandContractions(t)), "$1")
	t = pluralMarkRE.ReplaceAllString(t, "${1}s")
	t = egRE.ReplaceAllString(t, "eg")
	t = etcRE.ReplaceAllString(t, "etc")
	t = h1bRE.ReplaceAllString(t, "h1b")
	t = nonImmRE.ReplaceAllString(t, "nonimmigrant")
	t = edgePunctRE.ReplaceAllString(t, "")
	if innerSentenceRE.MatchString(t) {
		return nil, false
	}
	t = strings.Join(strings.Fields(strings.Map(func(r rune) rune {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			return r
		}
		return ' '
	}, t)), " ")
	t = authTypoRE.ReplaceAllString(t, "authori$1$2")
	t = andOrRE.ReplaceAllString(t, "or")
	t = ableAndAbleRE.ReplaceAllString(t, "$1")
	t = unrestrictedWithoutRE.ReplaceAllString(t, "$1$2")
	t = doOrWillRE.ReplaceAllString(t, "will you now or in the future")
	t = doNowOrWillRE.ReplaceAllString(t, "will you now or in the future")
	t = splitTenseRE.ReplaceAllString(t, "will you now or in the future require")
	t = repeatedTenseRE.ReplaceAllStringFunc(t, sameNounTense)
	t = inOrderRE.ReplaceAllString(t, "to")
	t = visaPermitListRE.ReplaceAllString(t, "$1 or")
	t = sponsorCaseRE.ReplaceAllString(t, "sponsorship")
	t = provideAidRE.ReplaceAllString(t, "to $1")
	t = withoutEmployerSupportRE.ReplaceAllString(t, "without employer sponsorship")
	t = employYouRE.ReplaceAllString(t, "")
	t = eligibilityPurposeRE.ReplaceAllString(t, "")
	t = fullTimeHoursRE.ReplaceAllString(t, "")
	t = permanentJobRE.ReplaceAllString(t, "$1")
	t = obtainRE.ReplaceAllString(t, "obtain")
	t = rightToWorkAidRE.ReplaceAllString(t, "support")
	t = resideAndWorkRE.ReplaceAllString(t, "work")
	t = sponsorAidWithRE.ReplaceAllString(t, "sponsorship for")
	t = visaListRE.ReplaceAllString(t, "$1")
	t = workOrContinueRE.ReplaceAllString(t, "work for")
	t = orNounRE.ReplaceAllString(t, "")
	t = sponsorTransferRE.ReplaceAllString(t, "sponsorship")
	t = affiliatesRE.ReplaceAllString(t, "")
	t = immigrationRelatedRE.ReplaceAllString(t, "immigration")
	t = benefitOrVisaRE.ReplaceAllString(t, "$1")
	t = beginOrContinueRE.ReplaceAllString(t, "continue")
	t = beginWorkRE.ReplaceAllString(t, "to $1")
	t = soThatPurposeRE.ReplaceAllString(t, "$1")
	for prev := ""; prev != t; {
		prev, t = t, orOtherAidRE.ReplaceAllString(t, "$1")
	}
	t = orOtherwiseWorkRE.ReplaceAllString(t, "to work")
	t = sponsorOrOtherwiseRE.ReplaceAllString(t, "sponsor")
	t = needForRE.ReplaceAllString(t, "NEEDFOR")
	t = forYouRE.ReplaceAllString(t, "FORYOU")
	t = timeRE.ReplaceAllString(t, "T")
	t = leadRE.ReplaceAllString(t, "LEAD")
	t = rightRE.ReplaceAllString(t, "RIGHT")
	t = demonstrativeRE.ReplaceAllString(t, "the $1")

	own := map[string]bool{}
	// The company's words are compared without accents (the board's "ŌURA").
	for _, w := range tokenize(foldPlace(company + " " + companyAsWritten(text, company) + " " + companyJoined(text, company) + " " + namedEmployer(text))) {
		if structuralWords[w] == "" {
			own[w] = true
		}
	}
	var skeleton []string
	for _, w := range strings.Fields(t) {
		sym, structural := structuralWords[w]
		// Sola's "visa sponsporship": a misspelt word of the sponsor family is the noun.
		if !structural && strings.HasPrefix(w, "spons") {
			sym, structural = symSponsor, true
		}
		switch {
		case structural:
			// Adjacent requirement nouns are one ("work visa sponsorship"); the
			// sponsorship noun wins over a bare aid noun ("visa support").
			if last := len(skeleton) - 1; last >= 0 && (sym == symSponsor || sym == symAid) &&
				(skeleton[last] == symSponsor || skeleton[last] == symAid) {
				if sym == symSponsor {
					skeleton[last] = symSponsor
				}
				continue
			}
			// A time qualifier set off inside the noun ("sponsorship, now or in
			// the future, for work authorization") does not part it.
			if n := len(skeleton); n >= 2 && skeleton[n-1] == symTime && (sym == symSponsor || sym == symAid) &&
				(skeleton[n-2] == symSponsor || skeleton[n-2] == symAid) {
				if sym == symSponsor {
					skeleton[n-2] = symSponsor
				}
				continue
			}
			skeleton = append(skeleton, sym)
		case inertWords[w], own[w], own[foldPlace(w)]:
		default:
			return nil, false
		}
	}
	return skeleton, len(skeleton) > 0
}

// closedReading reads a question against a closed set of shapes. negated is
// the complement reading; ok is false when the wording is not one of the shapes,
// in which case negated is meaningless. The sponsorship and the work-
// authorization readers differ only in their shapes.
func closedReading(text, company string, shapes []*lazyregexp.Regexp) (negated, ok bool) {
	skeleton, ok := sponsorSkeleton(text, company)
	if !ok {
		return false, false
	}
	// One time qualifier at most: two would be two tenses under one reading.
	times := 0
	for _, sym := range skeleton {
		if sym == symTime {
			times++
		}
	}
	if times > 1 {
		return false, false
	}
	line := strings.Join(skeleton, " ")
	for _, shape := range shapes {
		if shape.MatchString(line) {
			return slices.Contains(skeleton, symNeg) || slices.Contains(skeleton, symNo) ||
				slices.Contains(skeleton, symWithout), true
		}
	}
	return false, false
}

// sponsorshipPolarity reads a sponsorship question. A company's own "we do not
// sponsor" notice in front of or behind the question is set aside first: it
// states the company's policy and leaves the question's polarity as it was.
// A note that names Yes as the answer of an applicant who requires sponsorship
// restates a plain question and is set aside; under a negated question it
// names the opposite answer, and the text is not read. A text that still holds
// an inclusion note naming a visa is not read either: the note widens Yes, and
// only a recorded need reads past it (derivedSponsorship).
func sponsorshipPolarity(text, company string) (negated, ok bool) {
	if withoutInclusionNote(text) != text {
		return false, false
	}
	// Each note's answer must be the reading's own: a note that tells a
	// citizen "no" or a non-citizen "yes" says the question asks for the need.
	var asksNeed []bool
	for _, m := range citizenAnswerNoteRE.FindAllStringSubmatch(text, -1) {
		asksNeed = append(asksNeed, (m[1] != "" || m[2] != "") == strings.EqualFold(m[3], "yes"))
	}
	text = citizenAnswerNoteRE.ReplaceAllString(text, " ")
	text = ifYesLeadRE.ReplaceAllString(text, "")
	text = immigrationLawRE.ReplaceAllString(ifYesFollowUpRE.ReplaceAllString(text, "?"), "")
	text = trailingTimeRE.ReplaceAllString(text, " $1?")
	text = eitherTenseLeadRE.ReplaceAllString(text, "Will you now or in the future ")
	text = workplaceTailRE.ReplaceAllString(text, "?")
	rest := withoutYesIfRequiredNote(text)
	q := placeLeadRE.ReplaceAllString(questionBeforeNote(withoutPreamble(withoutSponsorshipNotices(rest))), "")
	q = hiredLeadRE.ReplaceAllString(purposeLeadRE.ReplaceAllString(q, ""), "")
	negated, ok = closedReading(q, company, sponsorShapes)
	if rest != text && negated || ok && slices.Contains(asksNeed, negated) {
		return false, false
	}
	return negated, ok
}

// citizenAnswerNoteRE is a sentence after the question that tells a citizen,
// or a non-citizen, which answer to give (SmarterDx: "If you are a US
// citizen, your answer to this question should be "no." If you are not a US
// citizen and you have non-immigrant status, for example F-1 or H-1, your
// answer to this question should be "yes.""). Windfall asks the same with the
// non-immigrant status alone ("If you have non-immigrant status, ..."). The
// question is read without it; group 1 or 2 marks the non-citizen, group 3 is
// the answer.
var citizenAnswerNoteRE = lazyregexp.New(`(?i)\s*\bif\s+you\s+(?:are\s+(not\s+)?an?\s+(?:u\.?\s?s\.?|united\s+states)\s+citizen|(have|hold)\s+(?:a\s+)?non-?immigrant\s+(?:status|visa))\b[^.?!]{0,160}?,\s*(?:then\s+)?(?:your\s+answer(?:\s+to\s+this\s+question)?\s+should\s+be|you\s+should\s+(?:answer|select|choose))\s+["'\x{201C}\x{201D}\x{2018}\x{2019}]?(yes|no)\b[.?!"'\x{201C}\x{201D}\x{2018}\x{2019}]*`)

// ifYesLeadRE opens a whole sponsorship question with the answer before it
// that asks it (Sumo Logic: "If yes, will you now or in the future require
// employer sponsorship ...?"): the question after it is read as it stands.
var ifYesLeadRE = lazyregexp.New(`(?i)^\s*if\s+(?:yes|so)\s*[,:;-]\s*`)

// hiredLeadRE is a lead that puts the question after a hire (Osmo: "If hired
// by Osmo, will you now or in the future require visa sponsorship ...?"): a
// need of sponsorship is one in the job, so the lead leaves the polarity as it
// was.
var hiredLeadRE = lazyregexp.New(`(?i)^if\s+(?:you\s+are\s+|you're\s+)?(?:hired|selected|offered\s+(?:the|this|a)\s+(?:role|position|job))(?:\s+(?:by|at|with)\s+[^,?]{1,40})?\s*,\s*`)

// workplaceTailRE closes a question by naming where the work is done (Mural:
// "... need sponsorship from your employer in the location you will be working
// from?"), or by pointing back to the place the question before it named
// (Groupon: "... require employer sponsorship to work there?"), or the place
// applied to (Aquatic: "... in the location(s) for which you are applying?"):
// it names the place, not a second tense.
var workplaceTailRE = lazyregexp.New(`(?i)\s+(?:in\s+the\s+(?:location|country|place|region)\s+(?:where\s+|that\s+)?you\s+will\s+(?:be\s+)?work(?:ing)?(?:\s+(?:from|in))?|in\s+the\s+location(?:\(s\)|s)?\s+(?:for\s+which|to\s+which|that)\s+you\s+are\s+applying(?:\s+(?:for|to))?|to\s+work\s+there)\s*\?\s*$`)

// ifYesFollowUpRE is a sentence after the question that asks what to add when
// the answer is Yes (Mintlify: "... require visa sponsorship? If yes, select
// the type of sponsorship."): it leaves the question's polarity as it was.
var ifYesFollowUpRE = lazyregexp.New(`(?i)\?\s+if\s+(?:yes|so)\s*[,:;-]?\s*(?:please\s+)?(?:select|choose|specify|indicate|explain|describe|list|provide|state|tell\s+us|let\s+us\s+know)\b[^?]*$`)

// trailingTimeRE is a time qualifier asked as a sentence of its own after the
// question (Replo: "Do you need a visa to work in the United States? Anytime
// now or in the future?"): it qualifies the question, and a leading "anytime"
// adds nothing to it. The group is the qualifier.
var trailingTimeRE = lazyregexp.New(`(?i)\?\s+(?:(?:at\s+)?any\s*(?:time|point),?\s+)?((?:either\s+)?now\s+or\s+(?:at\s+any\s+(?:time|point)\s+)?in\s+the\s+future|now\s+or\s+later|in\s+the\s+future)\s*\?\s*$`)

// eitherTenseLeadRE asks the need with two auxiliaries (Arketa: "Will you or
// do you require sponsorship?"): the need now or later, as "now or in the
// future" asks it.
var eitherTenseLeadRE = lazyregexp.New(`(?i)^\s*(?:will|do)\s+you\s+or\s+(?:will|do)\s+you\s+`)

// purposeLeadRE is a lead that says why the question is asked (Proof: "In
// order to obtain or to maintain employment eligibility, will you now or in the
// future require ..."): it leaves the question's polarity as it was.
var purposeLeadRE = lazyregexp.New(`(?i)^in\s+order\s+to\s+(?:obtain|maintain|keep|retain|secure)(?:\s+or\s+(?:to\s+)?(?:obtain|maintain|keep|retain|secure))?\s+(?:your\s+)?(?:employment|work)\s+(?:eligibility|authori[sz]ation|status)\s*,\s*`)

var (
	// yesIfRequiredNoteRE is a sentence that names Yes as the answer of an
	// applicant who requires sponsorship (Envoy: "If you’ll require Envoy to
	// commence, i.e., “sponsor,” an immigration or work permit case in order to
	// employ you, either now or at some point in the near future, then you
	// should answer yes.").
	yesIfRequiredNoteRE = lazyregexp.New(`(?i)^if\s+you(?:['\x{2019}]ll|\s+will|\s+would)?\s+(?:require|need)\s+(.+?),?\s+(?:then\s+)?(?:you\s+should\s+|please\s+)?(?:answer|select)\s+["'\x{2018}\x{2019}\x{201C}\x{201D}]?yes["'\x{2018}\x{2019}\x{201C}\x{201D}]?\s*\.?$`)
	// yesIfRequiredBlockRE is a word that turns the note's condition away from
	// the plain requirement of sponsorship.
	yesIfRequiredBlockRE = lazyregexp.New(`(?i)` + noteTurnWords + `|\b(?:transfer\w*|extension\w*|extend\w*|renew\w*)\b`)
	// answerYesTurnRE is a word that narrows or turns an answer-Yes note's
	// condition. The note is set aside only for a recorded need, which answers
	// Yes whatever transfer or renewal the note adds (Zocdoc: "Write "Yes" if
	// you require any immigration or work permit/EAD application or renewal
	// ..."), so those words do not keep it.
	answerYesTurnRE = lazyregexp.New(`(?i)` + noteTurnWords)
)

// noteTurnWords are the words that turn or narrow a note's condition.
const noteTurnWords = `\b(?:not|no|never|without|unless|except|but|rather|instead|only)\b|n['\x{2019}]t\b`

// answerYesIfNoteRE is a note that tells which applicants answer Yes (Peloton:
// "Note: Answer yes if you are on an employer-sponsored visa or some other visa
// which may require sponsorship in the future."), the condition first or last
// (HackerOne: "(NOTE: If you have a visa with an expiration date, please
// select "Yes" below)"); its groups are the condition. answerYesBracketNoteRE
// is the bracketed note anywhere in a sentence, a statement run on after it.
var (
	answerYesIfNoteRE      = lazyregexp.New(`(?i)^\(?\s*` + answerYesNote(`.`) + `\.?\)?$`)
	answerYesBracketNoteRE = lazyregexp.New(`(?i)\(\s*` + answerYesNote(`[^()]`) + `\.?\s*\)`)
	visaWordRE             = lazyregexp.New(`(?i)\b(?:visa|sponsor\w*)\b`)
)

// answerYesNote is the note's pattern, its condition made of runes the class
// matches.
func answerYesNote(class string) string {
	const verb = `(?:please\s+)?(?:answer|select|choose|respond|write|type|enter)\s+["'\x{2018}\x{2019}\x{201C}\x{201D}]?yes\.?["'\x{2018}\x{2019}\x{201C}\x{201D}]?`
	return `(?:note\s*:\s*)?(?:` + verb + `\s+if\s+you\s+(` + class + `+?)|if\s+you\s+(` + class + `+?),?\s+` + verb + `(?:\s+(?:below|above))?)`
}

// answerYesCondition reports whether a note's condition names a visa or
// sponsorship and nothing that turns it.
func answerYesCondition(m []string) bool {
	c := m[1] + m[2]
	return visaWordRE.MatchString(c) && !answerYesTurnRE.MatchString(c)
}

// inclusionNoteRE is a note that says what the question includes or when it
// applies (LaunchDarkly: "(Note: This includes visa statuses such as H-1B,
// and applies even if you don't need sponsorship immediately but will in the
// future, i.e. OPT/CPT)"), in brackets or as a sentence of its own. A note
// that leaves something out ("This does not include ...") is not one. Dandy's
// "[“Other employer support” includes but is not limited to: ...]" says what a
// quoted term of the question includes.
var inclusionNoteRE = lazyregexp.New(`(?i)\(\s*note\s*:\s*this\s+(?:also\s+)?(?:includes?|applies)\b[^()]*\)|(?:^|[.!?]\s+)note\s*:\s*this\s+(?:also\s+)?(?:includes?|applies)\b[^.!?]*[.!?]?|` +
	`\[\s*["'\x{201C}\x{2018}][^"'\x{201C}\x{201D}\x{2018}\x{2019}\[\]]{1,60}["'\x{201D}\x{2019}]\s+(?:also\s+)?includes?\b[^\[\]]*\]`)

// withoutInclusionNote drops the inclusion notes that name a visa or
// sponsorship. Such a note only adds applicants who answer Yes, so it is set
// aside only for one whose recorded need already answers Yes.
func withoutInclusionNote(text string) string {
	return inclusionNoteRE.ReplaceAllStringFunc(text, func(m string) string {
		if !visaWordRE.MatchString(m) {
			return m
		}
		return " "
	})
}

var (
	// sponsorLimitNoteRE is a sentence that only states the employer's
	// sponsorship limit (Meadow: "Please Note: We are only considering
	// candidates who do not require sponsorship to work in the U.S. at this
	// time." and "As such, if you require sponsorship, we will reject your
	// application.").
	sponsorLimitNoteRE = lazyregexp.New(`(?i)^\W*(?:(?:please\s+)?note\s*:?\s*)?(?:(?:as\s+such|therefore|unfortunately|currently|at\s+this\s+time),?\s+)?` +
		`(?:if\s+you\s+(?:will\s+)?(?:require|need)\b[^,.!?]{0,60},\s+)?(?:we|the\s+company|this\s+(?:role|position))\s+(?:are|is|will|do|does|can|cannot|can['\x{2019}]t|won['\x{2019}]t)\b[^?]*$`)
	// answerWordRE is a note that tells the answer.
	answerWordRE = lazyregexp.New(`(?i)\b(?:answer|select|choose|respond|indicate|check|mark|yes)\b`)
)

// withoutSponsorLimitNote is the one question of a text whose later sentences
// each only state the employer's sponsorship limit; any other text is
// returned as it is. Such a note asks nothing and tells no answer, so the
// question before it is read whichever way the record answers it.
func withoutSponsorLimitNote(text string) string {
	sentences := sentencesOf(text)
	if len(sentences) < 2 || !strings.HasSuffix(sentences[0], "?") {
		return text
	}
	for _, n := range sentences[1:] {
		if !sponsorLimitNoteRE.MatchString(n) || !visaWordRE.MatchString(n) || answerWordRE.MatchString(n) {
			return text
		}
	}
	return sentences[0]
}

// answerNoNeedNoteRE is a note that tells applicants with no need to answer
// No, naming besides the absence of need only the standings that never need
// sponsorship (Brigit: "If you are a US citizen or permanent resident and do
// not need sponsorship at any time, answer “No.”").
var answerNoNeedNoteRE = lazyregexp.New(`(?i)^if\s+you\s+(?:are\s+)?(?:(?:a\s+)?` + noNeedStanding + `(?:\s*(?:,|/|or|and)\s*(?:a\s+)?` + noNeedStanding + `)*\s+and\s+)?` +
	`(?:do\s+not|don['\x{2019}]t|will\s+not|won['\x{2019}]t)\s+(?:need|require)\s+(?:(?:any|employer|visa)\s+)*sponsorship(?:\s+(?:at\s+any\s+time|now\s+or\s+in\s+the\s+future|now|in\s+the\s+future))?\s*,?\s+` +
	`(?:then\s+)?(?:please\s+)?(?:answer|select|choose)\s+["'\x{2018}\x{2019}\x{201C}\x{201D}]?no\.?["'\x{2018}\x{2019}\x{201C}\x{201D}]?\s*\.?$`)

// noNeedStanding is a standing that never needs sponsorship.
const noNeedStanding = `(?:(?:U\.?S\.?|united\s+states)\s+citizen|(?:lawful\s+)?permanent\s+resident|green\s+card\s+holder)`

// withoutAnswerNotes drops the notes answerYesIfNoteRE reads whose condition
// names a visa or sponsorship and nothing that turns it ("if you do not need
// a visa" would make Yes the other answer), and the notes answerNoNeedNoteRE
// reads, when another sentence remains. Either note only names applicants a
// recorded need answers Yes for, so it is set aside only for one whose
// recorded need already answers Yes.
func withoutAnswerNotes(text string) string {
	t := answerYesBracketNoteRE.ReplaceAllStringFunc(text, func(m string) string {
		if answerYesCondition(answerYesBracketNoteRE.FindStringSubmatch(m)) {
			return " "
		}
		return m
	})
	sentences := sentencesOf(t)
	var kept []string
	for _, s := range sentences {
		if m := answerYesIfNoteRE.FindStringSubmatch(strings.TrimSpace(s)); m != nil && answerYesCondition(m) || answerNoNeedNoteRE.MatchString(strings.TrimSpace(s)) {
			continue
		}
		kept = append(kept, s)
	}
	if len(kept) == 0 || (t == text && len(kept) == len(sentences)) {
		return text
	}
	return strings.Join(kept, " ")
}

// withoutYesIfRequiredNote drops the sentences yesIfRequiredNoteRE reads whose
// condition names sponsorship and nothing that turns it, when another sentence
// remains.
func withoutYesIfRequiredNote(text string) string {
	sentences := sentencesOf(text)
	var kept []string
	for _, s := range sentences {
		if m := yesIfRequiredNoteRE.FindStringSubmatch(s); m != nil &&
			strings.Contains(strings.ToLower(m[1]), "sponsor") && !yesIfRequiredBlockRE.MatchString(m[1]) {
			continue
		}
		kept = append(kept, s)
	}
	if len(kept) == len(sentences) || len(kept) == 0 {
		return text
	}
	return strings.Join(kept, " ")
}

// sponsorshipPolarityOf is the polarity of the step's question, read from text.
// A route that tagged the question as negated and a text that reads as plain
// disagree, and then nothing is certain.
func sponsorshipPolarityOf(s *Step, text string) (negated, ok bool) {
	negated, ok = sponsorshipPolarity(text, s.Req.Posting.Company)
	if ok && s.ClassTag() == "negated-sponsorship" && !negated {
		return false, false
	}
	return negated, ok
}

var (
	// exampleListRE is a parenthesised list of examples ("(e.g., F1, H-1B visa
	// status)", "(ex: H1B, E-3, etc...)"): an illustration of the noun before
	// it, not a clause.
	// The abbreviation may lose its last stop (Anduril: "(e.g, H1B visa)").
	// Zinnia: "(examples but not limited to: H1B, F-1 Student Visa, etc.)".
	// SentiLink: "(e.g.H-1B, TN, E-3, O-1, etc)", the space after the stop
	// left out; webAI: "(e.g;- H-1 B visa, Work Permit, etc)".
	exampleListRE = lazyregexp.New(`(?i)\(\s*(?:(?:e\.g|i\.e)\.[\s,:]?|(?:e\.g|i\.e|ex\.?|for example|for instance|such as|including|examples?(?:\s+(?:but\s+)?not\s+limited\s+to)?)[\s,:;][-\x{2013}\s]*)([^()]*)\)`)
	// visaCodeRE is a visa category code: F1, H-1B, H1-B, J-1, L1, E3.
	visaCodeRE = lazyregexp.New(`^[a-z]-?\d-?[a-z]?$`)
	// exampleCatchAllRE is an example list's closing catch-all, "or any
	// other work authorization requiring entry of GM as the immigration
	// employer of record ...": it restates the sponsorship the question asks
	// about, unless it holds a word that narrows or negates it.
	// DoorDash closes on "or any immigration work authorization requiring a
	// written submission from the company to a government agency".
	exampleCatchAllRE      = lazyregexp.New(`\bor\s+any\b.*$`)
	exampleCatchAllBlockRE = lazyregexp.New(`\b(?:not|no|never|without|except|unless|transfer\w*|extension\w*|extend\w*|renew\w*)\b`)

	// abbreviationRE is a period that does not end a sentence: that of a
	// listed abbreviation, and that of "U.S." when a lower-case word or a
	// bracket follows (Voxel: "work in the U.S. (e.g. H-1B, ...), either now").
	abbreviationRE = lazyregexp.New(`(?i)\b(?:e\.g|i\.e|etc|vs|inc|ltd|co)\.|\bu\.s\.(?:a\.)?(?-i:\s+[a-z(])`)
	sentenceEndRE  = lazyregexp.New(`[.?!]+["'\x{201D}\x{2019}]?(?:\s+|$)`)
	// noticeRE is a statement that the company does not sponsor, or that
	// sponsorship is not to be had (HackerOne: "Visa/work permit sponsorship is
	// not available for this position"; Legora: "We may not be able to
	// provide visa/work permit sponsorship for this role").
	noticeRE = lazyregexp.New(`(?i)\b(?:do(?:es)?\s+not|don['\x{2019}]?t|doesn['\x{2019}]?t|cannot|can['\x{2019}]?t|will\s+not|won['\x{2019}]?t|is\s+not|are\s+not|isn['\x{2019}]?t|aren['\x{2019}]?t|unable\s+to|not\s+able\s+to|(?:may|might)\s+not\s+be\s+able\s+to)\s+(?:currently\s+|able\s+to\s+)?(?:sponsor\w*|(?:offer|provide)\s+(?:[\w/-]+\s+){0,2}sponsorship)\b|` +
		`\bsponsorship\s+(?:is\s+not|isn['\x{2019}]t|are\s+not|aren['\x{2019}]t|will\s+not\s+be|won['\x{2019}]t\s+be)\s+(?:currently\s+)?(?:available|offered|provided|possible)\b`)
	// noticeBlockRE keeps a sentence that asks, instructs, conditions or turns
	// on the applicant: only a plain statement about the company is a notice.
	noticeBlockRE = lazyregexp.New(`(?i)\?|\b(?:if|unless|except|but|however|yes|no|answer|select|choose|click|mark|check|only|you|your)\b`)
)

// visaExampleWords are the words, besides the category codes, that an example
// list may hold and still be an illustration. Any other word ("transfer",
// "extension", "not") keeps the list in the text, so the reading stays closed.
var visaExampleWords = setOf("visa", "visas", "status", "etc", "and", "or", "stem", "opt", "cpt", "tn", "ead",
	"other", "employment-based", "immigration", "case", "petition", "sponsorship", "a", "an", "work", "permit", "permits",
	// Singapore's Employment Pass (Bitdeer); Zinnia's "F-1 Student Visa".
	"employment", "pass", "student",
	// Samsara's ways of being authorized: "you are a citizen, you have a visa".
	"you", "are", "have", "citizen", "citizens", "permanent", "resident", "residents", "green", "card", "holder", "holders",
	// New Relic's "U.S. F-1, H-1B, TN, E-3, or other non-immigrant visa category".
	"us", "non-immigrant", "nonimmigrant", "category", "categories",
	// Sumo Logic's "(e.g., nonimmigrant status such as H-1B, TN, O-1, etc.)".
	"such", "as",
	// Kobie's "(for example, an H-1B visa or other work authorization sponsorship)".
	"authorization", "authorisation")

// withoutExampleList drops the "(e.g., ...)" parentheticals that list nothing
// but visa categories.
// useNoteRE is a note on how the answer is used (Block: "(This information
// will not be used in assessing your qualifications for any position.)";
// Mapbox: "This information helps us understand your needs and will not impact
// your application."), bracketed or a closing sentence: it asks nothing, so
// its "not" never turns the question.
var useNoteRE = lazyregexp.New(`(?i)\(\s*(?:this|the|your)\s+(?:information|answer|response)s?\s+` + useNoteHelps + `(?:will|would|shall)\s+not\s+(?:be\s+(?:used|considered|shared)|affect|impact)\b[^()]*\)|` +
	`(?:^|[.?!]\s+)(?:this|the|your)\s+(?:information|answer|response)s?\s+` + useNoteHelps + `(?:will|would|shall)\s+not\s+(?:be\s+(?:used|considered|shared)|affect|impact)\b[^.?!]*[.?!]?\s*$`)

// useNoteHelps is what the note says the answer is for, before its "and will
// not": "helps us understand your needs and".
const useNoteHelps = `(?:(?:helps?|allows?|lets?)\s+us\s+[^.?!()]{1,60}?\s+and\s+)?`

// withoutStrayClose drops a closing bracket at the end of the text that the
// text never opened (Everlaw: "... visa status (e.g. H1-B status)?)"), a typo
// that would otherwise leave the question mark inside the reading.
func withoutStrayClose(text string) string {
	i := strings.LastIndex(text, ")")
	if i < 0 || strings.Count(text, ")") <= strings.Count(text, "(") || strings.Trim(text[i+1:], " ?.!") != "" {
		return text
	}
	return text[:i] + text[i+1:]
}

func withoutExampleList(text string) string {
	return exampleListRE.ReplaceAllStringFunc(text, func(m string) string {
		list := h1bRE.ReplaceAllString(strings.ToLower(usAbbrevRE.ReplaceAllString(exampleListRE.FindStringSubmatch(m)[1], "US")), "h1b")
		if tail := exampleCatchAllRE.FindString(list); tail != "" && !exampleCatchAllBlockRE.MatchString(tail) {
			list = strings.TrimSuffix(list, tail)
		}
		for _, w := range strings.FieldsFunc(list, func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '-' }) {
			if !visaExampleWords[w] && !visaCodeRE.MatchString(w) {
				return m
			}
		}
		return " "
	})
}

// withoutSponsorshipNotices drops the sentences that only state the company's
// refusal to sponsor, when a sentence that is not one remains. A text with no
// such sentence, or made of nothing else, is returned unchanged.
func withoutSponsorshipNotices(text string) string {
	masked := abbreviationRE.ReplaceAllStringFunc(text, func(m string) string { return strings.ReplaceAll(m, ".", "_") })
	var kept []string
	dropped, start := false, 0
	for _, loc := range append(sentenceEndRE.FindAllStringIndex(masked, -1), []int{len(text), len(text)}) {
		if loc[1] <= start {
			continue
		}
		sentence := text[start:loc[1]]
		start = loc[1]
		if noticeRE.MatchString(sentence) && !noticeBlockRE.MatchString(sentence) {
			dropped = true
			continue
		}
		kept = append(kept, strings.TrimSpace(sentence))
	}
	if !dropped || len(kept) == 0 {
		return text
	}
	return strings.Join(kept, " ")
}

// headingRE is a question's title run straight into the question
// (Teamtailor: "US Lawful Worker Required Are you ..."; Ashby: "Work
// Authorization: Are you ..."; Gem: "U.S. Work Authorization - Are you ..."):
// up to six capitalised words, an abbreviation's periods kept, the last
// possibly closed by a colon or followed by a dash, before an auxiliary and
// "you". A work-eligibility title is one in sentence case too (Aquatic:
// "Employment eligibility status Will you require ...").
var headingRE = lazyregexp.New(`^(?:(?:[A-Z][A-Za-z0-9./&-]*\s+){0,5}[A-Z][A-Za-z0-9./&-]*|(?:Employment|Work)\s+(?:eligibility|authori[sz]ation)(?:\s+status)?):?\s+(?:[-\x{2013}\x{2014}]\s+)?((?:Are|Will|Do|Does|Can|Could|Would|Is|Have|Has)\s+you\b)`)

// withoutPreamble reduces a question that follows plain statements to the
// question itself: the law-and-policy paragraph an employer writes in front of
// "Are you authorized to work ...?" is not part of what is asked. Only a last
// sentence that asks, after sentences that are all plain statements (no
// question, condition, instruction or address to the applicant), stands alone;
// any other text is returned unchanged.
func withoutPreamble(text string) string {
	text = withoutAskUs(text)
	if m := headingRE.FindStringSubmatchIndex(text); m != nil {
		text = text[m[2]:]
	}
	sentences := sentencesOf(text)
	if len(sentences) < 2 || !strings.HasSuffix(sentences[len(sentences)-1], "?") {
		return text
	}
	for _, s := range sentences[:len(sentences)-1] {
		if noticeBlockRE.MatchString(responseRequiredRE.ReplaceAllString(s, "")) {
			return text
		}
	}
	return sentences[len(sentences)-1]
}

// responseRequiredRE opens a sentence that says the question must be answered
// (Twitch: "Your response is mandatory when applying for a U.S.-based
// position."): it addresses the applicant but conditions nothing; what follows
// it is still read.
var responseRequiredRE = lazyregexp.New(`(?i)^\s*your\s+(?:response|answer)\s+is\s+(?:mandatory|required)\b`)

// askUsLead opens an invitation to ask the company about the question
// ("Please ask us if you are uncertain whether you may need immigration
// sponsorship or desire clarification."): it asks nothing of the applicant.
const askUsLead = `(?:please\s+)?(?:feel\s+free\s+to\s+)?(?:ask|contact|e-?mail|call|reach\s+out\s+to)\s+us\b`

var (
	askUsParenRE    = lazyregexp.New(`(?i)\(\s*` + askUsLead + `[^()]*\)`)
	askUsSentenceRE = lazyregexp.New(`(?i)^` + askUsLead)
)

// withoutAskUs drops the invitations to ask the company, parenthesised or
// standing as their own sentence, when something else remains.
func withoutAskUs(text string) string {
	t := strings.TrimSpace(askUsParenRE.ReplaceAllString(text, " "))
	var kept []string
	for _, s := range sentencesOf(t) {
		if !askUsSentenceRE.MatchString(s) || strings.HasSuffix(s, "?") {
			kept = append(kept, s)
		}
	}
	if len(kept) == 0 {
		return text
	}
	if t != text || len(kept) < len(sentencesOf(t)) {
		return strings.Join(kept, " ")
	}
	return text
}

// sentencesOf splits text into its trimmed sentences; an abbreviation's period
// ends none.
func sentencesOf(text string) []string {
	masked := abbreviationRE.ReplaceAllStringFunc(text, func(m string) string { return strings.ReplaceAll(m, ".", "_") })
	var sentences []string
	start := 0
	for _, loc := range append(sentenceEndRE.FindAllStringIndex(masked, -1), []int{len(text), len(text)}) {
		if loc[1] <= start {
			continue
		}
		if s := strings.TrimSpace(text[start:loc[1]]); s != "" {
			sentences = append(sentences, s)
		}
		start = loc[1]
	}
	return sentences
}
