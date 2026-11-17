package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"applysmith/internal/documents"
	"applysmith/internal/interview"
)

const interviewUsage = `usage:
  applysmith interview-prep --candidacy <id> --stage <stage-id> [--type <stage-type>] [--json] [--workspace <dir>]

builds the next prep pack revision for one interview stage of an application and prints it.
--type records the stage first (as confirmed by you) when it does not exist yet; the types are
phone_screen, technical, system_design, behavioral, case, onsite, final and other.
a running server holds the workspace; the command needs it stopped
`

func (e *env) interviewPrep(ctx context.Context, args []string) int {
	fs := e.flagsWithUsage("interview-prep", interviewUsage)
	ws := fs.String("workspace", "", "workspace directory")
	cand := fs.Int("candidacy", 0, "tracker row id")
	stage := fs.String("stage", "", "stage id")
	typ := fs.String("type", "", "record the stage with this type when it does not exist")
	asJSON := fs.Bool("json", false, "print JSON")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	var problems []string
	if *cand < 1 {
		problems = append(problems, "missing --candidacy <id>")
	}
	if *stage == "" {
		problems = append(problems, "missing --stage <stage-id>")
	}
	if len(problems) > 0 || fs.NArg() > 0 {
		if fs.NArg() > 0 {
			problems = append(problems, fmt.Sprintf("unexpected argument %q", fs.Arg(0)))
		}
		return e.refuseUsage("interview-prep", interviewUsage, problems...)
	}
	const cmd = "interview-prep"
	return e.withDocuments(ctx, cmd, *ws, func(x docEnv) int {
		iv := x.svc.Interview()
		if _, err := iv.Stage(ctx, int64(*cand), *stage); errors.Is(err, interview.ErrNotFound) && *typ != "" {
			if _, err := iv.RecordStage(ctx, int64(*cand), interview.StageInput{ID: *stage, Type: *typ, UserConfirmed: true}); err != nil {
				return e.reportInterviewError(cmd, err)
			}
		}
		h, err := x.svc.Submit(ctx, documents.Claim{Principal: "cli", Key: fmt.Sprintf("cli-%d-%d", os.Getpid(), time.Now().UnixNano())},
			documents.Request{Action: documents.ActionInterview, CandidacyID: int64(*cand), Stage: *stage})
		if err != nil {
			if errors.Is(err, documents.ErrNotFound) {
				if strings.Contains(err.Error(), "stage") {
					fmt.Fprintf(e.stderr, "applysmith %s: tracker row %d has no stage %q; record the stage first, or pass --type\n", cmd, *cand, *stage)
				} else {
					fmt.Fprintf(e.stderr, "applysmith %s: no tracker row %d; `applysmith tracker list` shows the row ids\n", cmd, *cand)
				}
				return 1
			}
			return e.reportDocumentError(cmd, err)
		}
		res, err := x.svc.Wait(ctx, h.RunID)
		if err != nil {
			return e.fail(cmd, err)
		}
		if !res.Published || res.Pack == nil {
			fmt.Fprintln(e.stdout, runFailure(res.Reason))
			return 1
		}
		pack, err := iv.Pack(ctx, res.Pack.Candidacy, res.Pack.Stage, res.Pack.Revision)
		if err != nil {
			return e.fail(cmd, err)
		}
		if *asJSON {
			return e.printJSON(pack)
		}
		e.printPack(pack)
		return 0
	})
}

func (e *env) reportInterviewError(cmd string, err error) int {
	if errors.Is(err, interview.ErrInvalid) {
		fmt.Fprintf(e.stderr, "applysmith %s: %v\n", cmd, err)
		return 2
	}
	return e.fail(cmd, err)
}

func (e *env) printPack(p interview.Pack) {
	w := e.stdout
	fmt.Fprintf(w, "prep pack for stage %s, revision %d (%s wording)\n", p.Stage, p.Revision, p.Generation)
	section := func(title string, lines []string) {
		if len(lines) == 0 {
			return
		}
		fmt.Fprintf(w, "\n%s\n", title)
		for _, l := range lines {
			fmt.Fprintf(w, "  - %s\n", l)
		}
	}
	if p.RoleBrief != "" {
		fmt.Fprintf(w, "\nrole: %s\n", p.RoleBrief)
	}
	if p.CompanyBrief != "" {
		fmt.Fprintf(w, "company: %s\n", p.CompanyBrief)
	}
	var hooks, qs []string
	for _, h := range p.Hooks {
		hooks = append(hooks, h.Text)
	}
	for _, q := range p.Questions {
		line := fmt.Sprintf("[%s] %s", q.Category, q.Prompt)
		if len(q.StoryIDs) > 0 {
			line += fmt.Sprintf(" (stories: %v)", q.StoryIDs)
		}
		qs = append(qs, line)
	}
	section("company hooks", hooks)
	section("stay consistent with your submitted résumé", p.Claims)
	section("likely questions", qs)
	section("prior feedback", p.PriorFeedback)
	section("gaps", interview.Texts(p.Gaps))
	section("next actions", interview.Texts(p.NextActions))
	for _, s := range p.Sources {
		fmt.Fprintf(w, "source (%s, %s): %s\n", s.Kind, s.State, s.Label)
	}
	for _, warn := range p.Warnings {
		fmt.Fprintf(w, "note: %s\n", warn)
	}
}
