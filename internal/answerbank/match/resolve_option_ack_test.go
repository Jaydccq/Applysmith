package match

import "testing"

func ackEntry(question string) Entry {
	return ent("mandatory-acknowledge", "auto-acknowledge", withExamples(question))
}

// ackResult matches a required question against an acknowledgement entry with
// the legal-declaration setting on.
func ackResult(q Question) Result {
	return matchReq(pickMatcher(ackEntry(q.Text)), required(q), withLegal(true))
}

func TestAutoAck_ShapeBranches(t *testing.T) {
	const q = "Please read the arbitration agreement below"
	// (a) sole affirmative
	wantFill(t, ackResult(radioQ("I certify that all information is true", "Yes")), "Yes", OutcomeFilled)
	// (b) sole non-negative: a restatement of the stem
	wantFill(t, ackResult(selectQ(q, "I will read the arbitration agreement below.")), "I will read the arbitration agreement below.", OutcomeFilled)
	// (c) affirmative/negative pair
	wantFill(t, ackResult(radioQ("Do you accept the terms?", "Accept", "Decline")), "Accept", OutcomeFilled)
	// (d) lone checkbox with a marker-only label, an empty label, or a restatement
	wantFill(t, ackResult(checkboxQ("At-will employment acknowledgement", "*")), "*", OutcomeFilled)
	wantFill(t, ackResult(checkboxQ("At-will employment acknowledgement", "")), "", OutcomeFilled)
	wantFill(t, ackResult(checkboxQ("I understand this is at-will employment", "I understand this is at-will employment")), "I understand this is at-will employment", OutcomeFilled)
	// Refusals: no ack token and no affirmative shape.
	wantAbstain(t, ackResult(selectQ("Work arrangement", "Option A", "Option B")), ReasonInputMismatch, "")
	wantAbstain(t, ackResult(radioQ(q, "No")), ReasonInputMismatch, "")
	wantAbstain(t, ackResult(checkboxQ("At-will employment acknowledgement", "No thanks")), ReasonInputMismatch, "")
	// A sole non-negated option of any control is the only election it can express.
	wantFill(t, ackResult(selectQ("At-will employment acknowledgement", "*")), "*", OutcomeFilled)
	// The fill answer is the option's VALUE.
	cb := Question{Selector: "s", Text: "At-will acknowledgement", Kind: KindCheckbox, Required: true, Options: []Option{{Value: "on", Label: "*"}}}
	if r := matchReq(pickMatcher(ackEntry(cb.Text)), cb, withLegal(true)); r.Answer != "on" {
		t.Fatalf("answer is the option value: %+v", r)
	}
}

func TestAutoAck_NonSemanticLabel(t *testing.T) {
	for _, l := range []string{"", "  ", "*", "\u2731", "* *", "-", "\u2013", "( )", "[ ]", "\u00A0", ": ;", "\u2022"} {
		if !isNonSemanticAckLabel(l, "I agree to the terms") {
			t.Errorf("%q carries no election", l)
		}
	}
	if !isNonSemanticAckLabel("the terms", "I agree to the terms") {
		t.Error("a restatement of the question carries no election")
	}
	if isNonSemanticAckLabel("Subscribe me", "I agree to the terms") {
		t.Error("a real label")
	}
}

func TestLegal_PolicyWrapperTable(t *testing.T) {
	const q = "I agree to the privacy policy"
	ok := radioQ(q, "Yes", "No")
	cases := []struct {
		name    string
		q       Question
		legal   bool
		outcome Outcome
		detail  Detail
	}{
		{"required, setting on", required(ok), true, OutcomeFilled, ""},
		{"required, setting off", required(ok), false, OutcomeAbstained, DetailLegalDeclarationOff},
		{"optional, setting on", ok, true, OutcomeAbstained, DetailMarketingOptIn},
		{"optional, setting off", ok, false, OutcomeAbstained, DetailMarketingOptIn},
		{"scraped required flag but the label says optional", required(radioQ("(Optional) I agree to the privacy policy", "Yes", "No")), true, OutcomeAbstained, DetailMarketingOptIn},
		{"marketing wording, required, follows the setting on", required(radioQ("I agree that Example Corp may contact me about future job opportunities", "Yes", "No")), true, OutcomeFilled, ""},
		{"marketing wording, required, follows the setting off", required(radioQ("I agree that Example Corp may contact me about future job opportunities", "Yes", "No")), false, OutcomeAbstained, DetailLegalDeclarationOff},
		{"marketing wording, optional, never ticked", radioQ("I agree that Example Corp may contact me about future job opportunities", "Yes", "No"), true, OutcomeAbstained, DetailMarketingOptIn},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := matchReq(pickMatcher(ackEntry(c.q.Text)), c.q, withLegal(c.legal))
			if r.Outcome != c.outcome || r.Detail != c.detail {
				t.Fatalf("got %+v", r)
			}
			if c.outcome == OutcomeAbstained && (r.Reason != ReasonContextPolicy || !r.Terminal || r.Answer != "" || r.ChosenLabel != "") {
				t.Fatalf("terminal context-policy with nothing chosen: %+v", r)
			}
		})
	}
	// A marketing opt-in with no choosable option is still left to the user.
	opt := Question{Selector: "s", Text: "Send me job alerts by SMS", Kind: KindCheckbox}
	r := matchReq(pickMatcher(ackEntry(opt.Text)), opt, withLegal(true))
	wantAbstain(t, r, ReasonContextPolicy, DetailMarketingOptIn)
}

func TestAutoAck_WithdrawingOptionIsNeverChosen(t *testing.T) {
	const q = "Arbitration agreement"
	wantFill(t, ackResult(radioQ(q, "Accept and withdraw my application", "I agree")), "I agree", OutcomeFilled)
	wantAbstain(t, ackResult(radioQ(q, "Accept and withdraw my application")), ReasonInputMismatch, "")
	wantAbstain(t, ackResult(checkboxQ("At-will employment acknowledgement", "I withdraw my interest")), ReasonInputMismatch, "")
}

func TestAutoAck_ContractedRefusalNeverChosen(t *testing.T) {
	for _, label := range []string{
		"I don't agree", "I don’t agree", "I donʼt agree", "I don＇t agree",
		"I don&apos;t agree", "I don\u200b't agree",
		"I can't agree", "I won't agree", "I haven't agreed", "I shouldn't accept",
		"Never confirm", "I refuse to accept", "I will not agree", "Don't accept",
		"I cannot consent", "Without agreement", "I reject the agreement",
		"The applicant will not readily and fully agree",
	} {
		t.Run(label, func(t *testing.T) {
			q := "Arbitration agreement"
			wantAbstain(t, ackResult(radioQ(q, label)), ReasonInputMismatch, "")
			wantFill(t, ackResult(radioQ(q, label, "I agree")), "I agree", OutcomeFilled)
			if hasUnnegatedAcknowledgement(label) {
				t.Error("refusal became an acknowledgement route")
			}
		})
	}
	wantFill(t, ackResult(radioQ("AI policy", "I agree not to use AI tools")), "I agree not to use AI tools", OutcomeFilled)
	for _, label := range []string{
		"I refuse this previous wording but I agree", "Yes, I have no objections",
		"I agree without coercion", "I will not readily and fully ever agree",
		"Yes, I will not accept outside assistance",
		"I certify that I will not accept outside assistance",
		"I attest that I will never consent to outside help",
	} {
		wantFill(t, ackResult(radioQ("Arbitration agreement", label)), label, OutcomeFilled)
	}
	wantAbstain(t, ackResult(radioQ("Arbitration agreement", "I will not readily and fully agree")), ReasonInputMismatch, "")
}

func TestAutoAck_NegatedRestatementNeverChosen(t *testing.T) {
	for _, label := range []string{"I have never read the policy", "I currently don't understand the agreement", "We have not read the terms"} {
		wantAbstain(t, ackResult(radioQ("Arbitration agreement", label)), ReasonInputMismatch, "")
	}
}
