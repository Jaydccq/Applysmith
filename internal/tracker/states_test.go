// Clean-room: docs/provenance.md#CR-STATES
// Written from the application-tracker spec alone.

package tracker

import (
	"context"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"applysmith/internal/store"
)

func TestStates_GroupsDerivedFromOneTable(t *testing.T) {
	for _, info := range statusTable {
		if info.class == classUnset {
			t.Errorf("status %q is not classified into the groups", info.status)
		}
	}
	tests := []struct {
		name string
		in   func(Status) bool
		want []Status
	}{
		{"submitted", Status.Submitted, []Status{Applied, Responded, Interview, Offer, Rejected}},
		{"active", Status.Active, []Status{Applied, Responded, Interview, Offer}},
		{"decided", Status.Decided, []Status{Applied, Responded, Interview, Offer, Rejected, Discarded}},
	}
	for _, tt := range tests {
		got := slices.DeleteFunc(Statuses(), func(s Status) bool { return !tt.in(s) })
		if !slices.Equal(got, tt.want) {
			t.Errorf("%s = %v, want %v", tt.name, got, tt.want)
		}
	}
	if Discarded.Submitted() {
		t.Error("discarding records a decision not to apply, not a submission")
	}
}

var checkRE = regexp.MustCompile(`status\s+TEXT NOT NULL CHECK \(status IN \(([^)]*)\)\)`)

// The store enforces the statuses in SQL; this keeps the two lists equal.
func TestStates_InSyncWithStoreCheck(t *testing.T) {
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "applysmith.db"), store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	var ddl string
	if err := st.Read().QueryRow(`SELECT sql FROM sqlite_master WHERE name = 'tracker_entries'`).Scan(&ddl); err != nil {
		t.Fatal(err)
	}
	m := checkRE.FindStringSubmatch(ddl)
	if m == nil {
		t.Fatalf("no status CHECK found in %s", ddl)
	}
	var got []Status
	for _, part := range strings.Split(m[1], ",") {
		got = append(got, Status(strings.Trim(strings.TrimSpace(part), "'")))
	}
	if !slices.Equal(got, Statuses()) {
		t.Errorf("store CHECK lists %v, code table lists %v", got, Statuses())
	}
}

// matrix rows follow Statuses() order, columns too:
// E S A R I O J D with . = no-op, a = allowed, c = user correction, x = refused.
var matrix = map[Actor][8]string{
	ActorUser: {
		". a a a a a a a",
		"a . a a a a a a",
		"c c . a a a a a",
		"c c c . a a a a",
		"c c c c . a a a",
		"c c c c c . a a",
		"c c c c c c . c",
		"c c c c c c c .",
	},
	ActorGmail: {
		". x a a a x a x",
		"x . a a a x a x",
		"x x . a a x a x",
		"x x x . a x a x",
		"x x x x . x a x",
		"x x x x x . x x",
		"x x x x x x . x",
		"x x x x x x x .",
	},
	ActorApplyChain: {
		". x a x x x x x",
		"x . a x x x x x",
		"x x . x x x x x",
		"x x x . x x x x",
		"x x x x . x x x",
		"x x x x x . x x",
		"x x x x x x . x",
		"x x a x x x x .",
	},
	ActorAgent: {
		". a a x x x x a",
		"a . a x x x x a",
		"x x . x x x x a",
		"x x x . x x x a",
		"x x x x . x x a",
		"x x x x x . x a",
		"x x x x x x . x",
		"x x x x x x x .",
	},
	ActorEvaluation: {
		". x x x x x x x",
		"a . x x x x x x",
		"x x . x x x x x",
		"x x x . x x x x",
		"x x x x . x x x",
		"x x x x x . x x",
		"x x x x x x . x",
		"x x x x x x x .",
	},
}

var verdictOf = map[string]Verdict{".": Noop, "a": Allow, "c": Correct, "x": Refuse}

func TestTransitions_Matrix(t *testing.T) {
	for _, actor := range Actors() {
		rows, ok := matrix[actor]
		if !ok {
			t.Fatalf("no matrix for actor %s", actor)
		}
		for i, from := range Statuses() {
			cols := strings.Fields(rows[i])
			if len(cols) != 8 {
				t.Fatalf("%s row %d has %d columns", actor, i, len(cols))
			}
			for j, to := range Statuses() {
				d := Decide(actor, from, to)
				if want := verdictOf[cols[j]]; d.Verdict != want {
					t.Errorf("%s: %s -> %s = verdict %d (%s), want %d", actor, from, to, d.Verdict, d.Reason, want)
				}
				if d.Verdict == Refuse && d.Reason == "" {
					t.Errorf("%s: %s -> %s refused without a reason", actor, from, to)
				}
			}
		}
	}
}

func TestTransitions_NewRowMatrix(t *testing.T) {
	allowed := map[Actor][]Status{
		ActorUser:       Statuses(),
		ActorEvaluation: {Evaluated, Skipped},
		ActorApplyChain: {Applied},
		ActorGmail:      nil,
		ActorAgent:      nil,
	}
	for _, actor := range Actors() {
		for _, to := range Statuses() {
			got := DecideNew(actor, to).Verdict == Allow
			if want := slices.Contains(allowed[actor], to); got != want {
				t.Errorf("%s may create a row as %s = %v, want %v", actor, to, got, want)
			}
		}
	}
}

func TestTargets_OrdinaryAndCorrection(t *testing.T) {
	ord, cor := Targets(Applied)
	if !slices.Equal(ord, []Status{Responded, Interview, Offer, Rejected, Discarded}) {
		t.Errorf("ordinary from applied = %v", ord)
	}
	if !slices.Equal(cor, []Status{Evaluated, Skipped}) {
		t.Errorf("correction from applied = %v", cor)
	}
	ord, cor = Targets(Rejected)
	if len(ord) != 0 || len(cor) != 7 {
		t.Errorf("from rejected: ordinary %v, correction %v", ord, cor)
	}
}
