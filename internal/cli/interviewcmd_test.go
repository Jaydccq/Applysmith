package cli

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"applysmith/internal/interview"
)

func TestInterviewPrepCLI_RecordsTheStageAndPrintsThePack(t *testing.T) {
	te := docsEnv(t)
	te.addRow("Example Corp", "Backend Engineer", "https://jobs.example.com/backend-1")
	ctx := context.Background()
	o := te.run(ctx, "interview-prep", "--candidacy", "1", "--stage", "tech-1", "--type", "technical", "--workspace", te.ws)
	if o.code != 0 {
		t.Fatalf("interview-prep: %+v", o)
	}
	for _, want := range []string{"prep pack for stage tech-1, revision 1", "likely questions", "[depth]", "[debugging]", "gaps", "No job description is saved"} {
		if !strings.Contains(o.stdout, want) {
			t.Errorf("output lacks %q:\n%s", want, o.stdout)
		}
	}
	o = te.run(ctx, "interview-prep", "--candidacy", "1", "--stage", "tech-1", "--workspace", te.ws, "--json")
	var p interview.Pack
	if err := json.Unmarshal([]byte(o.stdout), &p); o.code != 0 || err != nil || p.Revision != 2 || p.Stage != "tech-1" {
		t.Fatalf("second run: %+v %v %+v", o, err, p)
	}
}

func TestInterviewPrepCLI_Refusals(t *testing.T) {
	te := docsEnv(t)
	te.addRow("Example Corp", "Backend Engineer", "https://jobs.example.com/backend-1")
	ctx := context.Background()
	for name, c := range map[string]struct {
		args []string
		want string
	}{
		"no candidacy":  {[]string{"interview-prep", "--stage", "tech-1"}, "missing --candidacy"},
		"no stage flag": {[]string{"interview-prep", "--candidacy", "1"}, "missing --stage"},
	} {
		if o := te.run(ctx, append(c.args, "--workspace", te.ws)...); o.code != 2 || !strings.Contains(o.stderr, "usage") || !strings.Contains(o.stderr, c.want) {
			t.Errorf("%s: %+v", name, o)
		}
	}
	if o := te.run(ctx, "interview-prep", "--candidacy", "1", "--stage", "missing-1", "--workspace", te.ws); o.code != 1 || !strings.Contains(o.stderr, "record the stage first") {
		t.Errorf("missing stage: %+v", o)
	}
	if o := te.run(ctx, "interview-prep", "--candidacy", "9", "--stage", "tech-1", "--type", "technical", "--workspace", te.ws); o.code == 0 {
		t.Errorf("unknown candidacy succeeded: %+v", o)
	}
	if o := te.run(ctx, "interview-prep", "--candidacy", "1", "--stage", "Bad Id", "--type", "technical", "--workspace", te.ws); o.code != 2 {
		t.Errorf("invalid stage id: %+v", o)
	}
}
