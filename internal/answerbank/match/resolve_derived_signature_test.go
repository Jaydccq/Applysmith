package match

import "testing"

func nameEntry(name string) Entry {
	if name == "" {
		return blankEntry(IDApplicantFullName, KindFixed)
	}
	return ent(IDApplicantFullName, "fixed", withAnswer(name))
}

// Rula (Ashby): "Middle Name Please write N/A if you do not have a middle
// name." takes the words between the first and the last of the full name, and
// with none the word the form asks for; with none asked it is left.
func TestAnswerBank_Signature_TypedMiddleName(t *testing.T) {
	today := pfDate(2031, 3, 4)
	const rula = "Middle Name Please write N/A if you do not have a middle name."
	for _, c := range []struct{ name, text, full, want string }{
		{"a middle name", rula, "Alex Quinn Example", "Quinn"},
		{"two middle names", "Middle name", "Alex Quinn Lee Example", "Quinn Lee"},
		{"none, the asked word", rula, "Alex Example", "N/A"},
		{"none, none asked", "Middle Name", "Alex Example", ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			r := pfOne(t, pfMatcher(nameEntry(c.full)), alexFacts(), today, Question{Selector: "m", Text: c.text, Kind: KindText})
			if c.want == "" {
				if r.Outcome != OutcomeAbstained {
					t.Fatalf("got %+v", r)
				}
				return
			}
			if r.Answer != c.want || r.Source != SourceDerived {
				t.Fatalf("got %+v", r)
			}
		})
	}
}

func TestAnswerBank_TodaysDate_ShapesAndExclusions(t *testing.T) {
	m := pfMatcher()
	today := pfDate(2031, 3, 4)
	for _, c := range []struct{ text, want string }{
		{"Today's date", "03/04/2031"},
		{"Todays Date *", "03/04/2031"},
		{"Current date", "03/04/2031"},
		{"Date signed", "03/04/2031"},
		{"Date of signature", "03/04/2031"},
		{"Signature date", "03/04/2031"},
		{"Date", "03/04/2031"},
		{"Date*", "03/04/2031"},
		{"Date:", "03/04/2031"},
		{"Date *", "03/04/2031"},
		{"Today's date (YYYY-MM-DD)", "2031-03-04"},
		{"Date signed (DD/MM/YYYY)", "04/03/2031"},
		{"Date signed (MM-DD-YYYY)", "03-04-2031"},
		{"Date signed (DD/DD/YYYY)", "03/04/2031"},
		{"Date of Birth", ""},
		{"Graduation Date", ""},
		{"Start Date", ""},
		{"Date available", ""},
		{"Interview date", ""},
		{"Date of application", ""},
		{"Expiration date", ""},
		{"Today's date of birth", ""},
	} {
		t.Run(c.text, func(t *testing.T) {
			r := pfOne(t, m, alexFacts(), today, Question{Selector: "d", Text: c.text, Kind: KindText})
			if c.want == "" {
				if r.Source == SourceFormText {
					t.Fatalf("a subject date is never today: %+v", r)
				}
				return
			}
			if r.Answer != c.want || r.Source != SourceFormText || r.Outcome != OutcomeFilled {
				t.Fatalf("got %+v", r)
			}
		})
	}
	t.Run("the injected clock decides", func(t *testing.T) {
		q := Question{Selector: "d", Text: "Today's date", Kind: KindText}
		a := pfOne(t, m, alexFacts(), pfDate(2031, 3, 4), q)
		b := pfOne(t, m, alexFacts(), pfDate(2040, 12, 25), q)
		if a.Answer != "03/04/2031" || b.Answer != "12/25/2040" {
			t.Fatalf("%q %q", a.Answer, b.Answer)
		}
	})
	t.Run("only a text control", func(t *testing.T) {
		if r := pfOne(t, m, alexFacts(), today, pfSelect("d", "Date", "A", "B")); r.Source == SourceFormText {
			t.Fatalf("got %+v", r)
		}
	})
}
