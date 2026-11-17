package match

// disclosureStages fills the step-4 short-circuits, disclosures and location
// slots (parity sections DS and LC): HumanVerification, ContactComposite,
// ApplicationStatement (with the posting-role picker), RelocationLiteral (with the commute-distance pickers),
// IfNoIndicateNo, CombinedDisclosures and ContextPolicies. The typed initials
// and today's date are the profile family's RowControls chain, which runs ahead
// of these slots, and a full-name signature is the applicant-full-name entry
// that routes 491 to 493 classify.
//
// It also wraps the fixed resolver for the city list disambiguation, which has
// to see an option list before the entry's stored city is scored against it.
func disclosureStages(st *Stages) {
	st.HumanVerification = humanVerification
	st.ContactComposite = contactComposite
	st.ApplicationStatement = chain(counterpartyEmployer, applicationStatement, postingRole)
	st.RelocationLiteral = heldOnSharedCode(chain(relocationLiteral, relocationReason, relocationPlaces, commuteDistance, commuteYesNo, commuteOfficePick, listedStatesYesNo, listedPlaceResidence))
	st.IfNoIndicateNo = indicateNo
	st.CombinedDisclosures = combinedDisclosures
	st.ContextPolicies = contextPolicies
	fixed := st.Fixed
	st.Fixed = func(s *Step) (Draft, bool) {
		if d, ok := cityOption(s, s.Entry); ok {
			return d, true
		}
		if fixed == nil {
			return Draft{}, false
		}
		return fixed(s)
	}
}

// contextPolicies is step 10 for the chosen entry: the country, currency,
// held-compensation and employer-detail policies, terminal context-policy
// abstains. They are the profile family's; the slot calls them so an entry held
// as a pick-option is guarded as well.
func contextPolicies(s *Step) (Draft, bool) {
	if s.Entry == nil {
		return Draft{}, false
	}
	a, line, hit := profileContextGuards(s, s.Entry)
	if !hit {
		return Draft{}, false
	}
	d := Draft{Abstain: &a}
	if line != "" {
		d.Trace = []string{line}
	}
	return d, true
}
