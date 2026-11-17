package onboarding

import (
	"context"
	"strings"
	"testing"
	"time"

	"applysmith/internal/answerbank/bind"
	"applysmith/internal/engine"
)

// matchDemo asks the demo workspace's own answer bank, over the persona's
// profile, one question as the page filler would. The posting is the place the
// run is for; an empty one is a posting that states no location.
func matchDemo(t *testing.T, env *Env, location string, q engine.WireQuestion) engine.Resolution {
	t.Helper()
	b := bind.New(bind.Deps{Bank: bankService(env), Workspace: env.Workspace, Now: func() time.Time { return time.Date(2026, 10, 13, 9, 0, 0, 0, time.UTC) }})
	q.Selector = "#q"
	resp, err := b.Match(context.Background(), engine.MatchRequest{RunID: "r", Host: "jobs.example.com",
		Posting: engine.PostingContext{Company: "Example Corp", Location: location}, Questions: []engine.WireQuestion{q}})
	if err != nil || len(resp.Results) != 1 {
		t.Fatalf("match: %v, %d results", err, len(resp.Results))
	}
	return resp.Results[0]
}

func yesNo(text string) engine.WireQuestion {
	return engine.WireQuestion{Text: text, Kind: "radio-group", InputType: "yes_no", Options: []string{"Yes", "No"}, Required: true}
}

func menu(text string, options ...string) engine.WireQuestion {
	return engine.WireQuestion{Text: text, Kind: "select", InputType: "single_select", Options: options, Required: true}
}

func textBox(text string) engine.WireQuestion {
	return engine.WireQuestion{Text: text, Kind: "text", InputType: "text", Options: []string{}}
}

const (
	sponsorAsk      = "Will you now or in the future require sponsorship for employment visa status (e.g., H-1B, etc.)?"
	plainSponsorAsk = "Will you now or in the future require sponsorship for employment visa status?"
	countryAsk      = "Are you legally authorized to work in the country for which you are applying?"
	yearsAsk        = "How many years of relevant, post college work experience do you have?"
	londonPost      = "London, United Kingdom"
	durhamPost      = "Durham, NC"
	noLocPost       = ""
	loopbackURL     = "https://alex.example.org"
)

// The persona's sponsorship standing follows from its US work authorization: no
// sponsorship is needed in the US, and nothing is said for a posting elsewhere.
// An H-1B question asks about the United States wherever the posting is (only
// the United States issues an H-1B; the owner's rule), so it takes the US answer.
func TestDemoPersona_SponsorshipFollowsTheUSWorkAuthorizationAndOnlyThere(t *testing.T) {
	env := demoEnv(t)
	for _, c := range []struct {
		name, location, ask string
		wantNo              bool
	}{
		{"US posting", durhamPost, sponsorAsk, true},
		{"posting with no location", noLocPost, sponsorAsk, true},
		{"posting in London, H-1B example", londonPost, sponsorAsk, true},
	} {
		r := matchDemo(t, env, c.location, yesNo(c.ask))
		if r.Outcome == "abstained" || r.OptionLabel != "No" {
			t.Errorf("%s: %+v, want the persona's No", c.name, r)
		}
	}
	for _, ask := range []string{plainSponsorAsk, countryAsk} {
		r := matchDemo(t, env, londonPost, yesNo(ask))
		if r.Outcome != "abstained" || !r.Terminal || r.OptionLabel != "" || r.Value != "" {
			t.Errorf("a posting in London, %q: %+v, want the terminal abstain", ask, r)
		}
	}
	// The H-1B entry holds the persona's own No, under the same country rule.
	const h1bAsk = "Will you require H-1B sponsorship?"
	if r := matchDemo(t, env, durhamPost, yesNo(h1bAsk)); r.Outcome == "abstained" || r.OptionLabel != "No" {
		t.Errorf("a US posting, %q: %+v, want No", h1bAsk, r)
	}
	if r := matchDemo(t, env, londonPost, yesNo(h1bAsk)); r.Outcome == "abstained" || r.OptionLabel != "No" {
		t.Errorf("a posting in London, %q: %+v, want No", h1bAsk, r)
	}
	if r := matchDemo(t, env, durhamPost, yesNo(countryAsk)); r.OptionLabel != "Yes" {
		t.Errorf("a US posting, %q: %+v, want Yes", countryAsk, r)
	}
}

// The persona's years of full-time work (the CV's nine) answer a years menu
// whatever its shape: single years, ranges and open ranges. A menu no bucket of
// which holds nine is left alone.
func TestDemoPersona_YearsOfExperienceAreChosenFromTheBucketThatHoldsThem(t *testing.T) {
	env := demoEnv(t)
	for _, c := range []struct {
		name    string
		options []string
		want    string
	}{
		{"single years with an open top", []string{"0", "1", "2", "3", "4", "5", "6", "7", "8", "9", "10+"}, "9"},
		{"ranges with an open top", []string{"0-1", "2-4", "5-7", "8+"}, "8+"},
		{"ranges closed on both ends", []string{"0 - 1 years", "2 - 4 years", "5 - 10 years", "More than 10 years"}, "5 - 10 years"},
		{"open ranges", []string{"Less than 2 years", "2-5 years", "6+ years"}, "6+ years"},
		{"no bucket holds nine", []string{"0-1", "2-4", "5-7"}, ""},
	} {
		r := matchDemo(t, env, noLocPost, menu(yearsAsk, c.options...))
		switch {
		case c.want == "" && r.Outcome != "abstained":
			t.Errorf("%s: %+v, want an abstain", c.name, r)
		case c.want != "" && (r.Outcome == "abstained" || r.OptionLabel != c.want):
			t.Errorf("%s: %+v, want %q", c.name, r, c.want)
		}
	}
}

// A bare Website box is left blank by the user's decision: the matcher gives
// it the blank entry's non-overwritable abstain, so no profile link lands in it.
// A Portfolio box takes the persona's site; a LinkedIn box takes the persona's
// linkedin.com link, the one shape the forms' own format checks accept.
func TestDemoPersona_LinkBoxesTakeTheMatchingProfileLink(t *testing.T) {
	env := demoEnv(t)
	if r := matchDemo(t, env, noLocPost, textBox("Website")); r.Outcome != "abstained" || r.Value != "" || r.ClientMayOverwrite {
		t.Errorf("Website: %+v, want the blank entry's abstain that the profile pass leaves alone", r)
	}
	if r := matchDemo(t, env, noLocPost, textBox("Portfolio URL")); r.Outcome == "abstained" || r.Value != loopbackURL {
		t.Errorf("Portfolio URL: %+v, want %s", r, loopbackURL)
	}
	for _, label := range []string{"LinkedIn Profile", "LinkedIn URL"} {
		r := matchDemo(t, env, noLocPost, textBox(label))
		if r.Outcome == "abstained" || strings.TrimSuffix(r.Value, "/") != personaProfile(t).Links.LinkedIn {
			t.Errorf("%q: %+v, want the persona's LinkedIn link", label, r)
		}
	}
}

// The screening page of an ADP application (a real one) asks the persona's own
// facts in its own wording: "the country you are applying to" is the posting's
// country, never-employed-here is the persona's No (a company outside its
// employers), the referral box takes the persona's own "Not referred", the
// salary is its target, and the currency menu takes its currency. A question
// the bank has no entry for (telling the manager) stays blank.
func TestDemoPersona_ADPScreeningQuestionsAreAnsweredFromTheBank(t *testing.T) {
	env := demoEnv(t)
	const (
		appliedCountry = "Are you legally authorized to work in the country you are applying to?"
		employedHere   = "Have you ever been employed by this company before?"
	)
	if r := matchDemo(t, env, durhamPost, yesNo(appliedCountry)); r.Outcome == "abstained" || r.OptionLabel != "Yes" {
		t.Errorf("a US posting, %q: %+v, want Yes", appliedCountry, r)
	}
	if r := matchDemo(t, env, londonPost, yesNo(appliedCountry)); r.Outcome != "abstained" || !r.Terminal {
		t.Errorf("a London posting, %q: %+v, want the terminal abstain: the country is not the recorded one", appliedCountry, r)
	}
	if r := matchDemo(t, env, durhamPost, yesNo(employedHere)); r.Outcome == "abstained" || r.OptionLabel != "No" {
		t.Errorf("%q: %+v, want the persona's No", employedHere, r)
	}
	referral := engine.WireQuestion{Text: "Are you an employee referral? What is the name of the referring employee?", Kind: "text", InputType: "long_text", Options: []string{}, Required: true}
	if r := matchDemo(t, env, durhamPost, referral); r.Outcome == "abstained" || r.Value != "Not referred" {
		t.Errorf("referral: %+v, want the persona's own answer", r)
	}
	if r := matchDemo(t, env, durhamPost, textBox("What is your desired salary?")); r.Outcome != "abstained" {
		t.Errorf("desired salary: %+v, want it left for the person", r)
	}
	if r := matchDemo(t, env, durhamPost, yesNo("Did you notify your current manager that you are applying for this position?")); r.Outcome != "abstained" {
		t.Errorf("telling the manager: %+v, want it left for the person", r)
	}
}
