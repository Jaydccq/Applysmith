package signals

import (
	"bytes"
	"embed"
	"encoding/json"
	"fmt"
	"math"
	"path/filepath"
	"strings"
	"time"
)

//go:embed policy/*.json
var policyFS embed.FS

// CurrentPolicy is the identity of the shipped policy.
const CurrentPolicy = "v1"

// Policy is the versioned scoring data: weights, bands, factors, half-lives
// and tier thresholds. Its declared version equals its identity (the file
// name), and a shipped version is never edited.
type Policy struct {
	Version      string                `json:"version"`
	TypeWeight   map[string]float64    `json:"typeWeight"`
	StrengthBand map[string][2]float64 `json:"strengthBand"`
	SourceFactor map[string]float64    `json:"sourceFactor"`
	HalfLifeDays map[string]float64    `json:"halfLifeDays"`
	WeakCap      float64               `json:"weakCap"`
	Tiers        struct {
		Hot   float64 `json:"hot"`
		Warm  float64 `json:"warm"`
		Watch float64 `json:"watch"`
	} `json:"tiers"`
}

// LoadPolicy returns the shipped policy.
func LoadPolicy() (Policy, error) {
	b, err := policyFS.ReadFile("policy/" + CurrentPolicy + ".json")
	if err != nil {
		return Policy{}, err
	}
	return parsePolicy(b, CurrentPolicy)
}

// LoadPolicyFile reads a candidate policy. It rejects a file whose declared
// version does not match its file name.
func LoadPolicyFile(data []byte, fileName string) (Policy, error) {
	id := strings.TrimSuffix(filepath.Base(fileName), filepath.Ext(fileName))
	return parsePolicy(data, id)
}

func parsePolicy(b []byte, id string) (Policy, error) {
	var p Policy
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&p); err != nil {
		return Policy{}, fmt.Errorf("policy %s: %w", id, err)
	}
	if p.Version != id {
		return Policy{}, fmt.Errorf("policy file %s declares version %q; the declared version must equal the file name", id, p.Version)
	}
	for _, t := range []string{TypeFunding, TypeLeadership, TypeExpansion, TypeProduct, TypeOther} {
		if _, ok := p.TypeWeight[t]; !ok {
			return Policy{}, fmt.Errorf("policy %s has no weight for %s", id, t)
		}
		if p.HalfLifeDays[t] <= 0 {
			return Policy{}, fmt.Errorf("policy %s has no half-life for %s", id, t)
		}
	}
	for _, s := range []string{ScaleHeadcount, ScaleUnquantified, ScaleVague} {
		if _, ok := p.StrengthBand[s]; !ok {
			return Policy{}, fmt.Errorf("policy %s has no strength band for %s", id, s)
		}
	}
	if _, ok := p.SourceFactor["other"]; !ok {
		return Policy{}, fmt.Errorf("policy %s has no source factor for other", id)
	}
	return p, nil
}

// StrengthFactor interpolates strength 0..10 inside the band of the scale.
func (p Policy) StrengthFactor(scale string, strength int) float64 {
	band := p.StrengthBand[scale]
	s := math.Min(math.Max(float64(strength), 0), 10)
	return band[0] + (band[1]-band[0])*s/10
}

// Factors are the five factors of one contribution.
type Factors struct {
	Type, Strength, Confidence, Source, Decay float64
}

// Contribution is the product of the five factors.
func (f Factors) Contribution() float64 {
	return f.Type * f.Strength * f.Confidence * f.Source * f.Decay
}

// Score computes the factors of an ok assessment at time now. adapters are the
// adapters of the event's documents; the highest source factor counts.
func (p Policy) Score(a Assessment, eventTime time.Time, adapters []string, now time.Time) Factors {
	src := p.SourceFactor["other"]
	for i, ad := range adapters {
		f, ok := p.SourceFactor[ad]
		if !ok {
			f = p.SourceFactor["other"]
		}
		if i == 0 || f > src {
			src = f
		}
	}
	days := math.Max(now.Sub(eventTime).Hours()/24, 0)
	hl := p.HalfLifeDays[a.Type]
	if hl <= 0 {
		hl = p.HalfLifeDays[TypeOther]
	}
	w, ok := p.TypeWeight[a.Type]
	if !ok {
		w = p.TypeWeight[TypeOther]
	}
	return Factors{Type: w, Strength: p.StrengthFactor(a.Scale, a.Strength), Confidence: a.Confidence, Source: src,
		Decay: math.Pow(0.5, days/hl)}
}

// Scored is one assessment with its factors.
type Scored struct {
	Assessment Assessment
	EventTime  time.Time
	Adapters   []string
	Factors    Factors
}

// Momentum sums the contributions of strong and moderate assessments plus the
// weak ones (indirect or none) limited to the weak cap, so no pile of weak
// signals reaches the hot threshold alone.
func (p Policy) Momentum(items []Scored) float64 {
	var strong, weak float64
	for _, it := range items {
		c := it.Factors.Contribution()
		switch it.Assessment.Intent {
		case IntentStrong, IntentModerate:
			strong += c
		default:
			weak += c
		}
	}
	return strong + math.Min(weak, p.WeakCap)
}

// TierOf maps momentum to a tier; an active hiring block limits it to warm.
func (p Policy) TierOf(momentum float64, blocked bool) string {
	t := TierCold
	switch {
	case momentum >= p.Tiers.Hot:
		t = TierHot
	case momentum >= p.Tiers.Warm:
		t = TierWarm
	case momentum >= p.Tiers.Watch:
		t = TierWatch
	}
	if blocked && t == TierHot {
		return TierWarm
	}
	return t
}
