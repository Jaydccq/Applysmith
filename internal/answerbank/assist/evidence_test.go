package assist

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"applysmith/internal/answerbank/match"
)

func gatherFor(t *testing.T, svc *Service, src Sources, q evidenceQuery) []Fact {
	t.Helper()
	return svc.gather(context.Background(), src, q)
}

func refs(fs []Fact) string {
	var out []string
	for _, f := range fs {
		out = append(out, f.Ref)
	}
	return strings.Join(out, ",")
}

func TestAnswerBank_Retrieve_AdmissionFloorTwoDistinctTokens(t *testing.T) {
	over18 := fixedEntry("age-18", "Am I over 18", "Yes")
	q := evidenceQuery{question: "Please give an overview of the project and framework", options: nil}
	if got := bankFacts(Sources{Bank: []match.Entry{over18}}, q); len(got) != 0 {
		t.Fatalf("over/overview admitted an entry: %v", got)
	}
	e := fixedEntry("remote", "Open to remote work arrangements", "Yes")
	one := evidenceQuery{question: "Remote?"}
	if got := bankFacts(Sources{Bank: []match.Entry{e}}, one); len(got) != 1 {
		t.Fatalf("a one-content-word question must stay answerable: %v", got)
	}
	two := evidenceQuery{question: "Is remote work something you do?"}
	if got := bankFacts(Sources{Bank: []match.Entry{e}}, two); len(got) != 1 {
		t.Fatalf("two shared terms: %v", got)
	}
	oneShared := evidenceQuery{question: "Is remote capable hardware provided?"}
	if got := bankFacts(Sources{Bank: []match.Entry{e}}, oneShared); len(got) != 0 {
		t.Fatalf("one shared term admitted an entry: %v", got)
	}
}

// LiveKit (Ashby, live): route 289 pinned the blanket skill gate on "Do you
// contribute to open source repos?", its screening found no supported term,
// and the option lane then chose Yes citing the gate's own "Yes". A blanket
// gate answers Yes to whatever a question names, so it is no evidence for any
// model lane, pinned or found.
func TestAnswerBank_Retrieve_ABlanketGateIsNeverEvidence(t *testing.T) {
	gate := fixedEntry("skill-experience-knowledge-gate", "Whether the applicant has the experience or knowledge a screening question names", "Yes")
	gate.Examples = []string{"Do you contribute to open source repos?"}
	q := evidenceQuery{question: "Do you contribute to open source repos?", options: []string{"Yes", "No"}, pinnedID: gate.ID}
	if got := bankFacts(Sources{Bank: []match.Entry{gate}}, q); len(got) != 0 {
		t.Fatalf("a blanket gate was offered as evidence: %v", refs(got))
	}
	q.pinnedID = ""
	if got := bankFacts(Sources{Bank: []match.Entry{gate}}, q); len(got) != 0 {
		t.Fatalf("a blanket gate was found as evidence: %v", refs(got))
	}
}

func TestAnswerBank_Fallback_BankEntryEvidence(t *testing.T) {
	good := fixedEntry("remote-ok", "Open to remote work arrangements", "Yes")
	blank := fixedEntry("remote-blank", "Open to remote work arrangements", "  ")
	ask := fixedEntry("remote-ask", "Open to remote work arrangements", match.AskMe)
	placeholder := fixedEntry("remote-ph", "Open to remote work arrangements", "Yes")
	placeholder.Placeholder = true
	archived := fixedEntry("remote-old", "Open to remote work arrangements", "No")
	archived.State = match.StateArchived
	runCtx := fixedEntry("remote-run", "Open to remote work arrangements", "Yes")
	runCtx.RunContext = true
	scoped := fixedEntry("remote-scoped", "Open to remote work arrangements", "Maybe")
	scoped.CapturedScope = []match.ScopeItem{{Host: "other.example.com", Question: "Open to remote work arrangements?"}}
	q := evidenceQuery{question: "Open to remote work arrangements?", host: "jobs.example.com", options: []string{"Yes", "No"}}
	got := bankFacts(Sources{Bank: []match.Entry{blank, ask, placeholder, archived, runCtx, scoped, good}}, q)
	if refs(got) != "answer-bank#remote-ok" {
		t.Fatalf("facts: %s", refs(got))
	}
	if got[0].Text != "Open to remote work arrangements: Yes" {
		t.Fatalf("text %q", got[0].Text)
	}
	// An entry that states a target compensation is no fact about held pay.
	target := match.Entry{ID: "comp", Description: "Compensation expectations", Kind: match.KindCompensationRange,
		Body: match.Body{Minimum: "100000", Maximum: "120000", DefaultAnswer: "100000 to 120000", Currency: "USD"}}
	held := evidenceQuery{question: "What compensation do you currently receive annually?", held: true}
	if got := bankFacts(Sources{Bank: []match.Entry{target}}, held); len(got) != 0 {
		t.Fatalf("target compensation became a held-pay fact: %v", got)
	}
	target2 := evidenceQuery{question: "What are your compensation expectations?"}
	if got := bankFacts(Sources{Bank: []match.Entry{target}}, target2); len(got) != 1 {
		t.Fatalf("target compensation for a target question: %v", got)
	}
	// An entry with the same captured scope is eligible on its own host.
	scoped.CapturedScope[0].Host = "jobs.example.com"
	scoped.CapturedScope[0].Options = []string{"Yes", "No"}
	if got := bankFacts(Sources{Bank: []match.Entry{scoped}}, q); len(got) != 1 {
		t.Fatalf("scoped on its own host: %v", got)
	}
}

func TestAnswerBank_Fallback_PinnedEntryAlwaysInEvidence(t *testing.T) {
	var entries []match.Entry
	for i := 0; i < 8; i++ {
		entries = append(entries, fixedEntry(fmt.Sprintf("hit-%d", i), "Preferred work location remote", "Remote"))
	}
	pinned := fixedEntry("unrelated-pin", "Zebra", "Striped")
	entries = append(entries, pinned)
	q := evidenceQuery{question: "Preferred work location remote", pinnedID: "unrelated-pin"}
	got := bankFacts(Sources{Bank: entries}, q)
	if len(got) != maxBankFacts || got[0].Ref != "answer-bank#unrelated-pin" {
		t.Fatalf("pinned entry not first of %d: %s", len(got), refs(got))
	}
	// A pinned entry with a blank answer is no fact.
	blank := fixedEntry("unrelated-pin", "Zebra", "")
	if got := bankFacts(Sources{Bank: []match.Entry{blank}}, q); len(got) != 0 {
		t.Fatalf("blank pinned entry: %v", got)
	}
}

func TestAnswerBank_Retrieve_ProfileFieldsDescriptorGated(t *testing.T) {
	src := Sources{Profile: []ProfileField{
		{Ref: "profile#name", Label: "Name", Value: "Alex Example", Category: CatName},
		{Ref: "profile#salary", Label: "Salary target", Value: "100000", Category: CatSalary},
		{Ref: "profile#workauth", Label: "Work authorization", Value: "Authorized", Category: CatWorkAuth},
		{Ref: "profile#employment-1", Label: "Employment", Value: "Engineer at Example Corp", Category: CatEmployment},
		{Ref: "profile#city", Label: "City", Value: "Springfield", Category: CatCity},
	}}
	got := refs(profileFacts(src, evidenceQuery{question: "Which framework is your favorite?"}))
	if got != "profile#city" {
		t.Fatalf("unrelated question saw %s", got)
	}
	got = refs(profileFacts(src, evidenceQuery{question: "What is your full name?"}))
	if got != "profile#name,profile#city" {
		t.Fatalf("name question saw %s", got)
	}
	if got := refs(profileFacts(src, evidenceQuery{question: "What are your salary expectations?"})); !strings.Contains(got, "profile#salary") {
		t.Fatalf("salary question saw %s", got)
	}
	// The salary statement is a target: never a fact for held compensation.
	if got := refs(profileFacts(src, evidenceQuery{question: "What is your current salary?", held: true})); strings.Contains(got, "salary") {
		t.Fatalf("held pay saw the target: %s", got)
	}
	// Work authorization never grounds a narrative answer.
	if got := refs(profileFacts(src, evidenceQuery{question: "Are you authorized to work here?", narrative: true})); strings.Contains(got, "workauth") {
		t.Fatalf("narrative saw work authorization: %s", got)
	}
}

func TestAnswerBank_Retrieve_IdentityFloorNonSensitiveOnly(t *testing.T) {
	src := Sources{Profile: []ProfileField{
		{Ref: "profile#city", Label: "City", Value: "Springfield", Category: CatCity},
		{Ref: "profile#country", Label: "Country", Value: "US", Category: CatCountry},
		{Ref: "profile#degree", Label: "Highest degree", Value: "BSc", Category: CatDegree},
		{Ref: "profile#school", Label: "School", Value: "Example University", Category: CatSchool},
		{Ref: "profile#gpa", Label: "Grade", Value: "3.8", Category: CatGPA},
		{Ref: "profile#workauth", Label: "Work authorization", Value: "Authorized", Category: CatWorkAuth},
		{Ref: "profile#salary", Label: "Salary", Value: "100000", Category: CatSalary},
	}}
	got := refs(profileFacts(src, evidenceQuery{question: "Degree"}))
	if got != "profile#city,profile#country,profile#degree,profile#school" {
		t.Fatalf("a bare Degree* saw %s", got)
	}
}

// A sensitive question is refused before evidence is gathered for a model, so a
// CV bullet that names its very words never reaches a prompt.
func TestAnswerBank_Fallback_SensitiveQuestionIgnoresCVFacts(t *testing.T) {
	const bullet = "US citizen status confirmed; citizenship held since birth"
	e := newEnv(t)
	e.respond(func(_, _ string) string { return replyOption("Citizen") })
	src := profileSources()
	src.CV = []string{bullet}
	got := e.runHand(match.ReasonNoEntry, enabled, src, selectQ("#s", "What is your citizenship status?", "Citizen", "Permanent resident", "Other"))
	if isFill(got[0]) {
		t.Fatalf("a sensitive question was answered: %+v", got[0])
	}
	for _, p := range e.prompts {
		if strings.Contains(p.Prompt, bullet) {
			t.Fatal("a CV line reached a prompt for a sensitive question")
		}
	}
}

func TestAnswerBank_Retrieve_CorrectionOutranksAll(t *testing.T) {
	e := newEnv(t)
	src := profileSources()
	src.Bank = []match.Entry{fixedEntry("remote-ok", "Open to remote work arrangements", "Yes")}
	src.Corrections = []Correction{
		{Ref: "correction#7", Question: "Are you open to remote work arrangements?", Value: "No"},
		{Ref: "correction#8", Question: "A different question entirely", Value: "Maybe"},
		{Ref: "correction#9", Question: "Are you open to remote work arrangements?", Value: " "},
	}
	got := gatherFor(t, e.svc, src, evidenceQuery{question: "Are you open to remote work arrangements?", options: []string{"Yes", "No"}})
	if len(got) < 2 || got[0].Ref != "correction#7" || !strings.Contains(got[0].Text, "Previously corrected answer for this question: No") || got[1].Ref != "answer-bank#remote-ok" {
		t.Fatalf("order: %s", refs(got))
	}
	for _, f := range got {
		if f.Ref == "correction#8" || f.Ref == "correction#9" {
			t.Fatalf("an unrelated or blank correction is a fact: %s", f.Ref)
		}
	}
}

func TestAnswerBank_Fallback_PromptNeverContainsSensitiveValues(t *testing.T) {
	e := newEnv(t)
	e.respond(func(_, _ string) string { return replyOption("Alpha") })
	src := profileSources()
	// Each saved entry shares terms with the question, so only its category keeps
	// it out of the evidence.
	eth := fixedEntry("eeo-ethnicity", "Team ethnicity to join", "Hispanic")
	eth.Category = match.CategorySelfIdentification
	con := fixedEntry("felony", "Team join record", "I was convicted in 2010")
	con.Category = match.CategoryCriminalHistory
	dob := fixedEntry("date-of-birth", "Team join birth date", "1985-03-04")
	dob.Category = match.CategoryIdentity
	src.Bank = []match.Entry{eth, con, dob}
	// The request holds self-identification, date-of-birth, conviction and
	// veteran answers, a posting description and a cover letter; none of them
	// reaches the lane's prompt.
	req, resp := hand(match.ReasonNoEntry, roleQ(1),
		selectQ("#eth", "Are you Hispanic or Latino?", "Hispanic", "Not Hispanic"),
		textQ("#dob", "What is your date of birth?", false),
		selectQ("#con", "Have you ever been convicted of a felony?", "Yes", "No"),
		selectQ("#vet", "Please select a veteran status", "Protected veteran", "Not a veteran"))
	for k, ans := range []string{"Hispanic", "1985-03-04", "No", "Not a veteran"} {
		r := &resp.Results[k+1]
		r.Outcome, r.Reason, r.Answer, r.ChosenLabel = match.OutcomeFilled, "", ans, ans
	}
	req.Posting.Description = "The posting says we pay in marbles. Cover letter: Dear hiring manager"
	e.svc.Run(context.Background(), RunInput{Req: req, Response: resp, Settings: enabled, Sources: src})
	p := e.seen(TaskOption)[0]
	for _, banned := range []string{"marbles", "Dear hiring manager", "Hispanic", "convicted", "1985-03-04", "veteran"} {
		if strings.Contains(p, banned) {
			t.Fatalf("prompt holds %q:\n%s", banned, p)
		}
	}
}

func TestAnswerBank_Fallback_SiblingDigestOmitsSensitiveCategories(t *testing.T) {
	e := newEnv(t)
	e.respond(func(_, _ string) string { return replyOption("Alpha") })
	qs := []match.Question{
		selectQ("#a", "What is your date of birth?", "A", "B"),
		selectQ("#b", "Do you require sponsorship?", "Yes", "No"),
		selectQ("#c", "What are your salary expectations?", "Low", "High"),
		selectQ("#d", "Do you have any criminal convictions?", "Yes", "No"),
		selectQ("#e", "Which framework do you use daily?", "Rails", "Phoenix"),
		roleQ(1),
	}
	req, resp := hand(match.ReasonNoEntry, qs...)
	for i := 0; i < 5; i++ {
		resp.Results[i] = match.Result{Selector: qs[i].Selector, Outcome: match.OutcomeFilled, Answer: "secret-" + fmt.Sprint(i), ChosenLabel: "secret-" + fmt.Sprint(i)}
	}
	e.svc.Run(context.Background(), RunInput{Req: req, Response: resp, Settings: enabled, Sources: profileSources()})
	p := e.seen(TaskOption)[0]
	for i := 0; i < 4; i++ {
		if strings.Contains(p, fmt.Sprintf("secret-%d", i)) {
			t.Fatalf("sibling %d leaked:\n%s", i, p)
		}
	}
	if !strings.Contains(p, "secret-4") {
		t.Fatalf("the harmless sibling is missing:\n%s", p)
	}
}

func TestAnswerBank_Fallback_FollowUpGetsGateContext(t *testing.T) {
	gate := selectQ("#gate", "Have you worked with Kubernetes in production?", "Yes", "No")
	follow := selectQ("#f", "If yes, which environment did you mostly use?", "Staging", "Production")
	run := func(t *testing.T, gateQ match.Question, label string, kind match.Outcome) string {
		e := newEnv(t)
		e.respond(func(_, _ string) string { return replyOption("Staging") })
		req, resp := hand(match.ReasonNoEntry, gateQ, follow)
		resp.Results[0] = match.Result{Selector: gateQ.Selector, Outcome: kind, Answer: strings.ToLower(label), ChosenLabel: label}
		e.svc.Run(context.Background(), RunInput{Req: req, Response: resp, Settings: enabled, Sources: profileSources()})
		if p := e.seen(TaskOption); len(p) == 1 {
			return p[0]
		}
		return ""
	}
	if p := run(t, gate, "Yes", match.OutcomeFilled); !strings.Contains(p, `Context: the preceding question "Have you worked with Kubernetes in production?" was answered "Yes".`) {
		t.Fatalf("no gate context:\n%s", p)
	}
	// A model-filled gate gives no context, and neither does a sensitive one.
	if p := run(t, gate, "Yes", match.OutcomeFilledModel); strings.Contains(p, "Context:") {
		t.Fatalf("a model fill became gate context:\n%s", p)
	}
	sens := selectQ("#gate", "Have you ever been convicted of a felony?", "Yes", "No")
	if p := run(t, sens, "No", match.OutcomeFilled); strings.Contains(p, "Context:") || strings.Contains(p, "convicted") {
		t.Fatalf("a sensitive gate leaked:\n%s", p)
	}
	// Only a conditional wording follows its gate, and only a Yes/No gate leads.
	e := newEnv(t)
	e.respond(func(_, _ string) string { return replyOption("Staging") })
	plain := selectQ("#f", "Which environment did you mostly use?", "Staging", "Production")
	req, resp := hand(match.ReasonNoEntry, gate, plain)
	resp.Results[0] = match.Result{Selector: gate.Selector, Outcome: match.OutcomeFilled, Answer: "yes", ChosenLabel: "Yes"}
	e.svc.Run(context.Background(), RunInput{Req: req, Response: resp, Settings: enabled, Sources: profileSources()})
	if p := e.seen(TaskOption); len(p) != 1 || strings.Contains(p[0], "Context:") {
		t.Fatalf("a follow-up that is not conditional took the gate context: %v", p)
	}
	e = newEnv(t)
	e.respond(func(_, _ string) string { return replyOption("Staging") })
	notGate := selectQ("#gate", "Which cloud do you use most?", "AWS", "GCP")
	req, resp = hand(match.ReasonNoEntry, notGate, follow)
	resp.Results[0] = match.Result{Selector: notGate.Selector, Outcome: match.OutcomeFilled, Answer: "aws", ChosenLabel: "AWS"}
	e.svc.Run(context.Background(), RunInput{Req: req, Response: resp, Settings: enabled, Sources: profileSources()})
	if p := e.seen(TaskOption); len(p) != 1 || strings.Contains(p[0], "Context:") {
		t.Fatalf("a question that is not a Yes/No gate became context: %v", p)
	}
}

// A CV line needs the same two distinct terms a bank entry needs, or one when
// the question has only one.
func TestAnswerBank_Retrieve_CVFactsNeedTwoTerms(t *testing.T) {
	cv := []string{"Led a payments migration", "Enjoys hiking and cooking"}
	if got := cvFacts(Sources{CV: cv}, evidenceQuery{question: "Describe your payments background"}); len(got) != 0 {
		t.Fatalf("one shared term of three admitted a line: %v", refs(got))
	}
	both := []string{"Led a payments migration for banks", "Enjoys hiking"}
	if got := cvFacts(Sources{CV: both}, evidenceQuery{question: "Tell us about payments migration"}); refs(got) != "cv#fact-1" {
		t.Fatalf("two shared terms: %v", refs(got))
	}
	if got := cvFacts(Sources{CV: cv}, evidenceQuery{question: "Payments?"}); refs(got) != "cv#fact-1" {
		t.Fatalf("a one-word question: %v", refs(got))
	}
}

// demoEvidence is the CV and profile evidence of the fictional persona, in the
// order documents.ApplicantEvidence lists it.
func demoEvidence() []EvidenceLine {
	nw, cc, fr := "Senior Platform Engineer, Northwind Labs (current role)", "Backend Engineer, Contoso Cloud", "Software Engineer, Fabrikam Robotics"
	return []EvidenceLine{
		{Ref: "profile.yml#history.employment", Entry: nw, Text: "Runs the internal deployment platform for forty product teams."},
		{Ref: "profile.yml#history.employment", Entry: cc, Text: "Built the order and billing services in Go on PostgreSQL."},
		{Ref: "profile.yml#skills", Text: "Skills: Go, Kubernetes, PostgreSQL"},
		{Ref: "cv.md#Summary", Text: "Platform and backend engineer with nine years of experience. Has mentored engineers but not managed them."},
		{Ref: "cv.md#nw", Entry: nw, Text: "Replaced a hand-run release process with a self-service deployment pipeline; median lead time fell from two days to forty minutes."},
		{Ref: "cv.md#nw", Entry: nw, Text: "Moved 120 services from virtual machines to Kubernetes with no customer-visible outage."},
		{Ref: "cv.md#nw", Entry: nw, Text: "Cut the platform's cloud bill by 23 percent through right-sizing."},
		{Ref: "cv.md#nw", Entry: nw, Text: "Led the weekly incident review and mentored four engineers; pages per on-call week fell from 31 to 9."},
		{Ref: "cv.md#cc", Entry: cc, Text: "Built the order and billing services in Go on PostgreSQL, handling 2 million orders a month."},
		{Ref: "cv.md#cc", Entry: cc, Text: "Wrote the idempotent payment retry that ended duplicate charges."},
		{Ref: "cv.md#cc", Entry: cc, Text: "Cut p99 checkout latency from 1.8 seconds to 420 milliseconds by batching inventory calls."},
		{Ref: "cv.md#cc", Entry: cc, Text: "Added contract tests across 14 services, which caught 60 breaking changes before release."},
		{Ref: "cv.md#fr", Entry: fr, Text: "Wrote the telemetry ingest service for 3,000 warehouse robots in Python on PostgreSQL."},
		{Ref: "cv.md#Projects", Entry: "Project: deploykit", Text: "An open-source command line tool that renders Kubernetes manifests from one short service file."},
	}
}

func evidenceTexts(ls []EvidenceLine) string {
	var b strings.Builder
	for _, l := range ls {
		b.WriteString(l.Text + "\n")
	}
	return b.String()
}

// A short question gets only the lines that share its terms; none, nothing.
func TestAnswerBank_ComposeEvidence_ShortQuestionNeedsSharedTerms(t *testing.T) {
	got := composeEvidence(demoEvidence(), "Which warehouse robots telemetry?", "", false)
	if len(got) != 1 || !strings.Contains(got[0].Text, "telemetry ingest") {
		t.Fatalf("%s", evidenceTexts(got))
	}
	if got := composeEvidence(demoEvidence(), "Favorite editor?", "", false); len(got) != 0 {
		t.Fatalf("%s", evidenceTexts(got))
	}
}
