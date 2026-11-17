package guard

import (
	"strings"
	"testing"
)

func TestCommentRules(t *testing.T) {
	tests := []struct {
		name  string
		file  string
		src   string
		wants []string // detail prefixes, in order; nil means clean
	}{
		{"go: ISO date", "a.go", "package a\n\n// fixed on " + join("2024", "-01-", "02") + " for one case\nvar x int\n", []string{"calendar date"}},
		{"go: slash date", "a.go", "// " + join("2024", "/1/", "5") + "\n", []string{"calendar date"}},
		{"go: US date", "a.go", "// " + join("1/", "5/", "2024") + "\n", []string{"calendar date"}},
		{"go: month name", "a.go", "// since March 3, 2024\n// on 3rd of June\n", []string{"calendar date", "calendar date"}},
		{"go: block comment line numbers", "a.go", "/*\nfirst\nsee " + join("PR ", "123") + "\n*/\n", []string{"pull-request"}},
		{"go: PR, issue and URL references", "a.go", "// PR 12\n// issue #34\n// https://example.com/org/repo/pull/56\n", []string{"pull-request", "pull-request", "pull-request"}},
		{"go: decision phrase", "a.go", "// this was approved by the lead\n// requested by someone\n", []string{"decision", "decision"}},
		{"go: bare year in copyright passes", "a.go", "// Copyright 2026 The Authors\npackage a\n", nil},
		{"go: date inside a string is not a comment", "a.go", "package a\n\nvar d = \"" + join("2024", "-01-", "02") + "\" // ok\nvar e = `// " + join("2024", "-01-", "02") + "`\n", nil},
		{"go: neutral rationale passes", "a.go", "// The retry count is 3 because the server drops idle sockets.\n", nil},
		{"ts: line comment date", "a.ts", "const x = 1 // " + join("2024", "-02-", "03") + "\n", []string{"calendar date"}},
		{"ts: URL in string is not a comment", "a.ts", "const u = 'http://example.com/pull/99'\n", nil},
		{"ts: decision phrase", "a.tsx", "/* approved by the owner */\n", []string{"decision"}},
		{"ts: ticket in block comment", "a.ts", "/**\n * See #123 for details\n */\n", []string{"pull-request"}},
		{"sh: tracker reference", "run.sh", "#!/bin/sh\necho hi # " + join("see #", "123") + "\n", []string{"pull-request"}},
		{"sh: standalone token", "run.sh", "#" + join("12", "3") + "\n", []string{"pull-request"}},
		{"sh: expansion is not a comment", "run.sh", "echo ${#arr[@]} \"$#\" 'a # b'\n", nil},
		{"sh: single digit passes", "run.sh", "# step #1 of the run\n", nil},
		{"yaml: date in comment", "ci.yml", "# updated " + join("2024", "-05-", "06") + "\nname: x\n", []string{"calendar date"}},
		{"yaml: quoted hash is not a comment", "ci.yml", "key: \"a # " + join("2024", "-05-", "06") + "\"\n", nil},
		{"makefile: comment", "Makefile", "# issue 77\nall:\n", []string{"pull-request"}},
		{"toml: comment", "a.toml", "x = 1 # approved by me\n", []string{"decision"}},
		{"sql: dash comment", "a.sql", "-- " + join("2024", "-01-", "01") + "\nselect '-- not a comment';\n", []string{"calendar date"}},
		{"css: hex colour passes", "a.css", "/* accent #00ff88 and #123456 and #abc */\n", nil},
		{"css: tracker reference", "a.css", "/* fixes #12345 */\n", []string{"pull-request"}},
		{"html: comment", "a.html", "<p>x</p>\n<!-- done " + join("2024", "-01-", "02") + " -->\n", []string{"calendar date"}},
		{"markdown is not scanned", "a.md", "# Notes\n" + join("2024", "-01-", "02") + " decided by us\n", nil},
		{"go.mod comment", "go.mod", "module m\n\nrequire x v1.0.0 // approved by the lead\n", []string{"decision"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tree := dirTree(t, map[string]string{tc.file: tc.src})
			fs, err := checkComments(tree)
			if err != nil {
				t.Fatal(err)
			}
			if len(fs) != len(tc.wants) {
				t.Fatalf("findings = %v, want %d", fs, len(tc.wants))
			}
			for i, w := range tc.wants {
				if !strings.HasPrefix(fs[i].Detail, w) {
					t.Errorf("finding %d detail = %q, want prefix %q", i, fs[i].Detail, w)
				}
			}
		})
	}
}

func TestCommentFindingLocation(t *testing.T) {
	src := "package a\n\n/*\nblock\n  see " + join("PR ", "123") + " here\n*/\n"
	fs, _ := checkComments(dirTree(t, map[string]string{"a.go": src}))
	if len(fs) != 1 || fs[0].Line != 5 || fs[0].Col != 7 {
		t.Fatalf("findings = %v", fs)
	}
	if strings.Contains(fs[0].String(), "123") {
		t.Errorf("output reveals the match: %s", fs[0])
	}
}
