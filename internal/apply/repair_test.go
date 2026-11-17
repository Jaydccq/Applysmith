package apply

import (
	"context"
	"strings"
	"testing"
	"time"

	"applysmith/internal/browser"
	"applysmith/internal/engine"
	"applysmith/internal/gates"
)

func refusalPage(lines ...string) *PageState {
	f := formState()
	f.Validation = lines
	return f
}

// repairRig is a submit rig whose page holds the rows this run wrote and whose
// Submit control answers from a script of results, one per press.
func repairRig(t *testing.T, results ...*PageState) (*senv, *int) {
	t.Helper()
	x := newSubmit(t, nil)
	x.s.pages = []*pageState{{report: PageReport{URL: formState().URL}, runID: "er1", rows: []engine.Row{
		{Selector: "#loc", Label: "Location", Outcome: "filled", Value: "Austin, Texas", Kind: "text"},
		{Selector: "#fn", Label: "First Name", Outcome: "filled", Value: "Alex", Kind: "text"},
		{Selector: "#ok", Label: "Years of experience", Outcome: "filled", Value: "4", Kind: "text"},
	}}}
	presses := 0
	x.tab.onClick = func(tb *scriptTab, c Control) {
		if strings.Join(c.Labels, "") != "Submit application" {
			return
		}
		res := results[min(presses, len(results)-1)]
		presses++
		if res == nil {
			return
		}
		if res == thanksMarker {
			tb.watch.add(post(200, `{"ok":true}`))
			tb.set(thanks())
			return
		}
		tb.set(res)
	}
	return x, &presses
}

var thanksMarker = &PageState{URL: "thanks"}

func TestApply_Submit_RepairRoundOncePerRun(t *testing.T) {
	// Ashby refusal naming a written field: the field is re-written, re-read,
	// and Submit is pressed once more on the same claim.
	x, presses := repairRig(t, refusalPage("Location: Missing entry for required field"), thanksMarker)
	x.page.rows = nil
	if st := x.submit(); st != nil {
		t.Fatalf("stop %+v", st)
	}
	if *presses != 2 || x.tab.clicksOf("Submit application") != 2 {
		t.Fatalf("%d presses, %d clicks: one repair round presses once more", *presses, x.tab.clicksOf("Submit application"))
	}
	if v, ok := answeredValue(x, "#loc"); !ok || v != "Austin, Texas" {
		t.Fatalf("answers %+v: the named field is re-written", x.page.answers)
	}
	if _, ok := answeredValue(x, "#ok"); ok {
		t.Fatal("a field the refusal does not name was re-written")
	}
	if x.e.Run.Outcome.Kind != OutcomeSubmitted || !x.e.Run.Outcome.Confirmed {
		t.Fatalf("outcome %+v", x.e.Run.Outcome)
	}
	line := stageOf(x.e.Run, StageSubmit).Detail
	if !strings.Contains(line, "repair round: fields=Location result=landed") || x.e.Run.Presses != 2 {
		t.Fatalf("submit line %q presses %d", line, x.e.Run.Presses)
	}
	// One claim, however many presses: the ledger holds one row and one claim
	// decision, and both presses were audited as control presses.
	if n := x.g.count(`SELECT count(*) FROM submissions`); n != 1 {
		t.Fatalf("%d ledger rows", n)
	}
	if n := x.g.audits("press_claim"); n != 1 {
		t.Fatalf("%d press claims", n)
	}
	if n := x.g.audits("control_press"); n != 2 {
		t.Fatalf("%d control presses", n)
	}
}

func TestApply_Submit_SecondRefusalStopsNeedsUserWithAtMostOneExtraPress(t *testing.T) {
	x, presses := repairRig(t, refusalPage("Location: Missing entry for required field"), refusalPage("Location: Missing entry for required field"), thanksMarker)
	st := x.submit()
	if st == nil || st.class != ClassDefect || st.board != boardNeedsUser || !strings.Contains(st.reason, "repair round: fields=Location result=refused again") {
		t.Fatalf("stop %+v", st)
	}
	if *presses != 2 || x.e.Run.Presses != 2 {
		t.Fatalf("%d presses (run %d): never a third", *presses, x.e.Run.Presses)
	}
	var state string
	if err := x.g.st.Read().QueryRow(`SELECT state FROM submissions`).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if state != "failed" || x.g.count(`SELECT count(*) FROM submissions`) != 1 {
		t.Fatalf("ledger state %q: a proven refusal settles the claim as failed", state)
	}
	// The guard holds even when a caller asks again.
	if again := x.g.r.pressSubmitAgain(context.Background(), x.e, x.s, gates.SubmitAuthorization(gates.SubmitAuthInput{Caller: gates.CallerCLI, Tool: ToolAutofill, Submit: true, ExplicitRequest: true})); again == nil || x.tab.clicksOf("Submit application") != 2 {
		t.Fatalf("a third press was made: %+v", again)
	}
}

func TestApply_Submit_RepairNeverOnNonValidationRefusal(t *testing.T) {
	t.Run("application service rejection", func(t *testing.T) {
		x, presses := repairRig(t, refusalPage("Location: Missing entry for required field"))
		x.tab.onClick = func(tb *scriptTab, c Control) {
			*presses++
			tb.watch.add(post(422, "bad"))
			tb.set(refusalPage("Location: Missing entry for required field"))
		}
		if st := x.submit(); st == nil || *presses != 1 {
			t.Fatalf("stop %+v presses %d", st, *presses)
		}
	})
	t.Run("challenge", func(t *testing.T) {
		ch := formState()
		ch.Captcha = []CaptchaW{{Kind: "recaptcha_v2", Visible: true, W: 304, H: 78}}
		ch.Resp = []RespField{{Name: "g-recaptcha-response", Empty: true}}
		x, presses := repairRig(t, ch)
		if st := x.submit(); st == nil || *presses != 1 {
			t.Fatalf("stop %+v presses %d: a challenge is not repaired", st, *presses)
		}
	})
	t.Run("form that merely looks unchanged", func(t *testing.T) {
		x, presses := repairRig(t, formState())
		x.submit()
		if *presses != 1 {
			t.Fatalf("%d presses", *presses)
		}
	})
	t.Run("field the run never wrote", func(t *testing.T) {
		x, presses := repairRig(t, refusalPage("Notice period: Missing entry for required field"), thanksMarker)
		st := x.submit()
		if st == nil || *presses != 1 || !strings.Contains(st.reason, "nothing the run wrote is named") || st.board == boardNeedsUser {
			t.Fatalf("stop %+v presses %d", st, *presses)
		}
	})
	t.Run("identity row", func(t *testing.T) {
		x, presses := repairRig(t, refusalPage("First Name is required"), thanksMarker)
		x.submit()
		if *presses != 1 || len(x.page.answers) != 0 {
			t.Fatalf("presses %d answers %+v: an identity row is never re-written", *presses, x.page.answers)
		}
	})
}

// Node's refusal-repair test: Ashby answers a refused Submit with HTTP 200 and
// the form again, naming "Missing entry for required field: ..." in
// errorMessages while the page shows no validation words. A named field this
// run wrote is repaired and Submit is pressed once more; the saved form's
// Yes/No repair runs first and, when it leaves no gap, is reason enough.
func ashbyBounceOf(field string) string {
	return `{"data":{"submitApplicationFormAction":{"__typename":"SingleFormSubmitResult","messages":null,"applicationFormResult":{"__typename":"FormRender","errorMessages":["Missing entry for required field: ` + field + `"],"formErrors":[{"__typename":"FormError","message":"Missing entry for required field","fieldEntryId":"a_b"}],"sections":[]}}}}`
}

func TestApply_Submit_AshbyBounceErrorMessagesDriveOneRepairRound(t *testing.T) {
	const op = "https://jobs.ashbyhq.com/api/non-user-graphql?op=ApiSubmitSingleApplicationFormAction"
	rig := func(t *testing.T, field string, left []string) (*senv, *int) {
		x, presses := repairRig(t)
		x.e.Run.ATS = ATSAshby
		x.page.gaps, x.page.repaired = []string{field}, left
		x.tab.onClick = func(tb *scriptTab, c Control) {
			*presses++
			if *presses == 1 {
				tb.watch.add(browser.Response{Method: "POST", URL: op, Status: 200, Body: ashbyBounceOf(field)})
				tb.set(formState())
				return
			}
			tb.watch.add(browser.Response{Method: "POST", URL: op, Status: 200, Body: `{"data":{"submitApplicationFormAction":{"applicationFormResult":{"__typename":"FormSubmitSuccess"}}}}`})
			tb.set(thanks())
		}
		return x, presses
	}
	t.Run("a named field the run wrote", func(t *testing.T) {
		x, presses := rig(t, "Location", []string{"Location"})
		if st := x.submit(); st != nil {
			t.Fatalf("stop %+v", st)
		}
		if *presses != 2 || x.page.repairs != 1 {
			t.Fatalf("%d presses, %d saved-form repairs", *presses, x.page.repairs)
		}
		if strings.Contains(x.e.Run.Repaired, "saved form") {
			t.Fatalf("repaired %q: the named field's rewrite pressed again", x.e.Run.Repaired)
		}
		if v, ok := answeredValue(x, "#loc"); !ok || v != "Austin, Texas" {
			t.Fatalf("answers %+v: the named field is re-written", x.page.answers)
		}
	})
	t.Run("a lost Yes/No the saved-form repair restores", func(t *testing.T) {
		x, presses := rig(t, "Visa Support", nil)
		if st := x.submit(); st != nil || *presses != 2 || !strings.Contains(x.e.Run.Repaired, "saved form") {
			t.Fatalf("stop %+v presses %d repaired %q", st, *presses, x.e.Run.Repaired)
		}
	})
	t.Run("a saved copy never read is no claim", func(t *testing.T) {
		x, presses := rig(t, "Visa Support", nil)
		x.page.gaps = nil
		if st := x.submit(); st == nil || *presses != 1 {
			t.Fatalf("stop %+v presses %d", st, *presses)
		}
	})
	t.Run("nothing repaired presses nothing", func(t *testing.T) {
		x, presses := rig(t, "Visa Support", []string{"Visa Support"})
		if st := x.submit(); st == nil || *presses != 1 {
			t.Fatalf("stop %+v presses %d", st, *presses)
		}
	})
}

// Node's check.ashby-unanswered test: a required question Ashby's saved copy
// lacks is a required leftover on the row's own selector, even when the row
// read as filled; a question with no row is named by its title.
func TestApply_Check_AshbySavedFormGapsAreRequiredLeftovers(t *testing.T) {
	x := newSubmit(t, func(d *Deps) { d.Page.Check = CheckTiming{Reread: time.Millisecond, Settle: time.Millisecond} })
	x.e.Run.ATS = ATSAshby
	x.page.gaps = []string{"Graduation Date", "Start Date"}
	grad := engine.Row{Selector: "#grad", Label: "Graduation Date Pick date...", Required: true, Outcome: "filled", Value: "May 2026", Kind: "text"}
	x.page.rows = func() []engine.InspectRow {
		return []engine.InspectRow{{Selector: "#grad", Label: grad.Label, Required: true, Kind: "text", Value: "May 2026"}}
	}
	ps := &pageState{}
	x.s.check(context.Background(), doneResult(grad), ps)
	if len(ps.leftovers) != 2 || ps.leftovers[0].Selector != "#grad" || ps.leftovers[0].Reason != ReasonSavedFormGap || ps.leftovers[1].Label != "Start Date" {
		t.Fatalf("leftovers %+v", ps.leftovers)
	}
	if v := gates.SubmitReadiness(gates.ReadinessInput{Pages: []gates.PageReport{ps.gatesReport(1)}, Final: true}); v.Allowed() {
		t.Fatal("the verdict allowed Submit with a gap in Ashby's saved form")
	}
	x.e.Run.ATS = ATSGreenhouse
	other := &pageState{}
	x.s.savedFormGaps(context.Background(), other)
	if len(other.leftovers) != 0 {
		t.Fatal("a saved-form gap was read off Ashby")
	}
}

// A run reads its page through the fill log, as drivePages, resumePages and
// Operate set it up: the saved copy is still read behind that wrapper.
func TestApply_Check_AshbySavedFormIsReadThroughTheFillLog(t *testing.T) {
	x := newSubmit(t, nil)
	x.e.Run.ATS = ATSAshby
	x.page.gaps = []string{"Start Date"}
	x.s.page = &loggedFiller{Filler: x.page, log: &fillLog{}}
	ps := &pageState{}
	x.s.savedFormGaps(context.Background(), ps)
	if len(ps.leftovers) != 1 || ps.leftovers[0].Label != "Start Date" || ps.leftovers[0].Reason != ReasonSavedFormGap {
		t.Fatalf("leftovers %+v", ps.leftovers)
	}
}
