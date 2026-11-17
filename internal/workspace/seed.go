package workspace

import (
	"bytes"
	"embed"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
)

//go:embed templates
var templates embed.FS

func templateBytes(f File) []byte {
	b, err := templates.ReadFile("templates/" + f.Name())
	if err != nil {
		return nil
	}
	return b
}

// InitResult reports one seeded file.
type InitResult struct {
	Name    string
	Created bool
}

// Init creates the workspace and state directories and seeds every user-owned
// file that does not exist yet. It never overwrites, renames or modifies an
// existing file and is safe to repeat.
func (w *Workspace) Init() ([]InitResult, error) {
	dirs := []string{w.Root, w.StateDir()}
	for _, d := range userDirs {
		dirs = append(dirs, filepath.Join(w.Root, d))
	}
	for _, d := range stateDirs {
		dirs = append(dirs, filepath.Join(w.StateDir(), d))
	}
	for _, d := range dirs {
		if err := os.MkdirAll(d, 0o700); err != nil {
			return nil, err
		}
	}
	var results []InitResult
	for _, f := range Files {
		if !f.Seeded() {
			continue
		}
		created, err := createExclusive(w.Path(f), templateBytes(f))
		if err != nil {
			return results, err
		}
		results = append(results, InitResult{Name: f.Name(), Created: created})
	}
	return results, nil
}

func createExclusive(path string, data []byte) (bool, error) {
	fh, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if errors.Is(err, fs.ErrExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if _, err := fh.Write(data); err != nil {
		_ = fh.Close()
		_ = os.Remove(path)
		return false, err
	}
	return true, fh.Close()
}

// IsTemplate reports whether data still equals the embedded template of f.
func IsTemplate(f File, data []byte) bool {
	t := templateBytes(f)
	return t != nil && bytes.Equal(t, data)
}
