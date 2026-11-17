package ledger

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"applysmith/internal/store"
)

func open(t *testing.T) (*Ledger, *store.Store) {
	t.Helper()
	now := func() time.Time { return time.Date(2027, 3, 10, 9, 0, 0, 0, time.UTC) }
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "applysmith.db"), store.Options{Now: now})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return New(st, now), st
}

func obs(entry, url string, confirmed bool) Observation {
	return Observation{EntryID: entry, URL: url, Host: "jobs.example.com", Title: "Engineer", ObservedAt: time.Date(2027, 3, 10, 8, 0, 0, 0, time.UTC), Confirmed: confirmed}
}

func TestOneGreenhousePostingUnderTwoURLShapesIsOneRow(t *testing.T) {
	l, st := open(t)
	ctx := context.Background()
	a, err := l.RecordObserved(ctx, obs("a", "https://job-boards.greenhouse.io/example-corp/jobs/1234567", true))
	if err != nil || a.Result != Created || a.IdentityKey != "greenhouse:example-corp/1234567" {
		t.Fatalf("%+v %v", a, err)
	}
	b, err := l.RecordObserved(ctx, obs("b", "https://job-boards.greenhouse.io/embed/job_app?for=example-corp&token=1234567", true))
	if err != nil || b.Result != Attached || b.ID != a.ID {
		t.Fatalf("%+v %v", b, err)
	}
	var n int
	if err := st.Read().QueryRow(`SELECT count(*) FROM submissions`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("%d rows %v", n, err)
	}
}

func TestAnUnconfirmedRowDoesNotAttachAnotherEntry(t *testing.T) {
	l, st := open(t)
	ctx := context.Background()
	a, _ := l.RecordObserved(ctx, obs("a", "https://jobs.example.com/p/1", false))
	b, err := l.RecordObserved(ctx, obs("b", "https://jobs.example.com/p/1", false))
	if err != nil || b.Result != Created || b.ID == a.ID {
		t.Fatalf("%+v %v", b, err)
	}
	var n int
	if err := st.Read().QueryRow(`SELECT count(*) FROM submissions`).Scan(&n); err != nil || n != 2 {
		t.Fatalf("%d %v", n, err)
	}
}

func TestInvalidObservationsAreRefused(t *testing.T) {
	l, _ := open(t)
	for name, o := range map[string]Observation{
		"no scheme":   obs("a", "jobs.example.com/p/1", true),
		"ftp":         obs("a", "ftp://jobs.example.com/p/1", true),
		"no entry":    obs("", "https://jobs.example.com/p/1", true),
		"blank entry": obs("  ", "https://jobs.example.com/p/1", true),
	} {
		if _, err := l.RecordObserved(context.Background(), o); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: %v", name, err)
		}
	}
}

// The banner quotes a confirmed submission over a later unconfirmed one, the
// most recent among equals, and never a row that did not submit.
func TestPriorPrefersConfirmedThenMostRecent(t *testing.T) {
	l, st := open(t)
	ctx := context.Background()
	const page = "https://jobs.example.com/p/9"
	if _, ok, err := l.PriorFor(ctx, page); ok || err != nil {
		t.Fatalf("a posting with no row: %v %v", ok, err)
	}
	at := func(day int) time.Time { return time.Date(2027, 3, day, 8, 0, 0, 0, time.UTC) }
	early := obs("a", page, false)
	early.ObservedAt = at(1)
	if _, err := l.RecordObserved(ctx, early); err != nil {
		t.Fatal(err)
	}
	p, ok, err := l.PriorFor(ctx, page)
	if err != nil || !ok || p.Confirmed || p.Source != SourceUserObserved || !p.ObservedAt.Equal(at(1)) {
		t.Fatalf("%+v %v %v", p, ok, err)
	}
	later := obs("b", page, false)
	later.ObservedAt = at(3)
	if _, err := l.RecordObserved(ctx, later); err != nil {
		t.Fatal(err)
	}
	if p, _, _ := l.PriorFor(ctx, page); !p.ObservedAt.Equal(at(3)) {
		t.Fatalf("the most recent unconfirmed row: %+v", p)
	}
	confirmed := obs("c", page, true)
	confirmed.ObservedAt = at(2)
	if _, err := l.RecordObserved(ctx, confirmed); err != nil {
		t.Fatal(err)
	}
	if err := st.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.Exec(`UPDATE submissions SET state = 'may-have-submitted', confirmed = 1, observed_at = '2027-03-09T08:00:00.000Z' WHERE entry_id = 'b'`)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if p, _, _ := l.PriorFor(ctx, page); !p.Confirmed || !p.ObservedAt.Equal(at(2)) {
		t.Fatalf("the confirmed submitted row wins: %+v", p)
	}
}
