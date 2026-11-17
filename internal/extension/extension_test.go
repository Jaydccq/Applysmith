package extension

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func built(t *testing.T) {
	t.Helper()
	if _, err := Files(); errors.Is(err, ErrNotBuilt) {
		t.Skip("run make generate first")
	}
}

func TestExport_WritesTheUnpackedExtension(t *testing.T) {
	built(t)
	dir := filepath.Join(t.TempDir(), "ext")
	if err := Export(dir); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(dir, "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	var m struct {
		VersionName string `json:"version_name"`
		Background  struct {
			Worker string `json:"service_worker"`
		} `json:"background"`
	}
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	st, err := Stamp()
	if err != nil || !strings.HasSuffix(m.VersionName, "+"+st) {
		t.Fatalf("version_name %q, stamp %q, err %v", m.VersionName, st, err)
	}
	if _, err := os.Stat(filepath.Join(dir, m.Background.Worker)); err != nil {
		t.Fatalf("the service worker the manifest names is not exported: %v", err)
	}
}

// The app keeps one unpacked copy for Chrome to load: Keep leaves a folder of
// this build alone and replaces a missing folder or another build's whole, so
// Chrome never reads two builds mixed (as Export never does).
func TestKeep_HoldsExactlyTheEmbeddedBuild(t *testing.T) {
	built(t)
	want, err := generated.ReadFile("generated/manifest.json")
	if err != nil {
		t.Fatal(err)
	}
	write := func(t *testing.T, dir, name, body string) {
		t.Helper()
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for _, tc := range []struct {
		name  string
		seed  func(t *testing.T, dir string)
		wrote bool
	}{
		{name: "no folder yet", seed: func(*testing.T, string) {}, wrote: true},
		{name: "another build", seed: func(t *testing.T, dir string) {
			write(t, dir, "manifest.json", `{"version":"0.0.1","version_name":"0.0.1+0000000000000000"}`)
			write(t, dir, "retired.js", "old")
		}, wrote: true},
		{name: "this build", seed: func(t *testing.T, dir string) {
			if err := Export(dir); err != nil {
				t.Fatal(err)
			}
			write(t, dir, "kept.txt", "x")
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "Applysmith", "extension")
			tc.seed(t, dir)
			wrote, err := Keep(dir)
			if err != nil || wrote != tc.wrote {
				t.Fatalf("wrote %v, err %v; want wrote %v", wrote, err, tc.wrote)
			}
			if got, err := os.ReadFile(filepath.Join(dir, "manifest.json")); err != nil || string(got) != string(want) {
				t.Fatalf("the folder's manifest is not this build's: %v", err)
			}
			if _, err := os.Stat(filepath.Join(dir, "retired.js")); err == nil {
				t.Error("a file of the other build is still in the folder")
			}
			if _, err := os.Stat(filepath.Join(dir, "kept.txt")); (err == nil) == tc.wrote {
				t.Errorf("wrote %v, but the file added to the folder is there: %v", tc.wrote, err == nil)
			}
			if beside, _ := os.ReadDir(filepath.Dir(dir)); len(beside) != 1 {
				t.Errorf("Keep left %d entries where the folder lives, want only the folder", len(beside))
			}
		})
	}
}

func TestExport_RefusesANonEmptyDirectory(t *testing.T) {
	built(t)
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "old.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := Export(dir); err == nil {
		t.Fatal("export into a non-empty directory succeeded")
	}
}
