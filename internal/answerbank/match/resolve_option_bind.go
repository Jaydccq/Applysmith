package match

import (
	"fmt"
	"strings"
)

// bindAnswerToOptions binds an answer an entry derived (a fixed answer, a
// profile fact) to the rendered options of a non-text control. It is the
// generic arm every resolver that ends in an option shares, and always handles
// the question: a draft that fills, or an abstain.
func bindAnswerToOptions(s *Step, e *Entry, answer string) Draft {
	q := s.Q
	norm := normalizeLabel(answer)
	if !hasRenderedOptions(q.Options) {
		return Draft{Answer: answer, ChosenLabel: answer,
			Trace: []string{fmt.Sprintf("closed-listbox short-circuit: no options reported but the entry has the answer %q: defer the option lookup to the apply step", answer)}}
	}
	if (norm == "yes" || norm == "no") && capabilityLadder(realOptions(q.Options)) {
		return Draft{Abstain: &Abstain{Reason: ReasonInputMismatch}, Trace: []string{fmt.Sprintf(
			"derived answer %q is a bare yes/no but the %d rendered options are an ascending capability ladder: abstain", answer, len(q.Options))}}
	}

	scored := scoreAll(s.scorer(), q.Options, answer)
	var tr []string
	rejected := false
	if e.LocationPolicy != nil && e.LocationPolicy.Resolved != nil && norm == "yes" {
		for i := range scored {
			if scored[i].Score != 0 && unearnedResidence(e, scored[i].opt.Label) {
				tr = append(tr, fmt.Sprintf("residence option %q states where the applicant lives, which the profile does not hold: rejected", scored[i].opt.Label))
				scored[i].optScore = optScore{0, "rejected: option asserts a residence the profile does not hold"}
				rejected = true
			}
		}
	}
	if cat, ok := categoryDichotomy(answer, q.Options); ok && !anyAtOrAbove(scored, ReviewFillConfidence) {
		for i := range scored {
			scored[i].optScore = optScore{0, "yes/no fact on a <category> | Other pair"}
			if scored[i].opt == cat {
				scored[i].optScore = optScore{0.9, "yes/no fact on a <category> | Other pair"}
			}
		}
	}
	sortScored(scored)
	var top []string
	for i := 0; i < len(scored) && i < 3; i++ {
		top = append(top, fmt.Sprintf("%q=%.2f(%s)", scored[i].opt.Label, scored[i].Score, scored[i].Why))
	}
	tr = append(tr, "top option scores: "+strings.Join(top, "; "))
	done := func(d Draft) Draft { d.Trace = append(tr, d.Trace...); return d }

	best := scored[0]
	fill := func(b scoredOption) Draft {
		return Draft{Answer: b.opt.Value, ChosenLabel: b.opt.Label, HasOption: true, OptionScore: b.Score,
			OtherValueScore: otherValueScore(scored), Deterministic: isDeterministicEntry(e)}
	}
	if best.Score < ReviewFillConfidence {
		if selectish(q) && len(q.Options) == 1 && e.Kind == KindFixed && e.allowsInput(InputText) && !e.allowsInput(q.InputType) {
			return done(Draft{Answer: answer, ChosenLabel: answer, Trace: []string{fmt.Sprintf(
				"placeholder typeahead: lone option %q scored %.2f (no match) and %q is a free-text entry: defer %q to the type-and-pick step",
				best.opt.Label, best.Score, e.ID, answer)}})
		}
		if isClearWinner(scored) {
			return done(fill(best))
		}
		if other, ok := openSetOther(e, q); ok && s.Class.start >= ReviewFillConfidence {
			return done(Draft{Answer: other.Value, ChosenLabel: other.Label, Start: min(s.Class.start, FuzzyConfidenceCap),
				Trace: []string{fmt.Sprintf("open-set list: %q is absent from %d options: select the explicit list-gap choice %q", answer, len(q.Options), other.Label)}})
		}
		if q.Kind == KindCustomListbox && !rejected {
			// A custom listbox's rendered options are a sample, never the whole
			// list (a typeahead loads more as it is searched): the answer is
			// handed to the engine's own search instead of being refused. Not
			// after a residence option was rejected: the search for "Yes" would
			// pick it (BILL).
			return done(Draft{Answer: answer, ChosenLabel: answer, Trace: []string{fmt.Sprintf(
				"open sample: %q is not among the %d rendered options of a custom listbox, which are not the whole list: defer to the engine's search", answer, len(q.Options))}})
		}
		return done(Draft{Abstain: &Abstain{Reason: ReasonLowConfidence}})
	}
	if len(scored) > 1 && scored[1].Score == best.Score {
		if optionValue(scored[1].opt) == optionValue(best.opt) {
			tr = append(tr, "same-value tie at top score: DOM-order tie-break, fill first")
		} else {
			tied := tiedAtTop(scored)
			opts := make([]Option, len(tied))
			for i, t := range tied {
				opts[i] = t.opt
			}
			tb, ok := qualifiedAffirmativeTie(s, opts)
			if !ok && norm == "yes" {
				tb, ok = placeRankedAffirmativeTie(s, opts)
			}
			if !ok || tb.Index < 0 || tb.Index >= len(tied) {
				return done(Draft{Abstain: &Abstain{Reason: ReasonAmbiguous}, Trace: []string{"distinct-value tie at top score: abstain"}})
			}
			best = tied[tb.Index]
		}
	}
	return done(fill(best))
}

func anyAtOrAbove(list []scoredOption, floor float64) bool {
	for _, s := range list {
		if s.Score >= floor {
			return true
		}
	}
	return false
}

func tiedAtTop(sorted []scoredOption) []scoredOption {
	n := 1
	for n < len(sorted) && sorted[n].Score == sorted[0].Score {
		n++
	}
	return sorted[:n]
}

func selectish(q Question) bool { return q.Kind == KindSelect || q.Kind == KindCustomListbox }

// isDeterministicEntry reports an answer that does not depend on phrasing: a
// fixed entry, or a pick-option entry with exactly one preferred value.
func isDeterministicEntry(e *Entry) bool {
	return e.Kind == KindFixed || e.Kind == KindPickOption && len(e.Preferred) == 1
}

// capabilityLadder reports options that read as an ascending capability scale.
func capabilityLadder(opts []Option) bool {
	_, ok := readCapabilityLadder(opts)
	return ok
}

// placeRankedAffirmativeTie breaks a tie between Yes options that each name one
// place (Krea: "Are you available to work full-time in-person in SF or NYC?"
// over "Yes, from SF", "Yes, from NYC" and "No") by the relocation preference's
// ranking: the top acceptable place (owner decision). A tied option that names
// no place, or several, leaves the tie, and so does a place the list names only
// by a code it leaves open (openSharedCode).
func placeRankedAffirmativeTie(s *Step, opts []Option) (optionBinding, bool) {
	pol := policyOf(s)
	if pol == nil {
		return optionBinding{}, false
	}
	if pl, open := openSharedCode(pol, s); open {
		s.Tracef("%s", sharedCodeTrace(pl))
		return optionBinding{}, false
	}
	labels := make([]string, len(opts))
	for i, o := range opts {
		if len(optionPlaces(pol, o.Label)) != 1 {
			return optionBinding{}, false
		}
		labels[i] = o.Label
	}
	for _, r := range rankRenderedPlaces(pol, labels) {
		if r.Acceptable {
			o := opts[r.Index]
			s.Tracef("Yes options tied on their places: the relocation preference ranks %s first", quoteTerm(o.Label))
			return optionBinding{Label: o.Label, Value: o.Value, Index: r.Index, Score: 0.85}, true
		}
	}
	return optionBinding{}, false
}
