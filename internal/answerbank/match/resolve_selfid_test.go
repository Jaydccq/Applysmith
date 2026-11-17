package match

import (
	"slices"
	"testing"
)

func axesOf(labels ...string) []string {
	set := readSelfIDSet(opts(labels...))
	if set == nil {
		return nil
	}
	return set.axes
}

func TestSelfID_AxisCues(t *testing.T) {
	for label, axis := range map[string]string{
		"Person with a disability": axisDisability, "Disabled": axisDisability, "Handicapped": axisDisability,
		"Autistic": axisNeurodivergence, "ADHD": axisNeurodivergence, "Neurodiverse": axisNeurodivergence, "Dyslexic": axisNeurodivergence,
		"Protected veteran": axisVeteran, "Military service": axisVeteran, "Armed forces": axisVeteran,
		"Refugee": axisRefugee, "Asylum seeker": axisRefugee, "First generation immigrant": axisRefugee,
		"Parent": axisCaregiver, "Caregiver": axisCaregiver, "Caring responsibilities": axisCaregiver,
		"Hispanic": axisRaceEthnicity, "Latinx": axisRaceEthnicity, "Black": axisRaceEthnicity, "African American": axisRaceEthnicity,
		"White": axisRaceEthnicity, "Asian": axisRaceEthnicity, "Pacific Islander": axisRaceEthnicity, "Two or more races": axisRaceEthnicity,
		"Female": axisGender, "Woman": axisGender, "Male": axisGender, "Man": axisGender, "Men": axisGender,
		"Transgender": axisGenderIdentity, "Genderqueer": axisGenderIdentity, "Non binary": axisGenderIdentity,
		"LGBTQ+": axisOrientation, "Gay": axisOrientation, "Bisexual": axisOrientation, "Queer": axisOrientation,
		"Pregnant": axisFamily, "Maternity leave": axisFamily, "Marital status": axisFamily,
		"Inclusion": "", "Community": "", "Diversity": "", "Engineering": "", "Manager": "", "Alaska": "",
	} {
		if got := selfIDAxisOfLabel(label); got != axis {
			t.Errorf("selfIDAxisOfLabel(%q) = %q, want %q", label, got, axis)
		}
	}
}

func TestSelfID_AxisFirstHitWins(t *testing.T) {
	// Gender is listed before gender identity, so a transgender woman reads as gender.
	if got := selfIDAxisOfLabel("Transgender woman"); got != axisGender {
		t.Fatalf("axis = %q", got)
	}
	if got := selfIDAxisOfLabel("Black disabled veteran"); got != axisDisability {
		t.Fatalf("table order: %q", got)
	}
	if got := []string{axisDisability, axisNeurodivergence, axisVeteran, axisRefugee, axisCaregiver, axisRaceEthnicity,
		axisGender, axisGenderIdentity, axisOrientation, axisFamily}; len(axisCues) != len(got) {
		t.Fatal("ten axes")
	} else {
		for i, c := range axisCues {
			if c.axis != got[i] {
				t.Fatalf("axis %d = %q", i, c.axis)
			}
		}
	}
}

func TestSelfID_IsleOfManIsNotGender(t *testing.T) {
	if got := selfIDAxisOfLabel("Isle of Man"); got != "" {
		t.Fatalf("a country is not a gender cue: %q", got)
	}
	if got := selfIDAxisOfLabel("Isle of Man / Man"); got != axisGender {
		t.Fatalf("a label holding both: %q", got)
	}
	if got := selfIDAxisOfLabel("Man"); got != axisGender {
		t.Fatalf("Man: %q", got)
	}
	if got := selfIDAxisOfLabel("Chisle of Man"); got != axisGender {
		t.Fatalf("a word that merely ends in 'isle of': %q", got)
	}
	countries := []string{"Ireland", "Isle of Man", "Israel", "Italy"}
	if readSelfIDSet(opts(countries...)) != nil {
		t.Fatal("a country list is not a set")
	}
	// The single stray cue used to exclude a 264-country picker.
	big := make([]string, 0, 264)
	for i := 0; i < 263; i++ {
		big = append(big, "Country "+string(rune('A'+i%26))+string(rune('a'+i/26%26)))
	}
	big = append(big, "Isle of Man")
	if readSelfIDSet(opts(big...)) != nil {
		t.Fatal("residence list")
	}
}

// The cue table also reads some ordinary lists (colors, shirt sizes, pets) as a
// self-identification set. That direction only withholds: such a control is never
// filled by the matcher, and the user answers it.
func TestSelfID_CueFalsePositivesOnlyWithhold(t *testing.T) {
	lists := map[string][]string{
		"colors": {"Red", "Blue", "White", "Black", "Green", "Yellow"},
		"sizes":  {"Small", "Medium", "Large", "Men's L", "Women's M"},
		"pets":   {"Dog", "Cat", "Black Lab", "White Cat"},
	}
	for name, labels := range lists {
		r := matchReq(pickMatcher(), radioQ("Pick one", labels...))
		if r.Outcome != OutcomeAbstained || r.ChosenLabel != "" {
			t.Errorf("%s: %+v", name, r)
		}
	}
}

func TestSelfID_CueBoundaries(t *testing.T) {
	for label, axis := range map[string]string{
		"Woman": axisGender, "Female": axisGender, "Non-binary": "", "Parent company": axisCaregiver, "trans": axisGenderIdentity,
		"Manager": "", "Mandatory": "", "Whitespace": "", "Blackout dates": "",
	} {
		if got := selfIDAxisOfLabel(label); got != axis {
			t.Errorf("%q = %q, want %q", label, got, axis)
		}
	}
	// "Non-binary" is a cue by itself only through the \s* form; the live gender
	// picker is a set through its Man and Woman options plus a decline.
	if got := axesOf("Man", "Woman", "Non-Binary", "Prefer Not to Say"); !slices.Contains(got, axisGender) {
		t.Fatalf("gender picker axes: %v", got)
	}
}

func TestSelfID_OptionRoles(t *testing.T) {
	for label, kind := range map[string]string{
		"I prefer not to say whether I am a veteran": roleDecline, "Decline to self-identify": roleDecline, "Prefer not to say": roleDecline,
		"I do not wish to answer": roleDecline, "Decline": roleDecline, "Not specified": roleDecline, "No response": roleDecline,
		"None of the above": roleNone, "None of these": roleNone, "None": roleNone, "None apply": roleNone, "Not applicable": roleNone, "N/A": roleNone, "n / a": roleNone,
		"Veteran": roleAxis, "Software": roleOther,
	} {
		if got := selfIDRoleOf(label).kind; got != kind {
			t.Errorf("role of %q = %q, want %q", label, got, kind)
		}
	}
}

func TestSelfID_DetectionThreshold(t *testing.T) {
	cases := []struct {
		name   string
		labels []string
		axes   int
	}{
		{"empty", nil, 0},
		{"referral list", []string{"Job board", "Referral", "Career fair"}, 0},
		{"work setting", []string{"Remote", "Hybrid", "Onsite"}, 0},
		{"proficiency ladder", []string{"Beginner", "Intermediate", "Advanced"}, 0},
		{"single-axis Yes/No form", []string{"Yes", "No"}, 0},
		{"one axis without an escape hatch", []string{"Yes, I have a disability", "No, I do not have a disability"}, 0},
		{"one axis plus a decline", []string{"Yes, I have a disability", "No, I do not have a disability", "I do not want to answer"}, 1},
		{"one axis plus none-of-the-above", []string{"Person with a disability", "None of the above"}, 1},
		{"two axes", []string{"Veteran", "Person with a disability"}, 2},
		{"race categories without a decline", []string{"Hispanic or Latino", "Black or African American", "Asian"}, 1},
		{"a single race category", []string{"Asian", "Software"}, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			set := readSelfIDSet(opts(c.labels...))
			if (set != nil) != (c.axes > 0) || set != nil && len(set.axes) != c.axes {
				t.Fatalf("set = %+v", set)
			}
		})
	}
	if set := readSelfIDSet(opts("Veteran", "Parent", "I prefer not to answer")); set == nil || set.declineIndex() != 2 {
		t.Fatalf("decline index: %+v", set)
	}
}

func TestSelfID_AnyAxisExclusionSharedTable(t *testing.T) {
	// A stricter caller excludes a control as soon as any one option names an axis.
	if selfIDAxisOfLabel("Hispanic or Latino") == "" || selfIDAxisOfLabel("Full time") != "" {
		t.Fatal("shared cue table")
	}
}

func TestEEO_DeclineIsNeverAStance(t *testing.T) {
	for _, s := range []string{"I don't wish to answer whether I am a veteran", "I prefer not to say", "Decline to state",
		"I do not want to disclose", "I choose not to answer", "Prefer not to self identify", "Not specified", "No response"} {
		if !isDeclineText(s) {
			t.Errorf("%q is a decline", s)
		}
		if _, ok := readStance(s); ok {
			t.Errorf("%q must not read as a stance", s)
		}
	}
	if isDeclineText("I am not a veteran") || isDeclineText("") {
		t.Fatal("a stance is not a decline")
	}
	if _, ok := readStance("I don't wish to answer whether I am a veteran"); ok {
		t.Fatal("a refusal never becomes non-veteran")
	}
}

func TestEEO_WorldUniverse(t *testing.T) {
	if len(conceptUniverse[conceptVeteran]) != 3 || len(conceptUniverse[conceptDisability]) != 2 {
		t.Fatalf("universe: %v", conceptUniverse)
	}
	for _, s := range []string{"Asian", "Hispanic or Latino", "Not Hispanic or Latino", "I identify as Asian (Not Hispanic or Latino)"} {
		if _, ok := readStance(s); ok {
			t.Errorf("race and ethnicity are not modelled: %q", s)
		}
	}
}

func worldsOf(t *testing.T, text string) string {
	t.Helper()
	r, ok := readStance(text)
	if !ok {
		return ""
	}
	return string(r.concept) + ":" + r.format()
}

func TestEEO_ReadVeteranWorlds(t *testing.T) {
	for text, want := range map[string]string{
		"I am not a protected veteran":                "veteran:non-veteran|veteran-unprotected",
		"I am not a veteran":                          "veteran:non-veteran",
		"I am a protected veteran":                    "veteran:protected",
		"I am a veteran":                              "veteran:protected|veteran-unprotected",
		"I am a Veteran, but not a Protected Veteran": "veteran:veteran-unprotected",
		"I identify as one or more of the classifications of protected veteran": "veteran:protected",
		"I am a veteran but a veteran":                                          "veteran:protected|veteran-unprotected",
		"Seeking an apprenticeship":                                             "",
		"":                                                                      "",
	} {
		if got := worldsOf(t, text); got != want {
			t.Errorf("readStance(%q) = %q, want %q", text, got, want)
		}
	}
	// Veteran is checked before disability.
	if got := worldsOf(t, "I am a veteran with a disability"); got != "veteran:protected|veteran-unprotected" {
		t.Fatalf("order: %q", got)
	}
	// Statutory long form reads the same way.
	long := "I am not a protected veteran under VEVRAA, meaning I am not a disabled veteran, recently separated veteran, or other protected veteran"
	if got := worldsOf(t, long); got == "" {
		t.Fatalf("long form defers: %q", got)
	}
}

func TestEEO_ReadDisabilityWorlds(t *testing.T) {
	for text, want := range map[string]string{
		"Yes, I have a disability, or have had one in the past": "disability:has",
		"No, I do not have a disability":                        "disability:none",
		"I don't have a disability":                             "disability:none",
		"I have a disability but I do not have a disability":    "",
		"Disability status":                                     "disability:has",
		"No, I do not have a disability and have not had one in the past, whatever the long legal wording says": "disability:none",
	} {
		if got := worldsOf(t, text); got != want {
			t.Errorf("readStance(%q) = %q, want %q", text, got, want)
		}
	}
}

func TestEEO_BarePolarityInheritsQuestion(t *testing.T) {
	const q = "Are you a protected veteran? (see definitions)"
	yes, ok := readOptionStance("Yes", q)
	if !ok || yes.format() != "protected" {
		t.Fatalf("Yes asserts the question's world: %+v", yes)
	}
	no, ok := readOptionStance("No", q)
	if !ok || no.format() != "non-veteran|veteran-unprotected" {
		t.Fatalf("No asserts the complement: %+v", no)
	}
	n, ok := readOptionStance("N", "I have a disability:")
	if !ok || n.format() != "none" {
		t.Fatalf("statement form: %+v", n)
	}
	if _, ok := readOptionStance("No", ""); ok {
		t.Fatal("no question text, no concept")
	}
	if _, ok := readOptionStance("No", "Do you have a driver license?"); ok {
		t.Fatal("a non-EEO question lends no concept")
	}
	if _, ok := readOptionStance("Maybe", q); ok {
		t.Fatal("only a bare polarity word")
	}
}

func TestEEO_ConceptVerdict(t *testing.T) {
	cases := []struct {
		answer, label string
		want          float64
		decided       bool
	}{
		{"I am not a protected veteran", "I am not a protected veteran", scoreStanceExact, true},
		{"I am not a protected veteran", "I am a protected veteran", 0, true},
		{"I am not a veteran", "I am not a protected veteran", scoreStanceEntailed, true},
		{"I am not a protected veteran", "I am not a veteran", 0, false}, // narrower: defer
		{"I am a veteran but not a protected veteran", "I am not a protected veteran", scoreStanceEntailed, true},
		{"I am a veteran but not a protected veteran", "I am a protected veteran", 0, true},
		{"I am not a protected veteran", "Prefer not to say", 0, false},
		{"Asian", "Asian", 0, false},
	}
	for _, c := range cases {
		v, ok := stanceVerdict(c.answer, c.label, "")
		if ok != c.decided || ok && v.score != c.want {
			t.Errorf("stanceVerdict(%q, %q) = %+v, %v", c.answer, c.label, v, ok)
		}
	}
	// Different concepts never compare.
	if _, ok := stanceVerdict("I am not a veteran", "I have a disability", ""); ok {
		t.Fatal("concepts differ")
	}
}

func TestScoring_EEOConceptBranchOrder(t *testing.T) {
	// The concept layer sits after the exact, abbreviation, withdrawal and
	// placeholder rules and before the decline and Yes/No rules.
	exact := "I am not a protected veteran"
	if s := scoreOption(Option{Label: exact}, exact, ""); s.Why != "exact-normalized-match" {
		t.Fatalf("exact first: %+v", s)
	}
	long := "Yes, I am a veteran but not a protected veteran, I do not want to answer"
	if s := scoreOption(Option{Label: long}, "I am a protected veteran", ""); s.Score != declineDemoted {
		t.Fatalf("a decline phrase in the label demotes: %+v", s)
	}
	// The statutory long form scores a decisive match instead of 0.
	form := "I am not a protected veteran under the statute"
	if s := scoreOption(Option{Label: form}, "I am not a protected veteran", ""); s.Score != scoreStanceExact {
		t.Fatalf("long form: %+v", s)
	}
}

func TestScoring_EEOCrossATSCorpus(t *testing.T) {
	best := func(answer, q string, labels ...string) string {
		sc := scoreAll(newOptionScorer(q, nil), opts(labels...), answer)
		sortScored(sc)
		if sc[0].Score < ReviewFillConfidence {
			return ""
		}
		return sc[0].opt.Label
	}
	fourWay := []string{"I am a protected veteran", "I am a veteran but not a protected veteran", "I am not a veteran", "I prefer not to answer"}
	for _, a := range []string{"I am not a veteran", "I am not a protected veteran"} {
		if got := best(a, "", fourWay...); got != "I am not a veteran" {
			t.Errorf("four-way veteran set for %q chose %q", a, got)
		}
	}
	protOnly := []string{"I identify as a protected veteran", "I am not a protected veteran", "I do not wish to answer"}
	for _, a := range []string{"I am not a veteran", "I am not a protected veteran"} {
		if got := best(a, "", protOnly...); got != "I am not a protected veteran" {
			t.Errorf("protected-only set for %q chose %q", a, got)
		}
	}
	icims := []string{"Yes, I am a veteran", "No, I am not a veteran", "I choose not to disclose"}
	if got := best("No", "Are you a veteran?", icims...); got != "No, I am not a veteran" {
		t.Errorf("iCIMS: %q", got)
	}
	if got := best("I am a protected veteran", "", icims...); got == "No, I am not a veteran" {
		t.Errorf("a veteran is not dragged onto the negating option: %q", got)
	}
	// Only Protected and Veteran-not-protected offered to a non-veteran: nothing scores.
	if got := best("I am not a veteran", "", "I am a protected veteran", "I am a veteran but not a protected veteran"); got != "" {
		t.Errorf("no true option offered: %q", got)
	}
	// Criminal-history and transgender families use the same machinery.
	if got := best("I have never been convicted of a crime", "", "I have been convicted of a crime", "I have never been convicted of a crime"); got != "I have never been convicted of a crime" {
		t.Errorf("criminal history: %q", got)
	}
	if got := best("No", "Are you transgender?", "Yes", "No", "Prefer not to say"); got != "No" {
		t.Errorf("transgender yes/no: %q", got)
	}
	// CC-305: the long negation is selected decisively, its opposite scores 0.
	cc := []string{"Yes, I have a disability, or have had one in the past", "No, I do not have a disability and have not had one in the past", "I do not want to answer"}
	if got := best("No, I do not have a disability", "Disability status (Form CC-305, voluntary)", cc...); got != cc[1] {
		t.Errorf("CC-305: %q", got)
	}
	if s := scoreOption(Option{Label: cc[1]}, "Yes, I have a disability", ""); s.Score != 0 {
		t.Errorf("affirming answer on the negating option: %+v", s)
	}
}

func TestScoring_DeclineSynonymVocabulary(t *testing.T) {
	if len(declineList) < 14 {
		t.Fatalf("one vocabulary covers the old list: %d", len(declineList))
	}
	for _, p := range [][]string{{"prefer", "not", "to", "say"}, {"decline", "to", "state"}, {"not", "specified"}, {"no", "response"},
		{"do", "not", "want", "to", "answer"}, {"do", "not", "wish", "to", "disclose"}, {"choose", "not", "to", "answer"}} {
		found := false
		for _, d := range declineList {
			found = found || slices.Equal(d, p)
		}
		if !found {
			t.Errorf("missing decline phrase %v", p)
		}
	}
}

// stanceVerdict is stanceVerdictOf for raw texts.
func stanceVerdict(answer, label, questionText string) (stanceDecision, bool) {
	return stanceVerdictOf(newAnswerProfile(answer), newLabelProfile(Option{Label: label}, questionText))
}

// Shield AI (Lever, live): "Are you a transitioning service member?" is No
// when the user's military-service entry says they never served; a user who
// served is left to say, never mapped to Yes.
func TestAnswerBank_TransitioningServiceMember_OnlyANeverServedStanceAnswers(t *testing.T) {
	q := radioQ("Are you a transitioning service member?", "No", "Yes")
	for _, c := range []struct {
		pref []string
		want string
	}{
		{[]string{"No, I have not served", "I have not served"}, "No"},
		{[]string{"Yes, I have served", "I am a veteran"}, ""},
	} {
		e := ent("military-service-status", "pick-option", withExamples("Military status"))
		e.Preferred = c.pref
		r := matchReq(pickMatcher(e), q)
		if c.want != "" {
			wantFill(t, r, c.want, OutcomeFilled)
			continue
		}
		if r.Outcome != OutcomeAbstained {
			t.Fatalf("%v: a user who served was answered: %+v", c.pref, r)
		}
	}
}

// Axonius (Greenhouse, live): "Are you an active-duty service member eligible
// for ... the DoD SkillBridge program ...?" asks the same as a transitioning
// service member, and a never-served stance answers it No.
func TestAnswerBank_ActiveDutySkillBridge_NeverServedAnswersNo(t *testing.T) {
	for _, text := range []string{
		"Are you an active-duty service member eligible for or approved to participate in the DoD SkillBridge program within your final 180 days of service?",
		"Are you approved to participate in the SkillBridge program?",
	} {
		e := ent("military-service-status", "pick-option", withExamples("Military status"))
		e.Preferred = []string{"No, I have not served", "I have not served"}
		wantFill(t, matchReq(pickMatcher(e), radioQ(text, "Yes", "No")), "No", OutcomeFilled)
		e.Preferred = []string{"Yes, I have served"}
		if r := matchReq(pickMatcher(e), radioQ(text, "Yes", "No")); r.Outcome != OutcomeAbstained {
			t.Fatalf("%q: a user who served was answered: %+v", text, r)
		}
	}
}
