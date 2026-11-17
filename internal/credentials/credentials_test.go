package credentials

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"applysmith/internal/gates"
	"applysmith/internal/store"
	"applysmith/internal/vault"
)

// refusing is a keychain that refuses one account and serves the rest.
type refusing struct {
	vault.Backend
	account string
}

func (r refusing) Get(account string) (string, error) {
	if account == r.account {
		return "", vault.ErrDenied
	}
	return r.Backend.Get(account)
}

func setup(t *testing.T, backend func(vault.Backend) vault.Backend) (*Service, *vault.Vault, *store.Store) {
	t.Helper()
	now := func() time.Time { return time.Date(2027, 3, 10, 9, 0, 0, 0, time.UTC) }
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "applysmith.db"), store.Options{Now: now})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	mem := vault.NewMemory()
	var b vault.Backend = mem
	if backend != nil {
		b = backend(mem)
	}
	v, err := vault.New("ws", b, vault.WithGetenv(func(string) string { return "" }))
	if err != nil {
		t.Fatal(err)
	}
	return New(st, v, now), v, st
}

func index(t *testing.T, st *store.Store, name, kind, host, email string) {
	t.Helper()
	err := st.Write(context.Background(), func(tx *sql.Tx) error {
		_, err := tx.Exec(`INSERT INTO credential_index (name, kind, host, email, has_secret, created_at, updated_at) VALUES (?, ?, NULLIF(?, ''), ?, 1, 'x', 'x')`, name, kind, host, email)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestADeniedOverrideDoesNotFallThroughToTheSharedDefault(t *testing.T) {
	c, v, st := setup(t, func(b vault.Backend) vault.Backend {
		return refusing{Backend: b, account: "ws:signin-site-careers.example.test"}
	})
	if _, err := v.Set("signin-default", "Default-Pw-1!x"); err != nil {
		t.Fatal(err)
	}
	index(t, st, "signin-default", "signin_default", "", "alex@example.com")
	_, err := c.Resolve(context.Background(), "careers.example.test")
	if !vault.IsCode(err, vault.CodeKeychainDenied) {
		t.Fatalf("got %v", err)
	}
	// A host that has no override reads the default.
	got, err := c.Resolve(context.Background(), "jobs.example.test")
	if err != nil || got.Source != SourceDefault {
		t.Fatalf("%+v %v", got, err)
	}
}

func TestAHostThatCannotNameAnOverrideReadsTheDefault(t *testing.T) {
	c, v, st := setup(t, nil)
	if _, err := v.Set("signin-default", "Default-Pw-1!x"); err != nil {
		t.Fatal(err)
	}
	index(t, st, "signin-default", "signin_default", "", "alex@example.com")
	got, err := c.Resolve(context.Background(), "::1")
	if err != nil || got.Source != SourceDefault {
		t.Fatalf("%+v %v", got, err)
	}
}

func TestNothingStoredIsErrNotFound(t *testing.T) {
	c, _, _ := setup(t, nil)
	if _, err := c.Resolve(context.Background(), "jobs.example.test"); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
}

func TestNoHoldRecordMeansNoHold(t *testing.T) {
	c, _, _ := setup(t, nil)
	if h, err := c.Held(context.Background(), "jobs.example.test"); err != nil || h.Held {
		t.Fatalf("%+v %v", h, err)
	}
}

func TestARecordedHoldLastsTheLocalDayAndKeepsALockoutFlag(t *testing.T) {
	c, _, st := setup(t, nil)
	ctx := context.Background()
	record := func(lockout bool) {
		t.Helper()
		if err := st.Write(ctx, func(tx *sql.Tx) error { return c.RecordExhausted(ctx, tx, "Careers.Example.Test", lockout, "r1") }); err != nil {
			t.Fatal(err)
		}
	}
	record(true)
	record(false)
	h, err := c.Held(ctx, "careers.example.test")
	if err != nil || !h.Held || !h.Lockout {
		t.Fatalf("%+v %v", h, err)
	}
	// The hold carries the stored facts, so the password gate reaches the same
	// verdict from them.
	if d := gates.PasswordSubmission(h.Facts); !h.Facts.Prior.Recorded || !h.Facts.Prior.Lockout || h.Facts.Prior.At == 0 || h.Facts.Prior.Day != h.Facts.TodayLocal || d.Allowed() || d.Code != gates.CodeHostExhaustedToday {
		t.Fatalf("facts %+v decide %+v", h.Facts, d)
	}
	c.now = func() time.Time { return time.Date(2027, 3, 11, 0, 0, 1, 0, time.UTC) }
	if h, err := c.Held(ctx, "careers.example.test"); err != nil || h.Held || !gates.PasswordSubmission(h.Facts).Allowed() {
		t.Fatalf("next day %+v %v", h, err)
	}
	// A new day's record replaces yesterday's flag.
	record(false)
	if h, err := c.Held(ctx, "careers.example.test"); err != nil || !h.Held || h.Lockout {
		t.Fatalf("%+v %v", h, err)
	}
}
