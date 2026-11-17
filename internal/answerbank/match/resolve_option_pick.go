package match

import (
	"fmt"
	"math"
	"slices"
	"strconv"
	"strings"

	"applysmith/internal/lazyregexp"
)

// Fallback confidences of the pick-option walk, each capped by the confidence
// of the stage that classified the entry.
const (
	startSourceCategory = 0.85
	startFirstOption    = 0.90
	startDeferred       = 0.90
	startEducation      = 0.90
	startResidence      = 0.90
)

// jobBoards is the alternation of the job boards the source patterns know, in
// lower case.
const jobBoards = `linkedin|indeed|glassdoor|ziprecruiter|monster|dice|handshake|simplyhired|careerbuilder|wellfound`

var (
	sourceChannelIDs = setOf("job-source", "referral-source")
	sourcePlatformRE = lazyregexp.New(`(?i)\b(?:linkedin|twitter|x\.com|facebook|instagram|tiktok)\b`)
	sourceCategoryRE = lazyregexp.New(`(?i)\bsocial\b.*\bnetwork`)
	// A job board named in the answer, and the category option that holds it
	// ("Internet Job Board / Job Search").
	sourceBoardRE    = lazyregexp.New(`(?i)\b(?:` + jobBoards + `)\b`)
	sourceBoardCatRE = lazyregexp.New(`(?i)\bjob\s*boards?\b|\bjob\s+search\b`)
	// The owner's rule: the source question never stops a run.
	// With no category for the answer, a general channel is taken: Other, then
	// for an online answer the company's own site, then the internet at large.
	sourceOtherRE         = lazyregexp.New(`(?i)^\s*other\s*(?:$|[(:/-])`)
	sourceWebsiteRE       = lazyregexp.New(`(?i)\bweb\s*site\b|\bcareers?\s+(?:site|page)\b`)
	sourceOnlineRE        = lazyregexp.New(`(?i)\b(?:internet|online)\b`)
	hispanicMentionRE     = lazyregexp.New(`(?i)\b(?:hispanic|latin[oax])\b`)
	bareDialCodeRE        = lazyregexp.New(`^\+\d{1,4}$`)
	raceEthnicitySiblings = map[string]string{"eeo-race": "eeo-ethnicity", "eeo-ethnicity": "eeo-race"}
)

// capStart caps a walk-ending confidence at the starting confidence of the stage
// that classified the entry, so a fuzzy route never reaches the automatic band
// through a fallback.
func capStart(s *Step, v float64) float64 {
	if s.Class.start > 0 && s.Class.start < v {
		return s.Class.start
	}
	return v
}

// deterministicRoute reports an entry reached by a curated example, a form
// signature, a pattern route or a verbatim example: the stages that established
// that the question really is this entry's question.
func deterministicRoute(s *Step) bool {
	switch s.Class.stage {
	case stageCuratedVerbatim, stageCuratedLong, stageSignature, stageRoute, stageVerbatim:
		return true
	}
	return false
}

// pickOptionStage resolves a pick-option entry: its ranked values are searched
// against the rendered options. A race or ethnicity entry that finds no option
// hands the control to its documented sibling.
func pickOptionStage(s *Step) (Draft, bool) {
	e := s.Entry
	if consentAffirmativeIDs[e.ID] {
		return acknowledgeStage(s)
	}
	if s.Q.Kind == KindText {
		return pickOptionTextDraft(s, e), true
	}
	if d, ok := travelYesNo(s, e); ok {
		return d, true
	}
	d := pickWalk(s, e)
	if d.Abstain == nil {
		return d, true
	}
	if od, ok := stateListOther(s, e); ok {
		return od, true
	}
	if sibling, ok := raceEthnicitySiblings[e.ID]; ok {
		for i := range s.Entries {
			se := &s.Entries[i]
			if se.ID != sibling || se.Kind != KindPickOption || se.RunContext {
				continue
			}
			if sd := pickWalk(s, se); sd.Abstain == nil {
				sd.Entry = se
				swap := fmt.Sprintf("entry %q found no matching option; sibling %q resolves: using it (race/ethnicity label conflation)", e.ID, sibling)
				sd.Trace = append(append(append([]string(nil), d.Trace...), swap), sd.Trace...)
				return sd, true
			}
		}
	}
	return d, true
}

// pickOptionTextDraft answers a pick-option entry on a free-text control with its
// fallback text.
func pickOptionTextDraft(s *Step, e *Entry) Draft {
	text, ok := pickOptionText(e)
	if !ok {
		return abstainWith(ReasonNoEntry, "pick-option entry on a text control has no fallback text or single preferred value to type: abstain")
	}
	answer, notes := finishTextAnswer(e, s.Q, text)
	// A number box takes a number: "in 1 Month" with no unit to count it in
	// (a bare "Notice period" box) is no answer to type there.
	if s.Q.InputType == InputNumber {
		if _, err := strconv.ParseFloat(strings.TrimSpace(answer), 64); err != nil {
			return abstainWith(ReasonInputMismatch, fmt.Sprintf("pick-option text %q is no number for a number box: abstain", answer))
		}
	}
	return Draft{Answer: answer, Deterministic: len(e.Preferred) == 1,
		Trace: append([]string{fmt.Sprintf("pick-option text control: typed text %q", text)}, notes...)}
}

// abstainWith ends the walk with an abstain and a trace line.
func abstainWith(reason Reason, line string) Draft {
	d := Draft{Abstain: &Abstain{Reason: reason}}
	if line != "" {
		d.Trace = []string{line}
	}
	return d
}

// searchValues are the ranked values: the preferred list, else the fallback text
// as the single implicit value.
func searchValues(e *Entry) []string {
	switch {
	case len(e.Preferred) > 0:
		return e.Preferred
	case e.FallbackText != "":
		return []string{e.FallbackText}
	}
	return nil
}

// expandSearch expands a ranked value into every label form it renders as: a
// time zone in all its spellings, a state with its other spelling, a recorded
// citizenship country with its nationality adjectives, a consent's Yes or No
// as Opt-In or Opt-Out. Recorded values stay
// first and duplicates are skipped; no other entry is expanded.
func expandSearch(id string, ranked []string) []string {
	switch id {
	case "time-zone":
		return expandTimeZoneAliases(ranked)
	case "address-state":
		var out []string
		for _, v := range ranked {
			out = appendDistinct(out, stateAliases(v)...)
		}
		return out
	case "country-of-citizenship":
		return expandCitizenshipSearch(ranked)
	case "aedt-consent", "future-communications-consent", "interview-recording-consent":
		// A recorded Yes or No to a consent is also the form's bare Opt-In or
		// Opt-Out (Visa). Not the AI-screening opt-out, whose Yes opts out.
		var out []string
		for _, v := range ranked {
			out = appendDistinct(out, v)
			switch normalizeLabel(v) {
			case "yes":
				out = appendDistinct(out, "Opt-In")
			case "no":
				out = appendDistinct(out, "Opt-Out")
			}
		}
		return out
	}
	return ranked
}

func appendDistinct(list []string, vals ...string) []string {
outer:
	for _, v := range vals {
		n := normalizeLabel(v)
		for _, have := range list {
			if normalizeLabel(have) == n {
				continue outer
			}
		}
		list = append(list, v)
	}
	return list
}

// pickMatrix scores every option against every search value once, applying the
// modifiers of the entry: an option contradicting the sponsorship fact scores 0,
// one contradicting the claimed education level scores 0, one naming an
// unclaimed discipline loses 0.20.
type pickMatrix struct {
	opts      []Option
	search    []string
	cells     [][]optScore
	unclaimed [][]bool
}

func newPickMatrix(s *Step, e *Entry, search []string) *pickMatrix {
	opts := s.Q.Options
	level, _ := highestEducationLevelIn(append(append([]string(nil), e.Preferred...), nonEmpty(e.FallbackText)...))
	req, known := sponsorshipFactOf(s)
	m := &pickMatrix{opts: opts, search: search, cells: make([][]optScore, len(opts)), unclaimed: make([][]bool, len(opts))}
	sc := s.scorer()
	// A suffix every option carries ("(United States of America)") tells none
	// apart: it is scored without, so it cannot close the lead of the answer.
	// A list that also states Hispanic or Latino status keeps it: that list is
	// answered from both recorded axes (compoundRaceDraft), never from one.
	shared := sharedTrailingParen(opts)
	for _, o := range opts {
		if hispanicMentionRE.MatchString(o.Label) {
			shared = ""
		}
	}
	for i, o := range opts {
		m.cells[i] = make([]optScore, len(search))
		m.unclaimed[i] = make([]bool, len(search))
		scored := o
		if shared != "" {
			scored.Label = strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(o.Label), shared))
		}
		for j, p := range search {
			raw := sc.score(scored, p)
			switch {
			// A state's code is never a country's ("SC" is Seychelles, "CA" Canada
			// by ISO code); only the code itself, as a state list shows it, is the state.
			case e.ID == "address-state" && countryEquivalent(o.Label, p) && !strings.EqualFold(strings.TrimSpace(o.Label), strings.TrimSpace(p)):
				raw = optScore{0, raw.Why + " - a state's code is not a country's"}
			case known && contradictsSponsorship(o.Label, req):
				raw = optScore{0, raw.Why + " - option contradicts the sponsorship fact"}
			case level > 0 && contradictsEducationLevel(level, o.Label):
				raw = optScore{0, raw.Why + " - contradicts claimed education level"}
			case level > 0 && namesUnclaimedDiscipline(p, o.Label):
				raw = optScore{math.Max(0, raw.Score-degreeSpecialisationPenalty), raw.Why + " - unclaimed discipline"}
				m.unclaimed[i][j] = true
			}
			m.cells[i][j] = raw
		}
	}
	return m
}

func nonEmpty(s string) []string {
	if s == "" {
		return nil
	}
	return []string{s}
}

// perPreferred is every option scored against one search value, sorted.
func (m *pickMatrix) perPreferred(j int) []scoredOption {
	out := make([]scoredOption, len(m.opts))
	for i, o := range m.opts {
		out[i] = scoredOption{o, i, m.cells[i][j]}
	}
	sortScored(out)
	return out
}

// bestAcross is the best score each option reaches against any search value,
// skipping a pairing that names an unclaimed discipline.
func (m *pickMatrix) bestAcross() []scoredOption {
	out := make([]scoredOption, len(m.opts))
	for i, o := range m.opts {
		out[i] = scoredOption{o, i, optScore{0, "page-time best across preferred"}}
		for j := range m.search {
			if !m.unclaimed[i][j] && m.cells[i][j].Score > out[i].Score {
				out[i].Score = m.cells[i][j].Score
			}
		}
	}
	sortScored(out)
	return out
}

// preferenceSlotRE reads the place a ranked question asks for: Citadel's "top
// two preferred coding languages" are two lists, "Preference 1" and
// "Preference 2".
var preferenceSlotRE = lazyregexp.New(`(?i)\b(?:preference|choice|priority)\s*#?\s*([1-9])\s*$|\b(second|third|2nd|3rd)\s+(?:preference|choice|priority|most\s+preferred)\b`)

// preferenceSlot is the place a ranked question asks for, 1 when it names none.
func preferenceSlot(text string) int {
	m := preferenceSlotRE.FindStringSubmatch(strings.TrimSpace(text))
	switch {
	case m == nil:
		return 1
	case m[1] != "":
		return int(m[1][0] - '0')
	case strings.HasPrefix(strings.ToLower(m[2]), "s"), m[2] == "2nd":
		return 2
	}
	return 3
}

// pickWalk walks the ranked values of one pick-option entry against the
// options of the question; a question that asks a later place takes the
// option that many distinct preferences down.
func pickWalk(s *Step, e *Entry) Draft {
	q := s.Q
	var tr []string
	note := func(f string, a ...any) { tr = append(tr, fmt.Sprintf(f, a...)) }
	draft := func(d Draft) Draft { d.Trace = append(tr, d.Trace...); return d }

	if d, ok := officePicker(s, e); ok {
		return draft(d)
	}
	if d, ok := relocationOrLocalPick(s, e); ok {
		return draft(d)
	}
	if d, ok := sponsorshipNeedFirst(s, e); ok {
		return draft(d)
	}
	ranked := searchValues(e)
	if neg := sponsorshipSearchOf(s, e); neg != nil {
		note("pick-option sponsorship negated-polarity: complemented the maintained fact with [%s]", strings.Join(neg, ", "))
		ranked = neg
	}
	search := expandSearch(e.ID, ranked)
	if len(e.Preferred) > 0 {
		note("pick-option preferred=[%s]", strings.Join(e.Preferred, ", "))
	} else {
		note("pick-option preferred=[]: using fallback_text %q as the implicit single preferred", e.FallbackText)
	}

	if !hasRenderedOptions(q.Options) && len(search) > 0 {
		chosen := search[0]
		for _, v := range search {
			if bareDialCodeRE.MatchString(strings.TrimSpace(v)) {
				chosen = v
				break
			}
		}
		note("closed-listbox short-circuit: no options reported: defer to the apply step with %q", chosen)
		// The rest of the ranking travels with it: a search that lists only what
		// holds the typed text finds nothing for "Python 3" and lists "Python"
		// (Applied Intuition's Ashby language search).
		var alts []string
		for _, v := range search {
			if !slices.ContainsFunc(append([]string{chosen}, alts...), func(x string) bool { return strings.EqualFold(x, v) }) {
				alts = append(alts, v)
			}
		}
		return draft(Draft{Answer: chosen, ChosenLabel: chosen, Alternatives: alts, Start: capStart(s, startDeferred)})
	}

	m := newPickMatrix(s, e, search)
	slot, taken := preferenceSlot(q.Text), map[int]bool{}
	if slot > 1 {
		note("the question asks for preference %d: the options earlier places take are skipped", slot)
	}
	for j, p := range search {
		best, bestScore, bestWhy := -1, 0.0, ""
		// A channel the user prefers names no one; an option naming a person who
		// referred or reached them would claim one (the owner's ruling).
		person := sourceChannelIDs[e.ID] && !personSourceRE.MatchString(p)
		for i := range m.opts {
			if person && personSourceRE.MatchString(m.opts[i].Label) {
				continue
			}
			if sc := m.cells[i][j]; sc.Score >= ReviewFillConfidence && sc.Score > bestScore {
				best, bestScore, bestWhy = i, sc.Score, sc.Why
			}
		}
		if best < 0 {
			continue
		}
		if taken[best] = true; len(taken) < slot {
			continue
		}
		chosen := m.opts[best]
		if raceEthnicityIDs[e.ID] {
			ranking := m.perPreferred(j)
			if margin := clearWinnerMargin(ranking); !leadsBy(margin, ClearWinnerLead) {
				note("sub-granular demographic option set: %q leads the next distinct value by %.2f < %.2f: terminal, no lane may assert an identity",
					chosen.Label, margin, ClearWinnerLead)
				d := Draft{Abstain: &Abstain{Reason: ReasonSelfIDPolicy}}
				return draft(d)
			}
		}
		// Rank decides the pick; the confidence is the best any search value
		// justifies for that same option, never a re-pick.
		for k := range search {
			if k != j && m.cells[best][k].Score > bestScore {
				bestScore, bestWhy = m.cells[best][k].Score, fmt.Sprintf("%s; via preferred %q", m.cells[best][k].Why, search[k])
			}
		}
		note("preferred %q -> option %q score=%.2f (%s)", p, chosen.Label, bestScore, bestWhy)
		ranking := m.perPreferred(j)
		return draft(Draft{Answer: chosen.Value, ChosenLabel: chosen.Label, HasOption: true, OptionScore: bestScore,
			OtherValueScore: otherValueScore(ranking)})
	}
	note("no preferred matched any option at or above %.2f", ReviewFillConfidence)
	if d, ok := residenceRegion(s, e, ranked); ok {
		return draft(d)
	}

	req, known := sponsorshipFactOf(s)
	if known {
		if d, ok := sponsorshipStandingOption(s, e, req); ok {
			return draft(d)
		}
	}
	if sourceChannelIDs[e.ID] {
		named := strings.Join(ranked, " ")
		for _, c := range [][2]*lazyregexp.Regexp{{sourcePlatformRE, sourceCategoryRE}, {sourceBoardRE, sourceBoardCatRE}} {
			if !c[0].MatchString(named) {
				continue
			}
			for _, o := range q.Options {
				if c[1].MatchString(o.Label) {
					note("no exact channel option rendered: truthful category %q", o.Label)
					return draft(Draft{Answer: o.Value, ChosenLabel: o.Label, Start: capStart(s, startSourceCategory)})
				}
			}
		}
		general := []*lazyregexp.Regexp{sourceOtherRE}
		if sourcePlatformRE.MatchString(named) || sourceBoardRE.MatchString(named) {
			general = append(general, sourceWebsiteRE, sourceOnlineRE)
		}
		for _, re := range general {
			for _, o := range q.Options {
				if re.MatchString(o.Label) {
					note("no channel category rendered: general channel %q", o.Label)
					return draft(Draft{Answer: o.Value, ChosenLabel: o.Label, Start: capStart(s, startSourceCategory)})
				}
			}
		}
	}
	if e.FallbackAnyOption && len(q.Options) > 0 {
		if ok, why := firstOptionAllowed(s, e); !ok {
			note("%s", why)
		} else if d, found := firstRealOption(s, req, known); found {
			return draft(d)
		}
	}

	if ranking := m.bestAcross(); isClearWinner(ranking) {
		win := ranking[0]
		note("option-margin: multi-preferred %q page-time clear winner %q=%.2f (margin >= %.2f): review fill without a model",
			e.ID, win.opt.Label, win.Score, ClearWinnerLead)
		return draft(Draft{Answer: win.opt.Value, ChosenLabel: win.opt.Label, HasOption: true, OptionScore: win.Score,
			OtherValueScore: otherValueScore(ranking)})
	}
	if d, ok := educationLadder(s, e, search); ok {
		return draft(d)
	}
	if label, ok := blockedWithdrawal(s, search); ok {
		note("destructive-option guard: %q would be the best option but withdraws the candidacy; only a verbatim answer can select it", label)
		return draft(Draft{Abstain: &Abstain{Reason: ReasonDestructiveOption}})
	}
	note("best option stays below the review band for every preferred value")
	return draft(Draft{Abstain: &Abstain{Reason: ReasonLowConfidence}})
}

// residenceRegion answers a residence menu that names some countries and
// gathers the rest under one option ("Germany", "Rest of World"): the country
// entry's value picks its country, else that rest-of-world option.
func residenceRegion(s *Step, e *Entry, ranked []string) (Draft, bool) {
	if e.ID != "address-country" || len(ranked) == 0 {
		return Draft{}, false
	}
	i := ResidenceOption(optionLabels(s.Q.Options), ranked[0])
	if i < 0 {
		return Draft{}, false
	}
	o := s.Q.Options[i]
	return Draft{Answer: o.Value, ChosenLabel: o.Label, Start: capStart(s, startResidence),
		Trace: []string{fmt.Sprintf("residence menu: every other option is another country -> %q", o.Label)}}, true
}

func optionLabels(opts []Option) []string {
	out := make([]string, len(opts))
	for i, o := range opts {
		out[i] = o.Label
	}
	return out
}

// blockedWithdrawal reports a withdrawing option that the destructive guard
// held back although it would have reached the review band: the only answer the
// walk could give would end the candidacy.
func blockedWithdrawal(s *Step, search []string) (string, bool) {
	sc := s.scorer()
	for _, o := range s.Q.Options {
		shared := sc.label(o)
		if !shared.withdrawal {
			continue
		}
		lp := *shared
		lp.withdrawal = false
		for _, p := range search {
			if scoreProfiles(&lp, sc.answer(p)).Score >= ReviewFillConfidence {
				return o.Label, true
			}
		}
	}
	return "", false
}

// firstOptionAllowed decides whether fallback_any_option may commit the first
// real option. The net's safety argument, "any choice is fine for THIS
// question", holds only if the question really is this entry's question, so it
// is refused on a self-identification set, on an entry reached by fuzzy
// phrasing, on a work-authorization entry and on a sensitive or disqualifying
// question.
func firstOptionAllowed(s *Step, e *Entry) (bool, string) {
	switch {
	case readSelfIDSetUsing(s.Q.Options, s.scorer()) != nil:
		return false, "self-id option set: first-option fallback refused (would assert a protected characteristic)"
	case !deterministicRoute(s):
		return false, "first-option fallback refused: entry reached by fuzzy phrasing match, not a deterministic route"
	case e.Category == CategoryWorkAuthorization:
		return false, "first-option fallback refused: work-authorization entry"
	case isSensitiveCategoryQuestion(s.Q.Text) || isDisqualifyingFactDisclosure(s.Q.Text):
		return false, "first-option fallback refused: sensitive or disqualifying question"
	}
	return true, ""
}

// personSourceRE is a source that names a person who referred or reached the
// applicant (Tenex: "Current Employee"; "Employee Referral", "Friend or
// family", "Recruiter reached out"; Workday's "Current Worker" and "Saw on
// personal LinkedIn feed", which asks whose post it was).
var personSourceRE = lazyregexp.New(`(?i)\b(?:referr(?:al|als|ed|er)|referral|employees?|friends?|family|colleagues?|co-?workers?|current\s+workers?|recruiters?|word\s+of\s+mouth|personal\s+(?:connection|network|contact|(?:\w+\s+)?feed)|someone'?s?\s+(?:i\s+know|post|feed))\b`)

// firstRealOption is the first option that is not a placeholder, does not
// contradict the sponsorship fact and, for a source, names no person who
// referred or reached the applicant: the net claims no referral.
func firstRealOption(s *Step, sponsorReq, sponsorKnown bool) (Draft, bool) {
	source := s.Entry != nil && s.Entry.Category == CategorySource
	for _, o := range s.Q.Options {
		if isScorePlaceholder(o.Label) || sponsorKnown && contradictsSponsorship(o.Label, sponsorReq) || source && personSourceRE.MatchString(o.Label) {
			continue
		}
		return Draft{Answer: o.Value, ChosenLabel: o.Label, Start: capStart(s, startFirstOption),
			Trace: []string{fmt.Sprintf("fallback_any_option: no ranked option present -> first real option %q", o.Label)}}, true
	}
	return Draft{}, false
}

// educationLadder is the last resort: both sides name the same rung of an
// education ladder in different words, which no lexical score sees.
func educationLadder(s *Step, e *Entry, search []string) (Draft, bool) {
	level, ok := highestEducationLevelIn(append(append([]string(nil), e.Preferred...), nonEmpty(e.FallbackText)...))
	if !ok {
		return Draft{}, false
	}
	rung, trace, ok := pickEducationRung(s.Q.Options, level, search)
	if !ok {
		if strings.Contains(trace, "names a field") {
			return Draft{Abstain: &Abstain{Reason: ReasonLowConfidence}, Trace: []string{trace}}, true
		}
		return Draft{}, false
	}
	return Draft{Answer: rung.Value, ChosenLabel: rung.Label, Start: capStart(s, startEducation), Trace: []string{trace}}, true
}
