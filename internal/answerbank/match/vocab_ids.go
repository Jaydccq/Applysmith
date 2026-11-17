package match

import "slices"

// Entry ids the bank rules depend on. The ids are stable across default-bank
// releases.
const (
	IDEarliestStartDate       = "earliest-start-date"
	IDCurrentAgeBracket       = "current-age-bracket"
	IDAgeMinimumThreshold     = "age-minimum-threshold"
	IDFullTimeStartAvailable  = "full-time-start-availability"
	IDWorkAuthorizationDetail = "work-authorization-detail"
	IDOtherWebsiteURL         = "other-website-url"
	IDTwitterURL              = "twitter-url"
	IDOfficeAttendanceOK      = "office-attendance-ok"
	IDWillingToTravel         = "willing-to-travel"
	IDTravelLimitedPeriod     = "travel-above-ceiling-limited-period"
	IDDesiredCareerLevel      = "desired-career-level"
	IDPreferredWorkLocation   = "preferred-work-location"
	IDRelocationOrLocal       = "relocation-or-local"
	IDBasedInNamedMetro       = "based-in-named-metro"
	IDWillingToRelocate       = "willing-to-relocate"
	IDRelocationAssistance    = "relocation-assistance-needed"
	IDApplicantFullName       = "applicant-full-name"
	IDCurrentJobTitle         = "current-job-title"
	IDCurrentCompany          = "current-company"
	IDCoverLetterSubmitted    = "cover-letter-submitted"
	IDResumeSubmitted         = "resume-submitted"
	ReasonSuffix              = "-reason"
	YearsExperiencePrefix     = "years-experience-"
	IDYearsFullTimeWork       = "years-full-time-work-experience-number"
	idYearsExperienceTotal    = "years-experience-total"
)

// DerivedIDs are answered from profile facts or other entries although their
// stored body is empty; the needs-answer rule never lists them.
var DerivedIDs = []string{
	IDCurrentAgeBracket, IDAgeMinimumThreshold, IDFullTimeStartAvailable, IDWorkAuthorizationDetail,
	"sponsorship-now-or-future", "work-auth-and-future-sponsorship", "skill-checklist", idRightToWorkCountries, idEADCategoryCode,
}

// IntentionallyBlankIDs are optional links that ship empty and stay empty.
var IntentionallyBlankIDs = []string{IDOtherWebsiteURL, IDTwitterURL}

// blanketIDs answer Yes to whatever skill, capability or requirement a
// question names. Only a curated route may decide that a question is such a
// gate; a model reading one into a question would claim any experience the
// page asks about.
var blanketIDs = []string{"skill-experience-knowledge-gate", "capability-self-assessment", "meets-role-requirements", "edu-experience-gate"}

// IsBlanketGate reports one of those entries, which no model lane offers.
func IsBlanketGate(id string) bool { return slices.Contains(blanketIDs, id) }

// ownAnswerOnlyIDs are filled only by the user's own answer: blank, each
// leaves its question to the user, and no model lane is asked it (Anduril's
// transcripts question went to two model calls that could only decline).
var ownAnswerOnlyIDs = []string{
	"academic-transcripts-shareable", "work-authorization-other-countries", "sponsorship-other-countries", IDAIUseDisclosure,
	"automated-application-disclosure", "languages-spoken", "current-salary", "bar-admission", "faa-part-107",
	"active-immigration-case", idCertificationsHeld,
}

// OwnAnswerOnly reports one of those entries.
func OwnAnswerOnly(id string) bool { return slices.Contains(ownAnswerOnlyIDs, id) }

// BlankByDesign reports whether an entry with this id is finished while its
// body is empty: the start date is too, as StartDate computes a blank one.
func BlankByDesign(id string) bool {
	return slices.Contains(DerivedIDs, id) || slices.Contains(IntentionallyBlankIDs, id) || id == IDEarliestStartDate
}
