package guard

import (
	"fmt"
	"strings"
	"testing"
)

func allowTree(t *testing.T) *Tree {
	return dirTree(t, map[string]string{"docs/a.md": "x", "docs/b.md": "x", "testdata/intl/mixed.txt": "x", "internal/guard/allow.yml": ""})
}

func TestAllowlistValidation(t *testing.T) {
	tests := []struct {
		name, yaml, want string
	}{
		{"empty list", "entries: []\n", ""},
		{"comment only", "# nothing\n", ""},
		{"valid entry", "entries:\n  - {rule: email, file: docs/a.md, value: x, reason: documentation sample}\n", ""},
		{"stale file", "entries:\n  - {rule: email, file: docs/gone.md, value: x, reason: documentation sample}\n", "does not exist"},
		{"wildcard", "entries:\n  - {rule: email, file: 'docs/*.md', value: x, reason: documentation sample}\n", "one exact path"},
		{"other directory", "entries:\n  - {rule: email, file: docs/, value: x, reason: documentation sample}\n", "one exact path"},
		{"token rule", "entries:\n  - {rule: pii-tokens, file: docs/a.md, value: x, reason: documentation sample}\n", "cannot be allowlisted"},
		{"retired rule", "entries:\n  - {rule: retired-names, file: docs/a.md, value: x, reason: documentation sample}\n", "cannot be allowlisted"},
		{"internal-material rule", "entries:\n  - {rule: internal-material, file: docs/a.md, value: x, reason: documentation sample}\n", "cannot be allowlisted"},
		{"secret rule", "entries:\n  - {rule: secret, file: docs/a.md, value: x, reason: documentation sample}\n", "cannot be allowlisted"},
		{"external secret rule", "entries:\n  - {rule: gitleaks, file: docs/a.md, value: x, reason: documentation sample}\n", "cannot be allowlisted"},
		{"unknown rule", "entries:\n  - {rule: nope, file: docs/a.md, value: x, reason: documentation sample}\n", "unknown rule"},
		{"short reason", "entries:\n  - {rule: email, file: docs/a.md, value: x, reason: short}\n", "at least 10"},
		{"missing value", "entries:\n  - {rule: email, file: docs/a.md, reason: documentation sample}\n", "value is required"},
		{"unknown field", "entries:\n  - {rule: email, file: docs/a.md, value: x, reason: documentation sample, extra: 1}\n", "invalid YAML"},
		{"intl directory", "entries:\n  - {rule: english-only, file: testdata/intl/, reason: internationalisation test data}\n", ""},
		{"intl directory with value", "entries:\n  - {rule: english-only, file: testdata/intl/, value: x, reason: internationalisation test data}\n", "takes no value"},
		{"directory for another rule", "entries:\n  - {rule: email, file: testdata/intl/, reason: internationalisation test data}\n", "only valid for the english-only"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, problems := parseAllowlist(allowTree(t), []byte(tc.yaml))
			if tc.want == "" && len(problems) != 0 || tc.want != "" && (len(problems) != 1 || !strings.Contains(problems[0].Detail, tc.want)) {
				t.Fatalf("problems = %v, want %q", problems, tc.want)
			}
		})
	}
}

func TestAllowlistEntryLimit(t *testing.T) {
	var b strings.Builder
	b.WriteString("entries:\n")
	for i := 0; i <= maxAllowed; i++ {
		fmt.Fprintf(&b, "  - {rule: email, file: docs/a.md, value: v%d, reason: documentation sample}\n", i)
	}
	_, problems := parseAllowlist(allowTree(t), []byte(b.String()))
	if len(problems) != 1 || !strings.Contains(problems[0].Detail, "51 entries (limit 50)") {
		t.Fatalf("problems = %v", problems)
	}
}

func TestAllowlistSuppressAndUnused(t *testing.T) {
	yaml := `entries:
  - {rule: email, file: docs/a.md, value: v1, reason: documentation sample}
  - {rule: email, file: docs/b.md, value: v2, reason: documentation sample}
  - {rule: english-only, file: testdata/intl/, reason: internationalisation test data}
`
	a, problems := parseAllowlist(allowTree(t), []byte(yaml))
	if len(problems) != 0 {
		t.Fatal(problems)
	}
	kept := a.Suppress([]Finding{
		{Rule: ruleEmail, Path: "docs/a.md", Value: "v1"},                  // suppressed
		{Rule: ruleEmail, Path: "docs/a.md", Value: "other"},               // different value
		{Rule: ruleEmail, Path: "docs/c.md", Value: "v1"},                  // different file
		{Rule: rulePhone, Path: "docs/a.md", Value: "v1"},                  // different rule
		{Rule: ruleLanguage, Path: "testdata/intl/mixed.txt", Value: "z"},  // directory entry
		{Rule: ruleLanguage, Path: "testdata/other/mixed.txt", Value: "z"}, // outside the directory
		{Rule: ruleTokens, Path: "docs/a.md", Value: "v1"},                 // never suppressible
	})
	if len(kept) != 5 {
		t.Fatalf("kept = %v", kept)
	}
	unused := a.Unused(map[string]bool{ruleEmail: true, ruleLanguage: true})
	if len(unused) != 1 || !strings.Contains(unused[0].Detail, "entry 2") || !strings.Contains(unused[0].Detail, "remove it") {
		t.Fatalf("unused = %v", unused)
	}
	if got := a.Unused(map[string]bool{}); len(got) != 0 {
		t.Errorf("entries of rules that did not run must not be judged: %v", got)
	}
}
