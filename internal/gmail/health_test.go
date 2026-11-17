package gmail

import (
	"testing"
	"time"
)

func (s *syncEnv) service() *Service {
	return &Service{Client: s.client, Account: s.account, Repo: s.repo, Now: s.clk.now}
}

func (e *env) refuseLogins(text string) {
	e.imap.Refuse(text)
}

func TestGmail_Health_SyncStatusDerivation(t *testing.T) {
	s := newSyncEnv(t)
	svc := s.service()
	h, err := svc.Health(s.ctx)
	if err != nil || h.Sync != "never-run" || h.CursorAgeSeconds != nil {
		t.Fatalf("never-run health = %+v, %v", h, err)
	}
	for i := 0; i < 5; i++ {
		s.addJob(string(rune('a'+i))+"1", int64(i))
	}
	s.search("Q1", "a1", "b1", "c1", "d1", "e1")
	s.sy.Cap = 2
	s.run(TriggerCLI)
	if h, _ = svc.Health(s.ctx); h.Sync != "partial" || h.CursorAgeSeconds != nil {
		t.Fatalf("capped health = %+v", h)
	}
	s.sy.Cap = 0
	s.run(TriggerCLI)
	s.clk.advance(90 * time.Second)
	h, _ = svc.Health(s.ctx)
	if h.Sync != "healthy" || h.CursorAgeSeconds == nil || *h.CursorAgeSeconds != 90 {
		t.Fatalf("healthy health = %+v", h)
	}
	s.sy.Classify = func(*Message, string) Verdict { panic("broken") }
	s.addJob("f1", 100)
	s.run(TriggerScheduled)
	if h, _ = svc.Health(s.ctx); h.Sync != "degraded" || h.FailedStage != StageClassify || h.Login != LoginOK {
		t.Fatalf("classify failure health = %+v", h)
	}
	if got := svc.Component(s.ctx); got != ComponentDegraded {
		t.Fatalf("component = %s", got)
	}
}

func TestGmail_Health_ContinuityAfterResyncNeedsACompleteRun(t *testing.T) {
	s := newSyncEnv(t)
	s.bootstrapped()
	s.clk.advance(time.Minute)
	_ = s.repo.SetState(s.ctx, map[string]string{keyCursorResetAt: stamp(s.clk.now())})
	h, _ := s.service().Health(s.ctx)
	if h.Sync != "partial" {
		t.Fatalf("health after a reset with no new complete run = %+v", h)
	}
}

// A refused password fails the list stage, says so in health with Gmail's
// own text, and raises the sign-in item once however many runs fail.
func TestGmail_Health_RefusedLoginIsAFailedListStage(t *testing.T) {
	s := newSyncEnv(t)
	s.bootstrapped()
	s.refuseLogins("Invalid credentials (Failure)")
	rec := s.run(TriggerScheduled)
	if rec.FailedStage != StageList {
		t.Fatalf("run = %+v", rec)
	}
	s.run(TriggerScheduled)
	svc := s.service()
	h, _ := svc.Health(s.ctx)
	if h.Login != LoginRefused || h.Sync != "degraded" || h.LoginError == "" || !h.Connected {
		t.Fatalf("health = %+v", h)
	}
	if got := svc.Component(s.ctx); got != ComponentDegraded {
		t.Fatalf("component = %s", got)
	}
	if s.refused.Load() != 1 {
		t.Fatalf("the refusal hook fired %d times over two failed runs", s.refused.Load())
	}
	// The stored stage error alone is enough to classify it.
	if !AuthFailureText(rec.Stages[0].Error) {
		t.Fatal("the stored list error is not classified as a sign-in failure")
	}
}

func TestGmail_Health_NoNetworkCall(t *testing.T) {
	s := newSyncEnv(t)
	s.bootstrapped()
	before := len(s.imap.Commands(""))
	if _, err := s.service().Health(s.ctx); err != nil {
		t.Fatal(err)
	}
	if len(s.imap.Commands("")) != before {
		t.Fatal("health reached the server")
	}
}

func TestGmail_Health_NeverConnectedIsNotAConnection(t *testing.T) {
	e := newEnv(t)
	svc := &Service{Client: e.client, Account: e.account, Repo: e.repo, Now: e.clk.now}
	h, err := svc.Health(e.ctx)
	if err != nil || h.Connected || h.Login != "" {
		t.Fatalf("not connected health = %+v, %v", h, err)
	}
	e.connect()
	if h, _ = svc.Health(e.ctx); !h.Connected || h.Login != LoginOK || h.Mailbox != addr("alex", "example.com") {
		t.Fatalf("connected health = %+v", h)
	}
}
