package bundler

import (
	"bytes"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"applysmith/internal/engine/stamp"
)

const engineRoot = "../../../engine"

// copyEngine copies the engine sources into a fresh directory, which holds no
// node_modules and so proves the build needs none.
func copyEngine(t *testing.T) string {
	t.Helper()
	dst := t.TempDir()
	for _, name := range []string{"src", "tsconfig.json"} {
		err := filepath.WalkDir(filepath.Join(engineRoot, name), func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			rel, _ := filepath.Rel(engineRoot, p)
			if d.IsDir() {
				return os.MkdirAll(filepath.Join(dst, rel), 0o755)
			}
			data, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			return os.WriteFile(filepath.Join(dst, rel), data, 0o644)
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	return dst
}

func edit(t *testing.T, root, file, old, repl string) {
	t.Helper()
	p := filepath.Join(root, "src", file)
	data, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(data, []byte(old)) {
		t.Fatalf("%s does not contain %q", file, old)
	}
	if err := os.WriteFile(p, bytes.Replace(data, []byte(old), []byte(repl), 1), 0o644); err != nil {
		t.Fatal(err)
	}
}

// The copies hold no node_modules and PATH is empty: the build needs neither.
func TestBuildIsDeterministicAcrossLocations(t *testing.T) {
	t.Setenv("PATH", "")
	a, err := Build(copyEngine(t))
	if err != nil {
		t.Fatal(err)
	}
	b, err := Build(copyEngine(t))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(a, b) {
		t.Fatal("same sources gave different bytes")
	}
	sa, err := stamp.Of(a)
	if err != nil {
		t.Fatal(err)
	}
	sb, _ := stamp.Of(b)
	if sa != sb {
		t.Fatalf("stamps differ: %s %s", sa, sb)
	}
}

func TestStampFollowsSource(t *testing.T) {
	root := copyEngine(t)
	before, err := Build(root)
	if err != nil {
		t.Fatal(err)
	}
	edit(t, root, "engine.ts", `stamp = "dev"`, `stamp = "dex"`)
	after, err := Build(root)
	if err != nil {
		t.Fatal(err)
	}
	s1, err := stamp.Of(before)
	if err != nil {
		t.Fatal(err)
	}
	s2, err := stamp.Of(after)
	if err != nil {
		t.Fatal(err)
	}
	if s1 == s2 {
		t.Fatalf("one changed character left the stamp at %s", s1)
	}
}

func TestBundleShape(t *testing.T) {
	out, err := Build(copyEngine(t))
	if err != nil {
		t.Fatal(err)
	}
	s := string(out)
	if head := s[:40]; !strings.Contains(head, "(() => {") {
		t.Fatalf("bundle is not an IIFE: starts %q", head)
	}
	st, err := stamp.Of(out)
	if err != nil {
		t.Fatal(err)
	}
	if want := "\n__applysmithEngine.setStamp(\"" + st + "\");\n"; !strings.HasSuffix(s, want) {
		t.Fatalf("trailer is %q, want suffix %q", s[len(s)-60:], want)
	}
	if strings.Contains(s, "sourceMappingURL") {
		t.Fatal("bundle references a source map")
	}
	for _, banned := range []string{"eval(", "new Function(", "importScripts(", "fetch(", "XMLHttpRequest"} {
		if strings.Contains(s, banned) {
			t.Fatalf("bundle contains %q", banned)
		}
	}
}

func TestSizeBudget(t *testing.T) {
	root := copyEngine(t)
	big := strings.Repeat("0123456789abcdef", 104858) // 1.6 MiB
	if err := os.WriteFile(filepath.Join(root, "src", "ballast.ts"), []byte("export const ballast = \""+big+"\";\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	edit(t, root, "entry.ts", "install(globalThis as unknown as Win);", "install(globalThis as unknown as Win);\nconsole.log(ballast.length);")
	edit(t, root, "entry.ts", `import { bindingHost } from "./host";`, "import { bindingHost } from \"./host\";\nimport { ballast } from \"./ballast\";")
	_, err := Build(root)
	if err == nil {
		t.Fatal("oversize bundle was accepted")
	}
	for _, want := range []string{"budget is 1572864 bytes", "bundle is "} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error %q lacks %q", err, want)
		}
	}
}

func TestBuildReportsSourceErrors(t *testing.T) {
	root := copyEngine(t)
	edit(t, root, "entry.ts", `import { bindingHost } from "./host";`, `import { bindingHost } from "./missing";`)
	if _, err := Build(root); err == nil || !strings.Contains(err.Error(), "missing") {
		t.Fatalf("err = %v", err)
	}
}
