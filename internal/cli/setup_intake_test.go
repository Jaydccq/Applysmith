package cli

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"applysmith/internal/api/wire"
)

const intakeRecordText = `## Education

### BSc Computer Science, Northern College (2011-09 to 2015-05)
`

const intakeCourseText = "# Web Services\n\n- Week 4: Building services with Django\n"

func TestSetupIntakePreviewAndApplyThroughTheCommandLine(t *testing.T) {
	te := newTestEnv(t)
	te.hook = machineProbes
	ctx := context.Background()
	if out := te.run(ctx, "init", "--workspace", te.ws); out.code != 0 {
		t.Fatalf("init: %+v", out)
	}
	te.run(ctx, "setup", "apply", "cv", "--workspace", te.ws, "--file", writeFile(t, t.TempDir(), "in.json",
		`{"education":[{"school":"Northern College","degree":"BSc Computer Science","start":"2011-09","end":"2015-06"}]}`))
	dir := t.TempDir()
	cv := writeFile(t, dir, "cv.md", cvText)
	rec := writeFile(t, dir, "registrar.md", intakeRecordText)
	course := writeFile(t, dir, "outline.txt", intakeCourseText)
	profile := func() string { b, _ := os.ReadFile(filepath.Join(te.ws, "profile.yml")); return string(b) }
	before := profile()

	out := te.run(ctx, "setup", "intake", "preview", "--json", "--workspace", te.ws, cv, "education-record:"+rec, "course:"+course)
	if out.code != 0 {
		t.Fatalf("preview: %+v", out)
	}
	if profile() != before {
		t.Fatal("a preview wrote profile.yml")
	}
	var pv wire.IntakePreview
	if err := json.Unmarshal([]byte(out.stdout), &pv); err != nil {
		t.Fatal(err)
	}
	var conflict, signal wire.IntakeItem
	for _, it := range pv.Items {
		switch it.Type {
		case wire.IntakeConflict:
			conflict = it
		case wire.IntakeSignal:
			signal = it
		}
	}
	if conflict.ID == "" || signal.Entry["name"] != "Django" {
		t.Fatalf("%+v", pv.Items)
	}
	if text := te.run(ctx, "setup", "intake", "preview", "--workspace", te.ws, cv, "education-record:"+rec); !strings.Contains(text.stdout, "conflict") || !strings.Contains(text.stdout, "registrar.md") {
		t.Fatalf("the text preview does not name the conflict and its source: %+v", text)
	}

	apply := func(rev, selection string) output {
		sel := writeFile(t, t.TempDir(), "sel.json", selection)
		return te.run(ctx, "setup", "intake", "apply", "--workspace", te.ws, "--revision", rev, "--select", sel, cv, "education-record:"+rec, "course:"+course)
	}
	if o := apply(pv.Revision, `[{"id":"`+conflict.ID+`"}]`); o.code != 2 || !strings.Contains(o.stderr, "choices.end") || profile() != before {
		t.Fatalf("a conflict was applied without a pick: %+v", o)
	}
	if o := apply(pv.Revision, `[{"id":"`+signal.ID+`"}]`); o.code != 2 || !strings.Contains(o.stderr, "confirm") || profile() != before {
		t.Fatalf("a suggestion was applied without confirmation: %+v", o)
	}
	if o := apply("0000", `[]`); o.code != 2 || !strings.Contains(o.stderr, "preview the documents again") {
		t.Fatalf("a stale revision: %+v", o)
	}
	if o := apply(pv.Revision, `[{"id":"`+conflict.ID+`","choices":{"end":"2015-05"}}]`); o.code != 0 {
		t.Fatalf("apply: %+v", o)
	}
	if after := profile(); !strings.Contains(after, "2015-05") || strings.Contains(after, "Django") {
		t.Fatalf("profile after apply:\n%s", after)
	}
	if o := te.run(ctx, "setup", "intake", "apply", "--workspace", te.ws, cv); o.code != 2 {
		t.Fatalf("apply without a revision and selections: %+v", o)
	}
	if o := te.run(ctx, "setup", "intake", "preview", "--workspace", te.ws, dir); o.code != 2 {
		t.Fatalf("a directory was scanned: %+v", o)
	}
}
