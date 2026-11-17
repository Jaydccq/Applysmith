package mcpserver

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// The package may import only the wire types, the vault (token) and the
// workspace (lock file path). Nothing that writes, applies, submits or opens
// the database is reachable from it.
func TestBoundary_OnlyReadServicesAreLinked(t *testing.T) {
	out, err := exec.Command("go", "list", "-deps", "-f", "{{.ImportPath}}", ".").Output()
	if err != nil {
		t.Fatalf("go list: %v", err)
	}
	var internal []string
	for _, p := range strings.Fields(string(out)) {
		if strings.HasPrefix(p, "applysmith/") && p != "applysmith/internal/mcpserver" {
			internal = append(internal, p)
		}
	}
	slices.Sort(internal)
	want := []string{"applysmith/internal/api/wire", "applysmith/internal/lazyregexp", "applysmith/internal/vault", "applysmith/internal/workspace"}
	if !slices.Equal(internal, want) {
		t.Errorf("internal dependencies = %v, want exactly %v", internal, want)
	}
}

// No source file of the package can listen, write files, run programs or open
// a database.
func TestBoundary_NoWriteOrListenCalls(t *testing.T) {
	files, _ := filepath.Glob("*.go")
	banned := []string{"net.Listen", "ListenAndServe", "os.WriteFile", "os.Create", "os.OpenFile", "os.Remove", "os.Mkdir",
		"exec.Command", "sql.Open", "http.MethodPost", "http.MethodPut", "http.MethodPatch", "http.MethodDelete", "http.Post"}
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for _, call := range banned {
			if strings.Contains(string(b), call) {
				t.Errorf("%s uses %s", f, call)
			}
		}
	}
}
