package guard

import (
	"bytes"
	"encoding/json"
	"fmt"
	"go/ast"
	"go/format"
	"go/parser"
	"go/token"
	"os"
	"path"
	"sort"
	"strings"
)

const (
	ruleBudget       = "repo-budget"
	ruleFileLength   = "file-length"
	ruleBuildOutput  = "build-output"
	ruleInternalPath = "internal-material"
	ruleDeps         = "dependency-budget"
	ruleRegexp       = "regexp-compile"
	ruleGofmt        = "gofmt"

	// Budgets are constants; raising one needs a spec change.
	maxTrackedFiles   = 3300
	maxTrackedBytes   = 40_000_000
	maxBlobBytes      = 1 << 20
	maxGoLines        = 1200
	maxTSLines        = 1500
	maxGoDirectDeps   = 20
	maxDashboardDeps  = 10
	largestFilesShown = 10
)

// ---- repository size and count ----

func checkBudgets(t *Tree) ([]Finding, error) {
	type sized struct {
		path string
		size int64
	}
	var files []sized
	var total int64
	for _, p := range t.Paths {
		fi, err := os.Stat(t.abs(p))
		if err != nil {
			return nil, err
		}
		files = append(files, sized{p, fi.Size()})
		total += fi.Size()
	}
	var out []Finding
	if len(files) > maxTrackedFiles || total > maxTrackedBytes {
		sort.Slice(files, func(i, j int) bool { return files[i].size > files[j].size })
		var top []string
		for i := 0; i < len(files) && i < largestFilesShown; i++ {
			top = append(top, fmt.Sprintf("%s (%d bytes)", files[i].path, files[i].size))
		}
		detail := fmt.Sprintf("%d files (limit %d), %d bytes (limit %d); largest: %s",
			len(files), maxTrackedFiles, total, maxTrackedBytes, strings.Join(top, ", "))
		out = append(out, Finding{Rule: ruleBudget, Path: ".", Detail: detail})
	}
	return out, nil
}

// ---- regular expressions compiled at start or on every call ----

// lazyRegexpPkg declares expressions compiled on first use
// (docs/architecture/performance.md, "Process start").
const lazyRegexpPkg = "internal/lazyregexp"

// checkRegexpCompiles keeps regular-expression compiles out of process start
// and out of repeated calls: a package-level regexp.MustCompile runs in every
// process before main, and a fixed pattern compiled inside a function is
// compiled again on each call. Both are declared with lazyregexp.New. A pattern
// built at run time is compiled where it is built. Test files and packages that
// cannot import lazyregexp (at or below its layer) are exempt.
func checkRegexpCompiles(t *Tree) ([]Finding, error) {
	data, err := t.Read(layersFile)
	if err != nil {
		return nil, err
	}
	layers, _ := parseLayers(string(data))
	floor, ok := layers[lazyRegexpPkg]
	if !ok {
		return []Finding{{Rule: ruleRegexp, Path: layersFile, Detail: lazyRegexpPkg + " has no layer"}}, nil
	}
	keep := func(p string) bool {
		l, listed := layers[path.Dir(p)]
		return ext(p) == ".go" && !strings.HasSuffix(p, "_test.go") && (!listed || l.n > floor.n)
	}
	var out []Finding
	fset := token.NewFileSet()
	err = t.eachText(keep, func(f textFile) {
		if !bytes.Contains(f.Data, []byte(`"regexp"`)) {
			return
		}
		file, err := parser.ParseFile(fset, f.Path, f.Data, parser.SkipObjectResolution)
		if err != nil {
			return // the compiler reports it
		}
		pkg := ""
		for _, imp := range file.Imports {
			if imp.Path.Value == `"regexp"` {
				pkg = "regexp"
				if imp.Name != nil {
					pkg = imp.Name.Name
				}
			}
		}
		report := func(n ast.Node, detail string) {
			pos := fset.Position(n.Pos())
			out = append(out, Finding{Rule: ruleRegexp, Path: f.Path, Line: pos.Line, Col: pos.Column, Detail: detail})
		}
		for _, decl := range file.Decls {
			ast.Inspect(decl, func(n ast.Node) bool {
				c := regexpCompile(n, pkg)
				switch d := decl.(type) {
				case *ast.GenDecl:
					if c != nil && d.Tok == token.VAR {
						report(c, "compiles at every process start; declare it with lazyregexp.New")
					}
				case *ast.FuncDecl:
					if c != nil && constantString(c.Args[0]) {
						report(c, "compiles a fixed pattern on every call; declare it once with lazyregexp.New")
					}
				}
				return true
			})
		}
	})
	return out, err
}

// regexpCompile is n when it calls one of package regexp's compile functions,
// imported as pkg.
func regexpCompile(n ast.Node, pkg string) *ast.CallExpr {
	c, ok := n.(*ast.CallExpr)
	if !ok || len(c.Args) != 1 {
		return nil
	}
	sel, ok := c.Fun.(*ast.SelectorExpr)
	if !ok {
		return nil
	}
	if id, ok := sel.X.(*ast.Ident); !ok || pkg == "" || id.Name != pkg {
		return nil
	}
	switch sel.Sel.Name {
	case "MustCompile", "MustCompilePOSIX", "Compile", "CompilePOSIX":
		return c
	}
	return nil
}

// constantString reports a string literal or a sum of string literals.
func constantString(e ast.Expr) bool {
	switch e := e.(type) {
	case *ast.BasicLit:
		return e.Kind == token.STRING
	case *ast.ParenExpr:
		return constantString(e.X)
	case *ast.BinaryExpr:
		return e.Op == token.ADD && constantString(e.X) && constantString(e.Y)
	}
	return false
}

// ---- source file length ----

func physicalLines(data []byte) int {
	n := bytes.Count(data, []byte("\n"))
	if len(data) > 0 && data[len(data)-1] != '\n' {
		n++
	}
	return n
}

func generatedHeader(data []byte) bool {
	head := data
	if len(head) > 1024 {
		head = head[:1024]
	}
	return bytes.Contains(head, []byte("Code generated")) && bytes.Contains(head, []byte("DO NOT EDIT"))
}

func checkFileLength(t *Tree) ([]Finding, error) {
	var out []Finding
	keep := func(p string) bool {
		e := ext(p)
		return e == ".go" || e == ".swift" || e == ".ts" || e == ".tsx"
	}
	err := t.eachText(keep, func(f textFile) {
		limit := maxTSLines
		if e := ext(f.Path); e == ".go" || e == ".swift" {
			limit = maxGoLines
		}
		if n := physicalLines(f.Data); n > limit && !generatedHeader(f.Data) {
			out = append(out, Finding{Rule: ruleFileLength, Path: f.Path, Detail: fmt.Sprintf("%d lines (limit %d)", n, limit)})
		}
	})
	return out, err
}

// checkGofmt flags every Go file that gofmt would change or cannot parse.
func checkGofmt(t *Tree) ([]Finding, error) {
	var out []Finding
	err := t.eachText(func(p string) bool { return ext(p) == ".go" }, func(f textFile) {
		formatted, err := format.Source(f.Data)
		switch {
		case err != nil:
			out = append(out, Finding{Rule: ruleGofmt, Path: f.Path, Detail: "gofmt cannot parse the file: " + err.Error()})
		case !bytes.Equal(formatted, f.Data):
			out = append(out, Finding{Rule: ruleGofmt, Path: f.Path, Detail: "not gofmt-formatted; run gofmt -w"})
		}
	})
	return out, err
}

// ---- build output, state and binaries ----

var (
	buildDirs     = map[string]bool{"node_modules": true, "dist": true, "build": true, "out": true, ".next": true}
	buildExts     = map[string]bool{".app": true, ".dmg": true, ".pkg": true, ".exe": true, ".dll": true, ".dylib": true, ".so": true, ".a": true, ".o": true, ".wasm": true, ".test": true, ".db": true, ".sqlite": true, ".sqlite3": true, ".log": true, ".pprof": true, ".prof": true}
	stateSuffixes = []string{".db-wal", ".db-shm", ".sqlite-wal", ".sqlite-shm", ".sqlite3-wal", ".sqlite3-shm"}
	stateNames    = map[string]bool{"Local State": true, "Preferences": true, "DevToolsActivePort": true, ".DS_Store": true}
	iconExts      = map[string]bool{".png": true, ".icns": true, ".ico": true, ".svg": true}
)

// buildOutputReason names why a path is build output or runtime state, or "".
func buildOutputReason(p string) string {
	parts := strings.Split(p, "/")
	for _, part := range parts[:len(parts)-1] {
		if buildDirs[part] {
			return "inside a build-output directory; bundles are generated, never committed"
		}
		if strings.HasSuffix(part, ".app") {
			return "inside an application bundle"
		}
	}
	base := parts[len(parts)-1]
	if stateNames[base] {
		return "runtime state or editor residue"
	}
	for _, s := range stateSuffixes {
		if strings.HasSuffix(base, s) {
			return "database sidecar file"
		}
	}
	if buildExts[ext(p)] {
		return "build output, database, log or profile"
	}
	return ""
}

func isIcon(p string) bool {
	if !iconExts[ext(p)] {
		return false
	}
	parts := strings.Split(p, "/")
	for _, part := range parts[:len(parts)-1] {
		if part == "icons" {
			return true
		}
	}
	return false
}

// executableHeader recognises ELF, Mach-O and PE images by their first bytes.
func executableHeader(head []byte) string {
	switch {
	case bytes.HasPrefix(head, []byte("\x7fELF")):
		return "ELF"
	case bytes.HasPrefix(head, []byte{0xfe, 0xed, 0xfa, 0xce}), bytes.HasPrefix(head, []byte{0xfe, 0xed, 0xfa, 0xcf}),
		bytes.HasPrefix(head, []byte{0xce, 0xfa, 0xed, 0xfe}), bytes.HasPrefix(head, []byte{0xcf, 0xfa, 0xed, 0xfe}),
		bytes.HasPrefix(head, []byte{0xca, 0xfe, 0xba, 0xbe}):
		return "Mach-O"
	case bytes.HasPrefix(head, []byte("MZ")) && len(head) >= 0x40:
		off := int(head[0x3c]) | int(head[0x3d])<<8
		if off+4 <= len(head) && bytes.Equal(head[off:off+4], []byte("PE\x00\x00")) {
			return "PE"
		}
	}
	return ""
}

func checkBuildOutput(t *Tree) ([]Finding, error) {
	var out []Finding
	for _, p := range t.Paths {
		if why := buildOutputReason(p); why != "" {
			out = append(out, Finding{Rule: ruleBuildOutput, Path: p, Detail: why})
			continue
		}
		fi, err := os.Stat(t.abs(p))
		if err != nil {
			return nil, err
		}
		if fi.Size() > maxBlobBytes && !isIcon(p) {
			out = append(out, Finding{Rule: ruleBuildOutput, Path: p, Detail: fmt.Sprintf("%d bytes exceeds the %d byte limit for non-icon files", fi.Size(), maxBlobBytes)})
			continue
		}
		head, err := readHead(t.abs(p), 4096)
		if err != nil {
			return nil, err
		}
		if kind := executableHeader(head); kind != "" {
			out = append(out, Finding{Rule: ruleBuildOutput, Path: p, Detail: kind + " executable header"})
		}
	}
	return out, nil
}

func readHead(file string, n int) ([]byte, error) {
	f, err := os.Open(file)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	buf := make([]byte, n)
	m, err := f.Read(buf)
	if err != nil && m == 0 {
		return nil, nil // empty file
	}
	return buf[:m], nil
}

// ---- internal material (class A: forbidden in every mode; class B: release only) ----

var userLayerRootFiles = map[string]bool{
	"cv.md": true, "profile.yml": true, "portals.yml": true, "article-digest.md": true,
	"answer-bank.yml": true, "answer-bank.json": true,
}

var userLayerRootDirs = map[string]bool{
	"data": true, "reports": true, "output": true, "interview-prep": true,
}

func internalPathReason(p string) string {
	parts := strings.Split(p, "/")
	base := parts[len(parts)-1]
	switch {
	case len(parts) == 1 && userLayerRootFiles[base]:
		return "user-layer file at the repository root"
	case len(parts) > 1 && userLayerRootDirs[parts[0]]:
		return "user-layer directory at the repository root"
	case base == ".gitmodules":
		return "git submodule configuration"
	case strings.HasSuffix(base, ".bak"), strings.HasSuffix(base, ".orig"), strings.HasSuffix(base, ".rej"), strings.HasSuffix(base, "~"):
		return "backup or patch residue"
	case strings.HasSuffix(base, ".jsonl"):
		for _, part := range parts[:len(parts)-1] {
			if part == "testdata" {
				return ""
			}
		}
		return "ledger file outside testdata"
	}
	return ""
}

func checkInternalPaths(t *Tree) ([]Finding, error) {
	var out []Finding
	for _, p := range t.Paths {
		if why := internalPathReason(p); why != "" {
			out = append(out, Finding{Rule: ruleInternalPath, Path: p, Detail: why})
		}
	}
	for _, g := range t.Gitlinks {
		out = append(out, Finding{Rule: ruleInternalPath, Path: g, Detail: "git submodule (gitlink)"})
	}
	if t.Release {
		for _, p := range t.Paths {
			if why := ClassBReason(p); why != "" {
				out = append(out, Finding{Rule: ruleInternalPath, Path: p, Detail: "internal working material (class B) in a publication tree: " + why})
			}
		}
	}
	return out, nil
}

// ---- dependency budgets ----

// goDirectRequires lists the requirements of a go.mod that carry no indirect
// marker.
func goDirectRequires(mod string) []string {
	var direct []string
	inBlock := false
	for _, raw := range strings.Split(mod, "\n") {
		line := strings.TrimSpace(raw)
		code, comment, _ := strings.Cut(line, "//")
		code = strings.TrimSpace(code)
		indirect := strings.Contains(comment, "indirect")
		switch {
		case inBlock && code == ")":
			inBlock = false
		case inBlock && code != "" && !indirect:
			direct = append(direct, strings.Fields(code)[0])
		case strings.HasPrefix(code, "require ("):
			inBlock = true
		case strings.HasPrefix(code, "require ") && !indirect:
			direct = append(direct, strings.Fields(code)[1])
		}
	}
	return direct
}

func npmDependencies(data []byte) (int, error) {
	var pkg struct {
		Dependencies map[string]string `json:"dependencies"`
	}
	if err := json.Unmarshal(data, &pkg); err != nil {
		return 0, err
	}
	return len(pkg.Dependencies), nil
}

func checkDependencyBudgets(t *Tree) ([]Finding, error) {
	var out []Finding
	if t.Has("go.mod") {
		data, err := t.Read("go.mod")
		if err != nil {
			return nil, err
		}
		if direct := goDirectRequires(string(data)); len(direct) > maxGoDirectDeps {
			out = append(out, Finding{Rule: ruleDeps, Path: "go.mod", Detail: fmt.Sprintf("%d direct requirements (limit %d): %s", len(direct), maxGoDirectDeps, strings.Join(direct, ", "))})
		}
	}
	limits := map[string]int{"web/package.json": maxDashboardDeps, "engine/package.json": 0, "extension/package.json": 0}
	for _, p := range []string{"web/package.json", "engine/package.json", "extension/package.json"} {
		if !t.Has(p) {
			continue
		}
		data, err := t.Read(p)
		if err != nil {
			return nil, err
		}
		n, err := npmDependencies(data)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", path.Base(p), err)
		}
		if n > limits[p] {
			out = append(out, Finding{Rule: ruleDeps, Path: p, Detail: fmt.Sprintf("%d runtime dependencies (limit %d)", n, limits[p])})
		}
	}
	return out, nil
}
