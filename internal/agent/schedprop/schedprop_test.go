package schedprop

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"applysmith/internal/gates"
	"applysmith/internal/store"
)

type auditLog struct {
	mu    sync.Mutex
	codes []string
}

func (a *auditLog) AppendDecision(_ context.Context, r gates.AuditRow) error {
	a.mu.Lock()
	a.codes = append(a.codes, r.Code+"/"+string(r.Caller))
	a.mu.Unlock()
	return nil
}

func newStore(t *testing.T) (*Store, *auditLog) {
	t.Helper()
	db, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "applysmith.db"), store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	a := &auditLog{}
	at := time.Date(2027, 3, 1, 9, 0, 0, 0, time.UTC)
	return New(db, a, func() time.Time { return at }), a
}

func on(b bool) *bool { return &b }

func TestPropose_StoresAProposalAndChangesNothingElse(t *testing.T) {
	s, audit := newStore(t)
	p, err := s.Propose(context.Background(), 7, "The scan finds nothing on weekends.", Change{Enabled: on(false)})
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.Get(context.Background(), p.ID)
	if err != nil || got.ScheduleID != 7 || got.Change.Enabled == nil || *got.Change.Enabled {
		t.Fatalf("stored = %+v, %v", got, err)
	}
	if len(audit.codes) != 1 || audit.codes[0] != CodeProposed+"/ask_agent" {
		t.Fatalf("audit = %v", audit.codes)
	}
}

func TestPropose_ANewerProposalReplacesThePendingOne(t *testing.T) {
	s, audit := newStore(t)
	ctx := context.Background()
	if _, err := s.Propose(ctx, 3, "first", Change{Enabled: on(false)}); err != nil {
		t.Fatal(err)
	}
	second, err := s.Propose(ctx, 3, "second", Change{Preset: &Preset{Kind: "daily", Time: "08:00", Timezone: "UTC"}})
	if err != nil {
		t.Fatal(err)
	}
	all, _ := s.List(ctx)
	if len(all) != 1 || all[0].ID != second.ID {
		t.Fatalf("pending = %+v", all)
	}
	if audit.codes[1] != CodeReplaced+"/ask_agent" {
		t.Fatalf("audit = %v", audit.codes)
	}
}

func TestPropose_RefusesWhatHasNoSchedulesReasonOrChange(t *testing.T) {
	s, _ := newStore(t)
	cases := []struct {
		name   string
		id     int64
		reason string
		ch     Change
	}{
		{"no schedule", 0, "r", Change{Enabled: on(true)}},
		{"no reason", 1, "  ", Change{Enabled: on(true)}},
		{"long reason", 1, strings.Repeat("x", MaxReasonChars+1), Change{Enabled: on(true)}},
		{"no change", 1, "r", Change{}},
	}
	for _, c := range cases {
		if _, err := s.Propose(context.Background(), c.id, c.reason, c.ch); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: err = %v", c.name, err)
		}
	}
}

func TestResolve_AcceptAndDismissEndTheProposalAndAreAudited(t *testing.T) {
	s, audit := newStore(t)
	ctx := context.Background()
	a, _ := s.Propose(ctx, 1, "a", Change{Enabled: on(true)})
	b, _ := s.Propose(ctx, 2, "b", Change{Enabled: on(false)})
	if err := s.Resolve(ctx, a.ID, true, ActorDashboard); err != nil {
		t.Fatal(err)
	}
	if err := s.Resolve(ctx, b.ID, false, ActorCLI); err != nil {
		t.Fatal(err)
	}
	if err := s.Resolve(ctx, a.ID, true, ActorDashboard); !errors.Is(err, ErrNotFound) {
		t.Fatalf("second resolve = %v", err)
	}
	want := []string{CodeAccepted + "/dashboard", CodeDismissed + "/cli"}
	if got := audit.codes[2:]; len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("audit = %v", audit.codes)
	}
}

// Undo of a dismissal (flow round 12, U-1): the proposal waits again exactly as
// it waited before the press. An accepted proposal has no undo, one its
// schedule replaced since stays dismissed under the newer one, and an undo is
// spent once used.
func TestRestore_PutsTheDismissedProposalBackAsItWas(t *testing.T) {
	ctx := context.Background()
	for _, c := range []struct {
		name     string
		accepted bool
		newer    bool
		want     error
	}{
		{"dismissed", false, false, nil},
		{"accepted", true, false, ErrNotFound},
		{"replaced since", false, true, ErrReplaced},
	} {
		s, audit := newStore(t)
		if err := s.db.Write(ctx, func(tx *sql.Tx) error {
			_, err := tx.Exec(`INSERT INTO schedules (id, name, job_kind, dtstart, created_at, updated_at) VALUES (4, 'scan', 'scan', '', '', '')`)
			return err
		}); err != nil {
			t.Fatal(err)
		}
		p, err := s.Propose(ctx, 4, "Mornings are busy.", Change{Enabled: on(false), Preset: &Preset{Kind: "weekly", Time: "07:15", Weekdays: []string{"mon"}, Timezone: "UTC"}})
		if err != nil {
			t.Fatal(err)
		}
		before, _ := s.List(ctx)
		if err := s.Resolve(ctx, p.ID, c.accepted, ActorDashboard); err != nil {
			t.Fatal(err)
		}
		if c.newer {
			if _, err := s.Propose(ctx, 4, "Evenings are quiet.", Change{Enabled: on(true)}); err != nil {
				t.Fatal(err)
			}
		}
		pending, _ := s.List(ctx)
		if c.want == nil {
			pending = before
		}
		_, err = s.Restore(ctx, p.ID, ActorDashboard)
		if after, _ := s.List(ctx); !errors.Is(err, c.want) || !reflect.DeepEqual(after, pending) {
			t.Errorf("%s: undo = %v, pending %+v, want %+v", c.name, err, after, pending)
		}
		if c.want == nil && audit.codes[len(audit.codes)-1] != CodeRestored+"/dashboard" {
			t.Errorf("%s: audit = %v", c.name, audit.codes)
		}
		if c.want == nil {
			if err := s.Resolve(ctx, p.ID, true, ActorDashboard); err != nil {
				t.Fatal(err)
			}
			pending, _ = s.List(ctx)
		}
		if _, err := s.Restore(ctx, p.ID, ActorDashboard); err == nil {
			t.Errorf("%s: a second undo restored again", c.name)
		}
		if after, _ := s.List(ctx); !reflect.DeepEqual(after, pending) {
			t.Errorf("%s: a second undo changed %+v", c.name, after)
		}
	}
}
