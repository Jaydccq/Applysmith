package scheduler

import (
	"testing"
)

func TestScheduler_Packs_PolicyPerPack(t *testing.T) {
	env := fakeEnv("UTC", "registry", "tracked", "browser-a")
	// A slot that passes while the machine sleeps must still run once, so the
	// packs the spec names carry fire-once on every schedule.
	for _, id := range []string{"daily", "twice-daily"} {
		var pack *Pack
		for _, p := range Packs() {
			if p.ID == id {
				pack = &p
			}
		}
		if pack == nil {
			t.Fatalf("pack %s is not shipped", id)
		}
		list := pack.Build(env)
		if len(list) == 0 {
			t.Fatalf("pack %s is empty", id)
		}
		for _, s := range list {
			if s.Policy != PolicyFireOnce {
				t.Errorf("pack %s schedule %s has policy %q, want fire-once", id, s.Key, s.Policy)
			}
		}
	}
	// Every schedule of every pack names a policy, so none depends on a default.
	for _, p := range Packs() {
		for _, s := range p.Build(env) {
			if s.Policy == "" {
				t.Errorf("pack %s schedule %s names no policy", p.ID, s.Key)
			}
		}
	}
}
