package onboarding

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"applysmith/internal/answerbank/bind"
	"applysmith/internal/api/wire"
	"applysmith/internal/workspace"
)

const intakeCV = `# Alex Example

## Experience

### Platform Engineer, Example Systems (2019-03 to present)

- Built the deployment pipeline.

## Education

### BSc Computer Science, Northern College (2011-09 to 2015-06)

## Skills

- Languages: Python, SQL
`

const intakeRecord = `# Education record

## Education

### BSc Computer Science, Northern College (2011-09 to 2015-05)
`

const intakeCourse = "# Web Services\n\n- Week 4: Building services with Django\n"

func intakeDocs(parts ...string) []wire.IntakeDocument {
	var out []wire.IntakeDocument
	for i := 0; i+2 < len(parts); i += 3 {
		out = append(out, wire.IntakeDocument{Name: parts[i], Kind: parts[i+1], Text: parts[i+2]})
	}
	return out
}

func approveDegree(t *testing.T, env *Env) {
	t.Helper()
	mustApply(t, env, "cv", `{"education":[{"school":"Northern College","degree":"BSc Computer Science","start":"2011-09","end":"2015-06"}]}`)
}

func profileBytes(t *testing.T, env *Env) (workspace.Profile, string) {
	t.Helper()
	p, data, bad := profileData(env)
	if bad != nil {
		t.Fatalf("profile: %+v", bad)
	}
	return p, workspace.Revision(data)
}

func preview(t *testing.T, env *Env, docs []wire.IntakeDocument) wire.IntakePreview {
	t.Helper()
	pv, err := IntakePreview(bg(), env, docs)
	if err != nil {
		t.Fatal(err)
	}
	return pv
}

func itemsOf(pv wire.IntakePreview, typ string) []wire.IntakeItem {
	var out []wire.IntakeItem
	for _, it := range pv.Items {
		if it.Type == typ {
			out = append(out, it)
		}
	}
	return out
}

func TestIntake_ConflictingGraduationDatesRequireASelection(t *testing.T) {
	env := setupEnv(t)
	approveDegree(t, env)
	docs := intakeDocs("cv.md", wire.IntakeKindCV, intakeCV, "registrar.md", wire.IntakeKindEducationRecord, intakeRecord)
	pv := preview(t, env, docs)
	conflicts := itemsOf(pv, wire.IntakeConflict)
	if len(conflicts) != 1 || conflicts[0].Fields[0].Field != "end" || len(conflicts[0].Fields[0].Options) != 2 {
		t.Fatalf("want one graduation-date conflict showing both sources: %+v", pv.Items)
	}
	_, before := profileBytes(t, env)

	// No pick: refused, and the approved date stays.
	_, err := IntakeAccept(bg(), env, wire.IntakeAcceptRequest{Revision: pv.Revision, Documents: docs, Selections: []wire.IntakeSelection{{ID: conflicts[0].ID}}})
	var fe FieldErrors
	if !errors.As(err, &fe) || !strings.Contains(fe.Error(), "choices.end") {
		t.Fatalf("a conflict was accepted without a pick: %v", err)
	}
	if _, rev := profileBytes(t, env); rev != before {
		t.Fatal("a refused acceptance changed profile.yml")
	}

	// Picking the record's date is the person's decision and is written.
	res, err := IntakeAccept(bg(), env, wire.IntakeAcceptRequest{Revision: pv.Revision, Documents: docs,
		Selections: []wire.IntakeSelection{{ID: conflicts[0].ID, Choices: map[string]string{"end": "2015-05"}}}})
	if err != nil || !res.Wrote || res.Revision == before {
		t.Fatalf("%+v %v", res, err)
	}
	if p, _ := profileBytes(t, env); len(p.History.Education) != 1 || p.History.Education[0].End != "2015-05" || p.History.Education[0].Start != "2011-09" {
		t.Fatalf("education = %+v", p.History.Education)
	}
}

func TestIntake_KeepingTheApprovedDateWritesNothing(t *testing.T) {
	env := setupEnv(t)
	approveDegree(t, env)
	docs := intakeDocs("cv.md", wire.IntakeKindCV, intakeCV, "registrar.md", wire.IntakeKindEducationRecord, intakeRecord)
	pv := preview(t, env, docs)
	c := itemsOf(pv, wire.IntakeConflict)[0]
	res, err := IntakeAccept(bg(), env, wire.IntakeAcceptRequest{Revision: pv.Revision, Documents: docs,
		Selections: []wire.IntakeSelection{{ID: c.ID, Choices: map[string]string{"end": "keep"}}}})
	if err != nil || res.Wrote || res.Revision != pv.Revision {
		t.Fatalf("%+v %v", res, err)
	}
}

func TestIntake_AStaleRevisionIsRefusedNotMerged(t *testing.T) {
	env := setupEnv(t)
	docs := intakeDocs("cv.md", wire.IntakeKindCV, intakeCV)
	pv := preview(t, env, docs)
	adds := itemsOf(pv, wire.IntakeAddition)
	if len(adds) == 0 {
		t.Fatalf("%+v", pv.Items)
	}
	// The profile moves on after the preview.
	mustApply(t, env, "cv", `{"skills":[{"name":"Rust"}]}`)
	_, now := profileBytes(t, env)
	_, err := IntakeAccept(bg(), env, wire.IntakeAcceptRequest{Revision: pv.Revision, Documents: docs, Selections: []wire.IntakeSelection{{ID: adds[0].ID}}})
	var stale *StaleError
	if !errors.As(err, &stale) {
		t.Fatalf("want a stale refusal, got %v", err)
	}
	if _, rev := profileBytes(t, env); rev != now {
		t.Fatal("a stale acceptance changed profile.yml")
	}
}

func TestIntake_UnchangedDocumentsImportedTwiceAddNoFactsAndNoWrite(t *testing.T) {
	env := setupEnv(t)
	docs := intakeDocs("cv.md", wire.IntakeKindCV, intakeCV)
	pv := preview(t, env, docs)
	var sels []wire.IntakeSelection
	for _, it := range itemsOf(pv, wire.IntakeAddition) {
		sels = append(sels, wire.IntakeSelection{ID: it.ID})
	}
	if len(sels) != 4 {
		t.Fatalf("want a job, a study and two skills, got %+v", pv.Items)
	}
	first, err := IntakeAccept(bg(), env, wire.IntakeAcceptRequest{Revision: pv.Revision, Documents: docs, Selections: sels})
	if err != nil || !first.Wrote {
		t.Fatalf("%+v %v", first, err)
	}
	p1, rev1 := profileBytes(t, env)

	again := preview(t, env, docs)
	if len(itemsOf(again, wire.IntakeAddition)) != 0 || len(itemsOf(again, wire.IntakeMatch)) != 4 || again.Revision != first.Revision {
		t.Fatalf("a second import proposed new facts: %+v", again.Items)
	}
	// The same acceptance replayed against the new revision selects nothing real.
	_, err = IntakeAccept(bg(), env, wire.IntakeAcceptRequest{Revision: again.Revision, Documents: docs, Selections: sels})
	if err == nil {
		t.Fatal("the earlier selections were accepted a second time")
	}
	res, err := IntakeAccept(bg(), env, wire.IntakeAcceptRequest{Revision: again.Revision, Documents: docs})
	if err != nil || res.Wrote {
		t.Fatalf("%+v %v", res, err)
	}
	p2, rev2 := profileBytes(t, env)
	if rev1 != rev2 || len(p1.History.Employment) != 1 || len(p2.History.Employment) != 1 || len(p2.Skills) != 2 {
		t.Fatalf("duplicates or a write: %d jobs, %d skills", len(p2.History.Employment), len(p2.Skills))
	}
}

func TestIntake_ACourseOutlineIsOnlyAnUnconfirmedSuggestion(t *testing.T) {
	env := setupEnv(t)
	cvBefore, _ := os.ReadFile(env.Workspace.Path(workspace.FileCV))
	docs := intakeDocs("outline.md", wire.IntakeKindCourse, intakeCourse, "cv.md", wire.IntakeKindCV, intakeCV)
	pv := preview(t, env, docs)
	sig := itemsOf(pv, wire.IntakeSignal)
	if len(sig) != 1 || sig[0].Entry["name"] != "Django" || sig[0].Sources[0].Origin != wire.IntakeInferred {
		t.Fatalf("%+v", pv.Items)
	}
	// Every other item is accepted; the suggestion is not.
	var sels []wire.IntakeSelection
	for _, it := range itemsOf(pv, wire.IntakeAddition) {
		sels = append(sels, wire.IntakeSelection{ID: it.ID})
	}
	if _, err := IntakeAccept(bg(), env, wire.IntakeAcceptRequest{Revision: pv.Revision, Documents: docs, Selections: sels}); err != nil {
		t.Fatal(err)
	}
	p, _ := profileBytes(t, env)
	for _, s := range p.Skills {
		if s.Name == "Django" {
			t.Fatal("an unconfirmed suggestion became a profile skill")
		}
	}
	// What autofill reads, and what the draft chain reads, carries no trace of it.
	for _, s := range bind.FactsFromProfile(p).Skills {
		if s.Name == "Django" {
			t.Fatal("an unconfirmed suggestion reached the answer bank's facts")
		}
	}
	if cvAfter, _ := os.ReadFile(env.Workspace.Path(workspace.FileCV)); string(cvAfter) != string(cvBefore) {
		t.Fatal("the import rewrote cv.md")
	}

	// Selecting it without the confirmation is refused; with it, it is the person's own statement.
	again := preview(t, env, docs)
	id := itemsOf(again, wire.IntakeSignal)[0].ID
	if _, err := IntakeAccept(bg(), env, wire.IntakeAcceptRequest{Revision: again.Revision, Documents: docs, Selections: []wire.IntakeSelection{{ID: id}}}); err == nil {
		t.Fatal("a suggestion was applied without confirmation")
	}
	if _, err := IntakeAccept(bg(), env, wire.IntakeAcceptRequest{Revision: again.Revision, Documents: docs, Selections: []wire.IntakeSelection{{ID: id, Confirm: true}}}); err != nil {
		t.Fatal(err)
	}
	p, _ = profileBytes(t, env)
	found := false
	for _, s := range p.Skills {
		found = found || (s.Name == "Django" && s.Years == nil && s.Level == "")
	}
	if !found {
		t.Fatalf("skills = %+v", p.Skills)
	}
}

func TestIntake_SingleCVAndManualSetupStayTheMinimalPath(t *testing.T) {
	// The CV step takes no documents, and the intake adds no step.
	env := setupEnv(t)
	body := `{"employment":[{"employer":"Example Systems","title":"Engineer","start":"2019-03","end":"present"}],"review_complete":true}`
	if res := mustApply(t, env, "cv", body); !res.Wrote {
		t.Fatalf("%+v", res)
	}
	for _, f := range cvFields {
		if strings.Contains(f.Name, "document") || strings.Contains(f.Name, "intake") {
			t.Fatalf("the CV writer grew a document field: %s", f.Name)
		}
	}
	if len(Catalog()) != 18 {
		t.Fatalf("the catalog has %d steps", len(Catalog()))
	}
}

func TestIntake_LoadFilesNamesEachFileAndRefusesGeneratedOnes(t *testing.T) {
	env := setupEnv(t)
	dir := t.TempDir()
	cv := filepath.Join(dir, "cv.md")
	os.WriteFile(cv, []byte(intakeCV), 0o600)
	course := filepath.Join(dir, "outline.txt")
	os.WriteFile(course, []byte(intakeCourse), 0o600)

	docs, err := LoadIntakeFiles(env.Workspace, []string{cv, "course:" + course})
	if err != nil || len(docs) != 2 || docs[0].Kind != wire.IntakeKindCV || docs[1].Kind != wire.IntakeKindCourse || docs[1].Name != "outline.txt" {
		t.Fatalf("%+v %v", docs, err)
	}
	if _, err := LoadIntakeFiles(env.Workspace, []string{dir}); err == nil {
		t.Fatal("a directory was scanned")
	}
	gen := filepath.Join(env.Workspace.Root, "documents", "resume-draft.md")
	os.MkdirAll(filepath.Dir(gen), 0o700)
	os.WriteFile(gen, []byte(intakeCV), 0o600)
	if _, err := LoadIntakeFiles(env.Workspace, []string{gen}); err == nil || !strings.Contains(err.Error(), "generated") {
		t.Fatalf("a generated draft was read as evidence: %v", err)
	}
	many := make([]string, 21)
	for i := range many {
		many[i] = cv
	}
	if _, err := LoadIntakeFiles(env.Workspace, many); err == nil {
		t.Fatal("21 files")
	}
	if _, err := IntakePreview(bg(), env, []wire.IntakeDocument{{Name: "cv.pdf", Kind: wire.IntakeKindCV, Text: "x"}}); err == nil {
		t.Fatal("a PDF name was accepted")
	}
}
