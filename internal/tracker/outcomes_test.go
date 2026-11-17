package tracker

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"testing"

	"applysmith/internal/store"
)

// step is one status event of a synthetic history.
type step struct {
	at         string
	kind       EventKind
	prev, next Status
}

func created(at string, s Status) step { return step{at: at, kind: EventCreated, next: s} }
func moved(at string, from, to Status) step {
	return step{at: at, kind: EventStatusChanged, prev: from, next: to}
}
func corrected(at string, from, to Status) step {
	return step{at: at, kind: EventStatusCorrected, prev: from, next: to}
}

type histRow struct {
	company, role, key, source, applied, family string
	steps                                       []step
}

// history inserts a row and its events exactly as given, so a test controls
// every timestamp. It returns the row id.
func (f *fixture) history(h histRow) int64 {
	f.t.Helper()
	var id int64
	err := f.st.Write(f.ctx, func(tx *sql.Tx) error {
		var report any
		if h.family != "" {
			res, err := tx.Exec(`INSERT INTO report_artifacts (kind, path, status, content_hash, created_at, updated_at) VALUES ('report', ?, 'ready', 'h', '2027-01-01T00:00:00Z', '2027-01-01T00:00:00Z')`, "reports/"+h.key+".md")
			if err != nil {
				return err
			}
			n, _ := res.LastInsertId()
			if _, err := tx.Exec(`INSERT INTO report_index (report_number, kind, company, role, identity_key, score_kind, recommendation, legitimacy, verification, role_family, markdown_path, html_path, job_id, created_at)
				VALUES (?, 'final', ?, ?, ?, 'final', 'apply', 'credible', 'pass', ?, 'm', 'h', 'j', '2027-01-01T00:00:00Z')`, n, h.company, h.role, h.key, h.family); err != nil {
				return err
			}
			report = n
		}
		res, err := tx.Exec(`INSERT INTO tracker_entries (date, company, role, status, applied_at, source, report_number, identity_key, created_at, updated_at)
			VALUES ('2027-01-01', ?, ?, 'applied', ?, ?, ?, ?, '2027-01-01T00:00:00Z', '2027-01-01T00:00:00Z')`,
			h.company, h.role, nullStr(h.applied), nullStr(h.source), report, h.key)
		if err != nil {
			return err
		}
		id, _ = res.LastInsertId()
		for _, s := range h.steps {
			if _, err := tx.Exec(`INSERT INTO tracker_events (entry_id, at, actor, kind, previous_status, new_status) VALUES (?, ?, 'user', ?, ?, ?)`,
				id, s.at, string(s.kind), nullStr(string(s.prev)), nullStr(string(s.next))); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		f.t.Fatal(err)
	}
	return id
}

func (f *fixture) outcomes(q OutcomeQuery) Outcomes {
	f.t.Helper()
	o, err := f.tr.Outcomes(f.ctx, q)
	if err != nil {
		f.t.Fatal(err)
	}
	return o
}

func stage(g OutcomeGroup, s Status) StageReach {
	for _, r := range g.Stages {
		if r.Stage == s {
			return r
		}
	}
	return StageReach{}
}

func TestOutcomes_InterviewThenRejectedStillCountsAsInterview(t *testing.T) {
	f := newFixture(t)
	f.history(histRow{company: "Example Corp", role: "Data Engineer", key: "k:1", applied: "2027-02-01", steps: []step{
		created("2027-02-01T10:00:00Z", Applied), moved("2027-02-10T10:00:00Z", Applied, Interview), moved("2027-02-20T10:00:00Z", Interview, Rejected)}})
	o := f.outcomes(OutcomeQuery{AsOf: "2027-03-01"})
	if got := stage(o.Overall, Interview); got.Numerator != 1 || got.Denominator != 1 {
		t.Fatalf("interview reach = %+v, want 1 of 1", got)
	}
	if got := stage(o.Overall, Rejected); got.Numerator != 1 {
		t.Fatalf("rejected = %+v, want 1", got)
	}
	if o.Overall.Closed != 1 || o.Overall.Open != 0 {
		t.Fatalf("open %d closed %d, want 0 and 1", o.Overall.Open, o.Overall.Closed)
	}
}

func TestOutcomes_DuplicatePostingsCountOnce(t *testing.T) {
	f := newFixture(t)
	f.history(histRow{company: "Example Corp", role: "Data Engineer", key: "k:1", applied: "2027-02-01", steps: []step{created("2027-02-01T10:00:00Z", Applied)}})
	f.history(histRow{company: "Example Corp", role: "Data Engineer", key: "k:2", applied: "2027-02-03", steps: []step{
		created("2027-02-03T10:00:00Z", Applied), moved("2027-02-09T10:00:00Z", Applied, Interview)}})
	o := f.outcomes(OutcomeQuery{AsOf: "2027-03-01"})
	if o.Overall.Applications != 1 || o.Folded != 1 {
		t.Fatalf("applications %d folded %d, want 1 and 1", o.Overall.Applications, o.Folded)
	}
	if got := stage(o.Overall, Interview); got.Numerator != 1 || got.Denominator != 1 {
		t.Fatalf("interview reach = %+v: the duplicate's history belongs to the one application", got)
	}
}

func TestOutcomes_DifferentRequisitionsAreNotFolded(t *testing.T) {
	f := newFixture(t)
	f.history(histRow{company: "Example Corp", role: "Data Engineer", key: "greenhouse:examplecorp/1001", applied: "2027-02-01", steps: []step{created("2027-02-01T10:00:00Z", Applied)}})
	f.history(histRow{company: "Example Corp", role: "Data Engineer", key: "greenhouse:examplecorp/1002", applied: "2027-02-01", steps: []step{created("2027-02-01T10:00:00Z", Applied)}})
	if o := f.outcomes(OutcomeQuery{AsOf: "2027-03-01"}); o.Overall.Applications != 2 || o.Folded != 0 {
		t.Fatalf("applications %d folded %d, want 2 and 0", o.Overall.Applications, o.Folded)
	}
}

func TestOutcomes_OpenApplicationIsNotARejection(t *testing.T) {
	f := newFixture(t)
	f.history(histRow{company: "Example Corp", role: "Data Engineer", key: "k:1", applied: "2027-02-01", steps: []step{created("2027-02-01T10:00:00Z", Applied)}})
	// The rejection comes after the cutoff and must not be read.
	f.history(histRow{company: "Sample Works", role: "Analyst", key: "k:2", applied: "2027-02-02", steps: []step{
		created("2027-02-02T10:00:00Z", Applied), moved("2027-03-20T10:00:00Z", Applied, Rejected)}})
	o := f.outcomes(OutcomeQuery{AsOf: "2027-03-01"})
	rej := stage(o.Overall, Rejected)
	if rej.Numerator != 0 || rej.Open != 2 || rej.Denominator != 2 || o.Overall.Open != 2 {
		t.Fatalf("rejected %+v open %d, want 0 of 2 with both open", rej, o.Overall.Open)
	}
	later := f.outcomes(OutcomeQuery{AsOf: "2027-04-01"})
	if got := stage(later.Overall, Rejected); got.Numerator != 1 || got.Open != 1 {
		t.Fatalf("a later cutoff reads the rejection: %+v", got)
	}
}

func TestOutcomes_CohortSelectedByApplicationDate(t *testing.T) {
	f := newFixture(t)
	for i, d := range []string{"2027-01-10", "2027-02-10", "2027-03-05"} {
		f.history(histRow{company: fmt.Sprintf("Company %c", 'A'+i), role: "Engineer", key: fmt.Sprintf("k:%d", i), applied: d, steps: []step{created(d+"T10:00:00Z", Applied)}})
	}
	o := f.outcomes(OutcomeQuery{From: "2027-02-01", To: "2027-02-28", AsOf: "2027-03-08"})
	if o.Overall.Applications != 1 || o.From != "2027-02-01" || o.AsOf != "2027-03-08" {
		t.Fatalf("outcomes = %+v", o)
	}
	if all := f.outcomes(OutcomeQuery{AsOf: "2027-03-01"}); all.Overall.Applications != 2 {
		t.Fatalf("an application dated after the cutoff is outside the cohort, got %d", all.Overall.Applications)
	}
}

func TestOutcomes_CorrectionWithdrawsTheWrongStatus(t *testing.T) {
	f := newFixture(t)
	f.history(histRow{company: "Example Corp", role: "Data Engineer", key: "k:1", applied: "2027-02-01", steps: []step{
		created("2027-02-01T10:00:00Z", Applied), moved("2027-02-02T10:00:00Z", Applied, Interview), corrected("2027-02-03T10:00:00Z", Interview, Responded)}})
	o := f.outcomes(OutcomeQuery{AsOf: "2027-03-01"})
	if got := stage(o.Overall, Interview); got.Numerator != 0 {
		t.Fatalf("a corrected-away interview still counted: %+v", got)
	}
	if got := stage(o.Overall, Responded); got.Numerator != 1 {
		t.Fatalf("responded = %+v, want 1", got)
	}
}

func TestOutcomes_GroupsBySourceAndRoleFamily(t *testing.T) {
	f := newFixture(t)
	steps := []step{created("2027-02-01T10:00:00Z", Applied)}
	f.history(histRow{company: "Alpha Co", role: "Engineer", key: "k:1", source: "referral", family: "backend", applied: "2027-02-01", steps: steps})
	f.history(histRow{company: "Beta Co", role: "Engineer", key: "k:2", source: "referral", family: "data", applied: "2027-02-01", steps: steps})
	f.history(histRow{company: "Gamma Co", role: "Engineer", key: "k:3", applied: "2027-02-01", steps: steps})
	o := f.outcomes(OutcomeQuery{AsOf: "2027-03-01"})
	if len(o.BySource) != 2 || o.BySource[0].Key != "referral" || o.BySource[0].Applications != 2 || o.BySource[1].Key != "" {
		t.Fatalf("by source = %+v", o.BySource)
	}
	if len(o.ByRoleFamily) != 3 {
		t.Fatalf("by role family = %+v", o.ByRoleFamily)
	}
}

func TestOutcomes_ReadOnly(t *testing.T) {
	f := newFixture(t)
	f.history(histRow{company: "Example Corp", role: "Data Engineer", key: "k:1", source: "referral", family: "data", applied: "2027-02-01", steps: []step{
		created("2027-02-01T10:00:00Z", Applied), moved("2027-02-10T10:00:00Z", Applied, Interview)}})
	dump := func() string {
		var b strings.Builder
		tables, err := f.st.Read().Query(`SELECT name FROM sqlite_master WHERE type = 'table' AND name NOT LIKE 'sqlite_%' ORDER BY name`)
		if err != nil {
			t.Fatal(err)
		}
		var names []string
		for tables.Next() {
			var n string
			tables.Scan(&n)
			names = append(names, n)
		}
		tables.Close()
		for _, n := range names {
			var c int
			f.st.Read().QueryRow(`SELECT count(*) FROM ` + n).Scan(&c)
			fmt.Fprintf(&b, "%s=%d\n", n, c)
		}
		rows, _ := f.st.Read().Query(`SELECT id || '|' || status || '|' || coalesce(score, '') || '|' || version || '|' || updated_at FROM tracker_entries ORDER BY id`)
		for rows.Next() {
			var l string
			rows.Scan(&l)
			b.WriteString(l + "\n")
		}
		rows.Close()
		return b.String()
	}
	before := dump()
	f.outcomes(OutcomeQuery{AsOf: "2027-03-01"})
	if after := dump(); after != before {
		t.Fatalf("Outcomes changed the database:\nbefore:\n%s\nafter:\n%s", before, after)
	}
}

func TestOutcomes_RejectsBadDates(t *testing.T) {
	f := newFixture(t)
	for _, q := range []OutcomeQuery{{From: "soon"}, {To: "2027-13-01"}, {AsOf: "x"}, {From: "2027-03-01", To: "2027-02-01"}} {
		if _, err := f.tr.Outcomes(f.ctx, q); !errors.Is(err, ErrInvalid) {
			t.Fatalf("Outcomes(%+v) = %v, want ErrInvalid", q, err)
		}
	}
}

// The spec's `applysmith report override <number>`: a final report below the
// floor becomes eligible for tailored PDFs, once, with a user event on its
// row; a screening report, an agent and an unknown report are refused.
func TestTracker_OverrideDocuments(t *testing.T) {
	f := newFixture(t)
	id := f.history(histRow{company: "Mercury", role: "Senior Backend Engineer", key: "k:override", family: "backend"})
	var report int64
	if err := f.st.Read().QueryRow(`SELECT report_number FROM tracker_entries WHERE id = ?`, id).Scan(&report); err != nil {
		t.Fatal(err)
	}
	if _, err := f.tr.OverrideDocuments(f.ctx, report, ActorAgent); !errors.Is(err, ErrForbidden) {
		t.Fatalf("an agent override: %v", err)
	}
	for range 2 {
		e, err := f.tr.OverrideDocuments(f.ctx, report, ActorUser)
		if err != nil || e.ID != id {
			t.Fatalf("override: %+v %v", e, err)
		}
	}
	var eligible, events int
	_ = f.st.Read().QueryRow(`SELECT documents_eligible FROM report_index WHERE report_number = ?`, report).Scan(&eligible)
	_ = f.st.Read().QueryRow(`SELECT count(*) FROM tracker_events WHERE entry_id = ? AND kind = 'documents_override' AND actor = 'user'`, id).Scan(&events)
	if eligible != 1 || events != 1 {
		t.Fatalf("eligible %d, override events %d", eligible, events)
	}
	if err := f.st.Write(f.ctx, func(tx *sql.Tx) error {
		_, err := tx.Exec(`UPDATE report_index SET kind = 'screening', score_kind = 'screening', documents_eligible = 0 WHERE report_number = ?`, report)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.tr.OverrideDocuments(f.ctx, report, ActorUser); !errors.Is(err, ErrInvalid) {
		t.Fatalf("a screening report was overridden: %v", err)
	}
	if _, err := f.tr.OverrideDocuments(f.ctx, report+100, ActorUser); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("an unknown report: %v", err)
	}
}
