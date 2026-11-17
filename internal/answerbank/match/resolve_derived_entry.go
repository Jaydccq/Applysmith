package match

import (
	"fmt"
	"slices"
	"strings"
)

// entryDraft is the draft of an answer computed for the classified entry: the
// result keeps the entry's id and answer key and reports a derived source.
func entryDraft(answer, trace string) Draft {
	d := Draft{Answer: answer, Source: SourceDerived, Start: startProfileDerive, Deterministic: true}
	if trace != "" {
		d.Trace = []string{trace}
	}
	return d
}

// derivedEntry is the Derived slot. The first deriver that knows the entry id
// decides, abstains included; the stored body is never read as the answer of
// an id that is computed.
func derivedEntry(s *Step) (Draft, bool) {
	e := s.Entry
	switch e.ID {
	case IDCurrentAgeBracket, IDAgeMinimumThreshold:
		return deriveAge(s, e)
	case IDFullTimeStartAvailable:
		return deriveAvailability(s, e)
	case IDWorkAuthorizationDetail:
		return deriveWorkAuthDetail(s, e)
	case IDCurrentCompany:
		return currentEmployer(s, e)
	case IDEarliestStartDate:
		return futureOnlyDate(s, e)
	case idSponsorshipNowOrFuture, idWorkAuthAndFutureSponsorship:
		return derivedSponsorship(s, e)
	case IDUSPerson:
		return deriveUSPerson(s, e)
	case idCurrentlyEmployerSponsored:
		return deriveEmployerSponsored(s, e)
	case idCurrentlyEmployed:
		return deriveCurrentlyEmployed(s, e)
	case idPermanentWorkAuthorization:
		return derivePermanentWorkAuth(s, e)
	case idSkillChecklist:
		return deriveSkillChecklist(s, e)
	case idConflictSituations:
		return deriveConflictSituations(s, e)
	case idRightToWorkCountries:
		return deriveRightToWorkCountries(s, e)
	case idEADCategoryCode:
		return deriveEADCategory(s, e)
	case idGoogleScholarURL, idPublications:
		return deriveBlankIsNo(s, e)
	case idCompetingOfferDeadline:
		return deriveOfferDeadline(s, e)
	}
	if slices.Contains(credentialIDs, e.ID) {
		return deriveCredential(s, e)
	}
	return Draft{}, false
}

const (
	idSponsorshipNowOrFuture       = "sponsorship-now-or-future"
	idWorkAuthAndFutureSponsorship = "work-auth-and-future-sponsorship"
	idCurrentlyEmployerSponsored   = "currently-employer-sponsored"
	idCurrentlyEmployed            = "currently-employed"
	idPermanentWorkAuthorization   = "permanent-work-authorization"
	idSkillChecklist               = "skill-checklist"
)

// startLeadDays is the owner's rule for a start date the user did not set: an
// earliest-start-date left blank, or one that has passed, is the day thirty
// days from today.
const startLeadDays = 30

// StartDate is the first day the applicant can start, from the stored
// earliest-start-date answer: the stored date when it is today or later, else
// (blank or passed) the day startLeadDays from today. The ask-me marker, a
// value that is not a date, or a blank one without a valid today is no start
// date.
func StartDate(stored string, today Date) (Date, bool) {
	stored = strings.TrimSpace(stored)
	if IsAskMe(stored) {
		return Date{}, false
	}
	if stored == "" {
		if !today.Valid() {
			return Date{}, false
		}
		return today.AddDays(startLeadDays), true
	}
	d, ok := parseCalendarDate(stored)
	if !ok {
		return Date{}, false
	}
	if today.Valid() && d.Before(today) {
		return today.AddDays(startLeadDays), true
	}
	return d, true
}

// storedStart is the raw earliest-start-date answer of the step's bank, the
// ask-me marker included, or "" when the bank has no such entry.
func storedStart(s *Step) string {
	if e := entryNamed(s, IDEarliestStartDate); e != nil && e.Kind == KindFixed {
		return e.Answer
	}
	return ""
}

// futureOnlyDate answers earliest-start-date with StartDate: a stored date
// that is today or later is typed unchanged, a blank or passed one as the day
// startLeadDays from today in ISO form, and either as the last day of an
// education entry that ends after it (AfterEducation). The ask-me marker, a
// value that is not a date or an education entry without an end month
// abstains, and an option list is left to the availability entry because a
// date matches no lead-time label.
func futureOnlyDate(s *Step, e *Entry) (Draft, bool) {
	q := s.Q
	if hasRenderedOptions(q.Options) {
		return abstainDraft(ReasonInputMismatch, "", "an option list is answered by %s, not a date", IDFullTimeStartAvailable), true
	}
	stored := strings.TrimSpace(e.Answer)
	d, ok := StartDate(stored, s.Req.Today)
	if !ok {
		return abstainDraft(ReasonPlaceholderEmpty, "", "%s %q is left for you or not an ISO or US date", e.ID, stored), true
	}
	from, ok := AfterEducation(d, s.Req.Today, s.Req.Facts.Education)
	switch {
	case !ok:
		return abstainDraft(ReasonPlaceholderEmpty, "", "an education entry has no end month: availability is unknown"), true
	case from != d:
		return entryDraft(from.ISO(), fmt.Sprintf("an education entry ends after %s: typed as its last day", d.ISO())), true
	}
	if own, ok := parseCalendarDate(stored); ok && own == d {
		out := entryDraft(stored, "")
		out.Source = SourceEntry
		return out, true
	}
	why := fmt.Sprintf("%s is blank: typed as %d days from today", e.ID, startLeadDays)
	if stored != "" {
		why = fmt.Sprintf("stored start date %s is before today: typed as %d days from today", stored, startLeadDays)
	}
	return entryDraft(d.ISO(), why), true
}

const idConflictSituations = "conflict-of-interest-situations"

// conflictSituationFacts are the entries whose situations a conflict-of-interest
// list asks about together (HP: outside employment with a customer or
// competitor, a financial interest, business with someone close, a relationship
// with another employee).
var conflictSituationFacts = []string{"relatives-at-employer", "outside-business-activities",
	"competitive-outside-activities", "financial-interests-conflict", "client-affiliation-conflict"}

// deriveConflictSituations answers "would you be engaging in any of the above
// listed situations": the user's own answer, else No when every situation fact
// the user recorded is No. A blank fact, or any other answer, leaves it to the
// user: one fact's Yes does not say whether it is one of the listed situations.
func deriveConflictSituations(s *Step, e *Entry) (Draft, bool) {
	if own := ownText(e); own != "" {
		return bindEntryAnswer(s, e, own, "the user's own answer"), true
	}
	for _, id := range conflictSituationFacts {
		if !strings.EqualFold(strings.TrimSpace(ownText(entryNamed(s, id))), "no") {
			return abstainDraft(ReasonPlaceholderEmpty, "", "%s: %s does not record No", e.ID, id), true
		}
	}
	return bindEntryAnswer(s, e, "No", "every recorded conflict situation ("+strings.Join(conflictSituationFacts, ", ")+") is No -> No"), true
}
