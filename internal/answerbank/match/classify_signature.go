package match

import (
	"slices"
	"strings"

	"applysmith/internal/lazyregexp"
)

// signature is one row of the form-signature table: a predicate over the
// normalized option labels, an optional condition on the question text, and the
// entry it routes to. Every predicate needs two corroborating labels, so a plain
// Yes/No set never matches.
type signature struct {
	Target string
	Labels func(labels []string) bool
	Text   func(question string) bool // nil: no condition
	// Owns marks a question only the target entry can answer: when the entry
	// binds none of the options, the walk stops there and the entry abstains,
	// instead of going on to an entry about something else.
	Owns bool
}

// brightwheel: "authorized to work ... without sponsorship" beside "I require
// company sponsorship for a new visa".
var (
	// Intercom: an authorization as a citizen or permanent resident ("I am
	// authorised to work ... (citizen, permanent resident etc)") needs no
	// sponsorship either.
	authWithoutSponsorRE = lazyregexp.New(`\bauthori[sz]ed to work\b.*\b(?:without (?:\w+ ){0,2}sponsorship|citizens?|permanent residents?|green card)\b`)
	requiresSponsorRE    = lazyregexp.New(`\b(?:require|need)s? (?:\w+ ){0,2}sponsorship\b`)
	// Circleback: a Yes qualified by a sponsorship or visa need ("Yes, but I
	// will need visa sponsorship in the future") beside the bare Yes.
	bareYesRE    = lazyregexp.New(`(?i)^\W*yes\W*$`)
	yesButNeedRE = lazyregexp.New(`(?i)^\W*yes\W+but\b.*\b(?:require|need)s?\b.*\b(?:sponsor\w*|visas?)\b`)
	// Method and Treasury Prime: "I'm looking for visa sponsorship" beside
	// "I'm authorized to work in the United States".
	seekingSponsorRE = lazyregexp.New(`(?i)\b(?:looking\s+for|seeking|in\s+need\s+of)\b.*\bsponsorship\b`)
)

var (
	dialCodeRE        = lazyregexp.New(`^\+\d{1,4}\b|\+\d{1,4}$`)
	retainRE          = lazyregexp.New(`\b(?:keep|kept|retain|retained)\b.*\bmonths?\b`)
	deleteRE          = lazyregexp.New(`\b(?:delete|deleted|deletion|remove|removed)\b.*\b(?:end|application)\b`)
	authAnyEmployerRE = lazyregexp.New(`authori[sz]ed to work.*any employer`)
	authRestrictedRE  = lazyregexp.New(`present employer only|current employer only|require sponsorship|status.*unknown`)
	relocationLabelRE = lazyregexp.New(`\b(?:relocate|relocating|relocation|(?:willing|able) to move|move|moving to)\b`)
	presenceLabelRE   = lazyregexp.New(`\b(?:local|commutable|commute|in the office|into the office|on-?site|in-person|not able to work|(?:currently )?(?:live|reside) in this location|currently (?:living|residing) in|(?:located|based) in)\b`) // Harvey: "Yes, I'm based in this location"; Abridge: "I am currently living in the San Francisco Bay ..."
	// basedInOfficeLabelRE is a Yes that places the applicant in a place:
	// "Yes, I am based in Chicago and am comfortable working from the Chicago
	// office." Group 1 is the place's first word; a word that points rather
	// than names ("this location", Harvey) is no office (basedInPointerWords).
	basedInOfficeLabelRE = lazyregexp.New(`^\W*yes\b\W*(?:i\s+am|i\W?m)\s+(?:currently\s+)?(?:based|located|living|residing)\s+(?:in|near)\s+(?:the\s+)?(\w+)`)
	basedInPointerWords  = setOf("this", "that", "these", "those", "our", "your", "one", "any", "a", "an", "area", "office", "location", "region", "city")
	// remoteEligibleAskRE is a presence or relocation question about the
	// employer's eligible places (BILL).
	remoteEligibleAskRE = lazyregexp.New(`(?i)\b(?:located|live|living|reside|residing|based)\b[^?]{0,80}\brelocat\w*\b[^?]{0,80}\b(?:remote[- ])?eligible\s+(?:states?|locations?|regions?|countries)\b`)
	// relocationAssistanceAskRE asks about help with the move (Scopely).
	relocationAssistanceAskRE = lazyregexp.New(`(?i)` + rxRelocationAssistanceAsk)
	// Partly says "need" and "won't", and its current standing is "not
	// eligible and will need sponsorship". Realm: "... now, but may require
	// it in the future" and "Yes, I would require new sponsorship".
	willRequireFutureR = lazyregexp.New(`(?:will|would|may|might) (?:require|need) (?:sponsorship|it)\b.*future`)
	// futureWordRE is a label's time to come.
	futureWordRE       = lazyregexp.New(`\b(?:future|later|eventually)\b`)
	willNotRequireRE   = lazyregexp.New(`\b(?:will not|won't|do not|don't|not) (?:require|need) sponsorship`)
	currentlyRequireRE = lazyregexp.New(`currently (?:require|need) sponsorship|\bi (?:require|need) sponsorship now\b|\bnot (?:eligible|authori[sz]ed)\b.*\b(?:require|need) sponsorship|\bi would (?:require|need) new sponsorship\b`)
	requireSponsorRE   = lazyregexp.New(`require sponsorship`)
	armedForcesRE      = lazyregexp.New(`armed forces`)
	veteranOrgRE       = lazyregexp.New(`\bnoaa\b|\busphs\b|have not served`)
	optInRE            = lazyregexp.New(`\bopt[- ]?in\b`)
	smsRE              = lazyregexp.New(`\b(?:sms|text messages?)\b`)
	// Socure: "Yes - I consent to receiving WhatsApp messages" is the same consent.
	consentReceiveRE   = lazyregexp.New(`consent to receiv\w*.*\b(?:text|sms|whatsapp)\b`)
	negatedConsentRE   = lazyregexp.New(`\b(?:not|no|never|don't)\b.{0,12}consent to receiv`)
	smsNoRE            = lazyregexp.New(`\b(?:no|do not|not opt)\b`)
	refuseConsentRE    = lazyregexp.New(`\b(?:do not|don't) consent\b`)
	degreeLevelRE      = lazyregexp.New(`\b(?:high school|associates?|bachelors?|masters?|doctor(?:ate|al)?|ph\.?d|mba|juris)\b`)
	moreSpecificEduRE  = lazyregexp.New(`(?i)\b(?:currently|pursuing|enrolled|undergraduate|undergrad|field of study|area of study|major|discipline|graduat\w*|do you have)\b`)
	addressTypeLabelRE = lazyregexp.New(`^(?:home|work|business|office|other|mailing|primary|residential|select one)$`)
	addressTypeTextRE  = lazyregexp.New(`(?i)^\s*(?:address\s+)?type\s*$`)
	jobBoardRE         = lazyregexp.New(`\b(?:` + jobBoards + `)\b`)
	pleaseSpecifyRE    = lazyregexp.New(`(?i)^\s*please\s+specify(?:\s+the\s+job\s+board)?\s*$`)
	cisRE              = lazyregexp.New(`\bcis[- ]?gender\b`)
	transRE            = lazyregexp.New(`\btrans[- ]?gender\b`)
	// bareGenderRE is a plain gender option: a list that holds one asks the
	// gender, whatever cisgender and transgender options sit beside it.
	bareGenderRE = lazyregexp.New(`^(?:male|female|man|woman)$`)
	spouseRE     = lazyregexp.New(`\bspouse\b`)
	noticeWordRE = lazyregexp.New(`\bnotice\b`)
	// noticePeriodAskRE asks the notice period, which availability-notice-period
	// answers, not the start date.
	noticePeriodAskRE   = lazyregexp.New(`(?i)\bnotice\s+period\b`)
	authUSRE            = lazyregexp.New(`\bauthori[sz]ed to work in the (?:united states|u\.?s\.?a?)\b`)
	notAuthUSRE         = lazyregexp.New(`\bnot authori[sz]ed to work in the (?:united states|u\.?s\.?a?)\b`)
	authOtherSubjectRE  = lazyregexp.New(`\b(?:sponsor\w*|employers?|visa|future)\b`)
	workAuthStatementRE = lazyregexp.New(`\bwork authori[sz]ation\b|\bauthori[sz]ed to work\b`)
	// Vercel: "Please choose the option that describes your work authorization."
	statementPickRE = lazyregexp.New(`(?i)\b(?:statements?|options?)\b[^?.]*\b(?:best\s+)?(?:applies|apply|describes?)\b`)
	// authorizationToWorkRE heads a list of work-authorization statements with
	// the applicant's own authorization (Snorkel AI: "Your authorization to work
	// in the country where you live:").
	authorizationToWorkRE = lazyregexp.New(`(?i)^\W*your\s+(?:legal\s+)?authori[sz]ation\s+to\s+work\b`)
	// authByNationalityRE and authByPermitRE are two classes of status in a
	// list of work-authorization statements (Zapier, Vercel, Snorkel AI).
	authByNationalityRE = lazyregexp.New(`(?i)\bauthori[sz]ed to work\b[^.]*\b(?:due to|because of|based on|by)\s+(?:my\s+)?(?:nationality|citizenship)\b`)
	authByPermitRE      = lazyregexp.New(`(?i)\bauthori[sz]ed to work\b[^.]*\bbased on\s+(?:a\s+)?(?:valid\s+)?work permit\b`)
	// citizenOrResidentRE and visaHolderRE are two classes of status named
	// plainly (AngelList: "United States Citizen or Permanent Resident", "US
	// Work Visa Holder"; Twilio: "Valid visa with permission to work with any
	// employer where this role is listed"; Instructure: "Temporary visa with
	// open / transferable work authorization").
	citizenOrResidentRE = lazyregexp.New(`(?i)\b(?:citizens?|permanent residents?|green card)\b`)
	visaHolderRE        = lazyregexp.New(`(?i)\b(?:(?:work|employment|non-?immigrant)\s+)?visa\s+holders?\b|^\W*(?:valid|temporary)\s+(?:work\s+)?visa\b`)
	sponsorOrVisaRE     = lazyregexp.New(`(?i)\b(?:sponsor\w*|visas?)\b`)
	workAuthHeadingRE   = lazyregexp.New(`(?i)^\s*(?:(?:u\.?s\.?|united states)\s+)?(?:work|employment)\s+authori[sz]ation(?:\s+in\s+(?:the\s+)?(?:u\.?s\.?a?|united states))?(?:\s+status)?\s*[.:*?]?\s*$`)
	workAuthInRE        = lazyregexp.New(`(?i)^\s*(?:work|employment)\s+authori[sz]ation\s+in\s+(?:the\s+)?([^.:*?]{2,40}?)\s*[.:*?]?\s*$`)
	residenceStateRE    = lazyregexp.New(`(?i)\bwhere (?:are you|do you) (?:currently )?(?:located|live|reside|based)\b|\b(?:what|which) state (?:are you|do you) (?:currently )?(?:located|live|reside|based)\b|\bstate of residence\b|\b(?:current|home) state\b|^\W*(?:current|home) location\W*$`)
	otherStateRE        = lazyregexp.New(`(?i)\b(?:relocat\w*|willing|prefer\w*|licen[cs]\w*|would you like)\b`)
	relocateAskRE       = lazyregexp.New(`(?i)\brelocat\w*\b|\bmove\b`)
	// resideAskRE asks where the applicant lives now; residePreferRE is a
	// place the applicant would choose instead.
	resideAskRE      = lazyregexp.New(`(?i)\b(?:reside|live|located|based|resident)\b`)
	residePreferRE   = lazyregexp.New(`(?i)\b(?:willing|prefer\w*|would\s+you\s+like|want|open\s+to|interested|authori[sz]ed)\b`)
	communicateAskRE = lazyregexp.New(`(?i)\b(?:communicat\w*|contact\w*|reach)\b`)
	onsiteAskRE      = lazyregexp.New(`(?i)\b(?:on-?site|in[- ]office|in[- ]person|offices?|hq|headquarters)\b`)
)

// cityLabel is a label that names a US city, alone or before its state
// ("seattle, wa").
func cityLabel(l string) bool {
	head, _, _ := strings.Cut(l, ",")
	return len(usCityStates[strings.ToLower(strings.TrimSpace(head))]) > 0
}

// officeLabel is a label that names a US city or a metro's short form ("NYC",
// "SF"), alone or after a per-office Yes (Cogent: "Yes, I can work in San
// Francisco").
func officeLabel(l string) bool {
	if m := officeYesLabelRE.FindStringSubmatch(l); m != nil {
		l = m[1]
	}
	return cityLabel(l) || len(expandCityAliases(l)) > 1
}

var officeYesLabelRE = lazyregexp.New(`(?i)^\W*yes\W+i\s+can\s+work\s+(?:in|from|at)\s+(?:the\s+|our\s+)?(.+?)\W*$`)

// usStateList reports a list that is the US states: at least ten labels, and
// four in five of them a state's name or code (a placeholder and a misspelt
// name stay under the fifth).
func usStateList(ls []string) bool {
	n := 0
	for _, l := range ls {
		if _, ok := stateByName[l]; ok || stateByAbbr[l] != "" {
			n++
		}
	}
	return n >= 10 && n*5 >= len(ls)*4
}

// countryList reports a list that is countries: at least ten labels, four in
// five of them a country's name or code.
func countryList(ls []string) bool {
	n := 0
	for _, l := range ls {
		if _, ok := CountryCode(l); ok {
			n++
		}
	}
	return len(ls) >= 10 && n*5 >= len(ls)*4
}

// visaCodeLabel is a label that is a visa category and nothing else: "OPT",
// "H1B", "TN", "STEM OPT".
func visaCodeLabel(l string) bool {
	for _, re := range visaCategoryREs {
		if loc := re.FindStringIndex(l); loc != nil && strings.TrimSpace(l[:loc[0]]+l[loc[1]:]) == "" {
			return true
		}
	}
	return false
}

// aidNowRE and aidLaterRE are a need of aid now and one only later (Popl: "I
// require assistance immediately.", "... in the future.").
var (
	aidNowRE   = lazyregexp.New(`\b(?:require|need)s?\s+(?:immigration\s+|visa\s+)?(?:assistance|support)\s+(?:immediately|now|currently|right\s+now)\b`)
	aidLaterRE = lazyregexp.New(`\b(?:require|need)s?\s+(?:immigration\s+|visa\s+)?(?:assistance|support)\s+(?:in\s+the\s+future|later)\b`)
)

func anyLabel(labels []string, re *lazyregexp.Regexp) bool {
	for _, l := range labels {
		if re.MatchString(l) {
			return true
		}
	}
	return false
}

// requiresWithNoTime is a statement of a sponsorship need that names no time
// and does not deny it: the need now.
func requiresWithNoTime(l string) bool {
	return requiresSponsorRE.MatchString(l) && !willNotRequireRE.MatchString(l) && !futureWordRE.MatchString(l)
}

// authorizedToWorkAskRE asks whether the applicant may work.
var authorizedToWorkAskRE = lazyregexp.New(`(?i)\b(?:authori[sz]ed|eligible|permitted)\s+to\s+work\b`)

// countVisaStatements counts the labels that name a visa status category.
func countVisaStatements(labels []string) int {
	n := 0
	for _, l := range labels {
		for _, re := range visaCategoryREs {
			if re.MatchString(l) {
				n++
				break
			}
		}
	}
	return n
}

func countLabels(labels []string, re *lazyregexp.Regexp) int {
	n := 0
	for _, l := range labels {
		if re.MatchString(l) {
			n++
		}
	}
	return n
}

// formSignatures is the ordered signature table. The first signature whose
// predicate and text condition hold and whose target exists wins.
func formSignatures() []signature {
	return []signature{
		{Target: "phone-country-code", Labels: func(ls []string) bool {
			return len(ls) >= 3 && countLabels(ls, dialCodeRE)*2 >= len(ls)
		}},
		{Target: "full-time-start-availability", Labels: func(ls []string) bool {
			parsed, immediate := 0, false
			for _, l := range ls {
				if days, ok := parseLeadTime(l); ok {
					parsed++
					if days == 0 {
						immediate = true
					}
				}
				if noticeWordRE.MatchString(l) {
					immediate = true
				}
			}
			return parsed >= 2 && immediate
		}, Text: func(q string) bool { return !noticePeriodAskRE.MatchString(q) }},
		// Keystone: "Please share when you are available to start at Keystone."
		// over "As Soon as possible", months and quarters. The start asks route
		// to earliest-start-date, whose date takes no option list; a list of
		// months or terms is the availability the date derives.
		{Target: "full-time-start-availability", Labels: func(ls []string) bool {
			terms := 0
			for _, l := range ls {
				if _, ok := parseSeasonBracket(l); ok {
					terms++
				}
			}
			return terms >= 2
		}, Text: func(q string) bool { return startAskRE.MatchString(q) && !startAskExcludeRE.MatchString(q) }},
		{Target: "data-retention-preference", Labels: func(ls []string) bool {
			return anyLabel(ls, retainRE) && anyLabel(ls, deleteRE)
		}},
		{Target: "us-work-authorization-status", Labels: func(ls []string) bool {
			return anyLabel(ls, authAnyEmployerRE) && anyLabel(ls, authRestrictedRE)
		}},
		// Aquatic: "Current Professional Experience" over "1-3", "4-10" and
		// "10+" asks the years worked; the stored total picks the range.
		{Target: IDYearsFullTimeWork, Labels: func(ls []string) bool {
			n := 0
			for _, l := range ls {
				if _, ok := parseYearsBucket(l); ok {
					n++
				}
			}
			return n >= 3
		}, Text: func(q string) bool { return experienceTitleRE.MatchString(q) }},
		// A question about help with the move is the assistance entry's, though
		// its options say where the applicant lives (Scopely).
		{Target: IDRelocationOrLocal, Labels: func(ls []string) bool {
			return anyLabel(ls, relocationLabelRE) && anyLabel(ls, presenceLabelRE)
		}, Text: func(q string) bool { return !relocationAssistanceAskRE.MatchString(q) }},
		// BILL (Greenhouse): "Are you currently located in, or open to
		// relocating to, one BILL's remote-eligible states?" names states no
		// profile holds; it is read before every relocation route.
		{Target: "remote-eligible-location", Labels: func(ls []string) bool { return len(ls) >= 2 },
			Text: func(q string) bool { return remoteEligibleAskRE.MatchString(q) }},
		// Twitch: "Are you open to relocation?" over No, a remote No and its
		// offices asks where the applicant would move; the office picker
		// answers it from the relocation policy.
		{Target: "preferred-work-location", Labels: func(ls []string) bool {
			n := 0
			for _, l := range ls {
				if cityLabel(l) {
					n++
				}
			}
			return n >= 3
		}, Text: func(q string) bool { return relocateAskRE.MatchString(q) }},
		// Unlock Health (Greenhouse): "Are you currently based near one of our
		// offices in Chicago, Nashville, or New York, and comfortable working on
		// a hybrid schedule ...?" over "Yes, I am based in <office> and am
		// comfortable working from the <office> office." for each office and
		// "No, I am seeking a fully remote position." Owner decision: the office
		// the relocation preference ranks first, wherever the home is.
		{Target: "preferred-work-location", Labels: func(ls []string) bool {
			n := 0
			for _, l := range ls {
				if m := basedInOfficeLabelRE.FindStringSubmatch(l); m != nil && !basedInPointerWords[m[1]] {
					n++
				}
			}
			return n >= 2
		}},
		// Sigma (Greenhouse): "Are you open to working 4 days onsite in one of
		// our central offices?" over "NYC", "SF", "London" and "Remote only"
		// asks which office; the office picker answers it.
		{Target: "preferred-work-location", Labels: func(ls []string) bool {
			n := 0
			for _, l := range ls {
				if officeLabel(l) {
					n++
				}
			}
			return n >= 2
		}, Text: func(q string) bool { return onsiteAskRE.MatchString(q) && !resideAskRE.MatchString(q) }},
		// NYISO (Greenhouse): the need now said with no time at all ("I would
		// require sponsorship for a working visa.") beside the need later.
		{Target: "work-auth-and-future-sponsorship", Labels: func(ls []string) bool {
			return anyLabel(ls, willRequireFutureR) && anyLabel(ls, willNotRequireRE) &&
				(anyLabel(ls, currentlyRequireRE) || slices.ContainsFunc(ls, requiresWithNoTime))
		}},
		{Target: "sponsorship-now-or-future", Labels: func(ls []string) bool {
			return anyLabel(ls, requireSponsorRE) && anyLabel(ls, willNotRequireRE)
		}},
		{Target: "work-auth-us", Labels: func(ls []string) bool {
			yes, no := false, false
			for _, l := range ls {
				if authOtherSubjectRE.MatchString(l) {
					return false
				}
				switch {
				case notAuthUSRE.MatchString(l):
					no = true
				case authUSRE.MatchString(l):
					yes = true
				}
			}
			return yes && no
		}, Text: workAuthHeading},
		// Ideogram (Ashby): "Work Authorization in the U.S." over statements to
		// tick, one of authorization and one of sponsorship.
		{Target: "work-auth-and-future-sponsorship", Labels: workAuthStatementList, Text: workAuthHeading},
		// P&G: "Please select the statement that best applies to you." over
		// statements of work authorization, one per class of status; Snorkel AI
		// heads them "Your authorization to work in the country where you live:".
		{Target: "us-work-authorization-status", Labels: func(ls []string) bool {
			return countLabels(ls, workAuthStatementRE) >= 2
		}, Text: func(q string) bool { return statementPickRE.MatchString(q) || authorizationToWorkRE.MatchString(q) }},
		// Zapier (Ashby): the same statements under "Are you legally authorized
		// to work in that country without company support?": an authorization
		// by nationality beside one by a work permit is the status picker,
		// whatever heads it.
		{Target: "us-work-authorization-status", Labels: func(ls []string) bool {
			return anyLabel(ls, authByNationalityRE) && anyLabel(ls, authByPermitRE)
		}},
		// AngelList (Ashby): "What is your work authorization status in the
		// United States?" over a citizen or resident and a work-visa holder.
		{Target: "us-work-authorization-status", Labels: func(ls []string) bool {
			return len(ls) <= standingListMaxOptions && anyLabel(ls, citizenOrResidentRE) && anyLabel(ls, visaHolderRE)
		}},
		// Base Power (Ashby): "Are you legally authorized to work in the
		// U.S.?" over a citizen or resident and a statement for each of
		// several visa statuses ("I currently hold OPT ...", "... STEM OPT
		// ...", "... a TN visa ...") names the status, however many options.
		{Target: "us-work-authorization-status", Labels: func(ls []string) bool {
			return anyLabel(ls, citizenOrResidentRE) && countVisaStatements(ls) >= 3
		}, Text: authorizedToWorkAskRE.MatchString},
		// brightwheel (Ashby): "Are you currently authorized to work in the US?"
		// over an authorization without sponsorship and a requirement of it
		// states the standing in its options.
		{Target: "work-auth-and-future-sponsorship", Labels: func(ls []string) bool {
			if !anyLabel(ls, authWithoutSponsorRE) {
				return false
			}
			for _, l := range ls {
				if requiresSponsorRE.MatchString(l) && !willNotRequireRE.MatchString(l) && !authWithoutSponsorRE.MatchString(l) {
					return true
				}
			}
			return false
		}},
		// Method (Ashby), Treasury Prime (Greenhouse): "Employment Status"
		// over an authorization and a search for sponsorship states the
		// standing in its two options.
		{Target: "work-auth-and-future-sponsorship", Labels: func(ls []string) bool {
			return len(ls) == 2 && anyLabel(ls, authUSRE) && anyLabel(ls, seekingSponsorRE)
		}},
		// Popl (Ashby): "Will you now or in the future require sponsorship
		// for employment visa status?" over "I require assistance
		// immediately.", "I am legally authorized to work in the USA." and "I
		// require assistance in the future.": the need now, only later, or
		// none, the aid the sponsorship the question names.
		{Target: "work-auth-and-future-sponsorship", Labels: func(ls []string) bool {
			return anyLabel(ls, aidNowRE) && anyLabel(ls, aidLaterRE) && anyLabel(ls, authUSRE)
		}, Text: func(q string) bool { return sponsorOrVisaRE.MatchString(q) }},
		// Circleback (Ashby): "Are you authorized to work in the United
		// States?" over "Yes" and "Yes, but I will need visa sponsorship in
		// the future" qualifies the Yes with the sponsorship standing.
		{Target: "work-auth-and-future-sponsorship", Labels: func(ls []string) bool {
			return anyLabel(ls, bareYesRE) && anyLabel(ls, yesButNeedRE)
		}},
		// Notion (Ashby): "Will you now or at any time in the future require
		// sponsorship for employment visa status (e.g. H1B, OPT)?" over OPT, H1B,
		// TN, None and Other asks which status the sponsorship is for.
		{Target: "us-work-authorization-status", Labels: func(ls []string) bool {
			n := 0
			for _, l := range ls {
				if visaCodeLabel(l) {
					n++
				}
			}
			return n >= 2
		}, Text: func(q string) bool { return sponsorOrVisaRE.MatchString(q) }},
		// A state the list leaves out (an employer that does not hire there, or a
		// recorded shape cut to its first options) is no other entry's answer.
		{Target: "address-state", Labels: usStateList, Text: func(q string) bool {
			return residenceStateRE.MatchString(q) && !otherStateRE.MatchString(q)
		}, Owns: true},
		// Groupon: "Do you currently reside where this role is based, or would
		// you need to relocate?" over the countries asks the country of
		// residence.
		{Target: "address-country", Labels: countryList, Text: func(q string) bool {
			return resideAskRE.MatchString(q) && !residePreferRE.MatchString(q)
		}},
		{Target: "military-service-status", Labels: func(ls []string) bool {
			if anyLabel(ls, spouseRE) {
				return false
			}
			return anyLabel(ls, armedForcesRE) && anyLabel(ls, veteranOrgRE)
		}},
		// Elastic (Greenhouse): "How should we communicate with you?" over
		// "Email", "Phone Call", "Text Message", "WhatsApp".
		{Target: "marketing-text-consent", Labels: func(ls []string) bool {
			opts := make([]Option, len(ls))
			for i, l := range ls {
				opts[i] = Option{Label: l}
			}
			return channelList(opts)
		}, Text: func(q string) bool { return communicateAskRE.MatchString(q) }},
		{Target: "marketing-text-consent", Labels: func(ls []string) bool {
			optIn, optOut := false, false
			for _, l := range ls {
				if (optInRE.MatchString(l) && smsRE.MatchString(l)) || (consentReceiveRE.MatchString(l) && !negatedConsentRE.MatchString(l)) || textsOptIn(l) {
					optIn = true
				}
				if (smsRE.MatchString(l) && smsNoRE.MatchString(l)) || refuseConsentRE.MatchString(l) || textsOptOut(l) {
					optOut = true
				}
			}
			return optIn && optOut
		}},
		{Target: "highest-level-education", Labels: func(ls []string) bool {
			return countLabels(ls, degreeLevelRE) >= 3
		}, Text: func(q string) bool { return !moreSpecificEduRE.MatchString(q) }},
		{Target: "address-type", Labels: func(ls []string) bool {
			home := false
			for _, l := range ls {
				if l == "home" {
					home = true
				}
				if !addressTypeLabelRE.MatchString(l) {
					return false
				}
			}
			return home
		}, Text: func(q string) bool { return addressTypeTextRE.MatchString(q) }},
		{Target: "referral-source-specify", Labels: func(ls []string) bool {
			return countLabels(ls, jobBoardRE) >= 2
		}, Text: func(q string) bool { return pleaseSpecifyRE.MatchString(q) }},
		{Target: "eeo-transgender", Labels: func(ls []string) bool {
			return anyLabel(ls, cisRE) && anyLabel(ls, transRE) && !anyLabel(ls, bareGenderRE)
		}},
	}
}

// workAuthHeading is a bare work-authorization heading, for the United States
// or another named country (Ideogram: "Work authorization in Canada"); the
// posting-country guard then reads the country it names.
func workAuthHeading(q string) bool {
	if workAuthHeadingRE.MatchString(q) {
		return true
	}
	m := workAuthInRE.FindStringSubmatch(q)
	if m == nil {
		return false
	}
	_, ok := CountryCode(m[1])
	return ok
}

func signatureLabels(opts []Option, sc *optionScorer) []string {
	out := make([]string, len(opts))
	for i, o := range opts {
		out[i] = sc.plain(o.Label)
	}
	return out
}

// signatureCandidates lists the signatures that fire for a control, in table
// order, whose target entry exists.
func signatureCandidates(q Question, exists func(id string) bool, sc *optionScorer) []signature {
	if len(q.Options) == 0 {
		return nil
	}
	labels := signatureLabels(q.Options, sc)
	var out []signature
	for _, s := range formSignatures() {
		if sc != nil && sc.bud.over() {
			break // the request's result is the budget abstain
		}
		// The text condition reads one question; the labels may be hundreds.
		if !exists(s.Target) || s.Text != nil && !s.Text(q.Text) || !s.Labels(labels) {
			continue
		}
		out = append(out, s)
	}
	return out
}

// experienceTitleRE is a question that is only an experience title.
var experienceTitleRE = lazyregexp.New(`(?i)^\W*(?:(?:current|total|overall)\s+)?(?:(?:professional|work|industry|full[- ]time)\s+)?experience\W*$`)

var (
	// startAskRE asks when the applicant can start; startAskExcludeRE is a
	// start that follows an offer or opens a degree program.
	startAskRE        = lazyregexp.New(`(?i)\b(?:able|available)\s+to\s+(?:start|begin|join|commence)\b|\b(?:earliest|when)\b[^?.]{0,40}\b(?:start|begin|commence)\b`)
	startAskExcludeRE = lazyregexp.New(`(?i)` + rxbProgramStart + `|\boffer(?:s|ed|ing)?\b`)
	leadRangeRE       = lazyregexp.New(`^\d+\s*(?:-|to)\s*\d+`)
	leadNumberRE      = lazyregexp.New(`(?i)^(?:(\d+)(?:\s*(?:-|to)\s*\d+)?|(a|an|` + countWords + `))\b\s*[+]?\s*(day|week|month|year)s?\b`)
	// leadTimeMaxWords bounds a label read as "start immediately".
	leadTimeMaxWords = 6
	immediateRE      = lazyregexp.New(`(?i)\b(?:immediately|immediate|asap|right away|now)\b`)
)

// parseLeadTime reads an availability label such as "Two weeks notice" or
// "1 month" as a lead time in days; "immediately" is 0.
func parseLeadTime(label string) (days int, ok bool) {
	l := strings.ToLower(strings.TrimSpace(label))
	// "Immediately" or "Available now" is a lead time; a sentence that says
	// "now or in the future" about visa sponsorship is not.
	if immediateRE.MatchString(l) && len(strings.Fields(l)) <= leadTimeMaxWords {
		return 0, true
	}
	l = strings.TrimPrefix(l, "in ")
	m := leadNumberRE.FindStringSubmatch(l)
	if m == nil {
		return 0, false
	}
	n := 0
	if m[1] != "" {
		for _, c := range m[1] {
			n = n*10 + int(c-'0')
		}
	} else if n = numberWords[m[2]]; n == 0 {
		// "a" or "an".
		n = 1
	}
	if m[3] == "year" && leadRangeRE.MatchString(l) {
		// "0-1 years" is an experience bucket, not an availability.
		return 0, false
	}
	switch m[3] {
	case "day":
		return n, true
	case "week":
		return n * 7, true
	case "month":
		return n * 30, true
	case "year":
		return n * 365, true
	}
	return 0, false
}
