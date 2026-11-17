package kb

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
)

var mineAnswers = map[string]string{
	"situation": "Our on-call rotation was drowning in alerts after the Q3 migration.",
	"task":      "I was asked to make the pager sustainable before the holiday freeze.",
	"tension":   "The platform lead wanted to silence alerts; I worried we would miss a real outage.",
	"action":    "I proposed grouping alerts by cause and I wrote the grouping rules myself.",
	"result":    "Pages dropped from 200 a week to 15 a week.",
}

func mineAll(t *testing.T, fx *fixture, id string) MiningSession {
	t.Helper()
	var m MiningSession
	for _, f := range MiningFields {
		var err error
		cur, _ := fx.svc.GetMining(fx.ctx, id)
		m, err = fx.svc.MiningReply(fx.ctx, id, mineAnswers[f], len(cur.Log))
		if err != nil {
			t.Fatalf("reply for %s: %v", f, err)
		}
	}
	return m
}

func TestMining_FixedOrderOfPrompts(t *testing.T) {
	fx := newFixture(t)
	m, err := fx.svc.StartMining(fx.ctx, MiningStart{Quadrant: QuadTeam})
	if err != nil || m.State != MineCollecting || m.Next == nil || m.Next.Field != "situation" || m.SourceType != "team-conflict" {
		t.Fatalf("start = %+v, %v", m, err)
	}
	var seen []string
	for _, f := range MiningFields {
		if m.Next == nil || m.Next.Field != f {
			t.Fatalf("expected a question about %s, got %+v", f, m.Next)
		}
		seen = append(seen, m.Next.Field)
		m, err = fx.svc.MiningReply(fx.ctx, m.ID, mineAnswers[f], len(m.Log))
		if err != nil {
			t.Fatal(err)
		}
		if got := m.Fields[f]; got.Text != mineAnswers[f] || got.Source != "user" {
			t.Fatalf("field %s = %+v", f, got)
		}
	}
	if m.State != MineReady || m.Next != nil || len(seen) != 5 {
		t.Fatalf("session = %+v", m)
	}
	// The transcript holds each question and each reply.
	q, r := 0, 0
	for _, e := range m.Log {
		switch e.Role {
		case "question":
			q++
		case "reply":
			r++
		}
	}
	if q != 5 || r != 5 {
		t.Fatalf("log: %d questions, %d replies", q, r)
	}
	if _, err := fx.svc.StartMining(fx.ctx, MiningStart{Quadrant: "nonsense"}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("bad quadrant = %v", err)
	}
}

func TestMining_CannotSaveEarly(t *testing.T) {
	fx := newFixture(t)
	m, _ := fx.svc.StartMining(fx.ctx, MiningStart{Quadrant: QuadProject})
	for _, f := range MiningFields[:3] {
		m, _ = fx.svc.MiningReply(fx.ctx, m.ID, mineAnswers[f], len(m.Log))
	}
	_, err := fx.svc.SaveMining(fx.ctx, m.ID)
	if !errors.Is(err, ErrConflict) || !strings.Contains(err.Error(), "action") || !strings.Contains(err.Error(), miningPrompts["action"]) {
		t.Fatalf("early save = %v", err)
	}
	if list, _ := fx.svc.ListDrafts(fx.ctx, true); len(list) != 0 {
		t.Fatal("an early save created a draft")
	}
	got, _ := fx.svc.GetMining(fx.ctx, m.ID)
	if got.Next == nil || got.Next.Field != "action" {
		t.Fatalf("next = %+v", got.Next)
	}
}

func TestMining_SurvivesRestart(t *testing.T) {
	fx := newFixture(t)
	m, _ := fx.svc.StartMining(fx.ctx, MiningStart{Quadrant: QuadFailure})
	m, _ = fx.svc.MiningReply(fx.ctx, m.ID, mineAnswers["situation"], len(m.Log))
	if m.Next.Field != "task" || m.Next.Text == "" {
		t.Fatalf("next = %+v", m.Next)
	}
	// A new Service over the same database resumes at the same prompt.
	again, err := New(fx.st, Options{Workspace: fx.ws, Now: fx.clk.now})
	if err != nil {
		t.Fatal(err)
	}
	got, err := again.GetMining(fx.ctx, m.ID)
	if err != nil || got.Next == nil || got.Next.Field != "task" || got.Next.Text != m.Next.Text || got.Fields["situation"].Text != mineAnswers["situation"] {
		t.Fatalf("resumed = %+v, %v", got, err)
	}
	if len(got.Log) != len(m.Log) {
		t.Fatal("the transcript was not kept")
	}
}

func TestMining_SaveIdempotent_PostReadyIgnored(t *testing.T) {
	fx := newFixture(t)
	m, _ := fx.svc.StartMining(fx.ctx, MiningStart{Quadrant: QuadTeam})
	m = mineAll(t, fx, m.ID)
	// A reply after every field is filled is not recorded.
	logLen := len(m.Log)
	late, err := fx.svc.MiningReply(fx.ctx, m.ID, "And also, I forgot to mention a thing.", len(m.Log))
	if err != nil || !late.Ignored || len(late.Log) != logLen || late.Fields["result"].Text != mineAnswers["result"] {
		t.Fatalf("late reply = %+v, %v", late, err)
	}
	a, err := fx.svc.SaveMining(fx.ctx, m.ID)
	if err != nil {
		t.Fatal(err)
	}
	b, err := fx.svc.SaveMining(fx.ctx, m.ID)
	if err != nil || a.ID != b.ID {
		t.Fatalf("second save = %+v, %v", b, err)
	}
	if list, _ := fx.svc.ListDrafts(fx.ctx, true); len(list) != 1 {
		t.Fatalf("drafts = %d", len(list))
	}
	if strings.Contains(a.Markdown, "forgot to mention") {
		t.Fatal("an ignored reply reached the draft")
	}
	after, _ := fx.svc.MiningReply(fx.ctx, m.ID, "Even later.", len(m.Log))
	if !after.Ignored || after.State != MineSaved || after.DraftID != a.ID {
		t.Fatalf("saved session = %+v", after)
	}
}

// A reply quotes the transcript length it answers: the same reply sent twice
// does not land in the next field.
func TestMining_ResubmittedReplyIsStale(t *testing.T) {
	fx := newFixture(t)
	m, _ := fx.svc.StartMining(fx.ctx, MiningStart{Quadrant: QuadTeam})
	at := len(m.Log)
	if _, err := fx.svc.MiningReply(fx.ctx, m.ID, mineAnswers["situation"], at); err != nil {
		t.Fatal(err)
	}
	again, err := fx.svc.MiningReply(fx.ctx, m.ID, mineAnswers["situation"], at)
	if !errors.Is(err, ErrStale) {
		t.Fatalf("resubmitted reply = %v", err)
	}
	if cur, _ := fx.svc.GetMining(fx.ctx, m.ID); cur.Fields["task"].Text != "" {
		t.Fatalf("the resubmitted reply became the task: %+v (%+v)", cur.Fields, again)
	}
}

// Saving again returns the draft's newest revision; once the draft is
// deleted, saving again makes a new draft from the transcript.
func TestMining_SaveAgainFollowsTheDraft(t *testing.T) {
	fx := newFixture(t)
	m, _ := fx.svc.StartMining(fx.ctx, MiningStart{Quadrant: QuadTeam})
	m = mineAll(t, fx, m.ID)
	first, err := fx.svc.SaveMining(fx.ctx, m.ID)
	if err != nil {
		t.Fatal(err)
	}
	confirmed, err := fx.svc.ConfirmDraftFacts(fx.ctx, first.ID, nil)
	if err != nil {
		t.Fatal(err)
	}
	again, err := fx.svc.SaveMining(fx.ctx, m.ID)
	if err != nil || again.ID != confirmed.ID || again.Status != StatusDraft {
		t.Fatalf("save after a revision = %+v, %v; want draft %d", again, err, confirmed.ID)
	}
	if _, err := fx.svc.DeleteDraft(fx.ctx, again.ID); err != nil {
		t.Fatal(err)
	}
	remade, err := fx.svc.SaveMining(fx.ctx, m.ID)
	if err != nil || remade.ID == 0 || remade.ID == again.ID || remade.Status != StatusDraft {
		t.Fatalf("save after a delete = %+v, %v", remade, err)
	}
}

func TestMining_DraftFactsAreUserSuppliedAndPending(t *testing.T) {
	fx := newFixture(t)
	m, _ := fx.svc.StartMining(fx.ctx, MiningStart{Quadrant: QuadCustomer})
	m = mineAll(t, fx, m.ID)
	d, _ := fx.svc.SaveMining(fx.ctx, m.ID)
	if d.Kind != DraftStory || d.Origin != OriginMining || d.Status != StatusDraft || len(d.Facts) != 5 {
		t.Fatalf("draft = %+v", d)
	}
	for _, f := range d.Facts {
		if f.Origin != FactUser || !f.Pending {
			t.Fatalf("fact = %+v", f)
		}
	}
	sn, err := ParseSnippet("draft.md", []byte(d.Markdown))
	if err != nil || sn.StorySourceType != "customer" || len(sn.AllowedFacts) != 0 || !sn.ExplicitFacts {
		t.Fatalf("snippet = %+v, %v", sn, err)
	}
	for _, f := range MiningFields {
		if !strings.Contains(d.Markdown, mineAnswers[f]) {
			t.Errorf("the draft lacks the user's %s", f)
		}
	}
}

func TestMining_DraftsOnly_QualityGatesApply(t *testing.T) {
	fx := newFixture(t)
	m, _ := fx.svc.StartMining(fx.ctx, MiningStart{Quadrant: QuadTeam})
	m = mineAll(t, fx, m.ID)
	d, _ := fx.svc.SaveMining(fx.ctx, m.ID)
	if fx.exists("knowledge/behavioral/" + Slug(d.SnippetID) + ".md") {
		t.Fatal("saving published the story")
	}
	if r := fx.search("on-call rotation alerts pager sustainable"); len(r.Facts) != 0 {
		t.Fatal("a mined draft is retrievable")
	}
	// No principles yet: the gate refuses and says so.
	if _, err := fx.svc.PublishStory(fx.ctx, d.ID); !isGate(err) || !strings.Contains(err.Error(), "at least two") {
		t.Fatalf("publish = %v", err)
	}
	// The user picks principles; unconfirmed statements are not promoted.
	edited := strings.Replace(d.Markdown, "principles: []", "principles: [collaboration, disagreement]", 1)
	d2, err := fx.svc.RefineDraft(fx.ctx, d.ID, edited, d.Facts)
	if err != nil {
		t.Fatal(err)
	}
	out, err := fx.svc.PublishStory(fx.ctx, d2.ID)
	if err != nil {
		t.Fatalf("publish after edit: %v", err)
	}
	if len(out.NotPromoted) != 5 {
		t.Fatalf("unconfirmed statements were promoted: %+v", out)
	}
	if file := fx.read(out.Path); strings.Contains(file, "Pages dropped from 200") && strings.Contains(strings.SplitN(file, "---", 3)[1], "Pages dropped") {
		t.Fatal("allowed_facts holds an unconfirmed statement")
	}
}

func TestMining_ConfirmedFactsArePromoted(t *testing.T) {
	fx := newFixture(t)
	m, _ := fx.svc.StartMining(fx.ctx, MiningStart{Quadrant: QuadTeam})
	m = mineAll(t, fx, m.ID)
	d, _ := fx.svc.SaveMining(fx.ctx, m.ID)
	confirmed, err := fx.svc.ConfirmDraftFacts(fx.ctx, d.ID, nil)
	if err != nil || confirmed.Revision != 2 {
		t.Fatalf("confirm = %+v, %v", confirmed, err)
	}
	edited := strings.Replace(confirmed.Markdown, "principles: []", "principles: [collaboration, disagreement]", 1)
	d3, _ := fx.svc.RefineDraft(fx.ctx, confirmed.ID, edited, confirmed.Facts)
	out, err := fx.svc.PublishStory(fx.ctx, d3.ID)
	if err != nil || len(out.NotPromoted) != 0 {
		t.Fatalf("publish = %+v, %v", out, err)
	}
	if file := fx.read(out.Path); !strings.Contains(file, "allowed_facts:") || !strings.Contains(file, "Pages dropped from 200 a week to 15 a week.") {
		t.Fatalf("confirmed facts missing:\n%s", file)
	}
}

func TestMining_QualityFindingsOnMinedDraft(t *testing.T) {
	fx := newFixture(t)
	m, _ := fx.svc.StartMining(fx.ctx, MiningStart{Quadrant: QuadTeam})
	for _, f := range MiningFields {
		text := mineAnswers[f]
		if f == "action" {
			text = "We decided to group alerts and we wrote the rules and we shipped it together with the team."
		}
		m, _ = fx.svc.MiningReply(fx.ctx, m.ID, text, len(m.Log))
	}
	d, _ := fx.svc.SaveMining(fx.ctx, m.ID)
	findings, err := fx.storyFindings(d.ID)
	if err != nil {
		t.Fatal(err)
	}
	codes := codesOf(findings)
	if codes[FindWeOveruse] != SevWarn || codes[GatePrincipleMinimum] != SevBlock {
		t.Fatalf("findings = %+v", findings)
	}
}

func TestMining_RepliesAreScannedForSecrets(t *testing.T) {
	fx := newFixture(t)
	m, _ := fx.svc.StartMining(fx.ctx, MiningStart{Quadrant: QuadProject})
	key := "sk" + "-" + "ant-api03-" + strings.Repeat("Q9x", 9)
	_, err := fx.svc.MiningReply(fx.ctx, m.ID, "the key was "+key, len(m.Log))
	if !errors.Is(err, ErrSecret) || strings.Contains(err.Error(), key) {
		t.Fatalf("err = %v", err)
	}
	got, _ := fx.svc.GetMining(fx.ctx, m.ID)
	if got.Fields["situation"].Text != "" || len(got.Log) != 1 {
		t.Fatalf("session = %+v", got)
	}
	if _, err := fx.svc.MiningReply(fx.ctx, m.ID, "   ", len(m.Log)); !errors.Is(err, ErrInvalid) {
		t.Fatalf("empty reply = %v", err)
	}
	if _, err := fx.svc.StartMining(fx.ctx, MiningStart{Quadrant: QuadProject, Notes: "notes with " + key}); !errors.Is(err, ErrSecret) {
		t.Fatalf("secret in notes = %v", err)
	}
}

const pastedNotes = "Last spring the checkout service paged us every night after the queue upgrade. I was asked to stabilise it. " +
	"We cut the median checkout time from 4 seconds to 900 ms by caching the price lookups."

func TestMining_StartedFromNotes(t *testing.T) {
	model := &fakeModel{replies: []string{
		`{"situation":"Last spring the checkout service paged us every night after the queue upgrade.","task":"","tension":"made up tension","action":"","result":"We cut the median checkout time from 4 seconds to 900 ms by caching the price lookups."}`,
		"What were you asked to do about it?",
	}}
	fx := newFixture(t, func(o *Options) { o.Model = model })
	m, err := fx.svc.StartMining(fx.ctx, MiningStart{Quadrant: QuadProject, Notes: pastedNotes})
	if err != nil {
		t.Fatal(err)
	}
	if v := m.Fields["situation"]; v.Source != "notes" || !strings.Contains(pastedNotes, v.Text) {
		t.Fatalf("situation = %+v", v)
	}
	if v := m.Fields["result"]; v.Source != "notes" {
		t.Fatalf("result = %+v", v)
	}
	if _, invented := m.Fields["tension"]; invented {
		t.Fatal("a passage that is not in the notes was accepted")
	}
	if m.Next == nil || m.Next.Field != "task" {
		t.Fatalf("first question = %+v", m.Next)
	}
	if !strings.Contains(model.systems[0], "untrusted") {
		t.Fatal("the notes prompt does not mark the notes as untrusted")
	}
	// Questions are not reworded unless that is switched on.
	if len(model.prompts) != 1 {
		t.Fatalf("model calls = %d", len(model.prompts))
	}
	// Without the model the notes cannot be read.
	plain := newFixture(t)
	if _, err := plain.svc.StartMining(plain.ctx, MiningStart{Quadrant: QuadProject, Notes: pastedNotes}); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("no model = %v", err)
	}
}

func TestMining_NotesFileMustBeInsideTheWorkspace(t *testing.T) {
	model := &fakeModel{reply: `{"situation":"The queue upgrade paged us nightly.","task":"","tension":"","action":"","result":""}`}
	fx := newFixture(t, func(o *Options) { o.Model = model })
	fx.write("notes/checkout.md", "The queue upgrade paged us nightly. More text.")
	m, err := fx.svc.StartMining(fx.ctx, MiningStart{Quadrant: QuadProject, NotesFile: "notes/checkout.md"})
	if err != nil || m.NotesFrom != "file:notes/checkout.md" || m.Fields["situation"].Source != "notes" {
		t.Fatalf("session = %+v, %v", m, err)
	}
	outside := filepath.Join(t.TempDir(), "outside.md")
	os.WriteFile(outside, []byte("outside"), 0o600)
	fx.write(".applysmith/held.md", "The queue upgrade paged us nightly.")
	for _, p := range []string{outside, "../outside.md", ".applysmith/applysmith.db", ".applysmith/held.md", ".Applysmith/held.md", "notes/missing.md"} {
		if _, err := fx.svc.StartMining(fx.ctx, MiningStart{Quadrant: QuadProject, NotesFile: p}); err == nil {
			t.Errorf("notes file %q was read", p)
		}
	}
	if err := os.Symlink(outside, filepath.Join(fx.ws.Root, "notes", "link.md")); err == nil {
		if _, err := fx.svc.StartMining(fx.ctx, MiningStart{Quadrant: QuadProject, NotesFile: "notes/link.md"}); err == nil {
			t.Error("a symlinked notes file was read")
		}
	}
	if _, err := fx.svc.StartMining(fx.ctx, MiningStart{Quadrant: QuadProject, Notes: "x", NotesFile: "notes/checkout.md"}); !errors.Is(err, ErrInvalid) {
		t.Errorf("both sources = %v", err)
	}
}

func TestMining_Abandon(t *testing.T) {
	fx := newFixture(t)
	m, _ := fx.svc.StartMining(fx.ctx, MiningStart{Quadrant: QuadProject})
	m, err := fx.svc.AbandonMining(fx.ctx, m.ID)
	if err != nil || m.State != MineAbandoned {
		t.Fatalf("abandon = %+v, %v", m, err)
	}
	late, _ := fx.svc.MiningReply(fx.ctx, m.ID, "still here", len(m.Log))
	if !late.Ignored {
		t.Fatal("an abandoned session took a reply")
	}
	if _, err := fx.svc.SaveMining(fx.ctx, m.ID); !errors.Is(err, ErrConflict) {
		t.Fatalf("save abandoned = %v", err)
	}
}

func TestMining_DeleteRemovesSessionAndUnpublishedDraft(t *testing.T) {
	fx := newFixture(t)
	m, _ := fx.svc.StartMining(fx.ctx, MiningStart{Quadrant: QuadTeam})
	m = mineAll(t, fx, m.ID)
	d, _ := fx.svc.SaveMining(fx.ctx, m.ID)
	m, _ = fx.svc.GetMining(fx.ctx, m.ID)
	token, err := fx.svc.DeleteMining(fx.ctx, m.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		// Undo brings the session and its draft back as they were.
		if _, err := fx.svc.Restore(fx.ctx, token); err != nil {
			t.Fatal(err)
		}
		gotM, _ := fx.svc.GetMining(fx.ctx, m.ID)
		gotD, _ := fx.svc.GetDraft(fx.ctx, d.ID)
		if !reflect.DeepEqual(gotM, m) || !reflect.DeepEqual(gotD, d) {
			t.Fatalf("restored %+v and %+v", gotM, gotD)
		}
	}()
	if _, err := fx.svc.GetMining(fx.ctx, m.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("session survives: %v", err)
	}
	if _, err := fx.svc.GetDraft(fx.ctx, d.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("draft survives: %v", err)
	}
}

// The eval set: essay questions that stopped real fill-only runs, each with
// the type the interview fills for it ("" for one no type covers yet). Every
// type's fields have a prompt.
func TestClassifyEssay_TheStuckQuestionsOfRealRuns(t *testing.T) {
	for _, c := range []struct{ company, q, want string }{
		{"Databricks", "Describe a time you worked directly with an external enterprise customer on an ML/AI problem.", EssayCustomer},
		{"Hopper", "Describe a time you presented analytical findings directly to an external customer or partner. What was the question, what did you tell them, and how did they react?", EssayCustomer},
		{"Ocrolus", "Tell us about a time you worked directly with a lender’s operations or technology leadership to diagnose a problem or roll out a change in Encompass.", EssayCustomer},
		{"Firecrawl", "Tell us about a conversion, activation, or funnel metric you personally owned as an engineer. Where did it start, where did you take it, and what did you actually build or change to move it? We want the real numbers.", EssayMetric},
		{"Khan Academy", "Tell us about a technical SEO improvement you personally implemented and shipped. What problem did you identify, what code or system changes did you make, and what impact did you measure?", EssayMetric},
		{"Dave", "Describe the most impactful marketing or growth-focused machine learning model you've built. Please include: The business problem you were solving, the type of model (e.g., churn, propensity, LTV, cross-sell, lookalike, attribution, uplift), how the model was used by the business, and the measurable", EssayModel},
		{"Dave", "Tell us about a project where you partnered closely with Marketing, Growth, CRM, or Lifecycle teams. Please describe: Who your key stakeholders were, how you translated business goals into a modeling solution, any challenges aligning technical and business priorities, the impact of the project.", EssayPartnership},
		{"Dave", "What experience do you have building models that influence customer acquisition, retention, engagement, or monetization? Please provide specific examples and include: The customer behavior you were predicting, the modeling approach you used, how the outputs informed marketing or product decisions, a", EssayModel},
		{"PlantingSpace", "What was a model that you have built for someone else, how was it useful for them and what were its limitations? Keep it short and sweet.", EssayModel},
		{"Apollo.io", "Describe a vulnerability you found in a SaaS application (e.g., authz/IDOR, SSRF, OAuth) and how you validated the fix. What bypass or variant did you test for, and did you write or review the patch yourself?", EssaySecurity},
		{"Mapbox", "Describe a security issue you found in an AWS environment. How did you find it, and what happened next?", EssaySecurity},
		{"Kinetic", "How did you use AI to ship this project?", EssayAI},
		{"Sourcegraph", "Describe your hands-on experience with coding agents. What are you opinionated about? Where do they shine, and where do you insist on deterministic code or human judgment?", EssayAI},
		{"Close", "Briefly describe your experience working remotely.", EssayRemote},
		{"ElevenLabs", "Have you used ElevenLabs - even in a personal or side project? What did you build or explore?", EssayProduct},
		{"Hugging Face", "Share 2–3 of your open-source contributions with links (GitHub PRs, issues, or projects you maintain), and a quick note on your role and what each one involved.", EssayOpenSource},
		{"Roboflow", "Share one link to a page or project you built that showcases why you'd be great at this role.", EssayLink},
		{"Finch", "Please share a link to a mobile product that you have built.", EssayLink},
		{"3y Health", "Please share the URL of a personal project you built that is currently live and usable by anyone. Briefly explain what you built and whether it includes user authentication, payments, or other production features.", EssayLink},
		{"Svix", "Proof of excellence Share a link to something you've done that will impress us.", EssayLink},
		{"Krea", "Share relevant links from your work or portfolio.", EssayLink},
		{"Runway", "In 3-5 bullet points, please share why you're interested in a position at Runway.", EssayMotivation},
		{"Column", "How did you hear about Column? Why are you interested in this role here?", EssayMotivation},
		{"MotherDuck", "What interests you in MotherDuck?", EssayMotivation},
		{"Sumble", "What is one thing you think we should improve about Sumble?", EssayMotivation},
		{"Ideogram", "Describe an enterprise deal you’re especially proud of — ideally one that started cold or was considered highly unlikely. How did you make it happen? Explain the impact and outcome.", EssaySalesDeal},
		{"Wonderschool", "Describe a full-cycle small-business sale you closed, from prospecting through close, including the outcome.", EssaySalesDeal},
		{"Hex", "Describe a deal where you and the AE saw the customer situation differently. How did you handle it, and how did it resolve?", EssaySalesDeal},
		{"Ambience", "What is the largest enterprise health system deal you have personally owned end-to-end? Please include the health system name, total contract value, your specific role, and the outcome.", EssaySalesDeal},
		{"Aven", "What is the highest-impact project that you have done?", EssayProud},
		{"Aven", "What is the hardest thing you have ever worked on in your life?", EssayProud},
		{"Aven", "What do you think is your single biggest accomplishment to date?", EssayProud},
		{"Kinetic", "Tell us about one product, feature, or system you shipped that you're genuinely proud of", EssayProud},
		{"Nomic", "Please share a detailed description of a problem you've solved in the past year that you think best exemplifies the skills needed for this role at Nomic.", EssayProud},
		{"Ramp", "Please share some details about your proudest Android feature in 2 to 3 sentences. What made it challenging and what was the impact?", EssayProud},
		{"Astranis", "What is the most impressive thing you have ever accomplished?", EssayProud},
		{"PlantingSpace", "What was the most complex piece of logic that you have implemented within a commercial application ? Keep it short and sweet.", EssayProud},
		{"Expensify", "Tell us about a project where you had a lot of freedom and little direction. How did you decide what to do and whether you’d succeeded?", EssayProud},
		{"LiveKit", "Tell us about a project you're proud of!", EssayProud},
		{"Arketa", "What are you proudest of that you’ve built in the past year?", EssayProud},
		{"Bastion", "What is the best idea you have ever had?", EssayProud},
		{"Arketa", "What has shifted the way you work the most over the past year?", ""},
		{"fal", "Describe your experience administering payroll tax processes across multiple states.", ""},
	} {
		got, ok := ClassifyEssay(c.q)
		if got.ID != c.want || ok != (c.want != "") {
			t.Errorf("%s %q: got %q, want %q", c.company, c.q, got.ID, c.want)
		}
	}
	for _, et := range essayTypes {
		for _, f := range et.Fields {
			if essayPrompts[f] == "" {
				t.Errorf("type %s: field %s has no prompt", et.ID, f)
			}
		}
		for _, n := range et.Numbers {
			if !slices.Contains(et.Fields, n) {
				t.Errorf("type %s: number field %s is not one of its fields", et.ID, n)
			}
		}
	}
}

const apolloTarget = "Describe a vulnerability you found in a SaaS application (e.g., authz/IDOR, SSRF, OAuth) and how you validated the fix. What bypass or variant did you test for, and did you write or review the patch yourself?"

func reply(t *testing.T, fx *fixture, id, text string) MiningSession {
	t.Helper()
	cur, _ := fx.svc.GetMining(fx.ctx, id)
	m, err := fx.svc.MiningReply(fx.ctx, id, text, len(cur.Log))
	if err != nil {
		t.Fatalf("reply %q: %v", text, err)
	}
	return m
}

func TestMining_TargetedSessionAsksTheEssaysFieldsAndTakesSkips(t *testing.T) {
	fx := newFixture(t)
	m, err := fx.svc.StartMining(fx.ctx, MiningStart{Target: apolloTarget})
	if err != nil || m.Essay != EssaySecurity || m.Quadrant != QuadProject || m.Target != apolloTarget || m.Next == nil || m.Next.Field != "finding" || m.Next.Text != essayPrompts["finding"] {
		t.Fatalf("start = %+v, %v", m, err)
	}
	if m.Log[0].Role != "target" || m.Log[0].Field != EssaySecurity {
		t.Fatalf("log = %+v", m.Log)
	}
	m = reply(t, fx, m.ID, "An IDOR in our admin API let one tenant read another tenant's invoices by changing the id.")
	// The risk is the whole Task of the story, so it cannot be skipped.
	if _, err := fx.svc.MiningReply(fx.ctx, m.ID, "skip", len(m.Log)); !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), "risk") {
		t.Fatalf("skipping a whole part = %v", err)
	}
	m = reply(t, fx, m.ID, "Any customer could read every invoice; we rated it critical.")
	m = reply(t, fx, m.ID, "I wrote the patch: an ownership check in the repository layer.")
	// The validation shares the Result with the variant, so one of them may go.
	m = reply(t, fx, m.ID, "\u8df3\u8fc7")
	if v := m.Fields["validation"]; v.Source != sourceSkipped || m.Next.Field != "variant" {
		t.Fatalf("skip = %+v, next %+v", v, m.Next)
	}
	if _, err := fx.svc.MiningReply(fx.ctx, m.ID, "I don't know", len(m.Log)); !errors.Is(err, ErrInvalid) {
		t.Fatalf("skipping the last field of a part = %v", err)
	}
	// "No" and "none" are answers, not skips.
	// A reply that only mentions not knowing ("I don't know the exact figure,
	// but about half") is an answer.
	if !skipRE.MatchString("Skip.") || !skipRE.MatchString("\u4e0d\u77e5\u9053\u3002") || skipRE.MatchString("None") || skipRE.MatchString("No") ||
		skipRE.MatchString("\u6211\u4e0d\u77e5\u9053\u5177\u4f53\u6570\u5b57\uff0c\u5927\u7ea6\u4e00\u534a") {
		t.Fatal("skip wording")
	}
	m = reply(t, fx, m.ID, "I replayed the request with ids of other tenants and with a guessed UUID.")
	if m.State != MineReady {
		t.Fatalf("state = %s, next %+v", m.State, m.Next)
	}
	got, err := fx.svc.GetMining(fx.ctx, m.ID)
	if err != nil || got.Essay != EssaySecurity || got.Target != apolloTarget || got.State != MineReady {
		t.Fatalf("reloaded = %+v, %v", got, err)
	}
	d, err := fx.svc.SaveMining(fx.ctx, m.ID)
	if err != nil {
		t.Fatal(err)
	}
	sn, err := ParseSnippet("draft.md", []byte(d.Markdown))
	if err != nil || sn.StorySourceType != "work" || !strings.HasPrefix(sn.Title, "An IDOR in our admin API") {
		t.Fatalf("snippet = %+v, %v", sn, err)
	}
	if res, _ := storyPart(sn, "Result"); !strings.Contains(res, "guessed UUID") {
		t.Fatalf("result = %q", res)
	}
	if task, _ := storyPart(sn, "Task"); !strings.Contains(task, "rated it critical") {
		t.Fatalf("task = %q", task)
	}
	if len(d.Facts) != 4 {
		t.Fatalf("facts = %+v", d.Facts)
	}
}

func TestMining_TargetedSessionAsksOnceForAMissingFigure(t *testing.T) {
	fx := newFixture(t)
	m, err := fx.svc.StartMining(fx.ctx, MiningStart{Target: "Tell us about a conversion, activation, or funnel metric you personally owned as an engineer. We want the real numbers."})
	if err != nil || m.Essay != EssayMetric {
		t.Fatalf("start = %+v, %v", m, err)
	}
	m = reply(t, fx, m.ID, "Trial-to-paid conversion of the self-serve plan.")
	m = reply(t, fx, m.ID, "It was low and flat.")
	if m.Next.Field != "before" || m.Next.Text != numberFollowUp || m.Fields["before"].Source != sourcePartial {
		t.Fatalf("follow-up = %+v, %+v", m.Next, m.Fields["before"])
	}
	m = reply(t, fx, m.ID, "About 4% in January.")
	if v := m.Fields["before"]; v.Source != "user" || v.Text != "It was low and flat.\nAbout 4% in January." || m.Next.Field != "after" {
		t.Fatalf("before = %+v, next %+v", v, m.Next)
	}
	m = reply(t, fx, m.ID, "Much higher by summer.")
	m = reply(t, fx, m.ID, "\u6ca1\u6709\u6570\u5b57")
	if v := m.Fields["after"]; v.Source != "user" || v.Text != "Much higher by summer." || m.Next.Field != "change" {
		t.Fatalf("after = %+v, next %+v", v, m.Next)
	}
	m = reply(t, fx, m.ID, "I rebuilt the onboarding checklist and added a usage nudge on day three.")
	m = reply(t, fx, m.ID, "The checklist was mine; design did the nudge copy.")
	d, err := fx.svc.SaveMining(fx.ctx, m.ID)
	if err != nil {
		t.Fatal(err)
	}
	metric := map[string]bool{}
	for _, f := range d.Facts {
		metric[f.Text] = f.Metric
	}
	if !metric["It was low and flat.\nAbout 4% in January."] || metric["Much higher by summer."] {
		t.Fatalf("facts = %+v", d.Facts)
	}
	// A quadrant session keeps its five fields and takes "skip" as a reply.
	q, _ := fx.svc.StartMining(fx.ctx, MiningStart{Quadrant: QuadProject})
	if q = reply(t, fx, q.ID, "skip"); q.Fields["situation"].Text != "skip" || q.Next.Field != "task" {
		t.Fatalf("quadrant session = %+v", q)
	}
}

func TestMining_AReplyThatTellsMoreFillsTheOpenFields(t *testing.T) {
	said := "We built a churn model for Acme Bank, a regional lender. Their retention team had no way to see who would leave. Churn fell a lot."
	model := &fakeModel{reply: `{"problem":"Their retention team had no way to see who would leave.","actions":"I led everything.","result":"Churn fell a lot."}`}
	fx := newFixture(t, func(o *Options) { o.Model = model })
	m, err := fx.svc.StartMining(fx.ctx, MiningStart{Target: "Describe a time you worked directly with an external enterprise customer on an ML/AI problem."})
	if err != nil || m.Essay != EssayCustomer {
		t.Fatalf("start = %+v, %v", m, err)
	}
	m = reply(t, fx, m.ID, said)
	if v := m.Fields["context"]; v.Text != said || v.Source != "user" {
		t.Fatalf("context = %+v", v)
	}
	if v := m.Fields["problem"]; v.Source != sourceReply || v.Text != "Their retention team had no way to see who would leave." {
		t.Fatalf("problem = %+v", v)
	}
	// Words that are not the user's, and a result without a figure, are left
	// for their questions.
	if _, ok := m.Fields["actions"]; ok {
		t.Fatal("an invented passage was kept")
	}
	if _, ok := m.Fields["result"]; ok {
		t.Fatal("a result without a figure was quoted")
	}
	if m.Next.Field != "actions" || !strings.Contains(model.systems[0], "untrusted") || !strings.Contains(model.prompts[0], "- problem: ") {
		t.Fatalf("next = %+v, prompt %q", m.Next, model.prompts[0])
	}
	quotes := 0
	for _, e := range m.Log {
		if e.Role == "quote" {
			quotes++
		}
	}
	if quotes != 1 {
		t.Fatalf("log = %+v", m.Log)
	}
}
