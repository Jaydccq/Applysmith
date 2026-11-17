package toolchain

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSetup_NoSupportedPython(t *testing.T) {
	dir := t.TempDir()
	root := Root(filepath.Join(dir, "root"))
	if err := Setup(context.Background(), Config{Root: root, SearchDirs: []string{dir}}); !errors.Is(err, ErrNoPython) {
		t.Fatalf("no interpreter: err = %v", err)
	}
	old := filepath.Join(dir, "bin")
	if err := os.MkdirAll(old, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(old, "python3"), []byte("#!/bin/sh\necho 3.9.6\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := Setup(context.Background(), Config{Root: root, SearchDirs: []string{old}}); !errors.Is(err, ErrNoPython) {
		t.Fatalf("Python 3.9: err = %v", err)
	}
	if _, err := os.Stat(root.venv()); !os.IsNotExist(err) {
		t.Error("a failed setup must not leave a venv behind")
	}
}

func TestMinimalEnvCarriesNoCredentials(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "x")
	t.Setenv("TEXINPUTS", "/elsewhere")
	for _, kv := range Root("/r").minimalEnv("/bin") {
		if strings.HasPrefix(kv, "OPENAI") || strings.HasPrefix(kv, "TEXINPUTS") {
			t.Errorf("environment leaks %s", kv)
		}
	}
	env := strings.Join(Root("/r").minimalEnv(), "\n")
	for _, want := range []string{"HOME=/r/home", "TEXMFVAR=/r/texmf-var", "PYTHONNOUSERSITE=1"} {
		if !strings.Contains(env, want) {
			t.Errorf("environment lacks %s", want)
		}
	}
}
