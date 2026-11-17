package intake

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	"applysmith/internal/api/wire"
	"applysmith/internal/workspace"
)

const cvOne = `# Alex Example

## Experience

### Platform Engineer, Example Systems (2019-03 to present)

- Built the deployment pipeline.

## Education

### BSc Computer Science, Northern College (2011-09 to 2015-06)

## Skills

- Languages: Python, SQL
`

const educationRecord = `# Education record

## Education

### BSc Computer Science, Northern College (2011-09 to 2015-05)
`

func profileWith(edu ...workspace.Education) workspace.Profile {
	p := workspace.Profile{}
	p.History.Education = edu
	return p
}

var approvedDegree = workspace.Education{School: "Northern College", Degree: "BSc Computer Science", Start: "2011-09", End: "2015-06"}

func doc(name, kind, text string) wire.IntakeDocument {
	return wire.IntakeDocument{Name: name, Kind: kind, Text: text}
}

func build(t *testing.T, p workspace.Profile, in ...wire.IntakeDocument) ChangeSet {
	t.Helper()
	docs, err := Prepare(in)
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	return Build(docs, p)
}

func byType(cs ChangeSet, typ, kind string) []wire.IntakeItem {
	var out []wire.IntakeItem
	for _, it := range cs.Items {
		if it.Type == typ && it.Kind == kind {
			out = append(out, it)
		}
	}
	return out
}

func TestConflictingGraduationDates_BothSourcesShownAndTheApprovedDateStays(t *testing.T) {
	p := profileWith(approvedDegree)
	cs := build(t, p, doc("cv.md", wire.IntakeKindCV, cvOne), doc("registrar.md", wire.IntakeKindEducationRecord, educationRecord))
	conflicts := byType(cs, wire.IntakeConflict, kindEducation)
	if len(conflicts) != 1 || len(conflicts[0].Fields) != 1 {
		t.Fatalf("want one education conflict on one field, got %+v", cs.Items)
	}
	f := conflicts[0].Fields[0]
	if f.Field != "end" || f.Profile != "2015-06" || len(f.Options) != 2 {
		t.Fatalf("conflict field %+v", f)
	}
	// Both documents are shown with their values: the CV agrees with the approved date, the record does not.
	if o := f.Options[0]; o.Value != "2015-06" || o.Sources[0].Label != "cv.md" {
		t.Fatalf("the CV is not shown as a source: %+v", o)
	}
	if s := f.Options[1].Sources[0]; f.Options[1].Value != "2015-05" || s.Label != "registrar.md" || s.Origin != wire.IntakeDirect || s.Hash == "" || !strings.Contains(s.Locator, "line") {
		t.Fatalf("the record is not named as the source: %+v", f.Options[1])
	}
	if len(byType(cs, wire.IntakeAddition, kindEducation)) != 0 {
		t.Fatal("a conflicting study must not also be proposed as a new entry")
	}
	id := conflicts[0].ID

	if _, err := cs.Select([]wire.IntakeSelection{{ID: id}}); err == nil || !strings.Contains(err.Error(), "choices.end") {
		t.Fatalf("a conflict accepted without a pick: %v", err)
	}
	keep, err := cs.Select([]wire.IntakeSelection{{ID: id, Choices: map[string]string{"end": Keep}}})
	if err != nil || len(keep.Ops) != 0 {
		t.Fatalf("keeping the approved date must change nothing: %+v %v", keep, err)
	}
	pick, err := cs.Select([]wire.IntakeSelection{{ID: id, Choices: map[string]string{"end": "2015-05"}}})
	if err != nil || len(pick.Ops) != 1 || pick.Ops[0].Path != "history.education[0].end" || pick.Ops[0].Value != `"2015-05"` {
		t.Fatalf("picking the record's date: %+v %v", pick, err)
	}
	if _, err := cs.Select([]wire.IntakeSelection{{ID: id, Choices: map[string]string{"end": "2016-01"}}}); err == nil {
		t.Fatal("a date no document gives was accepted")
	}
}

func TestConflictingDates_TwoDocumentsDisagreeWithNoApprovedEntry(t *testing.T) {
	cs := build(t, workspace.Profile{}, doc("cv.md", wire.IntakeKindCV, cvOne), doc("registrar.md", wire.IntakeKindEducationRecord, educationRecord))
	conflicts := byType(cs, wire.IntakeConflict, kindEducation)
	if len(conflicts) != 1 || conflicts[0].Fields[0].Profile != "" || len(conflicts[0].Fields[0].Options) != 2 {
		t.Fatalf("want a conflict with two sides and no approved value: %+v", cs.Items)
	}
	id := conflicts[0].ID
	if _, err := cs.Select([]wire.IntakeSelection{{ID: id, Choices: map[string]string{"end": Keep}}}); err == nil {
		t.Fatal("there is no profile value to keep")
	}
	got, err := cs.Select([]wire.IntakeSelection{{ID: id, Choices: map[string]string{"end": "2015-05"}}})
	if err != nil || len(got.Ops) != 1 || got.Ops[0].Kind != workspace.OpAppend || got.Ops[0].Path != "history.education" ||
		got.Ops[0].Value != `{"school": "Northern College", "degree": "BSc Computer Science", "start": "2011-09", "end": "2015-05"}` {
		t.Fatalf("%+v %v", got, err)
	}
}

// TestDocumentOrderNeverDecidesWhichSourceIsRight: a document carries no
// modification time (docs/architecture/setup.md), so the order the documents
// arrive in is the only "newer" a reader could use, and it must decide nothing.
func TestDocumentOrderNeverDecidesWhichSourceIsRight(t *testing.T) {
	a := build(t, workspace.Profile{}, doc("old.md", wire.IntakeKindCV, cvOne), doc("new.md", wire.IntakeKindEducationRecord, educationRecord))
	b := build(t, workspace.Profile{}, doc("new.md", wire.IntakeKindEducationRecord, educationRecord), doc("old.md", wire.IntakeKindCV, cvOne))
	ca, cb := byType(a, wire.IntakeConflict, kindEducation), byType(b, wire.IntakeConflict, kindEducation)
	if len(ca) != 1 || len(cb) != 1 {
		t.Fatal("the order the documents are given in must not resolve a conflict")
	}
	sides := func(c wire.IntakeItem) []string {
		var out []string
		for _, f := range c.Fields {
			for _, o := range f.Options {
				for _, s := range o.Sources {
					out = append(out, fmt.Sprintf("%s=%s from %+v", f.Field, o.Value, s))
				}
			}
		}
		slices.Sort(out)
		return out
	}
	if sa, sb := sides(ca[0]), sides(cb[0]); len(sa) < 2 || !slices.Equal(sa, sb) {
		t.Fatalf("the conflict's sides differ with the order:\n%q\n%q", sa, sb)
	}
	if len(a.plans) != len(b.plans) || len(a.Items) != len(b.Items) {
		t.Fatal("the same documents in another order gave another change set")
	}
}

func TestUnchangedDocumentsImportedTwice_AddNothing(t *testing.T) {
	// The same bytes twice in one batch: the second adds no item and no source.
	cs := build(t, workspace.Profile{}, doc("cv.md", wire.IntakeKindCV, cvOne), doc("copy-of-cv.md", wire.IntakeKindCV, cvOne))
	docs, _ := Prepare([]wire.IntakeDocument{doc("cv.md", wire.IntakeKindCV, cvOne), doc("copy-of-cv.md", wire.IntakeKindCV, cvOne)})
	if !docs[1].Duplicate || docs[0].Duplicate {
		t.Fatalf("duplicate flags %v %v", docs[0].Duplicate, docs[1].Duplicate)
	}
	single := build(t, workspace.Profile{}, doc("cv.md", wire.IntakeKindCV, cvOne))
	if len(cs.Items) != len(single.Items) {
		t.Fatalf("%d items against %d for one copy", len(cs.Items), len(single.Items))
	}
	for _, it := range cs.Items {
		for _, s := range it.Sources {
			if s.Label == "copy-of-cv.md" {
				t.Fatalf("a repeated hash was counted as a second source: %+v", it)
			}
		}
	}
	// A profile that already holds every accepted fact: all matches, no addition to select.
	p := workspace.Profile{}
	p.History.Employment = []workspace.Employment{{Employer: "Example Systems", Title: "Platform Engineer", Start: "2019-03", End: "present"}}
	p.History.Education = []workspace.Education{approvedDegree}
	p.Skills = []workspace.Skill{{Name: "Python"}, {Name: "SQL"}}
	again := build(t, p, doc("cv.md", wire.IntakeKindCV, cvOne))
	for _, it := range again.Items {
		if it.Type != wire.IntakeMatch {
			t.Fatalf("an unchanged document proposed %s %q", it.Type, it.Summary)
		}
	}
	if len(again.Items) != 4 {
		t.Fatalf("want 4 matches (job, study, two skills), got %d", len(again.Items))
	}
	if _, err := again.Select([]wire.IntakeSelection{{ID: again.Items[0].ID}}); err == nil {
		t.Fatal("a match was accepted as a change")
	}
}

func TestSecondCVVersionWithTheSameJobAddsNoDuplicate(t *testing.T) {
	v2 := strings.Replace(cvOne, "Built the deployment pipeline.", "Built and ran the deployment pipeline.", 1)
	cs := build(t, workspace.Profile{}, doc("cv-2024.md", wire.IntakeKindCV, cvOne), doc("cv-2025.md", wire.IntakeKindCV, v2))
	jobs := byType(cs, wire.IntakeAddition, kindEmployment)
	if len(jobs) != 1 || len(jobs[0].Sources) != 2 {
		t.Fatalf("one job from two versions, with both sources: %+v", jobs)
	}
}

const courseOutline = `# Web Services, week by week

- Week 1: HTTP basics
- Week 4: Building services with Django and the Django REST framework
`

func TestCourseMentionsAFramework_OnlyAnUnconfirmedSuggestion(t *testing.T) {
	p := workspace.Profile{Skills: []workspace.Skill{{Name: "Python"}}}
	cs := build(t, p, doc("outline.md", wire.IntakeKindCourse, courseOutline))
	sig := byType(cs, wire.IntakeSignal, kindSkill)
	if len(sig) != 1 || sig[0].Entry["name"] != "Django" || sig[0].Sources[0].Origin != wire.IntakeInferred || sig[0].Question == "" {
		t.Fatalf("want one inferred Django suggestion with a question: %+v", cs.Items)
	}
	for _, it := range cs.Items {
		if it.Type == wire.IntakeAddition {
			t.Fatalf("a course produced an addition: %+v", it)
		}
	}
	if _, err := cs.Select([]wire.IntakeSelection{{ID: sig[0].ID}}); err == nil || !strings.Contains(err.Error(), "confirm") {
		t.Fatalf("a suggestion was applied without confirmation: %v", err)
	}
	got, err := cs.Select([]wire.IntakeSelection{{ID: sig[0].ID, Confirm: true}})
	if err != nil || len(got.Ops) != 1 || got.Ops[0].Path != "skills" || got.Ops[0].Value != `{"name": "Django"}` {
		t.Fatalf("confirmed suggestion: %+v %v", got, err)
	}
	// Selecting nothing changes nothing, whatever the preview showed.
	none, err := cs.Select(nil)
	if err != nil || len(none.Ops) != 0 {
		t.Fatalf("%+v %v", none, err)
	}
}

func TestRepositoryReadmeAndReferenceAreSignalsToo(t *testing.T) {
	readme := "# tool\n\n## Built with\n\n- Rust, Tokio, SQLite\n"
	ref := "To whom it may concern: Alex built our services with Kubernetes and Terraform.\n"
	cs := build(t, workspace.Profile{}, doc("README.md", wire.IntakeKindRepository, readme), doc("letter.txt", wire.IntakeKindReference, ref))
	names := map[string]bool{}
	for _, it := range cs.Items {
		if it.Type != wire.IntakeSignal {
			t.Fatalf("a %s produced %s", it.Sources[0].Label, it.Type)
		}
		names[it.Entry["name"]] = true
	}
	for _, want := range []string{"Rust", "Tokio", "SQLite", "Kubernetes", "Terraform"} {
		if !names[want] {
			t.Errorf("missing suggestion %s in %v", want, names)
		}
	}
}

func TestSignalForASkillTheProfileHasOrACVStatesIsNotSuggested(t *testing.T) {
	cs := build(t, workspace.Profile{Skills: []workspace.Skill{{Name: "django"}}}, doc("cv.md", wire.IntakeKindCV, cvOne+"- Frameworks: Flask\n"), doc("o.md", wire.IntakeKindCourse, courseOutline+"- Flask labs\n"))
	if sig := byType(cs, wire.IntakeSignal, kindSkill); len(sig) != 0 {
		t.Fatalf("suggested what is already approved or stated: %+v", sig)
	}
	if len(byType(cs, wire.IntakeAddition, kindSkill)) != 3 {
		t.Fatalf("want Python, SQL, Flask as additions: %+v", cs.Items)
	}
}

func TestBareYearNeedsAMonthBeforeItIsAccepted(t *testing.T) {
	cv := "## Experience\n\n### Developer, Harbour Software (2015 to 2017)\n"
	cs := build(t, workspace.Profile{}, doc("cv.md", wire.IntakeKindCV, cv))
	adds := byType(cs, wire.IntakeAddition, kindEmployment)
	if len(adds) != 1 || len(adds[0].NeedsMonth) != 2 {
		t.Fatalf("%+v", cs.Items)
	}
	id := adds[0].ID
	if _, err := cs.Select([]wire.IntakeSelection{{ID: id}}); err == nil || !strings.Contains(err.Error(), "months.start") {
		t.Fatalf("accepted without months: %v", err)
	}
	if _, err := cs.Select([]wire.IntakeSelection{{ID: id, Months: map[string]string{"start": "2016-01", "end": "2017-12"}}}); err == nil {
		t.Fatal("a month from another year was accepted")
	}
	got, err := cs.Select([]wire.IntakeSelection{{ID: id, Months: map[string]string{"start": "2015-02", "end": "2017-12"}}})
	if err != nil || got.Ops[0].Value != `{"employer": "Harbour Software", "title": "Developer", "start": "2015-02", "end": "2017-12"}` {
		t.Fatalf("%+v %v", got, err)
	}
}

func TestEmployerConflictWhenTitleAndDatesAgree(t *testing.T) {
	p := workspace.Profile{}
	p.History.Employment = []workspace.Employment{{Employer: "Example Systems", Title: "Platform Engineer", Start: "2019-03", End: "2022-01"}}
	cv := "## Experience\n\n### Platform Engineer, Example Systems Group (2019-03 to 2022-01)\n"
	cs := build(t, p, doc("cv.md", wire.IntakeKindCV, cv))
	c := byType(cs, wire.IntakeConflict, kindEmployment)
	if len(c) != 1 || c[0].Fields[0].Field != "employer" || c[0].Fields[0].Profile != "Example Systems" {
		t.Fatalf("%+v", cs.Items)
	}
}

func TestPrepareLimits(t *testing.T) {
	many := make([]wire.IntakeDocument, MaxDocuments+1)
	for i := range many {
		many[i] = doc("d.md", wire.IntakeKindCV, strings.Repeat("x", i+1))
	}
	if _, err := Prepare(many); err == nil || !strings.Contains(err.Error(), "at most 20") {
		t.Fatalf("21 documents: %v", err)
	}
	if _, err := Prepare(many[:MaxDocuments]); err != nil {
		t.Fatalf("20 documents: %v", err)
	}
	bad := []wire.IntakeDocument{doc("cv.pdf", wire.IntakeKindCV, "x"), doc("a.md", "diary", "x"), doc("b.md", wire.IntakeKindCV, " "), doc("c.md", wire.IntakeKindCV, string([]byte{0xff, 0xfe}))}
	_, err := Prepare(bad)
	var rej Rejection
	if !errors.As(err, &rej) || len(rej) != 4 {
		t.Fatalf("want 4 rejections, got %v", err)
	}
	if _, err := Prepare(nil); err == nil {
		t.Fatal("no documents")
	}
}
