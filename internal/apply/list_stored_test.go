package apply

import (
	"context"
	"testing"
	"time"

	"applysmith/internal/identity"
)

type memLiveness struct {
	m     map[string]StoredVerdict
	saved []string
}

func (s *memLiveness) Get(_ context.Context, key string) (StoredVerdict, bool, error) {
	v, ok := s.m[key]
	return v, ok, nil
}

func (s *memLiveness) Save(_ context.Context, key, _ string, v StoredVerdict) error {
	s.m[key] = v
	s.saved = append(s.saved, key)
	return nil
}

type countingProber struct {
	seen   []string
	result ProbeResult
}

func (c *countingProber) Probe(_ context.Context, ts []ProbeTarget) ([]ProbeVerdict, error) {
	out := make([]ProbeVerdict, len(ts))
	for i, t := range ts {
		c.seen = append(c.seen, t.URL)
		out[i] = ProbeVerdict{Result: c.result, Reason: "read"}
	}
	return out, nil
}

func TestApplyList_Probe_UsesFreshStoredLivenessAndProbesStaleOrMissing(t *testing.T) {
	now := time.Date(2027, 3, 10, 12, 0, 0, 0, time.UTC)
	key := func(u string) string { return identity.Posting{URL: u}.IdentityKey() }
	urls := map[string]string{
		"fresh-expired": "https://jobs.example.test/a/1", "fresh-active": "https://jobs.example.test/a/2", "fresh-mismatch": "https://jobs.example.test/a/3",
		"stale": "https://jobs.example.test/a/4", "missing": "https://jobs.example.test/a/5", "inconclusive": "https://jobs.example.test/a/6",
	}
	st := &memLiveness{m: map[string]StoredVerdict{
		key(urls["fresh-expired"]):  {Verdict: "expired", Reason: "http-404", CheckedAt: now.Add(-time.Hour)},
		key(urls["fresh-active"]):   {Verdict: "active", CheckedAt: now.Add(-23 * time.Hour)},
		key(urls["fresh-mismatch"]): {Verdict: "mismatch", Reason: "another role", CheckedAt: now.Add(-time.Minute)},
		key(urls["stale"]):          {Verdict: "active", CheckedAt: now.Add(-25 * time.Hour)},
		key(urls["inconclusive"]):   {Verdict: "uncertain", Reason: "timeout", CheckedAt: now.Add(-time.Minute)},
	}}
	next := &countingProber{result: ProbeLive}
	p := StoredProber{Store: st, Next: next, Now: func() time.Time { return now }}
	order := []string{"fresh-expired", "fresh-active", "fresh-mismatch", "stale", "missing", "inconclusive"}
	var targets []ProbeTarget
	for _, k := range order {
		targets = append(targets, ProbeTarget{URL: urls[k], Company: "Example Corp", Role: "Engineer"})
	}
	got, err := p.Probe(context.Background(), targets)
	if err != nil {
		t.Fatal(err)
	}
	want := []ProbeResult{ProbeExpired, ProbeLive, ProbeWrongJob, ProbeLive, ProbeLive, ProbeLive}
	for i, w := range want {
		if got[i].Result != w {
			t.Errorf("%s: %s, want %s", order[i], got[i].Result, w)
		}
	}
	// Only the stale, missing and inconclusive pages were loaded, in order.
	if len(next.seen) != 3 || next.seen[0] != urls["stale"] || next.seen[1] != urls["missing"] || next.seen[2] != urls["inconclusive"] {
		t.Fatalf("pages loaded: %v", next.seen)
	}
	// What the browser read is stored for the next list.
	if v := st.m[key(urls["missing"])]; v.Verdict != "active" || !v.CheckedAt.Equal(now) {
		t.Fatalf("stored = %+v", v)
	}
	// A second probe loads nothing.
	next.seen = nil
	if _, err := p.Probe(context.Background(), targets); err != nil || len(next.seen) != 0 {
		t.Fatalf("second probe loaded %v (%v)", next.seen, err)
	}
	// An unknown or challenged page is not stored.
	next.result, st.saved = ProbeChallenge, nil
	verdicts, err := StoredProber{Store: st, Next: next, Now: func() time.Time { return now }}.Probe(context.Background(), []ProbeTarget{{URL: "https://jobs.example.test/a/9"}})
	if err != nil || len(verdicts) != 1 || verdicts[0].Result != ProbeChallenge {
		t.Fatalf("challenged probe = %+v (%v)", verdicts, err)
	}
	if len(st.saved) != 0 {
		t.Fatalf("a challenge was stored: %v", st.saved)
	}
}
