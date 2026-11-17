package onboarding

import (
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"applysmith/internal/cvparse"
	"applysmith/internal/demo"
	"applysmith/internal/workspace"
)

var usStates = map[string]string{"NC": "North Carolina"}

var monthNames = [...]string{"January", "February", "March", "April", "May", "June", "July", "August", "September", "October", "November", "December"}

func personaProfile(t *testing.T) workspace.Profile {
	t.Helper()
	p, rep := workspace.ParseProfile(demo.File(demo.FileProfile))
	if !rep.OK() {
		t.Fatalf("the persona's profile.yml is invalid: %+v", rep.Issues)
	}
	return p
}

// The persona's files describe one person: the CV, the profile, the narrative
// and the answers agree on every fact the forms ask for.
func TestDemoPersona_TheFilesAgreeWithEachOther(t *testing.T) {
	p := personaProfile(t)
	cv := string(demo.File(demo.FileCV))
	c, a := p.Candidate, p.Candidate.Address

	for name, want := range map[string]string{"name": c.FullName, "email": c.Email, "phone": c.Phone, "city and region": a.City + ", " + a.Region,
		"portfolio": p.Links.Portfolio, "linkedin": p.Links.LinkedIn, "github": p.Links.GitHub} {
		if want == "" || !strings.Contains(cv, want) {
			t.Errorf("cv.md does not carry the profile's %s %q", name, want)
		}
	}
	for _, l := range []string{p.Links.Portfolio, p.Links.GitHub} {
		if !strings.Contains(l, ".example.org") {
			t.Errorf("link %q is not on a reserved example domain", l)
		}
	}
	// Application forms reject a LinkedIn link that is not on linkedin.com, so
	// the persona's is on the real domain with a slug that names the demo.
	if l := p.Links.LinkedIn; !strings.HasPrefix(l, "https://www.linkedin.com/in/") || !strings.HasSuffix(l, "-applysmith-demo") {
		t.Errorf("linkedin %q must be a linkedin.com/in/ link whose slug ends in -applysmith-demo", l)
	}
	if !isFictionalPhone(c.Phone) || !reservedEmail(c.Email) {
		t.Errorf("phone %q and email %q must be reserved example values", c.Phone, c.Email)
	}

	draft := cvparse.Parse(cv)
	hist := p.History
	if len(draft.Employment) != 3 || len(draft.Employment) != len(hist.Employment) {
		t.Fatalf("cv.md has %d roles, profile.yml %d, want 3 each", len(draft.Employment), len(hist.Employment))
	}
	for i, j := range draft.Employment {
		e := hist.Employment[i]
		if j.Employer != e.Employer || j.Title != e.Title || j.Start != e.Start || j.End != e.End {
			t.Errorf("role %d: cv.md says %+v, profile.yml says %s, %s, %s to %s", i+1, j, e.Title, e.Employer, e.Start, e.End)
		}
	}
	if len(draft.Education) != 1 || len(hist.Education) != 1 {
		t.Fatalf("education: cv.md %d, profile.yml %d, want 1 each", len(draft.Education), len(hist.Education))
	}
	if s, e := draft.Education[0], hist.Education[0]; s.School != e.School || s.Degree != e.Degree || s.Field != e.Field || s.Start != e.Start || s.End != e.End {
		t.Errorf("education: cv.md says %+v, profile.yml says %+v", s, e)
	}
	for _, s := range p.Skills {
		if !strings.Contains(cv, s.Name) {
			t.Errorf("profile skill %s is not on the CV", s.Name)
		}
	}

	current := hist.Employment[0]
	if current.End != "present" {
		t.Fatalf("the first role should be the current one, it ends %s", current.End)
	}
	earliest, err := time.Parse("2006-01", hist.Employment[len(hist.Employment)-1].Start)
	if err != nil {
		t.Fatal(err)
	}
	asOf := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC) // the persona's present
	years := int(asOf.Sub(earliest).Hours() / 24 / 365.25)
	for _, s := range p.Skills {
		if s.Years != nil && int(*s.Years) > years {
			t.Errorf("skill %s has %v years, more than the %d years of work", s.Name, *s.Years, years)
		}
	}
	if !strings.Contains(cv, "nine years") || years != 9 {
		t.Errorf("the summary says nine years; the dates give %d", years)
	}

	narrative := string(demo.File(demo.FileNarrative))
	if !strings.Contains(narrative, current.Employer) || !strings.Contains(narrative, hist.Employment[1].Employer) {
		t.Error("narrative.md does not name the employers its proof points come from")
	}
	for _, f := range []string{demo.FileCV, demo.FileNarrative, demo.FileVoice, demo.FileProfile} {
		for _, old := range []string{"Example Corp", "Example Labs"} {
			if strings.Contains(string(demo.File(f)), old) {
				t.Errorf("%s still names %s", f, old)
			}
		}
	}
	if len(p.Resumes) != 1 || p.Resumes[0].File != demo.ResumeFile {
		t.Errorf("profile resumes = %+v, want the persona's PDF %s", p.Resumes, demo.ResumeFile)
	}

	// No sample posting is from an employer of the persona.
	for _, post := range demo.Postings() {
		for _, e := range hist.Employment {
			if strings.EqualFold(post.Company, e.Employer) {
				t.Errorf("the persona applies to %s, where it works or worked", post.Company)
			}
		}
	}

	ans := demo.Answers()
	graduated, _ := time.Parse("2006-01", hist.Education[0].End)
	for id, want := range map[string]string{
		"current-company":                        current.Employer,
		"most-recent-school":                     hist.Education[0].School,
		"undergraduate-school":                   hist.Education[0].School,
		"graduation-year-month":                  fmt.Sprintf("%s %d", monthNames[graduated.Month()-1][:3], graduated.Year()),
		"years-full-time-work-experience-number": fmt.Sprint(years),
		"address-state":                          usStates[a.Region],
		"address-country":                        "United States",
		"address-county":                         a.County,
		"salary-currency":                        p.Targets.Compensation.Currency,
		"phone-country-code":                     "+1",
		"work-auth-us":                           "Yes",
		"h1b-sponsorship":                        yesNoOf(p.WorkAuthorization.RequiresSponsorshipNow, p.WorkAuthorization.RequiresSponsorshipFuture),
		"us-person-itar-ear":                     usPersonOf(p.WorkAuthorization.VisaStatus),
	} {
		if want == "" || ans[id] != want {
			t.Errorf("answers.json %s = %q, the profile and CV say %q", id, ans[id], want)
		}
	}
	if !strings.HasPrefix(c.Phone, ans["phone-country-code"]) || a.Country != "US" || !slices.Contains(p.WorkAuthorization.AuthorizedCountries, "US") {
		t.Error("the phone prefix, country and work authorization disagree")
	}
}

// yesNoOf is the sponsorship answer the profile's two facts give: Yes when
// either is true, No when both are false, nothing when one is unset.
func yesNoOf(now, future *bool) string {
	switch {
	case now == nil || future == nil:
		return ""
	case *now || *future:
		return "Yes"
	}
	return "No"
}

// usPersonOf is the export-control answer a visa status gives: a citizen is a
// US person; any other status leaves the question unanswered.
func usPersonOf(visa string) string {
	if strings.EqualFold(visa, "citizen") {
		return "Yes"
	}
	return ""
}

// Every answer the persona gives lands in an entry the default bank has: as
// written by the seed, or as its own setup states it (the seed does not copy
// what the stamped bank already answers).
func TestDemoPersona_EveryAnswerIsStoredInTheSeededBank(t *testing.T) {
	env := demoEnv(t)
	loaded, err := bankService(env).Bank()
	if err != nil {
		t.Fatal(err)
	}
	for id, want := range demo.Answers() {
		e, ok := loaded.Bank.Find(id)
		if !ok {
			t.Errorf("the persona answers %s, which the default bank has no entry for", id)
			continue
		}
		if e.Answer != want && e.AnswerIfNoMatch != want && !strings.Contains(strings.Join(e.Preferred, "|"), want) {
			t.Errorf("entry %s holds %q after the demo seed, want %q", id, e.Answer, want)
		}
	}
}
