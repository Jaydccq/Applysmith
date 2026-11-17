package workspace

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// SourceModule is the module path of the Applysmith source tree; a workspace
// inside a directory tree whose go.mod declares it is refused.
const SourceModule = "applysmith"

const (
	// EnvWorkspace overrides the recorded workspace, never the flag.
	EnvWorkspace = "APPLYSMITH_WORKSPACE"
	defaultDir   = "Applysmith"
	settingsDir  = "applysmith"
	settingsFile = "workspace.json"
)

// ResolveOptions carries every outside input, so tests need no real home.
type ResolveOptions struct {
	Flag      string              // --workspace value, empty when absent
	Getenv    func(string) string // defaults to os.Getenv
	HomeDir   string              // defaults to os.UserHomeDir
	ConfigDir string              // user configuration directory; defaults to os.UserConfigDir
	volumes   string              // overrides "/Volumes" in tests
	fsType    func(string) string // overrides the filesystem probe in tests
}

func (o *ResolveOptions) fill() error {
	if o.Getenv == nil {
		o.Getenv = os.Getenv
	}
	if o.HomeDir == "" {
		h, err := os.UserHomeDir()
		if err != nil {
			return err
		}
		o.HomeDir = h
	}
	if o.ConfigDir == "" {
		d, err := os.UserConfigDir()
		if err != nil {
			return err
		}
		o.ConfigDir = d
	}
	if o.volumes == "" {
		o.volumes = "/Volumes"
	}
	if o.fsType == nil {
		o.fsType = filesystemType
	}
	return nil
}

// Resolve picks the workspace by precedence (flag, environment, recorded
// settings, ~/Applysmith) and checks it against the workspace rules. It creates
// nothing; `applysmith init` does.
func Resolve(opts ResolveOptions) (string, error) {
	if err := opts.fill(); err != nil {
		return "", err
	}
	raw := opts.Flag
	if raw == "" {
		raw = opts.Getenv(EnvWorkspace)
	}
	if raw == "" {
		recorded, err := readSettings(opts.ConfigDir)
		if err != nil {
			return "", err
		}
		raw = recorded
	}
	if raw == "" {
		raw = filepath.Join(opts.HomeDir, defaultDir)
	}
	path := expandHome(raw, opts.HomeDir)
	if err := checkRules(path, opts); err != nil {
		return "", err
	}
	return path, nil
}

// RecordChoice writes the per-user settings file; it holds the path only.
func RecordChoice(configDir, path string) error {
	dir := filepath.Join(configDir, settingsDir)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	b, err := json.Marshal(struct {
		Path string `json:"path"`
	}{path})
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, settingsFile), append(b, '\n'), 0o600)
}

// RecordedChoice is the workspace the per-user settings file names, or ""
// when none is recorded. An empty configDir is the user's own configuration
// directory, as in Resolve.
func RecordedChoice(configDir string) (string, error) {
	if configDir == "" {
		d, err := os.UserConfigDir()
		if err != nil {
			return "", err
		}
		configDir = d
	}
	return readSettings(configDir)
}

func readSettings(configDir string) (string, error) {
	b, err := os.ReadFile(filepath.Join(configDir, settingsDir, settingsFile))
	if errors.Is(err, fs.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	var s struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal(b, &s); err != nil {
		return "", newError(CodeWorkspaceInvalid, filepath.Join(configDir, settingsDir, settingsFile), "settings file is not valid JSON")
	}
	return s.Path, nil
}

func expandHome(p, home string) string {
	switch {
	case p == "~":
		return home
	case strings.HasPrefix(p, "~/"):
		return filepath.Join(home, p[2:])
	}
	return p
}

func invalid(path, rule string) error {
	return newError(CodeWorkspaceInvalid, path, "%s", rule)
}

func checkRules(path string, opts ResolveOptions) error {
	if !filepath.IsAbs(path) {
		return invalid(path, "path is not absolute")
	}
	path = filepath.Clean(path)
	if filepath.Dir(path) == path {
		return invalid(path, "filesystem root")
	}
	if sameDir(path, opts.HomeDir) {
		return invalid(path, "home directory")
	}
	if root, ok := sourceCheckout(path); ok {
		return invalid(path, "inside the Applysmith source checkout at "+root)
	}
	if vol, ok := unmountedVolume(path, opts.volumes); ok {
		return invalid(path, "volume "+vol+" is not mounted")
	}
	real, anchor := resolveExisting(path)
	if err := checkSynchronised(path, real, opts.HomeDir); err != nil {
		return err
	}
	if t := opts.fsType(anchor); networkFilesystems[t] {
		return invalid(path, "not on a local volume ("+t+"); a network share cannot hold the write-ahead-logged database safely")
	}
	return checkWritable(path)
}

// networkFilesystems are the filesystem names that are shares, not local disks.
var networkFilesystems = map[string]bool{
	"smbfs": true, "smb": true, "smb2": true, "smb3": true, "cifs": true,
	"nfs": true, "nfs4": true, "afpfs": true, "webdav": true,
}

// iCloudContainer is the synchronised folder a workspace must not live in.
const iCloudContainer = "Library/Mobile Documents"

// checkSynchronised refuses a path inside iCloud Drive's container, judged on
// the path as given and as the filesystem resolves it, so neither a symlink
// nor a symlinked home directory gets around the rule.
func checkSynchronised(path, real, home string) error {
	homes := []string{filepath.Clean(home)}
	if r, err := filepath.EvalSymlinks(home); err == nil {
		homes = append(homes, r)
	}
	for _, h := range homes {
		container := filepath.Join(h, iCloudContainer)
		for _, p := range []string{path, real} {
			if rel, err := filepath.Rel(container, p); err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
				return invalid(path, "inside iCloud Drive's container ("+container+"), which corrupts a write-ahead-logged database; choose a folder outside it, such as "+filepath.Join(home, defaultDir))
			}
		}
	}
	return nil
}

// resolveExisting resolves symlinks in the longest existing prefix of path and
// returns the whole path so resolved, plus that existing directory.
func resolveExisting(path string) (resolved, anchor string) {
	rest := ""
	for probe := path; ; probe = filepath.Dir(probe) {
		if r, err := filepath.EvalSymlinks(probe); err == nil {
			return filepath.Join(r, rest), r
		}
		rest = filepath.Join(filepath.Base(probe), rest)
		if filepath.Dir(probe) == probe {
			return path, path
		}
	}
}

func sameDir(a, b string) bool {
	ra, errA := filepath.EvalSymlinks(a)
	rb, errB := filepath.EvalSymlinks(b)
	if errA != nil || errB != nil {
		return filepath.Clean(a) == filepath.Clean(b)
	}
	return ra == rb
}

// unmountedVolume reports a path under the volumes directory whose volume
// directory is absent, so the workspace is never created on the boot disk by
// accident.
func unmountedVolume(path, volumes string) (string, bool) {
	rest, ok := strings.CutPrefix(path, volumes+string(filepath.Separator))
	if !ok {
		return "", false
	}
	name, _, _ := strings.Cut(rest, string(filepath.Separator))
	vol := filepath.Join(volumes, name)
	if _, err := os.Stat(vol); err != nil {
		return vol, true
	}
	return "", false
}

// sourceCheckout finds an ancestor (or the path itself) holding the Applysmith go.mod.
func sourceCheckout(path string) (string, bool) {
	for dir := path; ; dir = filepath.Dir(dir) {
		if b, err := os.ReadFile(filepath.Join(dir, "go.mod")); err == nil && declaresModule(b, SourceModule) {
			return dir, true
		}
		if filepath.Dir(dir) == dir {
			return "", false
		}
	}
}

func declaresModule(gomod []byte, module string) bool {
	for _, line := range strings.Split(string(gomod), "\n") {
		if rest, ok := strings.CutPrefix(strings.TrimSpace(line), "module"); ok {
			return strings.Trim(strings.TrimSpace(rest), `"`) == module
		}
	}
	return false
}

// checkWritable requires the path, or the nearest existing ancestor it would be
// created under, to be a writable directory.
func checkWritable(path string) error {
	probe := path
	for {
		info, err := os.Stat(probe)
		if err == nil {
			if !info.IsDir() {
				return invalid(path, probe+" is not a directory")
			}
			if err := writableDir(probe); err != nil {
				return invalid(path, "not writable")
			}
			return nil
		}
		if !errors.Is(err, fs.ErrNotExist) {
			return invalid(path, "cannot be inspected")
		}
		parent := filepath.Dir(probe)
		if parent == probe {
			return invalid(path, "no existing parent directory")
		}
		probe = parent
	}
}

func writableDir(dir string) error {
	f, err := os.CreateTemp(dir, ".applysmith-write-check-*")
	if err != nil {
		return err
	}
	name := f.Name()
	_ = f.Close()
	return os.Remove(name)
}

// HasConfiguredChoice distinguishes a recorded or explicit workspace from the
// default candidate, so desktop first launch can ask before creating it.
func HasConfiguredChoice(opts ResolveOptions) (bool, error) {
	if err := opts.fill(); err != nil {
		return false, err
	}
	if opts.Flag != "" || opts.Getenv(EnvWorkspace) != "" {
		return true, nil
	}
	path, err := readSettings(opts.ConfigDir)
	return path != "", err
}
