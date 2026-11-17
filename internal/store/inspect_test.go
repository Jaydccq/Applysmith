package store

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
)

func TestInspectMigrations(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "applysmith.db")

	st, err := InspectMigrations(ctx, path)
	if err != nil || st.Exists || !st.Pending() || st.Supported != SchemaVersion() {
		t.Fatalf("missing database: %+v, %v", st, err)
	}
	s, err := Open(ctx, path, Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	st, err = InspectMigrations(ctx, path)
	if err != nil || !st.Exists || st.Pending() || st.Applied != SchemaVersion() {
		t.Fatalf("open database: %+v, %v", st, err)
	}
	// The open store's own readers give the same answer.
	if own, err := s.Migrations(ctx); err != nil || own != st {
		t.Fatalf("through the store: %+v, %v; want %+v", own, err, st)
	}
	if _, err := s.rw.ExecContext(ctx, `INSERT INTO schema_migrations (version, name, checksum, applied_at) VALUES (999, 'future', 'x', 'now')`); err != nil {
		t.Fatal(err)
	}
	if _, err := InspectMigrations(ctx, path); !errors.Is(err, ErrSchemaTooNew) {
		t.Fatalf("newer schema: %v", err)
	}
	if _, err := s.Migrations(ctx); !errors.Is(err, ErrSchemaTooNew) {
		t.Fatalf("newer schema through the store: %v", err)
	}
}
