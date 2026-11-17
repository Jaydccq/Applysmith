package answerbank

import (
	"fmt"
	"testing"

	"applysmith/internal/answerbank/match"
)

func boolPtr(b bool) *bool { return &b }

// polarity is how a sponsorship question reads.
type polarity int

const (
	plain      polarity = iota // answered with the fact
	negated                    // answered with the complement of the fact
	unreadable                 // double negation, contrast or mixed tense: left to the user
)

// TestSponsorship_NegatedPhrasingIsAnsweredWithTheComplement is the polarity
// requirement of "Work authorization and sponsorship": a negated phrasing is
// answered with the complement of the fact, a wording outside the recognised
// shapes is never filled, and a plain wording keeps the fact. Every phrasing
// is asked under both fact values; a wrong fill is a failure whether or not the
// question is routed to the sponsorship entry, and a phrasing marked fills must
// be answered, not abstained.
func TestSponsorship_NegatedPhrasingIsAnsweredWithTheComplement(t *testing.T) {
	m := match.New(defaultBank(t), match.DefaultStages())
	cases := []struct {
		text  string
		read  polarity
		fills bool
	}{
		// Plain wordings keep the fact.
		{"Do you require sponsorship?", plain, true},
		{"Will you now or in the future require sponsorship?", plain, true},
		{"Do you currently hold a visa that will require sponsorship?", plain, true},
		{"Will you need visa sponsorship to work in the United States?", plain, true},
		// An example list in parentheses and "to continue working" add no polarity.
		{"Will you now or in the future require sponsorship for employment visa status (e.g., F1, H-1B visa status)?", plain, true},
		{"Do you now or in the future require visa sponsorship to continue working in the United States? *", plain, true},
		{"Will you now or in the future require sponsorship for an employment-based visa status?", plain, true},
		// An equal-opportunity note after the question tells nothing about the answer (American Express on Oracle).
		{"Will you now or in the future require employment sponsorship (e.g., visa, work permit) in the United States? Note: The Company, in its sole discretion determines which positions are eligible for sponsorship based on business unit, band/level and /or job responsibilities. Sponsorship decisions are based on job related criteria and applicable immigration laws and not on an applicant's national origin, citizenship or other protected characteristics.", plain, true},
		// "That country" is the one the question before it named, the posting's (NVIDIA on Workday).
		{"Will you require employer support to obtain or maintain authorization to work in that country? e.g. (work permit)", plain, true},
		{"Are you legally authorized to work in the US without restriction, and will you require sponsorship?", plain, false},
		{"Do you require sponsorship for a non-immigrant visa?", plain, false},
		// Negated wordings take the complement.
		{"Are you able to work in the US without sponsorship?", negated, true},
		// The company's own "we do not sponsor" notices around the question set the polarity of nothing.
		{"We do not sponsor visas, including H-1B, H-1B transfers, OPT extensions (STEM or otherwise), or any other work authorization. Are you able to work in the US without any form of sponsorship? Note: Example Corp is not sponsoring or transferring work visas at this time.", negated, true},
		{"We do not sponsor visas. Do you require sponsorship?", plain, true},
		{"Do you NOT require sponsorship?", negated, true},
		{"Will you not now or in the future require sponsorship?", negated, true},
		{"Is it true that you will never require visa sponsorship?", negated, true},
		{"Can you work in the US without the need of visa sponsorship?", negated, true},
		{"Can you work in the US without the need for visa sponsorship?", negated, true},
		{"Can you work without needing a work permit or visa sponsorship?", negated, true},
		{"Can you work here without an employer sponsoring your work visa?", negated, true},
		{"Are you eligible to work in the US with no sponsorship required?", negated, true},
		{"Are you authorized to work in the US without sponsorship now and in the future?", negated, true},
		{"Do you not need sponsorship?", negated, true},
		{"Is no visa sponsorship needed?", negated, false},
		{"Would you be able to work for us without requiring sponsorship?", negated, false},
		{"Are you able to work in the United States without being sponsored by an employer?", negated, false},
		// Wordings that cannot be classified confidently are never filled.
		{"Is it not true that you will never require sponsorship?", unreadable, false},
		{"Are you unable to work without sponsorship?", unreadable, false},
		{"Do you not currently require sponsorship, but will you in the future?", unreadable, false},
		{"Do you currently not require sponsorship but will need it later?", unreadable, false},
		{"Do you not currently require sponsorship, or will you require it in the future?", unreadable, false},
		{"Select No if you do not require sponsorship.", unreadable, false},
		{"Will you never need sponsorship now, however you may later?", unreadable, false},
		// A transition question ("no longer") is not one of the recognised shapes.
		{"Will you no longer require sponsorship?", unreadable, false},
		// The review's five: each read as a plain or wrongly negated question
		// while the predicate or the "or" decided the answer.
		{"Is visa sponsorship unnecessary for you?", unreadable, false},
		{"Are you independent of visa sponsorship?", unreadable, false},
		{"Is it false that you require sponsorship?", unreadable, false},
		{"Is visa sponsorship something you can do without?", unreadable, false},
		{"Can you work without restriction or will you need sponsorship?", unreadable, false},
		// Adversarial wordings: an unknown predicate, a second clause, a
		// disjunction, a comparison or a hypothetical is never a fill.
		{"Is sponsorship something you will never need?", unreadable, false},
		{"Do you require sponsorship, or are you exempt from it?", unreadable, false},
		{"Are you free of any need for sponsorship?", unreadable, false},
		{"Do you lack the need for sponsorship?", unreadable, false},
		{"Is sponsorship irrelevant to you?", unreadable, false},
		{"Is it untrue that you need visa sponsorship?", unreadable, false},
		{"Would you require sponsorship if we hired you?", unreadable, false},
		{"Do you require sponsorship more than a colleague would?", unreadable, false},
		{"Do you need sponsorship less than your peers do?", unreadable, false},
		{"Are you able to work without sponsorship, and do you hold a green card?", unreadable, false},
		{"Are you able to work without sponsorship or will you need it later?", unreadable, false},
		{"Do you require sponsorship, whether now or later?", unreadable, false},
		{"Is sponsorship the opposite of what you need?", unreadable, false},
		{"Is it untrue that you do not require sponsorship?", unreadable, false},
		{"Would you decline sponsorship if it were offered?", unreadable, false},
		{"Can you work without sponsorship unless the role is on-site?", unreadable, false},
		{"Are you not able to work without sponsorship?", unreadable, false},
		{"Do you require sponsorship, not counting a student visa?", unreadable, false},
		{"Do you require no sponsorship?", unreadable, false},
		{"Is it not the case that you need sponsorship?", unreadable, false},
		{"Do you need sponsorship to work in the US, yes or no?", unreadable, false},
		{"Do you require sponsorship now and not later?", unreadable, false},
		{"Do you not not require sponsorship?", unreadable, false},
		// A notice that conditions or addresses the applicant, and a carve-out in an example list, are not set aside.
		{"We do not sponsor visas unless you are a current employee. Do you require sponsorship?", unreadable, false},
		{"We do not sponsor visas, but you may ask. Do you require sponsorship?", unreadable, false},
		{"Will you require sponsorship for employment visa status (not including H-1B)?", unreadable, false},
	}
	for _, f := range []struct {
		name string
		need bool
	}{{"both facts true", true}, {"both facts false", false}} {
		facts := match.Facts{AuthorizedCountries: []string{"US"}, RequiresSponsorshipNow: boolPtr(f.need), RequiresSponsorshipFuture: boolPtr(f.need)}
		for _, c := range cases {
			t.Run(fmt.Sprintf("%s/%s", f.name, c.text), func(t *testing.T) {
				resp, err := m.Match(match.Request{Host: "jobs.example.com", Facts: facts, Posting: match.Posting{Location: "Austin, TX"}, Questions: []match.Question{
					{Selector: "#q", Text: c.text, Kind: match.KindRadioGroup, Options: yesNo, Required: true}}})
				if err != nil {
					t.Fatal(err)
				}
				r := resp.Results[0]
				answerYes := f.need
				if c.read == negated {
					answerYes = !f.need
				}
				want := "No"
				if answerYes {
					want = "Yes"
				}
				switch {
				case c.read == unreadable:
					if r.Outcome != match.OutcomeAbstained {
						t.Fatalf("an unreadable wording must be left to the user: %s %q at %.2f", r.Outcome, r.ChosenLabel, r.Confidence)
					}
				case r.Outcome == match.OutcomeAbstained:
					if c.fills {
						t.Fatalf("expected %q, got an abstain (%s)", want, r.Reason)
					}
				case r.ChosenLabel != want:
					t.Fatalf("expected %q, got %s %q at %.2f", want, r.Outcome, r.ChosenLabel, r.Confidence)
				}
			})
		}
	}
}

// TestSponsorship_UnrecognisedWordingAbstainsAmbiguous is the closed-shape
// requirement: a sponsorship question outside the recognised shapes is the
// non-terminal ambiguous abstain at confidence 0, whichever facts are recorded.
func TestSponsorship_UnrecognisedWordingAbstainsAmbiguous(t *testing.T) {
	m := match.New(defaultBank(t), match.DefaultStages())
	for _, text := range []string{
		"Is visa sponsorship unnecessary for you?",
		"Are you independent of visa sponsorship?",
		"Is it false that you require sponsorship?",
		"Is visa sponsorship something you can do without?",
		"Can you work without restriction or will you need sponsorship?",
	} {
		for _, need := range []bool{true, false} {
			facts := match.Facts{AuthorizedCountries: []string{"US"}, RequiresSponsorshipNow: boolPtr(need), RequiresSponsorshipFuture: boolPtr(need)}
			resp, err := m.Match(match.Request{Host: "jobs.example.com", Facts: facts, Questions: []match.Question{
				{Selector: "#q", Text: text, Kind: match.KindRadioGroup, Options: yesNo, Required: true}}})
			if err != nil {
				t.Fatal(err)
			}
			r := resp.Results[0]
			if r.Outcome != match.OutcomeAbstained || r.Reason != match.ReasonAmbiguous || r.Confidence != 0 || r.Terminal {
				t.Errorf("%q (need=%t): %s %q reason=%s confidence=%.2f terminal=%t", text, need, r.Outcome, r.ChosenLabel, r.Reason, r.Confidence, r.Terminal)
			}
		}
	}
}

// TestSponsorship_CorpusFillRate counts what the closed reading costs the
// common case: the ATS wordings in testdata are asked under three fact states,
// and the plain, explicitly negated and compound ones must still fill. A
// compound question is left alone when its two clauses would answer differently
// (a user authorized and needing no sponsorship), which is one fact state in three.
func TestSponsorship_CorpusFillRate(t *testing.T) {
	filled, claimed, total := sponsorshipCorpusFillRate(t)
	t.Logf("sponsorship entry claimed %d of %d question/fact-state pairs and filled %d", claimed, total, filled)
	if claimed == 0 || float64(filled)/float64(claimed) < 0.75 {
		t.Fatalf("the closed reading fills %d of the %d pairs the sponsorship entry claimed", filled, claimed)
	}
}
