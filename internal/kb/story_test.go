package kb

import (
	"errors"
	"strings"
	"testing"
)

type storyOpt struct {
	principles string
	source     string
	dims       string
	triggers   string
	facts      string
	situation  string
	task       string
	action     string
	result     string
}

// storyMarkdown renders a story draft with sensible defaults; override fields
// through the map keys principles, source, dims, triggers, facts, situation,
// task, action and result.
func storyMarkdown(id string, o map[string]string) string {
	s := storyOpt{
		principles: "[ownership, delivery]", source: "work", dims: "[metric, tradeoff]", triggers: "[]",
		facts:     "[\"Cut 200 alerts to 15 alerts in 12 minutes\"]",
		situation: "The pager fired all night.",
		task:      "I had to cut the noise before the launch deadline.",
		action:    "I decided to group alerts by cause because the risk of missing a real outage was higher than the cost of one tuning week.",
		result:    "I cut 200 alerts to 15 alerts and the median page time fell to 12 minutes.",
	}
	set := map[string]*string{"principles": &s.principles, "source": &s.source, "dims": &s.dims, "triggers": &s.triggers,
		"facts": &s.facts, "situation": &s.situation, "task": &s.task, "action": &s.action, "result": &s.result}
	for k, v := range o {
		*set[k] = v
	}
	front := "id: " + id + "\nprinciples: " + s.principles + "\nstory_source_type: " + s.source +
		"\ndimensions: " + s.dims + "\ntriggers: " + s.triggers + "\nallowed_facts: " + s.facts
	body := "## Situation\n" + s.situation + "\n\n## Task\n" + s.task + "\n\n## Action\n" + s.action + "\n\n## Result\n" + s.result
	return snippet(front, body)
}

func storyDraft(t *testing.T, f *fixture, id string, o map[string]string, facts ...DraftFact) Draft {
	t.Helper()
	d, err := f.svc.CreateDraft(f.ctx, NewDraft{Kind: DraftStory, Origin: OriginUser, SnippetID: id, Markdown: storyMarkdown(id, o), Facts: facts})
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func codesOf(fs []StoryFinding) map[string]string {
	m := map[string]string{}
	for _, f := range fs {
		m[f.Code] = f.Severity
	}
	return m
}

func TestStory_PrincipleMinimum(t *testing.T) {
	f := newFixture(t)
	d := storyDraft(t, f, "one", map[string]string{"principles": "[ownership]"})
	_, err := f.svc.PublishStory(f.ctx, d.ID)
	var ge *GateError
	if !errors.As(err, &ge) || !strings.Contains(err.Error(), "at least two") {
		t.Fatalf("publish = %v", err)
	}
	if f.exists("knowledge/behavioral/one.md") {
		t.Fatal("a refused story wrote a file")
	}
}

func TestStory_PublishWritesDimensionsTriggersAndFacts(t *testing.T) {
	f := newFixture(t)
	d := storyDraft(t, f, "alerts", nil)
	out, err := f.svc.PublishStory(f.ctx, d.ID)
	if err != nil || out.Path != "knowledge/behavioral/alerts.md" {
		t.Fatalf("publish = %+v, %v", out, err)
	}
	file := f.read(out.Path)
	for _, want := range []string{"dimensions:", "- metric", "- tradeoff", "tell me about a time you took ownership", "tell me about a time you delivered under pressure"} {
		if !strings.Contains(file, want) {
			t.Errorf("published file lacks %q:\n%s", want, file)
		}
	}
	if r := f.search("pager noise alerts launch deadline"); len(r.Facts) == 0 {
		t.Fatal("the published story is not retrievable")
	}
}

func TestStory_PublishPersistsDimensions(t *testing.T) {
	f := newFixture(t)
	d := storyDraft(t, f, "alerts", map[string]string{"dims": "[]"}, DraftFact{Text: "Cut 200 alerts to 15", Origin: FactUser, Dimension: "metric", Metric: true, Edited: true})
	out, err := f.svc.PublishStory(f.ctx, d.ID)
	if err != nil {
		t.Fatal(err)
	}
	sn, _ := ParseSnippet(out.Path, []byte(f.read(out.Path)))
	if !contains(sn.Dimensions, "metric") {
		t.Fatalf("dimensions = %v", sn.Dimensions)
	}
}

func TestStory_InferredMetricNotPromoted(t *testing.T) {
	f := newFixture(t)
	d := storyDraft(t, f, "repo-story", map[string]string{"facts": "[]"},
		DraftFact{Text: "Owned 412 commits in the service", Origin: FactInferred, Metric: true},
		DraftFact{Text: "Edited by the user: 3 regions", Origin: FactInferred, Metric: true, Edited: true},
		DraftFact{Text: "Reviewed the API surface", Origin: FactCited})
	out, err := f.svc.PublishStory(f.ctx, d.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(out.NotPromoted) != 1 || !strings.Contains(out.NotPromoted[0], "412 commits") {
		t.Fatalf("not promoted = %v", out.NotPromoted)
	}
	file := f.read(out.Path)
	if strings.Contains(file, "412 commits") || !strings.Contains(file, "3 regions") || !strings.Contains(file, "Reviewed the API surface") {
		t.Fatalf("allowed_facts wrong:\n%s", file)
	}
}

func TestStory_PlaceholderMetricRefusedAndFollowUpsAsk(t *testing.T) {
	f := newFixture(t)
	d := storyDraft(t, f, "todo-story", map[string]string{"result": "The page time fell by TODO minutes."})
	if _, err := f.svc.PublishStory(f.ctx, d.ID); !isGate(err) {
		t.Fatalf("publish = %v", err)
	}
	qs, err := f.svc.FollowUps(f.ctx, d.ID)
	if err != nil || len(qs) == 0 || qs[0].Kind != "metric" || !strings.Contains(qs[0].Question, "real figure") {
		t.Fatalf("follow-ups = %+v, %v", qs, err)
	}
}

func TestStory_FollowupAnswer_ClearsTODO_Idempotent(t *testing.T) {
	f := newFixture(t)
	d := storyDraft(t, f, "todo-story", map[string]string{"result": "I cut triage time from 40 to TODO minutes."})
	qs, _ := f.svc.FollowUps(f.ctx, d.ID)
	nd, err := f.svc.RecordAnswer(f.ctx, d.ID, qs[0].ID, "15")
	if err != nil || nd.Revision != 2 || strings.Contains(nd.Markdown, "TODO") || !strings.Contains(nd.Markdown, "from 40 to 15 minutes") {
		t.Fatalf("answer = %+v, %v", nd, err)
	}
	found := false
	for _, fact := range nd.Facts {
		found = found || (fact.Origin == FactUser && fact.Text == "15" && fact.Dimension == "metric")
	}
	if !found {
		t.Fatalf("facts = %+v", nd.Facts)
	}
	findings, _ := f.storyFindings(nd.ID)
	if _, ok := codesOf(findings)[GatePlaceholder]; ok {
		t.Fatal("the TODO finding is still reported")
	}
	// Recording the same answer again changes nothing.
	again, err := f.svc.RecordAnswer(f.ctx, nd.ID, qs[0].ID, "15")
	if err != nil || again.ID != nd.ID {
		t.Fatalf("repeat = %+v, %v", again, err)
	}
	if _, err := f.svc.PublishStory(f.ctx, nd.ID); err != nil {
		t.Fatalf("publish after the answer: %v", err)
	}
}

// A repository read leaves "TODO: add the real figure."; the answer takes the
// place of the whole marker, hint included.
func TestStory_FollowupAnswer_ReplacesTheTODOAndItsHint(t *testing.T) {
	f := newFixture(t)
	d := storyDraft(t, f, "todo-hint", map[string]string{"result": "The noise fell. TODO: add the real figure."})
	qs, _ := f.svc.FollowUps(f.ctx, d.ID)
	nd, err := f.svc.RecordAnswer(f.ctx, d.ID, qs[0].ID, "Pages fell from 200 to 15 a day.")
	if err != nil || !strings.Contains(nd.Markdown, "The noise fell. Pages fell from 200 to 15 a day.\n") || strings.Contains(nd.Markdown, "real figure") {
		t.Fatalf("answer = %v\n%s", err, nd.Markdown)
	}
}

func TestStory_FollowupAnswer_SameRecordFromTwoSurfaces(t *testing.T) {
	f := newFixture(t)
	d := storyDraft(t, f, "dims", map[string]string{"principles": "[disagreement, collaboration]", "source": "team-conflict", "dims": "[]"})
	qs, _ := f.svc.FollowUps(f.ctx, d.ID)
	ids := map[string]bool{}
	for _, q := range qs {
		ids[q.ID] = true
	}
	if !ids["dim-conflict"] || !ids["dim-tradeoff"] {
		t.Fatalf("follow-ups = %+v", qs)
	}
	d2, err := f.svc.RecordAnswer(f.ctx, d.ID, "dim-conflict", "The platform lead wanted a rewrite; I wanted a migration path.")
	if err != nil {
		t.Fatal(err)
	}
	d3, err := f.svc.RecordAnswer(f.ctx, d2.ID, "dim-tradeoff", "We kept two runtimes for a quarter to avoid a freeze.")
	if err != nil {
		t.Fatal(err)
	}
	answers, _ := f.svc.Answers(f.ctx, d3.ID)
	if len(answers) != 2 {
		t.Fatalf("answers = %+v", answers)
	}
	left, _ := f.svc.FollowUps(f.ctx, d3.ID)
	if len(left) != 0 {
		t.Fatalf("questions left: %+v", left)
	}
	if _, err := f.svc.RecordAnswer(f.ctx, d3.ID, "dim-conflict", "x"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("answering a closed question = %v", err)
	}
}

// A repository re-read rewrites the draft with the model's text and facts and
// drops the folded answer; answering the reopened question with the same text
// folds it again rather than reporting success with nothing changed.
func TestStory_FollowupAnswer_SameTextFoldsAgainWhenTheDraftLostIt(t *testing.T) {
	f := newFixture(t)
	d := storyDraft(t, f, "dims", map[string]string{"principles": "[disagreement, collaboration]", "source": "team-conflict", "dims": "[]"})
	answer := "The platform lead wanted a rewrite; I wanted a migration path."
	d2, err := f.svc.RecordAnswer(f.ctx, d.ID, "dim-conflict", answer)
	if err != nil {
		t.Fatal(err)
	}
	reread, err := f.svc.RefineDraft(f.ctx, d2.ID, d.Markdown, d.Facts)
	if err != nil {
		t.Fatal(err)
	}
	d4, err := f.svc.RecordAnswer(f.ctx, reread.ID, "dim-conflict", answer)
	if err != nil || d4.ID == reread.ID {
		t.Fatalf("same answer on a reopened question = %+v, %v", d4, err)
	}
	qs, _ := f.svc.FollowUps(f.ctx, d4.ID)
	for _, q := range qs {
		if q.ID == "dim-conflict" {
			t.Fatalf("dim-conflict is still open: %+v", qs)
		}
	}
}

// A published story whose page the user deleted is drafted again by the
// next repository read; one whose page is still there is left alone.
func TestStory_RepoStoryDraftedAgainAfterItsPageIsDeleted(t *testing.T) {
	f := newFixture(t)
	nd := NewDraft{Kind: DraftStory, Origin: OriginRepo, SnippetID: "reef-story", Markdown: storyMarkdown("reef-story", nil)}
	d, err := f.svc.upsertStoryDraft(f.ctx, nd)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.PublishStory(f.ctx, d.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.upsertStoryDraft(f.ctx, nd); !errors.Is(err, ErrConflict) {
		t.Fatalf("read over a live page = %v", err)
	}
	if _, err := f.svc.Delete(f.ctx, "reef-story"); err != nil {
		t.Fatal(err)
	}
	again, err := f.svc.upsertStoryDraft(f.ctx, nd)
	if err != nil || again.Status != StatusDraft || again.ID == d.ID {
		t.Fatalf("read after the page was deleted = %+v, %v", again, err)
	}
}

func TestStory_FollowupAnswer_NeverPublishes(t *testing.T) {
	f := newFixture(t)
	d := storyDraft(t, f, "todo-story", map[string]string{"result": "Time fell to TODO minutes."})
	qs, _ := f.svc.FollowUps(f.ctx, d.ID)
	if _, err := f.svc.RecordAnswer(f.ctx, d.ID, qs[0].ID, "15"); err != nil {
		t.Fatal(err)
	}
	if f.exists("knowledge/behavioral/todo-story.md") {
		t.Fatal("recording an answer published the story")
	}
}

func TestStory_DuplicateEvent_Fingerprint(t *testing.T) {
	f := newFixture(t)
	first := storyDraft(t, f, "alerts-a", nil)
	if _, err := f.svc.PublishStory(f.ctx, first.ID); err != nil {
		t.Fatal(err)
	}
	dup := storyDraft(t, f, "alerts-b", map[string]string{
		"facts": "[\"Reduced 200 alerts to 15 alerts, a 12 minutes median\"]", "situation": "Noisy paging during onboarding."})
	findings, _ := f.storyFindings(dup.ID)
	if codesOf(findings)[FindDuplicateEvent] != SevBlock {
		t.Fatalf("findings = %+v", findings)
	}
	_, err := f.svc.PublishStory(f.ctx, dup.ID)
	if !isGate(err) || !strings.Contains(err.Error(), "alerts-a") {
		t.Fatalf("publish = %v", err)
	}
	// Number words count as their digits, and a story with no facts is never compared.
	fp1 := fingerprintOf([]string{"Cut two hundred alerts to fifteen alerts in 12 minutes"})
	fp2 := fingerprintOf([]string{"Cut 200 alerts to 15 alerts in 12 minutes"})
	if !fp1.metrics["15 alert"] || !sameEvent(fp1, fp2) {
		t.Fatalf("metrics = %v and %v are not one event", fp1.metrics, fp2.metrics)
	}
	if sameEvent(fingerprintOf(nil), fp2) {
		t.Fatal("a story with no facts was compared")
	}
}

func TestStory_RepublishNotSelfDuplicate(t *testing.T) {
	f := newFixture(t)
	d := storyDraft(t, f, "alerts-a", nil)
	if _, err := f.svc.PublishStory(f.ctx, d.ID); err != nil {
		t.Fatal(err)
	}
	again := storyDraft(t, f, "alerts-a", nil)
	findings, _ := f.storyFindings(again.ID)
	if _, ok := codesOf(findings)[FindDuplicateEvent]; ok {
		t.Fatal("a story duplicated itself")
	}
	if _, err := f.svc.PublishStory(f.ctx, again.ID); err != nil {
		t.Fatalf("republish = %v", err)
	}
}

func TestStory_TriggerHonesty(t *testing.T) {
	f := newFixture(t)
	d := storyDraft(t, f, "trig", map[string]string{"triggers": "[\"mentored a junior engineer\", \"tell me about shipping a launch\"]"})
	out, err := f.svc.PublishStory(f.ctx, d.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !contains(out.DroppedTriggers, "mentored a junior engineer") || contains(out.DroppedTriggers, "tell me about shipping a launch") {
		t.Fatalf("dropped = %v", out.DroppedTriggers)
	}
	file := f.read(out.Path)
	if strings.Contains(file, "mentored a junior engineer") || !strings.Contains(file, "tell me about shipping a launch") {
		t.Fatalf("file triggers wrong:\n%s", file)
	}
	// With matching evidence the trigger is kept.
	d2 := storyDraft(t, f, "trig2", map[string]string{
		"triggers": "[\"mentored a junior engineer\"]", "facts": "[\"Paired weekly to mentor a new hire through 5 reviews\"]",
		"action": "I decided to mentor the new hire because the team had no onboarding path.",
		"result": "The hire shipped their first change in 9 days."})
	out2, err := f.svc.PublishStory(f.ctx, d2.ID)
	if err != nil || len(out2.DroppedTriggers) != 0 {
		t.Fatalf("publish = %+v, %v", out2, err)
	}
}

func TestStory_WarningsDoNotBlock(t *testing.T) {
	f := newFixture(t)
	d := storyDraft(t, f, "wes", map[string]string{
		"action": "We decided to group alerts because we feared missing a real outage and we agreed we would tune weekly.",
		"task":   "We had to cut the noise before the launch deadline."})
	findings, _ := f.storyFindings(d.ID)
	if codesOf(findings)[FindWeOveruse] != SevWarn {
		t.Fatalf("findings = %+v", findings)
	}
	if _, err := f.svc.PublishStory(f.ctx, d.ID); err != nil {
		t.Fatalf("publish = %v", err)
	}
}

func TestStory_QualityFindings(t *testing.T) {
	f := newFixture(t)
	cases := []struct {
		name string
		o    map[string]string
		want string
		not  string
	}{
		{"no_tension", map[string]string{"situation": "Things were fine.", "task": "I improved a dashboard.", "action": "I rebuilt it.", "result": "It was faster, 3 seconds."}, FindNoTension, ""},
		{"unquantified_result", map[string]string{"result": "It went well and everyone was happy."}, FindUnquantifiedResult, ""},
		{"missing_impact", map[string]string{"result": "Customers were happier with the product."}, FindMissingImpact, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := storyDraft(t, f, "q-"+tc.name, tc.o)
			findings, err := f.storyFindings(d.ID)
			if err != nil {
				t.Fatal(err)
			}
			m := codesOf(findings)
			if m[tc.want] != SevWarn {
				t.Fatalf("want %s in %+v", tc.want, findings)
			}
			if tc.not != "" {
				if _, bad := m[tc.not]; bad {
					t.Fatalf("unexpected %s in %+v", tc.not, findings)
				}
			}
		})
	}
	// A clean story reports none of the warnings.
	d := storyDraft(t, f, "clean", nil)
	findings, _ := f.storyFindings(d.ID)
	for _, fd := range findings {
		if fd.Severity == SevBlock {
			t.Fatalf("clean story blocked: %+v", findings)
		}
	}
}

func TestStory_ProjectTypeReservedForRepos(t *testing.T) {
	f := newFixture(t)
	d, err := f.svc.CreateDraft(f.ctx, NewDraft{Kind: DraftStory, Origin: OriginMining, SnippetID: "mined", Markdown: storyMarkdown("mined", map[string]string{"source": "project"})})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.PublishStory(f.ctx, d.ID); !isGate(err) {
		t.Fatalf("publish = %v", err)
	}
	r, _ := f.svc.CreateDraft(f.ctx, NewDraft{Kind: DraftStory, Origin: OriginRepo, SnippetID: "from-repo", Markdown: storyMarkdown("from-repo", map[string]string{"source": "project"})})
	if _, err := f.svc.PublishStory(f.ctx, r.ID); err != nil {
		t.Fatalf("repo-derived project story: %v", err)
	}
}

func TestStory_MissingSectionBlocks(t *testing.T) {
	f := newFixture(t)
	d, _ := f.svc.CreateDraft(f.ctx, NewDraft{Kind: DraftStory, SnippetID: "short", Markdown: snippet("id: short\nprinciples: [ownership, delivery]\nstory_source_type: work", "## Situation\nText.\n\n## Action\nText.")})
	findings, _ := f.storyFindings(d.ID)
	if codesOf(findings)[GateMissingSection] != SevBlock {
		t.Fatalf("findings = %+v", findings)
	}
}

func TestCompetencies_UserFileReplacesDefaultsAndValidates(t *testing.T) {
	f := newFixture(t)
	f.write("knowledge/competencies.yml", "competencies:\n  - id: craft\n    name: Craft\n    satisfied_by: [work]\n    required_dimensions: [metric]\n    question_patterns: [\"tell me about your best work\", \"describe your craft\"]\n")
	cs, err := f.svc.Competencies()
	if err != nil || len(cs) != 1 || cs[0].ID != "craft" {
		t.Fatalf("competencies = %+v, %v", cs, err)
	}
	f.write("knowledge/competencies.yml", "competencies:\n  - id: Bad Id\n    name: x\n    satisfied_by: [work]\n    question_patterns: [a, b]\n")
	if _, err := f.svc.Competencies(); !errors.Is(err, ErrInvalid) {
		t.Fatalf("invalid file = %v", err)
	}
	if _, err := f.coverage(); err == nil {
		t.Fatal("coverage fell back to the defaults on an invalid user file")
	}
}

// Story text is lower-cased before the shape words are matched, so every
// alternative, "outage I" included, must match in lower case.
func TestCoverage_MistakeShapeMatchesOutageI(t *testing.T) {
	sn := &Snippet{Dimensions: []string{"learning"}, Sections: []Section{{Text: "The outage I caused taught me to stage rollouts."}}}
	for _, d := range shapeDefs {
		if d.id == "mistake" && !d.match(sn, storyText(sn)) {
			t.Fatal("a story about an outage I caused is not a mistake story")
		}
	}
}

// isGate reports a publish refused by the story gates.
func isGate(err error) bool {
	var g *GateError
	return errors.As(err, &g)
}

// coverage grades the bank as the coverage route does: over the index after a sync.
func (f *fixture) coverage() (Coverage, error) {
	if _, err := f.svc.Sync(f.ctx); err != nil {
		return Coverage{}, err
	}
	return f.svc.CoverageIndexed(f.ctx)
}

// storyFindings runs the publish checks of a story draft without publishing it.
func (f *fixture) storyFindings(id int64) ([]StoryFinding, error) {
	d, err := f.svc.GetDraft(f.ctx, id)
	if err != nil {
		return nil, err
	}
	sn, err := ParseSnippet("draft.md", []byte(d.Markdown))
	if err != nil {
		return nil, err
	}
	sn.ID = d.SnippetID
	return f.svc.storyReport(f.ctx, sn, d)
}

func publishStoryFile(f *fixture, id string, o map[string]string) {
	f.write("knowledge/behavioral/"+id+".md", storyMarkdown(id, o))
}

func gradeOf(c Coverage, id string) PrincipleGrade {
	for _, p := range c.Principles {
		if p.ID == id {
			return p
		}
	}
	return PrincipleGrade{}
}

func TestCoverage_HonestyRule(t *testing.T) {
	f := newFixture(t)
	publishStoryFile(f, "s1", map[string]string{"principles": "[disagreement, ownership]", "source": "project", "dims": "[conflict, tradeoff, metric]", "facts": "[\"a\"]"})
	cov, _ := f.coverage()
	if g := gradeOf(cov, "disagreement"); g.Grade != GradeRed || len(g.Stories) != 0 {
		t.Fatalf("disagreement = %+v", g)
	}
}
