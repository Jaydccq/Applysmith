package bind

import (
	"context"
	"strings"
	"testing"
	"time"

	"applysmith/internal/answerbank/match"
	"applysmith/internal/kb"
	"applysmith/internal/workspace"
)

// A finance, a life-science and a software applicant, each unlike the owner in
// some way the matcher once read as the owner's: their profile stamped through
// setup's statements, their bank, résumé and knowledge read whole, as a fill
// reads them (Preview).
const financeProfile = `version: 1
candidate:
  full_name: Alex Example
  email: alex@example.com
  address: {line1: 1 Example Street, city: Albany, region: NY, postal_code: "12207", country: US}
work_authorization:
  authorized_countries: [US]
  requires_sponsorship_now: false
  requires_sponsorship_future: false
  visa_status: Citizen
history:
  employment:
    - {employer: Example Capital, title: Financial Analyst, start: "2027-01", end: present}
    - {employer: Sample Bank, title: Operations Associate, start: "2019-06", end: "2026-12"}
  education:
    - {school: Example University, degree: BS, field: Finance, start: "2015-09", end: "2019-05"}
`

var biologyProfile = strings.NewReplacer("Example Capital", "Example Bio", "Financial Analyst", "Research Associate",
	"Sample Bank", "Sample Labs", "Operations Associate", "Lab Technician", "field: Finance", "field: Biology").Replace(financeProfile)

// Every recorded job a software engineer's, and a move from research to data
// science (three years, then four to the fixture's March 2031).
var (
	engineerProfile  = strings.NewReplacer("Financial Analyst", "Senior Software Engineer", "Operations Associate", "Software Engineer II").Replace(financeProfile)
	scientistProfile = strings.NewReplacer(`start: "2027-01", end: present`, `start: "2027-03", end: present`, "Financial Analyst", "Data Scientist",
		`start: "2019-06", end: "2026-12"`, `start: "2024-03", end: "2027-02"`, "Operations Associate", "Research Assistant").Replace(financeProfile)
)

func TestBind_Preview_ApplicantsUnlikeTheOwner(t *testing.T) {
	yesNo := []match.Option{{Value: "Yes", Label: "Yes"}, {Value: "No", Label: "No"}}
	var states []match.Option
	for _, l := range []string{"California", "Florida", "Illinois", "New Jersey", "New York", "North Carolina", "Ohio", "Texas", "Virginia", "Washington"} {
		states = append(states, match.Option{Value: l, Label: l})
	}
	countries := []match.Option{{Value: "United States", Label: "United States"}, {Value: "Canada", Label: "Canada"}, {Value: "United Kingdom", Label: "United Kingdom"}}
	type row struct {
		q     match.Question
		want  string // the answer written; "" when nothing may be written
		avoid string // with no want, an answer that may not be written
		sure  bool   // the want is written at the confidence of a recorded fact
	}
	ask := func(text string, opts []match.Option, want string) row {
		kind := match.KindText
		if len(opts) > 0 {
			kind = match.KindSelect
		}
		return row{q: match.Question{Selector: "#q", Text: text, Kind: kind, Options: opts}, want: want}
	}
	sure := func(text, want string) row {
		return row{q: match.Question{Selector: "#q", Text: text, Kind: match.KindText}, want: want, sure: true}
	}
	not := func(text, avoid string) row {
		return row{q: match.Question{Selector: "#q", Text: text, Kind: match.KindText}, avoid: avoid}
	}
	for _, p := range []struct {
		name, profile, cv, certs string
		rows                     []row
	}{
		{"finance", financeProfile, "# Alex\n\n- Analyzed credit portfolios for the lending desk.\n", "Series 7\nChartered Financial Analyst", []row{
			// The state or country a credential was granted in is not the home's.
			ask("In which state are you registered with FINRA?", states, ""),
			ask("Which state do you live in?", states, "New York"),
			// Years in a named role are not the career's.
			not("How many years of experience do you have as a financial analyst?", "10"),
			ask("How many years of experience do you have as a whole?", nil, "10"),
			// A class described in common words is not a No; a credential named
			// and not listed is.
			ask("Do you hold a professional certification in this field?", yesNo, ""),
			ask("Do you hold a nursing license in California?", yesNo, ""),
			ask("Are you registered as disabled under the Equality Act?", yesNo, ""),
			ask("Do you hold a PMP certification?", yesNo, "No"),
			// A licence or charter held is read from the list.
			ask("Do you hold the Series 7 license?", yesNo, "Yes"),
			ask("Are you a CFA charterholder?", yesNo, "Yes"),
			ask("Are you a licensed CPA?", yesNo, "No"),
			// "Analyzed" mentions analysis, so its years are not none.
			not("How many years of experience do you have in data analysis?", "0"),
		}},
		{"life science", biologyProfile, "# Alex\n\n- Analyzed RNA-seq results with statistics.\n", "Certified Public Accountant", []row{
			ask("State of licensure", states, ""),
			ask("Are you a licensed CPA?", yesNo, "Yes"),
			not("How many years of experience do you have with statistical analysis?", "0"),
			// A career changer's years in another role are not the career's.
			not("How many years of experience do you have as a financial analyst?", "10"),
		}},
		// Years in a role the recorded titles name are those jobs' years, or
		// the career's when every job is one.
		{"software engineer", engineerProfile, "# Alex\n\n- Built the billing service in Go.\n", "", []row{
			sure("How many years of experience do you have as a software engineer?", "10"),
		}},
		{"data scientist", scientistProfile, "# Alex\n\n- Built churn models.\n", "", []row{
			sure("How many years of experience do you have as a data scientist?", "4"),
			sure("How many years of experience do you have as a research assistant?", "3"),
		}},
		{"software", alexSponsored, "# Alex\n\n- Built the billing service in Go.\n", "", []row{
			ask("Which country issued your licence?", countries, ""),
			// A U.S. status says nothing of another country's.
			ask("Are you a Canadian citizen or permanent resident?", yesNo, ""),
			ask("Are you a U.S. citizen or permanent resident?", yesNo, "Yes"),
			// An "Engineer" title is a mention of the role.
			not("How many years of experience do you have as a software engineer?", "0"),
		}},
	} {
		t.Run(p.name, func(t *testing.T) {
			f := newFixture(t, p.profile)
			f.write(t, workspace.FileCV, p.cv)
			loaded, err := f.bank.File()
			if err != nil {
				t.Fatal(err)
			}
			b := loaded.Bank
			for i := range b.Entries {
				switch e := &b.Entries[i]; e.ID {
				case "certifications-held":
					if p.certs != "" {
						e.Answer, e.Placeholder = p.certs, false
					}
				case match.IDYearsFullTimeWork:
					e.Answer, e.Placeholder = "10", false
				}
			}
			if _, err := f.bank.Save(b, loaded.Revision); err != nil {
				t.Fatal(err)
			}
			ev := &fakeEvidence{sections: []kb.OwnSection{{Ref: "cv.md", Text: p.cv}}}
			for _, r := range p.rows {
				out, err := f.binding(func(d *Deps) { d.Evidence = ev }).Preview(context.Background(),
					Request{Host: "jobs.example.com", Posting: Posting{Location: "New York, NY"}, Questions: []match.Question{r.q}})
				if err != nil {
					t.Fatal(err)
				}
				got := out.Results[0]
				written := got.Outcome == match.OutcomeFilled || got.Outcome == match.OutcomeFilledReview
				if r.avoid != "" && got.Answer == r.avoid || r.avoid == "" && (r.want == "" && written || r.want != "" && (!written || got.Answer != r.want || r.sure && got.Confidence < 0.95)) {
					t.Errorf("%q: want %q (not %q), got %s %q from %q at %.2f", r.q.Text, r.want, r.avoid, got.Outcome, got.Answer, got.EntryID, got.Confidence)
				}
			}
		})
	}
}

// The owner's replayed "as a software engineer" over None / 1 year / 2 years /
// 3+ years: five months as a software engineer beside an internship, a
// recorded total of one year. A span under a year is the least non-none
// option, never "None" and never the open "3+ years".
func TestBind_Preview_RoleYearsOnAnIntegerLadder(t *testing.T) {
	profile := strings.NewReplacer(`title: Financial Analyst, start: "2027-01"`, `title: Software Engineer, start: "2026-06"`,
		`title: Operations Associate, start: "2019-06", end: "2026-12"`, `title: Researcher Intern, start: "2023-07", end: "2023-09"`).Replace(financeProfile)
	f := newFixture(t, profile)
	f.now = time.Date(2026, 11, 16, 10, 0, 0, 0, time.UTC)
	f.write(t, workspace.FileCV, "# Alex\n\n- Built the billing service in Go.\n")
	loaded, err := f.bank.File()
	if err != nil {
		t.Fatal(err)
	}
	b := loaded.Bank
	for i := range b.Entries {
		if e := &b.Entries[i]; e.ID == match.IDYearsFullTimeWork {
			e.Answer, e.Placeholder = "1", false
		}
	}
	if _, err := f.bank.Save(b, loaded.Revision); err != nil {
		t.Fatal(err)
	}
	var opts []match.Option
	for _, l := range []string{"None", "1 year", "2 years", "3+ years"} {
		opts = append(opts, match.Option{Value: l, Label: l})
	}
	ev := &fakeEvidence{sections: []kb.OwnSection{{Ref: "cv.md", Text: "Built the billing service in Go."}}}
	out, err := f.binding(func(d *Deps) { d.Evidence = ev }).Preview(context.Background(), Request{Host: "jobs.example.com", Questions: []match.Question{
		{Selector: "#q", Text: "How many years of experience do you have as a software engineer?", Kind: match.KindCustomListbox, Options: opts}}})
	if err != nil {
		t.Fatal(err)
	}
	if r := out.Results[0]; r.ChosenLabel != "1 year" || r.Confidence < 0.95 {
		t.Fatalf("got %s %q at %.2f: %v", r.Outcome, r.ChosenLabel, r.Confidence, r.Trace)
	}
}
