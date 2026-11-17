package main

import (
	"encoding/json"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// maxBundleBytes is the packaged-app bound of the desktop-shell capability.
const maxBundleBytes = 80_000_000

// TestPackagedBundle checks a built bundle against the Packaging requirement.
// `make package` builds the bundle and runs it with APPLYSMITH_APP_BUNDLE set;
// without that variable there is nothing to inspect.
func TestPackagedBundle(t *testing.T) {
	bundle := os.Getenv("APPLYSMITH_APP_BUNDLE")
	if bundle == "" {
		t.Skip("APPLYSMITH_APP_BUNDLE names the bundle to inspect (make package)")
	}
	t.Run("size bound", func(t *testing.T) {
		var total int64
		err := filepath.WalkDir(bundle, func(_ string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return err
			}
			info, err := d.Info()
			if err == nil {
				total += info.Size()
			}
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
		t.Logf("bundle size %d bytes", total)
		if total > maxBundleBytes {
			t.Errorf("bundle is %d bytes, over the %d byte bound", total, maxBundleBytes)
		}
	})
	t.Run("notices shipped", func(t *testing.T) {
		for _, name := range []string{"NOTICE", "NOTICE-dependencies.txt", "licenses"} {
			if _, err := os.Stat(filepath.Join(bundle, "Contents", "Resources", name)); err != nil {
				t.Errorf("missing %s: %v", name, err)
			}
		}
	})
	t.Run("own icon", func(t *testing.T) {
		icns, _ := filepath.Glob(filepath.Join(bundle, "Contents", "Resources", "*.icns"))
		if len(icns) != 1 {
			t.Fatalf("want exactly one .icns in the bundle, found %v", icns)
		}
		if fi, err := os.Stat(icns[0]); err != nil || fi.Size() < 10_000 {
			t.Errorf("%s is missing or too small to be the app icon: %v", icns[0], err)
		}
	})
	t.Run("nothing the setup guide installs", func(t *testing.T) {
		banned := []string{"chrome", "chromium", "node", "python", "pdftoppm", "pdftotext", "pdflatex", "xelatex", "lualatex", "latexmk", "venv", "site-packages"}
		err := filepath.WalkDir(bundle, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			name := strings.ToLower(d.Name())
			for _, b := range banned {
				if name == b || strings.HasPrefix(name, b+".") || strings.HasPrefix(name, b+"-") {
					t.Errorf("%s does not belong in the bundle", p)
				}
			}
			if ext := filepath.Ext(name); ext == ".db" || ext == ".sqlite" || ext == ".sqlite3" {
				t.Errorf("%s: the bundle must hold no database", p)
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	})
	t.Run("runs as the applysmith command", func(t *testing.T) { packagedBundleRunsAsCLI(t, bundle) })
	t.Run("stable identifier", func(t *testing.T) {
		if runtime.GOOS != "darwin" {
			t.Skip("codesign is macOS only")
		}
		raw, err := os.ReadFile("mygo.json")
		if err != nil {
			t.Fatal(err)
		}
		var cfg struct {
			Identifier string `json:"identifier"`
		}
		if err := json.Unmarshal(raw, &cfg); err != nil {
			t.Fatal(err)
		}
		out, err := exec.Command("codesign", "-dv", bundle).CombinedOutput()
		if err != nil {
			t.Fatalf("codesign: %v\n%s", err, out)
		}
		if !strings.Contains(string(out), "Identifier="+cfg.Identifier+"\n") {
			t.Errorf("codesign does not report identifier %s:\n%s", cfg.Identifier, out)
		}
		if out, err := exec.Command("codesign", "--verify", "--deep", "--strict", bundle).CombinedOutput(); err != nil {
			t.Errorf("signature does not verify: %v\n%s", err, out)
		}
	})
}
