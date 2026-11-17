package toolchain

import (
	"os"
	"path/filepath"
	"strings"
)

// EnvToolPath replaces the default executable search with a list of
// directories. It is installation state of this machine, not request input.
const EnvToolPath = "APPLYSMITH_DOCUMENT_TOOL_PATH"

// Root is the per-user directory that holds the venv, the TeX cache and the
// smoke record.
type Root string

// DefaultRoot places the toolchain below the per-user configuration directory.
func DefaultRoot(configDir string) Root {
	if configDir == "" {
		configDir, _ = os.UserConfigDir()
	}
	return Root(filepath.Join(configDir, "applysmith", "documents-toolchain"))
}

func (r Root) venv() string      { return filepath.Join(string(r), "venv") }
func (r Root) texVar() string    { return filepath.Join(string(r), "texmf-var") }
func (r Root) texConfig() string { return filepath.Join(string(r), "texmf-config") }
func (r Root) home() string      { return filepath.Join(string(r), "home") }
func (r Root) smokeDir() string  { return filepath.Join(string(r), "smoke") }

// smokeFile is the record of one key. Each build of the app, and each change
// of tools, has its own record, so one never stales another's.
func (r Root) smokeFile(key string) string { return filepath.Join(r.smokeDir(), key+".json") }
func (r Root) builds() string              { return filepath.Join(string(r), "builds") }
func (r Root) venvPython() string          { return filepath.Join(r.venv(), "bin", "python") }

// Path returns the root as a plain path.
func (r Root) Path() string { return string(r) }

// SearchDirs returns the directories executables are looked up in: the
// override list when set, else PATH followed by the usual install locations,
// because an app started from the Finder has a short PATH.
func SearchDirs(getenv func(string) string) []string {
	if v := getenv(EnvToolPath); v != "" {
		return splitList(v)
	}
	dirs := splitList(getenv("PATH"))
	return append(dirs, "/Library/TeX/texbin", "/usr/local/texlive/bin", "/opt/homebrew/bin", "/usr/local/bin", "/usr/bin")
}

func splitList(v string) []string {
	var out []string
	for _, d := range strings.Split(v, string(os.PathListSeparator)) {
		if d != "" && filepath.IsAbs(d) {
			out = append(out, d)
		}
	}
	return out
}

// lookup finds an executable file by name in dirs.
func lookup(dirs []string, name string) (string, bool) {
	for _, d := range dirs {
		p := filepath.Join(d, name)
		if fi, err := os.Stat(p); err == nil && fi.Mode().IsRegular() && fi.Mode().Perm()&0o111 != 0 {
			return p, true
		}
	}
	return "", false
}

// minimalEnv is the environment of every tool: no credentials, no user TeX or
// Python search paths. HOME and the TeX variable trees live in the root.
func (r Root) minimalEnv(pathDirs ...string) []string {
	return []string{
		"PATH=" + strings.Join(append(pathDirs, "/usr/bin", "/bin"), string(os.PathListSeparator)),
		"HOME=" + r.home(),
		"TEXMFHOME=" + filepath.Join(r.home(), "texmf"),
		"TEXMFVAR=" + r.texVar(),
		"TEXMFCONFIG=" + r.texConfig(),
		"LC_ALL=C",
		"PYTHONNOUSERSITE=1",
		"PYTHONDONTWRITEBYTECODE=1",
	}
}
