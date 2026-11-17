package guard

import (
	"slices"
	"strings"
	"testing"
)

func TestCaptchaScope(t *testing.T) {
	provider := join("2", "captcha")
	setting := join("captcha", "_solver")
	other := join("anti", "captcha")
	imp := "import \"fixture/internal/captcha\"\n"
	tests := []struct {
		name  string
		files map[string]string
		want  []string // paths of the expected findings
	}{
		{"approved paths and prose are clean", map[string]string{
			"internal/captcha/s.go":     "// talks to " + provider + "\n",
			"internal/gates/captcha.go": "// the " + provider + " gate\n",
			"internal/workspace/p.go":   "const k = \"" + setting + "\"\n",
			"internal/apply/step.go":    imp + "// " + provider + "\n",
			"docs/architecture/c.md":    provider + " " + setting + "\n",
			"openspec/x/spec.md":        provider + "\n",
			"README.md":                 provider + "\n",
		}, nil},
		{"call site outside the list imports the solver", map[string]string{
			"internal/agent/tool.go": imp,
		}, []string{"internal/agent/tool.go"}},
		{"a test file outside the list imports the solver", map[string]string{
			"internal/agent/tool_test.go": "import (\n\t\"fixture/internal/captcha\"\n)\n",
		}, []string{"internal/agent/tool_test.go"}},
		{"provider named outside the list", map[string]string{
			"internal/agent/tool.go": "var u = \"https://api." + provider + ".com\"\n",
			"web/src/panel.tsx":      "const s = '" + setting + "'\n",
		}, []string{"internal/agent/tool.go", "web/src/panel.tsx"}},
		{"provider spelled in capitals outside the list", map[string]string{
			"cmd/applysmith/main.go": "// " + strings.ToUpper(provider) + "\n",
		}, []string{"cmd/applysmith/main.go"}},
		{"another provider anywhere", map[string]string{
			"internal/captcha/s.go": "// fall back to " + other + "\n",
			"docs/notes.md":         other + "\n",
		}, []string{"docs/notes.md", "internal/captcha/s.go"}},
		{"a similar directory name is not approved", map[string]string{
			"internal/captchaextra/s.go": provider + "\n",
		}, []string{"internal/captchaextra/s.go"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			found, err := checkCaptchaScope(dirTree(t, tc.files))
			if err != nil {
				t.Fatal(err)
			}
			var got []string
			for _, f := range found {
				if f.Rule != ruleCaptchaScope {
					t.Fatalf("rule = %s", f.Rule)
				}
				got = append(got, f.Path)
			}
			if !slices.Equal(slices.Sorted(slices.Values(got)), slices.Sorted(slices.Values(tc.want))) {
				t.Fatalf("findings = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestCaptchaScope_RegisteredAndFindingsNameTheFile(t *testing.T) {
	registered := false
	for _, r := range Rules() {
		registered = registered || r.Name == ruleCaptchaScope
	}
	if !registered {
		t.Fatal("the rule is not in Rules()")
	}
	found, _ := checkCaptchaScope(dirTree(t, map[string]string{"internal/agent/tool.go": "import \"fixture/internal/captcha\"\n"}))
	if len(found) != 1 || !strings.Contains(found[0].String(), "internal/agent/tool.go") {
		t.Fatalf("findings = %v", found)
	}
}
