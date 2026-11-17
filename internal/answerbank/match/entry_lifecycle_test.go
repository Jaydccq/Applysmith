package match

import (
	"testing"
	"time"
)

func TestStale_ThirdCorrection(t *testing.T) {
	now := time.Date(2031, 3, 4, 5, 6, 7, 0, time.UTC)
	pinned, archived, stale := abFixed("pinned", "x"), abFixed("archived", "x"), abFixed("stale", "x")
	pinned.Pinned = true
	archived.State = StateArchived
	stale.State, stale.StaleCorrectionCount = StateStale, 3
	b := Bank{Entries: []Entry{abFixed("active", "x"), abFixed("few", "x"), pinned, archived, stale}}
	counts := map[string]int{"active": 3, "few": 2, "pinned": 9, "archived": 9, "stale": 7}
	got, changed := b.Stale(counts, now.Format(time.RFC3339))
	if len(changed) != 1 || changed[0] != "active" {
		t.Fatalf("changed = %v", changed)
	}
	byID := map[string]Entry{}
	for _, e := range got.Entries {
		byID[e.ID] = e
	}
	if a := byID["active"]; a.State != StateStale || a.StaleCorrectionCount != 3 || a.StaleAt != "2031-03-04T05:06:07Z" {
		t.Fatalf("active entry = %+v", a.Meta)
	}
	for _, id := range []string{"few", "pinned"} {
		if byID[id].EffectiveState() != StateActive {
			t.Fatalf("%s state = %q", id, byID[id].State)
		}
	}
	if byID["archived"].State != StateArchived || byID["stale"].StaleCorrectionCount != 3 {
		t.Fatal("archived and stale entries must not change")
	}
	if b.Entries[0].State != "" {
		t.Fatal("the receiver bank was modified")
	}
}
