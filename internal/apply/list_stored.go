package apply

import (
	"context"
	"time"

	"applysmith/internal/identity"
)

// StoredLivenessFresh is how long a stored liveness verdict answers a list's
// probe: a posting seen live today is not loaded again before a row runs.
const StoredLivenessFresh = 24 * time.Hour

// Stored liveness verdicts, as discovery's liveness check keeps them.
const (
	storedActive   = "active"
	storedExpired  = "expired"
	storedMismatch = "mismatch"
)

// StoredVerdict is one stored liveness result.
type StoredVerdict struct {
	Verdict, Reason string
	CheckedAt       time.Time
}

// LivenessStore is the liveness results the discovery checks keep, by posting
// identity key.
type LivenessStore interface {
	Get(ctx context.Context, key string) (StoredVerdict, bool, error)
	Save(ctx context.Context, key, rawURL string, v StoredVerdict) error
}

// StoredProber answers a probe from a fresh stored verdict and loads only the
// pages whose verdict is stale, missing or inconclusive, storing what the
// browser then reads, so the next list and the evaluation share it.
type StoredProber struct {
	Store LivenessStore
	Next  Prober
	Now   func() time.Time
	// Fresh bounds the age of a verdict that answers; zero reads as
	// StoredLivenessFresh.
	Fresh time.Duration
}

// Probe returns one verdict per target. A store that cannot be read is a
// missing verdict, never a failure.
func (p StoredProber) Probe(ctx context.Context, targets []ProbeTarget) ([]ProbeVerdict, error) {
	now, fresh := time.Now, p.Fresh
	if p.Now != nil {
		now = p.Now
	}
	if fresh == 0 {
		fresh = StoredLivenessFresh
	}
	out := make([]ProbeVerdict, len(targets))
	var stale []ProbeTarget
	var at []int
	for i, t := range targets {
		if v, ok := p.stored(ctx, t.URL, now(), fresh); ok {
			out[i] = v
			continue
		}
		stale = append(stale, t)
		at = append(at, i)
	}
	if len(stale) == 0 || p.Next == nil {
		for _, i := range at {
			out[i] = ProbeVerdict{Result: ProbeUnknown, Reason: "no stored verdict"}
		}
		return out, nil
	}
	got, err := p.Next.Probe(ctx, stale)
	for k, i := range at {
		if k >= len(got) {
			out[i] = ProbeVerdict{Result: ProbeUnknown, Reason: "not probed"}
			continue
		}
		out[i] = got[k]
		p.remember(ctx, stale[k].URL, got[k], now())
	}
	return out, err
}

func (p StoredProber) stored(ctx context.Context, raw string, now time.Time, fresh time.Duration) (ProbeVerdict, bool) {
	if p.Store == nil {
		return ProbeVerdict{}, false
	}
	rec, ok, err := p.Store.Get(ctx, identity.Posting{URL: raw}.IdentityKey())
	if err != nil || !ok || now.Sub(rec.CheckedAt) >= fresh {
		return ProbeVerdict{}, false
	}
	switch rec.Verdict {
	case storedActive:
		return ProbeVerdict{Result: ProbeLive, Reason: rec.Reason}, true
	case storedExpired:
		return ProbeVerdict{Result: ProbeExpired, Reason: rec.Reason}, true
	case storedMismatch:
		return ProbeVerdict{Result: ProbeWrongJob, Reason: rec.Reason}, true
	}
	return ProbeVerdict{}, false
}

// remember stores a conclusive verdict. A challenge or an unknown page says
// nothing about the posting, so it is not stored.
func (p StoredProber) remember(ctx context.Context, raw string, v ProbeVerdict, now time.Time) {
	if p.Store == nil {
		return
	}
	var verdict string
	switch v.Result {
	case ProbeLive:
		verdict = storedActive
	case ProbeExpired:
		verdict = storedExpired
	case ProbeWrongJob:
		verdict = storedMismatch
	default:
		return
	}
	_ = p.Store.Save(ctx, identity.Posting{URL: raw}.IdentityKey(), raw, StoredVerdict{Verdict: verdict, Reason: v.Reason, CheckedAt: now})
}
