package workspace

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type env map[string]string

func (e env) get(k string) string { return e[k] }

func tempHome(t *testing.T) (home, cfg string) {
	t.Helper()
	base := t.TempDir()
	home = filepath.Join(base, "home")
	cfg = filepath.Join(base, "config")
	for _, d := range []string{home, cfg} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return home, cfg
}

func mustResolve(t *testing.T, o ResolveOptions) string {
	t.Helper()
	got, err := Resolve(o)
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func TestResolutionPrecedence(t *testing.T) {
	home, cfg := tempHome(t)
	flagDir := filepath.Join(home, "flagdir")
	envDir := filepath.Join(home, "envdir")
	recDir := filepath.Join(home, "recdir")
	if err := RecordChoice(cfg, recDir); err != nil {
		t.Fatal(err)
	}
	base := ResolveOptions{HomeDir: home, ConfigDir: cfg}

	tests := []struct {
		name string
		opts func(o ResolveOptions) ResolveOptions
		want string
	}{
		{"flag beats environment", func(o ResolveOptions) ResolveOptions {
			o.Flag, o.Getenv = flagDir, env{EnvWorkspace: envDir}.get
			return o
		}, flagDir},
		{"environment beats settings file", func(o ResolveOptions) ResolveOptions {
			o.Getenv = env{EnvWorkspace: envDir}.get
			return o
		}, envDir},
		{"settings file beats default", func(o ResolveOptions) ResolveOptions { o.Getenv = env{}.get; return o }, recDir},
		{"tilde expands", func(o ResolveOptions) ResolveOptions { o.Flag = "~/notes"; return o }, filepath.Join(home, "notes")},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := mustResolve(t, tc.opts(base)); got != tc.want {
				t.Fatalf("got %s want %s", got, tc.want)
			}
		})
	}
}

func TestDefaultLocation(t *testing.T) {
	home, cfg := tempHome(t)
	got := mustResolve(t, ResolveOptions{HomeDir: home, ConfigDir: cfg, Getenv: env{}.get})
	if got != filepath.Join(home, "Applysmith") {
		t.Fatalf("got %s", got)
	}
	if _, err := os.Stat(got); err == nil {
		t.Fatal("Resolve must not create the workspace")
	}
}

func TestInvalidWorkspaces(t *testing.T) {
	home, cfg := tempHome(t)
	file := filepath.Join(home, "afile")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	checkout := filepath.Join(home, "src", "applysmith")
	if err := os.MkdirAll(filepath.Join(checkout, "internal"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(checkout, "go.mod"), []byte("module applysmith\n\ngo 1.27\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	otherProject := filepath.Join(home, "src", "other")
	if err := os.MkdirAll(otherProject, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(otherProject, "go.mod"), []byte("module example.com/other\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	readonly := filepath.Join(home, "ro")
	if err := os.Mkdir(readonly, 0o500); err != nil {
		t.Fatal(err)
	}
	volumes := filepath.Join(home, "Volumes")
	if err := os.MkdirAll(volumes, 0o755); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name string
		flag string
		rule string // substring of the failed rule; empty means valid
	}{
		{"home directory", home, "home directory"},
		{"home via tilde", "~", "home directory"},
		{"filesystem root", "/", "filesystem root"},
		{"relative", "relative/dir", "not absolute"},
		{"not a directory", file, "not a directory"},
		{"inside source checkout", filepath.Join(checkout, "internal", "ws"), "source checkout"},
		{"checkout itself", checkout, "source checkout"},
		{"unmounted volume", filepath.Join(volumes, "Backup", "Applysmith"), "not mounted"},
		{"unrelated go project is fine", filepath.Join(otherProject, "ws"), ""},
		{"creatable under existing parent", filepath.Join(home, "new", "deep", "ws"), ""},
	}
	if os.Getuid() != 0 {
		tests = append(tests, struct {
			name, flag, rule string
		}{"read-only parent", filepath.Join(readonly, "ws"), "not writable"})
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Resolve(ResolveOptions{Flag: tc.flag, HomeDir: home, ConfigDir: cfg, Getenv: env{}.get, volumes: volumes})
			if tc.rule == "" {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			if !IsCode(err, CodeWorkspaceInvalid) {
				t.Fatalf("err = %v", err)
			}
			if msg := err.(*Error).Message; !strings.Contains(msg, tc.rule) {
				t.Fatalf("message %q lacks %q", msg, tc.rule)
			}
		})
	}
}

// With no directory given the recorded choice is read from the user's own
// configuration directory, where Resolve reads it.
func TestRecordedChoiceDefaultsToTheUserConfigDir(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))
	cfg, err := os.UserConfigDir()
	if err != nil {
		t.Fatal(err)
	}
	if err := RecordChoice(cfg, "/some/where"); err != nil {
		t.Fatal(err)
	}
	if got, err := RecordedChoice(""); err != nil || got != "/some/where" {
		t.Fatalf("recorded = %q, %v", got, err)
	}
}

func TestCorruptSettingsFileIsWorkspaceInvalid(t *testing.T) {
	home, cfg := tempHome(t)
	if err := os.MkdirAll(filepath.Join(cfg, "applysmith"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cfg, "applysmith", "workspace.json"), []byte("{nope"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := Resolve(ResolveOptions{HomeDir: home, ConfigDir: cfg, Getenv: env{}.get})
	if !IsCode(err, CodeWorkspaceInvalid) {
		t.Fatalf("err = %v", err)
	}
}

func TestConfiguredChoiceDoesNotCreateADefaultAndHonorsPrecedence(t *testing.T) {
	home, cfg := tempHome(t)
	opts := ResolveOptions{HomeDir: home, ConfigDir: cfg, Getenv: env{}.get}
	if yes, err := HasConfiguredChoice(opts); yes || err != nil {
		t.Fatalf("fresh choice: %v %v", yes, err)
	}
	root := filepath.Join(home, "Applysmith")
	if _, err := os.Stat(root); !os.IsNotExist(err) {
		t.Fatal("checking for a choice created the default workspace")
	}
	opts.Flag = root
	if yes, err := HasConfiguredChoice(opts); !yes || err != nil {
		t.Fatal("explicit choice was ignored")
	}
	opts.Flag, opts.Getenv = "", env{EnvWorkspace: root}.get
	if yes, err := HasConfiguredChoice(opts); !yes || err != nil {
		t.Fatal("environment choice was ignored")
	}
	opts.Getenv = env{}.get
	if err := RecordChoice(cfg, root); err != nil {
		t.Fatal(err)
	}
	if yes, err := HasConfiguredChoice(opts); !yes || err != nil {
		t.Fatal("recorded choice was ignored")
	}
}
