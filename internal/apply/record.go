package apply

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"applysmith/internal/tracker"
)

// boardReason is the runs-board vocabulary of a stop that needs the user.
func boardReason(o Outcome) string { return o.Board }

// freeze marks the exact document versions this run attached as attached to a
// submitted application. A frozen version is never changed and is deleted only
// on an explicit request that names it.
func (r *Runner) freeze(ctx context.Context, e *Exec, s *session) {
	if s == nil || s.docs == nil || r.d.Page.Documents == nil {
		return
	}
	ids := s.docs.attachedIDs()
	if len(ids) == 0 {
		return
	}
	cand := int64(0)
	if e.Run.Report != nil {
		cand = e.Run.Report.TrackerID
	}
	if cand == 0 {
		e.Run.Warnings = append(e.Run.Warnings, "attached document versions were not frozen: the posting has no candidacy")
		return
	}
	if err := r.d.Page.Documents.Freeze(ctx, cand, ids...); err != nil {
		e.Run.Warnings = append(e.Run.Warnings, "freezing the attached versions failed: "+err.Error())
	}
}

// runRecord is the record stage: it always runs and never raises. A confirmed
// submission sets the tracker row to applied through the actor apply_chain
// (which marks it as applied by the agent); an unconfirmed one leaves the
// tracker unchanged and says so. A tracker refusal or failure never fails the
// submission, which the ledger already holds.
func (r *Runner) runRecord(e *Exec) {
	start := r.d.Now()
	run := e.Run
	status, detail := StatusOK, "run record written"
	if run.Outcome.Kind == OutcomeSubmitted {
		detail = r.trackerEffect(e)
	}
	if err := saveRun(context.WithoutCancel(e.Ctx), r.d.Store, run); err != nil {
		status, detail = StatusFailed, "the run record could not be written: "+err.Error()
		fmt.Fprintln(os.Stderr, "apply: "+detail)
	}
	r.record(e, StageRecord, status, detail, start)
}

// trackerEffect applies the tracker consequence of a submission and returns
// the stage detail. A correction re-sends an application the tracker already
// holds, so it never touches the tracker.
func (r *Runner) trackerEffect(e *Exec) string {
	run := e.Run
	if run.Correction {
		return fmt.Sprintf("correction recorded; it supersedes ledger row %d, so the tracker is unchanged", run.Supersedes)
	}
	if !run.Outcome.Confirmed {
		return "submission recorded; unconfirmed, so the tracker is unchanged"
	}
	tr := r.d.Page.Tracker
	if tr == nil {
		return "submission recorded; no tracker is configured"
	}
	ctx := context.WithoutCancel(e.Ctx)
	id := int64(0)
	if run.Report != nil {
		id = run.Report.TrackerID
	}
	if id == 0 {
		if lk, err := tr.Lookup(ctx, run.Input.URL); err == nil && lk.Found && lk.Entry != nil {
			id = lk.Entry.ID
		}
	}
	if id == 0 {
		company, role := run.Input.Company, run.Input.Role
		if run.Report != nil {
			company, role = run.Report.Company, run.Report.Role
		}
		if company == "" || role == "" {
			return "submission recorded; no tracker row was created because the company and role are not known"
		}
		ent, err := tr.Add(ctx, tracker.AddRow{Company: company, Role: role, URL: redactURL(run.Input.URL), Status: tracker.Evaluated, Source: "apply-chain"})
		if err != nil {
			return "submission recorded; the tracker row could not be created: " + err.Error()
		}
		id = ent.ID
	}
	// A submission of the single-posting tool, which the submit gate grants
	// only on the user's explicit request, takes an archived row out of the
	// archive (owner decision in apply.md); a list row never does.
	if _, err := tr.SetStatus(ctx, tracker.StatusChange{ID: id, To: tracker.Applied, Actor: tracker.ActorApplyChain, Note: "submitted by the apply chain", AppliedBy: tracker.ByAgent,
		Unarchive: run.Input.Tool == ToolAutofill}); err != nil {
		return "submission recorded; the tracker row was not changed: " + err.Error()
	}
	run.TrackerRow = id
	return fmt.Sprintf("submission recorded; tracker row %d is applied by the agent", id)
}

// snapshot writes the page's DOM and a screenshot under dir, with every
// password input's value blanked. It is best effort.
func (s *session) snapshot(ctx context.Context, label string) {
	dir := s.r.d.Page.SnapshotDir
	if dir == "" || s.tab == nil {
		return
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return
	}
	base := filepath.Join(dir, fmt.Sprintf("%s-%s", s.e.Run.ID, label))
	var html string
	const expr = `(()=>{const c=document.documentElement.cloneNode(true);c.querySelectorAll('input[type=password]').forEach(i=>{i.removeAttribute('value');i.value=''});c.querySelectorAll('input').forEach(i=>{if(/one-time|otp|verification|security.?code|passcode/i.test((i.name||'')+(i.id||'')+(i.autocomplete||''))){i.removeAttribute('value')}});return '<!doctype html>'+c.outerHTML})()`
	sctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	if err := s.tab.Eval(sctx, expr, &html); err == nil {
		if os.WriteFile(base+".html", []byte(html), 0o600) == nil && label == "stop" {
			s.e.Run.Snapshot = base + ".html"
		}
	}
	if label == "stop" && s.fl != nil {
		s.fl.write(base + ".fill-log.jsonl")
	}
	// The engine's own log says what each writer tried and saw: the top
	// document's, then each frame's the fills reached.
	if label == "stop" {
		var lines []string
		if lg, ok := s.tab.Page().(interface {
			Log(context.Context) ([]string, error)
		}); ok {
			if top, err := lg.Log(sctx); err == nil {
				lines = top
			}
		}
		if lf, ok := s.page.(*loggedFiller); ok {
			lines = append(lines, lf.frameLogs(sctx)...)
		}
		if len(lines) > 0 {
			_ = os.WriteFile(base+".engine-log.txt", []byte(strings.Join(lines, "\n")+"\n"), 0o600)
		}
	}
	if png, err := s.tab.Screenshot(sctx); err == nil {
		_ = os.WriteFile(base+".png", png, 0o600)
	}
}
