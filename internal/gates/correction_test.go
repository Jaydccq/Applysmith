// Clean-room: docs/provenance.md#CR-EVAL-RUBRIC

package gates

import (
	"strings"
	"testing"
)

func TestGuard_Resubmit_Door(t *testing.T) {
	for _, c := range []struct {
		name string
		in   ResubmitInput
		v    Verdict
		code string
	}{
		{"ask agent names company", ResubmitInput{Caller: CallerAskAgent, Message: "please resubmit Example Corp", Company: "Example Corp"}, Allow, CodeOK},
		{"ask agent without the name", ResubmitInput{Caller: CallerAskAgent, Message: "please resubmit that one", Company: "Example Corp"}, Deny, CodeResubmitCompanyNotNamed},
		{"ask agent without wording", ResubmitInput{Caller: CallerAskAgent, Message: "Example Corp", Company: "Example Corp"}, Deny, CodeResubmitNotRequested},
		{"cli flag", ResubmitInput{Caller: CallerCLI, CLIFlag: true}, Allow, CodeOK},
		{"cli flag with list", ResubmitInput{Caller: CallerCLI, CLIFlag: true, List: true}, Deny, CodeResubmitWithList},
		{"cli without the flag", ResubmitInput{Caller: CallerCLI}, Deny, CodeResubmitNotRequested},
		{"dashboard", ResubmitInput{Caller: CallerDashboard, CLIFlag: true}, Deny, CodeResubmitCallerRefused},
		{"telegram", ResubmitInput{Caller: CallerTelegram, Message: "resubmit Example Corp", Company: "Example Corp"}, Deny, CodeResubmitCallerRefused},
	} {
		t.Run(c.name, func(t *testing.T) { got(t, ResubmitDoor(c.in), GateResubmit, c.v, c.code) })
	}
}

func TestGuard_Resubmit_PhraseTableOneCasePerPhrase(t *testing.T) {
	for _, p := range ResubmitPhrases {
		t.Run(p, func(t *testing.T) {
			in := ResubmitInput{Caller: CallerAskAgent, Message: "Please " + p + " Example Corp now", Company: "Example Corp"}
			got(t, ResubmitDoor(in), GateResubmit, Allow, CodeOK)
		})
	}
}

const correctionURL = "https://jobs.example.test/apply/42"

func correctionIn(msg string, rows ...CorrectionRow) CorrectionInput {
	return CorrectionInput{Caller: CallerAskAgent, SinglePosting: true, Message: msg, Submit: true, LedgerReadable: true, Rows: rows}
}

var confirmedRow = CorrectionRow{ID: "row-1", At: t0 - Day, Company: "Example Corp"}

func TestGuard_Correction_OpensOnlyWhenAllConditionsHold(t *testing.T) {
	msg := "correct the answers for Example Corp " + correctionURL
	for _, c := range []struct {
		name string
		mut  func(*CorrectionInput)
		v    Verdict
		code string
	}{
		{"all hold", func(*CorrectionInput) {}, Allow, CodeOK},
		{"not through the ask agent", func(i *CorrectionInput) { i.Caller = CallerCLI }, Deny, CodeCorrectionSingleOnly},
		{"a batch", func(i *CorrectionInput) { i.SinglePosting = false }, Deny, CodeCorrectionSingleOnly},
		{"submit false", func(i *CorrectionInput) { i.Submit = false }, Deny, CodeCorrectionNoSubmit},
		{"ledger unreadable", func(i *CorrectionInput) { i.LedgerReadable = false }, Deny, CodeCorrectionLedgerUnreadable},
		{"no ledger row", func(i *CorrectionInput) { i.Rows = nil }, Deny, CodeCorrectionNoRow},
		{"no wording", func(i *CorrectionInput) { i.Message = "Example Corp " + correctionURL }, Deny, CodeCorrectionNotRequested},
		{"a question", func(i *CorrectionInput) { i.Message = "can we correct the answers for Example Corp?" }, Deny, CodeCorrectionQuestion},
	} {
		t.Run(c.name, func(t *testing.T) {
			in := correctionIn(msg, confirmedRow)
			c.mut(&in)
			got(t, CorrectionDoor(in).Decision, GateCorrection, c.v, c.code)
		})
	}
}

func TestGuard_Correction_EmitsSupersedesAndTrace(t *testing.T) {
	d := CorrectionDoor(correctionIn("correct the answers for Example Corp "+correctionURL, confirmedRow))
	got(t, d.Decision, GateCorrection, Allow, CodeOK)
	if d.SupersedesRowID != "row-1" || d.Trace != "submission-correction" {
		t.Fatalf("decision = %+v", d)
	}
}

func TestGuard_Correction_PhraseTableOneCasePerPhrase(t *testing.T) {
	for _, p := range CorrectionPhrases {
		t.Run(p, func(t *testing.T) {
			d := CorrectionDoor(correctionIn("Please "+p+" for Example Corp "+correctionURL, confirmedRow))
			got(t, d.Decision, GateCorrection, Allow, CodeOK)
		})
	}
	for _, msg := range []string{"the correct answer was Yes for Example Corp " + correctionURL, "that is correct, Example Corp " + correctionURL, "Example Corp incorrect the answers"} {
		t.Run(msg, func(t *testing.T) {
			got(t, CorrectionDoor(correctionIn(msg, confirmedRow)).Decision, GateCorrection, Deny, CodeCorrectionNotRequested)
		})
	}
}

func TestGuard_Correction_QuestionMarkInsideURLIsNotAQuestion(t *testing.T) {
	d := CorrectionDoor(correctionIn("correct the answers for Example Corp "+correctionURL+"?token=abc", confirmedRow))
	got(t, d.Decision, GateCorrection, Allow, CodeOK)
	d = CorrectionDoor(correctionIn("correct the answers for Example Corp "+correctionURL+" ?", confirmedRow))
	got(t, d.Decision, GateCorrection, Deny, CodeCorrectionQuestion)
}

func TestGuard_Correction_CompanyNamedOutsideURLsOnly(t *testing.T) {
	row := CorrectionRow{ID: "r", At: t0, Company: "Example Corp"}
	d := CorrectionDoor(correctionIn("correct the answers "+correctionURL, row))
	got(t, d.Decision, GateCorrection, Deny, CodeCorrectionNoCompany)
	if d.Reason != "correction refused: the message must name the company" {
		t.Fatalf("reason = %q", d.Reason)
	}
	// A slug inside a pasted link does not name the company.
	d = CorrectionDoor(correctionIn("correct the answers https://jobs.example.test/example-corp/42", row))
	got(t, d.Decision, GateCorrection, Deny, CodeCorrectionNoCompany)
	// Whole word, case-insensitive.
	got(t, CorrectionDoor(correctionIn("correct the answers for EXAMPLE CORP", row)).Decision, GateCorrection, Allow, CodeOK)
	got(t, CorrectionDoor(correctionIn("correct the answers for Example Corporation", row)).Decision, GateCorrection, Deny, CodeCorrectionNoCompany)
	// The report's company stands in when the ledger row names none.
	got(t, CorrectionDoor(correctionIn("correct the answers for Rocket", CorrectionRow{ID: "r", At: t0, ReportCompany: "Rocket"})).Decision, GateCorrection, Allow, CodeOK)
}

func TestGuard_Correction_MatchesByIdentityKey(t *testing.T) {
	// The caller passes every row of the posting's identity key, whatever URL
	// shape each was submitted under; the door needs only that one exists.
	rows := []CorrectionRow{{ID: "embed-row", At: t0 - 2*Day, Company: "Example Corp"}, {ID: "board-row", At: t0 - Day, Company: "Example Corp"}}
	d := CorrectionDoor(correctionIn("correct the answers for Example Corp", rows...))
	got(t, d.Decision, GateCorrection, Allow, CodeOK)
	if d.SupersedesRowID != "board-row" {
		t.Fatalf("supersedes %q, want the newest row", d.SupersedesRowID)
	}
}

func TestGuard_Correction_OnceAndUnreadableLedgerRefuses(t *testing.T) {
	msg := "correct the answers for Example Corp"
	for _, row := range []CorrectionRow{
		{ID: "a", At: t0, Company: "Example Corp", Supersedes: true},
		{ID: "b", At: t0, Company: "Example Corp", SupersededByOther: true},
	} {
		d := CorrectionDoor(correctionIn(msg, row))
		got(t, d.Decision, GateCorrection, Deny, CodeCorrectionOnce)
		if !strings.Contains(d.Reason, "a posting is corrected once") {
			t.Fatalf("reason = %q", d.Reason)
		}
	}
	// One flagged row anywhere in the posting's rows closes the door.
	got(t, CorrectionDoor(correctionIn(msg, confirmedRow, CorrectionRow{ID: "c", At: t0 - 1, Company: "Example Corp", Supersedes: true})).Decision, GateCorrection, Deny, CodeCorrectionOnce)
	in := correctionIn(msg, confirmedRow)
	in.LedgerReadable = false
	got(t, CorrectionDoor(in).Decision, GateCorrection, Deny, CodeCorrectionLedgerUnreadable)
}

func TestGuard_Correction_NeverBatchNeverCLI(t *testing.T) {
	in := correctionIn("correct the answers for Example Corp", confirmedRow)
	in.SinglePosting = false
	got(t, CorrectionDoor(in).Decision, GateCorrection, Deny, CodeCorrectionSingleOnly)
	in = correctionIn("correct the answers for Example Corp", confirmedRow)
	for _, c := range []Caller{CallerCLI, CallerDashboard, CallerApplyList, CallerTelegram, CallerScheduler} {
		in.Caller = c
		got(t, CorrectionDoor(in).Decision, GateCorrection, Deny, CodeCorrectionSingleOnly)
	}
}

func TestGuard_Correction_LiftsEvidenceOnlyAfterTheDoorExceptALiveSubmit(t *testing.T) {
	confirmed := LedgerRow{RunID: "run-1", State: StateSubmitted, Confirmed: true, At: t0 - Day, Matched: true}
	applied := TrackerRow{Status: "Applied", Relation: RelRunReport, Label: "#1 Example Corp / Engineer"}
	in := DuplicateInput{Submit: true, Now: t0, LedgerReadable: true, TrackerReadable: true, Ledger: []LedgerRow{confirmed}, Tracker: []TrackerRow{applied}}
	got(t, DuplicateGuard(in), GateDuplicateGuard, Deny, CodeAlreadySubmitted)
	in.Resubmit = true
	got(t, DuplicateGuard(in), GateDuplicateGuard, Deny, CodeAlreadySubmitted)
	in.Resubmit, in.Correction = false, true
	got(t, DuplicateGuard(in), GateDuplicateGuard, Allow, CodeCorrectionLifted)
	in.Ledger = append(in.Ledger, LedgerRow{RunID: "run-2", State: StateSubmitting, At: t0 - Minute, Matched: true})
	got(t, DuplicateGuard(in), GateDuplicateGuard, Deny, CodeLiveSubmitting)
	in.Ledger, in.LedgerReadable = nil, false
	got(t, DuplicateGuard(in), GateDuplicateGuard, Deny, CodeEvidenceUnreadable)
}

func TestGuard_Correction_OfferedMessageOpensTheDoor(t *testing.T) {
	for _, company := range []string{"Example Corp", "Rocket", "Northwind Example Labs"} {
		msg := CorrectionMessage(company, correctionURL)
		if !CorrectionAsked(msg) {
			t.Fatalf("%q is not read as a correction request", msg)
		}
		d := CorrectionDoor(correctionIn(msg, CorrectionRow{ID: "r", At: t0, Company: company}))
		got(t, d.Decision, GateCorrection, Allow, CodeOK)
	}
}

func TestGuard_Correction_AskedReadsTheUsersWording(t *testing.T) {
	for _, c := range []struct {
		msg  string
		want bool
	}{
		{"correct the answers for Example Corp " + correctionURL, true},
		{"Redo my answers on Example Corp", true},
		{"the correct answer was Yes for Example Corp " + correctionURL, false},
		{"the application is correct, apply to Example Corp", false},
		{"apply to Example Corp " + correctionURL, false},
		{"do not correct the application for Example Corp", false},
		{"https://jobs.example.test/correct-the-answers/42", false},
	} {
		if got := CorrectionAsked(c.msg); got != c.want {
			t.Errorf("CorrectionAsked(%q) = %v", c.msg, got)
		}
	}
}
