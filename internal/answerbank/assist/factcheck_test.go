package assist

import (
	"strings"
	"testing"

	"applysmith/internal/answerbank/match"
	"applysmith/internal/workspace"
)

func spanTexts(ss []span) string {
	var out []string
	for _, s := range ss {
		out = append(out, s.Text)
	}
	return strings.Join(out, "|")
}

func TestAnswerBank_FactCheck_ExtractNounPhrases(t *testing.T) {
	cases := []struct{ text, want string }{
		{"I built the Example Ledger in Go for Example Corp.", "Example Ledger|Go|Example Corp"},
		{"Honestly, we shipped it. Instead, the team grew.", ""},
		{"I use Kubernetes and the AWS CLI with SQL.", "Kubernetes|AWS"},
		{"I led 3 engineers and cut costs by 18% in 2 years.", "3 engineers|18%|2 years"},
		{"Examplify powers it. Yeah, it works.", "Examplify"},
		{"In March I joined Acme Robotics at Example's office.", "Acme Robotics|Example"},
		{"Over 200M predictions per day, 5 were slow.", "200M"},
		{"I worked on it in 2019, and again in 2021.", ""},
		{"Our B2B product serves USA and UK customers.", ""},
	}
	for _, c := range cases {
		if got := spanTexts(extractSpans(c.text)); got != c.want {
			t.Errorf("extractSpans(%q) = %q, want %q", c.text, got, c.want)
		}
	}
}

func TestAnswerBank_FactCheck_SubstringMembership(t *testing.T) {
	g := newGrounding([]string{"Built the Example Payments API in Go", "Example", "Worked three years on Kubernetes"})
	for _, ok := range []string{"Example Payments API", "Go", "Example Platform", "3 years"} {
		kind := spanName
		if ok == "3 years" {
			kind = spanFigure
		}
		if !g.supported(span{Text: ok, Kind: kind, Words: 1}) {
			t.Errorf("%q should be supported", ok)
		}
	}
	for _, bad := range []string{"Acme Robotics", "Examplify"} {
		if g.supported(span{Text: bad, Kind: spanName, Words: 2}) {
			t.Errorf("%q should not be supported", bad)
		}
	}
	if g.supported(span{Text: "4 years", Kind: spanFigure, Words: 1}) || g.supported(span{Text: "18%", Kind: spanFigure, Words: 1}) {
		t.Error("an unstated figure was supported")
	}
	// Superset tolerance: "Example Payments API" is supported by the fact "Example".
	if !g.supported(span{Text: "Example Payments API Gateway", Kind: spanName, Words: 4}) {
		t.Error("a name built on a supported fact")
	}
}

func TestAnswerBank_FactCheck_Modes(t *testing.T) {
	g := newGrounding([]string{"Built the Example Ledger in Go"})
	text := "I built the Example Ledger in Go and later led 4 engineers at Acme Robotics."
	un := g.unsupportedSpans(text)
	if len(un) != 2 {
		t.Fatalf("unsupported: %s", spanTexts(un))
	}
	for _, mode := range []Mode{ModeStrict, ModeFlex} {
		if got := blocking(mode, un); len(got) != 2 {
			t.Errorf("%s blocks %d spans, want name and figure", mode, len(got))
		}
	}
	if got := blocking(ModeDangerous, un); len(got) != 0 {
		t.Error("dangerous mode blocks nothing")
	}
}

func TestAnswerBank_FactCheck_StripSentencesWithSpans(t *testing.T) {
	text := "I shipped Examplify to customers. I like Go. Later I used Examplify-like tools. I enjoy testing."
	got := stripSentencesWithSpans(text, []span{{Text: "Examplify", Kind: spanName, Words: 1}})
	// "Examplify-like" has a boundary after the word, so that sentence goes too.
	if got != "I like Go. I enjoy testing." {
		t.Fatalf("%q", got)
	}
	if got := stripSentencesWithSpans("I use C++ daily.", []span{{Text: "C++", Kind: spanName}}); got != "" {
		t.Fatalf("%q", got)
	}
}

func TestAnswerBank_Compose_SalvageSingleWordOnly(t *testing.T) {
	g := newGrounding([]string{"Example Corp"})
	long := "I shipped Examplify to customers. " + longPlain
	rest, ok := salvage(g, ModeStrict, long, []span{{Text: "Examplify", Kind: spanName, Words: 1}})
	if !ok || strings.Contains(rest, "Examplify") {
		t.Fatalf("%q %v", rest, ok)
	}
	// Fewer than 25 words remain.
	if _, ok := salvage(g, ModeStrict, "I shipped Examplify to customers. I like Go a lot.", []span{{Text: "Examplify", Kind: spanName, Words: 1}}); ok {
		t.Fatal("salvaged a stub")
	}
	// The 40 percent boundary: a 60-word sentence is removed, so 40 words
	// remaining of 100 is exactly 40 percent and is kept, and 39 of 99 is not.
	block := []span{{Text: "Examplify", Kind: spanName, Words: 1}}
	bad := "I shipped Examplify " + strings.Repeat("and ", 56) + "done."
	plain := func(n int) string { return "I " + strings.Repeat("and ", n-2) + "done." }
	if wordCount(bad) != 60 || wordCount(plain(40)) != 40 {
		t.Fatalf("fixture sizes %d %d", wordCount(bad), wordCount(plain(40)))
	}
	if rest, ok := salvage(g, ModeStrict, bad+" "+plain(40), block); !ok || wordCount(rest) != 40 {
		t.Fatalf("exactly 40 percent was refused: %v %q", ok, rest)
	}
	if _, ok := salvage(g, ModeStrict, bad+" "+plain(39), block); ok {
		t.Fatal("39 of 99 words (under 40 percent) was salvaged")
	}
	// A multi-word invented name is never salvaged.
	if _, ok := salvage(g, ModeStrict, "At Acme Robotics I worked. "+longPlain, []span{{Text: "Acme Robotics", Kind: spanName, Words: 2}}); ok {
		t.Fatal("a multi-word invented name was salvaged")
	}
	// The remainder must pass the check again.
	if _, ok := salvage(g, ModeStrict, "I shipped Examplify. "+longPlain+" Later I met Zorblat.", []span{{Text: "Examplify", Kind: spanName, Words: 1}}); ok {
		t.Fatal("a remainder with another unsupported name was kept")
	}
}

// The question is page text the employer controls: a name or figure that only
// the question states is not grounded.
func TestAnswerBank_Compose_QuestionTextIsNotGrounding(t *testing.T) {
	e := newCompose(t)
	e.respond(func(_, _ string) string {
		return "At Zorblat Industries I led the platform team and built the deployment tooling that several groups relied on every day, which kept releases calm and the services stable across the whole organization."
	})
	got := e.compose(ComposeRequest{Question: "Describe your work at Zorblat Industries on the platform team."})
	if got.Filled || got.Reason != match.ReasonUnsupported {
		t.Fatalf("a name from the question grounded the answer: %+v", got)
	}
	// A figure stated only by the question is unsupported too.
	e = newCompose(t)
	e.respond(func(_, _ string) string {
		return "I ran the platform team for 7 years and kept the deployment pipeline reliable for every group that depended on it, writing down what I learned so the next person started further along."
	})
	got = e.compose(ComposeRequest{Question: "Describe your 7 years running the platform team."})
	if got.Filled || got.Reason != match.ReasonUnsupported {
		t.Fatalf("a figure from the question grounded the answer: %+v", got)
	}
}

// The job description is employer-controlled text: it may inform the prompt but
// never stands as evidence of the applicant's own work.
func TestAnswerBank_Compose_PostingTextIsNotGrounding(t *testing.T) {
	e := newCompose(t)
	e.respond(func(_, _ string) string {
		return "I have 12 years of Kubernetes experience and I ran the Zorblat platform for customers, keeping releases calm and the services stable across the whole organization for every team that relied on them."
	})
	got := e.compose(ComposeRequest{Question: "Why do you want to work at Example Corp?", JD: "We need 12 years of Kubernetes experience on the Zorblat platform.", ApplicationURL: "https://jobs.example.com/apply/1"})
	if got.Filled || got.Reason != match.ReasonUnsupported {
		t.Fatalf("a figure from the posting grounded the answer: %+v", got)
	}
}

// A lone sentence-opening adverb, gerund or plural common noun is not a name;
// a short product name still is (measured: "Earlier", "Applying",
// "Releases" and "Payments" caused every stricter retry of 20 answers).
func TestAnswerBank_FactCheck_SentenceOpenersAreNotNames(t *testing.T) {
	g := newGrounding([]string{"Replaced a hand-run release process.", "Built payment services in Go."})
	for _, text := range []string{"Earlier, I built payment services in Go.", "Applying that, I replaced a process.", "Releases were hand-run.", "Payments matter to me."} {
		if u := g.unsupportedSpans(text); len(u) != 0 {
			t.Errorf("%q: %v", text, u)
		}
	}
	if u := g.unsupportedSpans("Spring is what I used."); len(u) != 1 {
		t.Errorf("Spring: %v", u)
	}
}

// A degree or professional credential is a claim only the applicant's own facts
// ground, by any of its spellings and never by a substring ("ms" of
// "systems"); first-person credential wording is checked too, and a word like
// "registered users" is no claim.
func TestAnswerBank_FactCheck_CredentialClaims(t *testing.T) {
	cases := []struct {
		facts   []string
		text    string
		blocked string
	}{
		{[]string{"CFA", "Master of Science in Finance"}, "As a CFA charterholder, I priced credit.", ""},
		{[]string{"Master of Science in Finance", "Built trading systems in Go"}, "As a CFA charterholder, I priced credit.", "CFA|charterholder"},
		{[]string{"Built trading systems in Go"}, "As a CPA and MBA holder, I priced credit.", "CPA|MBA"},
		{[]string{"Certified Public Accountant", "Master of Business Administration"}, "As a CPA and MBA holder, I priced credit.", ""},
		{[]string{"Master of Science in Finance"}, "I hold an MS in Finance.", ""},
		{[]string{"Bachelor of Science in Economics", "Built trading systems in Go", "Cut jobs runtime"}, "I hold an MS and a BS in Economics.", "MS"},
		{[]string{"Ph.D. Molecular Biology"}, "I hold a PhD in Molecular Biology.", ""},
		{[]string{"MS Molecular Biology"}, "I hold a PhD in Molecular Biology.", "PhD"},
		{[]string{"MS Molecular Biology"}, "I hold a doctorate in molecular biology.", "doctorate"},
		{[]string{"Professional Engineer license, Texas"}, "I am a licensed professional engineer.", ""},
		{[]string{"Built trading systems in Go"}, "I am a licensed professional engineer.", "licensed professional engineer"},
		{[]string{"CPA"}, "I'm a certified public accountant.", ""},
		{[]string{"Built trading systems in Go"}, "I'm a certified public accountant.", "certified public accountant"},
		{[]string{"Built a portal"}, "I built a portal for registered users and certified the release.", ""},
		{[]string{"Built pricing models in Excel"}, "I built pricing models in MS Excel.", ""},
		{[]string{"Master's in Computer Science"}, "I hold an MS in Computer Science.", ""},
		{[]string{"B.Tech Computer Science"}, "I have a bachelor's degree in Computer Science.", ""},
		{[]string{"MS Molecular Biology"}, "My Ph.D. research is on molecular biology.", "Ph.D"},
		{[]string{"Ph.D. Molecular Biology"}, "My Ph.D. research is on molecular biology.", ""},
	}
	for _, c := range cases {
		if got := spanTexts(newGrounding(c.facts).unsupportedSpans(c.text)); got != c.blocked {
			t.Errorf("facts %q, %q: unsupported %q, want %q", c.facts, c.text, got, c.blocked)
		}
	}
}

// The company's own text and the posting never ground the applicant's
// credential: "our analysts are CFA charterholders" makes no one a holder.
func TestAnswerBank_FactCheck_CompanyTextGroundsNoCredential(t *testing.T) {
	p := &composePlan{in: composeIn{Question: "Why do you want to join Example Capital?", Company: "Example Capital", Src: profileSources()},
		cinfo: companyInfo{Sections: []string{"Our analysts are CFA charterholders."}}}
	if got := spanTexts(p.buildGrounding().unsupportedSpans("As a CFA charterholder, I want to join Example Capital.")); got != "CFA|charterholder" {
		t.Fatalf("unsupported %q", got)
	}
}

// Through the composer: a holder whose certifications list names the CFA keeps
// the claim although no selected line states it; a non-holder's claim is
// dropped with its sentence. The two share no cached reply.
func TestAnswerBank_Compose_CredentialClaimHolderAndNot(t *testing.T) {
	e := newCompose(t)
	e.respond(func(_, _ string) string { return "As a CFA charterholder, I priced municipal credit. " + longPlain })
	non := e.compose(ComposeRequest{Question: essayQuestion})
	if strings.Contains(non.Answer, "CFA") {
		t.Fatalf("a non-holder's claim shipped: %+v", non)
	}
	src := profileSources()
	src.Credentials = []string{"CFA"}
	held := e.compose(ComposeRequest{Question: essayQuestion, Sources: src})
	if !held.Filled || !strings.Contains(held.Answer, "As a CFA charterholder") {
		t.Fatalf("a holder's claim was dropped: %+v", held)
	}
}

// The applicant's records hold a degree however it is written, and a job title
// is no credential: a financial analyst is not a chartered or certified one.
func TestAnswerBank_FactCheck_CredentialsFromRecords(t *testing.T) {
	p := fictionalProfile()
	p.History.Education = []workspace.Education{
		{School: "Example Institute", Degree: "Ph.D.", Field: "Molecular Biology", End: "2016-05"},
		{School: "Example University", Degree: "Master", Field: "Finance", End: "2019-05"},
	}
	src := SourcesFromProfile(p, "", nil)
	src.Evidence = []EvidenceLine{{Ref: "cv.md#Experience", Entry: "Financial Analyst, Example Capital", Text: "Built credit models for a municipal bond desk."}}
	plan := &composePlan{in: composeIn{Question: essayQuestion, Company: "Example Capital", Src: src}, evidence: src.Evidence}
	g := plan.buildGrounding()
	for text, want := range map[string]string{
		"I hold a PhD in Molecular Biology.":                       "",
		"I hold a master's degree in Finance.":                     "",
		"As a certified financial analyst, I built credit models.": "certified financial analyst",
		"I am a chartered financial analyst.":                      "chartered financial analyst",
	} {
		if got := spanTexts(g.unsupportedSpans(text)); got != want {
			t.Errorf("%q: unsupported %q, want %q", text, got, want)
		}
	}
	src.Evidence = nil
	src.Credentials = []string{"MS Molecular Biology"}
	if got := spanTexts((&composePlan{in: composeIn{Question: essayQuestion, Src: src}}).buildGrounding().unsupportedSpans("I hold a PhD in Molecular Biology.")); got != "PhD" {
		t.Errorf("a non-holder's PhD: unsupported %q", got)
	}
}
