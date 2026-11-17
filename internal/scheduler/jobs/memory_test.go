package jobs

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"applysmith/internal/agent/memory"
	"applysmith/internal/agent/sessions"
	"applysmith/internal/llm"
	"applysmith/internal/scheduler"
	"applysmith/internal/store"
)

type answerOnce struct{ text string }

func (a answerOnce) Complete(context.Context, llm.Request) (*llm.Response, error) {
	return &llm.Response{Text: a.text}, nil
}

func TestMemoryConsolidate_RunsThePassAndWritesProposalsOnly(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(ctx, filepath.Join(t.TempDir(), "applysmith.db"), store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	now := time.Date(2026, 3, 2, 12, 0, 0, 0, time.UTC)
	clock := func() time.Time { return now }
	ses := sessions.New(db, clock)
	s, _ := ses.Create(ctx, sessions.Dashboard, "")
	if _, err := ses.Append(ctx, sessions.Message{SessionID: s.ID, TurnID: "t1", Role: sessions.RoleUser, Body: "I want remote roles", OK: true}); err != nil {
		t.Fatal(err)
	}
	now = now.Add(time.Hour)
	mem := memory.New(db, clock)
	c := &memory.Consolidator{Memory: mem, Sessions: ses, Model: answerOnce{`{"facts":[{"kind":"preference","subject":"user","body":"Wants remote roles"}],"rules":[]}`}}

	reg := scheduler.NewRegistry()
	if err := Register(reg, Deps{Memory: c}); err != nil {
		t.Fatal(err)
	}
	k, ok := reg.Get("memory.consolidate")
	if !ok {
		t.Fatal("kind not registered")
	}
	summary, err := k.Run(ctx, scheduler.Invocation{})
	if err != nil || !strings.Contains(summary, "1 candidate facts") {
		t.Fatalf("summary %q, err %v", summary, err)
	}
	if trusted, _ := mem.Facts(ctx, memory.Trusted); len(trusted) != 0 {
		t.Fatalf("the nightly pass created a trusted fact: %+v", trusted)
	}
	if cands, _ := mem.Facts(ctx, memory.NeedsReview); len(cands) != 1 {
		t.Fatalf("candidates = %+v", cands)
	}
}
