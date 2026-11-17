package guard

import (
	"strings"
	"testing"
)

func TestParseLayers(t *testing.T) {
	layers, problems := parseLayers("# header\ninternal/a 10 pure # trailing\ninternal/b 20\n\n")
	if len(problems) != 0 || len(layers) != 2 || !layers["internal/a"].pure || layers["internal/b"].n != 20 {
		t.Fatalf("layers=%v problems=%v", layers, problems)
	}
	for _, bad := range []string{"internal/a", "internal/a -1", "internal/a 1\ninternal/a 2", "a 1 pure extra"} {
		if _, problems := parseLayers(bad); len(problems) == 0 {
			t.Errorf("%q should be rejected", bad)
		}
	}
}

const testModule = "fixture"

func pkg(dir string, imports ...string) pkgInfo { return pkgInfo{Dir: dir, Imports: imports} }

func TestLayerGraph(t *testing.T) {
	layers, _ := parseLayers("internal/low 10\ninternal/mid 20\ninternal/high 30\ninternal/same 20\n")
	tests := []struct {
		name string
		pkgs []pkgInfo
		want string // substring of the one expected finding; empty means clean
	}{
		{"downward import", []pkgInfo{pkg("internal/high", testModule+"/internal/low", testModule+"/internal/mid")}, ""},
		{"upward import", []pkgInfo{pkg("internal/low", testModule+"/internal/high")}, "layer 10 imports internal/high at layer 30"},
		{"same layer import", []pkgInfo{pkg("internal/mid", testModule+"/internal/same")}, "strictly lower"},
		{"unlisted package", []pkgInfo{pkg("internal/newthing")}, "not listed"},
		{"import of an unlisted package", []pkgInfo{pkg("internal/high", testModule+"/internal/ghost")}, "internal/ghost, which is not listed"},
		{"internal imports cmd", []pkgInfo{pkg("internal/high", testModule+"/cmd/tool")}, "must not import cmd/ or app/"},
		{"internal imports app", []pkgInfo{pkg("internal/high", testModule+"/app")}, "must not import cmd/ or app/"},
		{"framework in internal", []pkgInfo{pkg("internal/high", frameworkPkg)}, "desktop framework"},
		{"framework subpackage in cmd", []pkgInfo{pkg("cmd/applysmith", frameworkPkg+"/cmd/mygo")}, "desktop framework"},
		{"framework in a test of internal", []pkgInfo{{Dir: "internal/high", TestImports: []string{frameworkPkg}}}, "desktop framework"},
		{"framework in app", []pkgInfo{pkg("app", frameworkPkg)}, ""},
		{"standard library and third party", []pkgInfo{pkg("internal/low", "fmt", "gopkg.in/yaml.v3")}, ""},
		{"packages outside internal are unlisted-free", []pkgInfo{pkg("web"), pkg("cmd/applysmith", testModule+"/internal/high")}, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fs := checkLayerGraph(testModule, layers, tc.pkgs)
			if tc.want == "" {
				if len(fs) != 0 {
					t.Fatalf("findings = %v", fs)
				}
				return
			}
			if len(fs) != 1 || !strings.Contains(fs[0].Detail, tc.want) {
				t.Fatalf("findings = %v, want %q", fs, tc.want)
			}
		})
	}
}

func TestPureGraph(t *testing.T) {
	layers, _ := parseLayers("internal/gates 10 pure\ninternal/match 15 pure\ninternal/store 30\n")
	tests := []struct {
		name string
		pkg  pkgInfo
		want string
	}{
		{"clock import", pkg("internal/gates", "time"), "imports time"},
		{"input output", pkg("internal/gates", "os"), "imports os"},
		{"randomness", pkg("internal/match", "math/rand/v2"), "imports math/rand/v2"},
		{"network", pkg("internal/match", "net/http"), "imports net/http"},
		{"impure internal package", pkg("internal/match", testModule+"/internal/store"), "not on the pure list"},
		{"pure internal package", pkg("internal/match", testModule+"/internal/gates", "strings", "regexp"), ""},
		{"test imports are exempt", pkgInfo{Dir: "internal/gates", TestImports: []string{"testing", "os"}}, ""},
		{"packages not marked pure are not checked", pkg("internal/store", "os", "time"), ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fs := checkPureGraph(testModule, layers, []pkgInfo{tc.pkg})
			if tc.want == "" && len(fs) != 0 || tc.want != "" && (len(fs) != 1 || !strings.Contains(fs[0].Detail, tc.want)) {
				t.Fatalf("findings = %v, want %q", fs, tc.want)
			}
		})
	}
}

// TestLayersOnRealModule runs go list over a throwaway module.
func TestLayersOnRealModule(t *testing.T) {
	files := map[string]string{
		"go.mod":                    "module " + testModule + "\n\ngo 1.27.1\n",
		"internal/guard/layers.txt": "internal/store 10\ninternal/api 20\n",
		"internal/store/store.go":   "package store\n\nimport _ \"" + testModule + "/internal/api\"\n",
		"internal/api/api.go":       "package api\n",
		"internal/extra/extra.go":   "package extra\n",
	}
	tree := dirTree(t, files)
	fs, err := checkLayers(tree)
	if err != nil {
		t.Fatal(err)
	}
	var details []string
	for _, f := range fs {
		details = append(details, f.Path+": "+f.Detail)
	}
	joined := strings.Join(details, "\n")
	if len(fs) != 2 || !strings.Contains(joined, "internal/store: layer 10 imports internal/api at layer 20") || !strings.Contains(joined, "internal/extra: package is not listed") {
		t.Fatalf("findings:\n%s", joined)
	}
}
