package gmail

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"applysmith/internal/gmail/classify"
	"applysmith/internal/gmail/imaptest"
)

const testVersion = "test-classifier-1"

// jobClassify is a stand-in classifier: a subject starting with "JOB" is a
// review-free signal, anything else is ignored.
func jobClassify(m *Message, mailbox string) Verdict {
	if strings.HasPrefix(m.Subject, "JOB") {
		return Verdict{Kind: VerdictSignal, Type: "applied_confirmation", Tier: "actionable", Direction: "incoming",
			Employer: "Example Corp", Role: "Engineer", Confidence: 0.9, RuleID: "test.rule",
			Snippet: m.Snippet, Body: m.Text, Raw: m.HTML}
	}
	if strings.HasPrefix(m.Subject, "REVIEW") {
		return Verdict{Kind: VerdictSignal, Type: "review", Tier: "review", Direction: "incoming", Confidence: 0.3,
			ReviewReasons: []string{"company_unknown"}, Snippet: m.Snippet, Body: m.Text}
	}
	return Verdict{Kind: VerdictIgnored, Reason: "no_event_match"}
}

type syncEnv struct {
	*env
	sy *Syncer
}

func newSyncEnv(t *testing.T) *syncEnv {
	e := newEnv(t)
	e.connect()
	sy := &Syncer{Client: e.client, Repo: e.repo, Classify: jobClassify, Version: testVersion, Queries: []string{"Q1", "Q2", "Q3"}}
	return &syncEnv{env: e, sy: sy}
}

func (s *syncEnv) addJob(id string, ms int64) {
	s.imap.Add(imaptest.AllMail, msgSpec{ID: id, From: "Recruiting <" + addr("jobs", "example.com") + ">", Subject: "JOB " + id, Text: "Body of " + id, HTML: "<p>Body of " + id + "</p>", MS: ms})
}

func (s *syncEnv) addOther(id string, ms int64) {
	s.imap.Add(imaptest.AllMail, msgSpec{ID: id, From: addr("news", "example.org"), Subject: "Newsletter " + id, Text: "Not job mail " + id, MS: ms})
}

func (s *syncEnv) search(q string, ids ...string) {
	s.imap.Search(q, ids...)
}

func (s *syncEnv) run(trig Trigger) *RunRecord {
	s.t.Helper()
	rec, err := s.sy.Run(s.ctx, trig)
	if err != nil {
		s.t.Fatalf("Run: %v", err)
	}
	return rec
}

func (s *syncEnv) count(table string) int {
	s.t.Helper()
	var n int
	if err := s.st.Read().QueryRow("SELECT count(*) FROM " + table).Scan(&n); err != nil {
		s.t.Fatal(err)
	}
	return n
}

func (s *syncEnv) cursor() string {
	id, _, _ := s.repo.Cursor(s.ctx)
	return id
}

// bootstrapped runs one complete bootstrap over three job messages, UIDs 1 to
// 3 of All Mail (UIDVALIDITY 7): the cursor is 7:3.
func (s *syncEnv) bootstrapped() {
	s.t.Helper()
	for i, id := range []string{"a1", "a2", "a3"} {
		s.addJob(id, int64(1000+i))
	}
	s.search("Q1", "a1", "a2", "a3")
	if rec := s.run(TriggerCLI); !rec.CoverageComplete || s.cursor() != "7:3" {
		s.t.Fatalf("bootstrap incomplete: %+v cursor=%q", rec, s.cursor())
	}
}

// failBodiesOf makes a body FETCH that asks for one of ids fail.
func (s *syncEnv) failBodiesOf(ids ...string) {
	s.imap.OnBodyFetch(func(got []string) bool {
		return !slices.ContainsFunc(got, func(id string) bool { return slices.Contains(ids, id) })
	})
}

func TestGmail_Run_RecordsStagesAndCounts(t *testing.T) {
	s := newSyncEnv(t)
	s.addJob("a1", 1000)
	s.addJob("a2", 1001)
	s.addOther("a3", 1002)
	s.imap.Add(imaptest.AllMail, msgSpec{ID: "a4", From: addr("x", "example.org"), Subject: "REVIEW me", Text: "?", MS: 1003})
	s.addJob("dead", 1004)
	s.imap.Vanish("dead")
	s.search("Q1", "a1", "a2", "a3", "a4", "dead")
	rec := s.run(TriggerScheduled)
	if rec.Status != "succeeded" || rec.Trigger != TriggerScheduled || rec.EndedAt == nil {
		t.Fatalf("run = %+v", rec)
	}
	want := Counts{Listed: 5, Fetched: 4, Classified: 4, Stored: 4, Review: 1, Ignored: 1, Tombstoned: 1}
	if rec.Counts != want {
		t.Fatalf("counts = %+v, want %+v", rec.Counts, want)
	}
	got, err := s.repo.Run(s.ctx, rec.ID)
	if err != nil || got.Counts != want || len(got.Stages) != 5 {
		t.Fatalf("durable run = %+v, %v", got, err)
	}
	for _, st := range got.Stages {
		if st.Stage == StageLink && st.Status != StageSkipped {
			t.Errorf("link stage = %s without a linker", st.Status)
		}
		if st.Stage != StageLink && st.Status != StageOK {
			t.Errorf("stage %s = %s", st.Stage, st.Status)
		}
	}
}

func TestGmail_Sync_NonJobMailStoresNoBody(t *testing.T) {
	s := newSyncEnv(t)
	s.addOther("a1", 1)
	s.addJob("a2", 2)
	s.search("Q1", "a1", "a2")
	s.run(TriggerCLI)
	var n int
	if err := s.st.Read().QueryRow(`SELECT count(*) FROM gmail_evidence WHERE message_id = 'a1'`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("an ignored message left evidence: %d, %v", n, err)
	}
	if err := s.st.Read().QueryRow(`SELECT count(*) FROM gmail_bodies WHERE message_id = 'a1'`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("an ignored message left a body: %d, %v", n, err)
	}
	var reason, outcome string
	if err := s.st.Read().QueryRow(`SELECT outcome, reason FROM gmail_decisions WHERE message_id = 'a1'`).Scan(&outcome, &reason); err != nil || outcome != "ignored" || reason != "no_event_match" {
		t.Fatalf("decision = %q %q, %v", outcome, reason, err)
	}
	if err := s.st.Read().QueryRow(`SELECT count(*) FROM gmail_bodies WHERE message_id = 'a2'`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("a signal's body was not kept for reclassification: %d, %v", n, err)
	}
}

func TestGmail_Bootstrap_ThreeSearches(t *testing.T) {
	s := newSyncEnv(t)
	s.sy.Queries = nil
	s.run(TriggerCLI)
	qs := s.imap.Commands("UID SEARCH X-GM-RAW")
	if len(qs) != 3 {
		t.Fatalf("searches = %v, want 3", qs)
	}
	for i, wantPrefix := range []string{"in:anywhere newer_than:6m {from:", "in:anywhere newer_than:6m -category:promotions -category:social {", "in:sent newer_than:6m -category:promotions"} {
		if !strings.HasPrefix(strings.TrimPrefix(qs[i], "UID SEARCH X-GM-RAW "), wantPrefix) {
			t.Errorf("search %d = %q", i, qs[i])
		}
	}
	rec, _ := s.repo.Run(s.ctx, 1)
	if rec.QueryVersion != QueryVersion {
		t.Fatalf("query version = %q", rec.QueryVersion)
	}
}

// The cap bounds the messages a run fetches, and they are fetched a batch at
// a time.
func TestGmail_Sync_CapAndBatchSizes(t *testing.T) {
	s := newSyncEnv(t)
	var ids []string
	for i := range 5 {
		id := fmt.Sprintf("a%d", i+1)
		s.addJob(id, int64(i))
		ids = append(ids, id)
	}
	s.search("Q1", ids...)
	var batches [][]string
	s.imap.OnBodyFetch(func(got []string) bool { batches = append(batches, got); return true })
	s.sy.Cap, s.sy.Batch = 3, 2
	rec := s.run(TriggerCLI)
	if rec.Counts.Fetched != 3 || len(batches) != 2 || len(batches[0]) != 2 || len(batches[1]) != 1 {
		t.Fatalf("fetched %d in batches %v", rec.Counts.Fetched, batches)
	}
}

func TestGmail_Bootstrap_ResumesFromToken(t *testing.T) {
	s := newSyncEnv(t)
	s.sy.Cap = 5
	var ids []string
	for i := range 8 {
		id := fmt.Sprintf("a%d", i+1)
		s.addJob(id, int64(i))
		ids = append(ids, id)
	}
	s.search("Q1", ids...)
	first := s.run(TriggerCLI)
	if first.CoverageComplete || first.ResumeToken == "" || first.Counts.Stored != 5 {
		t.Fatalf("first run = %+v", first)
	}
	if s.cursor() != "" {
		t.Fatalf("a capped bootstrap set the cursor to %q", s.cursor())
	}
	second := s.run(TriggerCLI)
	// It went on below where Q1 stopped instead of listing the five again.
	if !second.CoverageComplete || second.Counts.Stored != 3 || second.Counts.Listed != 3 {
		t.Fatalf("second run = %+v", second)
	}
	if s.cursor() != "7:8" {
		t.Fatalf("cursor after the drain = %q", s.cursor())
	}
}

// The bootstrap reads its mark before listing, so mail that arrives while a
// capped bootstrap drains is left to the incremental run.
func TestGmail_Bootstrap_CoversMailArrivingDuringDrain(t *testing.T) {
	s := newSyncEnv(t)
	s.addJob("a1", 1)
	s.addJob("a2", 2)
	s.search("Q1", "a1", "a2", "1a7e")
	s.sy.Cap = 1
	if rec := s.run(TriggerCLI); rec.CoverageComplete {
		t.Fatalf("first run = %+v", rec)
	}
	s.addJob("1a7e", 5) // UID 3, after the mark 7:2
	s.sy.Cap = 0
	if rec := s.run(TriggerCLI); !rec.CoverageComplete || s.cursor() != "7:2" || rec.Counts.Stored != 1 {
		t.Fatalf("bootstrap = %+v cursor=%q", rec, s.cursor())
	}
	inc := s.run(TriggerScheduled)
	if inc.Mode != ModeIncr || inc.Counts.Stored != 1 || s.cursor() != "7:3" {
		t.Fatalf("incremental = %+v cursor=%q", inc, s.cursor())
	}
	var n int
	if err := s.st.Read().QueryRow(`SELECT count(*) FROM gmail_decisions WHERE message_id = '1a7e'`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("the late message was not listed: %d, %v", n, err)
	}
}

// An incremental run lists only the UIDs above the cursor; IMAP's "n:*"
// answers the highest UID even when it is below n, and that one is not
// listed again.
func TestGmail_Cursor_ReadsOnlyAboveTheMark(t *testing.T) {
	s := newSyncEnv(t)
	s.bootstrapped()
	rec := s.run(TriggerScheduled)
	if rec.Counts.Listed != 0 || !rec.CoverageComplete || s.cursor() != "7:3" {
		t.Fatalf("idle run = %+v cursor=%q", rec, s.cursor())
	}
	s.addJob("b1", 10)
	if rec = s.run(TriggerScheduled); rec.Counts.Listed != 1 || rec.Counts.Stored != 1 || s.cursor() != "7:4" {
		t.Fatalf("run = %+v cursor=%q", rec, s.cursor())
	}
}

// A capped incremental run moves the cursor to the last UID it took; the next
// run goes on from there and completes the coverage.
func TestGmail_Cursor_CappedRunMovesToTheLastUIDTaken(t *testing.T) {
	s := newSyncEnv(t)
	s.bootstrapped()
	s.addJob("b1", 10)
	s.addJob("b2", 11)
	s.addJob("b3", 12)
	s.sy.Cap = 2
	capped := s.run(TriggerScheduled)
	if capped.CoverageComplete || s.cursor() != "7:5" || capped.Counts.Stored != 2 {
		t.Fatalf("capped run: %+v cursor=%q", capped, s.cursor())
	}
	s.sy.Cap = 0
	done := s.run(TriggerScheduled)
	if !done.CoverageComplete || s.cursor() != "7:6" || done.Counts.Stored != 1 {
		t.Fatalf("follow-up = %+v cursor=%q", done, s.cursor())
	}
}

func TestGmail_Sync_CoverageCompleteRules(t *testing.T) {
	t.Run("fetch failure", func(t *testing.T) {
		s := newSyncEnv(t)
		s.addJob("a1", 1)
		s.addJob("bad", 2)
		s.search("Q1", "a1", "bad")
		s.sy.Batch = 1
		s.failBodiesOf("bad")
		rec := s.run(TriggerCLI)
		if rec.CoverageComplete || rec.Status != "failed" || rec.FailedStage != StageFetch || rec.Counts.Failed != 1 {
			t.Fatalf("run = %+v", rec)
		}
		if s.cursor() != "" {
			t.Fatal("a failed run set the cursor")
		}
	})
	t.Run("classifier panic", func(t *testing.T) {
		s := newSyncEnv(t)
		s.addJob("a1", 1)
		s.search("Q1", "a1")
		s.sy.Classify = func(*Message, string) Verdict { panic("rule table broken") }
		rec := s.run(TriggerCLI)
		if rec.FailedStage != StageClassify || rec.CoverageComplete {
			t.Fatalf("run = %+v", rec)
		}
	})
	t.Run("refused sign-in", func(t *testing.T) {
		s := newSyncEnv(t)
		s.bootstrapped()
		s.imap.Refuse("Invalid credentials (Failure)")
		rec := s.run(TriggerScheduled)
		if rec.CoverageComplete || rec.FailedStage != StageList || !AuthFailureText(rec.Error) || s.cursor() != "7:3" {
			t.Fatalf("run = %+v cursor=%q", rec, s.cursor())
		}
	})
}

// A cursor of another UIDVALIDITY no longer names a place in All Mail: the
// bootstrap searches run again and the cursor starts over in the new one.
func TestGmail_Incremental_ChangedUIDValidityRelists(t *testing.T) {
	s := newSyncEnv(t)
	s.bootstrapped()
	s.imap.SetValidity(imaptest.AllMail, 8)
	s.addJob("f1", 50)
	s.search("Q1", "a1", "a2", "a3", "f1")
	rec := s.run(TriggerScheduled)
	if !rec.FallbackUsed || rec.Mode != ModeFallback || !rec.CoverageComplete || rec.Counts.Stored != 1 || rec.Counts.Deduplicated != 3 {
		t.Fatalf("run = %+v", rec)
	}
	if s.cursor() != "8:4" {
		t.Fatalf("cursor = %q", s.cursor())
	}
	if v, _, _ := s.repo.State(s.ctx, keyCursorResetAt); v == "" {
		t.Fatal("the reset was not recorded")
	}
}

func TestGmail_Sync_NotFoundFetchTombstones(t *testing.T) {
	s := newSyncEnv(t)
	s.addJob("c1", 1)
	s.addJob("dead", 2)
	s.imap.Vanish("dead")
	s.search("Q1", "c1", "dead")
	rec := s.run(TriggerCLI)
	if rec.Status != "succeeded" || rec.Counts.Tombstoned != 1 || !rec.CoverageComplete {
		t.Fatalf("run = %+v", rec)
	}
	var n int
	if err := s.st.Read().QueryRow(`SELECT count(*) FROM gmail_tombstones WHERE message_id = 'dead'`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("tombstones = %d, %v", n, err)
	}
}

// A message deleted after it was read keeps its evidence and decision but no
// longer shows as a signal: a new classifier version reads it again and finds
// it gone.
func TestGmail_Tombstone_KeepsEvidenceHidesSignal(t *testing.T) {
	s := newSyncEnv(t)
	s.bootstrapped()
	s.imap.Vanish("a2")
	s.sy.Version = "test-classifier-2"
	rec, err := s.sy.Resync(s.ctx, "alex", TriggerCLI, true)
	if err != nil || rec.Counts.Tombstoned != 1 {
		t.Fatalf("resync = %+v, %v", rec, err)
	}
	if s.count("gmail_evidence") != 3 || s.count("gmail_decisions") != 5 {
		t.Fatal("a tombstone erased evidence or a decision")
	}
	sigs, err := s.repo.Signals(s.ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, sg := range sigs {
		if sg.MessageID == "a2" {
			t.Fatal("a tombstoned signal is still visible")
		}
	}
	if len(sigs) != 2 {
		t.Fatalf("visible signals = %d, want 2", len(sigs))
	}
}

func TestGmail_Store_IdempotentByMessageAndVersion(t *testing.T) {
	s := newSyncEnv(t)
	s.addJob("a1", 1)
	s.search("Q1", "a1")
	s.search("Q2", "a1")
	rec := s.run(TriggerCLI)
	if s.count("gmail_evidence") != 1 || s.count("gmail_decisions") != 1 || rec.Counts.Listed != 1 {
		t.Fatalf("one message listed by two searches: evidence=%d decisions=%d", s.count("gmail_evidence"), s.count("gmail_decisions"))
	}
	// Listed again by a later run: still one row each.
	s.sy.Repo.SetState(s.ctx, map[string]string{keyCursor: ""})
	again := s.run(TriggerCLI)
	if s.count("gmail_evidence") != 1 || s.count("gmail_decisions") != 1 || again.Counts.Deduplicated != 1 {
		t.Fatalf("duplicate delivery: evidence=%d decisions=%d counts=%+v", s.count("gmail_evidence"), s.count("gmail_decisions"), again.Counts)
	}
	// A new classifier version adds a decision and keeps the old one.
	s.sy.Version = "test-classifier-2"
	s.sy.Repo.SetState(s.ctx, map[string]string{keyCursor: ""})
	s.run(TriggerCLI)
	if s.count("gmail_evidence") != 1 || s.count("gmail_decisions") != 2 {
		t.Fatalf("version bump: evidence=%d decisions=%d", s.count("gmail_evidence"), s.count("gmail_decisions"))
	}
}

func TestGmail_Sync_LinkFailureIsolated(t *testing.T) {
	s := newSyncEnv(t)
	s.addJob("a1", 1)
	s.search("Q1", "a1")
	s.sy.Linker = failingLinker{}
	rec := s.run(TriggerCLI)
	if rec.FailedStage != StageLink || rec.Status != "failed" {
		t.Fatalf("run = %+v", rec)
	}
	if s.count("gmail_evidence") != 1 || s.count("gmail_decisions") != 1 {
		t.Fatal("a link failure undid committed evidence or decisions")
	}
	if rec.stage(StageStore).Status != StageOK {
		t.Fatal("the store stage was marked failed")
	}
}

type failingLinker struct{}

func (failingLinker) Link(context.Context, int64) (int, error) {
	return 0, errors.New("candidacies cannot be loaded")
}

func TestGmail_Sync_SecondRunRefusedWhileRunning(t *testing.T) {
	s := newSyncEnv(t)
	s.addJob("a1", 1)
	s.search("Q1", "a1")
	entered, release := make(chan struct{}), make(chan struct{})
	s.imap.OnBodyFetch(func([]string) bool {
		select {
		case entered <- struct{}{}:
		default:
		}
		<-release
		return true
	})
	done := make(chan *RunRecord, 1)
	go func() { rec, _ := s.sy.Run(s.ctx, TriggerScheduled); done <- rec }()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("the first run never fetched")
	}
	if _, err := s.sy.Run(s.ctx, TriggerManual); !errors.Is(err, ErrAlreadyRunning) || !strings.Contains(err.Error(), "already running") {
		t.Fatalf("second run err = %v", err)
	}
	if _, err := s.sy.Resync(s.ctx, "test", TriggerManual, true); !errors.Is(err, ErrAlreadyRunning) {
		t.Fatalf("resync err = %v", err)
	}
	if _, err := s.sy.Reclassify(s.ctx, "test", TriggerManual, true); !errors.Is(err, ErrAlreadyRunning) {
		t.Fatalf("reclassify err = %v", err)
	}
	close(release)
	<-done
	if s.sy.Running() {
		t.Fatal("still running")
	}
}

// dump summarizes the decisions without run ids or timestamps.
func (s *syncEnv) dump() string {
	rows, err := s.st.Read().Query(`SELECT message_id, classifier_version, outcome, reason, signal_type, tier FROM gmail_decisions ORDER BY message_id`)
	if err != nil {
		s.t.Fatal(err)
	}
	defer rows.Close()
	var b strings.Builder
	for rows.Next() {
		var a, v, o, r, ty, ti string
		if err := rows.Scan(&a, &v, &o, &r, &ty, &ti); err != nil {
			s.t.Fatal(err)
		}
		fmt.Fprintf(&b, "%s|%s|%s|%s|%s|%s\n", a, v, o, r, ty, ti)
	}
	return b.String() + fmt.Sprintf("cursor=%s", s.cursor())
}

func TestGmail_Sync_CrashBeforeCommitReprocessesTheSameRange(t *testing.T) {
	setup := func() *syncEnv {
		s := newSyncEnv(t)
		s.bootstrapped()
		for i := range 4 {
			s.addJob(fmt.Sprintf("b%d", i+1), int64(100+i))
		}
		return s
	}
	clean := setup()
	clean.run(TriggerScheduled)
	want := clean.dump()

	crashed := setup()
	crashed.sy.Batch = 2
	calls := 0
	crashed.imap.OnBodyFetch(func([]string) bool {
		calls++
		return calls < 2 // the connection "dies" after the first batch is committed
	})
	rec := crashed.run(TriggerScheduled)
	if rec.CoverageComplete || crashed.cursor() != "7:3" {
		t.Fatalf("interrupted run: %+v cursor=%q", rec, crashed.cursor())
	}
	crashed.imap.OnBodyFetch(nil)
	crashed.run(TriggerScheduled)
	if got := crashed.dump(); got != want {
		t.Fatalf("state after the crash and a second run differs:\n%s\nwant:\n%s", got, want)
	}
}

func TestGmail_Resync_RequiresConfirmAndAudits(t *testing.T) {
	s := newSyncEnv(t)
	s.bootstrapped()
	if _, err := s.sy.Resync(s.ctx, "alex", TriggerCLI, false); !errors.Is(err, ErrConfirmRequired) {
		t.Fatalf("err = %v", err)
	}
	if s.cursor() != "7:3" {
		t.Fatal("an unconfirmed resync cleared the cursor")
	}
	s.clk.advance(time.Hour)
	rec, err := s.sy.Resync(s.ctx, "alex", TriggerCLI, true)
	if err != nil || rec.Mode != ModeBootstrap || !rec.CoverageComplete {
		t.Fatalf("resync = %+v, %v", rec, err)
	}
	if v, _, _ := s.repo.State(s.ctx, keyCursorResetAt); v == "" {
		t.Fatal("the reset time was not recorded")
	}
	rows, err := s.st.Read().Query(`SELECT actor, command, outcome FROM gmail_audit ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var got []string
	for rows.Next() {
		var a, c, o string
		_ = rows.Scan(&a, &c, &o)
		got = append(got, a+"|"+c+"|"+o)
	}
	if len(got) != 2 || !strings.HasPrefix(got[0], "alex|gmail resync|refused") || got[1] != "alex|gmail resync|ok" {
		t.Fatalf("audit = %v", got)
	}
}

// Reconnecting the same account keeps its place; another account syncs from
// its own start with the first sync's lookback, and the mail read before stays.
func TestGmail_Connect_AnotherAccountSyncsFromItsOwnStart(t *testing.T) {
	for _, c := range []struct{ account, previous, mode string }{
		{addr("alex", "example.com"), "", ModeIncr},
		{addr("sam", "example.com"), addr("alex", "example.com"), ModeBootstrap},
	} {
		t.Run(c.account, func(t *testing.T) {
			s := newSyncEnv(t)
			s.bootstrapped()
			s.imap.SetUser(c.account)
			if res, err := s.service().Connect(s.ctx, c.account, testPassword); err != nil || res.Previous != c.previous || res.Mailbox != c.account {
				t.Fatalf("result = %+v, %v", res, err)
			}
			if rec := s.run(TriggerScheduled); rec.Mode != c.mode {
				t.Fatalf("mode = %s, want %s", rec.Mode, c.mode)
			}
			if n := s.count("gmail_evidence"); n != 3 {
				t.Fatalf("%d messages kept, want the 3 read before", n)
			}
		})
	}
}

// A sync that was reading when another account connected does not leave the
// old mailbox's place behind for the new one.
func TestGmail_Sync_AConnectDuringTheRunKeepsTheRunsCursorOut(t *testing.T) {
	s := newSyncEnv(t)
	s.bootstrapped()
	s.addJob("b1", 10)
	s.imap.OnBodyFetch(func([]string) bool {
		s.clk.advance(time.Second)
		if _, err := s.account.save(s.ctx, Credentials{Address: addr("sam", "example.com"), Password: testPassword}); err != nil {
			t.Error(err)
		}
		return true
	})
	s.run(TriggerScheduled)
	if s.cursor() != "" {
		t.Fatalf("cursor = %q, want none for the new account", s.cursor())
	}
}

func TestGmail_Reclassify_NoNetworkNoRefetch(t *testing.T) {
	s := newSyncEnv(t)
	s.bootstrapped()
	s.addOther("e9", 9)
	s.search("Q1", "a1", "a2", "a3", "e9")
	s.repo.SetState(s.ctx, map[string]string{keyCursor: ""})
	s.run(TriggerCLI)
	s.imap.Close() // the network is gone
	calls := 0
	s.sy.Version = "test-classifier-2"
	s.sy.Classify = func(m *Message, mb string) Verdict {
		calls++
		v := jobClassify(m, mb)
		if v.Kind == VerdictSignal {
			v.Type = "rejection"
		}
		return v
	}
	if _, err := s.sy.Reclassify(s.ctx, "alex", TriggerCLI, false); !errors.Is(err, ErrConfirmRequired) {
		t.Fatalf("err = %v", err)
	}
	rec, err := s.sy.Reclassify(s.ctx, "alex", TriggerCLI, true)
	if err != nil || rec.Status != "succeeded" || rec.Mode != ModeReclassify {
		t.Fatalf("reclassify = %+v, %v", rec, err)
	}
	if calls != 3 {
		t.Fatalf("classified %d messages, want the 3 with stored bodies", calls)
	}
	var n int
	if err := s.st.Read().QueryRow(`SELECT count(*) FROM gmail_decisions WHERE classifier_version = 'test-classifier-2' AND signal_type = 'rejection'`).Scan(&n); err != nil || n != 3 {
		t.Fatalf("new decisions = %d, %v", n, err)
	}
	// The ignored message kept its old decision and was not refetched.
	if err := s.st.Read().QueryRow(`SELECT count(*) FROM gmail_decisions WHERE message_id = 'e9'`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("ignored decisions = %d, %v", n, err)
	}
}

func TestGmail_Retention_BodyAgeAndSizeCaps(t *testing.T) {
	s := newSyncEnv(t)
	s.addJob("c1d", 1)
	s.search("Q1", "c1d")
	s.run(TriggerCLI)
	s.clk.advance(31 * 24 * time.Hour)
	s.addJob("e1", 2)
	s.search("Q1", "c1d", "e1")
	s.repo.SetState(s.ctx, map[string]string{keyCursor: ""})
	s.run(TriggerCLI)
	res, err := s.repo.Sweep(s.ctx, BodyMaxBytes)
	if err != nil || res.ByAge != 1 {
		t.Fatalf("sweep = %+v, %v", res, err)
	}
	if s.count("gmail_bodies") != 1 || s.count("gmail_decisions") != 2 || s.count("gmail_evidence") != 2 {
		t.Fatalf("bodies=%d decisions=%d evidence=%d: the signal and its snippet must remain", s.count("gmail_bodies"), s.count("gmail_decisions"), s.count("gmail_evidence"))
	}
	// One message a minute, each read by the incremental run after it.
	for i := range 5 {
		s.clk.advance(time.Minute)
		s.addJob(fmt.Sprintf("b1%d", i), int64(10+i)*1000)
		s.run(TriggerCLI)
	}
	var total int64
	_ = s.st.Read().QueryRow(`SELECT SUM(bytes) FROM gmail_bodies`).Scan(&total)
	res, err = s.repo.Sweep(s.ctx, total/2)
	if err != nil || res.BySize == 0 {
		t.Fatalf("size sweep = %+v, %v", res, err)
	}
	var left int64
	_ = s.st.Read().QueryRow(`SELECT COALESCE(SUM(bytes), 0) FROM gmail_bodies`).Scan(&left)
	if left > total/2 {
		t.Fatalf("bodies after the size sweep = %d, cap %d", left, total/2)
	}
	// Oldest first: the newest body survives.
	var survivors int
	_ = s.st.Read().QueryRow(`SELECT count(*) FROM gmail_bodies WHERE message_id = 'b14'`).Scan(&survivors)
	if survivors != 1 {
		t.Fatal("the sweep removed the newest body")
	}
}

func TestGmail_Purge_RemovesRowsAndKeychainEntries(t *testing.T) {
	s := newSyncEnv(t)
	s.bootstrapped()
	if err := Purge(s.ctx, s.repo, s.vault, false); !errors.Is(err, ErrConfirmRequired) {
		t.Fatalf("err = %v", err)
	}
	if err := Purge(s.ctx, s.repo, s.vault, true); err != nil {
		t.Fatal(err)
	}
	for _, tbl := range gmailTables {
		if n := s.count(tbl); n != 0 {
			t.Errorf("%s still has %d rows", tbl, n)
		}
	}
	if got := s.kc.Accounts(); len(got) != 0 {
		t.Fatalf("keychain entries left: %v", got)
	}
}

func TestGmail_Sync_PostSyncRunsOnlyAfterACompleteSuccessfulRun(t *testing.T) {
	s := newSyncEnv(t)
	calls := 0
	s.sy.PostSync = func(context.Context) { calls++ }
	s.addJob("a1", 1)
	s.addJob("bad", 2)
	s.search("Q1", "a1", "bad")
	s.failBodiesOf("bad")
	if rec := s.run(TriggerScheduled); rec.Status != "failed" || calls != 0 {
		t.Fatalf("a failed run evaluated alerts: %+v (calls %d)", rec, calls)
	}
	s.imap.OnBodyFetch(nil)
	if rec := s.run(TriggerScheduled); rec.Status != "succeeded" || !rec.CoverageComplete || calls != 1 {
		t.Fatalf("a complete run: %+v (calls %d)", rec, calls)
	}
}

func TestAlerts_EvaluatedAfterEverySyncThroughTheStack(t *testing.T) {
	p := newPipeEnv(t)
	if p.stack.Syncer.PostSync == nil {
		t.Fatal("the stack does not evaluate alerts after a sync")
	}
	// The stack's hook is the alert evaluation (that it alerts once across
	// evaluations is TestAlerts_AssessmentAlertsOnceAcrossSyncs; that a complete
	// run calls it is TestGmail_Sync_PostSyncRunsOnlyAfterACompleteSuccessfulRun).
	p.stack.Syncer.PostSync(p.ctx) // seeds silently, like the first evaluation after a connection
	p.seed(sig{id: "a1", thread: "t1", typ: classify.TypeAssessment, role: "Backend Engineer", at: p.ago(2 * hour)})
	p.stack.Syncer.PostSync(p.ctx)
	if len(p.alerts) != 1 || !strings.Contains(p.alerts[0].Title, "online assessment") {
		t.Fatalf("alerts = %+v", p.alerts)
	}
}
