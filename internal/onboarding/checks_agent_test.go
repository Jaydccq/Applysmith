package onboarding

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func fakePiBinary(t *testing.T, version string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "pi")
	if err := os.WriteFile(path, []byte("#!/bin/sh\necho "+version+"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestPiCheck_ComparesTheInstalledVersionWithThePin(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the stand-in executable is a shell script")
	}
	env, _ := newEnv(t)

	env.Getenv = func(k string) string {
		if k == "APPLYSMITH_PI_PATH" {
			return fakePiBinary(t, "1.0.4")
		}
		return ""
	}
	if r := run(t, env, "agent.pi"); r.Status != Pass || !strings.Contains(r.Summary, "1.0.4") || r.Required {
		t.Errorf("pinned version: %+v", r)
	}

	env.Getenv = func(k string) string {
		if k == "APPLYSMITH_PI_PATH" {
			return fakePiBinary(t, "0.83.0")
		}
		return ""
	}
	r := run(t, env, "agent.pi")
	if r.Status != Warn || !strings.Contains(r.Summary, "0.83.0") || !strings.Contains(r.Summary, "1.0.4") || !strings.Contains(r.Fix, "github.com/earendil-works/pi/releases") {
		t.Errorf("other version: %+v", r)
	}

	env.Getenv = func(k string) string {
		if k == "APPLYSMITH_PI_PATH" {
			return filepath.Join(t.TempDir(), "missing")
		}
		return ""
	}
	if r := run(t, env, "agent.pi"); r.Status != Warn || r.Fix == "" {
		t.Errorf("missing binary: %+v", r)
	}
}
