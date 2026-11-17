package match_test

import (
	"testing"

	"applysmith/internal/answerbank/match"
)

// Vultr (Ashby): "Are you currently employed?" names no employer; it is no
// question of working for the hiring company. A position with no end, or an
// end still ahead, answers Yes; a history whose every position ended is left; the user's own answer wins.
func TestXC_CurrentlyEmployedIsAJobNow(t *testing.T) {
	working := xcBFacts()
	working.Employment = []match.EmploymentFact{{Employer: "Example Labs", Title: "Software Engineer", Start: "2026-06", End: "present"}}
	ended := xcBFacts()
	ended.Employment = []match.EmploymentFact{{Employer: "Example Labs", Title: "Intern", Start: "2023-07", End: "2023-09"}}
	ahead := xcBFacts() // a contract whose recorded end is still ahead of the harness day
	ahead.Employment = []match.EmploymentFact{{Employer: "Example Labs", Title: "Contractor", Start: "2031-01", End: "2031-12"}}
	vultr := match.Posting{Company: "Vultr", Location: "Remote - United States"}
	q := xcBYesNo("Are you currently employed?")
	xcBRunCases(t, []xcBCase{
		{name: "a position with no end", facts: working, posting: vultr, q: q, entry: "currently-employed", label: "Yes", notEntry: "currently-employed-by-this-company"},
		{name: "every position ended", facts: ended, posting: vultr, q: q, wantNone: true},
		{name: "an end still ahead", facts: ahead, posting: vultr, q: q, entry: "currently-employed", label: "Yes"},
		{name: "the user's own answer", sets: map[string]xcBSet{"currently-employed": xcBAns("No")}, facts: working, posting: vultr, q: q, entry: "currently-employed", label: "No"},
	})
	// The same form asks the state of residence in a text box.
	sc := xcBFacts()
	sc.Address = match.AddressFact{City: "Charleston", Region: "SC", Country: "US"}
	xcBRunCases(t, []xcBCase{
		{name: "the state located in", facts: sc, posting: vultr, q: xcBText("Which U.S. State are you located in?"), answer: "South Carolina"},
	})
}

// The posting's own place is the relocation policy's wherever the adjective
// sits: "the listed location" as "the location listed" (MaintainX).
func TestXC_TheListedLocationIsTheRelocationPolicys(t *testing.T) {
	sets := map[string]xcBSet{"office-attendance-ok": xcBAns("Yes"), "willing-to-relocate": xcBAns("Yes")}
	xcBRunCases(t, []xcBCase{
		{name: "adjective first", sets: sets, facts: xcBFacts(), posting: match.Posting{Company: "MaintainX", Location: "Austin, TX (Hybrid)"},
			q: xcBYesNo("Are you able to work in the listed location for this role?"), entry: "willing-to-relocate", label: "Yes"},
	})
}

// EvolutionIQ (Greenhouse) names an H-1B transfer before the visa
// sponsorship: a recorded need is one of the two; a recorded No says nothing
// of the transfer. Qrypt (Lever) asks both work questions in multi-line
// boxes: the recorded authorization is typed, and "to sponsor, file, or
// support any immigration paperwork ..., including but not limited to ..." is
// the sponsorship need.
func TestXC_ATransferNamedFirstAndWorkQuestionsInMultiLineBoxes(t *testing.T) {
	need := xcBFacts()
	need.RequiresSponsorshipNow, need.RequiresSponsorshipFuture = xcBBool(false), xcBBool(true)
	none := xcBFacts()
	none.RequiresSponsorshipNow, none.RequiresSponsorshipFuture = xcBBool(false), xcBBool(false)
	sets := map[string]xcBSet{"sponsorship-now-or-future": xcBAns("Yes"), "work-auth-us": xcBAns("Yes")}
	box := func(text string) match.Question {
		return match.Question{Selector: "s", Kind: match.KindText, InputType: match.InputLongText, Required: true, Text: text}
	}
	const transfer = "Will you now or in the future require a H1-B transfer / visa sponsorship for employment?"
	const paperwork = "Will you now, or at any point in the future, require Qrypt to sponsor, file, or support any immigration paperwork on your behalf, including but not limited to a new H-1B, an H-1B transfer from a current or former employer, an extension, OPT to H-1B conversion, TN or green card sponsorship?"
	evolution := match.Posting{Company: "EvolutionIQ", Location: "New York, NY"}
	qrypt := match.Posting{Company: "Qrypt", Location: "Austin, TX"}
	xcBRunCases(t, []xcBCase{
		{name: "a transfer first, a recorded need", sets: sets, facts: need, posting: evolution, q: xcBYesNo(transfer), entry: "sponsorship-now-or-future", label: "Yes"},
		{name: "a transfer first, no need", sets: sets, facts: none, posting: evolution, q: xcBYesNo(transfer), wantNone: true},
		{name: "authorization in a multi-line box", sets: sets, facts: need, posting: qrypt, q: box("Are you currently eligible to work in the United States of America?"), entry: "work-auth-us", answer: "Yes"},
		{name: "authorization with a follow-up", sets: sets, facts: need, posting: qrypt, q: box("Are you currently eligible to work in the United States of America? If not, please explain."), wantNone: true},
		{name: "an open list of immigration paperwork", sets: sets, facts: need, posting: qrypt, q: box(paperwork), entry: "sponsorship-now-or-future", answer: "Yes"},
	})
}

// CharterUP (Greenhouse) asks the company "to sponsor or petition for an
// employment-based visa or work authorization in order for you to continue
// working for CharterUP": the sponsorship need, read either way.
func TestXC_ASponsorshipOrPetitionForAVisa(t *testing.T) {
	need := xcBFacts()
	need.RequiresSponsorshipNow, need.RequiresSponsorshipFuture = xcBBool(false), xcBBool(true)
	none := xcBFacts()
	none.RequiresSponsorshipNow, none.RequiresSponsorshipFuture = xcBBool(false), xcBBool(false)
	sets := map[string]xcBSet{"sponsorship-now-or-future": xcBAns("Yes")}
	charterup := match.Posting{Company: "CharterUP", Location: "Austin, Texas"}
	q := xcBYesNo("Will you now or at any time in the future require CharterUP to sponsor or petition for an employment-based visa or work authorization in order for you to continue working for CharterUP?")
	xcBRunCases(t, []xcBCase{
		{name: "a recorded need", sets: sets, facts: need, posting: charterup, q: q, entry: "sponsorship-now-or-future", label: "Yes"},
		{name: "no need", sets: sets, facts: none, posting: charterup, q: q, entry: "sponsorship-now-or-future", label: "No"},
	})
}
