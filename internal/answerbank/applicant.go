package answerbank

import (
	"strings"
	"time"

	"applysmith/internal/answerbank/match"
	"applysmith/internal/workspace"
)

// Applicant is the identity and history snapshot the engine receives when a
// fill starts. It holds what the profile and the bank's identity entries say
// and the parts derived from them; a value nobody wrote is absent, and a
// field the user left to themselves is named in AskMe instead of valued.
type Applicant struct {
	FullName, FirstName, MiddleName, LastName, PreferredName string
	Email                                                    string
	Phone, PhoneCountryCode, PhoneNational                   string
	Address                                                  ApplicantAddress
	LinkedIn, GitHub, Portfolio                              string
	Employment                                               []ApplicantJob
	Education                                                []ApplicantStudy
	// AskMe names the fields, in the engine's path form, that hold the ask-me
	// marker, so the engine reports each as a leftover for the user.
	AskMe []string
}

// ApplicantAddress is the postal address; State is the profile's region.
type ApplicantAddress struct{ Line1, Line2, City, County, State, PostalCode, Country string }

// ApplicantJob is one employment entry. Dates are ISO year and month as the
// profile states them: no day is invented, so a control that asks for a whole
// date gets no value from them. A current job has no end.
type ApplicantJob struct {
	Employer, Title, Location, Summary string
	StartDate, EndDate                 string
	Current                            bool
}

// ApplicantStudy is one education entry, dated like ApplicantJob.
type ApplicantStudy struct {
	School, Degree, Field, GradeAverage string
	StartDate, EndDate                  string
	InProgress                          bool
}

// Applicant assembles the snapshot as of now. Profile values win; where one
// is blank the matching identity entry of the bank supplies it, and a blank
// placeholder supplies nothing. Both files are read as they are, so an edit
// takes effect on the next call.
func (s *Service) Applicant(now time.Time) (Applicant, error) {
	loaded, err := s.File()
	if err != nil {
		return Applicant{}, err
	}
	p, _ := s.readProfile()
	return buildApplicant(p, loaded.Bank, now), nil
}

type applicantPicker struct {
	bank  match.Bank
	askMe []string
}

// pick returns the profile value, else the bank entry's answer. A value that
// is the ask-me marker is not a value: the field is recorded under each path.
func (a *applicantPicker) pick(profileValue, bankID string, paths ...string) string {
	v := strings.TrimSpace(profileValue)
	if v == "" && bankID != "" {
		if e, ok := a.bank.Find(bankID); ok && e.Kind == match.KindFixed {
			v = strings.TrimSpace(e.Answer)
		}
	}
	if match.IsAskMe(v) {
		a.askMe = append(a.askMe, paths...)
		return ""
	}
	return v
}

func buildApplicant(p workspace.Profile, bank match.Bank, now time.Time) Applicant {
	pk := &applicantPicker{bank: bank}
	c, ad := p.Candidate, p.Candidate.Address
	var out Applicant
	out.FullName = pk.pick(c.FullName, match.IDApplicantFullName, "fullName", "firstName", "lastName")
	out.FirstName, out.MiddleName, out.LastName = splitName(out.FullName)
	out.PreferredName = pk.pick(c.PreferredName, "preferred-name", "preferredName")
	out.Email = pk.pick(c.Email, "email-address", "email")
	out.Phone = pk.pick(c.Phone, "phone-mobile", "phone")
	out.PhoneCountryCode, out.PhoneNational = splitPhone(out.Phone)
	out.Address = ApplicantAddress{
		Line1:      pk.pick(ad.Line1, "", "address.line1"),
		Line2:      pk.pick(ad.Line2, "", "address.line2"),
		City:       pk.pick(ad.City, "address-city", "address.city"),
		County:     pk.pick(ad.County, "address-county", "address.county"),
		State:      pk.pick(ad.Region, "", "address.state"),
		PostalCode: pk.pick(ad.PostalCode, "", "address.postalCode"),
		Country:    pk.pick(ad.Country, "", "address.country"),
	}
	out.LinkedIn = CanonicalLinkedIn(pk.pick(p.Links.LinkedIn, "linkedin-url", "links.linkedin"))
	out.GitHub = CanonicalLink(pk.pick(p.Links.GitHub, "github-url", "links.github"))
	out.Portfolio = CanonicalLink(pk.pick(p.Links.Portfolio, "portfolio-url", "links.portfolio"))
	for _, e := range p.History.Employment {
		out.Employment = append(out.Employment, job(e))
	}
	for _, e := range p.History.Education {
		out.Education = append(out.Education, study(e, now))
	}
	out.AskMe = pk.askMe
	return out
}

// splitName derives the parts of a full name by whitespace: the first word is
// the first name, the last word the last name and whatever lies between is the
// middle name. One word is a first name only; nothing is guessed for a missing
// part, and a particle or suffix is not recognised.
func splitName(full string) (first, middle, last string) {
	w := strings.Fields(full)
	switch len(w) {
	case 0:
		return "", "", ""
	case 1:
		return w[0], "", ""
	}
	return w[0], strings.Join(w[1:len(w)-1], " "), w[len(w)-1]
}

// splitPhone separates the country calling code of a number written with a
// leading plus. A number written without one has no known country, so only
// its digits are returned as the national part.
func splitPhone(phone string) (code, national string) {
	digits := strings.Map(func(r rune) rune {
		if r >= '0' && r <= '9' {
			return r
		}
		return -1
	}, phone)
	if !strings.HasPrefix(strings.TrimSpace(phone), "+") || len(digits) < 4 {
		return "", digits
	}
	n := callingCodeLen(digits)
	if n >= len(digits) {
		return "", digits
	}
	return "+" + digits[:n], digits[n:]
}

// callingCodeLen is the length of the ITU country calling code at the start of
// digits. The assigned codes form a prefix-free set, so the first digits decide
// it: zones 1 and 7 use one digit, the listed two-digit blocks use two, and
// every other code has three.
func callingCodeLen(digits string) int {
	switch digits[0] {
	case '1', '7':
		return 1
	}
	if twoDigitCodes[digits[:2]] {
		return 2
	}
	return 3
}

var twoDigitCodes = func() map[string]bool {
	m := map[string]bool{}
	for _, code := range []string{"20", "27", "30", "31", "32", "33", "34", "36", "39", "40", "41", "43", "44", "45", "46", "47", "48", "49",
		"51", "52", "53", "54", "55", "56", "57", "58", "60", "61", "62", "63", "64", "65", "66", "81", "82", "84", "86",
		"90", "91", "92", "93", "94", "95", "98"} {
		m[code] = true
	}
	return m
}()

func job(e workspace.Employment) ApplicantJob {
	current := ongoing(e.End)
	j := ApplicantJob{Employer: strings.TrimSpace(e.Employer), Title: strings.TrimSpace(e.Title), Location: strings.TrimSpace(e.Location),
		Summary: strings.TrimSpace(e.Summary), StartDate: month(e.Start), Current: current}
	if !current {
		j.EndDate = month(e.End)
	}
	return j
}

func study(e workspace.Education, now time.Time) ApplicantStudy {
	s := ApplicantStudy{School: strings.TrimSpace(e.School), Degree: strings.TrimSpace(e.Degree), Field: strings.TrimSpace(e.Field),
		GradeAverage: strings.TrimSpace(e.GradeAverage), StartDate: month(e.Start), EndDate: month(e.End)}
	end, err := time.Parse("2006-01", strings.TrimSpace(e.End))
	s.InProgress = ongoing(e.End) || (err == nil && end.AddDate(0, 1, 0).After(now))
	return s
}

func ongoing(end string) bool {
	end = strings.ToLower(strings.TrimSpace(end))
	return end == "" || end == "present"
}

// month returns a YYYY-MM profile date as it is, or "" for anything else. A
// first- or last-of-month day would be a date the user never stated (a
// first-of-month end date understated how long they stayed).
func month(v string) string {
	t, err := time.Parse("2006-01", strings.TrimSpace(v))
	if err != nil {
		return ""
	}
	return t.Format("2006-01")
}
