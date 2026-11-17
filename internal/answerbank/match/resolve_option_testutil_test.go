package match

import "testing"

// pickMatcher matches over the shipped stages with a synthetic bank.
func pickMatcher(es ...Entry) *Matcher { return New(bankOf(es...), DefaultStages()) }

func selectQ(text string, labels ...string) Question {
	return Question{Selector: "s", Text: text, Kind: KindSelect, InputType: InputSingleSelect, Options: opts(labels...)}
}

func radioQ(text string, labels ...string) Question {
	return Question{Selector: "s", Text: text, Kind: KindRadioGroup, Options: opts(labels...)}
}

func checkboxQ(text string, labels ...string) Question {
	return Question{Selector: "s", Text: text, Kind: KindCheckbox, Options: opts(labels...)}
}

func required(q Question) Question { q.Required = true; return q }

// matchReq matches one question with request modifiers applied.
func matchReq(m *Matcher, q Question, mods ...func(*Request)) Result {
	req := Request{Host: "jobs.example.com", Questions: []Question{q}, Facts: Facts{AuthorizedCountries: []string{"US"}}, Posting: Posting{Location: "Austin, TX"}}
	for _, mod := range mods {
		mod(&req)
	}
	resp, err := m.Match(req)
	if err != nil {
		panic(err)
	}
	return resp.Results[0]
}

func withLegal(on bool) func(*Request) {
	return func(r *Request) { r.AutoAcceptLegalDeclarations = on }
}

// curatedPick is a finished pick-option entry whose example is the question, so
// it is reached by the curated verbatim stage.
func curatedPick(id, question string, preferred ...string) Entry {
	return ent(id, "pick-option", withPreferred(preferred...), withExamples(question))
}

func wantFill(t *testing.T, r Result, label string, outcome Outcome) {
	t.Helper()
	if r.Outcome != outcome || r.ChosenLabel != label {
		t.Fatalf("want %s %q, got %+v", outcome, label, r)
	}
}

func wantAbstain(t *testing.T, r Result, reason Reason, detail Detail) {
	t.Helper()
	if r.Outcome != OutcomeAbstained || r.Reason != reason || r.Detail != detail {
		t.Fatalf("want abstain %s/%s, got %+v", reason, detail, r)
	}
}
