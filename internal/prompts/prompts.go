// Clean-room: docs/provenance.md#CR-EVAL-RUBRIC

// Package prompts embeds the evaluation prompt templates and applies the
// workspace overrides. A template is a Go text template that may reference
// only the documented fields; an invalid override is an error, never a silent
// fallback to the embedded text.
package prompts

import (
	"bytes"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"text/template"
	"text/template/parse"
	"unicode"

	"applysmith/internal/lazyregexp"
)

//go:embed templates/*.tmpl
var embedded embed.FS

// Spec documents one template: the root fields it may reference, which of them
// it must reference, and the nested field names it may use inside them.
type Spec struct {
	Name     string
	Fields   []string
	Required []string
}

// nested are the field names that may appear below a root field.
var nested = []string{
	"Title", "Company", "Location", "Compensation", "URL", "Source", "Text",
	"Summary", "CV", "Narrative", "Targets", "Preferences", "WorkAuth", "Stance", "Note", "Need", "Card", "ID",
	"Kind", "Name", "Date",
}

var specs = []Spec{
	{"quick-screen", []string{"Posting", "Profile", "Sponsor"}, []string{"Posting", "Profile", "Sponsor"}},
	{"deep-eval", []string{"Posting", "Profile", "Sponsor", "Knowledge", "Signals"}, []string{"Posting", "Profile", "Sponsor"}},
	{"deep-answers", []string{"Posting", "Profile", "Evaluation"}, []string{"Posting", "Profile"}},
	{"jd-extract", []string{"Page"}, []string{"Page"}},
	{"jd-decode", []string{"Posting", "Profile", "Sponsor", "Knowledge"}, []string{"Posting", "Profile"}},
	{"outreach", []string{"Contact", "Role", "Facts", "Voice"}, []string{"Contact", "Role", "Facts"}},
	{"followup", []string{"Message", "Contact", "Role", "Facts", "Voice"}, []string{"Message", "Role", "Facts"}},
}

func specOf(name string) (Spec, bool) {
	for _, s := range specs {
		if s.Name == name {
			return s, true
		}
	}
	return Spec{}, false
}

// Info says which text a render used.
type Info struct {
	Name     string `json:"name"`
	Hash     string `json:"hash"`
	Override bool   `json:"override"`
}

// Set resolves templates, preferring files in the override directory.
type Set struct{ dir string }

// New returns a Set whose overrides live in dir; an empty dir has none.
func New(dir string) *Set { return &Set{dir: dir} }

func (s *Set) overridePath(name string) string { return filepath.Join(s.dir, name+".tmpl") }

func embeddedText(name string) (string, error) {
	b, err := embedded.ReadFile("templates/" + name + ".tmpl")
	if err != nil {
		return "", fmt.Errorf("prompts: no template named %q", name)
	}
	return string(b), nil
}

// Source returns the active text of a template and whether it is an override.
func (s *Set) Source(name string) (string, bool, error) {
	base, err := embeddedText(name)
	if err != nil {
		return "", false, err
	}
	if s.dir == "" {
		return base, false, nil
	}
	b, err := os.ReadFile(s.overridePath(name))
	switch {
	case errors.Is(err, os.ErrNotExist):
		return base, false, nil
	case err != nil:
		return "", false, fmt.Errorf("prompts/%s.tmpl: %w", name, err)
	}
	return string(b), true, nil
}

// Render executes the active template with data. A broken override fails with
// an error naming the file.
func (s *Set) Render(name string, data any) (string, Info, error) {
	text, override, err := s.Source(name)
	if err != nil {
		return "", Info{}, err
	}
	label := name + ".tmpl"
	if override {
		label = "prompts/" + label
	}
	if err := validate(name, text); err != nil {
		return "", Info{}, fmt.Errorf("%s: %w", label, err)
	}
	t, err := template.New(name).Option("missingkey=error").Parse(text)
	if err != nil {
		return "", Info{}, fmt.Errorf("%s: %w", label, err)
	}
	var buf bytes.Buffer
	if err := t.Execute(&buf, data); err != nil {
		return "", Info{}, fmt.Errorf("%s: %w", label, err)
	}
	sum := sha256.Sum256([]byte(text))
	return buf.String(), Info{Name: name, Hash: hex.EncodeToString(sum[:]), Override: override}, nil
}

// validate checks that text parses, references only documented fields and
// references every required one.
func validate(name, text string) error {
	spec, ok := specOf(name)
	if !ok {
		return fmt.Errorf("no template named %q", name)
	}
	trees, err := parse.Parse(name, text, "{{", "}}")
	if err != nil {
		return err
	}
	seen := map[string]bool{}
	for _, tr := range trees {
		if tr.Root != nil {
			walk(tr.Root, seen)
		}
	}
	allowed := map[string]bool{}
	for _, f := range spec.Fields {
		allowed[f] = true
	}
	for _, f := range nested {
		allowed[f] = true
	}
	var unknown []string
	for f := range seen {
		if !allowed[f] {
			unknown = append(unknown, f)
		}
	}
	sort.Strings(unknown)
	if len(unknown) > 0 {
		return fmt.Errorf("references undocumented field %s", strings.Join(unknown, ", "))
	}
	for _, f := range spec.Required {
		if !seen[f] {
			return fmt.Errorf("does not reference the required field %s", f)
		}
	}
	return nil
}

func walk(n parse.Node, seen map[string]bool) {
	switch n := n.(type) {
	case *parse.ListNode:
		if n == nil {
			return
		}
		for _, c := range n.Nodes {
			walk(c, seen)
		}
	case *parse.ActionNode:
		walk(n.Pipe, seen)
	case *parse.PipeNode:
		if n == nil {
			return
		}
		for _, c := range n.Cmds {
			walk(c, seen)
		}
	case *parse.CommandNode:
		for _, a := range n.Args {
			walk(a, seen)
		}
	case *parse.FieldNode:
		if len(n.Ident) > 0 {
			seen[n.Ident[0]] = true
		}
	case *parse.IfNode:
		walkBranch(&n.BranchNode, seen)
	case *parse.RangeNode:
		walkBranch(&n.BranchNode, seen)
	case *parse.WithNode:
		walkBranch(&n.BranchNode, seen)
	case *parse.TemplateNode:
		walk(n.Pipe, seen)
	}
}

func walkBranch(b *parse.BranchNode, seen map[string]bool) {
	walk(b.Pipe, seen)
	walk(b.List, seen)
	if b.ElseList != nil {
		walk(b.ElseList, seen)
	}
}

// Names lists the template names.
func (s *Set) Names() []string {
	out := make([]string, len(specs))
	for i, sp := range specs {
		out[i] = sp.Name
	}
	return out
}

// Check validates every override file and returns one error per problem,
// naming the file.
func (s *Set) Check() []error {
	if s.dir == "" {
		return nil
	}
	var out []error
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return []error{fmt.Errorf("prompts: %w", err)}
	}
	for _, e := range entries {
		file, ok := strings.CutSuffix(e.Name(), ".tmpl")
		if !ok || e.IsDir() {
			continue
		}
		if _, known := specOf(file); !known {
			out = append(out, fmt.Errorf("prompts/%s: no template with this name", e.Name()))
			continue
		}
		text, _, err := s.Source(file)
		if err == nil {
			err = validate(file, text)
		}
		if err == nil {
			_, perr := template.New(file).Parse(text)
			err = perr
		}
		if err != nil {
			out = append(out, fmt.Errorf("prompts/%s: %w", e.Name(), err))
		}
	}
	return out
}

// Diff returns a line diff of the active text against the embedded text, or
// the empty string when they are the same.
func (s *Set) Diff(name string) (string, error) {
	base, err := embeddedText(name)
	if err != nil {
		return "", err
	}
	active, _, err := s.Source(name)
	if err != nil {
		return "", err
	}
	if active == base {
		return "", nil
	}
	return lineDiff(strings.Split(base, "\n"), strings.Split(active, "\n")), nil
}

func lineDiff(a, b []string) string {
	// Longest common subsequence table.
	lcs := make([][]int, len(a)+1)
	for i := range lcs {
		lcs[i] = make([]int, len(b)+1)
	}
	for i := len(a) - 1; i >= 0; i-- {
		for j := len(b) - 1; j >= 0; j-- {
			if a[i] == b[j] {
				lcs[i][j] = lcs[i+1][j+1] + 1
			} else {
				lcs[i][j] = max(lcs[i+1][j], lcs[i][j+1])
			}
		}
	}
	var out strings.Builder
	i, j := 0, 0
	for i < len(a) || j < len(b) {
		switch {
		case i < len(a) && j < len(b) && a[i] == b[j]:
			i++
			j++
		case j < len(b) && (i == len(a) || lcs[i][j+1] >= lcs[i+1][j]):
			out.WriteString("+ " + b[j] + "\n")
			j++
		default:
			out.WriteString("- " + a[i] + "\n")
			i++
		}
	}
	return out.String()
}

var controlTag = lazyregexp.New(`(?i)<(\s*/?\s*(?:instructions?|system|developer|user|assistant|job_description|candidate_profile|sponsor_card|knowledge|profile|role|contact|facts|voice|prior_exchange|tool\w*)\b[^>]*)>`)

// angleFold maps the fullwidth angle brackets to ASCII so a lookalike tag is
// matched like the real one.
var angleFold = strings.NewReplacer("\uff1c", "<", "\uff1e", ">")

// Escape neutralizes prompt-control tags in untrusted text so the model reads
// them as content: the angle brackets of such a tag become entities. Format
// characters (zero-width and similar) are removed first and fullwidth angle
// brackets are folded, so neither can hide a tag from the match.
func Escape(text string) string {
	text = strings.Map(func(r rune) rune {
		if unicode.Is(unicode.Cf, r) {
			return -1
		}
		return r
	}, text)
	return controlTag.ReplaceAllString(angleFold.Replace(text), "&lt;$1&gt;")
}
