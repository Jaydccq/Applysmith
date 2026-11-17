package answerbank

import (
	"testing"

	"applysmith/internal/answerbank/match"
)

func opts(labels ...string) []match.Option {
	out := make([]match.Option, len(labels))
	for i, l := range labels {
		out[i] = match.Option{Value: l, Label: l}
	}
	return out
}

func sponsorFacts(now, future bool) match.Facts {
	return match.Facts{AuthorizedCountries: []string{"US"}, RequiresSponsorshipNow: boolPtr(now), RequiresSponsorshipFuture: boolPtr(future)}
}

// TestWorkAuthCompound_AnswersOnlyWhenTheOptionsMakeItUnambiguous is the
// compound-question requirement of "Work authorization and sponsorship": one
// Yes/No cannot answer two clauses unless both have the same answer; options
// that state both facts, or that answer each clause in turn, can.
func TestWorkAuthCompound_AnswersOnlyWhenTheOptionsMakeItUnambiguous(t *testing.T) {
	const both = "Are you legally authorized to work in the US and will you now or in the future require sponsorship?"
	stating := opts(
		"Yes, I am authorized to work in the US and do not require sponsorship",
		"Yes, I am authorized to work in the US and will require sponsorship now or in the future",
		"No, I am not authorized to work in the US",
	)
	pairs := opts("Yes / Yes", "Yes / No", "No / Yes", "No / No")
	cases := []struct {
		name    string
		stored  string
		facts   match.Facts
		text    string
		options []match.Option
		want    string // chosen label; empty: abstain
	}{
		{"bare Yes: both clauses Yes", "Yes", sponsorFacts(true, true), both, yesNo, "Yes"},
		{"bare Yes: only the future fact", "Yes", sponsorFacts(false, true), both, yesNo, "Yes"},
		{"bare: clauses differ", "Yes", sponsorFacts(false, false), both, yesNo, ""},
		{"bare No: both clauses No", "No", sponsorFacts(false, false), both, yesNo, "No"},
		{"bare: not authorized but sponsored", "No", sponsorFacts(true, true), both, yesNo, ""},
		{"sponsorship first", "Yes", sponsorFacts(true, true), "Will you require sponsorship and are you authorized to work in the US?", yesNo, "Yes"},
		{"second clause without a subject", "Yes", sponsorFacts(true, true), "Are you authorized to work in the US and require sponsorship?", yesNo, "Yes"},
		{"negated sponsorship clause", "Yes", sponsorFacts(false, false), "Are you authorized to work in the US and will you not require sponsorship?", yesNo, "Yes"},
		{"future clause reads the future fact", "Yes", sponsorFacts(true, false), "Are you currently authorized to work in the US and will you require sponsorship in the future?", yesNo, ""},
		{"future clause with a future need", "Yes", sponsorFacts(false, true), "Are you currently authorized to work in the US and will you require sponsorship in the future?", yesNo, "Yes"},
		{"options state both facts: no need", "Yes", sponsorFacts(false, false), both, stating, stating[0].Label},
		{"options state both facts: needed", "Yes", sponsorFacts(false, true), both, stating, stating[1].Label},
		{"options state both facts: unset fact", "Yes", match.Facts{AuthorizedCountries: []string{"US"}}, both, stating, ""},
		{"a user who is not authorized never gets the authorized options", "No", sponsorFacts(false, false), both, stating, ""},
		{"options state only the other facts", "Yes", sponsorFacts(false, false), both, stating[1:2], ""},
		{"an option that opens with No is not read", "No", sponsorFacts(false, false), both, opts("No, I am not authorized to work in the US and do not require sponsorship", "Yes"), ""},
		{"pair options in clause order", "Yes", sponsorFacts(false, false), both, pairs, "Yes / No"},
		{"options that answer neither way", "Yes", sponsorFacts(true, true), both, opts("Authorized", "Not authorized"), ""},
		{"recorded answer missing", "", sponsorFacts(true, true), both, yesNo, ""},
		{"a clause outside the shapes", "Yes", sponsorFacts(true, true), "Are you authorized to work in the US without restriction and will you require sponsorship?", yesNo, ""},
		{"three clauses", "Yes", sponsorFacts(true, true), "Are you authorized to work in the US and over 18 and will you require sponsorship?", yesNo, ""},
		{"two work-authorization clauses", "Yes", sponsorFacts(true, true), "Are you authorized to work in the US and eligible to work in the UK?", yesNo, ""},
		{"time qualifier is not a conjunction", "Yes", sponsorFacts(true, true), "Do you require sponsorship now and in the future?", yesNo, "Yes"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			m := match.New(bankAnswering(t, c.stored), match.DefaultStages())
			r := askYesNo(t, m, c.facts, c.text, c.options)
			if c.want == "" {
				if r.Outcome != match.OutcomeAbstained {
					t.Fatalf("expected an abstain, got %s %q at %.2f", r.Outcome, r.ChosenLabel, r.Confidence)
				}
				return
			}
			if r.Outcome != match.OutcomeFilled || r.ChosenLabel != c.want {
				t.Fatalf("expected %q, got %s %q (%s)", c.want, r.Outcome, r.ChosenLabel, r.Reason)
			}
		})
	}
}

// TestWorkAuthCompound_AmbiguousAbstainIsNonTerminal: the refusal to answer two
// clauses with one word is the ambiguous abstain at confidence 0.
func TestWorkAuthCompound_AmbiguousAbstainIsNonTerminal(t *testing.T) {
	m := match.New(bankAnswering(t, "Yes"), match.DefaultStages())
	r := askYesNo(t, m, sponsorFacts(false, false),
		"Are you legally authorized to work in the US and will you now or in the future require sponsorship?", yesNo)
	if r.Outcome != match.OutcomeAbstained || r.Reason != match.ReasonAmbiguous || r.Confidence != 0 || r.Terminal {
		t.Fatalf("%s %q reason=%s confidence=%.2f terminal=%t", r.Outcome, r.ChosenLabel, r.Reason, r.Confidence, r.Terminal)
	}
}

// TestWorkAuthCompound_PickerNeverContradictsTheRecordedAuthorization: the
// three-standing sentence picker follows the sponsorship facts, but an
// option that states the work-authorization fact the other way round from the
// user's recorded answer is never chosen.
func TestWorkAuthCompound_PickerNeverContradictsTheRecordedAuthorization(t *testing.T) {
	labels := opts(
		"I am authorized to work lawfully in the United States and DO NOT require future sponsorship",
		"I am authorized to work lawfully in the United States and DO require future sponsorship",
		"I am NOT authorized to work in the United States",
	)
	const text = "Are you authorized to work lawfully in the United States and will you require future sponsorship?"
	cases := []struct {
		name, stored string
		facts        match.Facts
		want         string
	}{
		{"no recorded answer: the sponsorship facts decide", "", sponsorFacts(false, true), labels[1].Label},
		{"recorded authorized, no need", "Yes", sponsorFacts(false, false), labels[0].Label},
		{"recorded authorized, future need", "Yes", sponsorFacts(false, true), labels[1].Label},
		{"recorded not authorized, no need", "No", sponsorFacts(false, false), ""},
		{"recorded not authorized, future need", "No", sponsorFacts(false, true), ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			m := match.New(bankAnswering(t, c.stored), match.DefaultStages())
			resp, err := m.Match(match.Request{Host: "jobs.example.com", Facts: c.facts, Questions: []match.Question{
				{Selector: "#q", Text: text, Kind: match.KindSelect, Options: labels, Required: true}}})
			if err != nil {
				t.Fatal(err)
			}
			r := resp.Results[0]
			if c.want == "" {
				if r.Outcome != match.OutcomeAbstained {
					t.Fatalf("expected an abstain, got %s %q", r.Outcome, r.ChosenLabel)
				}
				return
			}
			if r.Outcome != match.OutcomeFilled || r.ChosenLabel != c.want {
				t.Fatalf("expected %q, got %s %q (%s)", c.want, r.Outcome, r.ChosenLabel, r.Reason)
			}
		})
	}
}

// TestWorkAuth_QuestionBehindASponsorshipNotice is the F-10 rule of "Work
// authorization and sponsorship": behind the employer's notice that the role
// needs authorization already held, or that it does not sponsor, the question
// asks whether the applicant can work there as they are. The positive rows are
// the Ashby wording; the near miss is a plain question after a statement that
// is no notice, which keeps the recorded answer whatever the sponsorship need.
func TestWorkAuth_QuestionBehindASponsorshipNotice(t *testing.T) {
	const notice = "This role requires you to already be legally authorized to work in the countries listed in the job posting and Northwind Example will not be offering work authorization, relocation assistance, or sponsorship/transferring of a visa. Do you live in, and are you legally authorized to work in the countries listed in the job posting?"
	const usOrCanada = "Remote (United States | Canada)"
	us, ca := match.AddressFact{Country: "US"}, match.AddressFact{Country: "CA"}
	no, yes := boolPtr(false), boolPtr(true)
	cases := []struct {
		name, stored, location, text string
		home                         match.AddressFact
		now                          *bool
		want                         string // empty: abstain
	}{
		{"authorized, no sponsorship now", "Yes", usOrCanada, notice, us, no, "Yes"},
		{"sponsorship needed now", "Yes", usOrCanada, notice, us, yes, "No"},
		{"recorded not authorized", "No", usOrCanada, notice, us, no, "No"},
		{"lives outside the listed countries", "Yes", "Austin, TX", notice, ca, no, "No"},
		{"no location: presumed in the United States", "Yes", "", notice, us, no, "Yes"},
		{"current sponsorship fact unset", "Yes", usOrCanada, notice, us, nil, ""},
		{"no recorded country of residence", "Yes", usOrCanada, notice, match.AddressFact{}, no, ""},
		{"a posting placed in no country", "Yes", "Remote", notice, us, no, ""},
		{"we do not sponsor", "Yes", "Austin, TX", "We do not sponsor visas. Are you authorized to work in the US?", us, yes, "No"},
		{"near miss: a plain question after a plain statement", "Yes", "Austin, TX", "Our team works remotely. Are you legally authorized to work in the US?", us, yes, "Yes"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			m := match.New(bankAnswering(t, c.stored), match.DefaultStages())
			facts := match.Facts{AuthorizedCountries: []string{"US"}, RequiresSponsorshipNow: c.now, RequiresSponsorshipFuture: yes, Address: c.home}
			resp, err := m.Match(match.Request{Host: "jobs.example.com", Facts: facts,
				Posting:   match.Posting{Company: "Northwind Example", Location: c.location},
				Questions: []match.Question{{Selector: "#q", Text: c.text, Kind: match.KindRadioGroup, Options: yesNo, Required: true}}})
			if err != nil {
				t.Fatal(err)
			}
			r := resp.Results[0]
			if c.want == "" {
				if r.Outcome != match.OutcomeAbstained {
					t.Fatalf("expected an abstain, got %s %q", r.Outcome, r.ChosenLabel)
				}
				return
			}
			if r.Outcome != match.OutcomeFilled || r.ChosenLabel != c.want {
				t.Fatalf("expected %q, got %s %q (%s)", c.want, r.Outcome, r.ChosenLabel, r.Reason)
			}
		})
	}
}

// A work-authorization status picker whose one requirement option states the
// recorded need is answered from the need alone, also where no authorized
// country is recorded: a user who needs sponsorship needs it there too. A user
// who needs none, or a picker with two requirement options, is still left to
// the user when no authorized country covers the posting (Hover on Lever).
func TestWorkAuthStatus_TheSoleRequirementOptionAnswersAUserWhoNeedsSponsorship(t *testing.T) {
	hover := opts("1. I am authorized to work for any employer in the country where the position is based without any renewal or sponsorship.",
		"2. I require/will require Hover's sponsorship to obtain work authorization in the country in which this position is based (e.g. H1-B, TN, etc)",
		"3. My status to work in the country in which this position is based is unknown.")
	twoNeeds := append(hover[:2:2], match.Option{Label: "I will require sponsorship in the future", Value: "later"})
	needs := match.Facts{RequiresSponsorshipNow: boolPtr(true), RequiresSponsorshipFuture: boolPtr(true)}
	noNeed := match.Facts{RequiresSponsorshipNow: boolPtr(false), RequiresSponsorshipFuture: boolPtr(false)}
	b := defaultBank(t)
	for i := range b.Entries {
		if b.Entries[i].ID == "us-work-authorization-status" {
			b.Entries[i].Preferred, b.Entries[i].Placeholder = []string{"F-1 OPT"}, false
		}
	}
	m := match.New(b, match.DefaultStages())
	for _, c := range []struct {
		name    string
		facts   match.Facts
		options []match.Option
		want    string // chosen label; empty: abstain
	}{
		{"needs sponsorship, no authorized country", needs, hover, hover[1].Label},
		{"needs none, no authorized country", noNeed, hover, ""},
		{"two requirement options", needs, twoNeeds, ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			resp, err := m.Match(match.Request{Host: "hover.to", Facts: c.facts, Posting: match.Posting{Location: "Denver, CO"}, Questions: []match.Question{
				{Selector: "#q", Text: "Work Authorization", Kind: match.KindCustomListbox, InputType: match.InputSingleSelect, Options: c.options, Required: true}}})
			if err != nil {
				t.Fatal(err)
			}
			r := resp.Results[0]
			if c.want == "" {
				if r.Outcome != match.OutcomeAbstained {
					t.Fatalf("expected an abstain, got %s %q", r.Outcome, r.ChosenLabel)
				}
				return
			}
			if r.Outcome == match.OutcomeAbstained || r.ChosenLabel != c.want {
				t.Fatalf("expected %q, got %s %q (%s) %q", c.want, r.Outcome, r.ChosenLabel, r.Reason, r.Trace)
			}
		})
	}
}
