package match

// locationPolicyAnswer answers a fixed entry that binds a location_policy. The
// bound predicate's verdict is mapped to if_true (yes) or if_false (no); an
// unknown verdict, an unmapped one and an unconfigured policy make the entry
// answer from its own body, and a blank body then abstains. On a control with
// a located / affirmative / negative option set the honest branch is picked
// instead. A "Yes" never lands on an option that states where the applicant
// lives unless the profile holds that place.
func locationPolicyAnswer(s *Step, e *Entry) (Draft, bool) {
	lp := e.LocationPolicy
	if lp == nil || lp.Resolved == nil {
		return Draft{}, false
	}
	pol, q := lp.Resolved, s.Q
	job := jobPtr(s.Req)

	if len(realOptions(q.Options)) >= 2 {
		if d, ok := locationBranchPick(s, e, pol, job); ok {
			return d, true
		}
	}
	v := predicateVerdict(pol, lp.Predicate, q.Text, job)
	answer := ""
	switch v {
	case verdictYes:
		answer = lp.IfTrue
	case verdictNo:
		answer = lp.IfFalse
	}
	if answer == "" {
		s.Tracef("location-policy %s -> %s: no mapped answer, the entry answers from its own body", lp.Predicate, v)
		if e.BodyEmpty() {
			return abstainDraft(ReasonPlaceholderEmpty, "", "%s is blank and the policy gives no answer", e.ID), true
		}
		return Draft{}, false
	}
	trace := "location-policy " + lp.Predicate + " -> " + v.String() + " -> " + quoteTerm(answer)
	if job != nil {
		if base := predicateVerdict(pol, lp.Predicate, q.Text, nil); base != v {
			trace += " (the posting location " + quoteTerm(job.finest()) + " moved the verdict from " + base.String() + ")"
		}
	}
	step := *s
	if normalizeLabel(answer) == "yes" {
		kept, notes := withoutUnearnedResidence(pol, job, q.Options)
		if hasRenderedOptions(q.Options) && !hasRenderedOptions(kept) {
			return abstainDraft(ReasonInputMismatch, "", "every option states where the applicant lives, which the profile does not hold"), true
		}
		step.Q.Options = kept
		step.trace = append(append([]string(nil), s.trace...), notes...)
	}
	d := bindEntryAnswer(&step, e, answer, trace)
	d.Source, d.Start = SourceDerived, startProfileDerive
	d.Trace = append(append([]string(nil), step.trace[len(s.trace):]...), d.Trace...)
	return d, true
}
