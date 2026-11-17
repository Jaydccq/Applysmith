// Clean-room: docs/provenance.md#CR-EVAL-RUBRIC

package evaluate

import (
	"math"
	"testing"

	"applysmith/internal/gates"
)

func near(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

func TestScore_WorkedExample(t *testing.T) {
	sc := Compute(ScoreInput{
		Must: []float64{1, 0.5, 1}, Nice: []float64{0.5, 0}, Hidden: []float64{0.5}, Align: []float64{1, 1},
		RedFlags: []float64{0.5}, Comp: 3,
	})
	if !near(sc.Must, 4.33) || !near(sc.Nice, 2.0) || !near(sc.Match, 3.75) || !near(sc.Alignment, 5.0) || !near(sc.Hidden, 3.0) || !near(sc.Penalty, 0.25) || !near(sc.Raw, 3.59) {
		t.Fatalf("scorecard = %+v", sc)
	}
	if !near(sc.Score, 3.6) || len(sc.Caps) != 0 {
		t.Fatalf("score = %v caps = %+v", sc.Score, sc.Caps)
	}
}

func TestScore_HitScoreEmptyIsThree(t *testing.T) {
	if HitScore(nil) != 3.0 {
		t.Fatal("empty list must score 3.0")
	}
	sc := Compute(ScoreInput{Must: []float64{1, 1, 1}, Comp: 3})
	if !near(sc.Match, 5.0) || !near(sc.Alignment, 3.0) || !near(sc.Hidden, 3.0) {
		t.Fatalf("scorecard = %+v", sc)
	}
}

func TestScore_PenaltyIsCapped(t *testing.T) {
	sc := Compute(ScoreInput{Must: []float64{1, 1, 1}, RedFlags: []float64{1, 1, 1, 1, 1}, Comp: 3})
	if !near(sc.Penalty, 1.5) {
		t.Fatalf("penalty = %v", sc.Penalty)
	}
}

var strong = ScoreInput{Must: []float64{1, 1, 1, 1}, Nice: []float64{1}, Hidden: []float64{1}, Align: []float64{1}, Comp: 5}

func TestScore_OneMissCap(t *testing.T) {
	in := strong
	in.Must, in.MustMisses = []float64{1, 1, 1, 0}, 1
	sc := Compute(in)
	if sc.Raw < 4.0 || !near(sc.Midpoint, 3.7) || len(sc.Caps) != 1 || sc.Caps[0].Rule != CapOneMiss {
		t.Fatalf("scorecard = %+v", sc)
	}
}

func TestScore_TwoMissesCap(t *testing.T) {
	in := strong
	in.MustMisses = 2
	if sc := Compute(in); !near(sc.Midpoint, 2.7) || sc.Caps[0].Rule != CapTwoMisses {
		t.Fatalf("scorecard = %+v", sc)
	}
}

func TestScore_HonoredGateCapAndInterval(t *testing.T) {
	in := strong
	in.MustMisses, in.HonoredGate = 1, true
	sc := Compute(in)
	if !near(sc.Midpoint, 1.5) || !near(sc.Low, 1.2) || !near(sc.High, 1.8) || !near(sc.Score, 1.5) {
		t.Fatalf("scorecard = %+v", sc)
	}
	if len(sc.Caps) != 1 || sc.Caps[0].Rule != CapHonoredGate {
		t.Fatalf("an honored gate replaces the miss caps: %+v", sc.Caps)
	}
}

func TestScore_WorkAuthorizationGate(t *testing.T) {
	in := strong
	in.WorkAuthGate = true
	sc := Compute(in)
	if !near(sc.Alignment, 1.5) && !near(sc.Alignment, 5.0) {
		t.Fatalf("alignment = %v", sc.Alignment)
	}
	if !near(sc.Midpoint, 2.5) || sc.Caps[0].Rule != CapWorkAuth {
		t.Fatalf("scorecard = %+v", sc)
	}
	in.Align = []float64{1, 1}
	if got := Compute(in).Alignment; !near(got, 1.5) {
		t.Fatalf("alignment must be limited to 1.5, got %v", got)
	}
}

func TestScore_ThinPostingCapAndInterval(t *testing.T) {
	sc := Compute(ScoreInput{Must: []float64{1, 1}, Nice: []float64{1}, Hidden: []float64{1}, Align: []float64{1}, Comp: 5})
	if !sc.Thin || !near(sc.Midpoint, 3.7) || !near(sc.Low, 3.2) || !near(sc.High, 4.2) {
		t.Fatalf("scorecard = %+v", sc)
	}
}

func TestScore_TierPenaltyAfterCaps(t *testing.T) {
	in := strong
	in.MustMisses, in.TierDelta = 1, 1.0
	sc := Compute(in)
	if !near(sc.Midpoint, 2.7) || !near(sc.Score, 2.7) {
		t.Fatalf("scorecard = %+v", sc)
	}
	// Applied once: the interval is centred on the final midpoint.
	if !near(sc.Low, 2.45) || !near(sc.High, 2.95) {
		t.Fatalf("interval = %v %v", sc.Low, sc.High)
	}
}

func TestScore_TierPenaltyOnAnUncappedScore(t *testing.T) {
	base := Compute(strong)
	pen := strong
	pen.TierDelta = 1.5
	got := Compute(pen)
	if !near(got.Score, round1(base.Midpoint-1.5)) || !near(got.Score, 3.5) {
		t.Fatalf("base %v penalized %v", base.Score, got.Score)
	}
	floor := strong
	floor.Must, floor.MustMisses, floor.TierDelta = []float64{0, 0, 0, 0}, 4, 5
	if got := Compute(floor); !near(got.Score, 1.0) {
		t.Fatalf("the penalized score is clamped to 1.0, got %v", got.Score)
	}
}

func TestScore_CompensationBands(t *testing.T) {
	target := CompTarget{Currency: "USD", Minimum: 100000, Target: 130000}
	for _, c := range []struct {
		high float64
		want float64
	}{{140000, 5}, {130000, 5}, {120000, 4}, {100000, 4}, {95000, 3}, {90000, 3}, {80000, 2}, {75000, 2}, {60000, 1}} {
		if got := CompensationScore(&gates.Range{Currency: "USD", Low: c.high - 5000, High: c.high}, target); got != c.want {
			t.Errorf("upper bound %v scored %v, want %v", c.high, got, c.want)
		}
	}
	if got := CompensationScore(nil, target); got != 3 {
		t.Errorf("no range = %v", got)
	}
	if got := CompensationScore(&gates.Range{Currency: "USD", High: 1}, CompTarget{}); got != 3 {
		t.Errorf("no minimum = %v", got)
	}
	if got := CompensationScore(&gates.Range{Currency: "USD", High: 1}, CompTarget{Currency: "CAD", Minimum: 100000}); got != 3 {
		t.Errorf("other currency = %v", got)
	}
	for _, tc := range []struct {
		stated, profile string
		want            float64
	}{
		{"CAD", "CAD", 5}, {"eur", "EUR", 5}, {"GBP", "gbp", 5},
		{"USD", "", 5}, {"CAD", "USD", 3}, {"USD", "CAD", 3},
		{"", "USD", 3}, {"", "", 3},
	} {
		if got := CompensationScore(&gates.Range{Currency: tc.stated, High: 140000}, CompTarget{Currency: tc.profile, Minimum: 100000, Target: 130000}); got != tc.want {
			t.Errorf("currency %q against profile %q = %v, want %v", tc.stated, tc.profile, got, tc.want)
		}
	}
	// Target defaults to 110 percent of the minimum.
	if got := CompensationScore(&gates.Range{Currency: "USD", High: 109000}, CompTarget{Minimum: 100000}); got != 4 {
		t.Errorf("below the derived target = %v", got)
	}
	if got := CompensationScore(&gates.Range{Currency: "USD", High: 110000}, CompTarget{Minimum: 100000}); got != 5 {
		t.Errorf("at the derived target = %v", got)
	}
}

func TestThresholds(t *testing.T) {
	for _, c := range []struct {
		score float64
		rec   string
		docs  bool
	}{{1.0, RecSkip, false}, {3.4, RecSkip, false}, {3.5, RecConsider, false}, {4.1, RecConsider, false}, {4.2, RecApply, false}, {4.4, RecApply, false}, {4.5, RecStrong, true}, {5, RecStrong, true}} {
		if got := Recommend(c.score); got != c.rec {
			t.Errorf("Recommend(%v) = %s", c.score, got)
		}
		if got := DocumentsEligible(c.score, true); got != c.docs {
			t.Errorf("DocumentsEligible(%v) = %v", c.score, got)
		}
	}
	if DocumentsEligible(4.9, false) {
		t.Error("a screening-kind score never makes documents eligible")
	}
}
