package match

import (
	"testing"
)

// xcAFixed is a finished fixed entry that carries one saved answer.
func xcAFixed(id, answer string, mods ...func(*Entry)) Entry {
	return ent(id, "fixed", append([]func(*Entry){withAnswer(answer)}, mods...)...)
}

// xcARun matches one question over the shipped stages.
func xcARun(es []Entry, q Question, mods ...func(*Request)) Result {
	return matchReq(pickMatcher(es...), q, mods...)
}

// xcAWith supplies the applicant's facts and the day of the request.
func xcAWith(f Facts, today Date) func(*Request) {
	return func(r *Request) { r.Facts, r.Today = f, today }
}

func TestXC_A_Guards(t *testing.T) {
	t.Run("XC-023 a contact field inside an entity section is a terminal abstain", func(t *testing.T) {
		phone := xcAFixed("phone-mobile", "555-0100", withExamples("Phone"))
		for _, path := range [][]string{{"Education"}, {"Work History", "Employer #2"}} {
			q := textQ("s", "Phone")
			q.SectionPath = path
			if r := xcARun([]Entry{phone}, q); r.Outcome != OutcomeAbstained || r.Reason != ReasonSectionPolicy || !r.Terminal {
				t.Fatalf("%v: %+v", path, r)
			}
		}
		for _, path := range [][]string{{"Apply now"}, {"Apply for Example Role"}, {"Work Location"}, {"Which office do you prefer?"}, {"Role Location & Work Model"}} {
			q := textQ("s", "Phone")
			q.SectionPath = path
			if r := xcARun([]Entry{phone}, q); r.Outcome != OutcomeFilled || r.Answer != "555-0100" {
				t.Fatalf("%v is not an entity section: %+v", path, r)
			}
		}
	})
	t.Run("XC-023 a single-value entry never smears over a repeated row", func(t *testing.T) {
		school := xcAFixed("most-recent-school", "Example State University", withExamples("School"))
		q := textQ("s", "School")
		q.SectionPath = []string{"Education History", "Row 20"}
		if r := xcARun([]Entry{school}, q); r.Reason != ReasonSectionPolicy || !r.Terminal {
			t.Fatalf("%+v", r)
		}
		q.SectionPath = []string{"Education"}
		if r := xcARun([]Entry{school}, q); r.Answer != "Example State University" {
			t.Fatalf("a single section keeps the global fill: %+v", r)
		}
	})
	t.Run("XC-024 signature and date", func(t *testing.T) {
		name := xcAFixed("applicant-full-name", "Alex Example")
		state := xcAFixed("address-state", "SC", withTypes(InputText, InputSingleSelect))
		today := pfDate(2031, 3, 4)
		run := func(q Question) Result { return xcARun([]Entry{name, state}, q, xcAWith(Facts{}, today)) }
		if r := run(textQ("s", "Your Name")); r.EntryID != "applicant-full-name" || r.Answer != "Alex Example" {
			t.Fatalf("a signature block name: %+v", r)
		}
		contaminated := selectQ("* State (US) Select one. The name entered is your legal name.", "SC", "NC", "GA")
		if r := run(contaminated); r.EntryID == "applicant-full-name" {
			t.Fatalf("a State picklist was read as a signature: %+v", r)
		}
		for _, text := range []string{"Today's date", "Current date", "Date signed", "Signature date", "Date"} {
			if r := run(textQ("s", text)); r.Answer != "03/04/2031" {
				t.Errorf("%q: %+v", text, r)
			}
		}
		for _, text := range []string{"Date of birth", "Graduation date", "Start date", "Hire date", "Expiration date", "Interview date", "Date available", "Date of application"} {
			if r := run(textQ("s", text)); r.Answer == "03/04/2031" {
				t.Errorf("%q was answered with today's date", text)
			}
		}
	})
}

func TestXC_A_Disclosure(t *testing.T) {
	gateEntry := func(example string) Entry {
		e := ent("conflict-gate", "fixed", withAnswer("No"), withExamples(example))
		e.InputTypes = []string{"yes_no"}
		e.Placeholder = false
		return e
	}
	t.Run("XC-025 if NO indicate NO", func(t *testing.T) {
		for _, c := range []struct{ gate, tail string }{
			{"Do you own or operate your own business or other employment?", " If YES, please explain. If NO, please indicate NO."},
			{"Do any of your relatives or significant other work for a competitor of Example Corp?", " If YES, please explain. If NO, please indicate NO."},
			{"Have you previously worked for Example Corp as a permanent employee?", " If YES, please indicate date and position. If NO, please indicate NO."},
		} {
			r := xcARun([]Entry{gateEntry(c.gate)}, textQ("s", c.gate+c.tail))
			if r.Answer != "NO" || r.Source != SourceConditional || r.Outcome != OutcomeFilled {
				t.Errorf("%q: %+v", c.gate, r)
			}
		}
		essay := "Describe in detail the business you operate. If NO, please indicate NO and explain your goals for the next five years in at least two paragraphs."
		if r := xcARun([]Entry{gateEntry("Do you own or operate your own business?")}, textQ("s", essay)); r.Outcome != OutcomeAbstained {
			t.Fatalf("a genuine essay is not stolen: %+v", r)
		}
	})
	t.Run("XC-027 a sponsorship description is not an essay and never a bare Yes", func(t *testing.T) {
		yesNo := ent("sponsorship-now-or-future", "fixed", withAnswer("Yes"), withTypes(InputYesNo))
		yesNo.Placeholder = false
		desc := ent("sponsorship-type-description", "fixed", withAnswer("H-1B cap-subject sponsorship after OPT"), withTypes(InputText))
		desc.Placeholder = false
		facts := func(r *Request) {
			now, future := false, true
			r.Facts.AuthorizedCountries = []string{"US"}
			r.Facts.RequiresSponsorshipNow, r.Facts.RequiresSponsorshipFuture = &now, &future
		}
		for _, text := range []string{"Please describe the type of sponsorship and/or support you may require", "Please describe your current visa status"} {
			q := textQ("s", text)
			q.InputType = InputText
			if r := xcARun([]Entry{yesNo, desc}, q, facts); r.EntryID != "sponsorship-type-description" || r.Answer != "H-1B cap-subject sponsorship after OPT" {
				t.Errorf("%q: %+v", text, r)
			}
		}
		q := textQ("s", "Describe the immigration support or visa you would need")
		q.InputType = InputText
		if r := xcARun([]Entry{yesNo, desc}, q, facts); r.Answer == "Yes" {
			t.Fatalf("a free-text immigration box took the Yes/No entry: %+v", r)
		}
		// Pinterest's optional textarea asks to "list the type of support"; the shipped entry accepts long_text.
		desc.InputTypes = append(desc.InputTypes, string(InputLongText))
		q = textQ("s", "If you do require employee sponsorship or assistance for work authorization, please list the type of support you may require.")
		q.InputType = InputLongText
		if r := xcARun([]Entry{yesNo, desc}, q, facts); r.Answer != "H-1B cap-subject sponsorship after OPT" {
			t.Fatalf("the support list box: %+v", r)
		}
	})
}
