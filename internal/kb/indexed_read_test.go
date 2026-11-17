package kb

import (
	"database/sql"
	"testing"
	"time"
)

// holdWriter keeps the database writer busy until the returned release runs.
func holdWriter(t *testing.T, f *fixture) (release func()) {
	t.Helper()
	held, free, done := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	go func() {
		done <- f.st.Write(f.ctx, func(*sql.Tx) error {
			close(held)
			<-free
			return nil
		})
	}()
	<-held
	return func() {
		close(free)
		if err := <-done; err != nil {
			t.Error(err)
		}
	}
}

// The indexed views read what the last Sync stored and never reconcile, so a
// read route built on them cannot write to the database.
func TestIndexedViewsReadTheLastSyncWithoutWriting(t *testing.T) {
	f := newFixture(t)
	publishStoryFile(f, "s1", map[string]string{"principles": "[ownership, delivery]", "source": "work", "dims": "[metric]", "facts": "[\"a\"]"})
	if _, err := f.svc.Sync(f.ctx); err != nil {
		t.Fatal(err)
	}
	release := holdWriter(t, f)
	defer release()

	type out struct {
		cov Coverage
		sc  StoryContext
		sug []QuadrantSuggestion
		err error
	}
	done := make(chan out, 1)
	go func() {
		var o out
		if o.cov, o.err = f.svc.CoverageIndexed(f.ctx); o.err == nil {
			o.sc, o.err = f.svc.StoryContextIndexed(f.ctx)
		}
		if o.err == nil {
			o.sug, o.err = f.svc.SuggestQuadrantsIndexed(f.ctx)
		}
		done <- o
	}()
	select {
	case o := <-done:
		if o.err != nil {
			t.Fatal(o.err)
		}
		if g := gradeOf(o.cov, "ownership"); g.Grade != GradeGreen {
			t.Errorf("ownership = %+v", g)
		}
		if len(o.sc.Banked) != 1 || o.sc.Banked[0].ID != "s1" {
			t.Errorf("banked = %+v", o.sc.Banked)
		}
		if len(o.sug) == 0 {
			t.Error("no quadrant suggestions")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("an indexed read waited for the database writer")
	}
}

func TestIndexedViewsSeeAFileOnlyAfterTheNextSync(t *testing.T) {
	f := newFixture(t)
	if _, err := f.svc.Sync(f.ctx); err != nil {
		t.Fatal(err)
	}
	publishStoryFile(f, "late", map[string]string{"principles": "[ownership, delivery]", "source": "work", "dims": "[metric]", "facts": "[\"a\"]"})
	cov, err := f.svc.CoverageIndexed(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	if g := gradeOf(cov, "ownership"); g.Grade == GradeGreen {
		t.Fatalf("an unsynced file graded: %+v", g)
	}
	if _, err := f.svc.Sync(f.ctx); err != nil {
		t.Fatal(err)
	}
	cov, _ = f.svc.CoverageIndexed(f.ctx)
	if g := gradeOf(cov, "ownership"); g.Grade != GradeGreen {
		t.Errorf("after Sync: %+v", g)
	}
}
