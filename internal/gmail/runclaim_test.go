package gmail

import (
	"context"
	"errors"
	"testing"
	"time"
)

// secondSyncer is a different Syncer object over the same store, as another
// process would hold.
func (s *syncEnv) secondSyncer() *Syncer {
	return &Syncer{Client: s.client, Repo: NewRepo(s.st, s.clk.now), Classify: jobClassify, Version: testVersion, Queries: s.sy.Queries}
}

func TestGmail_Sync_OneRunPerWorkspaceAcrossSyncerObjects(t *testing.T) {
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
	done := make(chan struct{})
	go func() { defer close(done); _, _ = s.sy.Run(s.ctx, TriggerScheduled) }()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("the first run never fetched")
	}
	other := s.secondSyncer()
	if _, err := other.Run(s.ctx, TriggerCLI); !errors.Is(err, ErrAlreadyRunning) {
		t.Fatalf("run on a second Syncer: %v", err)
	}
	cursor, _, _ := s.repo.Cursor(s.ctx)
	if _, err := other.Resync(s.ctx, "test", TriggerCLI, true); !errors.Is(err, ErrAlreadyRunning) {
		t.Fatalf("resync on a second Syncer: %v", err)
	}
	if got, _, _ := s.repo.Cursor(s.ctx); got != cursor {
		t.Fatalf("a refused resync moved the cursor from %q to %q", cursor, got)
	}
	if _, err := other.Reclassify(s.ctx, "test", TriggerCLI, true); !errors.Is(err, ErrAlreadyRunning) {
		t.Fatalf("reclassify on a second Syncer: %v", err)
	}
	// Starting up beside a live run must not close it.
	if err := other.Recover(s.ctx); err != nil {
		t.Fatal(err)
	}
	if h, _ := s.service().Health(s.ctx); !h.Running {
		t.Fatal("recovery closed a run that is still going")
	}
	close(release)
	<-done
	if _, err := other.Run(s.ctx, TriggerCLI); err != nil {
		t.Fatalf("the slot was not released: %v", err)
	}
}

func TestGmail_Sync_StaleRunningRowIsRecovered(t *testing.T) {
	s := newSyncEnv(t)
	dead := true
	s.repo.alive = func(int) bool { return !dead }
	if _, err := s.repo.beginRun(s.ctx, TriggerCLI, ModeBootstrap, testVersion, nil); err != nil {
		t.Fatal(err)
	}
	svc := s.service()
	if h, _ := svc.Health(s.ctx); !h.Running {
		t.Fatal("the orphan row is not running")
	}
	if err := s.sy.Recover(s.ctx); err != nil {
		t.Fatal(err)
	}
	h, _ := svc.Health(s.ctx)
	if h.Running || h.LatestRun == nil || h.LatestRun.Status != "failed" || h.LatestRun.EndedAt == nil {
		t.Fatalf("health after recovery = %+v", h)
	}
	// Without a recovery call a new run closes the orphan itself.
	if _, err := s.repo.beginRun(s.ctx, TriggerCLI, ModeBootstrap, testVersion, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := s.repo.beginRun(s.ctx, TriggerCLI, ModeBootstrap, testVersion, nil); err != nil {
		t.Fatalf("a row whose owner is gone blocked a new run: %v", err)
	}
	runs, _ := s.repo.Runs(s.ctx, 10)
	running := 0
	for _, r := range runs {
		if r.Status == "running" {
			running++
		}
	}
	if running != 1 {
		t.Fatalf("%d runs are running", running)
	}
}

func TestGmail_Sync_LiveOwnerIsHonouredUntilTheRowIsStale(t *testing.T) {
	s := newSyncEnv(t)
	s.repo.alive = func(int) bool { return true }
	if _, err := s.repo.beginRun(s.ctx, TriggerCLI, ModeBootstrap, testVersion, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := s.repo.beginRun(s.ctx, TriggerCLI, ModeBootstrap, testVersion, nil); !errors.Is(err, ErrAlreadyRunning) {
		t.Fatalf("a live owner: %v", err)
	}
	// A recycled process id must not hold the slot for ever.
	s.clk.advance(StaleRunAfter + time.Minute)
	if _, err := s.repo.beginRun(s.ctx, TriggerCLI, ModeBootstrap, testVersion, nil); err != nil {
		t.Fatalf("a stale row blocked a new run: %v", err)
	}
}

// A run whose caller gave up closes its row all the same, so the next run in
// this process is not refused as one beside a live run.
func TestGmail_Sync_ACancelledRunClosesItsRow(t *testing.T) {
	s := newSyncEnv(t)
	s.addJob("a1", 1)
	s.search("Q1", "a1")
	ctx, cancel := context.WithCancel(s.ctx)
	s.sy.Classify = func(m *Message, mailbox string) Verdict {
		cancel()
		return jobClassify(m, mailbox)
	}
	rec, err := s.sy.Run(ctx, TriggerCLI)
	if err != nil || rec.Status != "failed" || rec.EndedAt == nil {
		t.Fatalf("the cancelled run = %+v, %v", rec, err)
	}
	if h, _ := s.service().Health(s.ctx); h.Running || h.LatestRun == nil || h.LatestRun.Status != "failed" {
		t.Fatalf("health after the cancelled run = %+v", h)
	}
	s.sy.Classify = jobClassify
	if next := s.run(TriggerCLI); next.Status != "succeeded" {
		t.Fatalf("the run after it = %+v", next)
	}
	// A reclassification closes its row the same way.
	ctx, cancel = context.WithCancel(s.ctx)
	s.sy.Classify = func(m *Message, mailbox string) Verdict {
		cancel()
		return jobClassify(m, mailbox)
	}
	if rec, err = s.sy.Reclassify(ctx, "test", TriggerCLI, true); err != nil || rec.Status != "failed" {
		t.Fatalf("the cancelled reclassification = %+v, %v", rec, err)
	}
	if h, _ := s.service().Health(s.ctx); h.Running {
		t.Fatalf("health after the cancelled reclassification = %+v", h)
	}
}
