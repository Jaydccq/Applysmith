package match

import (
	"slices"
	"strings"
)

// Facts are the profile values the profile-grounded resolvers read. The caller
// copies them from profile.yml for every request, so a profile edit takes
// effect on the next match without a restart; the package never reads a file.
// A blank value is unanswered, never a default.
type Facts struct {
	// Education is history.education in profile order.
	Education []EducationFact
	// Employment is history.employment in profile order.
	Employment []EmploymentFact
	// Skills is the profile skills list.
	Skills []SkillFact
	// DateOfBirth is candidate.date_of_birth.
	DateOfBirth string
	// VisaStatus is work_authorization.visa_status.
	VisaStatus string
	// RequiresSponsorshipNow and RequiresSponsorshipFuture are the two sponsorship
	// facts of work_authorization. Nil is unset, which is never read as false.
	RequiresSponsorshipNow, RequiresSponsorshipFuture *bool
	// AuthorizedCountries is work_authorization.authorized_countries: the
	// countries (ISO alpha-2, or a region such as EU) the work-authorization
	// and sponsorship facts are recorded for.
	AuthorizedCountries []string
	// Citizenships is work_authorization.citizenships: every country (ISO
	// alpha-2) the applicant is a citizen of. Empty is unrecorded.
	Citizenships []string
	// Address is candidate.address; Country is an ISO 3166-1 alpha-2 code.
	Address AddressFact
	// WorkModes is preferences.work_modes: any of remote, hybrid and onsite.
	// Empty is unrecorded.
	WorkModes []string
	// Evidence is the text the user's proficiency is judged against: résumé and
	// knowledge snippets, supplied by the caller.
	Evidence []string
	// Knowledge are the sections of the user's own knowledge base (the
	// snippets, the narrative and the résumé; never a company brief), read for
	// a request that asks about the user's own experience (F-10).
	// KnowledgeRead reports that the knowledge base was read whole: without
	// it no answer is No for want of evidence. KnowledgeNear holds, by question
	// text, the snippet sections the knowledge base's semantic stage ranks near
	// it, which may mention it in other words.
	Knowledge     []KnowledgeSection
	KnowledgeRead bool
	KnowledgeNear map[string][]string
	// ProfileText is the rest of what the user wrote in the profile about
	// themselves: target roles and places, work modes, relocation places, the
	// résumés' labels, tags, descriptions and instructions, and their links.
	ProfileText []string
	// Relationships are company_relationships.relationships, and
	// RelationshipsComplete is company_relationships.complete: per family, the
	// date (YYYY-MM-DD) the user confirmed that family's list complete.
	Relationships         []RelationshipFact
	RelationshipsComplete map[RelationshipFamily]string
}

// RelationshipFact is one company relationship the user recorded. Recorded is
// the date (YYYY-MM-DD) it was written down; a completeness statement older
// than it no longer covers the family. A fact the tracker holds (bind.Jobs.History)
// has none: the system's own record never unsettles a statement.
type RelationshipFact struct {
	Family   RelationshipFamily
	Company  string
	Aliases  []string
	Current  bool
	Person   string
	Relation string
	Recorded string
}

// AddressFact is the profile address. A blank part is unanswered.
type AddressFact struct {
	Line1, Line2, City, County, Region, PostalCode, Country string
}

// EducationFact is one history.education entry. Start and End are YYYY-MM; an
// empty End is a degree not completed whose end is not known, so it holds no
// date: a lane that needs the end month leaves its question to the user.
type EducationFact struct {
	School       string
	Degree       string
	Field        string
	Start        string
	End          string
	GradeAverage string
}

// EmploymentFact is one history.employment entry. End is YYYY-MM, "present" or
// empty, the last two meaning the job is current.
type EmploymentFact struct {
	Employer string
	Title    string
	Start    string
	End      string
	Location string
	Summary  string
}

// SkillFact is one profile skill. Years is nil when the user recorded none; Level
// is beginner, intermediate, advanced, expert or empty.
type SkillFact struct {
	Name  string
	Years *float64
	Level string
}

// OngoingEnd reports an employment entry that has not ended.
func (e EmploymentFact) OngoingEnd() bool {
	end := strings.ToLower(strings.TrimSpace(e.End))
	return end == "" || end == "present"
}

// orderedEducation returns the education entries most recent first, the order
// a form's numbered education rows take them in.
func orderedEducation(f Facts) []EducationFact {
	return RecentFirst(f.Education, func(e EducationFact) string { return e.End })
}

// RecentFirst returns items most recent first by their end month: an item with
// no end sorts ahead of every finished one, then by end month descending, and
// ties keep the given order. The input is not modified.
func RecentFirst[T any](items []T, end func(T) string) []T {
	out := slices.Clone(items)
	key := func(e T) int {
		if ym, ok := parseYearMonth(end(e)); ok {
			return ym.key()
		}
		return 1 << 40
	}
	slices.SortStableFunc(out, func(a, b T) int { return key(b) - key(a) })
	return out
}

// mostRecentEducation is the first entry of orderedEducation.
func mostRecentEducation(f Facts) (EducationFact, bool) {
	rows := orderedEducation(f)
	if len(rows) == 0 {
		return EducationFact{}, false
	}
	return rows[0], true
}

// mostRecentEmployment is the current entry (no end, or "present"), otherwise
// the one with the latest end month; ties go to the first entry in the list,
// as for education (RecentFirst) and the profile's own reading.
func mostRecentEmployment(f Facts) (EmploymentFact, bool) {
	var best EmploymentFact
	bestKey, found := -1, false
	for _, e := range f.Employment {
		key := -1
		switch ym, ok := parseYearMonth(e.End); {
		case e.OngoingEnd():
			key = 1 << 40
		case ok:
			key = ym.key()
		}
		if !found || key > bestKey {
			best, bestKey, found = e, key, true
		}
	}
	return best, found
}

// KnowledgeSection is one section of the user's own knowledge base: its ref
// ("knowledge/projects/a.md#Detail") and its text.
type KnowledgeSection struct{ Ref, Text string }
