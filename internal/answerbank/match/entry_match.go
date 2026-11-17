package match

import "slices"

// Curated reports an entry that is finished: a non-empty body, or a derived
// entry or the start date, whose blank body is by design, and not flagged for
// review. Only curated entries use the verbatim and long-example lanes at full
// confidence, so a derived entry owns its own example ahead of the pattern
// routes and the narrative guard.
func (e Entry) curated() bool {
	return (!e.BodyEmpty() || slices.Contains(DerivedIDs, e.ID) || e.futureOnlyDate()) && !e.NeedsReview
}

// classifiable reports whether wording may classify to the entry. The
// run-context entries are reached only by the statement rule, and the
// other-country entries only from the posting-country guard, so their examples
// never race the work-fact entries for the same wording.
func (e Entry) classifiable() bool {
	return !e.RunContext && e.ID != idWorkAuthOtherCountries && e.ID != idSponsorshipOtherCountries
}

// stale reports the stale lifecycle state.
func (e Entry) stale() bool { return e.EffectiveState() == StateStale }

// derived reports an entry answered from profile facts or other entries
// although its stored body is empty; the Derived stage owns it. The current
// employer, whether the applicant has a job now, the U.S. person status and
// whether a company sponsors the visa now are such: the user's own answer
// wins, the profile fills a blank, and the needs-answer rule still lists the
// blank entry.
func (e Entry) derived() bool {
	return slices.Contains(DerivedIDs, e.ID) || e.ID == IDCurrentCompany || e.ID == idCurrentlyEmployed || e.ID == IDUSPerson || e.ID == idCurrentlyEmployerSponsored ||
		e.ID == idConflictSituations || e.ID == idPermanentWorkAuthorization || slices.Contains(credentialIDs, e.ID) ||
		e.ID == idGoogleScholarURL || e.ID == idPublications || e.ID == idCompetingOfferDeadline
}

// policyResolved reports an entry bound to a configured relocation policy: its
// answer comes from the policy, so an empty body is not a blank to abstain on.
func (e Entry) policyResolved() bool {
	return e.LocationPolicy != nil && e.LocationPolicy.Resolved != nil
}

// thresholdExceeded reports a days-a-week question whose number is above the
// maximum stamped from the profile: the profile fact answers it, so an empty
// stored body is not a blank to abstain on.
func (e Entry) thresholdExceeded(text string) bool {
	nt := e.NumericThreshold
	if nt == nil || nt.MaxValue == nil || nt.Extractor != ExtractorDaysPerWeekOnsite {
		return false
	}
	days, _, ok := extractDaysPerWeek(text)
	return ok && days > *nt.MaxValue
}

// futureOnlyDate reports the start-date entry, whose value is never a past
// date: StartDate computes it when the body is blank or has passed.
func (e Entry) futureOnlyDate() bool { return e.ID == IDEarliestStartDate }

// bindsAnyOption reports whether the entry's ranked values can bind a rendered
// option of the question, so a route that claims an entry unable to bind does not
// end the question. Anything that has nothing to bind passes: an entry that is
// not a pick-option, one with the any-option net, a control with no real option
// and an entry with no search terms. A placeholder option counts, because the
// scorer lets one through only on an exact answer.
func (e Entry) bindsAnyOption(opts []Option, sc *optionScorer) bool {
	if e.Kind != KindPickOption || e.FallbackAnyOption || !hasRenderedOptions(opts) {
		return true
	}
	// The office picker ranks the rendered offices by the relocation policy and
	// abstains itself when none is acceptable; its texts bind nothing.
	if e.ID == IDPreferredWorkLocation && e.LocationPolicy != nil && e.LocationPolicy.Resolved != nil {
		return true
	}
	// The US work-authorization status picker also answers from the recorded
	// sponsorship standing (sponsorshipStandingOption): a list with an option
	// that states a sponsorship requirement, or an authorization without one,
	// is its to read whatever statuses it stores (Hover).
	if e.ID == idUSWorkAuthStatus && slices.ContainsFunc(opts, func(o Option) bool {
		return optionStatesSponsorshipRequirement(o.Label) || optionStatesAuthorizationWithoutSponsorship(o.Label)
	}) {
		return true
	}
	terms := append(append([]string(nil), e.Preferred...), nonEmpty(e.FallbackText)...)
	if !entryAnyText(terms) {
		return true
	}
	if sc == nil {
		sc = newOptionScorer("", nil)
	}
	for _, t := range expandSearch(e.ID, terms) {
		for _, o := range opts {
			if sc.score(o, t).Score >= ReviewFillConfidence {
				return true
			}
		}
	}
	return false
}
