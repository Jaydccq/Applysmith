package workspace

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func iCloudRoot(home string) string {
	return filepath.Join(home, "Library", "Mobile Documents", "com~apple~CloudDocs", "Applysmith")
}

func TestResolve_ICloudContainerIsRefusedWhicheverWayItIsNamed(t *testing.T) {
	home, cfg := tempHome(t)
	target := iCloudRoot(home)
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(home, "Shortcut")
	if err := os.Symlink(filepath.Dir(target), link); err != nil {
		t.Fatal(err)
	}
	recorded := t.TempDir()
	if err := RecordChoice(recorded, target); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name string
		opts ResolveOptions
	}{
		{"flag", ResolveOptions{Flag: target}},
		{"flag with tilde", ResolveOptions{Flag: "~/Library/Mobile Documents/com~apple~CloudDocs/Applysmith"}},
		{"environment", ResolveOptions{Getenv: env{EnvWorkspace: target}.get}},
		{"recorded choice", ResolveOptions{ConfigDir: recorded, Getenv: env{}.get}},
		{"symlink into the container", ResolveOptions{Flag: filepath.Join(link, "Applysmith")}},
		{"symlink to the container itself", ResolveOptions{Flag: link}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.opts.HomeDir = home
			if tt.opts.ConfigDir == "" {
				tt.opts.ConfigDir = cfg
			}
			_, err := Resolve(tt.opts)
			if err == nil || !strings.Contains(err.Error(), "iCloud") {
				t.Fatalf("err = %v", err)
			}
		})
	}
}

func TestResolve_ICloudCheckSeesThroughASymlinkedHome(t *testing.T) {
	base := t.TempDir()
	real := filepath.Join(base, "real")
	home := filepath.Join(base, "home-link")
	if err := os.MkdirAll(filepath.Join(real, "Library", "Mobile Documents"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(real, home); err != nil {
		t.Fatal(err)
	}
	_, err := Resolve(ResolveOptions{HomeDir: home, ConfigDir: t.TempDir(), Flag: filepath.Join(real, "Library", "Mobile Documents", "Applysmith")})
	if err == nil || !strings.Contains(err.Error(), "iCloud") {
		t.Fatalf("err = %v", err)
	}
}

func TestResolve_NetworkVolumeIsRefused(t *testing.T) {
	home, cfg := tempHome(t)
	for _, fsType := range []string{"smbfs", "nfs", "afpfs", "webdav", "cifs", "nfs4"} {
		t.Run(fsType, func(t *testing.T) {
			_, err := Resolve(ResolveOptions{HomeDir: home, ConfigDir: cfg, Flag: filepath.Join(home, "ws"), fsType: func(string) string { return fsType }})
			if err == nil || !strings.Contains(err.Error(), "local volume") {
				t.Fatalf("err = %v", err)
			}
		})
	}
	for _, fsType := range []string{"apfs", "hfs", "ext4", "exfat", ""} {
		if _, err := Resolve(ResolveOptions{HomeDir: home, ConfigDir: cfg, Flag: filepath.Join(home, "ws"), fsType: func(string) string { return fsType }}); err != nil {
			t.Errorf("%q refused: %v", fsType, err)
		}
	}
}

func TestResolve_VolumeCheckUsesTheNearestExistingAncestor(t *testing.T) {
	home, cfg := tempHome(t)
	var asked string
	_, err := Resolve(ResolveOptions{HomeDir: home, ConfigDir: cfg, Flag: filepath.Join(home, "new", "deeper", "ws"),
		fsType: func(p string) string { asked = p; return "apfs" }})
	if err != nil {
		t.Fatal(err)
	}
	if want, _ := filepath.EvalSymlinks(home); asked != want {
		t.Errorf("asked about %q, want %q", asked, want)
	}
}

func TestResolve_RealLocalDiskPasses(t *testing.T) {
	home, cfg := tempHome(t)
	if _, err := Resolve(ResolveOptions{HomeDir: home, ConfigDir: cfg, Flag: filepath.Join(home, "ws")}); err != nil {
		t.Fatalf("a temp directory is a local volume: %v", err)
	}
}
