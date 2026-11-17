package match

import (
	"fmt"
	"strings"

	"applysmith/internal/lazyregexp"
)

// deriveSkillChecklist answers skill-checklist: the options of the list that
// name a skill the profile records, each by its whole name, so a Java skill
// never ticks JavaScript and C# never C++, and the options the user's own
// information names whole (Binti's React, on the résumé's skills line; the
// bank's entries are not read, so an example question names nothing). A list
// none of whose options the user's information names is left to the user.
func deriveSkillChecklist(s *Step, e *Entry) (Draft, bool) {
	opts := realOptions(s.Q.Options)
	if len(opts) == 0 && s.Q.Kind == KindCustomListbox && s.Q.InputType == InputMultiSelect {
		// A search prompt that collects skills (Workday's "Type to Add Skills")
		// shows no list: each recorded skill is searched and picked in turn.
		var names []string
		for _, sk := range s.Req.Facts.Skills {
			if n := strings.TrimSpace(sk.Name); n != "" {
				names = append(names, n)
			}
		}
		if len(names) == 0 {
			return abstainDraft(ReasonPlaceholderEmpty, "", "%s: no profile skill is recorded", e.ID), true
		}
		return entryDraft(strings.Join(names, ", "), "profile skills searched in the prompt"), true
	}
	if len(opts) == 0 {
		return abstainDraft(ReasonInputMismatch, "", "%s answers a list of options", e.ID), true
	}
	skills := map[string]string{}
	for _, sk := range s.Req.Facts.Skills {
		if k := skillKey(sk.Name); k != "" {
			skills[k] = sk.Name
		}
	}
	var chosen, named []string
	for _, o := range opts {
		if name, ok := skills[skillKey(o.Label)]; ok {
			chosen, named = append(chosen, o.Label), append(named, name)
		} else if len([]rune(strings.TrimSpace(o.Label))) >= 3 && len(s.ownMentions("", "", []string{o.Label}, false).Found) > 0 {
			// A name of one or two letters ("Go", "R") is a word of any
			// sentence; only the profile's skills tick it. The search is given
			// no question, so the only hit is the option's own name: initials
			// the question spells and notes ranked near it name no option.
			chosen, named = append(chosen, o.Label), append(named, o.Label+" (the user's information)")
		}
	}
	if len(chosen) == 0 {
		return abstainDraft(ReasonNoEntry, "", "%s: no option names a recorded profile skill", e.ID), true
	}
	d := entryDraft(strings.Join(chosen, ", "), "profile skills ["+strings.Join(named, ", ")+"] -> ["+strings.Join(chosen, ", ")+"]")
	d.HasOption, d.OptionScore, d.ChosenLabel, d.ChosenLabels = true, 1, chosen[0], chosen
	return d, true
}

// skillKey is a skill name's comparable form: its symbol-bearing tokens joined,
// with any parenthesised gloss dropped ("Node.js" and "NodeJS" are one key).
func skillKey(name string) string {
	return strings.Join(symbolBearingTokens(parenRE.ReplaceAllString(name, " ")), "")
}

var (
	// eventsAskRE asks which of a list of recruiting events the applicant
	// attended or will attend (P&G's "Did you attend or will you be attending
	// any of the following conferences/career fairs?").
	eventsAskRE = lazyregexp.New(`(?i)\b(?:did|will|have|are)\s+you\b[^?]*\b(?:attend\w*|participat\w*)\b[^?]*\bof\s+the\s+following\s+(?:\w+\s+){0,2}(?:conferences?|career\s+fairs?|events?|expos?|hackathons?|summits?|info(?:rmation)?\s+sessions?)\b`)
	// noneAttendedRE is the list's option for none of the events.
	noneAttendedRE = lazyregexp.New(`(?i)^\s*(?:not\s+applicable|n/?a|none(?:\s+of\s+(?:the\s+)?(?:above|these))?|no|i\s+(?:did|have|will)\s+not\s+attend\w*[^,;]*)\s*\.?\s*$`)
	// eventPartRE splits an event label into its names: "SACC - Service
	// Academy Career Conference", "Out for Undergrad (O4U)".
	eventPartRE = lazyregexp.New(`\s+[-\x{2013}/]\s+|[()]`)
)

// eventsAttended answers a list of conferences or career fairs the applicant
// attended or will attend with the list's option for none when none of the
// user's information names a listed event, each by its whole name or its
// initials (F-10). An event the information names, or a knowledge base not
// read whole, leaves the list to the user, who alone knows whether they went.
func eventsAttended(s *Step) (Draft, bool) {
	if !eventsAskRE.MatchString(s.Q.Text) {
		return Draft{}, false
	}
	var none []Option
	var events, terms []string
	for _, o := range realOptions(s.Q.Options) {
		if noneAttendedRE.MatchString(o.Label) {
			none = append(none, o)
			continue
		}
		events = append(events, o.Label)
		for _, part := range eventPartRE.Split(o.Label, -1) {
			if part = strings.TrimSpace(part); part != "" {
				terms = append(terms, strings.ToLower(part))
			}
		}
	}
	if len(none) != 1 || len(events) == 0 {
		return Draft{}, false
	}
	found := s.ownMentions(s.Q.Text, strings.Join(events, "\n"), terms, true)
	if !found.Absent() {
		return abstainDraft(ReasonAmbiguous, "", "events attended: your information names %v or was not read whole -> left for you", found.Found), true
	}
	d := derivedDraft(none[0].Label, "", startProfileDerive, fmt.Sprintf("events attended: none of your information names any of %d listed events -> %q (F-10)", len(events), none[0].Label))
	d.ChosenLabel, d.ChosenLabels, d.HasOption, d.OptionScore = none[0].Label, []string{none[0].Label}, true, 1
	d.Deterministic = true
	return d, true
}
