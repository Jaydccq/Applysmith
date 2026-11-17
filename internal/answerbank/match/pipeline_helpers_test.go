package match

import "strings"

// ent builds a finished entry of the given kind with a minimal non-empty body,
// so a test states only what it cares about.
func ent(id, kind string, mods ...func(*Entry)) Entry {
	e := Entry{ID: id, Kind: Kind(kind)}
	switch e.Kind {
	case KindFixed:
		e.Answer = "x"
	case KindPickOption:
		e.Preferred = []string{"x"}
	}
	for _, m := range mods {
		m(&e)
	}
	return e
}

func withAnswer(a string) func(*Entry)      { return func(e *Entry) { e.Answer = a } }
func withExamples(x ...string) func(*Entry) { return func(e *Entry) { e.Examples = x } }
func withDescription(d string) func(*Entry) { return func(e *Entry) { e.Description = d } }
func withTypes(t ...InputType) func(*Entry) {
	return func(e *Entry) {
		e.InputTypes = nil
		for _, x := range t {
			e.InputTypes = append(e.InputTypes, string(x))
		}
	}
}
func withPreferred(p ...string) func(*Entry) {
	return func(e *Entry) { e.Preferred = p }
}
func notCurated() func(*Entry)       { return func(e *Entry) { e.NeedsReview = true } }
func emptyBody() func(*Entry)        { return func(e *Entry) { e.Body = Body{} } }
func withCap(c float64) func(*Entry) { return func(e *Entry) { e.Confidence = &c } }

// fixedStage answers a fixed entry with its literal body, the way the fixed
// resolver does, and marks the answer deterministic.
func fixedStage(s *Step) (Draft, bool) {
	if s.Entry.Kind != "fixed" {
		return Draft{}, false
	}
	return Draft{Answer: s.Entry.Answer, Deterministic: true}, true
}

func echoStages() Stages { return Stages{Fixed: fixedStage} }

func textQ(selector, text string) Question {
	return Question{Selector: selector, Text: text, Kind: KindText}
}

func yesNoQ(selector, text string) Question {
	return Question{Selector: selector, Text: text, Kind: KindRadioGroup, Options: opts("Yes", "No")}
}

func one(m *Matcher, q Question) Result {
	resp, err := m.Match(Request{Host: "jobs.example.com", Questions: []Question{q}})
	if err != nil {
		panic(err)
	}
	return resp.Results[0]
}

func traceHas(r Result, sub string) bool {
	for _, l := range r.Trace {
		if strings.Contains(l, sub) {
			return true
		}
	}
	return false
}

func bankOf(es ...Entry) Bank { return Bank{Entries: es} }
