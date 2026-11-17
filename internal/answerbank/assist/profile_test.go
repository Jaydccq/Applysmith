package assist

import (
	"context"
	"strings"
	"testing"

	"applysmith/internal/answerbank/match"
	"applysmith/internal/workspace"
)

func fictionalProfile() workspace.Profile {
	var p workspace.Profile
	p.Candidate.FullName = "Alex Example"
	p.Candidate.DateOfBirth = "1985-03-04"
	p.Candidate.Email = "alex@example.com"
	p.Candidate.Address = workspace.Address{City: "Springfield", Region: "IL", Country: "US"}
	p.WorkAuthorization.VisaStatus = "F-1 OPT, will require sponsorship"
	p.History.Education = []workspace.Education{{School: "Example University", Degree: "BSc", Field: "Computer Science", Start: "2018-09", End: "2022-05", GradeAverage: "3.8"}}
	p.History.Employment = []workspace.Employment{{Employer: "Example Corp", Title: "Engineer", Start: "2022-06", End: "present", Summary: "Built the Example Ledger in Go"}}
	p.Preferences.Relocation = workspace.Relocation{Default: "open", Places: []string{"Berlin, Germany"}, Excluded: []string{"Atlantis, Nowhere"}}
	return p
}

// A blank place in the profile's lists is no place: it must not make every
// place a question names an excluded one.
func TestSourcesFromProfile_ABlankPlaceExcludesNothing(t *testing.T) {
	p := fictionalProfile()
	p.Preferences.Relocation.Excluded = []string{"", " Atlantis, Nowhere ", "atlantis, nowhere"}
	rel := SourcesFromProfile(p, "", nil).Relocation
	if rel == nil || len(rel.Excluded) != 1 || rel.Excluded[0] != "Atlantis, Nowhere" {
		t.Fatalf("excluded = %q", rel.Excluded)
	}
	if excludedPlaces([]string{"Lisbon, Portugal", "Lisbon", "Portugal"}, rel) {
		t.Fatal("a blank excluded place refused a place the profile does not exclude")
	}
	// Places compare as places, not as letters: an excluded "LA" is no part of
	// Dallas, and one the list leaves open may be Louisiana.
	la := &match.RelocationPolicy{Excluded: []string{"LA"}}
	if excludedPlaces([]string{"Dallas"}, la) || !excludedPlaces([]string{"Louisiana"}, la) || !excludedPlaces([]string{"Los Angeles"}, la) {
		t.Fatal("an excluded shared code is read by its letters")
	}
	if excludedPlaces([]string{"Louisiana"}, &match.RelocationPolicy{Excluded: []string{"LA", "Seattle"}}) {
		t.Fatal("beside a city, LA is Los Angeles")
	}
	// An open code makes no place acceptable.
	if !excludedPlaces([]string{"Baton Rouge, Louisiana", "Baton Rouge", "Louisiana"}, &match.RelocationPolicy{Places: []string{"LA"}, Excluded: []string{"Baton Rouge"}}) {
		t.Fatal("an open code made an excluded place acceptable")
	}
}

func TestSourcesFromProfile(t *testing.T) {
	src := SourcesFromProfile(fictionalProfile(), "# CV\n- Built the Example Ledger in Go for payments settlement\n- short\n", []match.Entry{fixedEntry("a", "b", "c")})
	var dump strings.Builder
	for _, f := range src.Profile {
		dump.WriteString(f.Label + "=" + f.Value + "\n")
	}
	dump.WriteString(strings.Join(src.Identity, "\n") + strings.Join(src.CV, "\n"))
	for _, banned := range []string{"1985-03-04", "alex@example.com"} {
		if strings.Contains(dump.String(), banned) {
			t.Fatalf("the sources carry %q", banned)
		}
	}
	if src.DegreeEnd != "2022-05" || src.WorkAuthStatus == "" || src.Relocation == nil || src.Relocation.Home.City != "Springfield" || len(src.CV) != 1 || len(src.Bank) != 1 {
		t.Fatalf("%+v", src)
	}
	// A prompt built from these sources never holds the date of birth.
	e := newEnv(t)
	e.respond(func(_, _ string) string { return replyOption("Alpha") })
	req, resp := hand(match.ReasonNoEntry, roleQ(1))
	e.svc.Run(context.Background(), RunInput{Req: req, Response: resp, Settings: enabled, Sources: src})
	p := e.seen(TaskOption)
	if len(p) != 1 || strings.Contains(p[0], "1985") || strings.Contains(p[0], "alex@example.com") {
		t.Fatalf("%v", p)
	}
}

// Every recorded degree and the filled lists of certifications, licenses,
// spoken languages and publications are the applicant's credentials, and the
// lists are evidence lines; an entry's description never is.
func TestSourcesFromProfile_CredentialsAndLists(t *testing.T) {
	p := fictionalProfile()
	p.History.Education = []workspace.Education{
		{School: "Example College", Degree: "BS", Field: "Economics", End: "2014-05"},
		{School: "Example University", Degree: "MBA", Field: "Finance", End: "2019-05"},
	}
	langs := match.Entry{ID: "languages-spoken", Kind: match.KindMultiSelect, Body: match.Body{Selected: []string{"English", "Mandarin"}}}
	src := SourcesFromProfile(p, "", []match.Entry{
		fixedEntry("certifications-held", "Every certification, certificate and license the applicant holds", "CFA\nFRM"),
		langs,
		{ID: "publications", Kind: match.KindFixed, Meta: match.Meta{Placeholder: true}},
		fixedEntry("a", "b", "c"),
	})
	creds := strings.Join(src.Credentials, "|")
	for _, want := range []string{"BS Economics", "MBA Finance", "CFA", "FRM", "English, Mandarin"} {
		if !strings.Contains(creds, want) {
			t.Errorf("credentials %q lack %q", creds, want)
		}
	}
	var lines []string
	for _, l := range src.Evidence {
		lines = append(lines, l.Ref+"|"+l.Entry+"|"+l.Text)
	}
	got := strings.Join(lines, "\n")
	for _, want := range []string{"answer-bank#certifications-held|Certifications and licenses|CFA", "answer-bank#certifications-held|Certifications and licenses|FRM", "answer-bank#languages-spoken|Spoken languages|English, Mandarin"} {
		if !strings.Contains(got, want) {
			t.Errorf("evidence lacks %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "Every certification") || strings.Contains(got, "publications") || strings.Contains("|"+creds+"|", "|c|") {
		t.Fatalf("a description, a blank list or an unrelated entry is evidence:\n%s", got)
	}
}
