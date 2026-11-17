package answerbank

import (
	"fmt"
	"testing"

	"applysmith/internal/answerbank/match"
)

// bankAnswering is the default bank with the user's own answer recorded in the
// work-authorization entries.
func bankAnswering(t *testing.T, answer string) match.Bank {
	t.Helper()
	b := defaultBank(t)
	entries := append([]match.Entry(nil), b.Entries...)
	for i := range entries {
		switch entries[i].ID {
		case "work-auth-us", "work-auth-us-any-employer":
			entries[i].Answer = answer
		}
	}
	b.Entries = entries
	return b
}

func askYesNo(t *testing.T, m *match.Matcher, facts match.Facts, text string, opts []match.Option) match.Result {
	t.Helper()
	resp, err := m.Match(match.Request{Host: "jobs.example.com", Facts: facts, Posting: match.Posting{Location: "Austin, TX"}, Questions: []match.Question{
		{Selector: "#q", Text: text, Kind: match.KindRadioGroup, Options: opts, Required: true}}})
	if err != nil {
		t.Fatal(err)
	}
	return resp.Results[0]
}

// TestWorkAuth_NegatedPhrasingIsAnsweredWithTheComplement is the closed-shape
// requirement of "Work authorization and sponsorship" for the authorization
// fact: a plain question takes the user's own answer, an explicitly negated one
// its complement, and every phrasing is asked with the user recorded as both
// authorized and not authorized.
func TestWorkAuth_NegatedPhrasingIsAnsweredWithTheComplement(t *testing.T) {
	cases := []struct {
		text    string
		negated bool
		// fills is false for a wording the classifier does not route to the
		// work-authorization entry at all; it must still never be filled wrongly.
		fills bool
	}{
		{"Are you legally authorized to work in the US?", false, true},
		{"Are you authorized to work in the United States?", false, true},
		{"Are you eligible to work in the US?", false, true},
		{"Are you legally permitted to work in the United States?", false, true},
		{"Are you currently authorized to work in the US?", false, true},
		{"Do you have authorization to work in the US?", false, true},
		{"Do you have the right to work in the United States?", false, true},
		{"Are you legally allowed to work in the U.S.?", false, false},
		{"Will you be legally authorized to work in the US?", false, true},
		{"Is it true that you are authorized to work in the US?", false, true},
		{"Can you legally work in the United States?", false, true},
		{"Are you legally entitled to work in the United States?", false, true},
		{"Are you authorized to work in the US for any employer?", false, true},
		{"US work authorization *", false, true},
		{"Are you authorized to work lawfully in the country to which you are applying?", false, true},
		// An employer's legal paragraph in front of the question is not part of it.
		{"In compliance with federal law, all persons hired will be required to verify identity and eligibility to work in the United States and to complete the required employment eligibility verification form upon hire. Are you authorized to work legally in the United States?", false, true},
		{"In compliance with federal law, all persons hired will be required to verify identity and eligibility to work in the United States. Are you not authorized to work legally in the United States?", true, true},
		// Proof of authorization is the authorization (owner decision).
		{"Can you provide proof that you are authorized to work in the US?", false, true},
		{"Can you show proof of your right to work in the United States?", false, true},
		// A question asked first, then a note that does not speak to the applicant (CVS).
		{"If hired, can you provide proof that you are legally able to work in the United States? In compliance with federal law, all persons hired will be required to verify identity and eligibility to work in the United States and to complete the required employment eligibility document form upon hire.", false, true},
		{"Can you provide proof that you are not authorized to work in the US?", true, true},
		{"Are you not legally authorized to work in the US?", true, true},
		{"Are you not authorized to work in the United States?", true, true},
		{"Are you not eligible to work in the US?", true, true},
		{"Are you not currently authorized to work in the US?", true, true},
		{"Are you currently not authorized to work in the US?", true, true},
		{"Do you not have authorization to work in the US?", true, true},
		{"Do you not have the right to work in the United States?", true, true},
		{"Are you not legally permitted to work in the US?", true, false},
		{"Are you not legally allowed to work in the U.S.?", true, false},
		{"Is it true that you are not authorized to work in the US?", true, true},
		{"Are you never authorized to work in the United States?", true, true},
		{"Do you currently not have authorization to work in the US?", true, true},
	}
	for _, stored := range []string{"Yes", "No"} {
		m := match.New(bankAnswering(t, stored), match.DefaultStages())
		for _, c := range cases {
			t.Run(fmt.Sprintf("answer %s/%s", stored, c.text), func(t *testing.T) {
				want := stored
				if c.negated {
					want = map[string]string{"Yes": "No", "No": "Yes"}[stored]
				}
				r := askYesNo(t, m, match.Facts{AuthorizedCountries: []string{"US"}}, c.text, yesNo)
				switch {
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

// TestWorkAuth_UnrecognisedWordingAbstains is the fail-closed half: with the
// user's answer recorded as Yes, a wording outside the recognised shapes is
// never filled, whatever route claimed it.
func TestWorkAuth_UnrecognisedWordingAbstains(t *testing.T) {
	adversarial := []string{
		"Is it false that you are authorized to work in the US?",
		"Do you lack authorization to work in the US?",
		"Are you unauthorized to work in the US?",
		"Are you ineligible to work in the US?",
		"Are you prohibited from working in the US?",
		"Is it untrue that you are authorized to work in the US?",
		"Are you not unauthorized to work in the US?",
		"Are you not not authorized to work in the US?",
		"Are you authorized to work in the US? Select No if you are not.",
		// A preamble that instructs or conditions is not set aside.
		"Select No if you hold a visa that expires. Are you authorized to work in the US?",
		"If you are a student, answer carefully. Are you authorized to work in the US?",
		"Are you authorized to work in the US or do you hold a green card?",
		"Are you authorized to work in the US and over 18?",
		"Are you authorized to work in the US if sponsored by an employer?",
		"If you are authorized to work in the US, select Yes.",
		"Are you authorized to work in the US more than a colleague would be?",
		"Is working in the US something you are authorized to do?",
		"Is it not the case that you are authorized to work in the US?",
		"Would you decline authorization to work in the US?",
		"Are you unable to work in the US without authorization?",
		"Do you have no authorization to work in the US?",
		"Are you authorized to work in the US, yes or no?",
		"You are not authorized to work in the US, correct?",
		"Are you free of any restriction on working in the US?",
		"Are you lacking work authorization for the US?",
		"Is work authorization unnecessary for you?",
		"Have you ever been unauthorized to work in the US?",
		"Are you authorized to work in the US without restriction?",
		"Are you authorized to work in the US only on a temporary basis?",
		"Are you the opposite of unauthorized to work in the US?",
		"Are you authorized to work in the US less than your peers?",
		"If hired, can you provide proof that you are legally able to work in the United States? Answer No if your status expires.",
		"Can you provide proof of your degree?",
	}
	if len(adversarial) < 20 {
		t.Fatalf("only %d adversarial wordings", len(adversarial))
	}
	m := match.New(bankAnswering(t, "Yes"), match.DefaultStages())
	for _, text := range adversarial {
		t.Run(text, func(t *testing.T) {
			r := askYesNo(t, m, match.Facts{AuthorizedCountries: []string{"US"}, RequiresSponsorshipNow: boolPtr(false), RequiresSponsorshipFuture: boolPtr(false)}, text, yesNo)
			if r.Outcome != match.OutcomeAbstained {
				t.Fatalf("an unrecognised wording must be left to the user: %s %q at %.2f", r.Outcome, r.ChosenLabel, r.Confidence)
			}
		})
	}
}

// TestWorkAuth_UnrecognisedWordingAbstainsAmbiguous: the headline fail-open
// wordings are the non-terminal ambiguous abstain at confidence 0, for either
// recorded answer.
func TestWorkAuth_UnrecognisedWordingAbstainsAmbiguous(t *testing.T) {
	for _, stored := range []string{"Yes", "No"} {
		m := match.New(bankAnswering(t, stored), match.DefaultStages())
		for _, text := range []string{
			"Is it false that you are authorized to work in the US?",
			"Do you lack authorization to work in the US?",
		} {
			r := askYesNo(t, m, match.Facts{AuthorizedCountries: []string{"US"}}, text, yesNo)
			if r.EntryID != "work-auth-us" || r.Outcome != match.OutcomeAbstained || r.Reason != match.ReasonAmbiguous || r.Confidence != 0 || r.Terminal {
				t.Errorf("%q (answer %s): entry=%s %s %q reason=%s confidence=%.2f terminal=%t", text, stored, r.EntryID, r.Outcome, r.ChosenLabel, r.Reason, r.Confidence, r.Terminal)
			}
		}
	}
}

// TestWorkAuth_NegatedQuestionNeedsAYesOrNoAnswer: a stored answer that is not
// a bare Yes or No has no complement.
func TestWorkAuth_NegatedQuestionNeedsAYesOrNoAnswer(t *testing.T) {
	m := match.New(bankAnswering(t, "Yes - I hold a valid visa"), match.DefaultStages())
	r := askYesNo(t, m, match.Facts{AuthorizedCountries: []string{"US"}}, "Are you not legally authorized to work in the US?", yesNo)
	if r.Outcome != match.OutcomeAbstained || r.Reason != match.ReasonAmbiguous {
		t.Fatalf("got %s %q (%s)", r.Outcome, r.ChosenLabel, r.Reason)
	}
}
