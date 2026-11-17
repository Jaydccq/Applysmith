package answerbank

import (
	"testing"

	"applysmith/internal/answerbank/match"
)

// textConsentBank is the default bank with the user's own text-consent answer
// recorded ("" leaves the shipped placeholder).
func textConsentBank(t *testing.T, recorded string) match.Bank {
	t.Helper()
	b := defaultBank(t)
	entries := append([]match.Entry(nil), b.Entries...)
	for i := range entries {
		if entries[i].ID == "marketing-text-consent" && recorded != "" {
			entries[i].Answer, entries[i].Placeholder = recorded, false
		}
	}
	b.Entries = entries
	return b
}

func askListbox(t *testing.T, m *match.Matcher, q match.Question) match.Result {
	t.Helper()
	q.Selector = "#q"
	resp, err := m.Match(match.Request{Host: "jobs.example.com", Questions: []match.Question{q}})
	if err != nil {
		t.Fatal(err)
	}
	return resp.Results[0]
}

// A form that asks for a notification channel instead of Yes/No is answered by
// the user's recorded text consent: Yes picks the channel that includes texts,
// No the one that leaves them off, and a blank record picks nothing.
func TestTextConsent_ChannelListFollowsTheRecordedAnswer(t *testing.T) {
	const text = "Select your preference to receive notification updates regarding the application journey for the position you are currently applying for. Your consent is not a condition of your application or your employment. Message and data rates may apply."
	opts := []match.Option{{Value: "e", Label: "Email only"}, {Value: "s", Label: "SMS text and email"}}
	for _, c := range []struct{ recorded, want string }{
		{"Yes", "SMS text and email"},
		{"No", "Email only"},
		{"", ""},
	} {
		m := match.New(textConsentBank(t, c.recorded), match.DefaultStages())
		r := askListbox(t, m, match.Question{Text: text, Kind: match.KindCustomListbox, Options: opts, Required: true})
		switch {
		case c.want == "" && r.Outcome != match.OutcomeAbstained:
			t.Errorf("blank record: must stay unanswered, got %s %q", r.Outcome, r.ChosenLabel)
		case c.want != "" && (r.Outcome == match.OutcomeAbstained || r.ChosenLabel != c.want || r.EntryID != "marketing-text-consent"):
			t.Errorf("recorded %q: got %s %q entry=%s (%s), want %q", c.recorded, r.Outcome, r.ChosenLabel, r.EntryID, r.Reason, c.want)
		}
	}
}

// Two options that neither opt in nor out of texts are never read as consent.
func TestTextConsent_UnrelatedChannelListIsNotAnswered(t *testing.T) {
	m := match.New(textConsentBank(t, "Yes"), match.DefaultStages())
	r := askListbox(t, m, match.Question{
		Text: "Select your preference to receive notification updates regarding your application. Message and data rates may apply.",
		Kind: match.KindCustomListbox, Options: []match.Option{{Value: "a", Label: "Weekly digest"}, {Value: "b", Label: "Daily digest"}}, Required: true})
	if r.Outcome != match.OutcomeAbstained {
		t.Errorf("no option speaks of texts: got %s %q", r.Outcome, r.ChosenLabel)
	}
}

// The same disclaimer on a phone-number box does not make it the consent entry's.
func TestTextConsent_PhoneBoxWithTheDisclaimerIsNotConsent(t *testing.T) {
	m := match.New(textConsentBank(t, "Yes"), match.DefaultStages())
	r := askListbox(t, m, match.Question{Text: "Mobile phone number (message and data rates may apply)", Kind: match.KindText, Required: true})
	if r.EntryID == "marketing-text-consent" {
		t.Errorf("a phone number box was answered by the text-consent entry: %s %q", r.Outcome, r.Answer)
	}
}
