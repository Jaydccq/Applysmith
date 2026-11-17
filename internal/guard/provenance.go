package guard

import (
	"bytes"
	"encoding/json"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"
)

const (
	ruleProvenance = "provenance"

	provenanceFile     = "docs/provenance.md"
	registerHeading    = "## Register"
	cleanRoomHeader    = "Clean-room: " + provenanceFile + "#"
	cleanRoomTreatment = "clean-room"
)

var (
	treatments = map[string]bool{"clean-room": true, "attributed-source": true, "dependency": true, "data-fetched": true, "asset-shipped": true}
	statuses   = map[string]bool{"planned": true, "implemented": true}
	// Source files whose first lines must carry the clean-room header.
	headerExts = map[string]bool{".go": true, ".ts": true, ".tsx": true, ".js": true, ".mjs": true, ".css": true, ".sh": true}
	// Third-party directories may hold licence texts and notices, not code.
	codeExts = map[string]bool{".go": true, ".ts": true, ".tsx": true, ".js": true, ".mjs": true, ".py": true, ".sh": true, ".c": true, ".h": true, ".rs": true, ".tex": true, ".cls": true}
	idRE     = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9-]*$`)
	globRE   = regexp.MustCompile("`([^`]+)`")
)

// provenanceRow is one line of the register table:
// | ID | Paths | Origin and licence | Treatment | Spec | Review | Status |
type provenanceRow struct {
	ID, Origin, Treatment, Spec, Review, Status string
	Globs                                       []string
	Line                                        int
}

func parseProvenance(content string) ([]provenanceRow, []Finding) {
	var rows []provenanceRow
	var problems []Finding
	seen := map[string]bool{}
	inRegister := false
	for i, line := range strings.Split(content, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "## ") {
			inRegister = line == registerHeading
		}
		if !inRegister || !strings.HasPrefix(line, "|") {
			continue
		}
		cells := strings.Split(strings.Trim(line, "|"), "|")
		for j := range cells {
			cells[j] = strings.TrimSpace(cells[j])
		}
		if len(cells) > 0 && (strings.EqualFold(cells[0], "id") || strings.Trim(cells[0], "-: ") == "") {
			continue // header and separator rows
		}
		bad := func(detail string) {
			problems = append(problems, Finding{Rule: ruleProvenance, Path: provenanceFile, Line: i + 1, Detail: detail})
		}
		if len(cells) != 7 {
			bad("register row needs 7 columns: id, paths, origin and licence, treatment, spec, review, status")
			continue
		}
		r := provenanceRow{ID: cells[0], Origin: cells[2], Treatment: cells[3], Spec: cells[4], Review: cells[5], Status: cells[6], Line: i + 1}
		for _, m := range globRE.FindAllStringSubmatch(cells[1], -1) {
			r.Globs = append(r.Globs, m[1])
		}
		switch {
		case !idRE.MatchString(r.ID):
			bad("row identifier must be letters, digits and hyphens")
		case seen[r.ID]:
			bad("duplicate row identifier " + r.ID)
		case len(r.Globs) == 0:
			bad("row " + r.ID + " lists no path glob in backticks")
		case !treatments[r.Treatment]:
			bad("row " + r.ID + " has unknown treatment")
		case !statuses[r.Status]:
			bad("row " + r.ID + " status must be planned or implemented")
		case r.Origin == "" || r.Spec == "" || r.Review == "":
			bad("row " + r.ID + " must give origin, spec and review")
		default:
			seen[r.ID] = true
			rows = append(rows, r)
		}
	}
	return rows, problems
}

// globToRegexp supports "*" (within a segment), "?" and "**" (across segments).
func globToRegexp(glob string) *regexp.Regexp {
	var b strings.Builder
	b.WriteString("^")
	for i := 0; i < len(glob); i++ {
		switch c := glob[i]; {
		case c == '*' && i+1 < len(glob) && glob[i+1] == '*':
			i++
			if i+1 < len(glob) && glob[i+1] == '/' {
				i++
				b.WriteString("(?:.*/)?")
			} else {
				b.WriteString(".*")
			}
		case c == '*':
			b.WriteString("[^/]*")
		case c == '?':
			b.WriteString("[^/]")
		default:
			b.WriteString(regexp.QuoteMeta(string(c)))
		}
	}
	b.WriteString("$")
	return regexp.MustCompile(b.String())
}

// headIncludes reports whether the first five lines of src contain s.
func headIncludes(src []byte, s string) bool {
	lines := strings.SplitN(string(src), "\n", 6)
	if len(lines) > 5 {
		lines = lines[:5]
	}
	return strings.Contains(strings.Join(lines, "\n"), s)
}

func checkProvenance(t *Tree) ([]Finding, error) {
	var out []Finding
	for _, p := range t.Paths {
		parts := strings.Split(p, "/")
		for _, part := range parts[:len(parts)-1] {
			if part == "vendor" {
				out = append(out, Finding{Rule: ruleProvenance, Path: p, Detail: "vendor directories are not allowed"})
				break
			}
		}
		if underDir(p, "third_party") && codeExts[ext(p)] {
			out = append(out, Finding{Rule: ruleProvenance, Path: p, Detail: "third_party holds licence texts, notices and manifests only, not source"})
		}
	}
	if !t.Has(provenanceFile) {
		return append(out, Finding{Rule: ruleProvenance, Path: provenanceFile, Detail: "the provenance register is missing"}), nil
	}
	data, err := t.Read(provenanceFile)
	if err != nil {
		return nil, err
	}
	rows, problems := parseProvenance(string(data))
	out = append(out, problems...)

	cleanRoom := map[string]bool{}
	for _, r := range rows {
		var res []*regexp.Regexp
		for _, g := range r.Globs {
			res = append(res, globToRegexp(g))
		}
		matched := 0
		globHits := make([]int, len(res))
		for _, p := range t.Paths {
			hit := false
			for i, re := range res {
				if re.MatchString(p) {
					globHits[i]++
					hit = true
				}
			}
			if !hit {
				continue
			}
			matched++
			if r.Treatment != cleanRoomTreatment || !headerExts[ext(p)] {
				continue
			}
			src, err := t.Read(p)
			if err != nil {
				return nil, err
			}
			if !headIncludes(src, cleanRoomHeader+r.ID) {
				out = append(out, Finding{Rule: ruleProvenance, Path: p, Detail: fmt.Sprintf("file in clean-room area %s lacks the header %q", r.ID, cleanRoomHeader+r.ID)})
			}
		}
		if r.Status == "implemented" && matched == 0 {
			out = append(out, Finding{Rule: ruleProvenance, Path: provenanceFile, Line: r.Line, Detail: fmt.Sprintf("implemented row %s matches no tracked file", r.ID)})
		} else if r.Status == "implemented" {
			for i, n := range globHits {
				if n == 0 {
					out = append(out, Finding{Rule: ruleProvenance, Path: provenanceFile, Line: r.Line, Detail: fmt.Sprintf("implemented row %s: glob %s matches no tracked file", r.ID, r.Globs[i])})
				}
			}
		}
		if r.Treatment == cleanRoomTreatment {
			cleanRoom[r.ID] = true
		}
	}

	// A header must name a clean-room row of the register.
	headerRE := regexp.MustCompile(regexp.QuoteMeta(cleanRoomHeader) + `([A-Za-z0-9-]+)`)
	err = t.eachText(func(p string) bool { return headerExts[ext(p)] }, func(f textFile) {
		head := f.Lines
		if len(head) > 5 {
			head = head[:5]
		}
		for i, l := range head {
			if m := headerRE.FindStringSubmatch(l); m != nil && !cleanRoom[m[1]] {
				out = append(out, Finding{Rule: ruleProvenance, Path: f.Path, Line: i + 1, Detail: "header names " + m[1] + ", which is not a clean-room row of the register"})
			}
		}
	})
	return out, err
}

const ruleSimilarity = "similarity"
const envSimilarity = "APPLYSMITH_SIMILARITY_REFERENCES"

var similarityWords = regexp.MustCompile(`[\p{L}\p{N}_]+`)
var similarityIgnored = func() map[string]bool {
	out := map[string]bool{}
	for _, word := range strings.Fields(`break case chan const continue default defer else fallthrough for func go goto if import interface map package range return select struct switch type var true false nil any bool byte complex64 complex128 error float32 float64 int int8 int16 int32 int64 rune string uint uint8 uint16 uint32 uint64 uintptr append cap clear close complex copy delete imag len make max min new panic print println real recover as async await class constructor declare enum export extends from function implements in instanceof keyof let module namespace never null of private protected public readonly require static super symbol this throw try typeof undefined unique unknown void while with yield catch finally abstract assert def del elif except exec global lambda nonlocal pass raise and not or is none self bytes dict list tuple frozenset object str isinstance issubclass getattr setattr hasattr enumerate filter iter next open repr sorted zip array arraybuffer bigint boolean console date error eval function infinity intl isfinite isnan json math nan number parsefloat parseint promise proxy reflect regexp set string symbol weakmap weakset`) {
		out[word] = true
	}
	return out
}()

type similarityDocument struct {
	path        string
	tokens      []uint32
	shingles    map[[5]uint32]struct{}
	identifiers map[uint32]struct{}
}

type similarityRun struct{ file, offset int }

type similarityIndex struct {
	documents   []similarityDocument
	shingles    map[[5]uint32][]int
	runs        map[uint64][]similarityRun
	identifiers map[uint32][]int
}

type similarityVocabulary map[string]uint32

// Compare runtime text regardless of extension; also recognize source/data
// outside runtime roots. Documentation prose and licence inventories are metadata.
func similaritySource(p string) bool {
	// Lockfiles are written by the package managers from the dependency choices
	// in go.mod and package.json, which are compared.
	if base := filepath.Base(p); base == "go.sum" || base == "bun.lock" {
		return false
	}
	for _, root := range []string{"internal", "cmd", "engine", "web/src", "scripts", "templates", "datasets", "config"} {
		if underDir(p, root) {
			return true
		}
	}
	if underDir(p, "third_party/licenses") || strings.HasPrefix(filepath.Base(p), "NOTICE") {
		return false
	}
	switch ext(p) {
	case ".go", ".ts", ".tsx", ".js", ".mjs", ".cjs", ".css", ".sh", ".py", ".c", ".h", ".rs", ".tex", ".cls", ".sql", ".html", ".json", ".jsonl", ".yaml", ".yml", ".toml", ".csv", ".tsv", ".txt", ".tmpl", ".template", ".tpl", ".gotmpl", ".golden":
		return true
	case ".md":
		return strings.Contains(p, "/") && !underDir(p, "docs") && !underDir(p, "openspec") && !underDir(p, "third_party")
	}
	return slices.Contains([]string{"Makefile", "go.mod"}, filepath.Base(p))
}

// A supplied source snapshot is independent of its historical ignore rules.
// Never follow symlinks or enter installed dependencies, caches or Git metadata.
func similarityReference(root string) (*Tree, error) {
	tree := &Tree{Root: root}
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			if path != root && slices.Contains([]string{".git", "node_modules", ".venv", "venv", ".cache", "__pycache__"}, entry.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		if !entry.Type().IsRegular() {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		tree.Paths = append(tree.Paths, filepath.ToSlash(rel))
		return nil
	})
	return tree, err
}

var similarityStandardExports sync.Map

func similarityDotNames(root string) map[string]bool {
	if cached, ok := similarityStandardExports.Load(root); ok {
		return cached.(map[string]bool)
	}
	packages, _ := parser.ParseDir(token.NewFileSet(), root, func(info fs.FileInfo) bool { return !strings.HasSuffix(info.Name(), "_test.go") }, parser.SkipObjectResolution)
	names := map[string]bool{}
	for _, pkg := range packages {
		for _, file := range pkg.Files {
			for _, declaration := range file.Decls {
				switch declaration := declaration.(type) {
				case *ast.FuncDecl:
					if declaration.Recv == nil && ast.IsExported(declaration.Name.Name) {
						names[strings.ToLower(declaration.Name.Name)] = true
					}
				case *ast.GenDecl:
					for _, spec := range declaration.Specs {
						switch spec := spec.(type) {
						case *ast.TypeSpec:
							if ast.IsExported(spec.Name.Name) {
								names[strings.ToLower(spec.Name.Name)] = true
							}
						case *ast.ValueSpec:
							for _, name := range spec.Names {
								if ast.IsExported(name.Name) {
									names[strings.ToLower(name.Name)] = true
								}
							}
						}
					}
				}
			}
		}
	}
	similarityStandardExports.Store(root, names)
	return names
}

// htmlVocabulary is HTML's own element, attribute and standard meta words: in
// an HTML file they are the language, like a keyword in code.
var htmlVocabulary = strings.Fields("doctype html head body meta link title script style noscript template lang charset name content href type rel src class defer async media viewport width device initial scale theme color icon image module stylesheet description")

// Library-qualified Go names, and HTML's vocabulary in an HTML file, are excluded
// from identifier comparison, while their tokens still participate in the
// shingle and consecutive-run checks.
func similarityLibraryNames(p string, data []byte) map[string]bool {
	names := map[string]bool{}
	if ext(p) == ".html" {
		for _, w := range htmlVocabulary {
			names[w] = true
		}
		return names
	}
	if ext(p) != ".go" {
		return names
	}
	file, _ := parser.ParseFile(token.NewFileSet(), p, data, parser.SkipObjectResolution)
	if file == nil {
		return names
	}
	aliases := map[string]bool{}
	for _, imp := range file.Imports {
		value, err := strconv.Unquote(imp.Path.Value)
		if err != nil || strings.Contains(strings.Split(value, "/")[0], ".") {
			continue
		}
		libraryRoot := filepath.Join(runtime.GOROOT(), "src", filepath.FromSlash(value))
		info, err := os.Stat(libraryRoot)
		if err != nil || !info.IsDir() {
			continue
		}
		alias := filepath.Base(value)
		if imp.Name != nil {
			alias = imp.Name.Name
		}
		if alias == "." {
			exports := similarityDotNames(libraryRoot)
			for name := range exports {
				names[name] = true
			}
			continue
		}
		aliases[alias] = true
		names[strings.ToLower(alias)] = true
	}
	ast.Inspect(file, func(node ast.Node) bool {
		if selector, ok := node.(*ast.SelectorExpr); ok {
			if ident, ok := selector.X.(*ast.Ident); ok && aliases[ident.Name] {
				names[strings.ToLower(selector.Sel.Name)] = true
			}
		}
		return true
	})
	return names
}

func (v similarityVocabulary) document(p string, data []byte) similarityDocument {
	doc := similarityDocument{path: p, shingles: map[[5]uint32]struct{}{}, identifiers: map[uint32]struct{}{}}
	libraries := similarityLibraryNames(p, data)
	for _, word := range similarityWords.FindAllString(strings.ToLower(string(data)), -1) {
		id := v[word]
		if id == 0 {
			id = uint32(len(v) + 1)
			v[word] = id
		}
		doc.tokens = append(doc.tokens, id)
		first, _ := utf8.DecodeRuneInString(word)
		if utf8.RuneCountInString(word) >= 4 && (unicode.IsLetter(first) || first == '_') && !similarityIgnored[word] && !libraries[word] {
			doc.identifiers[id] = struct{}{}
		}
	}
	for i := 0; i+5 <= len(doc.tokens); i++ {
		doc.shingles[[5]uint32(doc.tokens[i:i+5])] = struct{}{}
	}
	return doc
}

func similarityRunHashes(tokens []uint32, visit func(uint64, int)) {
	const width = 30
	const base uint64 = 1000003
	if len(tokens) < width {
		return
	}
	var hash uint64
	power := uint64(1)
	for i := 0; i < width; i++ {
		hash = hash*base + uint64(tokens[i])
		if i > 0 {
			power *= base
		}
	}
	visit(hash, 0)
	for i := width; i < len(tokens); i++ {
		hash = (hash-uint64(tokens[i-width])*power)*base + uint64(tokens[i])
		visit(hash, i-width+1)
	}
}

func newSimilarityIndex() *similarityIndex {
	return &similarityIndex{shingles: map[[5]uint32][]int{}, runs: map[uint64][]similarityRun{}, identifiers: map[uint32][]int{}}
}

func (index *similarityIndex) add(doc similarityDocument) {
	id := len(index.documents)
	index.documents = append(index.documents, doc)
	for shingle := range doc.shingles {
		index.shingles[shingle] = append(index.shingles[shingle], id)
	}
	for identifier := range doc.identifiers {
		index.identifiers[identifier] = append(index.identifiers[identifier], id)
	}
	similarityRunHashes(doc.tokens, func(hash uint64, offset int) {
		postings := index.runs[hash]
		// Store each distinct 30-token window once per reference file. The
		// automaton computes its exact longest run; duplicate offsets add no
		// information and would make repetitive content expensive to index.
		for i := len(postings) - 1; i >= 0 && postings[i].file == id; i-- {
			previous := postings[i].offset
			if slices.Equal(doc.tokens[offset:offset+30], doc.tokens[previous:previous+30]) {
				return
			}
		}
		index.runs[hash] = append(postings, similarityRun{id, offset})
	})
}

// A suffix automaton computes the exact longest common token run in linear
// time per pair, including highly repetitive files. Construct it lazily only
// when the 30-token hash index finds a possible pair.
type similarityState struct {
	length, link int
	next         map[uint32]int
}

type similarityAutomaton []similarityState

func newSimilarityAutomaton(tokens []uint32) similarityAutomaton {
	states := similarityAutomaton{{link: -1, next: map[uint32]int{}}}
	last := 0
	for _, word := range tokens {
		current := len(states)
		states = append(states, similarityState{length: states[last].length + 1, next: map[uint32]int{}})
		p := last
		for p >= 0 {
			if _, ok := states[p].next[word]; ok {
				break
			}
			states[p].next[word] = current
			p = states[p].link
		}
		if p >= 0 {
			q := states[p].next[word]
			if states[p].length+1 == states[q].length {
				states[current].link = q
			} else {
				clone := len(states)
				states = append(states, similarityState{length: states[p].length + 1, link: states[q].link, next: maps.Clone(states[q].next)})
				for p >= 0 && states[p].next[word] == q {
					states[p].next[word] = clone
					p = states[p].link
				}
				states[q].link, states[current].link = clone, clone
			}
		}
		last = current
	}
	return states
}

func (states similarityAutomaton) longest(tokens []uint32) int {
	state, length, best := 0, 0, 0
	for _, word := range tokens {
		for {
			if next, ok := states[state].next[word]; ok {
				state, length = next, length+1
				best = max(best, length)
				break
			}
			if state == 0 {
				length = 0
				break
			}
			state = states[state].link
			length = states[state].length
		}
	}
	return best
}

func (index *similarityIndex) compare(doc similarityDocument) []Finding {
	overlap := map[int]int{}
	identifiers := map[int]int{}
	longest := map[int]int{}
	for shingle := range doc.shingles {
		for _, id := range index.shingles[shingle] {
			overlap[id]++
		}
	}
	for identifier := range doc.identifiers {
		for _, id := range index.identifiers[identifier] {
			identifiers[id]++
		}
	}
	var automaton similarityAutomaton
	similarityRunHashes(doc.tokens, func(hash uint64, pos int) {
		for _, match := range index.runs[hash] {
			if longest[match.file] >= 30 {
				continue
			}
			old := index.documents[match.file].tokens
			if !slices.Equal(doc.tokens[pos:pos+30], old[match.offset:match.offset+30]) {
				continue
			}
			if automaton == nil {
				automaton = newSimilarityAutomaton(doc.tokens)
			}
			longest[match.file] = automaton.longest(old)
		}
	})
	var out []Finding
	for id, reference := range index.documents {
		shingleScore := 0.0
		if len(doc.shingles) > 0 {
			shingleScore = float64(overlap[id]) / float64(len(doc.shingles))
		}
		identifierScore := 0.0
		union := len(doc.identifiers) + len(reference.identifiers) - identifiers[id]
		if union > 0 {
			identifierScore = float64(identifiers[id]) / float64(union)
		}
		shingleHit := len(doc.shingles) >= 60 && shingleScore >= 0.15
		identifierHit := len(doc.identifiers) >= 20 && len(reference.identifiers) >= 20 && identifierScore >= 0.5
		if shingleHit || identifierHit || longest[id] >= 30 {
			runDetail := "no shared run of 30 tokens"
			if longest[id] >= 30 {
				runDetail = fmt.Sprintf("longest run %d tokens", longest[id])
			}
			out = append(out, Finding{Rule: ruleSimilarity, Path: doc.path, Detail: fmt.Sprintf("reference %s: shingle overlap %.3f (%d/%d), %s, identifier Jaccard %.3f (%d/%d identifiers); provenance review required", reference.path, shingleScore, overlap[id], len(doc.shingles), runDetail, identifierScore, len(doc.identifiers), len(reference.identifiers))})
		}
	}
	return out
}

func similarityExemptions(tree *Tree) (map[string]bool, error) {
	exemptions := map[string]bool{}
	findings, err := checkDocumentSources(tree)
	if err != nil {
		return nil, err
	}
	if len(findings) > 0 {
		return nil, fmt.Errorf("document manifest must pass before similarity exemptions apply")
	}
	if !tree.Has(docSourcesFile) {
		return exemptions, nil
	}
	data, err := tree.Read(docSourcesFile)
	if err != nil {
		return nil, err
	}
	var manifest docSources
	if err := json.Unmarshal(data, &manifest); err != nil {
		return nil, err
	}
	for _, entry := range manifest.Entries {
		if entry.Kind == kindAttributed {
			exemptions[entry.Destination] = true
		}
	}
	return exemptions, nil
}

func checkSimilarity(tree *Tree) ([]Finding, error) {
	value := strings.TrimSpace(os.Getenv(envSimilarity))
	if value == "" {
		if tree.Release {
			return nil, fmt.Errorf("%s is required for release", envSimilarity)
		}
		return nil, Skipped(envSimilarity + " is unset; reference similarity did NOT run")
	}
	exemptions, err := similarityExemptions(tree)
	if err != nil {
		return nil, err
	}
	target, err := filepath.EvalSymlinks(tree.Root)
	if err != nil {
		return nil, err
	}
	vocabulary := similarityVocabulary{}
	index := newSimilarityIndex()
	for ordinal, root := range filepath.SplitList(value) {
		if strings.TrimSpace(root) == "" {
			return nil, fmt.Errorf("reference %d is empty", ordinal+1)
		}
		root, err = filepath.Abs(root)
		if err != nil {
			return nil, err
		}
		root, err = filepath.EvalSymlinks(root)
		if err != nil {
			return nil, fmt.Errorf("reference %d cannot be read: %w", ordinal+1, err)
		}

		for _, pair := range [][2]string{{target, root}, {root, target}} {
			rel, err := filepath.Rel(pair[0], pair[1])
			if err == nil && (rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)))) {
				return nil, fmt.Errorf("reference %d must not overlap the checked tree", ordinal+1)
			}
		}
		reference, err := similarityReference(root)
		if err != nil {
			return nil, fmt.Errorf("reference %d cannot be loaded: %w", ordinal+1, err)
		}
		count := len(index.documents)
		for _, p := range reference.Paths {
			if !similaritySource(p) {
				continue
			}
			data, err := reference.Read(p)
			if err != nil {
				return nil, err
			}
			if bytes.IndexByte(data, 0) >= 0 {
				continue
			}
			doc := vocabulary.document(p, data)
			if len(doc.tokens) < 5 {
				continue
			}
			doc.path = fmt.Sprintf("[%d]/%s", ordinal+1, p)
			index.add(doc)
		}
		if len(index.documents) == count {
			return nil, fmt.Errorf("reference %d contains no comparable source, prompt, template or data files", ordinal+1)
		}
	}
	var out []Finding
	for _, p := range tree.Paths {
		if !similaritySource(p) || exemptions[p] {
			continue
		}
		data, err := tree.Read(p)
		if err != nil {
			return nil, err
		}
		if bytes.IndexByte(data, 0) >= 0 {
			continue
		}
		out = append(out, index.compare(vocabulary.document(p, data))...)
	}
	return out, nil
}
