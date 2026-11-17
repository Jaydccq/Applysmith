package match

// profileStages fills the profile-grounded and derived-answer slots
// (parity sections PR and DV): RowControls, Derived, Fixed, EmployerHistory,
// CompensationRange, LanguageProficiency and MultiSelect.
func profileStages(st *Stages) {
	st.RowControls = rowControls
	st.Derived = guarded(derivedEntry)
	st.Fixed = guarded(fixedEntry)
	st.EmployerHistory = guarded(employerHistoryEntry)
	st.Relationship = relationshipLane
	st.CompensationRange = guarded(compensationEntry)
	st.LanguageProficiency = guarded(languageEntry)
	st.MultiSelect = guarded(multiSelectEntry)
}

// derivedLanes are the profile-grounded resolvers that answer a question
// without a bank entry, in the order of the resolver order. They stand down
// when a curated example names the question verbatim.
var derivedLanes = []StageFunc{
	educationDate, fieldOfStudy, currentSchool, graduatedSchool, educationAttained, undergradSchool, currentlyPursuing, schoolCoop, recentGraduateOrStudent, studentStatus, pursuingLevel, highestEducationLevel,
	startWindow, startPeriod, storedDateSpan,
	gpaBracket, scopedGPA, graduationChange, graduationTimeFrame, graduationWindow, employmentDate, stillEmployed,
	yearsThreshold, workedForFloor, skillYears, capabilityScale, proficiencyLadder, leastClaimedRating, smallestCompanySize, sponsorshipQuestionsYes, readNoticeYesNo, typedAcknowledgement, readCheck, travelShareBox, sponsorshipVisaAndExpiry, sponsorshipStatementBox, visaOrPermitNeed, nextOpportunityTiming, sanctionsChecklist, sanctionsFollowUp, sanctionsYesNo, thanksOnlyChoice, listedCountryCitizenship, secondCitizenshipNone, citizenshipList, usCitizenYesNo,
	skillYearsEmbedded, usResidence, regionResidence, usStateResidence, roleLocationResidence, roleCountryResidence, roleLocationPick, workModeNoPreference, workModeBoxes, authorizedOnsiteStatement, degreeHeld, degreeInField, stemFieldsNamed, advertisingShareDeclined, profileVisibilityNarrowest,
	transitioningServiceMember, noMilitaryService, usGovernmentWithMilitary, eventsAttended, anyShift, coreHours,
}

// rowControls is step 3: the controls of an identified education or
// work-history row, then the derived lanes, then the typed initials and
// today's date.
func rowControls(s *Step) (Draft, bool) {
	rows := chain(compoundRoleCompany, identifiedEducationRow, opaqueWorkdayDegree, identifiedWorkRow, greenhouseWorkRow, pairedWorkRow, sectionHeadingEducationRow)
	if d, ok := rows(s); ok {
		return d, true
	}
	for _, lane := range derivedLanes {
		d, ok := lane(s)
		if !ok {
			continue
		}
		if curatedVerbatimHit(s) {
			s.Tracef("a curated example names the question; the derived lane stands down")
			break
		}
		return d, true
	}
	return chain(typedInitials, typedMiddleName, signAndDate, todaysDate)(s)
}
