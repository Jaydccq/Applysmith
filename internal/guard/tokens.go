package guard

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"unicode/utf8"
)

const (
	ruleTokens  = "pii-tokens"
	ruleRetired = "retired-names"

	envTokens  = "APPLYSMITH_PII_TOKENS"
	envRetired = "APPLYSMITH_RETIRED_NAMES"

	minTokenLen = 3
)

// token is one entry of a local list. Literals are matched case-insensitively
// as substrings; expressions use RE2 syntax as written.
type listEntry struct {
	ordinal int // 1-based position among the usable tokens
	lit     string
	re      *regexp.Regexp
}

// parseTokens reads the list format shared by the token and retired-name
// files. Configuration problems name the line number and never the line.
func parseTokens(rule, envVar, content string) ([]listEntry, []Finding) {
	var toks []listEntry
	var problems []Finding
	bad := func(line int, detail string) {
		problems = append(problems, Finding{Rule: rule, Path: "$" + envVar, Line: line, Detail: detail})
	}
	for i, raw := range strings.Split(content, "\n") {
		line := strings.TrimSpace(strings.TrimPrefix(raw, "\uFEFF"))
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		body := line
		isRE := strings.HasPrefix(line, "re:")
		if isRE {
			body = line[len("re:"):]
		}
		if utf8.RuneCountInString(body) < minTokenLen {
			bad(i+1, fmt.Sprintf("configuration error: token shorter than %d characters", minTokenLen))
			continue
		}
		tok := listEntry{ordinal: len(toks) + 1}
		if isRE {
			re, err := regexp.Compile(body)
			if err != nil {
				bad(i+1, "configuration error: invalid regular expression")
				continue
			}
			tok.re = re
		} else {
			tok.lit = strings.ToLower(body)
		}
		toks = append(toks, tok)
	}
	if len(toks) == 0 && len(problems) == 0 {
		problems = append(problems, Finding{Rule: rule, Path: "$" + envVar, Detail: "configuration error: the list holds no usable token"})
	}
	return toks, problems
}

// find returns the byte offset of the first match in s, or -1.
func (k listEntry) find(s string) int {
	if k.re != nil {
		loc := k.re.FindStringIndex(s)
		if loc == nil {
			return -1
		}
		return loc[0]
	}
	return strings.Index(strings.ToLower(s), k.lit)
}

// scanTokens reports each token match as path, line, column and ordinal. The
// token text, the matched text and the surrounding line are never included.
func scanTokens(rule string, t *Tree, toks []listEntry) ([]Finding, error) {
	var out []Finding
	for _, p := range t.Paths {
		for _, k := range toks {
			if off := k.find(p); off >= 0 {
				out = append(out, Finding{Rule: rule, Path: p, Col: runeCol(p, off), Detail: fmt.Sprintf("path matches list entry #%d", k.ordinal)})
			}
		}
	}
	err := t.eachText(nil, func(f textFile) {
		for i, line := range f.Lines {
			for _, k := range toks {
				if off := k.find(line); off >= 0 {
					out = append(out, Finding{Rule: rule, Path: f.Path, Line: i + 1, Col: runeCol(line, off), Detail: fmt.Sprintf("content matches list entry #%d", k.ordinal)})
				}
			}
		}
	})
	return out, err
}

// insideTree reports whether file resolves to a location under root.
func insideTree(root, file string) bool {
	r, err1 := filepath.EvalSymlinks(root)
	f, err2 := filepath.EvalSymlinks(file)
	if err1 != nil || err2 != nil {
		return false
	}
	rel, err := filepath.Rel(r, f)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// tokenRule builds a rule over a list file named by an environment variable.
func tokenRule(rule, envVar string) Rule {
	return Rule{Name: rule, Check: func(t *Tree) ([]Finding, error) {
		path := os.Getenv(envVar)
		if path == "" {
			if t.Release {
				return []Finding{{Rule: rule, Path: "$" + envVar, Detail: "the list is required for release and is not set"}}, nil
			}
			return nil, Skipped("$" + envVar + " is not set; this scan did NOT run")
		}
		if insideTree(t.Root, path) {
			return []Finding{{Rule: rule, Path: "$" + envVar, Detail: "the list must live outside the repository"}}, nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return []Finding{{Rule: rule, Path: "$" + envVar, Detail: "the list file is unreadable"}}, nil
		}
		toks, problems := parseTokens(rule, envVar, string(data))
		if len(problems) > 0 {
			return problems, nil
		}
		if rule == ruleRetired && !t.Release {
			t = withoutPlanningTree(t)
		}
		return scanTokens(rule, t, toks)
	}}
}

// withoutPlanningTree drops the private openspec/ tree, which must name the
// retired identifiers to record the migration away from them. Release mode
// never takes this path because the export excludes openspec/ entirely.
func withoutPlanningTree(t *Tree) *Tree {
	c := *t
	c.Paths = nil
	for _, p := range t.Paths {
		if !strings.HasPrefix(p, "openspec/") {
			c.Paths = append(c.Paths, p)
		}
	}
	return &c
}
