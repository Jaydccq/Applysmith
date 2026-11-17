package guard

import (
	"errors"
	"fmt"
	"io"
	"strings"

	"gopkg.in/yaml.v3"
)

const (
	ruleAllow = "allowlist"

	allowFile      = "internal/guard/allow.yml"
	maxAllowed     = 50
	minReasonLen   = 10
	intlFixtureDir = "testdata/intl/"
)

// notAllowlistable rules can never be suppressed.
var notAllowlistable = map[string]bool{ruleTokens: true, ruleRetired: true, ruleInternalPath: true, ruleSecret: true, ruleGitleaks: true}

// valueRules are the rules whose findings carry the exact matched text, so an
// entry must name it.
var valueRules = map[string]bool{ruleEmail: true, rulePhone: true, ruleHomePath: true, ruleComment: true, ruleLanguage: true}

// AllowEntry suppresses one rule on one exact file for one exact value.
type AllowEntry struct {
	Rule   string `yaml:"rule"`
	File   string `yaml:"file"`
	Value  string `yaml:"value"`
	Reason string `yaml:"reason"`
	used   bool
}

// Allowlist is the parsed content of allow.yml.
type Allowlist struct{ entries []*AllowEntry }

// LoadAllowlist parses and validates the allowlist of a tree. A missing file
// is an empty list.
func LoadAllowlist(t *Tree) (*Allowlist, []Finding) {
	if !t.Has(allowFile) {
		return &Allowlist{}, nil
	}
	data, err := t.Read(allowFile)
	if err != nil {
		return &Allowlist{}, []Finding{{Rule: ruleAllow, Path: allowFile, Detail: "unreadable"}}
	}
	return parseAllowlist(t, data)
}

func parseAllowlist(t *Tree, data []byte) (*Allowlist, []Finding) {
	var doc struct {
		Entries []*AllowEntry `yaml:"entries"`
	}
	dec := yaml.NewDecoder(strings.NewReader(string(data)))
	dec.KnownFields(true)
	if err := dec.Decode(&doc); err != nil && !errors.Is(err, io.EOF) {
		return &Allowlist{}, []Finding{{Rule: ruleAllow, Path: allowFile, Detail: "invalid YAML: " + err.Error()}}
	}
	known := map[string]bool{}
	for _, r := range Rules() {
		known[r.Name] = true
	}
	var problems []Finding
	if len(doc.Entries) > maxAllowed {
		problems = append(problems, Finding{Rule: ruleAllow, Path: allowFile, Detail: fmt.Sprintf("%d entries (limit %d)", len(doc.Entries), maxAllowed)})
	}
	for i, e := range doc.Entries {
		bad := func(detail string) {
			problems = append(problems, Finding{Rule: ruleAllow, Path: allowFile, Detail: fmt.Sprintf("entry %d: %s", i+1, detail)})
		}
		dir := e.File == intlFixtureDir
		switch {
		case !known[e.Rule]:
			bad("unknown rule " + e.Rule)
		case notAllowlistable[e.Rule]:
			bad("rule " + e.Rule + " cannot be allowlisted")
		case e.File == "" || strings.ContainsAny(e.File, "*?[") || (strings.HasSuffix(e.File, "/") && !dir):
			bad("file must be one exact path (the only directory allowed is " + intlFixtureDir + ")")
		case !dir && !t.Has(e.File):
			bad("file " + e.File + " does not exist")
		case dir && e.Rule != ruleLanguage:
			bad("a directory entry is only valid for the " + ruleLanguage + " rule")
		case dir && e.Value != "":
			bad("a directory entry takes no value")
		case !dir && valueRules[e.Rule] && e.Value == "":
			bad("value is required for rule " + e.Rule)
		case len(strings.TrimSpace(e.Reason)) < minReasonLen:
			bad(fmt.Sprintf("reason must be at least %d characters", minReasonLen))
		}
	}
	return &Allowlist{entries: doc.Entries}, problems
}

func (e *AllowEntry) matches(f Finding) bool {
	if e.Rule != f.Rule {
		return false
	}
	if e.File == intlFixtureDir {
		return strings.HasPrefix(f.Path, intlFixtureDir)
	}
	return e.File == f.Path && e.Value == f.Value
}

// Suppress drops the findings covered by an entry and marks those entries used.
func (a *Allowlist) Suppress(fs []Finding) []Finding {
	var kept []Finding
next:
	for _, f := range fs {
		if !notAllowlistable[f.Rule] {
			for _, e := range a.entries {
				if e.matches(f) {
					e.used = true
					continue next
				}
			}
		}
		kept = append(kept, f)
	}
	return kept
}

// Unused reports entries that suppressed nothing in a run. Only entries whose
// rule actually ran are judged.
func (a *Allowlist) Unused(ran map[string]bool) []Finding {
	var out []Finding
	for i, e := range a.entries {
		if ran[e.Rule] && !e.used {
			out = append(out, Finding{Rule: ruleAllow, Path: allowFile, Detail: fmt.Sprintf("entry %d (%s on %s) suppressed nothing; remove it", i+1, e.Rule, e.File)})
		}
	}
	return out
}
