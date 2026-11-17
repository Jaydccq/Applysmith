package interview

import (
	"database/sql"
	"errors"
	"slices"
	"strings"
	"testing"

	"applysmith/internal/tracker"
)

func TestInterview_StageValidation(t *testing.T) {
	f := newFixture(t, fixtureOpts{})
	ok := StageInput{ID: "phone-1", Type: TypePhoneScreen, At: "2026-10-20T15:00:00Z", Format: "phone", Interviewers: []string{"Sam Sample"}, UserConfirmed: true}
	cases := []struct {
		name string
		mut  func(*StageInput)
	}{
		{"uppercase id", func(s *StageInput) { s.ID = "Phone-1" }},
		{"space in id", func(s *StageInput) { s.ID = "phone 1" }},
		{"one-character id", func(s *StageInput) { s.ID = "a" }},
		{"65-character id", func(s *StageInput) { s.ID = strings.Repeat("a", 65) }},
		{"unknown type", func(s *StageInput) { s.Type = "coffee" }},
		{"unknown format", func(s *StageInput) { s.Format = "carrier-pigeon" }},
		{"bad date", func(s *StageInput) { s.At = "next week" }},
		{"eleven interviewers", func(s *StageInput) { s.Interviewers = strings.Split(strings.Repeat("x,", 10)+"x", ",") }},
		{"long interviewer", func(s *StageInput) { s.Interviewers = []string{strings.Repeat("n", 121)} }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			in := ok
			c.mut(&in)
			if _, err := f.svc.RecordStage(f.ctx, f.cand, in); !errors.Is(err, ErrInvalid) {
				t.Fatalf("err = %v, want ErrInvalid", err)
			}
		})
	}
	got, err := f.svc.RecordStage(f.ctx, f.cand, ok)
	if err != nil || got.ID != "phone-1" || got.RecordedAt == "" {
		t.Fatalf("valid stage = %+v, %v", got, err)
	}
	if _, err := f.svc.RecordStage(f.ctx, 9999, ok); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown candidacy: %v", err)
	}
}

func TestInterview_EmailProposedStageStaysUnconfirmedUntilConfirmed(t *testing.T) {
	f := newFixture(t, fixtureOpts{})
	f.stage("tech-1", TypeTechnical, false)
	w, err := f.svc.Workspace(f.ctx, f.cand)
	if err != nil || len(w.Stages) != 1 || w.Stages[0].UserConfirmed {
		t.Fatalf("workspace = %+v, %v", w, err)
	}
	// Each next action names what settles it and the stage, so a view can link it.
	for _, want := range []Step{{Text: "Generate a prep pack for stage tech-1.", Kind: StepPack, Target: "tech-1"}, {Text: "Confirm the details of stage tech-1.", Kind: StepConfirm, Target: "tech-1"}} {
		if !slices.Contains(w.NextActions, want) {
			t.Errorf("actions = %+v, want %+v", w.NextActions, want)
		}
	}
	f.stage("tech-1", TypeTechnical, true)
	w, _ = f.svc.Workspace(f.ctx, f.cand)
	if len(w.Stages) != 1 || !w.Stages[0].UserConfirmed {
		t.Errorf("confirmed stage = %+v", w.Stages)
	}
	// The mail replays (a Gmail reprojection): the user's record stands.
	f.stage("tech-1", TypeTechnical, false)
	if w, _ = f.svc.Workspace(f.ctx, f.cand); len(w.Stages) != 1 || !w.Stages[0].UserConfirmed {
		t.Errorf("a replayed proposal replaced the confirmed stage: %+v", w.Stages)
	}
}

func TestInterview_EmptyWorkspaceNotNotFound(t *testing.T) {
	f := newFixture(t, fixtureOpts{})
	w, err := f.svc.Workspace(f.ctx, f.cand)
	if err != nil {
		t.Fatal(err)
	}
	if len(w.Stages)+len(w.Feedback)+len(w.MockTurns)+len(w.Packs) != 0 || w.Degraded || len(w.NextActions) != 1 || w.NextActions[0].Kind != StepStage || !strings.Contains(w.NextActions[0].Text, "Add an interview stage") {
		t.Errorf("empty workspace = %+v", w)
	}
	f.stage("tech-1", TypeTechnical, true)
	if w, _ = f.svc.Workspace(f.ctx, f.cand); len(w.Stages) != 1 {
		t.Errorf("the first stage needs no other setup: %+v", w)
	}
	if _, err := f.svc.Workspace(f.ctx, 9999); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown candidacy: %v", err)
	}
}

func TestInterview_EventLog_OversizedAndDegraded(t *testing.T) {
	f := newFixture(t, fixtureOpts{})
	st := f.stage("tech-1", TypeTechnical, true)
	err := f.st.Write(f.ctx, func(tx *sql.Tx) error {
		_, err := f.svc.appendEvent(f.ctx, tx, f.cand, "feedback", st.ID, Feedback{Stage: st.ID, From: FromUser, Text: strings.Repeat("a", 17000)})
		return err
	})
	if !errors.Is(err, ErrTooLarge) {
		t.Fatalf("oversized event = %v", err)
	}
	var n int
	if err := f.st.Read().QueryRow(`SELECT count(*) FROM interview_events WHERE kind = 'feedback'`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("an oversized event left %d rows (%v)", n, err)
	}
	if _, err := f.svc.RecordFeedback(f.ctx, f.cand, FeedbackInput{Stage: st.ID, Text: "Good depth answers."}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.RecordMockTurn(f.ctx, f.cand, MockInput{Stage: st.ID, Question: "Why Go?", Answer: "I like it."}); err != nil {
		t.Fatal(err)
	}
	err = f.st.Write(f.ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(f.ctx, `INSERT INTO interview_events (candidacy_id, kind, stage_id, body, created_at) VALUES (?, 'feedback', ?, '{not json', 'x')`, f.cand, st.ID)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	w, err := f.svc.Workspace(f.ctx, f.cand)
	if err != nil {
		t.Fatal(err)
	}
	if !w.Degraded || w.Repair == "" || len(w.Feedback) != 1 || len(w.MockTurns) != 1 || len(w.Stages) != 1 {
		t.Errorf("degraded view = %+v", w)
	}
}

func TestInterview_EventLog_AppendOnly(t *testing.T) {
	f := newFixture(t, fixtureOpts{})
	f.stage("tech-1", TypeTechnical, true)
	for _, q := range []string{`UPDATE interview_events SET body = '{}'`, `DELETE FROM interview_events`} {
		err := f.st.Write(f.ctx, func(tx *sql.Tx) error { _, err := tx.ExecContext(f.ctx, q); return err })
		if err == nil || !strings.Contains(err.Error(), "written once") {
			t.Errorf("%s: %v", q, err)
		}
	}
}

// A stage whose pack fell out of the capped pack list still has a pack: the
// view does not ask to generate one again.
func TestInterview_WorkspaceView_PackedStageBeyondTheListIsNotAskedAgain(t *testing.T) {
	f := newFixture(t, fixtureOpts{})
	old := f.stage("tech-1", TypeTechnical, true)
	busy := f.stage("tech-2", TypeTechnical, true)
	for i, st := range append([]Stage{old}, slices.Repeat([]Stage{busy}, ViewLimit)...) {
		in := f.input()
		in.Stage = st
		p, err := f.svc.Build(f.ctx, in)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.svc.Save(f.ctx, p); err != nil {
			t.Fatalf("pack %d: %v", i, err)
		}
	}
	w, err := f.svc.Workspace(f.ctx, f.cand)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range w.NextActions {
		if s.Kind == StepPack {
			t.Errorf("the view asks for a pack again: %+v", s)
		}
	}
}

func TestInterview_FeedbackAndMockNeedAStage(t *testing.T) {
	f := newFixture(t, fixtureOpts{})
	if _, err := f.svc.RecordFeedback(f.ctx, f.cand, FeedbackInput{Stage: "nope-1", Text: "x"}); !errors.Is(err, ErrNotFound) {
		t.Errorf("feedback: %v", err)
	}
	if _, err := f.svc.RecordMockTurn(f.ctx, f.cand, MockInput{Stage: "nope-1", Question: "q", Answer: "a"}); !errors.Is(err, ErrNotFound) {
		t.Errorf("mock turn: %v", err)
	}
	st := f.stage("tech-1", TypeTechnical, true)
	bad := []FeedbackInput{{Stage: st.ID, Text: ""}, {Stage: st.ID, Text: strings.Repeat("a", MaxFeedbackIn+1)}, {Stage: st.ID, From: "oracle", Text: "x"}}
	for _, in := range bad {
		if _, err := f.svc.RecordFeedback(f.ctx, f.cand, in); !errors.Is(err, ErrInvalid) {
			t.Errorf("%+v: %v", in, err)
		}
	}
	for _, in := range []MockInput{{Stage: st.ID, Question: strings.Repeat("q", MaxMockQuestion+1), Answer: "a"}, {Stage: st.ID, Question: "q", Answer: strings.Repeat("a", MaxMockAnswer+1)}} {
		if _, err := f.svc.RecordMockTurn(f.ctx, f.cand, in); !errors.Is(err, ErrInvalid) {
			t.Errorf("mock %d/%d: %v", len(in.Question), len(in.Answer), err)
		}
	}
}

func TestInterview_PackRevisionsAreImmutable(t *testing.T) {
	f := newFixture(t, fixtureOpts{})
	st := f.stage("tech-1", TypeTechnical, true)
	in := f.input()
	in.Stage = st
	p, err := f.svc.Build(f.ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	first, err := f.svc.Save(f.ctx, p)
	if err != nil || first.Revision != 1 {
		t.Fatalf("first = %+v, %v", first, err)
	}
	second, err := f.svc.Save(f.ctx, p)
	if err != nil || second.Revision != 2 {
		t.Fatalf("second = %+v, %v", second, err)
	}
	got, err := f.svc.Pack(f.ctx, f.cand, st.ID, 1)
	if err != nil || got.Revision != 1 {
		t.Fatalf("revision 1 = %+v, %v", got, err)
	}
	if latest, _ := f.svc.Pack(f.ctx, f.cand, st.ID, 0); latest.Revision != 2 {
		t.Errorf("latest = %d", latest.Revision)
	}
	err = f.st.Write(f.ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(f.ctx, `UPDATE interview_packs SET body = '{}' WHERE revision = 1`)
		return err
	})
	if err == nil || !strings.Contains(err.Error(), "never edited") {
		t.Errorf("a pack revision was edited: %v", err)
	}
	if _, err := f.svc.Pack(f.ctx, f.cand, st.ID, 9); !errors.Is(err, ErrNotFound) {
		t.Errorf("missing revision: %v", err)
	}
	w, _ := f.svc.Workspace(f.ctx, f.cand)
	if len(w.Packs) != 2 {
		t.Errorf("packs in view = %+v", w.Packs)
	}
	bad := p
	bad.Hooks = []Hook{{Text: "x", Sources: []string{"nowhere"}}}
	if _, err := f.svc.Save(f.ctx, bad); !errors.Is(err, ErrInvalid) {
		t.Errorf("an invalid pack was stored: %v", err)
	}
}

func TestInterview_MergeRepointsHistoryOntoTheSurvivor(t *testing.T) {
	f := newFixture(t, fixtureOpts{})
	st := f.stage("tech-1", TypeTechnical, true)
	in := f.input()
	in.Stage = st
	p, err := f.svc.Build(f.ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if _, err := f.svc.Save(f.ctx, p); err != nil {
			t.Fatal(err)
		}
	}
	// A twin of the candidacy with a score is the row a tracker merge keeps;
	// the merge repoints the history through the configured linked tables.
	var twin int64
	err = f.st.Write(f.ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(f.ctx, `INSERT INTO tracker_entries (date, company, role, status, identity_key, score, score_kind, created_at, updated_at)
			VALUES ('2026-10-12', 'Example Corp', 'Backend Engineer', 'evaluated', 'k:twin', 4.2, 'final', '2026-10-12T09:00:00Z', '2026-10-12T09:00:00Z')`)
		if err != nil {
			return err
		}
		twin, _ = res.LastInsertId()
		_, err = tx.ExecContext(f.ctx, `INSERT INTO tracker_events (entry_id, at, actor, kind, new_status) VALUES (?, '2026-10-12T09:00:00Z', 'user', 'created', 'evaluated')`, twin)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	survivor, _, err := f.tr.DedupMerge(f.ctx, tracker.ActorUser, []int64{f.cand, twin})
	if err != nil || survivor.ID != twin {
		t.Fatalf("merge kept %+v, want row %d: %v", survivor, twin, err)
	}
	w, err := f.svc.Workspace(f.ctx, survivor.ID)
	if err != nil || len(w.Stages) != 1 || len(w.Packs) != 2 {
		t.Fatalf("survivor workspace = %+v, %v", w, err)
	}
	if got, err := f.svc.Pack(f.ctx, survivor.ID, "tech-1", 0); err != nil || got.Revision != 2 {
		t.Errorf("latest pack = %+v, %v", got, err)
	}
}
