package signals

import (
	"math"
	"os"
	"strings"
	"testing"
	"time"
)

func near(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

func TestScore_SourceFactorHighestAmongDocuments(t *testing.T) {
	p, _ := LoadPolicy()
	a := Assessment{Type: TypeFunding, Scale: ScaleHeadcount, Strength: 10, Confidence: 1}
	cases := []struct {
		adapters []string
		want     float64
	}{
		{[]string{adapterX, adapterHN, adapterWeb}, 0.5}, {[]string{adapterX, adapterAlerts}, 0.7}, {[]string{adapterAlerts, adapterEdgar}, 1.0},
		{[]string{adapterHNHiring}, 0.8}, {[]string{"unknown"}, 0.4}, {nil, 0.4},
	}
	for _, c := range cases {
		if got := p.Score(a, epoch, c.adapters, epoch).Source; !near(got, c.want) {
			t.Errorf("%v: source factor %v, want %v", c.adapters, got, c.want)
		}
	}
}

func TestScore_WeakSignalsCappedAtPointOneFive(t *testing.T) {
	p, _ := LoadPolicy()
	var items []Scored
	for i := 0; i < 20; i++ {
		// 0.05 each: 0.2 * 1 * 1 * 0.25... built through the factors directly.
		items = append(items, Scored{Assessment: Assessment{Intent: IntentIndirect}, Factors: Factors{Type: 0.05, Strength: 1, Confidence: 1, Source: 1, Decay: 1}})
	}
	if got := p.Momentum(items); !near(got, 0.15) {
		t.Fatalf("momentum = %v", got)
	}
	if p.TierOf(p.Momentum(items), false) == TierHot {
		t.Fatal("weak signals reached hot")
	}
	items = append(items, Scored{Assessment: Assessment{Intent: IntentStrong}, Factors: Factors{Type: 0.1, Strength: 1, Confidence: 1, Source: 1, Decay: 1}})
	if got := p.Momentum(items); !near(got, 0.25) {
		t.Fatalf("with a strong one: %v", got)
	}
}

func TestTiers_ThresholdsAndBlockClamp(t *testing.T) {
	p, _ := LoadPolicy()
	for m, want := range map[float64]string{0.249: TierWarm, 0.25: TierHot, 0.08: TierWarm, 0.0799: TierWatch, 0.03: TierWatch, 0.0299: TierCold, 0: TierCold} {
		if got := p.TierOf(m, false); got != want {
			t.Errorf("momentum %v = %s, want %s", m, got, want)
		}
	}
	if got := p.TierOf(0.40, true); got != TierWarm {
		t.Errorf("blocked hot = %s", got)
	}
	if got := p.TierOf(0.05, true); got != TierWatch {
		t.Errorf("blocked watch = %s", got)
	}
}

func TestBlocks_ExpiresAt90DaysOnlyUserLifts(t *testing.T) {
	e := newEnv(t)
	c, ev, _ := e.company("Example Corp", "Example Corp raised $14M for runway.", epoch.Add(-time.Hour), adapterEdgar)
	a := Assessment{EventID: ev.ID, Rubric: RubricVersion, Status: StatusOK, Type: TypeFunding, Intent: IntentNone, Scale: ScaleHeadcount, Strength: 10, Confidence: 1}
	if err := e.svc.repo.StoreAssessment(e.ctx, a, "", "", true); err != nil {
		t.Fatal(err)
	}
	at := func(d time.Duration) bool {
		ok, _ := e.svc.repo.ActiveBlock(e.ctx, c.ID, epoch.Add(d))
		return ok
	}
	day := 24 * time.Hour
	if !at(89*day) || at(91*day) {
		t.Fatalf("expiry: 89d=%v 91d=%v", at(89*day), at(91*day))
	}
	// A strong score never lifts it: the tier stays clamped to warm.
	ev2id, _, _ := e.svc.repo.AttachEvent(e.ctx, e.doc(Doc{URL: "https://n.example.test/2", PublishedAt: epoch, Title: "t", Body: "b"}), c.ID, TypeExpansion, epoch.Add(-time.Hour), nil)
	ev2, _ := e.svc.repo.Event(e.ctx, ev2id)
	e.assessment(ev2, Assessment{Type: TypeExpansion, Strength: 10, Confidence: 1})
	if err := e.svc.Rescore(e.ctx); err != nil {
		t.Fatal(err)
	}
	tr, _, _ := e.svc.repo.Tier(e.ctx, c.ID)
	if tr.Momentum < 0.25 || tr.Tier != TierWarm {
		t.Fatalf("tier %s momentum %v, want warm over the hot threshold", tr.Tier, tr.Momentum)
	}
	if err := e.svc.LiftBlock(e.ctx, c.ID); err != nil {
		t.Fatal(err)
	}
	if tr, _, _ := e.svc.repo.Tier(e.ctx, c.ID); tr.Tier != TierHot {
		t.Fatalf("tier after the user lifted it = %s", tr.Tier)
	}
	if n := e.count(`SELECT count(*) FROM signal_audit WHERE kind = 'block-lifted'`); n != 1 {
		t.Fatalf("lift audited %d times", n)
	}
}

func TestScore_ScanPriorityFollowsTargetRoleTeams(t *testing.T) {
	kw := roleKeywords([]string{"Software Engineer", "Data Scientist"})
	with := func(teams ...string) []Scored { return []Scored{{Assessment: Assessment{Teams: teams}}} }
	if got := scanPriority(0.4, with("Engineering"), kw); got != 0.4 {
		t.Errorf("matching team = %v", got)
	}
	if got := scanPriority(0.4, with("Sales", "Marketing"), kw); got != 0 {
		t.Errorf("non-matching teams = %v", got)
	}
	if got := scanPriority(0.4, with(), kw); got != 0.4 {
		t.Errorf("no team evidence = %v", got)
	}
	if got := scanPriority(0.4, with("Sales"), nil); got != 0.4 {
		t.Errorf("no target roles = %v", got)
	}
}

func TestSignals_Rescore_OfflineIdempotent(t *testing.T) {
	e := newEnv(t)
	_, ev, _ := e.company("Example Corp", raiseBody, epoch.Add(-24*time.Hour), adapterEdgar)
	e.assessment(ev, Assessment{})
	reqs, calls := len(e.web.requests("")), e.brain.total()
	if err := e.svc.Rescore(e.ctx); err != nil {
		t.Fatal(err)
	}
	first, _ := e.svc.repo.Tiers(e.ctx)
	e.clk.advance(0)
	if err := e.svc.Rescore(e.ctx); err != nil {
		t.Fatal(err)
	}
	second, _ := e.svc.repo.Tiers(e.ctx)
	if len(first) != 1 || len(second) != 1 {
		t.Fatalf("tiers %v %v", first, second)
	}
	for id, a := range first {
		if b := second[id]; a.Tier != b.Tier || a.Momentum != b.Momentum || !a.ChangedAt.Equal(b.ChangedAt) || !a.HotSince.Equal(b.HotSince) {
			t.Fatalf("not idempotent: %+v %+v", a, b)
		}
	}
	if len(e.web.requests("")) != reqs || e.brain.total() != calls {
		t.Fatal("a rescore made a network or model request")
	}
}

func TestReplay_RejectsFileWhoseVersionDoesNotMatchName(t *testing.T) {
	data, err := os.ReadFile("policy/v1.json")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := LoadPolicyFile(data, "/tmp/v2.json"); err == nil || !strings.Contains(err.Error(), "declares version") {
		t.Fatalf("mismatched file accepted: %v", err)
	}
	candidate := strings.Replace(strings.Replace(string(data), `"version": "v1"`, `"version": "v2"`, 1), `"hot": 0.25`, `"hot": 0.50`, 1)
	p2, err := LoadPolicyFile([]byte(candidate), "/tmp/v2.json")
	if err != nil {
		t.Fatal(err)
	}
	e := newEnv(t)
	_, ev, _ := e.company("Example Corp", raiseBody, epoch.Add(-24*time.Hour), adapterEdgar)
	e.assessment(ev, Assessment{Strength: 1, Confidence: 0.5})
	e.svc.Rescore(e.ctx)
	before, _ := e.svc.repo.Tiers(e.ctx)
	rows, err := e.svc.repo.Replay(e.ctx, p2)
	if err != nil || len(rows) != 1 {
		t.Fatalf("rows %v %v", rows, err)
	}
	if rows[0].Before == rows[0].After {
		t.Fatalf("the candidate changed nothing: %+v", rows[0])
	}
	after, _ := e.svc.repo.Tiers(e.ctx)
	for id, a := range before {
		if after[id].Tier != a.Tier || after[id].Policy != "v1" {
			t.Fatalf("replay mutated a stored tier: %+v", after[id])
		}
	}
}
