package toolchain

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"applysmith/internal/documents/assets"
)

// Toolchain is a diagnosed set of tools that can compile and verify.
type Toolchain struct {
	Root   Root
	Tools  Tools
	Report Report
}

// UnavailableError carries the diagnosis of a toolchain that cannot be used.
type UnavailableError struct{ Report Report }

// With every component present only the smoke is missing, and the message is
// its fix: an apply run's note shows the owner the command to run.
func (e *UnavailableError) Error() string {
	if len(e.Report.Missing) == 0 {
		return CodeUnavailable + ": no smoke has passed for this combination of tools and templates; " + e.Report.Fix
	}
	return CodeUnavailable + ": " + strings.Join(e.Report.Missing, ", ")
}

// Code is the error code reported to callers.
func (e *UnavailableError) Code() string { return CodeUnavailable }

// OpenForSmoke diagnoses the machine and returns a toolchain when every
// component is present, whether or not a smoke is cached.
func OpenForSmoke(ctx context.Context, cfg Config) (*Toolchain, error) {
	rep, tools := Probe(ctx, cfg)
	if len(rep.Missing) > 0 {
		return nil, &UnavailableError{Report: rep}
	}
	return &Toolchain{Root: cfg.Root, Tools: tools, Report: rep}, nil
}

// Open returns a toolchain only when the cached smoke matches the current
// combination of tools, lock and embedded files.
func Open(ctx context.Context, cfg Config) (*Toolchain, error) {
	t, err := OpenForSmoke(ctx, cfg)
	if err != nil {
		return nil, err
	}
	if t.Report.State != StateReady {
		return nil, &UnavailableError{Report: t.Report}
	}
	return t, nil
}

// Smoke compiles and verifies both synthetic templates on both paper sizes,
// then records the result under the current key. It is the only code besides
// Setup that writes below the root.
func (t *Toolchain) Smoke(ctx context.Context) (SmokeResult, error) {
	build, err := t.newBuildDir()
	if err != nil {
		return SmokeResult{}, err
	}
	defer os.RemoveAll(build)

	rec := SmokeRecord{Key: t.Report.Key, At: time.Now().UTC(), Passed: true}
	closure := map[string]bool{}
	for _, d := range []struct {
		name, engine string
		resume       *assets.Resume
		cover        *assets.Cover
	}{
		{name: "resume-a4", engine: "lualatex", resume: ptr(syntheticResume(assets.PaperA4))},
		{name: "resume-letter", engine: "lualatex", resume: ptr(syntheticResume(assets.PaperLetter))},
		{name: "cover-a4", engine: "xelatex", cover: ptr(syntheticCover(assets.PaperA4))},
		{name: "cover-letter", engine: "xelatex", cover: ptr(syntheticCover(assets.PaperLetter))},
	} {
		doc, files, err := t.smokeDocument(ctx, build, d.name, d.engine, d.resume, d.cover)
		rec.Documents = append(rec.Documents, doc)
		for _, f := range files {
			closure[f] = true
		}
		if err != nil {
			if errors.Is(err, ErrCancelled) {
				return SmokeResult{}, err
			}
			rec.Passed = false
			doc.Problems = append(doc.Problems, err.Error())
			rec.Documents[len(rec.Documents)-1] = doc
			break
		}
		if doc.Verify != Pass || doc.Layout != Pass {
			rec.Passed = false
		}
	}
	rec.ClosureFiles = len(closure)
	res := SmokeResult{Record: rec, Closure: sortedKeys(closure)}
	if err := writeSmoke(t.Root, rec); err != nil {
		return res, err
	}
	if !rec.Passed {
		return res, ErrSmokeFailed
	}
	return res, nil
}

// SmokeResult is a smoke record with the TeX files the engines read.
type SmokeResult struct {
	Record  SmokeRecord
	Closure []string
}

// ErrSmokeFailed is returned when a synthetic document did not compile or
// verify.
var ErrSmokeFailed = errors.New("toolchain: the smoke did not pass")

func ptr[T any](v T) *T { return &v }

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func (t *Toolchain) newBuildDir() (string, error) {
	for _, d := range []string{t.Root.Path(), t.Root.builds(), t.Root.home(), t.Root.texVar(), t.Root.texConfig()} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			return "", err
		}
	}
	removeStaleBuilds(t.Root.builds())
	return os.MkdirTemp(t.Root.builds(), "smoke-")
}

// staleBuildAge is past any build or smoke budget: a scratch directory this
// old belongs to a process that died before its deferred removal ran.
const staleBuildAge = time.Hour

func removeStaleBuilds(dir string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if info, err := e.Info(); err == nil && strings.HasPrefix(e.Name(), "smoke-") && time.Since(info.ModTime()) > staleBuildAge {
			_ = os.RemoveAll(filepath.Join(dir, e.Name()))
		}
	}
}

func (t *Toolchain) smokeDocument(ctx context.Context, build, name, engine string, r *assets.Resume, c *assets.Cover) (SmokeDocument, []string, error) {
	doc := SmokeDocument{Name: name, Engine: engine, Verify: Unverified, Layout: Unverified}
	dir := filepath.Join(build, name)
	if err := os.Mkdir(dir, 0o700); err != nil {
		return doc, nil, err
	}
	var src []byte
	var err error
	layout := LayoutExpect{Template: TemplateCover}
	if r != nil {
		layout = LayoutExpect{Template: TemplateResume, Paper: r.Paper}
	} else {
		layout.Paper = c.Paper
	}
	expect := PDFExpect{MinChars: 300, ASCIIDates: true, DumpText: filepath.Join(dir, name+".txt")}
	if r != nil {
		src, err = assets.RenderResume(*r)
		expect.MaxPages, expect.Contains = 2, resumeRequired
	} else {
		src, err = assets.RenderCover(*c)
		expect.Pages, expect.Contains = 1, coverRequired
		err = errors.Join(err, writeAssets(dir))
	}
	if err != nil {
		return doc, nil, err
	}
	if err := os.WriteFile(filepath.Join(dir, name+".tex"), src, 0o600); err != nil {
		return doc, nil, err
	}
	sum := sha256.Sum256(src)
	doc.TeXSHA256 = hex.EncodeToString(sum[:])

	passes, err := t.compile(ctx, engine, dir, name)
	doc.Passes = passes
	if err != nil {
		return doc, nil, fmt.Errorf("%w (engine log kept at %s)", err, t.keepLog(dir, name))
	}
	closure, err := texClosure(filepath.Join(dir, name+".fls"), dir)
	if err != nil {
		return doc, nil, err
	}
	pdf := filepath.Join(dir, name+".pdf")
	if doc.PDFSHA256, doc.PDFBytes, err = fileSHA256(pdf); err != nil {
		return doc, closure, err
	}
	if err := dirWithin(dir, MaxScratchBytes); err != nil {
		return doc, closure, err
	}

	res, err := t.VerifyPDF(ctx, pdf, expect)
	if err != nil {
		return doc, closure, err
	}
	doc.Verify = res.Overall
	if res.Pages != nil {
		doc.Pages = *res.Pages
	}
	doc.Problems = append(doc.Problems, problems("verify_pdf", res.Checks)...)

	lay, err := t.VerifyLayout(ctx, pdf, layout)
	if err != nil {
		return doc, closure, err
	}
	doc.Layout = lay.Overall
	doc.Problems = append(doc.Problems, problems("verify_layout", lay.Checks)...)

	if doc.Previews, err = t.previews(ctx, dir, name); err != nil {
		return doc, closure, err
	}
	if err := t.popplerText(ctx, pdf); err != nil {
		return doc, closure, err
	}
	return doc, closure, nil
}

// keepLog copies the engine log of a failed build into the private
// diagnostics directory. The log can quote TeX lines, so it never goes to a
// generic log.
func (t *Toolchain) keepLog(dir, name string) string {
	rel := filepath.Join("diagnostics", name+".log")
	dst := filepath.Join(t.Root.Path(), rel)
	b, err := os.ReadFile(filepath.Join(dir, name+".log"))
	if err != nil {
		return "none"
	}
	if len(b) > MaxLogBytes {
		b = b[len(b)-MaxLogBytes:]
	}
	if os.MkdirAll(filepath.Dir(dst), 0o700) != nil || os.WriteFile(dst, b, 0o600) != nil {
		return "none"
	}
	// The message travels into warnings and the API, so it names the file
	// relative to the toolchain directory, never an absolute path.
	return "<toolchain>/" + filepath.ToSlash(rel)
}

func problems(tool string, checks []Check) []string {
	var out []string
	for _, c := range checks {
		if c.Status != Pass {
			out = append(out, fmt.Sprintf("%s %s: %s", tool, c.ID, c.Status))
		}
	}
	return out
}

// writeAssets copies the letter's cover class and fonts into the private
// build directory.
func writeAssets(dir string) error {
	cls, err := assets.Read("cover.cls")
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, "cover.cls"), cls, 0o600); err != nil {
		return err
	}
	for _, f := range assets.FontFiles() {
		b, err := assets.Read(f)
		if err != nil {
			return err
		}
		dst := filepath.Join(dir, filepath.FromSlash(f))
		if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
			return err
		}
		if err := os.WriteFile(dst, b, 0o600); err != nil {
			return err
		}
	}
	return nil
}

// engineArgs is the complete argument vector of one engine pass: fixed flags
// and the generated file name. Nothing from a document ever reaches it.
func engineArgs(name string) []string {
	return []string{"-no-shell-escape", "-interaction=nonstopmode", "-halt-on-error", "-file-line-error",
		"-no-parse-first-line", "-recorder", name + ".tex"}
}

// engineCommand is one pass of the engine exe on name.tex in dir.
func (t *Toolchain) engineCommand(exe, dir, name string) command {
	return command{Path: exe, Dir: dir, Env: t.Root.minimalEnv(filepath.Dir(exe)), Args: engineArgs(name), Timeout: EngineTimeout}
}

// compile runs the engine on name.tex in dir: at most two passes, the second
// only when the first asks for it.
func (t *Toolchain) compile(ctx context.Context, engine, dir, name string) (int, error) {
	exe := t.Tools.LuaLaTeX
	if engine == EngineXeLaTeX {
		exe = t.Tools.XeLaTeX
	}
	for pass := 1; pass <= 2; pass++ {
		if _, err := run(ctx, t.engineCommand(exe, dir, name)); err != nil {
			return pass, fmt.Errorf("%s %s: %w", engine, name, err)
		}
		log, _ := os.ReadFile(filepath.Join(dir, name+".log"))
		if !bytes.Contains(log, []byte("Rerun to get")) && !bytes.Contains(log, []byte("may have changed")) {
			return pass, nil
		}
	}
	return 2, nil
}

// texClosure lists the files the engine read from outside the build
// directory, from its recorder output, as paths relative to the TeX tree.
func texClosure(fls, build string) ([]string, error) {
	f, err := os.Open(fls)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	seen := map[string]bool{}
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for sc.Scan() {
		line := sc.Text()
		if !strings.HasPrefix(line, "INPUT ") {
			continue
		}
		p := strings.TrimPrefix(line, "INPUT ")
		if !filepath.IsAbs(p) || strings.HasPrefix(p, build+string(filepath.Separator)) {
			continue
		}
		seen[texRelative(p)] = true
	}
	return sortedKeys(seen), sc.Err()
}

// texRelative strips the installation prefix, so a closure reads the same on
// every machine with the same distribution.
func texRelative(p string) string {
	for _, marker := range []string{"/texmf-dist/", "/texmf-var/", "/texmf/"} {
		if i := strings.Index(p, marker); i >= 0 {
			return strings.TrimPrefix(marker, "/") + p[i+len(marker):]
		}
	}
	return p
}

func dirWithin(dir string, limit int64) error {
	var total int64
	err := filepath.WalkDir(dir, func(_ string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		fi, err := d.Info()
		if err == nil {
			total += fi.Size()
		}
		return err
	})
	if err != nil {
		return err
	}
	if total > limit {
		return fmt.Errorf("toolchain: the scratch directory exceeds %d bytes", limit)
	}
	return nil
}

// previews rasterises every page at a low resolution.
func (t *Toolchain) previews(ctx context.Context, dir, name string) ([]string, error) {
	_, err := run(ctx, command{Path: t.Tools.PDFToPPM, Env: t.Root.minimalEnv(),
		Args: []string{"-r", "40", "-png", filepath.Join(dir, name+".pdf"), filepath.Join(dir, name+"-page")}, Timeout: HelperTimeout})
	if err != nil {
		return nil, fmt.Errorf("pdftoppm: %w", err)
	}
	matches, _ := filepath.Glob(filepath.Join(dir, name+"-page*.png"))
	if len(matches) == 0 {
		return nil, errors.New("pdftoppm wrote no preview")
	}
	var out []string
	for _, m := range matches {
		fi, err := os.Stat(m)
		if err != nil || fi.Size() == 0 {
			return nil, errors.New("pdftoppm wrote an empty preview")
		}
		sum, _, err := fileSHA256(m)
		if err != nil {
			return nil, err
		}
		out = append(out, filepath.Base(m)+"="+sum)
	}
	sort.Strings(out)
	return out, nil
}

// popplerText proves pdftotext extracts the persona's name from the PDF.
func (t *Toolchain) popplerText(ctx context.Context, pdf string) error {
	out, err := run(ctx, command{Path: t.Tools.PDFToText, Env: t.Root.minimalEnv(),
		Args: []string{"-enc", "UTF-8", pdf, "-"}, Timeout: HelperTimeout})
	if err != nil {
		return fmt.Errorf("pdftotext: %w", err)
	}
	if !bytes.Contains(out.Stdout, []byte("Alex")) {
		return errors.New("pdftotext did not extract the document text")
	}
	return nil
}
