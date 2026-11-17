package tracker

import (
	"slices"
	"testing"
)

func (f *fixture) rowIDs() []int64 {
	f.t.Helper()
	rows, err := f.st.Read().Query(`SELECT id FROM tracker_entries ORDER BY id`)
	if err != nil {
		f.t.Fatal(err)
	}
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			f.t.Fatal(err)
		}
		ids = append(ids, id)
	}
	return ids
}

func assertNeverTogether(t *testing.T, groups []DedupGroup, a, b int64) {
	t.Helper()
	for _, g := range groups {
		if slices.Contains(g.Members, a) && slices.Contains(g.Members, b) {
			t.Errorf("rows %d and %d (unequal ATS-native keys) share a group: %+v", a, b, g)
		}
	}
}

func TestDedup_BridgingRowNeverJoinsUnequalATSKeys(t *testing.T) {
	for name, order := range map[string][3]string{
		"bridge in the middle": {"greenhouse:example-co/1001", "k:bridge", "greenhouse:example-co/2002"},
		"bridge first":         {"k:bridge", "greenhouse:example-co/1001", "greenhouse:example-co/2002"},
		"bridge last":          {"greenhouse:example-co/1001", "greenhouse:example-co/2002", "k:bridge"},
	} {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t)
			var ids [3]int64
			for i, key := range order {
				ids[i] = f.twin("Example Corp", "Software Engineer", key, "")
			}
			var gh1, gh2 int64
			for i, key := range order {
				switch key {
				case "greenhouse:example-co/1001":
					gh1 = ids[i]
				case "greenhouse:example-co/2002":
					gh2 = ids[i]
				}
			}
			plan, err := f.tr.DedupPlan(f.ctx)
			if err != nil {
				t.Fatal(err)
			}
			assertNeverTogether(t, plan, gh1, gh2)
			applied, err := f.tr.DedupApply(f.ctx, ActorUser)
			if err != nil {
				t.Fatal(err)
			}
			assertNeverTogether(t, applied, gh1, gh2)
			if got := f.rowIDs(); !slices.Contains(got, gh1) || !slices.Contains(got, gh2) {
				t.Errorf("apply deleted a requisition row: rows left %v", got)
			}
			for _, g := range applied {
				if g.Merged {
					t.Errorf("an ambiguous bridge was merged automatically: %+v", g)
				}
			}
			for _, g := range plan {
				if !g.NeedsUser || g.Reason == "" {
					t.Errorf("an ambiguous bridge must be reported for the user: %+v", g)
				}
			}
			if len(plan) == 0 {
				t.Error("the ambiguous bridge is not reported")
			}
		})
	}
}

func TestDedup_BridgeThatMatchesOneRequisitionStillMerges(t *testing.T) {
	f := newFixture(t)
	a := f.twin("Example Corp", "Software Engineer", "greenhouse:example-co/1001", "")
	b := f.twin("Example Corp", "Software Engineer", "k:bridge", "")
	plan, err := f.tr.DedupPlan(f.ctx)
	if err != nil || len(plan) != 1 || plan[0].NeedsUser || !slices.Equal(plan[0].Members, []int64{a, b}) {
		t.Fatalf("%+v %v", plan, err)
	}
}
