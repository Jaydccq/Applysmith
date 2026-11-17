package match

import (
	"slices"
	"strconv"
	"strings"

	"applysmith/internal/lazyregexp"
)

// Option-level resolvers of the location family: the branch pick of a
// "located in X or willing to relocate" option set, the residence-claim guard
// and the office picker ranked by the policy.

// rxbCurrentlyIn is a presence clause with no verb (Gamma: "Yes, I'm currently
// in the Bay Area"); the place starts with a capital, so "currently in school"
// or "in the process of moving" is no presence.
const rxbCurrentlyIn = `\b(?:currently|presently)\s+in\s+(?:the\s+)?(?-i:\p{Lu})`

var (
	presenceOptionRE   = lazyregexp.New(`(?i)` + rxbPresence + `|\balready\s+in\b|` + rxbCurrentlyIn)
	relocationOptionRE = lazyregexp.New(`(?i)` + rxbWilling + `|\b(?:willing|open)\s+to\s+relocation\b|\b(?:would|will)\s+be\s+relocating\b|\b(?:plan|planning|intend|intending)\s+to\s+relocate\b|\b(?:willing|open|plan|planning|intend|intending)\s+to\s+relo\b`)
	negatedPresenceRE  = lazyregexp.New(`(?i)\b(?:not|never)\s+(?:` + rxbPresence + `|` + rxbCurrentlyIn + `)`)
	// statedRelocationRE states that taking the role means moving (HappyRobot:
	// "Yes, but I don't live near an office and need to relocate."; Brex: "Yes,
	// I'd relocate prior to the start of the role").
	statedRelocationRE = lazyregexp.New(`(?i)\b(?:need\s+to|I(?:['\x{2019}](?:d|ll)|\s+would|\s+will|\s+can|\s+could))\s+relocate\b`)
	// currentLocationAskRE asks the applicant to confirm or state where they
	// live now (Gusto: "Please confirm your current long-term location and
	// ability to work in this hybrid schedule"); placedYesRE is a Yes that
	// names a place after it ("Yes - San Francisco").
	currentLocationAskRE = lazyregexp.New(`(?i)\b(?:confirm|verify|state|provide|share|tell\s+us)\b[^?.]{0,40}?\byour\s+(?:current\s+)?(?:long[- ]term\s+)?(?:location|residence)\b`)
	placedYesRE          = lazyregexp.New(`(?i)^\W*yes\s*[-\x{2013}\x{2014}:,(]\s*\S`)
	leadingYesRE         = lazyregexp.New(`(?i)^\W*yes\b`)
	plainYesRE           = lazyregexp.New(`(?i)^\s*yes\b[^a-z]*$`)
	plainNoRE            = lazyregexp.New(`(?i)^\s*no\b[^a-z]*$`)
	noButRE              = lazyregexp.New(`(?i)^\W*no\s*[,;:\-\x{2013}\x{2014}]?\s*but\b`)
	ordinalPreference    = lazyregexp.New(`(?i)\b(second|2nd|third|3rd)\s+(?:work\s+)?location\s+preference\b`)
)

// isLocatedOption reports the "located" branch: a presence clause that is not
// itself negated. "While I do not currently live in X, I am open to relocation"
// says the applicant is NOT there.
func isLocatedOption(label string) bool {
	return presenceOptionRE.MatchString(label) && !negatedPresenceRE.MatchString(expandContractions(label))
}

// isRelocationOption reports a willingness clause, the noun form and a stated
// plan included (Scopely: "Yes – I would be relocating for this role"), the
// move shortened too (CharterUP: "No, but I am planning to relo before
// starting").
func isRelocationOption(label string) bool { return relocationOptionRE.MatchString(label) }

// isNegatedRelocationOption reports a relocation clause under negation ("I am
// not willing to relocate"), the negative branch. The negated presence clause
// beside it is cut out first (relocationClause), so its "not" is never read as
// negating the relocation.
func isNegatedRelocationOption(label string) bool {
	return !isLocatedOption(label) && relocationOptionRE.MatchString(label) && hasNegationToken(relocationClause(label))
}

// isStatedRelocationOption reports a stated move that nothing negates, an
// affirmative branch: the applicant takes the role and moves for it. The
// negated presence clause is cut out first, as for the negative branch; "I do
// not need to relocate" is no branch at all.
func isStatedRelocationOption(label string) bool {
	return !isLocatedOption(label) && statedRelocationRE.MatchString(label) && !hasNegationToken(relocationClause(label))
}

// relocationClause is the label without its negated presence clause and,
// when there is one, without the leading No that answers it: the No denies
// the presence the clause spells out (Harvey: "No, I'm not based in this
// location but willing to relocate"), never the move. A leading "No, but"
// denies the presence the question asks about and turns to the move
// (CharterUP: "No, but I am planning to relo before starting").
func relocationClause(label string) string {
	text := expandContractions(label)
	if noButRE.MatchString(text) {
		return noButRE.ReplaceAllString(text, " ")
	}
	if !negatedPresenceRE.MatchString(text) {
		return text
	}
	return leadingNoRE.ReplaceAllString(strings.TrimSpace(negatedPresenceRE.ReplaceAllString(text, " ")), " ")
}

// deniesPresence reports an option that says the applicant is not at the
// place: a negated presence clause or a leading "No, but".
func deniesPresence(label string) bool {
	text := expandContractions(label)
	return noButRE.MatchString(text) || negatedPresenceRE.MatchString(text)
}

// knownAway reports that the applicant is known not to be at the place: the
// home is recorded and the place is one the question names or the posting's
// own.
func knownAway(p *RelocationPolicy, text string, j *jobPlace) bool {
	if p == nil || p.Home == (Home{}) {
		return false
	}
	v := currentlyAt(p, text, j)
	return v == verdictNo || v == verdictUnknown && j != nil && !j.Remote && j.finest() != ""
}

// locationBranchPick picks the honest branch of a located / relocate / no option
// set: the located option when the applicant is at the place, else the
// affirmative option when they would work there, else the negative option.
// Exactly one located and one affirmative option are required and at most one
// negative; any other shape (a city picker, two presence options) is not a
// branch set.
func locationBranchPick(s *Step, e *Entry, pol *RelocationPolicy, job *jobPlace) (Draft, bool) {
	located, affirmative, negative, ok := branchSet(s.Q.Options)
	if !ok {
		return Draft{}, false
	}
	pick := func(j *jobPlace) *Option {
		switch {
		case currentlyAt(pol, s.Q.Text, j) == verdictYes:
			return &located[0]
		case willingToWorkAt(pol, s.Q.Text, j) == verdictYes:
			// The relocation-assistance entry takes the relocating branch only
			// when it says Yes: Scopely's reads "would need relocation support".
			if need, _ := relocationAssistanceNeed(s); e.ID == IDRelocationAssistance && !need {
				return nil
			}
			// An affirmative that denies the presence says the applicant is
			// away, which must be known (BILL's "No, but I am open to
			// relocating to an eligible state" beside states it never lists).
			if deniesPresence(affirmative[0].Label) && !knownAway(pol, s.Q.Text, j) {
				return nil
			}
			return &affirmative[0]
		case len(negative) == 1:
			return &negative[0]
		}
		return nil
	}
	chosen := pick(job)
	if chosen == nil {
		return Draft{}, false
	}
	b := optionBinding{Label: chosen.Label, Value: chosen.Value, Score: 1}
	d := optionDraft(b, e, SourceDerived, true)
	d.Start = startProfileDerive
	if job != nil {
		if base := pick(nil); base == nil || base.Label != chosen.Label {
			d.Trace = append(d.Trace, "location-policy: the posting location "+quoteTerm(job.finest())+" decided the branch pick "+quoteTerm(chosen.Label))
		}
	}
	d.Trace = append(d.Trace, "location-policy branch pick: "+quoteTerm(chosen.Label)+
		" (located="+quoteTerm(located[0].Label)+", affirmative="+quoteTerm(affirmative[0].Label)+")")
	return d, true
}

// branchSet sorts the options into the located, affirmative and negative
// branches; ok is false unless there is exactly one located and one affirmative
// option and at most one negative one.
func branchSet(opts []Option) (located, affirmative, negative []Option, ok bool) {
	for _, o := range opts {
		if isPlaceholderOption(o.Label) {
			continue
		}
		switch {
		case isLocatedOption(o.Label):
			located = append(located, o)
		case isNegatedRelocationOption(o.Label):
			negative = append(negative, o)
		case isRelocationOption(o.Label) || isStatedRelocationOption(o.Label) || plainYesRE.MatchString(o.Label):
			affirmative = append(affirmative, o)
		case plainNoRE.MatchString(o.Label):
			negative = append(negative, o)
		}
	}
	return located, affirmative, negative, len(located) == 1 && len(affirmative) == 1 && len(negative) <= 1
}

// isBranchSet reports a located / affirmative / negative option set.
func isBranchSet(opts []Option) bool {
	_, _, _, ok := branchSet(opts)
	return ok
}

var (
	// localRE is an option's claim to live within commuting distance ("I'm
	// local to X", "within a commutable distance of X", Pigment's "I live a
	// commutable distance from"), notLocalRE its denial (Pigment's "My local
	// office is too far for a regular commute") and noOnsiteRE a local option's
	// refusal to work on site.
	localRE    = lazyregexp.New(`(?i)\blocal\b|\b(?:within|live)\s+(?:a\s+)?commut(?:able|ing)\s+distance\b`)
	notLocalRE = lazyregexp.New(`(?i)\bnot\s+(?:local|within\s+(?:a\s+)?commut(?:able|ing)\s+distance)\b|\btoo\s+far\s+(?:away\s+)?(?:for|to)\s+(?:a\s+)?(?:regular\s+|daily\s+)?commut(?:e|ing)\b`)
	noOnsiteRE = lazyregexp.New(`(?i)\b(?:can\s+not|unable\s+to|not\s+(?:able|willing)\s+to)\s+(?:work|commute|come)\b[^.]*?\bon-?site\b`)
	// commuteQuestionRE asks whether the applicant lives within commuting
	// distance of the places the question names.
	// Socure: "within 45 miles of one of our hubs" is a commuting reach too,
	// a distance under a hundred miles.
	// MotherDuck: "located in commuting distance to one of our hubs".
	// Cockroach Labs: "confirm you live near our NYC office".
	commuteQuestionRE = lazyregexp.New(`(?i)\b(?:within|in)\s+(?:a\s+)?commut(?:able|ing)\s+distance\b|\bwithin\s+(?:a\s+)?\d{1,2}[\s-]*(?:miles?|mi|km|kilomet(?:er|re)s?)\s+(?:radius\s+)?(?:of|from)\b|` +
		`\b(?:live|reside|located|based)\s+near\b`)
	// outsideSelectRE is the page's word on what one beyond its reach selects
	// (SecurityScorecard: "(If you live outside that range, please select
	// 'Yes' — this requirement does not apply to you.)"): group 1 the option.
	outsideSelectRE = lazyregexp.New(`(?i)\bif\s+you\s+(?:live|reside|are\s+(?:located|based))\s+(?:outside|beyond)\b[^.?!()]{0,60}?[,;:]?\s*(?:please\s+)?(?:select|choose|pick|answer|check|mark)\s+["'‘’“”]?(yes|no)\b`)
	// hourDriveRE asks the same within an hour's drive (WeInfuse: "within
	// 1-hour driving distance of"), a reach no wider than a commute's.
	hourDriveRE = lazyregexp.New(`(?i)\bwithin\s+(?:an?|1|one)[\s-]+hour(?:['’]s)?\s+(?:driv(?:e|ing)|commut(?:e|ing))\b`)
	// postingPlaceRE points at the posting's own place instead of naming one
	// (Cirrus Logic: "the work location listed in the job posting").
	// relocationAcceptedRE is a condition that takes one who would relocate
	// in place of one who lives near (Steadily: "If you do not currently
	// reside near this role's office location, you must be willing to
	// relocate.").
	// Gemini asks it outright: "This role is required to be based near our
	// New York City, NY hub. Are you open to relocating if you're not
	// currently based there?"
	relocationAcceptedRE = lazyregexp.New(`(?i)\bif\s+you\s+(?:do\s+not|don['’]t|are\s+not)\s+(?:currently\s+)?(?:live|reside|located|based)\s+(?:near|within)\b[^.?!]*\b(?:must|need\s+to|will\s+need\s+to|should)\s+be\s+(?:willing|able|open)\s+to\s+relocate\b|` +
		`\b(?:are|would)\s+you\s+(?:be\s+)?(?:open|willing|able|prepared)\s+to\s+relocat(?:e|ing)\b[^.?!]*\?`)
	postingPlaceRE = lazyregexp.New(`(?i)\b(?:location|office|site)s?\s+(?:listed|stated|named|posted|specified|mentioned|shown)\b|\b(?:this|the)\s+(?:job|role|position|posting)(?:['’]s)?\s+(?:location|office)\b|\bthis\s+(?:office|location|site)\b`)
	// leadYesNoRE is an option that opens with Yes or No; relocateOptionRE an
	// option that plans or is willing to relocate ("No, but willing to
	// relocate", "Open to Relocating to NYC").
	leadYesNoRE = lazyregexp.New(`(?i)^\W*(yes|no)\b`)
	// notNearOptionRE is an option that says the applicant is near none of
	// the offices, in a sentence of its own (Ivo: "I'm not located near an Ivo
	// Office").
	notNearOptionRE  = lazyregexp.New(`(?i)^\W*(?:i['’]?m|i\s+am)\s+not\s+(?:located|based|living)\s+(?:near|close\s+to|within\s+commut\w+\s+distance\s+(?:of|to))\s+(?:an?y?|one\s+of)\b[^.?!]*\boffices?\W*$`)
	relocateOptionRE = lazyregexp.New(`(?i)\b(?:planning|plan|intend|intending|willing|open|able|prepared)\s+to\s+(?:re)?locat(?:e|ing|ion)\b`)
	// awayOptionRE is an option stating the applicant lives beyond the
	// question's reach (Verana: "I live more than 30 miles away from the SF
	// office.").
	awayOptionRE  = lazyregexp.New(`(?i)^\W*i\s+(?:live|reside|am\s+(?:located|based))\s+(?:more\s+than|over|outside|beyond|further\s+than|farther\s+than)\b`)
	notRelocateRE = lazyregexp.New(`(?i)\bnot\s+(?:\w+\s+){0,2}(?:planning|plan|intending|willing|open|able|prepared)\s+to\s+(?:re)?locat(?:e|ing|ion)\b|\b(?:unable\s+to|won['’]?t|will\s+not|cannot|can['’]?t)\s+(?:re)?locate\b`)
)

// commuteDistance answers the option set that sorts applicants by whether they
// live within commuting distance of the offices the question names
// (Greenhouse: "I'm not local to X or Y", "I'm local to X or Y and am willing to
// work onsite", "... and cannot work onsite") from the location policy: a home
// city that is one of the places is local, and willing to work on site when the
// policy accepts it; a home state beyond the reach of every place's state
// (usStateReach) is not local. Anything else, and an unconfigured policy, stays
// with the user (owner decision F-10).
func commuteDistance(s *Step) (Draft, bool) {
	pol := policyOf(s)
	if pol == nil {
		return Draft{}, false
	}
	var notLocal, local, refuse []Option
	for _, o := range s.Q.Options {
		l := expandContractions(o.Label)
		switch {
		case isPlaceholderOption(o.Label):
		case notLocalRE.MatchString(l):
			notLocal = append(notLocal, o)
		case localRE.MatchString(l) && noOnsiteRE.MatchString(l):
			refuse = append(refuse, o)
		case localRE.MatchString(l):
			local = append(local, o)
		default:
			return Draft{}, false
		}
	}
	if len(notLocal) != 1 || len(local) != 1 || len(refuse) > 1 {
		return Draft{}, false
	}
	named := namedPlacesFor(pol, s.Q.Text+"\n"+notLocal[0].Label)
	if len(named.Unrecognized) > 0 || len(named.all()) == 0 {
		return Draft{}, false
	}
	var chosen *Option
	var why string
	if at := slices.DeleteFunc(slices.Clone(named.Cities), func(c string) bool { return !isHome(pol, c) }); len(at) > 0 {
		switch {
		case slices.ContainsFunc(at, func(c string) bool { return acceptable(pol, c) }):
			chosen, why = &local[0], "the home city is one of the places and the policy accepts it"
		case len(refuse) == 1:
			chosen, why = &refuse[0], "the home city is one of the places and the policy refuses it"
		}
	} else if farFrom(pol, named) {
		chosen, why = &notLocal[0], "the home state is beyond commuting reach of every place"
	}
	if chosen == nil {
		return Draft{}, false
	}
	d := optionDraft(optionBinding{Label: chosen.Label, Value: chosen.Value, Score: 1}, &Entry{}, SourceDerived, true)
	d.Start = startProfileDerive
	d.Trace = []string{"commute distance: " + why + " -> " + quoteTerm(chosen.Label)}
	return d, true
}

// commuteYesNo answers the same question over options that lead with Yes or
// No (Hover: "Are you currently located within commuting distance to either of
// our offices in San Francisco or New York City?" over "Yes, San Francisco",
// "Yes, New York City", "No, but I am planning to relocate" and "No (I
// understand this may disqualify me ...)", or a bare Yes and No, with or
// beside a relocation option that leads with neither) by the same
// rule: the Yes that names the home city, or the one bare Yes, when the home
// city is one of the places; when the home state is beyond commuting reach of
// every place, the No that plans to relocate when the policy would work at one
// of them, else the plain No. A question that names no place but points at
// the posting's own ("the work location listed in the job posting") is asked
// of the posting's location. Anything else stays with the user.
func commuteYesNo(s *Step) (Draft, bool) {
	pol := policyOf(s)
	if pol == nil || !commuteQuestionRE.MatchString(s.Q.Text) {
		return Draft{}, false
	}
	named, where, placeText := namedPlacesFor(pol, s.Q.Text), "", s.Q.Text
	if len(named.all()) == 0 && postingPlaceRE.MatchString(s.Q.Text) {
		loc := s.Req.postingLocation()
		named, where, placeText = namedPlacesFor(pol, loc), " (the posting's "+quoteTerm(loc)+")", loc
	}
	if len(named.Unrecognized) > 0 || len(named.all()) == 0 {
		return Draft{}, false
	}
	var yes, relocate, no, away []Option
	for _, o := range realOptions(s.Q.Options) {
		switch m := leadYesNoRE.FindStringSubmatch(o.Label); {
		case m == nil && relocateOptionRE.MatchString(o.Label) && !notRelocateRE.MatchString(o.Label):
			// Cockroach Labs: "Open to Relocating to NYC within 3 months of
			// starting" beside a bare Yes and No.
			relocate = append(relocate, o)
		case m == nil && awayOptionRE.MatchString(o.Label):
			away = append(away, o)
		case m == nil:
			return Draft{}, false
		case strings.EqualFold(m[1], "yes"):
			yes = append(yes, o)
		case relocateOptionRE.MatchString(o.Label) && !notRelocateRE.MatchString(o.Label):
			relocate = append(relocate, o)
		default:
			no = append(no, o)
		}
	}
	if len(yes) == 0 || len(no) != 1 || len(relocate) > 1 {
		return Draft{}, false
	}
	if len(away) > 0 {
		return commuteAwayYes(s, pol, named, placeText, where, yes, relocate)
	}
	var chosen *Option
	why := "the home city is one of the places"
	if m := outsideSelectRE.FindStringSubmatch(s.Q.Text); m != nil {
		// The question is asked only of one within reach; one beyond it
		// selects what the page names, and one within it is asked a
		// willingness this lane does not hold.
		pick := yes
		if strings.EqualFold(m[1], "no") {
			pick = no
		}
		if len(pick) != 1 || slices.ContainsFunc(named.Cities, func(c string) bool { return isHome(pol, c) }) || !farFrom(pol, named) {
			return Draft{}, false
		}
		chosen, why = &pick[0], "the home state is beyond commuting reach of every place, and the page has one beyond it select "+quoteTerm(pick[0].Label)
	} else if home := slices.DeleteFunc(slices.Clone(named.Cities), func(c string) bool { return !isHome(pol, c) }); len(home) > 0 {
		for i, o := range yes {
			if places := namedPlacesFor(pol, o.Label); len(places.all()) == 0 && len(yes) == 1 || slices.ContainsFunc(places.Cities, func(c string) bool { return slices.Contains(home, c) }) {
				chosen = &yes[i]
				break
			}
		}
	} else if farFrom(pol, named) {
		chosen, why = &no[0], "the home state is beyond commuting reach of every place"
		accepts := relocationAcceptedRE.MatchString(s.Q.Text) && len(yes) == 1 && len(relocate) == 0
		if len(relocate) == 1 || accepts {
			switch willingToWorkAt(pol, placeText, nil) {
			case verdictYes:
				if accepts {
					chosen, why = &yes[0], why+", the question takes one willing to relocate, and the policy would work at one of them"
				} else {
					chosen, why = &relocate[0], why+" and the policy would work at one of them"
				}
			case verdictUnknown:
				return Draft{}, false
			}
		}
	}
	if chosen == nil {
		return Draft{}, false
	}
	d := optionDraft(optionBinding{Label: chosen.Label, Value: chosen.Value, Score: 1}, &Entry{}, SourceDerived, true)
	d.Start = startProfileDerive
	d.Trace = []string{"commute distance: " + why + where + " -> " + quoteTerm(chosen.Label)}
	return d, true
}

// commuteAwayYes answers a question asked of those within reach whose
// options state the distance for those beyond it (Verana: "Employees who live
// within 30 miles of our SF or NY office are required to work from the office
// up to 3 days per week. Are you able to work in this hybrid schedule?" over
// "I live more than 30 miles away from the SF office.", the same for NY, Yes
// and No). A home in one of the places answers Yes. A home beyond reach of
// every place answers Yes too, by the owner's decision, when the relocation
// policy would work at one of them and office attendance is Yes within its
// days: the applicant would move within reach. Anything else is left open.
func commuteAwayYes(s *Step, pol *RelocationPolicy, named namedPlaces, placeText, where string, yes, relocate []Option) (Draft, bool) {
	if len(yes) != 1 || len(relocate) > 0 {
		return Draft{}, false
	}
	why := "the home city is one of the places"
	if !slices.ContainsFunc(named.Cities, func(c string) bool { return isHome(pol, c) }) {
		att := entryNamed(s, IDOfficeAttendanceOK)
		if !farFrom(pol, named) || willingToWorkAt(pol, placeText, nil) != verdictYes || att == nil || !isYesLabel(ownText(att)) || att.thresholdExceeded(s.Q.Text) {
			return Draft{}, false
		}
		why = "owner decision: the home is beyond reach of every place, the policy would work at one of them and office attendance is Yes, so the applicant would live within reach"
	}
	d := optionDraft(optionBinding{Label: yes[0].Label, Value: yes[0].Value, Score: 1}, &Entry{}, SourceDerived, true)
	d.Start = startProfileDerive
	d.Trace = []string{"commute distance: " + why + where + " -> " + quoteTerm(yes[0].Label)}
	return d, true
}

// commuteOfficePick answers the same question asked as one pick among the
// offices, each option one place, with one option for none of them (WeInfuse:
// "Which of our offices do you live within 1-hour driving distance of?" over
// "Austin, TX", "Dallas, TX" and "N/A"), by the same rule: the office in the
// home city, and the none option when the home state is beyond commuting reach
// of every office. A none option split by relocation (MotherDuck: "No, but
// willing to relocate", "No, NOT willing to relocate") is chosen by the
// policy's willingness to work there; Ivo's none option is a sentence of its
// own ("I'm not located near an Ivo Office"). With no option for none (BetterUp's
// hubs), a home out of reach of every office takes the offices the relocation
// policy accepts, ranked as it ranks them (owner decision): the top one, or
// every one when several may be ticked. Anything else stays with the user.
func commuteOfficePick(s *Step) (Draft, bool) {
	pol := policyOf(s)
	multi := s.Q.Kind == KindCheckbox || s.Q.InputType == InputMultiSelect
	if pol == nil || !commuteQuestionRE.MatchString(s.Q.Text) && !hourDriveRE.MatchString(s.Q.Text) {
		return Draft{}, false
	}
	var none, relocate, home []Option
	var named namedPlaces
	offices := 0
	for _, o := range realOptions(s.Q.Options) {
		switch {
		case noneOptions[normalizeLabel(o.Label)], notRelocateRE.MatchString(o.Label), notNearOptionRE.MatchString(o.Label):
			none = append(none, o)
			continue
		case relocateOptionRE.MatchString(o.Label):
			relocate = append(relocate, o)
			continue
		}
		// A city the tables do not know beside its state (BetterUp's
		// "Arlington, VA: ...") is an office too, reached through its state.
		places := namedPlacesFor(pol, o.Label)
		city := ""
		if len(places.Unrecognized) == 0 && len(places.Cities) == 1 {
			city = places.Cities[0]
			named.Cities = append(named.Cities, city)
		} else if c, ok := unknownCityBesideState(officeHead(o.Label), places.States); ok && len(places.Unrecognized) == 0 && len(places.Cities) == 0 {
			city = c
		} else {
			return Draft{}, false
		}
		offices++
		named.States = append(named.States, places.States...)
		if isHome(pol, city) {
			home = append(home, o)
		}
	}
	var chosen *Option
	var why string
	switch {
	case len(none) == 0 && len(relocate) == 0 && len(home) == 0 && offices > 0 && beyond(pol, named, usStateNeighborhood):
		return relocationRankedOffices(s, pol, multi)
	case multi || len(none) != 1 || len(relocate) > 1 || offices == 0:
		return Draft{}, false
	case len(home) == 1:
		chosen, why = &home[0], "the home city is one of the offices"
	case len(home) == 0 && farFrom(pol, named):
		chosen, why = &none[0], "the home state is beyond commuting reach of every office"
		if len(relocate) == 1 {
			switch willingToWorkAt(pol, s.Q.Text, nil) {
			case verdictYes:
				chosen, why = &relocate[0], why+" and the policy would work at one of them"
			case verdictUnknown:
				return Draft{}, false
			}
		}
	default:
		return Draft{}, false
	}
	d := optionDraft(optionBinding{Label: chosen.Label, Value: chosen.Value, Score: 1}, &Entry{}, SourceDerived, true)
	d.Start = startProfileDerive
	d.Trace = []string{"commute distance: " + why + " -> " + quoteTerm(chosen.Label)}
	return d, true
}

// relocationRankedOffices picks among offices that are all out of reach by the
// relocation policy's ranking: the top acceptable office, or every acceptable
// one in rank order when several may be ticked. With none acceptable it stays
// with the user.
func relocationRankedOffices(s *Step, pol *RelocationPolicy, multi bool) (Draft, bool) {
	if d, ok := workStandingOffices(s); ok {
		return d, true
	}
	opts := realOptions(s.Q.Options)
	labels := make([]string, len(opts))
	for i, o := range opts {
		labels[i] = o.Label
	}
	var picked []Option
	for _, r := range rankRenderedPlaces(pol, labels) {
		if r.Acceptable {
			picked = append(picked, opts[r.Index])
		}
	}
	if len(picked) == 0 {
		return Draft{}, false
	}
	if !multi {
		picked = picked[:1]
	}
	chosen := make([]string, len(picked))
	for i, o := range picked {
		chosen[i] = o.Label
	}
	d := optionDraft(optionBinding{Label: picked[0].Label, Value: picked[0].Value, Score: 1}, &Entry{}, SourceDerived, true)
	d.Start = startProfileDerive
	if multi {
		d.Answer, d.ChosenLabels = strings.Join(chosen, ", "), chosen
	}
	d.Trace = []string{"commute distance: the home state is beyond commuting reach of every office and no option says none -> the relocation preference ranks " + quoteTerm(strings.Join(chosen, "; "))}
	return d, true
}

// officeHead is an office label without the street address after its colon
// ("Arlington, VA: 1300 17th St N, ...").
func officeHead(label string) string {
	head, _, _ := strings.Cut(label, ":")
	return head
}

// farFrom reports a home in the United States whose state is beyond the
// commuting reach of every named place's state. A place whose state the tables
// do not know, or a home state they do not know, is never far.
func farFrom(p *RelocationPolicy, named namedPlaces) bool { return beyond(p, named, usStateReach) }

// beyond reports a home in the United States whose state is outside the reach
// of every named place's state. The ranked office pick claims no distance, so
// it needs only that no office is in the home state or one beside it
// (usStateNeighborhood); a "not local" answer needs the commuting reach.
func beyond(p *RelocationPolicy, named namedPlaces, reach func(string) map[string]bool) bool {
	if c, ok := CountryCode(p.Home.Country); p.Home.Country != "" && (!ok || c != "US") {
		return false
	}
	home := stateCodeOf(p.Home.Region)
	if home == "" {
		return false
	}
	var states []string
	for _, c := range named.Cities {
		in := usCityStates[strings.ToLower(c)]
		if len(in) == 0 {
			return false
		}
		for _, st := range in {
			states = append(states, strings.ToUpper(st))
		}
	}
	for _, st := range named.States {
		if st = stateCodeOf(st); st == "" {
			return false
		}
		states = append(states, st)
	}
	return !slices.ContainsFunc(states, func(st string) bool { return reach(st)[home] })
}

// withoutUnearnedResidence removes the options that state where the applicant
// lives from a "Yes" answer, unless the profile's home place is that place. An
// option naming no recognizable place is not earned either: a residence is never
// inferred.
func withoutUnearnedResidence(pol *RelocationPolicy, job *jobPlace, opts []Option) ([]Option, []string) {
	var kept []Option
	var notes []string
	for _, o := range opts {
		if isLocatedOption(o.Label) && currentlyAt(pol, o.Label, job) != verdictYes {
			notes = append(notes, "residence option "+quoteTerm(o.Label)+" states where the applicant lives, which the profile does not hold -> rejected")
			continue
		}
		kept = append(kept, o)
	}
	return kept, notes
}

// refusalOptionRE is an option that opens with No or None (Rocket Money's
// offices end in "None").
var refusalOptionRE = lazyregexp.New(`(?i)^\W*(?:no|none)\b`)

// officePicker ranks the rendered options of the office picker by the policy
// instead of a hand-ranked list. A single-answer control takes the top
// acceptable option (the second or third for an ordinal preference), a
// multi-answer control ticks every acceptable option. With nothing acceptable
// it abstains: an office the applicant will not move to is never committed.
func officePicker(s *Step, e *Entry) (Draft, bool) {
	if e == nil || e.ID != IDPreferredWorkLocation || e.LocationPolicy == nil || e.LocationPolicy.Resolved == nil || len(s.Q.Options) == 0 {
		return Draft{}, false
	}
	pol := e.LocationPolicy.Resolved
	if d, ok := workStandingOffices(s); ok {
		return d, true
	}
	if d, ok := postingOfficePick(s, e, pol); ok {
		return d, true
	}
	if d, ok := relocatingOfficeOption(s, e, pol); ok {
		return d, true
	}
	labels := make([]string, len(s.Q.Options))
	for i, o := range s.Q.Options {
		labels[i] = o.Label
	}
	ranked := rankRenderedPlaces(pol, labels)
	multi := mselMultiAnswer(s.Q, len(s.Q.Options))
	availability := workAvailabilityAskRE.MatchString(s.Q.Text)
	var acceptableOpts []rankedOption
	var lines []string
	for _, r := range ranked {
		note := "refused"
		if r.Acceptable {
			note = "#" + strconv.Itoa(r.Rank)
			if r.Rank < 0 {
				note = "#-"
			}
			// "Select..." is a prompt and "No, but I'm open to a remote
			// position" a refusal, not an office: under open with nothing
			// listed either would otherwise tie for last place and win on order.
			// A move option is no office either: relocatingOfficeOption took
			// it when it is the answer.
			if !isPlaceholderOption(r.Label) && !refusalOptionRE.MatchString(r.Label) && !relocationNeedOptionRE.MatchString(r.Label) {
				// Willing to move there and allowed to work there are judged
				// apart: a question of willingness takes the
				// policy alone, and one of where the applicant can work refuses
				// an office in a country they may not work in, and in a list
				// claims none that nothing places in a country.
				var countries []string
				if availability {
					countries = officeCountries(pol, r.Label)
				}
				switch {
				case len(countries) > 0 && !slices.ContainsFunc(countries, func(c string) bool { return covers(s.Req.Facts.AuthorizedCountries, c) }):
					note = "not authorized in " + strings.Join(countries, "/")
				case availability && len(countries) == 0 && multi:
					note = "no country places it"
				default:
					acceptableOpts = append(acceptableOpts, r)
				}
			}
		}
		lines = append(lines, quoteTerm(r.Label)+"="+note+"("+r.Reason+")")
	}
	trace := "preferred-work-location: ranked " + strings.Join(lines, "; ")
	if len(acceptableOpts) == 0 {
		return abstainDraft(ReasonInputMismatch, "", "%s", trace+"; no rendered option is acceptable (default="+pol.Default+") -> abstain"), true
	}
	index := 0
	if m := ordinalPreference.FindStringSubmatch(s.Q.Text); m != nil {
		switch strings.ToLower(m[1]) {
		case "second", "2nd":
			index = 1
		default:
			index = 2
		}
	}
	if !multi && index >= len(acceptableOpts) {
		return abstainDraft(ReasonInputMismatch, "", "%s", trace+"; no acceptable option at preference "+strconv.Itoa(index+1)+" -> abstain"), true
	}
	picked := acceptableOpts
	if !multi {
		picked = acceptableOpts[index : index+1]
	}
	chosen := make([]string, len(picked))
	for i, r := range picked {
		chosen[i] = s.Q.Options[r.Index].Label
	}
	d := Draft{Entry: e, Source: SourceDerived, Start: startProfileDerive, Deterministic: true, HasOption: true, OptionScore: 1,
		Answer: strings.Join(chosen, ", "), ChosenLabel: chosen[0], Trace: []string{trace}}
	if multi {
		d.ChosenLabels = chosen
	} else {
		d.Answer = s.Q.Options[picked[0].Index].Value
	}
	return d, true
}

// relocationNeedOptionRE is an option that says the role needs a move
// (Cogent: "Yes, but I would require relocation").
var relocationNeedOptionRE = lazyregexp.New(`(?i)\b(?:would|will)\s+(?:require|need)\s+(?:a\s+)?relocation\b|\b(?:would|will)\s+need\s+to\s+relocate\b`)

// relocatingOfficeOption takes the one option that says the role needs a move
// when the recorded home is at none of the offices the other options name and
// the policy accepts one of them: an office option says the applicant works
// there as things stand (Cogent: "Yes, I can work in San Francisco" / "Yes, I
// can work in New York" / "Yes, but I would require relocation").
func relocatingOfficeOption(s *Step, e *Entry, pol *RelocationPolicy) (Draft, bool) {
	if pol.Home == (Home{}) {
		return Draft{}, false
	}
	var move []Option
	acceptable := false
	for _, o := range realOptions(s.Q.Options) {
		if relocationNeedOptionRE.MatchString(o.Label) {
			move = append(move, o)
			continue
		}
		for _, pl := range optionPlaces(pol, o.Label) {
			if isHome(pol, pl) {
				return Draft{}, false
			}
			acceptable = acceptable || readPlace(pol, pl).acceptable
		}
	}
	if len(move) != 1 || !acceptable {
		return Draft{}, false
	}
	d := Draft{Entry: e, Source: SourceDerived, Start: startProfileDerive, Deterministic: true, HasOption: true, OptionScore: 1,
		Answer: move[0].Value, ChosenLabel: move[0].Label,
		Trace: []string{"preferred-work-location: the recorded home is at none of the offices -> the option that needs a move " + quoteTerm(move[0].Label)}}
	if mselMultiAnswer(s.Q, len(s.Q.Options)) {
		d.Answer, d.ChosenLabels = move[0].Label, []string{move[0].Label}
	}
	return d, true
}

// workStandingOptionRE is an option that states the applicant's work standing
// beside its place (n8n: "I can be based in the UK and do not need visa
// support"): a visa, sponsorship, a work permit or an authorization to work.
var workStandingOptionRE = lazyregexp.New(`(?i)\b(?:visas?|sponsor\w*|work\s+permits?|work\s+authori[sz]ation|authori[sz]ed\s+to\s+work|right\s+to\s+work)\b`)

// workStandingOffices leaves a list of places whose options state a work
// standing: it asks where the applicant may work, a work-authorization
// question the relocation policy cannot answer (the open policy ticked every
// one, the UK and Germany "without visa support" too). The abstain is
// terminal, so no later lane takes it.
func workStandingOffices(s *Step) (Draft, bool) {
	if !slices.ContainsFunc(s.Q.Options, func(o Option) bool { return workStandingOptionRE.MatchString(o.Label) }) {
		return Draft{}, false
	}
	return abstainDraft(ReasonContextPolicy, "", "an option states a work standing beside its place (a visa, sponsorship or authorization to work): a work-authorization question, the user's own"), true
}

// workAvailabilityAskRE asks where the applicant can work ("In what cities are
// you available to work?"; Lightmatter: "the location you can commute or
// relocate to"), which the work authorization answers too; a question of
// willingness ("willing to relocate to", "prefer") does not.
var workAvailabilityAskRE = lazyregexp.New(`(?i)\b(?:available|able|eligible|authori[sz]ed|permitted|allowed)\s+to\s+work\b|\bcan\s+you\s+work\b|\byou\s+can\s+(?:work|commute|relocate|move)\b`)

// officeCountries are the countries an office option is in: the countries it
// names, and the United States for a US state or a city the US tables know;
// none when nothing places it ("Amsterdam", "Remote").
func officeCountries(p *RelocationPolicy, label string) []string {
	_, countries := scanCountries(label, false)
	n := namedPlacesFor(p, label)
	us := len(n.States) > 0 || slices.ContainsFunc(n.Cities, func(c string) bool {
		return len(usCityStates[strings.ToLower(c)]) > 0 || aliasCityIndex[aliasKey(c)] != nil
	})
	if us && !slices.Contains(countries, "US") {
		countries = append(countries, "US")
	}
	return countries
}

// postingOfficeAskRE asks which office the application is for, or to choose
// the one the posting lists (Zoox: "Which location are you applying for?").
var postingOfficeAskRE = lazyregexp.New(`(?i)\b(?:which|what)\s+(?:office\s+|work\s+|job\s+)?locations?\s+(?:are\s+you|you(?:['\x{2019}]re|\s+are))\s+applying\s+(?:for|to)\b|\bchoose\s+the\s+office\s+listed\b`)

// postingOfficePick answers a single-answer question that asks which office
// the application is for with the one option that names the posting's city,
// when the policy does not refuse that city. Any other case is the ranked pick.
func postingOfficePick(s *Step, e *Entry, pol *RelocationPolicy) (Draft, bool) {
	if !postingOfficeAskRE.MatchString(s.Q.Text) || mselMultiAnswer(s.Q, len(s.Q.Options)) {
		return Draft{}, false
	}
	job, ok := jobPlaceOf(s.Req.Posting)
	if !ok || job.Remote || strings.TrimSpace(job.City) == "" || !readPlace(pol, job.City).acceptable {
		return Draft{}, false
	}
	want := placeKeys(job.City)
	var hits []Option
	for _, o := range realOptions(s.Q.Options) {
		if slices.ContainsFunc(optionPlaces(pol, o.Label), func(pl string) bool { return keysIntersect(placeKeys(pl), want) }) {
			hits = append(hits, o)
		}
	}
	if len(hits) != 1 {
		return Draft{}, false
	}
	return Draft{Entry: e, Source: SourceDerived, Start: startProfileDerive, Deterministic: true, HasOption: true, OptionScore: 1,
		Answer: hits[0].Value, ChosenLabel: hits[0].Label,
		Trace: []string{"preferred-work-location: the question asks which office the application is for -> the option naming the posting's city " + quoteTerm(hits[0].Label)}}, true
}

// unearnedResidence reports an option that states where the applicant lives when
// the profile's home is not that place, for an entry bound to a location policy.
func unearnedResidence(e *Entry, label string) bool {
	if e == nil || e.LocationPolicy == nil || e.LocationPolicy.Resolved == nil {
		return false
	}
	return isLocatedOption(label) && currentlyAt(e.LocationPolicy.Resolved, label, nil) != verdictYes
}

var (
	// selfFundedOptionRE is a relocating option that moves without the
	// employer's help ("Ready to move (Self-Funded)", "do not require relocation
	// assistance"); assistedOptionRE one that asks for it ("Relocation
	// Assistance Required", "would need relocation support"). A self-funded
	// option may name the assistance it declines, so it is read first.
	selfFundedOptionRE = lazyregexp.New(`(?i)\bself[-\s]?funded\b|\brelocate\s+myself\b|\b(?:at|on)\s+my\s+own\s+(?:expense|cost)\b|\b(?:without|no)\s+(?:financial\s+|relocation\s+)?(?:assistance|support)\b|\b(?:do\s+not|don't|will\s+not|won't)\s+(?:require|need)\s+(?:any\s+)?(?:financial\s+|relocation\s+)?(?:assistance|support|package)\b`)
	assistedOptionRE   = lazyregexp.New(`(?i)\brelocation\s+(?:assistance|package|support|stipend)\b|\bassistance\s+(?:required|needed)\b`)
)

// officeBranchPick answers office-attendance-ok's Yes on an option set whose
// Yes options differ in where the applicant lives (HappyRobot: "Yes, and I
// already live in the location listed on the job.", "Yes, but I don't live
// near an office and need to relocate.", "No, I need to work remotely."): both
// accept the office, so the branch is the one the location policy picks
// against the posting, as for a relocation question. An office answer of No,
// a set of another shape and an office in no known place are the entry's
// ordinary pick.
func officeBranchPick(s *Step, e *Entry) (Draft, bool) {
	if e.ID != IDOfficeAttendanceOK || normalizeLabel(e.Answer) != "yes" || len(realOptions(s.Q.Options)) < 2 {
		return Draft{}, false
	}
	pol := policyOf(s)
	if pol == nil {
		return Draft{}, false
	}
	// Where the office is must be known: an applicant open to any place may live beside it.
	job := jobPtr(s.Req)
	if len(questionPlaces(pol, s.Q.Text)) == 0 && (job == nil || job.Remote || !job.known()) {
		return Draft{}, false
	}
	return locationBranchPick(s, e, pol, job)
}

// officeResidenceClaim keeps office-attendance-ok's Yes off an option that
// claims where the applicant lives: on a question that asks them to confirm
// their current location, a Yes that names a place says they live there
// (Gusto: "Yes - San Francisco"). When every Yes option names a place the
// profile's home is not, the question is left for the applicant, terminally,
// so no model lane answers it either: the Yes would claim a home they do not
// have, and the No would decline an office they may move to (Gusto: the
// grounded rung wrote the No).
func officeResidenceClaim(s *Step, e *Entry) (Draft, bool) {
	if e.ID != IDOfficeAttendanceOK || normalizeLabel(e.Answer) != "yes" || !currentLocationAskRE.MatchString(s.Q.Text) {
		return Draft{}, false
	}
	pol := policyOf(s)
	if pol == nil {
		return Draft{}, false
	}
	var yes, claims []string
	for _, o := range realOptions(s.Q.Options) {
		if !leadingYesRE.MatchString(o.Label) {
			continue
		}
		yes = append(yes, o.Label)
		if placedYesRE.MatchString(o.Label) && len(questionPlaces(pol, o.Label)) > 0 && currentlyAt(pol, o.Label, nil) != verdictYes {
			claims = append(claims, quoteTerm(o.Label))
		}
	}
	if len(yes) == 0 || len(claims) != len(yes) {
		return Draft{}, false
	}
	return abstainDraft(ReasonContextPolicy, DetailResidenceClaim, "the question asks where the applicant lives and every Yes (%s) names a place the profile's home is not: left for the applicant", strings.Join(claims, ", ")), true
}

// namedMetroHome answers based-in-named-metro with Yes when a place the
// question names is the recorded home (Gigs: the user's No holds for New York
// or San Francisco, never for the city the address names). Any other question
// keeps the entry's own answer.
func namedMetroHome(s *Step, e *Entry) (Draft, bool) {
	if e.ID != IDBasedInNamedMetro {
		return Draft{}, false
	}
	pol := policyOf(s)
	if pol == nil || currentlyAt(pol, s.Q.Text, nil) != verdictYes {
		return Draft{}, false
	}
	return bindEntryAnswer(s, e, "Yes", "named metro: a place the question names is the recorded home -> \"Yes\""), true
}

// namedMetroOrRelocate answers based-in-named-metro over a Yes that also takes
// one willing to relocate and a No that also refuses to (Orum: "Are you
// currently located in either the San Francisco Bay Area or Austin, Texas?"
// over "Yes or willing to relocate" and "No and not willing to relocate"),
// or over a Yes and two Noes split by relocation (Orkes: "Yes, I currently
// live in ...", "No, ... and I am open to relocation", "No, ... and am not
// open to relocation"): a named place that is home picks the Yes; one the
// policy would work at picks the Yes of two options or the willing No of
// three; neither picks the refusing No. Of three, the No that offers no move is
// the refusing one (Everlywell: "No, I am not in ATX or willing to work in
// office"), and the question may reach the relocation entry instead ("Are you
// able to work out of our Austin office? If you are not located in ATX, are
// you willing to relocate?"). A place outside the vocabulary, an unknown
// verdict and any other option set keep the entry's own answer.
func namedMetroOrRelocate(s *Step, e *Entry) (Draft, bool) {
	opts := realOptions(s.Q.Options)
	if e.ID != IDBasedInNamedMetro && e.ID != IDWillingToRelocate || len(opts) < 2 || len(opts) > 3 {
		return Draft{}, false
	}
	pol := policyOf(s)
	if pol == nil {
		return Draft{}, false
	}
	var yes, willing, refuses *Option
	for i, o := range opts {
		m := leadYesNoRE.FindStringSubmatch(o.Label)
		moves, stays := relocateOptionRE.MatchString(o.Label), notRelocateRE.MatchString(o.Label)
		switch {
		case m == nil:
			return Draft{}, false
		case strings.EqualFold(m[1], "yes") && !stays && (moves || len(opts) == 3):
			yes = &opts[i]
		case strings.EqualFold(m[1], "no") && stays:
			refuses = &opts[i]
		case strings.EqualFold(m[1], "no") && moves && len(opts) == 3:
			willing = &opts[i]
		case strings.EqualFold(m[1], "no") && len(opts) == 3 && refuses == nil:
			refuses = &opts[i]
		default:
			return Draft{}, false
		}
	}
	if len(opts) == 2 {
		willing = yes
	}
	named := namedPlacesFor(pol, s.Q.Text)
	if yes == nil || willing == nil || refuses == nil || len(named.Unrecognized) > 0 || len(named.all()) == 0 {
		return Draft{}, false
	}
	chosen, why := yes, "a named place is the recorded home"
	if currentlyAt(pol, s.Q.Text, nil) != verdictYes {
		switch willingToWorkAt(pol, s.Q.Text, nil) {
		case verdictYes:
			chosen, why = willing, "the policy would work at a named place"
		case verdictNo:
			chosen, why = refuses, "no named place is home and the policy would work at none"
		default:
			return Draft{}, false
		}
	}
	d := optionDraft(optionBinding{Label: chosen.Label, Value: chosen.Value, Score: 1}, e, SourceDerived, true)
	d.Start = startProfileDerive
	d.Trace = []string{"named metro or relocation: " + why + " -> " + quoteTerm(chosen.Label)}
	return d, true
}

// relocationStatusPick answers a status list that splits relocating by who pays
// for the move (Niantic: "Local", "Ready to move (Self-Funded)", "Relocation
// Assistance Required", "Remote Only"). The place must be known: the local
// option when the applicant is there, else, when they would work there, the
// option the relocation-assistance entry names. Any other case and any other
// list shape is left to the entry's own values.
func relocationStatusPick(s *Step, e *Entry) (Draft, bool) {
	if e == nil || e.ID != IDRelocationOrLocal {
		return Draft{}, false
	}
	pol := policyOf(s)
	if pol == nil {
		return Draft{}, false
	}
	var local, selfFunded, assisted []Option
	for _, o := range realOptions(s.Q.Options) {
		switch {
		case selfFundedOptionRE.MatchString(o.Label):
			selfFunded = append(selfFunded, o)
		case assistedOptionRE.MatchString(o.Label):
			assisted = append(assisted, o)
		case localRE.MatchString(o.Label) && !notLocalRE.MatchString(o.Label), isLocatedOption(o.Label):
			local = append(local, o)
		}
	}
	job := jobPtr(s.Req)
	known := len(questionPlaces(pol, s.Q.Text)) > 0 || job != nil && !job.Remote && job.known()
	if len(selfFunded) != 1 || len(assisted) != 1 || !known {
		return Draft{}, false
	}
	var chosen Option
	switch {
	case currentlyAt(pol, s.Q.Text, job) == verdictYes:
		if len(local) != 1 {
			return Draft{}, false
		}
		chosen = local[0]
	case willingToWorkAt(pol, s.Q.Text, job) == verdictYes:
		need, ok := relocationAssistanceNeed(s)
		if !ok {
			return Draft{}, false
		}
		chosen = selfFunded[0]
		if need {
			chosen = assisted[0]
		}
	default:
		return Draft{}, false
	}
	d := optionDraft(optionBinding{Label: chosen.Label, Value: chosen.Value, Score: 1}, e, SourceDerived, true)
	d.Start = startProfileDerive
	d.Trace = append(d.Trace, "relocation status: the policy and "+IDRelocationAssistance+" pick "+quoteTerm(chosen.Label))
	return d, true
}

// relocationBranchPick answers relocation-or-local on a located / relocate / no
// option set with the branch the policy picks (Gamma: "Yes, I'm currently in
// the Bay Area and open to 4–5 days onsite", "I'm not currently in the Bay
// Area but am open to relocation and 4-5 days onsite", "No, I am looking for
// remote opportunities"), whether or not the entry's own words bind an option.
// The place must be known, as for the office branch pick.
func relocationBranchPick(s *Step, e *Entry) (Draft, bool) {
	if e == nil || e.ID != IDRelocationOrLocal {
		return Draft{}, false
	}
	pol := policyOf(s)
	if pol == nil {
		return Draft{}, false
	}
	job := jobPtr(s.Req)
	if len(questionPlaces(pol, s.Q.Text)) == 0 && (job == nil || job.Remote || !job.known()) {
		return Draft{}, false
	}
	return locationBranchPick(s, e, pol, job)
}

// relocationOrLocalPick answers relocation-or-local from the policy: a status
// list split by who pays for the move, else a located / relocate / no set.
func relocationOrLocalPick(s *Step, e *Entry) (Draft, bool) {
	if d, ok := relocationStatusPick(s, e); ok {
		return d, true
	}
	return relocationBranchPick(s, e)
}

// relocationStatusAnswers reports a relocation-or-local option set the policy
// and the relocation-assistance entry answer, so a blank relocation-or-local
// entry is not a blank to abstain on.
func relocationStatusAnswers(s *Step) bool {
	_, ok := relocationOrLocalPick(s, s.Entry)
	return ok
}

// relocationAssistanceNeed is the relocation-assistance entry's answer for an
// applicant who relocates: the if_true of the policy it binds, else its own
// answer (the shipped entry maps only the not-relocating No, so the answer the
// user edits decides). known is false when it is neither Yes nor No.
func relocationAssistanceNeed(s *Step) (need, known bool) {
	for i := range s.Entries {
		e := &s.Entries[i]
		if e.ID != IDRelocationAssistance || e.Kind != KindFixed {
			continue
		}
		a := e.Answer
		if lp := e.LocationPolicy; lp != nil && lp.IfTrue != "" {
			a = lp.IfTrue
		}
		switch normalizeLabel(a) {
		case "yes":
			return true, true
		case "no":
			return false, true
		}
	}
	return false, false
}

var (
	// workModelAskRE asks which work model the applicant prefers (CloudZero:
	// "what is your preferred work model?") or is looking for (Agave: "Which
	// work arrangement are you looking for?") or is seeking (Valkyrie Trading:
	// "What type of work environment are you seeking?").
	workModelAskRE = lazyregexp.New(`(?i)\b(?:preferred|prefer|preference)\b[^?]{0,60}\bwork(?:ing)?\s+(?:model|mode|arrangement|style|setup|environment)\b|\bwork(?:ing)?\s+(?:model|mode|arrangement|style|environment)\s+preference\b|\bwork(?:ing)?\s+(?:model|mode|arrangement|style|setup|environment)s?\b[^?]{0,30}\b(?:looking\s+for|seeking)\b`)
	// noPreferenceRE is an option that prefers none of the modes it names.
	noPreferenceRE = lazyregexp.New(`(?i)\bno\s+(?:strong\s+)?preference\b|\bopen\s+to\s+(?:either|any|all|both)\b`)
	// ofTheAboveRE is an option whose modes are the other options' (Agave:
	// "Flexible / open to any of the above").
	ofTheAboveRE = lazyregexp.New(`(?i)\b(?:any|all|either)\s+of\s+the\s+(?:above|options)\b`)
	// workModeRE names a work mode: group 1 hybrid, 2 remote, 3 onsite.
	workModeRE = lazyregexp.New(`(?i)\b(hybrid)\b|\b(remote)\b|\b(on-?\s?site|in[- ]office|in[- ]person)\b`)
)

// workModeNoPreference answers a work-model preference with its one option
// that prefers none of the modes it names, when the recorded work modes hold
// every one of them: the profile accepts each alike. An option open to "any of
// the above" names the modes of the other options. Anything else is the
// user's preference.
func workModeNoPreference(s *Step) (Draft, bool) {
	if len(s.Req.Facts.WorkModes) == 0 || !workModelAskRE.MatchString(s.Q.Text) {
		return Draft{}, false
	}
	var picks []Option
	for _, o := range realOptions(s.Q.Options) {
		if noPreferenceRE.MatchString(o.Label) {
			picks = append(picks, o)
		}
	}
	if len(picks) != 1 {
		return Draft{}, false
	}
	named := workModeRE.FindAllStringSubmatch(picks[0].Label, -1)
	if ofTheAboveRE.MatchString(picks[0].Label) {
		named = nil
		for _, o := range realOptions(s.Q.Options) {
			if o.Label != picks[0].Label {
				named = append(named, workModeRE.FindAllStringSubmatch(o.Label, -1)...)
			}
		}
	}
	if len(named) < 2 {
		return Draft{}, false
	}
	for _, m := range named {
		if !recordedWorkMode(s, m) {
			return Draft{}, false
		}
	}
	d := optionDraft(optionBinding{Label: picks[0].Label, Value: picks[0].Value, Score: 1}, &Entry{}, SourceDerived, true)
	d.Start = startProfileDerive
	if s.Q.Kind == KindCheckbox {
		d.ChosenLabels = []string{picks[0].Label}
	}
	d.Trace = []string{"work model: the recorded work modes (" + strings.Join(s.Req.Facts.WorkModes, ", ") + ") hold every mode " + quoteTerm(picks[0].Label) + " names -> it"}
	return d, true
}

// workModeBoxes ticks, among boxes that each name one work mode (Valkyrie
// Trading: "What type of work environment are you seeking?" over In-person,
// Hybrid and Fully remote), those the recorded work modes hold. A box that
// names no mode, or more than one, leaves the question to the user.
func workModeBoxes(s *Step) (Draft, bool) {
	if s.Q.Kind != KindCheckbox || len(s.Req.Facts.WorkModes) == 0 || !workModelAskRE.MatchString(s.Q.Text) {
		return Draft{}, false
	}
	var picks []string
	for _, o := range realOptions(s.Q.Options) {
		named := workModeRE.FindAllStringSubmatch(o.Label, -1)
		if len(named) != 1 || noPreferenceRE.MatchString(o.Label) || ofTheAboveRE.MatchString(o.Label) {
			return Draft{}, false
		}
		if recordedWorkMode(s, named[0]) {
			picks = append(picks, o.Label)
		}
	}
	if len(picks) == 0 {
		return Draft{}, false
	}
	d := derivedDraft(strings.Join(picks, ", "), "", startProfileDerive,
		"work model: the boxes of the recorded work modes ("+strings.Join(s.Req.Facts.WorkModes, ", ")+") -> ["+strings.Join(picks, ", ")+"]")
	d.HasOption, d.OptionScore, d.ChosenLabel, d.ChosenLabels = true, 1, picks[0], picks
	return d, true
}

// recordedWorkMode reports a workModeRE match whose mode the recorded work
// modes hold.
func recordedWorkMode(s *Step, m []string) bool {
	mode := "onsite"
	switch {
	case m[1] != "":
		mode = "hybrid"
	case m[2] != "":
		mode = "remote"
	}
	return slices.ContainsFunc(s.Req.Facts.WorkModes, func(w string) bool { return strings.EqualFold(strings.TrimSpace(w), mode) })
}
