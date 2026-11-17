package guard

import (
	"fmt"
	"math/rand/v2"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

const registerHeader = "| ID | Paths | Origin and licence | Treatment | Spec | Review | Status |\n|---|---|---|---|---|---|---|\n"

func register(rows ...string) string {
	return "## Register\n\n" + registerHeader + strings.Join(rows, "\n") + "\n"
}

func TestParseProvenance(t *testing.T) {
	rows, problems := parseProvenance(register(
		"| CR-A | `internal/a/**` `internal/b/*.go` | Origin X (MIT) | clean-room | job-discovery | pending | planned |",
		"| DEP-B | `web/src/ui/**` | Library (MIT) | dependency | dashboard-ui | pending | implemented |",
	))
	if len(problems) != 0 || len(rows) != 2 || len(rows[0].Globs) != 2 || rows[1].Status != "implemented" {
		t.Fatalf("rows=%+v problems=%v", rows, problems)
	}
	bad := map[string]string{
		"| A | `x` | o | clean-room | s | r |":                "7 columns",
		"| A | no globs | o | clean-room | s | r | planned |": "no path glob",
		"| A | `x` | o | borrowed | s | r | planned |":        "unknown treatment",
		"| A | `x` | o | clean-room | s | r | done |":         "planned or implemented",
		"| A | `x` | | clean-room | s | r | planned |":        "origin, spec and review",
		"| bad id | `x` | o | clean-room | s | r | planned |": "identifier",
	}
	for row, want := range bad {
		if _, problems := parseProvenance(register(row)); len(problems) != 1 || !strings.Contains(problems[0].Detail, want) {
			t.Errorf("%q: problems = %v, want %q", row, problems, want)
		}
	}
	if _, problems := parseProvenance(register(
		"| A | `x` | o | clean-room | s | r | planned |", "| A | `y` | o | clean-room | s | r | planned |")); len(problems) != 1 || !strings.Contains(problems[0].Detail, "duplicate") {
		t.Errorf("duplicate id: %v", problems)
	}
}

func provenanceFindings(t *testing.T, files map[string]string) []Finding {
	t.Helper()
	fs, err := checkProvenance(dirTree(t, files))
	if err != nil {
		t.Fatal(err)
	}
	return fs
}

func TestProvenanceChecks(t *testing.T) {
	header := func(id string) string { return "// " + cleanRoomHeader + id + "\npackage a\n" }
	reg := func(status string) string {
		return register("| CR-A | `internal/a/**` | Origin (MIT) | clean-room | job-discovery | pending | " + status + " |")
	}
	t.Run("header present", func(t *testing.T) {
		if fs := provenanceFindings(t, map[string]string{provenanceFile: reg("implemented"), "internal/a/x.go": header("CR-A")}); len(fs) != 0 {
			t.Fatal(fs)
		}
	})
	t.Run("missing header names the file", func(t *testing.T) {
		fs := provenanceFindings(t, map[string]string{provenanceFile: reg("planned"), "internal/a/x.go": "package a\n", "internal/a/data.json": "{}"})
		if len(fs) != 1 || fs[0].Path != "internal/a/x.go" || !strings.Contains(fs[0].Detail, "lacks the header") {
			t.Fatal(fs)
		}
	})
	t.Run("header beyond the first lines does not count", func(t *testing.T) {
		src := "package a\n\n\n\n\n\n// " + cleanRoomHeader + "CR-A\n"
		if fs := provenanceFindings(t, map[string]string{provenanceFile: reg("planned"), "internal/a/x.go": src}); len(fs) != 1 {
			t.Fatal(fs)
		}
	})
	t.Run("planned row may match nothing", func(t *testing.T) {
		if fs := provenanceFindings(t, map[string]string{provenanceFile: reg("planned")}); len(fs) != 0 {
			t.Fatal(fs)
		}
	})
	t.Run("implemented row matching nothing fails", func(t *testing.T) {
		fs := provenanceFindings(t, map[string]string{provenanceFile: reg("implemented")})
		if len(fs) != 1 || !strings.Contains(fs[0].Detail, "CR-A") {
			t.Fatal(fs)
		}
	})
	t.Run("implemented row with a glob matching nothing fails", func(t *testing.T) {
		two := register("| CR-A | `internal/a/**` `internal/gone/*.go` | Origin (MIT) | clean-room | job-discovery | pending | implemented |")
		fs := provenanceFindings(t, map[string]string{provenanceFile: two, "internal/a/x.go": header("CR-A")})
		if len(fs) != 1 || !strings.Contains(fs[0].Detail, "internal/gone/*.go") {
			t.Fatal(fs)
		}
	})
	t.Run("header naming an unknown row", func(t *testing.T) {
		fs := provenanceFindings(t, map[string]string{provenanceFile: reg("planned"), "internal/z/x.go": header("CR-NOPE")})
		if len(fs) != 1 || !strings.Contains(fs[0].Detail, "CR-NOPE") {
			t.Fatal(fs)
		}
	})
	t.Run("vendor directory", func(t *testing.T) {
		fs := provenanceFindings(t, map[string]string{provenanceFile: reg("planned"), "vendor/x/y.go": "package y\n"})
		if len(fs) != 1 || !strings.Contains(fs[0].Detail, "vendor") {
			t.Fatal(fs)
		}
	})
	t.Run("source under third_party", func(t *testing.T) {
		fs := provenanceFindings(t, map[string]string{provenanceFile: reg("planned"), "third_party/lib/x.go": "package x\n", "third_party/licenses/MIT.txt": "text", "third_party/document-sources.json": "{}"})
		if len(fs) != 1 || fs[0].Path != "third_party/lib/x.go" {
			t.Fatal(fs)
		}
	})
	t.Run("missing register", func(t *testing.T) {
		if fs := provenanceFindings(t, map[string]string{"a.txt": "x"}); len(fs) != 1 || !strings.Contains(fs[0].Detail, "missing") {
			t.Fatal(fs)
		}
	})
}

func TestSimilarityEntryPoint_ReleaseRequiresReferences(t *testing.T) {
	t.Setenv("APPLYSMITH_SIMILARITY_REFERENCES", "")
	tree := dirTree(t, map[string]string{"source.go": "package example\n"})
	tree.Release = true
	for _, rule := range Rules() {
		if rule.Name != "similarity" {
			continue
		}
		if _, err := rule.Check(tree); err == nil || IsSkipped(err) {
			t.Fatal("release similarity must fail when references are absent")
		}
		return
	}
	t.Fatal("the repository guard has no similarity rule")
}

func similaritySequence(prefix string, start, count int) string {
	words := make([]string, count)
	for i := range words {
		words[i] = fmt.Sprintf("%s%04d", prefix, start+i)
	}
	return strings.Join(words, " ")
}

func similarityFixture(t *testing.T, content string, references map[string]string) []Finding {
	t.Helper()
	tree := dirTree(t, map[string]string{"new.go": content})
	reference := dirTree(t, references)
	before := map[string]string{}
	for _, p := range reference.Paths {
		data, err := reference.Read(p)
		if err != nil {
			t.Fatal(err)
		}
		before[p] = string(data)
	}
	t.Setenv(envSimilarity, reference.Root)
	found, err := checkSimilarity(tree)
	if err != nil {
		t.Fatal(err)
	}
	for p, want := range before {
		data, err := reference.Read(p)
		if err != nil || string(data) != want {
			t.Fatal("the read-only reference changed")
		}
	}
	if _, err := os.Stat(filepath.Join(reference.Root, ".git")); !os.IsNotExist(err) {
		t.Fatal("the directory reader must not create history in a reference")
	}
	return found
}

func TestSimilarityThresholds(t *testing.T) {
	t.Run("forty copied tokens including comments", func(t *testing.T) {
		content := similaritySequence("", 1000, 40)
		found := similarityFixture(t, "// "+content, map[string]string{"old.ts": content})
		if len(found) != 1 || !strings.Contains(found[0].Detail, "longest run 40 tokens") {
			t.Fatalf("copied block: %v", found)
		}
		if strings.Contains(fmt.Sprint(found), "1000 1001") {
			t.Fatal("source content leaked into a finding")
		}
	})
	t.Run("twenty nine tokens is below the run threshold", func(t *testing.T) {
		content := similaritySequence("", 1000, 29)
		if found := similarityFixture(t, content, map[string]string{"old.ts": content}); len(found) != 0 {
			t.Fatalf("below run threshold: %v", found)
		}
	})
	for _, shared := range []int{18, 19} {
		t.Run(fmt.Sprintf("shingle overlap with %d shared tokens", shared), func(t *testing.T) {
			content := similaritySequence("", 1000, 104)
			reference := similaritySequence("", 1000, shared) + " " + similaritySequence("", 9000, 100)
			found := similarityFixture(t, content, map[string]string{"old.ts": reference})
			if (len(found) == 1) != (shared == 19) {
				t.Fatalf("15 percent boundary: %v", found)
			}
			if shared == 19 && !strings.Contains(found[0].Detail, "0.150 (15/100)") {
				t.Fatalf("incorrect shingle score: %v", found)
			}
		})
	}
	t.Run("fewer than sixty shingles", func(t *testing.T) {
		found := similarityFixture(t, similaritySequence("", 1000, 63), map[string]string{"old.ts": similaritySequence("", 1000, 20)})
		if len(found) != 0 {
			t.Fatalf("minimum shingle count: %v", found)
		}
	})
	t.Run("overlap is per reference file", func(t *testing.T) {
		found := similarityFixture(t, similaritySequence("", 1000, 104), map[string]string{"one.ts": similaritySequence("", 1000, 10), "two.ts": similaritySequence("", 1030, 10), "three.ts": similaritySequence("", 1060, 10)})
		if len(found) != 0 {
			t.Fatalf("separate files were aggregated: %v", found)
		}
	})
	for _, common := range []int{13, 14} {
		t.Run(fmt.Sprintf("identifier overlap with %d shared names", common), func(t *testing.T) {
			content := similaritySequence("domain_", 0, 20)
			words := strings.Fields(similaritySequence("domain_", 0, common))
			slices.Reverse(words)
			reference := strings.Join(words, " ") + " " + similaritySequence("foreign_", 0, 22-common)
			found := similarityFixture(t, content, map[string]string{"old.ts": reference})
			if (len(found) == 1) != (common == 14) {
				t.Fatalf("identifier boundary: %v", found)
			}
			if common == 14 && !strings.Contains(found[0].Detail, "identifier Jaccard 0.500") {
				t.Fatalf("incorrect identifier score: %v", found)
			}
		})
	}
	t.Run("nineteen identifiers is below the threshold", func(t *testing.T) {
		words := strings.Fields(similaritySequence("domain_", 0, 19))
		slices.Reverse(words)
		found := similarityFixture(t, similaritySequence("domain_", 0, 19), map[string]string{"old.ts": strings.Join(words, " ") + " other_name"})
		if len(found) != 0 {
			t.Fatalf("identifier minimum: %v", found)
		}
	})
	t.Run("case is ignored", func(t *testing.T) {
		content := similaritySequence("unusual_", 0, 40)
		found := similarityFixture(t, strings.ToUpper(content), map[string]string{"old.ts": content})
		if len(found) != 1 {
			t.Fatalf("case-normalized copy: %v", found)
		}
	})
}

func TestSimilarityIdentifiersExcludeKeywordsAndLibraryNames(t *testing.T) {
	vocabulary := similarityVocabulary{}
	doc := vocabulary.document("new.go", []byte("package sample\nimport (\"net/http\"; \"strings\")\nfunc main() { _ = http.NewRequest; _ = strings.NewReader }\n"))
	for _, word := range []string{"package", "import", "func", "http", "strings", "newrequest", "newreader"} {
		if _, ok := doc.identifiers[vocabulary[word]]; ok {
			t.Fatalf("library or keyword counted: %s", word)
		}
	}
	if _, ok := doc.identifiers[vocabulary["sample"]]; !ok {
		t.Fatal("domain identifiers must survive")
	}
}

func TestSimilarityReferenceInputs(t *testing.T) {
	t.Run("development skip is explicit", func(t *testing.T) {
		t.Setenv(envSimilarity, "")
		_, err := checkSimilarity(dirTree(t, nil))
		if !IsSkipped(err) {
			t.Fatalf("missing reference: %v", err)
		}
	})
	for _, kind := range []string{"missing", "empty", "self", "nested", "ancestor", "file"} {
		t.Run(kind, func(t *testing.T) {
			tree := dirTree(t, map[string]string{"source.go": "package sample\n"})
			root := t.TempDir()
			switch kind {
			case "missing":
				root = filepath.Join(root, "missing")
			case "self":
				root = tree.Root
			case "nested":
				root = filepath.Join(tree.Root, "reference")
				if err := os.Mkdir(root, 0755); err != nil {
					t.Fatal(err)
				}
			case "ancestor":
				root = filepath.Dir(tree.Root)
			case "file":
				root = filepath.Join(root, "file")
				if err := os.WriteFile(root, []byte("data"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			t.Setenv(envSimilarity, root)
			_, err := checkSimilarity(tree)
			if err == nil || IsSkipped(err) {
				t.Fatalf("invalid configured reference must fail: %v", err)
			}
		})
	}
	t.Run("multiple roots and git reference", func(t *testing.T) {
		content := similaritySequence("", 1000, 40)
		first := dirTree(t, map[string]string{"first.ts": content})
		second := gitRepo(t, map[string]string{"second.go": content})
		t.Setenv(envSimilarity, strings.Join([]string{first.Root, second}, string(os.PathListSeparator)))
		found, err := checkSimilarity(dirTree(t, map[string]string{"source.go": content}))
		if err != nil || len(found) != 2 || !strings.Contains(found[0].Detail, "[1]/first.ts") || !strings.Contains(found[1].Detail, "[2]/second.go") {
			t.Fatalf("multiple reference roots: %v, %v", found, err)
		}
	})
	t.Run("supplied ignored source is compared but binaries are not", func(t *testing.T) {
		content := similaritySequence("", 1000, 40)
		found := similarityFixture(t, content, map[string]string{".gitignore": "ignored.ts\n", "ignored.ts": content, "binary.ts": "\x00" + content, "unrelated.go": similaritySequence("", 9000, 40)})
		if len(found) != 1 || !strings.Contains(found[0].Detail, "ignored.ts") {
			t.Fatalf("supplied ignored source was omitted: %v", found)
		}
	})
}

func TestSimilarityAttributionExemptsOnlyAnExactValidatedFile(t *testing.T) {
	content := similaritySequence("domain_", 0, 70)
	reference := dirTree(t, map[string]string{"upstream.py": content})
	t.Setenv(envSimilarity, reference.Root)
	makeTree := func(neighbor, broken bool) *Tree {
		return docFiles(t, func(manifest *docSources, files map[string]string) {
			dest := manifest.Entries[0].Destination
			files[dest] = content
			manifest.Entries[0].ShippedSHA256 = hexOf(content)
			if neighbor {
				files["neighbor.py"] = content
			}
			if broken {
				manifest.Entries[0].ShippedSHA256 = hexOf("different")
			}
		})
	}
	found, err := checkSimilarity(makeTree(false, false))
	if err != nil || len(found) != 0 {
		t.Fatalf("validated attribution: %v, %v", found, err)
	}
	found, err = checkSimilarity(makeTree(true, false))
	if err != nil || len(found) != 1 || found[0].Path != "neighbor.py" {
		t.Fatalf("neighbor must still be compared: %v, %v", found, err)
	}
	if _, err := checkSimilarity(makeTree(false, true)); err == nil {
		t.Fatal("an invalid manifest cannot exempt copied source")
	}
}

func TestSimilarityRunHashCollisionStillRequiresExactTokens(t *testing.T) {
	vocabulary := similarityVocabulary{}
	old := vocabulary.document("old.go", []byte(similaritySequence("", 1000, 40)))
	fresh := vocabulary.document("new.go", []byte(similaritySequence("", 9000, 40)))
	index := newSimilarityIndex()
	index.add(old)
	similarityRunHashes(fresh.tokens, func(hash uint64, _ int) { index.runs[hash] = []similarityRun{{0, 0}} })
	if found := index.compare(fresh); len(found) != 0 {
		t.Fatalf("hash collision became a false copied run: %v", found)
	}
}

func TestSimilarityProductFormats(t *testing.T) {
	content := similaritySequence("", 1000, 40)
	for _, path := range []string{"internal/prompts/templates/deep-eval.tmpl", "internal/agent/pirpc/testdata/turn-abort.jsonl", "internal/api/testdata/remote-routes.golden", "internal/future.unknown", "docs/example.py", "templates/mail.tpl", "Makefile", "go.mod"} {
		t.Run(path, func(t *testing.T) {
			t.Setenv(envSimilarity, dirTree(t, map[string]string{path: content}).Root)
			found, err := checkSimilarity(dirTree(t, map[string]string{path: content}))
			if err != nil || len(found) != 1 {
				t.Fatalf("format omitted: %v, %v", found, err)
			}
		})
	}
}

func TestSimilarityDotImportedStandardLibrary(t *testing.T) {
	words := strings.Fields("Append Appendf Appendln Errorf FormatString Fprint Fprintf Fprintln Fscan Fscanf Fscanln Print Printf Println Scan Scanf Scanln Sprint Sprintf Sprintln Sscan Sscanf Sscanln")
	vocabulary := similarityVocabulary{}
	content := "package domain\nimport . \"fmt\"\nvar examples = []any{" + strings.Join(words, ",") + "}"
	doc := vocabulary.document("new.go", []byte(content))
	for _, word := range words {
		if _, ok := doc.identifiers[vocabulary[strings.ToLower(word)]]; ok {
			t.Fatalf("dot-import name counted: %s", word)
		}
	}
}

// TestSimilarityRepeatedContentBudget and the partial one below feed 400,000
// tokens: a quadratic matcher would need about 10^11 steps and outlive the
// package's test timeout, so no wall-clock bound is needed on a loaded machine.
func TestSimilarityRepeatedContentBudget(t *testing.T) {
	content := strings.Repeat("1 ", 400000)
	found := similarityFixture(t, content, map[string]string{"old.ts": content})
	if len(found) != 1 || !strings.Contains(found[0].Detail, "longest run 400000 tokens") {
		t.Fatalf("repeated run: %v", found)
	}
}

func TestSimilarityPartialRepetitionBudget(t *testing.T) {
	found := similarityFixture(t, strings.Repeat("1 ", 200000)+strings.Repeat("2 ", 200000), map[string]string{"old.ts": strings.Repeat("1 ", 400000)})
	if len(found) != 1 || !strings.Contains(found[0].Detail, "longest run 200000 tokens") {
		t.Fatalf("partial repetition: %v", found)
	}
}

func TestSimilarityAutomatonMatchesBruteForce(t *testing.T) {
	random := rand.New(rand.NewPCG(17, 29))
	for trial := 0; trial < 300; trial++ {
		a, b := make([]uint32, random.IntN(90)), make([]uint32, random.IntN(90))
		for i := range a {
			a[i] = uint32(random.IntN(5) + 1)
		}
		for i := range b {
			b[i] = uint32(random.IntN(5) + 1)
		}
		want := 0
		for i := range a {
			for j := range b {
				run := 0
				for i+run < len(a) && j+run < len(b) && a[i+run] == b[j+run] {
					run++
				}
				want = max(want, run)
			}
		}
		if got := newSimilarityAutomaton(a).longest(b); got != want {
			t.Fatalf("trial %d: got %d, want %d", trial, got, want)
		}
	}
}

func TestSimilarityIndexPreservesRealHashCollisions(t *testing.T) {
	a, b := make([]uint32, 30), make([]uint32, 30)
	for i := range a {
		a[i], b[i] = 3, 3
	}
	a[0], a[1], b[0], b[1] = 1, 1000004, 2, 1
	var firstHash, secondHash uint64
	similarityRunHashes(a, func(hash uint64, _ int) { firstHash = hash })
	similarityRunHashes(b, func(hash uint64, _ int) { secondHash = hash })
	if firstHash != secondHash {
		t.Fatal("fixture must contain an actual rolling-hash collision")
	}
	old := append(slices.Clone(a), 7)
	old = append(old, b...)
	old = append(old, 8)
	old = append(old, a...)
	index := newSimilarityIndex()
	index.add(similarityDocument{path: "old.go", tokens: old})
	index.add(similarityDocument{path: "other.go", tokens: a})
	if len(index.runs[firstHash]) != 3 {
		t.Fatal("dedup discarded a distinct window or reference file")
	}
	found := index.compare(similarityDocument{path: "new.go", tokens: b})
	if len(found) != 1 || !strings.Contains(found[0].Detail, "old.go") {
		t.Fatalf("collision caused a false pair: %v", found)
	}
}
