// Clean-room: docs/provenance.md#CR-EVAL-RUBRIC

package gates

import (
	"strings"
	"testing"

	"applysmith/internal/identity"
)

func submitIn(rows []LedgerRow, tr []TrackerRow) DuplicateInput {
	return DuplicateInput{Submit: true, Now: t0, LedgerReadable: true, TrackerReadable: true, Ledger: rows, Tracker: tr}
}

func TestGuard_Duplicate_GreenhouseBoardAndEmbedShareKey(t *testing.T) {
	board := "https://job-boards.greenhouse.io/example-corp/jobs/1234567"
	embed := "https://job-boards.greenhouse.io/embed/job_app?for=example-corp&token=1234567"
	kb, err1 := identity.URLKey(board)
	ke, err2 := identity.URLKey(embed)
	if err1 != nil || err2 != nil || kb != ke || kb != "greenhouse:example-corp/1234567" {
		t.Fatalf("keys %q %q (%v %v)", kb, ke, err1, err2)
	}
	// The caller matches the ledger row by that key; the gate then blocks.
	row := LedgerRow{RunID: "r1", State: StateSubmitted, Confirmed: true, At: t0 - Day, Matched: kb == ke}
	d := DuplicateGuard(submitIn([]LedgerRow{row}, nil))
	got(t, d, GateDuplicateGuard, Deny, CodeAlreadySubmitted)
	if d.Class != ClassPosting || !strings.Contains(d.Reason, "already submitted") {
		t.Fatalf("decision = %+v", d)
	}
}

func TestGuard_LedgerStates_BlockTable(t *testing.T) {
	for _, c := range []struct {
		name  string
		row   LedgerRow
		resub bool
		v     Verdict
		code  string
	}{
		{"live submitting, no override", LedgerRow{RunID: "a", State: StateSubmitting, At: t0 - Minute}, true, Deny, CodeLiveSubmitting},
		{"confirmed submitted, no override", LedgerRow{RunID: "a", State: StateSubmitted, Confirmed: true, At: t0 - Hour}, true, Deny, CodeAlreadySubmitted},
		{"unconfirmed submitted", LedgerRow{RunID: "a", State: StateSubmitted, At: t0 - Hour}, false, Deny, CodeUnconfirmedSubmission},
		{"unconfirmed submitted with resubmit", LedgerRow{RunID: "a", State: StateSubmitted, At: t0 - Hour}, true, Allow, CodeResubmitLifted},
		{"may-have-submitted", LedgerRow{RunID: "a", State: StateMayHaveSubmitted, At: t0 - Hour}, false, Deny, CodeMayHaveSubmitted},
		{"may-have-submitted with resubmit", LedgerRow{RunID: "a", State: StateMayHaveSubmitted, At: t0 - Hour}, true, Allow, CodeResubmitLifted},
		{"failed is ignored", LedgerRow{RunID: "a", State: StateFailed, At: t0 - Hour}, false, Allow, CodeOK},
	} {
		t.Run(c.name, func(t *testing.T) {
			c.row.Matched = true
			in := submitIn([]LedgerRow{c.row}, nil)
			in.Resubmit = c.resub
			got(t, DuplicateGuard(in), GateDuplicateGuard, c.v, c.code)
		})
	}
	// An unmatched row is not evidence.
	got(t, DuplicateGuard(submitIn([]LedgerRow{{RunID: "z", State: StateSubmitted, Confirmed: true}}, nil)), GateDuplicateGuard, Allow, CodeOK)
}

func TestGuard_Resubmit_LiftsOnlyUnconfirmedEvidence(t *testing.T) {
	unconf := LedgerRow{RunID: "a", State: StateSubmitted, At: t0 - Hour, Matched: true}
	for _, c := range []struct {
		name    string
		rows    []LedgerRow
		tracker []TrackerRow
		code    string
		v       Verdict
	}{
		{"unconfirmed alone", []LedgerRow{unconf}, nil, CodeResubmitLifted, Allow},
		{"plus a confirmed row", []LedgerRow{unconf, {RunID: "b", State: StateSubmitted, Confirmed: true, Matched: true}}, nil, CodeAlreadySubmitted, Deny},
		{"plus an applied tracker row", []LedgerRow{unconf}, []TrackerRow{{Status: "Applied", Relation: RelRunReport, Label: "Example Corp - Junior Engineer"}}, CodeTrackerApplied, Deny},
		{"plus a live submitting row", []LedgerRow{unconf, {RunID: "c", State: StateSubmitting, At: t0 - Minute, Matched: true}}, nil, CodeLiveSubmitting, Deny},
	} {
		t.Run(c.name, func(t *testing.T) {
			in := submitIn(c.rows, c.tracker)
			in.Resubmit = true
			got(t, DuplicateGuard(in), GateDuplicateGuard, c.v, c.code)
		})
	}
}

func TestGuard_Duplicate_TrackerStatusTable(t *testing.T) {
	for _, c := range []struct {
		status string
		rel    TrackerRelation
		v      Verdict
		code   string
	}{
		{"Evaluated", RelRunReport, Allow, CodeOK},
		{"Applied", RelRunReport, Deny, CodeTrackerApplied},
		{"Interview", RelSameCandidacy, Deny, CodeTrackerApplied},
		{"Offer", RelSameCandidacy, Deny, CodeTrackerApplied},
		{"Rejected", RelRunReport, Deny, CodeTrackerApplied},
		{"Discarded", RelSameCandidacy, Allow, CodeOK},
		{"SKIP", RelRunReport, Allow, CodeOK},
		{"Evaluated", RelConfirmedReport, Deny, CodeTrackerApplied},
	} {
		t.Run(c.status+"/"+string(c.rel), func(t *testing.T) {
			got(t, DuplicateGuard(submitIn(nil, []TrackerRow{{Status: c.status, Relation: c.rel, Label: "Example Corp"}})), GateDuplicateGuard, c.v, c.code)
		})
	}
}

func TestGuard_UnreadableTrackerBlocksSubmittingRunOnly(t *testing.T) {
	in := submitIn(nil, nil)
	in.TrackerReadable = false
	d := DuplicateGuard(in)
	got(t, d, GateDuplicateGuard, Deny, CodeEvidenceUnreadable)
	if d.Class != ClassEnvironment || !strings.Contains(d.Reason, "tracker") {
		t.Fatalf("decision = %+v", d)
	}
	in.Submit = false
	got(t, DuplicateGuard(in), GateDuplicateGuard, Allow, CodeNotSubmitting)
	in.Submit, in.TrackerReadable, in.LedgerReadable = true, true, false
	d = DuplicateGuard(in)
	got(t, d, GateDuplicateGuard, Deny, CodeEvidenceUnreadable)
	if !strings.Contains(d.Reason, "ledger") {
		t.Fatalf("reason %q must name the ledger", d.Reason)
	}
}

func TestGuard_Claim_LiveWindowFifteenMinutesBoundary(t *testing.T) {
	for _, c := range []struct {
		name string
		age  int64
		want LedgerState
	}{
		{"below", LiveWindow - 1, StateSubmitting},
		{"at", LiveWindow, StateSubmitting},
		{"above", LiveWindow + 1, StateMayHaveSubmitted},
		{"sixteen minutes", 16 * Minute, StateMayHaveSubmitted},
	} {
		t.Run(c.name, func(t *testing.T) {
			r := LedgerRow{State: StateSubmitting, At: t0 - c.age}
			if s := EffectiveState(r, t0); s != c.want {
				t.Fatalf("state = %s", s)
			}
			r.RunID, r.Matched = "a", true
			in := submitIn([]LedgerRow{r}, nil)
			want := Deny
			if c.want == StateMayHaveSubmitted {
				got(t, DuplicateGuard(in), GateDuplicateGuard, want, CodeMayHaveSubmitted)
			} else {
				got(t, DuplicateGuard(in), GateDuplicateGuard, want, CodeLiveSubmitting)
			}
		})
	}
}

func TestGuard_Claim_RaceOneWinnerOnePress(t *testing.T) {
	// A stand-in for the atomic write transaction: read, decide, then insert.
	var ledger []LedgerRow
	claim := func(run string, started int64) Decision {
		d := PressClaim(ClaimInput{RunID: run, RunStartedAt: started, Now: t0, LedgerReadable: true, ClaimWritable: true, Rows: ledger})
		if d.Allowed() {
			ledger = append(ledger, LedgerRow{RunID: run, State: StateSubmitting, At: t0})
		}
		return d
	}
	first, second := claim("run-1", t0-Minute), claim("run-2", t0-Minute)
	got(t, first, GatePressClaim, Allow, CodeOK)
	got(t, second, GatePressClaim, Deny, CodeClaimLost)
	if second.Class != ClassPosting || !strings.HasPrefix(second.Reason, "duplicate submit blocked: another run (run-1)") {
		t.Fatalf("loser = %+v", second)
	}
	if len(ledger) != 1 {
		t.Fatalf("ledger = %v", ledger)
	}
}

func TestGuard_Claim_Table(t *testing.T) {
	base := ClaimInput{RunID: "me", RunStartedAt: t0 - Hour, Now: t0, LedgerReadable: true, ClaimWritable: true}
	for _, c := range []struct {
		name string
		mut  func(*ClaimInput)
		v    Verdict
		code string
	}{
		{"no rows", func(*ClaimInput) {}, Allow, CodeOK},
		{"own row only", func(i *ClaimInput) { i.Rows = []LedgerRow{{RunID: "me", At: t0}} }, Allow, CodeOK},
		{"older row of another run", func(i *ClaimInput) { i.Rows = []LedgerRow{{RunID: "x", State: StateSubmitted, At: t0 - 2*Hour}} }, Allow, CodeOK},
		{"row of another run since this run began", func(i *ClaimInput) { i.Rows = []LedgerRow{{RunID: "x", State: StateFailed, At: t0 - Minute}} }, Deny, CodeClaimLost},
		{"row exactly at run start", func(i *ClaimInput) { i.Rows = []LedgerRow{{RunID: "x", At: i.RunStartedAt}} }, Deny, CodeClaimLost},
		{"older but live submitting row", func(i *ClaimInput) { i.Rows = []LedgerRow{{RunID: "x", State: StateSubmitting, At: t0 - 2*Minute}} }, Deny, CodeClaimLost},
		{"claim unwritable", func(i *ClaimInput) { i.ClaimWritable = false }, Deny, CodeClaimUnwritable},
	} {
		t.Run(c.name, func(t *testing.T) {
			in := base
			c.mut(&in)
			got(t, PressClaim(in), GatePressClaim, c.v, c.code)
		})
	}
}

func TestGuard_Claim_UnreadableLedgerBlocksPress(t *testing.T) {
	d := PressClaim(ClaimInput{RunID: "me", LedgerReadable: false, ClaimWritable: true})
	got(t, d, GatePressClaim, Deny, CodeClaimLedgerUnreadable)
	if d.Class != ClassEnvironment {
		t.Fatalf("class = %q", d.Class)
	}
}

func TestGuard_Settle_ThrowBeforeAndAfterPressSignal(t *testing.T) {
	for _, c := range []struct {
		name string
		in   SettleInput
		want LedgerState
	}{
		{"landed", SettleInput{Landed: true, PressDispatched: true}, StateSubmitted},
		{"threw before any press input", SettleInput{}, StateFailed},
		{"press refused by the page", SettleInput{PressDispatched: true, RefusalProven: true}, StateFailed},
		{"dispatched, no verdict", SettleInput{PressDispatched: true}, StateMayHaveSubmitted},
	} {
		t.Run(c.name, func(t *testing.T) {
			if s := SettleClaim(c.in); s != c.want {
				t.Fatalf("state = %s", s)
			}
		})
	}
}

func TestGuard_Sweep_OrphanedSubmittingBecomesMayHaveSubmitted(t *testing.T) {
	for _, c := range []struct {
		name    string
		row     LedgerRow
		running bool
		want    LedgerState
		changed bool
	}{
		{"claiming process gone", LedgerRow{State: StateSubmitting, PID: 4001}, false, StateMayHaveSubmitted, true},
		{"process still running", LedgerRow{State: StateSubmitting, PID: 4001}, true, StateSubmitting, false},
		{"no pid", LedgerRow{State: StateSubmitting}, true, StateMayHaveSubmitted, true},
		{"this process's own pid", LedgerRow{State: StateSubmitting, PID: 77}, true, StateMayHaveSubmitted, true},
		{"not submitting", LedgerRow{State: StateSubmitted, PID: 4001}, false, StateSubmitted, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			s, changed := SweepRow(c.row, 77, c.running)
			if s != c.want || changed != c.changed {
				t.Fatalf("got %s %v", s, changed)
			}
		})
	}
	// A retry stays blocked until the user passes resubmit.
	in := submitIn([]LedgerRow{{RunID: "a", State: StateMayHaveSubmitted, At: t0, Matched: true}}, nil)
	got(t, DuplicateGuard(in), GateDuplicateGuard, Deny, CodeMayHaveSubmitted)
}

func TestGuard_MayHaveSubmitted_EndsOnlyByResubmitOrLaterRow(t *testing.T) {
	old := LedgerRow{RunID: "a", State: StateMayHaveSubmitted, At: t0 - Hour, Matched: true}
	got(t, DuplicateGuard(submitIn([]LedgerRow{old}, nil)), GateDuplicateGuard, Deny, CodeMayHaveSubmitted)
	later := LedgerRow{RunID: "a", State: StateFailed, At: t0 - Minute, Matched: true}
	got(t, DuplicateGuard(submitIn([]LedgerRow{old, later}, nil)), GateDuplicateGuard, Allow, CodeOK)
	other := later
	other.RunID = "b"
	got(t, DuplicateGuard(submitIn([]LedgerRow{old, other}, nil)), GateDuplicateGuard, Deny, CodeMayHaveSubmitted)
	in := submitIn([]LedgerRow{old}, nil)
	in.Resubmit = true
	got(t, DuplicateGuard(in), GateDuplicateGuard, Allow, CodeResubmitLifted)
}

func TestGuard_Stamp(t *testing.T) {
	for ms, want := range map[int64]string{0: "1970-01-01T00:00:00Z", t0: "2027-01-15T08:00:00Z", -1000: "1969-12-31T23:59:59Z", 951782400000 + 86399000: "2000-02-29T23:59:59Z"} {
		if got := stamp(ms); got != want {
			t.Errorf("stamp(%d) = %s, want %s", ms, got, want)
		}
	}
}
