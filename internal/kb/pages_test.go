package kb

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

type scriptedModel struct{ reply string }

func (m scriptedModel) Generate(context.Context, string, string) (string, error) { return m.reply, nil }

func TestCreateSnippetPublishesTheUsersOwnTextWithItsTags(t *testing.T) {
	f := newFixture(t)
	out, err := f.svc.CreateSnippet(f.ctx, SnippetInput{Title: "Reef survey summary", Category: CatProjects, Tags: []string{"data", "field"}, Body: "I led a small survey team."})
	if err != nil {
		t.Fatal(err)
	}
	doc, err := f.svc.Show(f.ctx, out.SnippetID)
	if err != nil {
		t.Fatal(err)
	}
	if doc.Title != "Reef survey summary" || len(doc.Tags) != 2 || doc.Body != "I led a small survey team." || doc.Category != CatProjects {
		t.Fatalf("snippet = %+v", doc)
	}
	if _, err := f.svc.CreateSnippet(f.ctx, SnippetInput{Title: "Reef survey summary", Body: "again"}); !errors.Is(err, ErrConflict) {
		t.Fatalf("duplicate title err = %v", err)
	}
	if _, err := f.svc.CreateSnippet(f.ctx, SnippetInput{Title: "  ", Body: "x"}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("blank title err = %v", err)
	}
}

// A title the slug cannot spell whole (Chinese, or a word beside one) is not
// refused and does not take the id of a page with the slug's letters.
func TestNonASCIITitlesGetTheirOwnIDs(t *testing.T) {
	f := newFixture(t)
	if _, err := f.svc.CreateSnippet(f.ctx, SnippetInput{Title: "Kafka", Body: "Ran the Kafka cluster."}); err != nil {
		t.Fatal(err)
	}
	migration := "\u8fc1\u79fb" // "migration"
	ids := map[string]bool{"kafka": true}
	for _, title := range []string{"Kafka " + migration, migration} {
		out, err := f.svc.CreateSnippet(f.ctx, SnippetInput{Title: title, Body: "Moved the queues."})
		if err != nil || out.SnippetID == "" || ids[out.SnippetID] {
			t.Fatalf("CreateSnippet(%q) = %q, %v", title, out.SnippetID, err)
		}
		ids[out.SnippetID] = true
		if _, err := f.svc.CreateSnippet(f.ctx, SnippetInput{Title: title, Body: "again"}); !errors.Is(err, ErrConflict) {
			t.Fatalf("the same title again: %v", err)
		}
	}
	story := StoryInput{Title: migration + " story", SourceType: "work", Principles: []string{"ownership", "delivery"},
		Situation: "The queues were old.", Task: "Move them.", Action: "I moved them.", Result: "All 40 queues moved."}
	if out, err := f.svc.AddStory(f.ctx, story); err != nil || out.DraftID == 0 {
		t.Fatalf("AddStory = %+v, %v", out, err)
	}
}

// A snippet whose publish fails leaves no draft behind, so the same request
// succeeds once the cause is gone.
func TestCreateSnippetThatCannotPublishLeavesNoDraft(t *testing.T) {
	f := newFixture(t)
	f.write("knowledge/projects/reef-notes.md", snippet("id: other-notes\ntitle: Other", "Another page at that path."))
	in := SnippetInput{Title: "Reef notes", Category: CatProjects, Body: "Surveyed the reef."}
	if _, err := f.svc.CreateSnippet(f.ctx, in); !errors.Is(err, ErrConflict) {
		t.Fatalf("publish over another page's file = %v", err)
	}
	if drafts, _ := f.svc.ListDrafts(f.ctx, true); len(drafts) != 0 {
		t.Fatalf("drafts left = %+v", drafts)
	}
	if err := os.Rename(filepath.Join(f.ws.Root, "knowledge/projects/reef-notes.md"), filepath.Join(f.ws.Root, "knowledge/projects/other-notes.md")); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.CreateSnippet(f.ctx, in); err != nil {
		t.Fatalf("retry = %v", err)
	}
}

func TestReviseChangesTitleTagsAndBodyAgainstTheFileHash(t *testing.T) {
	f := newFixture(t)
	out, err := f.svc.CreateSnippet(f.ctx, SnippetInput{Title: "Alerting notes", Body: "Group alerts by cause."})
	if err != nil {
		t.Fatal(err)
	}
	before, _ := f.svc.Show(f.ctx, out.SnippetID)
	title, tags, body := "Alerting notes v2", []string{"ops"}, "Group alerts by cause, then page once."
	after, err := f.svc.Revise(f.ctx, out.SnippetID, Revision{Title: &title, Tags: &tags, Body: &body}, before.FileHash)
	if err != nil {
		t.Fatal(err)
	}
	if after.Title != title || len(after.Tags) != 1 || after.Body != body {
		t.Fatalf("revised = %+v", after)
	}
	if _, err := f.svc.Revise(f.ctx, out.SnippetID, Revision{Body: &body}, before.FileHash); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale hash err = %v", err)
	}
}

func TestAddStoryPublishesOrKeepsTheDraftWithTheBlockingFindings(t *testing.T) {
	f := newFixture(t)
	good := StoryInput{Title: "Quieting the pager", SourceType: "work", Principles: []string{"ownership"},
		Situation: "The pager fired all night.", Task: "I had to cut the noise before the launch deadline.",
		Action: "I decided to group alerts by cause because the risk of missing a real outage was higher than the cost of one tuning week.",
		Result: "I cut 200 alerts to 15 alerts and the median page time fell to 12 minutes."}
	out, err := f.svc.AddStory(f.ctx, good)
	if err != nil {
		t.Fatal(err)
	}
	// One principle: kept as a draft with the blocking finding.
	if out.Published || out.DraftID == 0 || !slices.ContainsFunc(out.Findings, func(fd StoryFinding) bool { return fd.Code == GatePrincipleMinimum }) {
		t.Fatalf("a one-principle story = %+v", out)
	}
	two := good
	two.Title, two.Principles = "Quieting the pager, again", []string{"ownership", "delivery"}
	if out, err := f.svc.AddStory(f.ctx, two); err != nil || !out.Published || out.Path == "" {
		t.Fatalf("a two-principle story = %+v, %v", out, err)
	}
	// Every origin the story gate accepts (SourceTypes) is one a story can be
	// added with; "project" is the form's choice for Ownership.
	for _, origin := range SourceTypes {
		in := good
		in.Title, in.SourceType = "Origin "+origin, origin
		if _, err := f.svc.AddStory(f.ctx, in); err != nil {
			t.Fatalf("origin %s: %v", origin, err)
		}
	}
	bad := good
	bad.Title = "Unknown theme"
	bad.Principles = []string{"not-a-competency"}
	if _, err := f.svc.AddStory(f.ctx, bad); !errors.Is(err, ErrInvalid) {
		t.Fatalf("unknown competency err = %v", err)
	}
	bad.Principles, bad.Result = nil, " "
	if _, err := f.svc.AddStory(f.ctx, bad); !errors.Is(err, ErrInvalid) {
		t.Fatalf("empty result err = %v", err)
	}
}

func TestProposeStoriesStoresNothingAndDropsNumbersTheCVLacks(t *testing.T) {
	cvText := "# Alex Example\n\nAt Example Corp I cut alert volume from 200 to 15 a day for the on-call team.\n"
	reply := `{"stories":[
	 {"title":"Quieter pager","principles":["ownership","made-up"],"situation":"On-call was noisy.","task":"Reduce noise.","action":"I grouped alerts.","result":"Alerts fell from 200 to 15 a day."},
	 {"title":"Invented figure","principles":["delivery"],"situation":"s","task":"t","action":"a","result":"Revenue grew 340%."},
	 {"title":"Part of a figure","principles":["delivery"],"situation":"s","task":"t","action":"a","result":"Revenue grew 20%."}]}`
	f := newFixture(t, func(o *Options) { o.Model = scriptedModel{reply} })
	f.write("cv.md", cvText)
	got, err := f.svc.ProposeStories(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Title != "Quieter pager" || strings.Join(got[0].Principles, ",") != "ownership" {
		t.Fatalf("proposals = %+v", got)
	}
	drafts, err := f.svc.ListDrafts(f.ctx, true)
	if err != nil || len(drafts) != 0 {
		t.Fatalf("proposing stored %d drafts (%v)", len(drafts), err)
	}
	// Once saved, the story is not proposed again.
	p := got[0]
	if _, err := f.svc.AddStory(f.ctx, StoryInput{Title: p.Title, SourceType: "work", Principles: p.Principles, Situation: p.Situation, Task: p.Task, Action: p.Action, Result: p.Result}); err != nil {
		t.Fatal(err)
	}
	if again, err := f.svc.ProposeStories(f.ctx); err != nil || len(again) != 0 {
		t.Fatalf("after saving it: %+v (%v)", again, err)
	}
}

func TestProposeStoriesNeedsAModelAndARealCV(t *testing.T) {
	f := newFixture(t)
	if _, err := f.svc.ProposeStories(f.ctx); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("no model err = %v", err)
	}
	g := newFixture(t, func(o *Options) { o.Model = scriptedModel{`{"stories":[]}`} })
	if _, err := g.svc.ProposeStories(g.ctx); !errors.Is(err, ErrInvalid) {
		t.Fatalf("no cv err = %v", err)
	}
}

// A proposal's number must be a number of the CV, not a piece of one: "20"
// is not in a CV that holds only "2020" and "200".
func TestProposeStoriesDropsANumberThatIsOnlyPartOfACVNumber(t *testing.T) {
	reply := `{"stories":[{"title":"Part of a year","principles":["ownership"],"situation":"s","task":"t","action":"a","result":"Cut 20 alerts."},
	 {"title":"Whole number","principles":["ownership"],"situation":"s","task":"t","action":"a","result":"Cut 200 alerts, in 2020."}]}`
	f := newFixture(t, func(o *Options) { o.Model = scriptedModel{reply} })
	f.write("cv.md", "# Alex Example\n\nIn 2020 I cut 200 alerts.\n")
	got, err := f.svc.ProposeStories(f.ctx)
	if err != nil || len(got) != 1 || got[0].Title != "Whole number" {
		t.Fatalf("proposals = %+v, %v", got, err)
	}
}
