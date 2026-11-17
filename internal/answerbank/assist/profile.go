package assist

import (
	"fmt"
	"strings"

	"applysmith/internal/answerbank"
	"applysmith/internal/answerbank/match"
	"applysmith/internal/workspace"
)

// credentialLists are the bank entries that list what the applicant holds or
// wrote, one item a line, with the label their lines carry as evidence. Only
// the user's own values are read, never an entry's description.
var credentialLists = map[string]string{
	"certifications-held":   "Certifications and licenses",
	"professional-licenses": "Certifications and licenses",
	"languages-spoken":      "Spoken languages",
	"publications":          "Publications",
}

// SourcesFromProfile reduces the user's profile, CV text and the bank's entries
// to the facts the lanes may use. A blank value is skipped, never defaulted. The
// self-identification values and the date of birth are not carried at all: no
// lane may use them.
func SourcesFromProfile(p workspace.Profile, cv string, entries []match.Entry) Sources {
	src := Sources{Bank: entries}
	add := func(ref, label, value string, c Category) {
		if v := strings.TrimSpace(value); v != "" {
			src.Profile = append(src.Profile, ProfileField{Ref: "profile#" + ref, Label: label, Value: v, Category: c})
		}
	}
	c := p.Candidate
	add("name", "Name", c.FullName, CatName)
	add("city", "City", c.Address.City, CatCity)
	add("country", "Country", c.Address.Country, CatCountry)
	if len(p.History.Education) > 0 {
		e := p.History.Education[len(p.History.Education)-1]
		for _, cand := range p.History.Education {
			if cand.End > e.End {
				e = cand
			}
		}
		add("degree", "Highest degree", strings.TrimSpace(e.Degree+" "+e.Field), CatDegree)
		add("school", "School", e.School, CatSchool)
		if e.Start != "" || e.End != "" {
			add("education-dates", "Education dates", strings.TrimSpace(e.Start+" to "+e.End), CatEduDates)
		}
		add("gpa", "Grade average", e.GradeAverage, CatGPA)
		src.DegreeEnd, src.Degree = e.End, strings.TrimSpace(e.Degree+" "+e.Field)
		if e.School != "" {
			src.Identity = append(src.Identity, "School: "+e.School)
		}
		for _, d := range p.History.Education {
			if v := strings.TrimSpace(d.Degree + " " + d.Field); v != "" {
				src.Credentials = append(src.Credentials, v)
			}
		}
	}
	for i, e := range p.History.Employment {
		line := strings.TrimSpace(e.Title + " at " + e.Employer)
		if e.Summary != "" {
			line += ": " + e.Summary
		}
		add(fmt.Sprintf("employment-%d", i+1), "Employment", line, CatEmployment)
	}
	var skills []string
	for _, s := range p.Skills {
		if n := strings.TrimSpace(s.Name); n != "" {
			skills = append(skills, n)
		}
	}
	add("skills", "Skills", strings.Join(skills, ", "), CatSkills)
	if wa := match.VisaStatusProse(p.WorkAuthorization.VisaStatus); wa != "" {
		src.WorkAuthStatus = wa
		add("work-authorization", "Work authorization status", wa, CatWorkAuth)
	}
	if name := strings.TrimSpace(c.FullName); name != "" {
		src.Identity = append(src.Identity, "Name: "+name)
	}
	src.Relocation = answerbank.RelocationPolicy(p)
	for _, e := range entries {
		label, ok := credentialLists[e.ID]
		if !ok || e.Archived() || e.Placeholder {
			continue
		}
		for _, v := range strings.Split(entryAnswerText(e), "\n") {
			if v = strings.TrimSpace(v); v != "" {
				src.Evidence = append(src.Evidence, EvidenceLine{Ref: "answer-bank#" + e.ID, Entry: label, Text: v})
				src.Credentials = append(src.Credentials, v)
			}
		}
	}
	for _, line := range strings.Split(cv, "\n") {
		if l := strings.TrimSpace(strings.TrimLeft(strings.TrimSpace(line), "-*+ ")); wordCount(l) >= 4 && !strings.HasPrefix(l, "#") {
			src.CV = append(src.CV, l)
		}
	}
	return src
}
