package accounts

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"applysmith/internal/gates"
	"applysmith/internal/store"
	"applysmith/internal/workspace"
)

type rig struct {
	svc *Service
	ws  *workspace.Workspace
	st  *store.Store
	now time.Time
}

func newRig(t *testing.T) *rig {
	t.Helper()
	r := &rig{now: time.Date(2027, 3, 10, 9, 0, 0, 0, time.UTC)}
	clock := func() time.Time { return r.now }
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "applysmith.db"), store.Options{Now: clock})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	r.ws = workspace.Open(filepath.Join(t.TempDir(), "ws"))
	if _, err := r.ws.Init(); err != nil {
		t.Fatal(err)
	}
	r.st = st
	r.svc = New(st, r.ws, clock)
	return r
}

func TestAccounts_Signal_StartsACooldownFromThePolicyHours(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	ends, err := r.svc.RecordSignal(ctx, "greenhouse", gates.SignalCaptcha, "run_a")
	if err != nil || !ends.Equal(r.now.Add(168*time.Hour)) {
		t.Fatalf("ends=%v err=%v", ends, err)
	}
	cds, err := r.svc.Cooldowns(ctx)
	if err != nil || len(cds) != 1 || cds[0].System != "greenhouse" || cds[0].End != ends.UnixMilli() {
		t.Fatalf("cooldowns = %+v %v", cds, err)
	}
	// 100 hours later it still holds; after the full 168 it is gone.
	r.now = r.now.Add(100 * time.Hour)
	if cds, _ = r.svc.Cooldowns(ctx); len(cds) != 1 {
		t.Fatalf("after 100h: %+v", cds)
	}
	r.now = r.now.Add(69 * time.Hour)
	if cds, _ = r.svc.Cooldowns(ctx); len(cds) != 0 {
		t.Fatalf("after 169h: %+v", cds)
	}
}

func TestAccounts_Signal_ZeroHoursStoresTheSignalAndStartsNoCooldown(t *testing.T) {
	r := newRig(t)
	cur, _ := r.ws.Read(workspace.FilePolicy)
	if _, err := r.ws.Edit(workspace.FilePolicy, []workspace.Op{{Kind: workspace.OpSet, Path: "cooldowns.rate_limited", Value: "0"}}, cur.Revision); err != nil {
		t.Fatal(err)
	}
	ends, err := r.svc.RecordSignal(context.Background(), "lever", gates.SignalRateLimited, "run_a")
	if err != nil || !ends.IsZero() {
		t.Fatalf("ends=%v err=%v", ends, err)
	}
	var n int
	_ = r.st.Read().QueryRow(`SELECT count(*) FROM risk_signals`).Scan(&n)
	cds, _ := r.svc.Cooldowns(context.Background())
	if n != 1 || len(cds) != 0 {
		t.Fatalf("signals=%d cooldowns=%+v", n, cds)
	}
}

// The risk view lists recent signals newest first and one active cooldown per
// system: the last-started one names the reason, the latest end is the end.
func TestAccounts_RiskView_RecentSignalsAndActiveCooldowns(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	first := r.now
	if _, err := r.svc.RecordSignal(ctx, "greenhouse", gates.SignalCaptcha, "run_a"); err != nil { // 168 h
		t.Fatal(err)
	}
	r.now = r.now.Add(time.Hour)
	if _, err := r.svc.RecordSignal(ctx, "greenhouse", gates.SignalSilentDegradation, "run_b"); err != nil { // 24 h, ends sooner
		t.Fatal(err)
	}
	r.now = r.now.Add(time.Hour)
	cur, _ := r.ws.Read(workspace.FilePolicy)
	if _, err := r.ws.Edit(workspace.FilePolicy, []workspace.Op{{Kind: workspace.OpSet, Path: "cooldowns.rate_limited", Value: "0"}}, cur.Revision); err != nil {
		t.Fatal(err)
	}
	if _, err := r.svc.RecordSignal(ctx, "lever", gates.SignalRateLimited, "run_c"); err != nil { // no cooldown
		t.Fatal(err)
	}
	sigs, err := r.svc.RecentSignals(ctx, first, 10)
	if err != nil || len(sigs) != 3 || sigs[0].System != "lever" || sigs[0].Started() || sigs[2].Signal != gates.SignalCaptcha {
		t.Fatalf("signals = %+v %v", sigs, err)
	}
	if cut, _ := r.svc.RecentSignals(ctx, first.Add(90*time.Minute), 10); len(cut) != 1 {
		t.Fatalf("only the signal after the cut should remain: %+v", cut)
	}
	active, err := r.svc.ActiveCooldowns(ctx)
	if err != nil || len(active) != 1 || active[0].System != "greenhouse" || active[0].Signal != gates.SignalSilentDegradation || !active[0].EndsAt.Equal(first.Add(168*time.Hour)) {
		t.Fatalf("active = %+v %v", active, err)
	}
	r.now = first.Add(169 * time.Hour)
	if active, _ = r.svc.ActiveCooldowns(ctx); len(active) != 0 {
		t.Fatalf("after every end: %+v", active)
	}
}

func TestAccounts_Signal_UnknownSignalIsRefused(t *testing.T) {
	r := newRig(t)
	if _, err := r.svc.RecordSignal(context.Background(), "lever", "grumpy", "run_a"); err == nil {
		t.Fatal("an unknown signal was stored")
	}
}
