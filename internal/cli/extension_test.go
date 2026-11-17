package cli

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"applysmith/internal/extension"
)

func TestExtensionExportWritesTheManifest(t *testing.T) {
	if _, err := extension.Files(); errors.Is(err, extension.ErrNotBuilt) {
		t.Skip("run make generate first")
	}
	te := newTestEnv(t)
	dir := filepath.Join(t.TempDir(), "ext")
	o := te.run(context.Background(), "extension", "export", dir)
	if o.code != 0 || !strings.Contains(o.stdout, "build") {
		t.Fatalf("export: %+v", o)
	}
	if _, err := os.Stat(filepath.Join(dir, "manifest.json")); err != nil {
		t.Fatal(err)
	}
	if again := te.run(context.Background(), "extension", "export", dir); again.code == 0 {
		t.Fatal("a second export into the same directory succeeded")
	}
	if bad := te.run(context.Background(), "extension"); bad.code != 2 {
		t.Fatalf("no arguments: %+v", bad)
	}
}
