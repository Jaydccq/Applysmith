package match

import "testing"

// runPasses matches qs as given, selectors included; matchAll renumbers them.
func runPasses(m *Matcher, qs ...Question) []Result {
	resp, err := m.Match(Request{Host: "jobs.example.com", Questions: qs})
	if err != nil {
		panic(err)
	}
	return resp.Results
}

func locationBank() []Entry {
	return []Entry{
		fixedEnt("address-city", "Where do you currently reside?", "Springfield", withTypes(InputText, InputSingleSelect)),
		fixedEnt("address-state", "State", "Illinois"),
		fixedEnt("address-country", "Country", "United States"),
	}
}

func TestOptions_ResidenceListReroute(t *testing.T) {
	var countries []string
	for _, c := range []string{"Canada", "France", "Germany", "Japan", "United States", "Brazil"} {
		countries = append(countries, c)
	}
	q := selectQ("Where do you currently reside?", countries...)
	res := runPasses(pickMatcher(locationBank()...), q)
	r := res[0]
	if r.Outcome == OutcomeAbstained || r.EntryID != "address-country" || r.ChosenLabel != "United States" || r.Selector != "s" {
		t.Fatalf("rerouted: %+v", r)
	}
	if len(r.Trace) == 0 || !traceHas(r, "re-routed to address-country") {
		t.Fatalf("trace: %v", r.Trace)
	}
	// State goes first: a state list answers with the state.
	states := selectQ("Where do you currently reside?", "Indiana", "Illinois", "Iowa")
	if r := runPasses(pickMatcher(locationBank()...), states)[0]; r.EntryID != "address-state" || r.ChosenLabel != "Illinois" {
		t.Fatalf("state first: %+v", r)
	}
	// An option-less control and other questions are untouched.
	if r := runPasses(pickMatcher(locationBank()...), textQ("s", "Where do you currently reside?"))[0]; r.EntryID != "address-city" || r.Answer != "Springfield" {
		t.Fatalf("typeahead: %+v", r)
	}
	// A fill among the options is never turned into anything else.
	cities := selectQ("Where do you currently reside?", "Springfield", "Shelbyville")
	if r := runPasses(pickMatcher(locationBank()...), cities)[0]; r.EntryID != "address-city" || r.ChosenLabel != "Springfield" {
		t.Fatalf("a fill stays: %+v", r)
	}
	// HelloFresh: a native list sent without its options (over 100) is a country list, and takes the country.
	long := selectQ("Where do you currently live?")
	if r := runPasses(pickMatcher(locationBank()...), long)[0]; r.EntryID != "address-country" || r.Answer != "United States" || !traceHas(r, "runs past 100") {
		t.Fatalf("long native list: %+v", r)
	}
	// Groupon (Greenhouse, live): a residence question over a custom list past
	// 100 options, which arrives with none, is a country list too.
	// Only a question no entry answered is re-routed: a city typeahead that
	// fills keeps the city.
	groupon := Question{Selector: "g", Text: "Do you currently reside where this role is based, or would you need to relocate?", Kind: KindCustomListbox, InputType: InputSingleSelect}
	coarse := locationBank()[1:]
	if r := runPasses(pickMatcher(coarse...), groupon)[0]; r.EntryID != "address-country" || r.Answer != "United States" || !traceHas(r, "past 100 options") {
		t.Fatalf("long custom list: %+v", r)
	}
	if r := runPasses(pickMatcher(locationBank()...), groupon)[0]; r.EntryID != "address-city" {
		t.Fatalf("a fill stays: %+v", r)
	}
	// Lever (live): the demographic "What is your location?" over its 250
	// countries, sent with none, which the city filled.
	if r := runPasses(pickMatcher(locationBank()...), selectQ("What is your location?"))[0]; r.EntryID != "address-country" || r.Answer != "United States" || !traceHas(r, "runs past 100") {
		t.Fatalf("Lever location list: %+v", r)
	}
	for _, text := range []string{"In which state do you currently reside?", "Where would you prefer to be based?"} {
		q := Question{Selector: "g", Text: text, Kind: KindCustomListbox, InputType: InputSingleSelect}
		if r := runPasses(pickMatcher(coarse...), q)[0]; r.EntryID == "address-country" {
			t.Fatalf("%q: %+v", text, r)
		}
	}
	// A rematch outside a pass is an error.
	if _, err := (Request{}).Rematch(textQ("s", "x")); err == nil {
		t.Fatal("Rematch is a post-pass tool")
	}
}

func TestGuards_GeoSearchPlaceholderReroute(t *testing.T) {
	es := []Entry{
		fixedEnt("address-country", "Search by country/region or code", "United States", withTypes(InputText, InputSingleSelect)),
		fixedEnt("address-state", "State or Region", "Illinois", withTypes(InputText, InputSingleSelect)),
	}
	search := Question{Selector: "state", Text: "Search by country/region or code", Kind: KindCustomListbox, InputType: InputSingleSelect}
	country := Question{Selector: "country", Text: "Country/Region", Kind: KindText, InputType: InputText}
	res := runPasses(pickMatcher(es...), search, country)
	if res[0].EntryID != "address-state" || res[0].Answer != "Illinois" || res[0].Selector != "state" || !traceHas(res[0], "geo-search placeholder reroute") {
		t.Fatalf("re-routed: %+v", res[0])
	}
	// Without a dedicated country control the route stays on address-country.
	res = runPasses(pickMatcher(es...), search)
	if res[0].EntryID != "address-country" {
		t.Fatalf("no dedicated country control: %+v", res[0])
	}
	// A real Country/Region label stays on address-country.
	res = runPasses(pickMatcher(es...), country)
	if res[0].EntryID != "address-country" && res[0].EntryID != "" {
		t.Fatalf("real label: %+v", res[0])
	}
}

func linkBank() []Entry {
	return []Entry{
		fixedEnt("linkedin-url", "LinkedIn URL", "https://www.linkedin.com/in/alex-example"),
		fixedEnt("github-url", "GitHub URL", "https://github.com/alex-example"),
		fixedEnt("portfolio-url", "Portfolio URL", "https://linkedin.com/in/alex-example/"),
		fixedEnt("other-website-url", "Other website", "https://alex.example.org"),
	}
}

func linkQ(sel, text string) Question { q := textQ(sel, text); q.InputType = InputURL; return q }

func TestLinks_Deduplication(t *testing.T) {
	m := pickMatcher(linkBank()...)
	// Portfolio stores the LinkedIn URL, but LinkedIn owns it: the portfolio field
	// gets the next unused link that is not reserved for a dedicated field.
	res := runPasses(m, linkQ("a", "LinkedIn URL"), linkQ("b", "Portfolio URL"), linkQ("c", "GitHub URL"))
	if res[1].Answer != "https://alex.example.org" || res[1].Outcome != OutcomeFilledReview || !traceHas(res[1], "link-dedupe") {
		t.Fatalf("portfolio: %+v", res[1])
	}
	if res[0].Answer != "https://www.linkedin.com/in/alex-example" || res[2].Answer != "https://github.com/alex-example" {
		t.Fatalf("owners keep theirs: %+v %+v", res[0], res[2])
	}
	// A lone link field keeps its curated answer.
	one := runPasses(m, linkQ("b", "Portfolio URL"))
	if one[0].Answer != "https://linkedin.com/in/alex-example/" || one[0].Outcome == OutcomeAbstained {
		t.Fatalf("lone: %+v", one[0])
	}
	// The dedicated LinkedIn field is filled by the profile lane and never seen here:
	// portfolio still must not keep the LinkedIn URL once another link field fills.
	two := runPasses(m, linkQ("b", "Portfolio URL"), linkQ("c", "GitHub URL"))
	if two[0].Answer == "https://linkedin.com/in/alex-example/" {
		t.Fatalf("ownership applies without the owner on the page: %+v", two[0])
	}
}

func TestLinks_AbstainsWhenLinksRunOut(t *testing.T) {
	es := []Entry{
		fixedEnt("linkedin-url", "LinkedIn URL", "https://linkedin.com/in/alex-example"),
		fixedEnt("portfolio-url", "Portfolio URL", "https://linkedin.com/in/alex-example"),
		fixedEnt("other-website-url", "Other website", "https://linkedin.com/in/alex-example"),
	}
	res := runPasses(pickMatcher(es...), linkQ("a", "LinkedIn URL"), linkQ("b", "Portfolio URL"), linkQ("c", "Other website"))
	if res[0].Outcome == OutcomeAbstained {
		t.Fatalf("owner: %+v", res[0])
	}
	for _, r := range res[1:] {
		if r.Outcome != OutcomeAbstained || r.Reason != ReasonDuplicateLink || !r.Terminal || r.Answer != "" || !traceHas(r, "no unowned link remains") {
			t.Fatalf("duplicate: %+v", r)
		}
	}
	// A blank link entry on a one-line box lacks an answer, not a shape.
	x := textQ("x", "X (formerly Twitter) URL")
	x.InputType = InputText
	wantAbstain(t, matchReq(pickMatcher(fixedEnt("twitter-url", "X", "", withTypes(InputURL))), x), ReasonPlaceholderEmpty, "")
	// Workable draws "Github profile" and "Linkedin" as multi-line boxes: a link label still gets the link,
	// a blank entry is placeholder-empty, and an essay that mentions GitHub never gets a bare URL.
	link := func(id, ex, url string) Entry {
		e := fixedEnt(id, ex, url, withTypes(InputURL))
		e.Category = CategoryLinks
		return e
	}
	box := func(text string) Question { q := textQ("t", text); q.InputType = InputLongText; return q }
	lm := pickMatcher(link("github-url", "Code repository profile URL", "https://github.com/alex-example"), link("linkedin-url", "LinkedIn URL", "https://linkedin.com/in/alex-example"))
	for text, want := range map[string]string{"Github profile": "https://github.com/alex-example", "Linkedin": "https://linkedin.com/in/alex-example"} {
		if r := matchReq(lm, box(text)); r.Outcome != OutcomeFilled || r.Answer != want {
			t.Fatalf("%s: %+v", text, r)
		}
	}
	if r := matchReq(lm, box("Share 2-3 of your open-source contributions with links (GitHub PRs, issues, or projects you maintain), and a note on your role")); r.Answer != "" {
		t.Fatalf("an essay is not a link box: %+v", r)
	}
	// Krea (Ashby, live): a "share" whose object is the links asks for a link, not a story.
	pm := pickMatcher(link("portfolio-url", "Portfolio URL", "https://alex-example.dev"))
	if r := matchReq(pm, box("Share relevant links from your work or portfolio.")); r.Outcome != OutcomeFilled || r.Answer != "https://alex-example.dev" {
		t.Fatalf("share links: %+v", r)
	}
	if r := matchReq(pm, box("Share your portfolio and explain your role")); r.Answer != "" {
		t.Fatalf("a share that is not of links stays narrative: %+v", r)
	}
	wantAbstain(t, matchReq(pickMatcher(link("github-url", "Code repository profile URL", "")), box("Github profile")), ReasonPlaceholderEmpty, "")
	// A label that names its own answer for no link gets it when the link is blank (Sumble); a held link still answers.
	eng := textQ("k", "Kaggle / Stack Overflow / other eng profile (write na if you don't have one)")
	eng.InputType = InputText
	if r := matchReq(pickMatcher(link("github-url", "Code repository profile URL", "")), eng); r.Outcome != OutcomeFilled || r.Answer != "na" {
		t.Fatalf("blank link, label names na: %+v", r)
	}
	if r := matchReq(lm, eng); r.Answer != "https://github.com/alex-example" {
		t.Fatalf("held link: %+v", r)
	}
	// A portfolio field before the GitHub field does not spend GitHub's URL.
	es2 := []Entry{
		fixedEnt("linkedin-url", "LinkedIn URL", "https://linkedin.com/in/alex-example"),
		fixedEnt("github-url", "GitHub URL", "https://github.com/alex-example"),
		fixedEnt("portfolio-url", "Portfolio URL", "https://github.com/alex-example"),
	}
	res = runPasses(pickMatcher(es2...), linkQ("a", "LinkedIn URL"), linkQ("p", "Portfolio URL"), linkQ("b", "GitHub URL"))
	if res[1].Outcome != OutcomeAbstained || res[1].Reason != ReasonDuplicateLink || res[2].Answer != "https://github.com/alex-example" {
		t.Fatalf("portfolio %+v github %+v", res[1], res[2])
	}
	// Non-link fields may repeat; only link entries are examined.
	plain := runPasses(pickMatcher(append(es, fixedEnt("ref-site", "Reference website", "https://linkedin.com/in/alex-example"))...), linkQ("a", "LinkedIn URL"), linkQ("b", "Reference website"))
	if plain[1].Outcome == OutcomeAbstained {
		t.Fatalf("non-link entry: %+v", plain[1])
	}
	for in, want := range map[string]string{"HTTPS://WWW.Example.com/": "example.com", " http://example.com// ": "example.com"} {
		if got := normalizeLink(in); got != want {
			t.Errorf("normalizeLink(%q) = %q", in, got)
		}
	}
}

// BILL (Greenhouse): a required "Website" whose entry is blank takes one of
// the user's own links the page does not show, GitHub before LinkedIn, for
// review; an optional one stays blank. With no link left, a required "Other
// URL" takes the LinkedIn link again (the owner's default; the Institute of
// Foundation Models asks LinkedIn, GitHub and a required "Other URL"), and a
// portfolio box is answered No (the owner's decision).
func TestLinks_RequiredBlankWebsiteTakesAnUnusedLink(t *testing.T) {
	blank := func(e *Entry) { e.Placeholder = true }
	es := []Entry{
		fixedEnt("linkedin-url", "LinkedIn URL", "https://www.linkedin.com/in/alex-example"),
		fixedEnt("github-url", "GitHub URL", "https://github.com/alex-example"),
		fixedEnt("other-website-url", "Website", "", blank),
	}
	m := pickMatcher(es...)
	site := required(linkQ("w", "Website"))
	res := runPasses(m, linkQ("a", "LinkedIn URL"), site)
	if res[1].Answer != "https://github.com/alex-example" || res[1].Outcome != OutcomeFilledReview || !traceHas(res[1], "required website") {
		t.Fatalf("required website: %+v", res[1])
	}
	if res := runPasses(m, linkQ("a", "LinkedIn URL"), linkQ("w", "Website")); res[1].Outcome != OutcomeAbstained {
		t.Fatalf("optional website: %+v", res[1])
	}
	other := required(linkQ("o", "Other URL"))
	if res := runPasses(m, linkQ("a", "LinkedIn URL"), linkQ("g", "GitHub URL"), other); res[2].Answer != "https://www.linkedin.com/in/alex-example" || res[2].Outcome != OutcomeFilledReview {
		t.Fatalf("no link left: %+v", res[2])
	}
	withPortfolio := pickMatcher(es[0], es[1], fixedEnt("portfolio-url", "Portfolio URL", "", blank))
	if res := runPasses(withPortfolio, linkQ("a", "LinkedIn URL"), linkQ("g", "GitHub URL"), required(linkQ("p", "Portfolio URL"))); res[2].Answer != "No" || res[2].Source != SourceOwnerDefault {
		t.Fatalf("a portfolio box with no link left: %+v", res[2])
	}
	onlyLinkedIn := pickMatcher(es[0], es[2])
	if res := runPasses(onlyLinkedIn, site); res[0].Answer != "https://www.linkedin.com/in/alex-example" || res[0].Outcome != OutcomeFilledReview {
		t.Fatalf("LinkedIn last: %+v", res[0])
	}
}
