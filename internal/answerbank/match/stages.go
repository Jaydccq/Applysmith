package match

// DefaultStages returns the resolver slots of the shipped matcher. Each
// resolver family fills its own slots in its own file, so the families are
// ported and reviewed independently.
func DefaultStages() Stages {
	var st Stages
	profileStages(&st)
	optionStages(&st)
	disclosureStages(&st)
	// The request-wide passes run in the order of the specification, whichever
	// family owns each: bare-city widening, residence re-routes, search-box
	// re-route, conditional follow-ups, the places an earlier answer chose,
	// the degree fields the posting lists, an accepted arrangement, link
	// de-duplication, a required website's link.
	st.PostPasses = []PostPass{
		widenBareLocationPass, rerouteResidenceOptionList, rerouteLongResidenceList, rerouteGeoSearchPlaceholder,
		conditionalFollowUpsPass, placesSelectedBeforePass, postedDegreeFieldsPass, acceptedArrangementPass, dedupeLinks, requiredWebsite,
	}
	return st
}
