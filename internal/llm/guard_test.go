package llm

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func sourceFiles(t *testing.T) []string {
	t.Helper()
	all, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, f := range all {
		if !strings.HasSuffix(f, "_test.go") {
			out = append(out, f)
		}
	}
	return out
}

// Provider keys have one path to them: the credentials vault. This package
// must not reach the keychain or a key environment variable itself.
func TestLLM_Credentials_NoSecondVaultPath(t *testing.T) {
	fset := token.NewFileSet()
	for _, f := range sourceFiles(t) {
		file, err := parser.ParseFile(fset, f, nil, parser.ImportsOnly)
		if err != nil {
			t.Fatal(err)
		}
		for _, imp := range file.Imports {
			if strings.Contains(imp.Path.Value, "keyring") {
				t.Errorf("%s imports %s: keychain access belongs to internal/vault", f, imp.Path.Value)
			}
		}
		src, _ := os.ReadFile(f)
		for _, banned := range []string{"APPLYSMITH_SECRET", "os.Getenv(\"APPLYSMITH_", "/.config/"} {
			if strings.Contains(string(src), banned) {
				t.Errorf("%s contains %q: secrets and their overrides are the vault's", f, banned)
			}
		}
	}
}

// The thinking level is a request parameter only: only the files that build a
// request or resolve a row may read it, so no budget, timeout or policy depends
// on it.
func TestLLM_Guard_NoPolicyReadsThinking(t *testing.T) {
	allowed := map[string]bool{"call.go": true, "openai.go": true, "anthropic.go": true, "chatgpt_client.go": true, "gateway.go": true, "resolve.go": true,
		"settings.go": true, "tasks.go": true, "presets.go": true}
	for _, f := range sourceFiles(t) {
		src, _ := os.ReadFile(f)
		if !allowed[f] && (strings.Contains(string(src), ".Thinking") || strings.Contains(string(src), ".thinking")) {
			t.Errorf("%s reads the thinking level", f)
		}
	}
}
