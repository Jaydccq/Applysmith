package match

import (
	"strings"
	"testing"
)

func workAuthFixtures(expiry, stem string) []Entry {
	es := []Entry{blankEntry(IDWorkAuthorizationDetail, KindFixed)}
	if expiry != "" {
		es = append(es, ent(idWorkAuthorizationExpiry, "fixed", withAnswer(expiry)))
	}
	if stem != "" {
		es = append(es, ent(idStemOptExpiry, "fixed", withAnswer(stem)))
	}
	return es
}

const workAuthBox = "Type of visa and remaining work authorization"

func TestAnswerBank_WorkAuthDetail_SentenceStatusProseAndStemClause(t *testing.T) {
	today := pfDate(2030, 9, 1)
	box := Question{Selector: "wa", Text: workAuthBox, Kind: KindText}
	run := func(status, expiry, stem string) Result {
		f := alexFacts()
		f.VisaStatus = status
		return pfOne(t, pfMatcher(workAuthFixtures(expiry, stem)...), f, today, box)
	}
	for _, c := range []struct{ name, status, expiry, stem, want string }{
		{"OPT with a STEM extension", "F-1 OPT", "2032-04-17", "2034-04-17",
			"F-1 visa, currently on OPT; work authorization valid through 2032-04-17, with a 24-month STEM OPT extension available through 2034-04-17"},
		{"unparseable STEM date is dropped", "F-1 OPT", "2032-04-17", "someday",
			"F-1 visa, currently on OPT; work authorization valid through 2032-04-17"},
		{"past STEM date is dropped", "F-1 OPT", "2032-04-17", "2030-01-01",
			"F-1 visa, currently on OPT; work authorization valid through 2032-04-17"},
		{"an extension already running is not offered as ahead", "stem opt", "2032-04-17", "2032-04-17",
			"F-1 visa, currently on a STEM OPT extension; work authorization valid through 2032-04-17"},
		{"expiry today still answers", "CPT", "2030-09-01", "",
			"F-1 visa, currently on CPT; work authorization valid through 2030-09-01"},
		{"US spelling normalized", "stem opt", "4/17/2032", "",
			"F-1 visa, currently on a STEM OPT extension; work authorization valid through 2032-04-17"},
		{"unknown status verbatim", "H-1B", "2032-04-17", "",
			"H-1B; work authorization valid through 2032-04-17"},
		{"setup's work visa in words", "work_visa: H-1B", "2032-04-17", "",
			"H-1B visa; work authorization valid through 2032-04-17"},
		{"setup's student status in words, not its id", "student_work_authorization: 2032-04-17", "2032-04-17", "",
			"Student work authorization; work authorization valid through 2032-04-17"},
	} {
		t.Run(c.name, func(t *testing.T) {
			r := run(c.status, c.expiry, c.stem)
			if r.Answer != c.want || r.EntryID != IDWorkAuthorizationDetail || r.Source != SourceDerived {
				t.Fatalf("got %+v", r)
			}
		})
	}
}

func TestAnswerBank_WorkAuthDetail_MissingFactAbstains(t *testing.T) {
	today := pfDate(2030, 9, 1)
	box := Question{Selector: "wa", Text: workAuthBox, Kind: KindText}
	for _, c := range []struct{ name, status, expiry string }{
		{"no status", "", "2032-04-17"},
		{"no expiry entry", "F-1 OPT", ""},
		{"lapsed expiry", "F-1 OPT", "2030-08-31"},
		{"unparseable expiry", "F-1 OPT", "spring 2032"},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := alexFacts()
			f.VisaStatus = c.status
			r := pfOne(t, pfMatcher(workAuthFixtures(c.expiry, "")...), f, today, box)
			if r.Outcome != OutcomeAbstained || r.Reason != ReasonPlaceholderEmpty {
				t.Fatalf("got %+v", r)
			}
		})
	}
	t.Run("status falls back to the immigration-status entry", func(t *testing.T) {
		f := alexFacts()
		f.VisaStatus = ""
		es := append(workAuthFixtures("2032-04-17", ""), ent(idCurrentImmigrationStatus, "pick-option", withPreferred("F-1 OPT")))
		r := pfOne(t, pfMatcher(es...), f, today, box)
		if !strings.HasPrefix(r.Answer, "F-1 visa, currently on OPT") {
			t.Fatalf("got %+v", r)
		}
	})
}

func TestAnswerBank_WorkAuth_ExpiryEntryPlaceholderThenStoredDate(t *testing.T) {
	stages := Stages{Fixed: fixedStage}
	today := pfDate(2030, 9, 1)
	q := Question{Selector: "exp", Text: "When does your work authorization expire?", Kind: KindText}
	blank := New(bankOf(blankEntry(idWorkAuthorizationExpiry, KindFixed)), stages)
	if r := pfOne(t, blank, alexFacts(), today, q); r.Outcome != OutcomeAbstained || r.Reason != ReasonPlaceholderEmpty {
		t.Fatalf("an unfilled expiry never guesses: %+v", r)
	}
	filled := New(bankOf(ent(idWorkAuthorizationExpiry, "fixed", withAnswer("2032-04-17"))), stages)
	if r := pfOne(t, filled, alexFacts(), today, q); r.Answer != "2032-04-17" || r.EntryID != idWorkAuthorizationExpiry {
		t.Fatalf("got %+v", r)
	}
	// The STEM date answers expiry-shaped wording only.
	stem := New(bankOf(ent(idStemOptExpiry, "fixed", withAnswer("2034-04-17"))), stages)
	if r := pfOne(t, stem, alexFacts(), today, Question{Selector: "s", Text: "When does your STEM OPT expire?", Kind: KindText}); r.Answer != "2034-04-17" {
		t.Fatalf("got %+v", r)
	}
	if r := pfOne(t, stem, alexFacts(), today, yesNoQ("s", "Are you eligible for a STEM OPT extension?")); r.EntryID == idStemOptExpiry && r.Outcome == OutcomeFilled {
		t.Fatalf("a STEM eligibility yes/no is not answered with a date: %+v", r)
	}
}

func TestAnswerBank_WorkAuth_QualifiedAffirmativeTieBreakByStanding(t *testing.T) {
	labels := []string{"Yes - I am a citizen", "Yes - I hold a green card", "Yes - I hold a valid visa", "No"}
	run := func(status string, lbls ...string) (optionBinding, bool) {
		f := alexFacts()
		f.VisaStatus = status
		s := &Step{Req: &Request{Facts: f}}
		return qualifiedAffirmativeTie(s, opts(lbls...))
	}
	if b, ok := run("F-1 OPT", labels...); !ok || b.Label != "Yes - I hold a valid visa" || b.Score != 0.85 || b.Index != 2 {
		t.Fatalf("visa holder: %+v %v", b, ok)
	}
	if b, ok := run("Green card holder", labels...); !ok || b.Label != "Yes - I hold a green card" {
		t.Fatalf("resident: %+v %v", b, ok)
	}
	if b, ok := run("U.S. citizen", labels...); !ok || b.Label != "Yes - I am a citizen" {
		t.Fatalf("citizen: %+v %v", b, ok)
	}
	if _, ok := run("", labels...); ok {
		t.Fatal("no recorded status is ambiguous")
	}
	if b, ok := run("permanent_resident", labels...); !ok || b.Label != "Yes - I hold a green card" {
		t.Fatalf("setup's resident status: %+v %v", b, ok)
	}
	if b, ok := run("citizen", labels...); !ok || b.Label != "Yes - I am a citizen" {
		t.Fatalf("setup's citizen status: %+v %v", b, ok)
	}
	if _, ok := run("not_authorized", labels...); ok {
		t.Fatal("no authorization names no standing")
	}
	if _, ok := run("F-1 OPT", "Yes - I am a citizen or green card holder", "Yes - I hold a valid visa", "No"); ok {
		t.Fatal("a combined option is not a clean discriminator")
	}
	if _, ok := run("F-1 OPT", "Yes - I hold an H-1B visa", "Yes - I hold a valid visa", "No"); ok {
		t.Fatal("two options naming the applicant's standing are ambiguous")
	}
	if _, ok := run("F-1 OPT", "Yes - I am a citizen", "Yes - I hold a green card", "No"); ok {
		t.Fatal("the applicant's standing absent")
	}
	if _, ok := run("F-1 OPT", "Yes - I hold a valid visa", "No"); ok {
		t.Fatal("a single affirmative is no tie")
	}
}

// ERCOT (Workday, live): "Are you currently being sponsored by another company
// in the US?" is the visa held now. A company-sponsored work visa is Yes; a
// citizen, a permanent resident and a student's work authorization are No; a
// status that does not say is the user's, and the user's own answer wins.
func TestAnswerBank_CurrentlyEmployerSponsored_FromTheVisaStatus(t *testing.T) {
	const q = "Are you currently being sponsored by another company in the US?"
	for _, tc := range []struct {
		status, own, want string
	}{
		{"work_visa: H-1B", "", "Yes"},
		{"L-1B", "", "Yes"},
		{"citizen", "", "No"},
		{"permanent_resident", "", "No"},
		{"student_work_authorization: 2027-06-30", "", "No"},
		{"F-1 OPT", "", "No"},
		{"work_visa: other", "", ""},
		{"prefer_not_to_say", "", ""},
		{"", "", ""},
		{"F-1 OPT", "Yes", "Yes"},
	} {
		t.Run(tc.status+"/"+tc.own, func(t *testing.T) {
			e := blankEntry(idCurrentlyEmployerSponsored, KindFixed)
			e.InputTypes = []string{InputYesNo}
			if tc.own != "" {
				e.Answer, e.Placeholder = tc.own, false
			}
			r := pfOne(t, pfMatcher(e), Facts{VisaStatus: tc.status}, Date{}, yesNoQ("s", q))
			if tc.want == "" {
				if r.Outcome != OutcomeAbstained {
					t.Fatalf("%+v: a status that does not say is the user's", r)
				}
				return
			}
			if r.Outcome == OutcomeAbstained || r.Answer != tc.want || r.EntryID != idCurrentlyEmployerSponsored {
				t.Fatalf("%+v", r)
			}
		})
	}
}
