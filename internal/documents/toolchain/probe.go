package toolchain

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"

	"applysmith/internal/documents/assets"
	"applysmith/internal/lazyregexp"
)

// CodeUnavailable is the error code of generation without a ready toolchain.
const CodeUnavailable = "document-toolchain-unavailable"

// State is how ready rendering is.
type State string

const (
	StateReady              State = "ready"
	StateVerificationNeeded State = "verification-needed"
	StateUnavailable        State = "unavailable"
)

// Component status values.
const (
	StatusOK          = "ok"
	StatusMissing     = "missing"
	StatusUnsupported = "unsupported"
)

// Component is one external piece, as detected.
type Component struct {
	Name    string `json:"name"`
	Path    string `json:"path,omitempty"`
	Version string `json:"version,omitempty"`
	Status  string `json:"status"`
	Detail  string `json:"detail,omitempty"`
	Fix     string `json:"fix,omitempty"`
}

// Report is the read-only diagnosis of the toolchain.
type Report struct {
	State      State       `json:"state"`
	Code       string      `json:"code,omitempty"`
	Root       string      `json:"root"`
	Components []Component `json:"components"`
	// Missing names every component that is absent or unsupported.
	Missing []string `json:"missing,omitempty"`
	// Smoke says whether a cached smoke matches the current combination.
	Smoke string `json:"smoke"`
	// Key identifies the combination of tools, lock and embedded files that a
	// smoke has to cover.
	Key string `json:"key"`
	Fix string `json:"fix,omitempty"`
}

// Config selects where Probe looks. Zero SearchDirs means the defaults.
type Config struct {
	Root       Root
	SearchDirs []string
}

// Tools are the resolved absolute executables a ready toolchain uses.
type Tools struct {
	LuaLaTeX, XeLaTeX, Kpsewhich string
	PDFToText, PDFToPPM          string
	Python                       string
}

var (
	reTeXLive  = lazyregexp.New(`\(TeX Live (\d{4})\)`)
	reXeTeX    = lazyregexp.New(`^XeTeX (\S+)`)
	reVersion  = lazyregexp.New(`[Vv]ersion:? (\d+(?:\.\d+)+)`)
	reProvides = lazyregexp.New(`\\ProvidesClass\{moderncv\}\[[^\]]*? v(\d+(?:\.\d+)+)`)
)

// Probe diagnoses the toolchain and changes nothing on the machine.
func Probe(ctx context.Context, cfg Config) (Report, Tools) {
	dirs := cfg.SearchDirs
	if len(dirs) == 0 {
		dirs = SearchDirs(os.Getenv)
	}
	p := prober{ctx: ctx, cfg: cfg, dirs: dirs}
	p.engines()
	p.moderncvAndPackages()
	p.venv()
	p.poppler()

	rep := Report{Root: cfg.Root.Path(), Components: p.comps}
	for _, c := range p.comps {
		if c.Status != StatusOK {
			rep.Missing = append(rep.Missing, c.Name)
		}
	}
	rep.Key = p.key()
	rep.Smoke = smokeStatus(cfg.Root, rep.Key)
	switch {
	case len(rep.Missing) > 0:
		rep.State, rep.Code = StateUnavailable, CodeUnavailable
		rep.Fix = fixFor(p.comps)
	case rep.Smoke != SmokeCurrent:
		rep.State = StateVerificationNeeded
		rep.Fix = "run `applysmith documents toolchain smoke` to compile and verify both synthetic templates"
	default:
		rep.State = StateReady
	}
	return rep, p.tools
}

type prober struct {
	ctx   context.Context
	cfg   Config
	dirs  []string
	comps []Component
	tools Tools
	// stamps are the size and modification time of every executable and TeX
	// file the smoke depends on.
	stamps []string
}

func (p *prober) add(c Component) { p.comps = append(p.comps, c) }

func (p *prober) env() []string {
	return p.cfg.Root.minimalEnv(p.dirs...)
}

func (p *prober) exec(path string, args ...string) (string, error) {
	out, err := run(p.ctx, command{Path: path, Args: args, Env: p.env(), Timeout: HelperTimeout})
	return string(out.Stdout) + string(out.Stderr), err
}

func (p *prober) stamp(path string) {
	if fi, err := os.Stat(path); err == nil {
		p.stamps = append(p.stamps, fmt.Sprintf("%s|%d|%d", path, fi.Size(), fi.ModTime().UnixNano()))
	}
}

func (p *prober) missing(name, detail, fix string) {
	p.add(Component{Name: name, Status: StatusMissing, Detail: detail, Fix: fix})
}

const (
	fixTeX     = "install a TeX distribution (macOS: MacTeX, or BasicTeX; the TeX packages check then names each package still missing and its `sudo tlmgr install` line)"
	fixPoppler = "install Poppler (macOS: `brew install poppler`)"
	fixVenv    = "run `applysmith documents toolchain setup`"
)

func (p *prober) engines() {
	for _, name := range []string{"lualatex", "xelatex"} {
		path, ok := lookup(p.dirs, name)
		if !ok {
			p.missing(name, "not found in the search path", fixTeX)
			continue
		}
		out, err := p.exec(path, "--version")
		if err != nil {
			p.add(Component{Name: name, Path: path, Status: StatusUnsupported, Detail: "--version failed", Fix: fixTeX})
			continue
		}
		first := strings.SplitN(out, "\n", 2)[0]
		ver := strings.TrimSpace(first)
		if m := reVersion.FindStringSubmatch(first); m != nil {
			ver = m[1]
		} else if m := reXeTeX.FindStringSubmatch(first); m != nil {
			ver = m[1]
		}
		if m := reTeXLive.FindStringSubmatch(first); m != nil {
			ver += " (TeX Live " + m[1] + ")"
		}
		p.stamp(path)
		p.add(Component{Name: name, Path: path, Version: ver, Status: StatusOK})
		if name == "lualatex" {
			p.tools.LuaLaTeX = path
		} else {
			p.tools.XeLaTeX = path
		}
	}
	if p.tools.LuaLaTeX != "" || p.tools.XeLaTeX != "" {
		base := p.tools.LuaLaTeX
		if base == "" {
			base = p.tools.XeLaTeX
		}
		if k := filepath.Join(filepath.Dir(base), "kpsewhich"); isExec(k) {
			p.tools.Kpsewhich = k
		} else if k, ok := lookup(p.dirs, "kpsewhich"); ok {
			p.tools.Kpsewhich = k
		}
	}
}

func isExec(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && fi.Mode().IsRegular() && fi.Mode().Perm()&0o111 != 0
}

func (p *prober) moderncvAndPackages() {
	if p.tools.Kpsewhich == "" {
		p.missing("moderncv", "kpsewhich is not available, so TeX packages cannot be located", fixTeX)
		p.missing("tex-packages", "kpsewhich is not available", fixTeX)
		return
	}
	names := []string{"moderncv.cls"}
	byBase := map[string]string{}
	for _, pk := range lock.TeX.Packages {
		if pk.Name != "moderncv" {
			names = append(names, pk.Witness)
		}
	}
	out, _ := p.exec(p.tools.Kpsewhich, append([]string{"--"}, names...)...)
	for _, line := range strings.Split(out, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			byBase[filepath.Base(line)] = line
		}
	}

	var absent []string
	for _, pk := range lock.TeX.Packages {
		if _, ok := byBase[filepath.Base(pk.Witness)]; !ok && pk.Name != "moderncv" {
			absent = append(absent, pk.Name)
		}
	}
	sort.Strings(absent)

	if path, ok := byBase["moderncv.cls"]; !ok {
		p.missing("moderncv", "moderncv.cls was not found", "install the TeX package moderncv (`sudo tlmgr install moderncv`)")
	} else {
		ver := moderncvVersion(path)
		c := Component{Name: "moderncv", Path: path, Version: ver, Status: StatusOK}
		if ver == "" || versionLess(ver, lock.TeX.Moderncv) {
			c.Status, c.Detail = StatusUnsupported, "version "+orUnknown(ver)+" is older than "+lock.TeX.Moderncv
			c.Fix = "update the TeX package moderncv (`sudo tlmgr update moderncv`)"
		}
		p.stamp(path)
		p.add(c)
	}
	if len(absent) > 0 {
		p.missing("tex-packages", "missing: "+strings.Join(absent, ", "),
			"install the missing packages (`sudo tlmgr install "+strings.Join(absent, " ")+"`)")
	} else {
		for _, line := range byBase {
			p.stamp(line)
		}
		p.add(Component{Name: "tex-packages", Version: fmt.Sprintf("%d packages", len(lock.TeX.Packages)), Status: StatusOK})
	}
}

func orUnknown(v string) string {
	if v == "" {
		return "unknown"
	}
	return v
}

func moderncvVersion(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	buf := make([]byte, 4096)
	n, _ := f.Read(buf)
	if m := reProvides.FindSubmatch(buf[:n]); m != nil {
		return string(m[1])
	}
	return ""
}

func (p *prober) venv() {
	py := p.cfg.Root.venvPython()
	if !isExec(py) {
		p.missing("python-venv", "no private venv at "+p.cfg.Root.venv(), fixVenv)
		return
	}
	out, err := p.exec(py, "-I", "-c", "import sys, pypdf; print(sys.version.split()[0]); print(pypdf.__version__)")
	lines := strings.Fields(out)
	if err != nil || len(lines) < 2 {
		p.add(Component{Name: "python-venv", Path: py, Status: StatusUnsupported, Detail: "the venv cannot import pypdf", Fix: fixVenv})
		return
	}
	pyVer, pdfVer := lines[0], lines[1]
	c := Component{Name: "python-venv", Path: py, Version: "python " + pyVer + ", pypdf " + pdfVer, Status: StatusOK}
	switch {
	case versionLess(pyVer, lock.Python.Min):
		c.Status, c.Detail, c.Fix = StatusUnsupported, "python "+pyVer+" is older than "+lock.Python.Min, fixVenv
	case pdfVer != lock.Pypdf.Version:
		c.Status, c.Detail, c.Fix = StatusUnsupported, "pypdf "+pdfVer+" is not the locked "+lock.Pypdf.Version, fixVenv
	default:
		p.tools.Python = py
		p.stamp(py)
	}
	p.add(c)
}

func (p *prober) poppler() {
	for _, name := range []string{"pdftotext", "pdftoppm"} {
		path, ok := lookup(p.dirs, name)
		if !ok {
			p.missing(name, "not found in the search path", fixPoppler)
			continue
		}
		out, _ := p.exec(path, "-v")
		m := reVersion.FindStringSubmatch(out)
		c := Component{Name: name, Path: path, Status: StatusOK}
		switch {
		case m == nil:
			c.Status, c.Detail, c.Fix = StatusUnsupported, "the version is not readable", fixPoppler
		case versionLess(m[1], lock.Poppler.Min):
			c.Version = m[1]
			c.Status, c.Detail, c.Fix = StatusUnsupported, "version "+m[1]+" is older than "+lock.Poppler.Min+" (an xpdf build has no word boxes)", fixPoppler
		default:
			c.Version = m[1]
			p.stamp(path)
			if name == "pdftotext" {
				p.tools.PDFToText = path
			} else {
				p.tools.PDFToPPM = path
			}
		}
		p.add(c)
	}
}

// key hashes everything a smoke result depends on.
func (p *prober) key() string {
	h := sha256.New()
	fmt.Fprintf(h, "%s/%s\n", runtime.GOOS, runtime.GOARCH)
	h.Write(lockJSON)
	for _, f := range EmbeddedHashes() {
		fmt.Fprintf(h, "%s=%s\n", f.Name, f.SHA256)
	}
	for _, c := range p.comps {
		fmt.Fprintf(h, "%s|%s|%s|%s\n", c.Name, c.Path, c.Version, c.Status)
	}
	sort.Strings(p.stamps)
	for _, s := range p.stamps {
		fmt.Fprintln(h, s)
	}
	return hex.EncodeToString(h.Sum(nil))
}

// FileHash is the SHA-256 of one embedded file.
type FileHash struct {
	Name   string `json:"name"`
	SHA256 string `json:"sha256"`
}

// EmbeddedHashes lists the hash of every embedded asset, sorted by name.
func EmbeddedHashes() []FileHash {
	var out []FileHash
	_ = fs.WalkDir(assets.FS(), ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, rerr := assets.Read(p)
		if rerr != nil {
			return rerr
		}
		sum := sha256.Sum256(b)
		out = append(out, FileHash{Name: p, SHA256: hex.EncodeToString(sum[:])})
		return nil
	})
	return out
}

func fixFor(comps []Component) string {
	seen := map[string]bool{}
	var fixes []string
	for _, c := range comps {
		if c.Status != StatusOK && c.Fix != "" && !seen[c.Fix] {
			seen[c.Fix] = true
			fixes = append(fixes, c.Fix)
		}
	}
	return strings.Join(fixes, "; ")
}
