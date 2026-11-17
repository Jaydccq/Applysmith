package match

import "testing"

// xcCFilled reports a chosen label that was filled (any fill outcome).
func xcCFilled(r Result) bool { return r.Outcome != OutcomeAbstained }

func xcCSelect(text string, labels ...string) Question { return selectQ(text, labels...) }

// TestXC_Options sends each option incident through the whole matcher.
func TestXC_Options(t *testing.T) {
	t.Run("XC-066", func(t *testing.T) {
		const q = "What is your notice period?"
		e := ent("notice-period", "pick-option", withPreferred("in 1 Month"), withExamples(q))
		r := matchReq(pickMatcher(e), xcCSelect(q, "15 days", "30 days", "60 days", "90 days", "Not applicable"))
		if !xcCFilled(r) || r.ChosenLabel != "30 days" {
			t.Fatalf("the notice picker abstained or misbound: %+v", r)
		}
		lit := matchReq(pickMatcher(e), xcCSelect(q, "30 days", "in 1 Month", "Not applicable"))
		if lit.ChosenLabel != "in 1 Month" {
			t.Fatalf("a literal option wins as an exact match: %+v", lit)
		}
	})
	t.Run("XC-067", func(t *testing.T) {
		const q = "What is your sexual orientation?"
		e := ent("eeo-orientation", "pick-option", withPreferred("Bisexual"), withExamples(q))
		r := matchReq(pickMatcher(e), xcCSelect(q, "Straight", "Bi", "Gay or Lesbian"))
		if r.ChosenLabel != "Bi" || !xcCFilled(r) {
			t.Fatalf("the abbreviated option should bind: %+v", r)
		}
		const mq = "What month were you born?"
		m := ent("birth-month", "pick-option", withPreferred("August"), withExamples(mq))
		r = matchReq(pickMatcher(m), xcCSelect(mq, "Jul", "Aug", "Sep"))
		if r.ChosenLabel != "Aug" {
			t.Fatalf("month abbreviation: %+v", r)
		}
		const tq = "Title"
		title := ent("name-title", "pick-option", withPreferred("Mr"), withExamples(tq))
		if r = matchReq(pickMatcher(title), xcCSelect(tq, "Mrs", "Ms")); xcCFilled(r) {
			t.Fatalf("Mr must not bind Mrs: %+v", r)
		}
	})
	t.Run("XC-068", func(t *testing.T) {
		const q = "Race / Ethnicity (select all that apply)"
		e := ent("eeo-race", "pick-option", withPreferred("Asian"), withExamples(q), withTypes(InputSingleSelect, InputCheckbox))
		m := pickMatcher(e)
		yes := matchReq(m, checkboxQ(q, "Asian (Not Hispanic or Latino)"))
		if !xcCFilled(yes) || yes.ChosenLabel != "Asian (Not Hispanic or Latino)" {
			t.Fatalf("the Asian checkbox should be checked: %+v", yes)
		}
		no := matchReq(m, checkboxQ(q, "White / Caucasian"))
		if xcCFilled(no) {
			t.Fatalf("the White checkbox must stay unchecked: %+v", no)
		}
		// Pinterest's Greenhouse "Race/Ethnicity" is one multi-select list.
		list := Question{Selector: "l", Text: "Race/Ethnicity", Kind: KindCustomListbox, InputType: InputMultiSelect, Required: true,
			Options: opts("American Indian or Alaskan Native", "Asian", "Black or African American", "White", "I don't wish to answer")}
		if r := matchReq(m, list); !xcCFilled(r) || r.ChosenLabel != "Asian" {
			t.Fatalf("the recorded race on a multi-select list: %+v", r)
		}
		eth := ent("eeo-ethnicity", "pick-option", withPreferred("Hispanic or Latino"), withExamples("Ethnicity"))
		if r := matchReq(pickMatcher(eth), xcCSelect("Ethnicity", "Not Hispanic or Latino", "Prefer not to say")); r.ChosenLabel == "Not Hispanic or Latino" {
			t.Fatalf("Hispanic or Latino selected its negation: %+v", r)
		}
	})
	t.Run("XC-069", func(t *testing.T) {
		const q = "Could you travel to our office for an interview?"
		e := ent("interview-travel", "pick-option", withPreferred("Yes"), withExamples(q))
		rungs := []string{
			"Possibly, I have flexibility on dates",
			"Yes. I could realistically travel with enough notice",
			"Unlikely, I have constraints on travel",
			"No, I am unable to travel",
		}
		for _, order := range [][]int{{0, 1, 2, 3}, {1, 0, 2, 3}, {3, 2, 0, 1}, {2, 0, 3, 1}, {3, 2, 1, 0}} {
			var labels []string
			for _, i := range order {
				labels = append(labels, rungs[i])
			}
			r := matchReq(pickMatcher(e), xcCSelect(q, labels...))
			if r.ChosenLabel != rungs[1] {
				t.Fatalf("order %v chose %q", order, r.ChosenLabel)
			}
			// 0.85 is the review band, never an auto fill.
			if r.Outcome != OutcomeFilledReview {
				t.Fatalf("order %v outcome = %s", order, r.Outcome)
			}
		}
		const rq = "Are you open to relocating?"
		rel := ent("open-to-relocate", "pick-option", withPreferred("Yes"), withExamples(rq))
		r := matchReq(pickMatcher(rel), xcCSelect(rq, "Maybe, depending on the location", "No", "Yes, I am open to relocating"))
		if r.ChosenLabel != "Yes, I am open to relocating" {
			t.Fatalf("a Yes / Maybe / No ladder picks Yes: %+v", r)
		}
	})
	t.Run("XC-070", func(t *testing.T) {
		const q = "How did you hear about us?"
		e := ent("referral-source", "pick-option", withPreferred("Employee referral"), withExamples(q))
		e.FallbackAnyOption = true
		m := pickMatcher(e)
		r := matchReq(m, xcCSelect(q, "—", "-- No answer --", "Select...", "Choose one", "No Selection", "Job fair"))
		if r.ChosenLabel != "Job fair" {
			t.Fatalf("the net must skip every placeholder: %+v", r)
		}
		r = matchReq(m, xcCSelect(q, "—", "-", "-- No answer --", "Select...", "Choose one", "No Selection"))
		if xcCFilled(r) {
			t.Fatalf("all-placeholder options must abstain: %+v", r)
		}
	})
	t.Run("XC-071", func(t *testing.T) {
		const q = "How did you hear about us?"
		// A fuzzy-only route is refused.
		fz := ent("referral-source", "pick-option", withPreferred("Employee referral"))
		fz.FallbackAnyOption = true
		fz.Description = "how did you hear about the referral"
		r := matchReq(pickMatcher(fz), xcCSelect("how did you feel about using AI tools", "Haven't tried it", "Use daily"))
		if xcCFilled(r) {
			t.Fatalf("a fuzzy route used the first-option net: %+v", r)
		}
		// Self-identification sets are refused even on a deterministic route;
		// only the self-identification check's own decline may answer one.
		any := ent("referral-source", "pick-option", withPreferred("Employee referral"), withExamples(q))
		any.FallbackAnyOption = true
		r = matchReq(pickMatcher(any), xcCSelect(q, "Person with disability", "Veteran", "I prefer not to answer"))
		if xcCFilled(r) && r.Source != SourceSelfIDDecline {
			t.Fatalf("first-option fallback on a self-identification set: %+v", r)
		}
		// EEO entries without the flag keep abstaining.
		vet := ent("eeo-veteran", "pick-option", withPreferred("I am not a protected veteran"), withExamples("Veteran status"))
		r = matchReq(pickMatcher(vet), xcCSelect("Veteran status", "Disabled veteran", "Recently separated veteran", "Armed forces service medal veteran"))
		if xcCFilled(r) || r.ChosenLabel == "Disabled veteran" {
			t.Fatalf("unmatched veteran options were filled: %+v", r)
		}
		// A free-text or typeahead referral input resolves from the entry.
		txt := ent("referral-source", "pick-option", withPreferred("Employee referral"), withExamples(q), withTypes(InputText, InputSingleSelect))
		txt.FallbackAnyOption = true
		txt.FallbackText = "Employee referral"
		tr := matchReq(pickMatcher(txt), textQ("s", q))
		if !xcCFilled(tr) || tr.Answer != "Employee referral" {
			t.Fatalf("a text input should resolve: %+v", tr)
		}
	})
	t.Run("XC-072", func(t *testing.T) {
		const q = "Are you one of the following?"
		status := []string{"U.S. Citizen or National", "Lawful Permanent Resident", "Refugee", "Asylum", "Agricultural Worker"}
		e := ent("us-citizen-lpr-asylee-refugee", "fixed", withAnswer("No"), withExamples(q))
		m := pickMatcher(e)
		if r := matchReq(m, xcCSelect(q, status...)); xcCFilled(r) {
			t.Fatalf("a status the user lacks was asserted: %+v", r)
		}
		withNone := append(append([]string{}, status...), "None of the above")
		if r := matchReq(m, xcCSelect(q, withNone...)); r.ChosenLabel != "None of the above" {
			t.Fatalf("only the none option may be chosen: %+v", r)
		}
		// The yes/no form answers from the recorded status.
		if r := matchReq(m, radioQ(q, "Yes", "No")); r.ChosenLabel != "No" {
			t.Fatalf("yes/no form: %+v", r)
		}
		// A category pair answers B for a non-immigrant.
		a := "A - US Citizen, US National; Lawful Permanent Resident; Refugee or Asylum"
		b := "B - Other (includes non-immigrant visa status such as F1, H, J)"
		if r := matchReq(m, xcCSelect(q, a, b)); r.ChosenLabel != b {
			t.Fatalf("category pair: %+v", r)
		}
	})
	t.Run("XC-073", func(t *testing.T) {
		// The terms and proctoring routes reach their consent entries by wording.
		terms := ent("terms-and-conditions-consent", "pick-option", withPreferred("No"))
		q := required(radioQ("I accept the terms and conditions", "No", "I have read and agree"))
		wantFill(t, matchReq(pickMatcher(terms), q, withLegal(true)), "I have read and agree", OutcomeFilled)
		proctor := ent("assessment-proctoring-consent", "pick-option", withPreferred("No"))
		pq := required(selectQ("This assessment is proctored", "Decline", "I consent"))
		wantFill(t, matchReq(pickMatcher(proctor), pq, withLegal(true)), "I consent", OutcomeFilled)
		// With only negated options, or no consent entry in the bank, nothing is chosen.
		only := required(selectQ("This assessment is proctored", "Decline", "I do not agree"))
		if r := matchReq(pickMatcher(proctor), only, withLegal(true)); xcCFilled(r) {
			t.Fatalf("negated-only options were chosen: %+v", r)
		}
		if r := matchReq(pickMatcher(ent("unrelated", "fixed", withAnswer("x"))), q, withLegal(true)); xcCFilled(r) {
			t.Fatalf("no consent entry should abstain: %+v", r)
		}
		// The setting decides whether the required acknowledgement is ticked.
		wantAbstain(t, matchReq(pickMatcher(terms), q), ReasonContextPolicy, DetailLegalDeclarationOff)
		// A Yes/No consent picks Yes.
		yn := required(radioQ("I accept the terms and conditions", "No", "Yes"))
		wantFill(t, matchReq(pickMatcher(terms), yn, withLegal(true)), "Yes", OutcomeFilled)
	})
	t.Run("XC-074", func(t *testing.T) {
		e := ent("time-zone", "pick-option", withPreferred("Eastern Time Zone"), withExamples("Time zone"))
		m := pickMatcher(e)
		others := []string{"(UTC-08:00) Pacific Time (US & Canada)", "(UTC-07:00) Mountain Time (US & Canada)", "Time Zone Not Listed"}
		for _, want := range []string{
			"Eastern Time Zone (America/Detroit)", "(UTC-05:00) Eastern Time (US & Canada)", "America/New_York",
			"Eastern Standard Time (EST)", "EST",
		} {
			r := matchReq(m, selectQ("Time zone", append([]string{want}, others...)...))
			if r.ChosenLabel != want {
				t.Errorf("%q: %+v", want, r)
			}
		}
		// Names outrank the seasonally ambiguous offset.
		r := matchReq(m, selectQ("Time zone", "(UTC-05:00) Central Time (US & Canada)", "(UTC-05:00) Eastern Time (US & Canada)", "Time Zone Not Listed"))
		if r.ChosenLabel != "(UTC-05:00) Eastern Time (US & Canada)" {
			t.Errorf("a Central UTC-5 label beat Eastern: %+v", r)
		}
		// A different zone's offset label ("-08:00") is not a range that contains
		// the stored zone's offset (5). A zone missing from the menu leaves the
		// field to the user; it never takes another zone.
		r = matchReq(m, selectQ("Time zone", others...))
		if xcCFilled(r) {
			t.Errorf("a zone missing from the menu must not take another zone: %+v", r)
		}
	})
	t.Run("XC-076", func(t *testing.T) {
		const q = "Example question for the empty entry"
		cases := []struct {
			name string
			e    Entry
			q    Question
		}{
			{"fixed", ent("empty-fixed", "fixed", emptyBody(), withExamples(q)), textQ("s", q)},
			{"pick", ent("empty-pick", "pick-option", emptyBody(), withExamples(q)), selectQ(q, "A", "B")},
			{"comp", ent("empty-comp", "compensation-range", emptyBody(), withExamples(q)), textQ("s", q)},
		}
		for _, c := range cases {
			r := matchReq(pickMatcher(c.e), c.q)
			if xcCFilled(r) || r.Reason != ReasonPlaceholderEmpty {
				t.Errorf("%s: an empty body must abstain placeholder-empty: %+v", c.name, r)
			}
		}
	})
}

// TestXC_SelfID covers the self-identification option incidents.
func TestXC_SelfID(t *testing.T) {
	t.Run("XC-081", func(t *testing.T) {
		const q = "Race / Ethnicity"
		race := ent("eeo-race", "pick-option", withPreferred("Asian"), withExamples(q))
		r := matchReq(pickMatcher(race), selectQ(q, "White", "Asian", "Black or African American", "I prefer not to answer"))
		if r.ChosenLabel != "Asian" {
			t.Errorf("the preferred category binds: %+v", r)
		}
		// The decline entry recognises the page's own decline wording.
		decl := ent("eeo-race", "pick-option", withPreferred("Decline to self-identify"), withExamples(q))
		for _, d := range []string{"I prefer not to answer", "I do not wish to answer"} {
			if r := matchReq(pickMatcher(decl), selectQ(q, "White", "Asian", d)); r.ChosenLabel != d {
				t.Errorf("decline group %q: %+v", d, r)
			}
		}
		// Male / Female / Decline offers the decline-capable options.
		const gq = "What is your gender identity?"
		gender := ent("eeo-gender", "pick-option", withPreferred("Decline to self-identify"), withExamples(gq))
		if r := matchReq(pickMatcher(gender), selectQ(gq, "Male", "Female", "Decline to self-identify")); r.ChosenLabel != "Decline to self-identify" {
			t.Errorf("gender decline: %+v", r)
		}
		// A recorded entry that cannot bind never takes the first option.
		const vq = "Veteran status"
		vet := ent("eeo-veteran", "pick-option", withPreferred("I am not a protected veteran"), withExamples(vq))
		if r := matchReq(pickMatcher(vet), selectQ(vq, "Prefer not to say but something else", "I identify as one or more of the classifications of protected veteran")); xcCFilled(r) {
			t.Errorf("first option committed: %+v", r)
		}
	})
}

// TestXC_Guards covers the abstain-rather-than-guess incident.
func TestXC_Guards(t *testing.T) {
	t.Run("XC-082", func(t *testing.T) {
		pref := ent("willing-to-relocate", "fixed", withAnswer("Yes"), withExamples("Are you willing to relocate?"))
		skill := ent("manual-yes-skill", "fixed", withAnswer("Yes"), withExamples("Do you have experience with Python?"))
		m := pickMatcher(pref, skill)
		cases := []struct {
			name string
			q    Question
		}{
			{"geographic claim", required(radioQ("Do you currently live within 25 miles of Example City?", "Yes", "No"))},
			{"superlative list", required(selectQ("Which database do you have the most experience with?", "Postgres", "MySQL", "SQLite"))},
			{"unlisted status list", required(selectQ("Which best describes your standing?", "Alpha tier", "Beta tier"))},
			{"required yes/no with no entry", required(radioQ("Have you visited the Example Corp campus?", "Yes", "No"))},
		}
		for _, c := range cases {
			r := matchReq(m, c.q)
			if r.Outcome != OutcomeAbstained || r.ChosenLabel == "Yes" {
				t.Errorf("%s: %+v", c.name, r)
			}
		}
		if r := matchReq(pickMatcher(), required(radioQ("Have you visited the Example Corp campus?", "Yes", "No"))); r.Outcome != OutcomeAbstained || r.Reason != ReasonNoEntry {
			t.Errorf("an empty bank abstains no-entry: %+v", r)
		}
	})
}

// TestXC_Disclosure covers the export-control heading-plus-follow-up pairs.
func TestXC_Disclosure(t *testing.T) {
	t.Run("XC-085", func(t *testing.T) {
		const follow = "If you answered No to Question 1, please indicate your (1) Citizenship and (2) U.S. Immigration Status"
		heading := "Example Corp Export Control Compliance Question 2: " + follow
		cited := ent("export-citizenship-status", "fixed", withAnswer("Exampleland citizen; no U.S. immigration status"), withExamples(follow), withTypes(InputText))
		area := Question{Selector: "s", Text: heading, Kind: KindText, InputType: InputLongText}
		r := matchReq(pickMatcher(cited), area)
		if !xcCFilled(r) || r.Answer != cited.Answer || r.Confidence != 1.0 || r.Outcome != OutcomeFilled {
			t.Fatalf("the curated follow-up should fill the textarea at 1.00: %+v", r)
		}
		// The preceding notice question takes the curated notice entry.
		const notice = "Example Corp is subject to export control rules and must verify applicant status. Do you consent to this review? Opt In or Opt Out"
		ne := ent("export-notice", "pick-option", withPreferred("Opt In"), withExamples(notice))
		nr := matchReq(pickMatcher(ne), radioQ(notice, "Opt In", "Opt Out"))
		wantFill(t, nr, "Opt In", OutcomeFilled)
		// A free-text visa-type box takes the work-authorization detail entry and
		// is read as a plain text box, not an address part.
		es := workAuthFixtures("2032-04-17", "")
		es[0].Examples = []string{"If working on a visa, please state type"}
		f := alexFacts()
		f.VisaStatus = "H-1B"
		vr := pfOne(t, pfMatcher(es...), f, pfDate(2030, 9, 1), textQ("s", "If working on a visa, please state type"))
		if vr.EntryID != IDWorkAuthorizationDetail || vr.Answer != "H-1B; work authorization valid through 2032-04-17" {
			t.Fatalf("the visa-type box: %+v", vr)
		}
	})
}
