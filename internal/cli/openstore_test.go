package cli

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"

	"applysmith/internal/store"
	"applysmith/internal/workspace"
)

// leaveACrashedPublish plants what a publish that died before its commit
// leaves: the journal and the file it names.
func leaveACrashedPublish(t *testing.T, root string) (orphan string) {
	t.Helper()
	body := []byte("# orphan\n")
	sum := sha256.Sum256(body)
	orphan = filepath.Join(root, "reports", "001-orphan.md")
	if err := os.MkdirAll(filepath.Dir(orphan), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(orphan, body, 0o600); err != nil {
		t.Fatal(err)
	}
	journal := filepath.Join(root, ".applysmith", "publish", "1-j_crashed.json")
	if err := os.MkdirAll(filepath.Dir(journal), 0o700); err != nil {
		t.Fatal(err)
	}
	doc := `[{"path":"reports/001-orphan.md","sha256":"` + hex.EncodeToString(sum[:]) + `"}]`
	if err := os.WriteFile(journal, []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}
	return orphan
}

func TestOpenStore_EveryWritableAccessSettlesACrashedPublish(t *testing.T) {
	for _, tt := range []struct {
		name   string
		access storeAccess
	}{{"shared", accessShared}, {"exclusive", accessExclusive}} {
		t.Run(tt.name, func(t *testing.T) {
			te := newTestEnv(t)
			te.addRow("Example Corp", "Platform Engineer", "https://example.com/jobs/1") // creates the workspace
			orphan := leaveACrashedPublish(t, te.ws)
			e := te.env(nil, nil)
			_, st, release, err := e.openStore(context.Background(), te.ws, tt.access)
			if err != nil {
				t.Fatal(err)
			}
			defer release()
			if _, err := os.Stat(orphan); !os.IsNotExist(err) {
				t.Errorf("the orphan file of a crashed publish survived the open: %v", err)
			}
			if err := st.Write(context.Background(), func(tx *sql.Tx) error { return nil }); err != nil {
				t.Errorf("not writable: %v", err)
			}
		})
	}
}

func TestOpenStore_ReadOnlyNeitherRecoversNorWrites(t *testing.T) {
	te := newTestEnv(t)
	te.addRow("Example Corp", "Platform Engineer", "https://example.com/jobs/1")
	orphan := leaveACrashedPublish(t, te.ws)
	_, st, release, err := te.env(nil, nil).openStore(context.Background(), te.ws, accessRead)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	if _, err := os.Stat(orphan); err != nil {
		t.Errorf("a read-only open touched the files: %v", err)
	}
	if err := st.Write(context.Background(), func(tx *sql.Tx) error { return nil }); err == nil {
		t.Error("a read-only open allowed a write")
	}
}

func TestOpenStore_SharedOnAFreshWorkspaceMigratesUnderTheLockThenJoins(t *testing.T) {
	te := newTestEnv(t)
	_, st, release, err := te.env(nil, nil).openStore(context.Background(), te.ws, accessShared)
	if err != nil {
		t.Fatal(err)
	}
	release()
	if _, err := os.Stat(filepath.Join(workspace.Open(te.ws).StateDir(), store.DBFile)); err != nil {
		t.Errorf("the database was not created: %v", err)
	}
	_ = st
	if o := te.tracker("list"); o.code != 0 {
		t.Errorf("%+v", o)
	}
}
