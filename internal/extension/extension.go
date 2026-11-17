// Package extension holds the unpacked browser extension as an embedded file
// tree. The tree is built by `go generate ./internal/extension` (make
// generate), is not committed, and is empty on a fresh checkout.
package extension

//go:generate go run gen.go

import (
	"bytes"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

//go:embed all:generated
var generated embed.FS

// ErrNotBuilt means the extension was not generated into this binary.
var ErrNotBuilt = errors.New("the extension is not built into this binary: run make generate")

// Files lists the embedded files in slash form, sorted by name.
func Files() ([]string, error) {
	var names []string
	err := fs.WalkDir(generated, "generated", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || d.Name() == ".gitkeep" {
			return err
		}
		names = append(names, strings.TrimPrefix(p, "generated/"))
		return nil
	})
	if err == nil && len(names) == 0 {
		return nil, ErrNotBuilt
	}
	return names, err
}

// BuildInfo names the embedded extension: the version and the build stamp its
// version name carries on either side of the plus sign. The manifest version
// adds the stamp's build number, so it is not the version shown.
type BuildInfo struct{ Version, Stamp string }

// Build reads the embedded manifest.
func Build() (BuildInfo, error) {
	b, err := generated.ReadFile("generated/manifest.json")
	if err != nil {
		return BuildInfo{}, ErrNotBuilt
	}
	var m struct {
		Version     string `json:"version"`
		VersionName string `json:"version_name"`
	}
	if err := json.Unmarshal(b, &m); err != nil {
		return BuildInfo{}, err
	}
	base, st, ok := strings.Cut(m.VersionName, "+")
	if !ok || base == "" || m.Version == "" {
		return BuildInfo{}, errors.New("extension manifest has no build stamp")
	}
	return BuildInfo{Version: base, Stamp: st}, nil
}

// Stamp is the build stamp in the embedded manifest's version name.
func Stamp() (string, error) {
	b, err := Build()
	return b.Stamp, err
}

// Export writes the unpacked extension into dir, creating it. It refuses a
// non-empty directory so an older export is never mixed with a newer one.
func Export(dir string) error {
	names, err := Files()
	if err != nil {
		return err
	}
	if entries, err := os.ReadDir(dir); err == nil && len(entries) > 0 {
		return fmt.Errorf("extension export: %s is not empty", dir)
	}
	for _, n := range names {
		data, err := generated.ReadFile("generated/" + n)
		if err != nil {
			return err
		}
		dst := filepath.Join(dir, filepath.FromSlash(n))
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(dst, data, 0o644); err != nil {
			return err
		}
	}
	return nil
}

// Keep makes dir hold exactly the embedded extension, for a browser that
// loads it from there. A folder whose manifest is this build's is left alone;
// any other is replaced whole by an export written beside it and renamed into
// place, so the browser never reads two builds mixed. It reports whether it
// wrote the folder.
func Keep(dir string) (bool, error) {
	want, err := generated.ReadFile("generated/manifest.json")
	if err != nil {
		return false, ErrNotBuilt
	}
	if have, err := os.ReadFile(filepath.Join(dir, "manifest.json")); err == nil && bytes.Equal(have, want) {
		return false, nil
	}
	parent := filepath.Dir(dir)
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return false, err
	}
	next, err := os.MkdirTemp(parent, ".extension-")
	if err != nil {
		return false, err
	}
	defer os.RemoveAll(next)
	if err := Export(next); err != nil {
		return false, err
	}
	old := next + ".old"
	if err := os.Rename(dir, old); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return false, err
	}
	if err := os.Rename(next, dir); err != nil {
		_ = os.Rename(old, dir)
		return false, err
	}
	return true, os.RemoveAll(old)
}
