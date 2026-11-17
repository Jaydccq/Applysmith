package kb

import (
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"
)

// Source kinds of an evidence fact.
const (
	KindSnippet    = "snippet"
	KindCV         = "cv"
	KindProfile    = "profile"
	KindNarrative  = "narrative"
	KindAnswerBank = "answer-bank"
	KindCorrection = "correction"
	KindBrief      = "company-brief"
)

// Sensitive profile categories. A section of one is returned only when the
// query itself asks about that category.
const (
	SensWorkAuth = "work-authorization"
	SensComp     = "compensation"
	SensDOB      = "date-of-birth"
	SensSelfID   = "self-identification"
	SensCriminal = "criminal-history"
)

// profileKeys are the top-level profile.yml keys that hold facts about the
// person; settings blocks such as scan and notifications are not knowledge.
var profileKeys = setOf("candidate", "links", "work_authorization", "targets", "preferences", "history", "skills")

// factSourceFiles maps the workspace files that act as fact sources to the
// kind they index as. voice.md is a style source and is deliberately absent.
var factSourceFiles = []struct{ file, kind string }{
	{"cv.md", KindCV},
	{"profile.yml", KindProfile},
	{"narrative.md", KindNarrative},
}

// humanize turns a YAML key path into a readable label.
func humanize(path []string) string {
	words := make([]string, 0, len(path))
	for _, p := range path {
		words = append(words, strings.ReplaceAll(strings.ReplaceAll(p, "_", " "), "-", " "))
	}
	label := strings.Join(words, " ")
	if label == "" {
		return label
	}
	return strings.ToUpper(label[:1]) + label[1:]
}

func sensitiveOf(path []string) string {
	joined := strings.ToLower(strings.Join(path, "."))
	switch {
	case strings.HasPrefix(joined, "work_authorization"):
		return SensWorkAuth
	case strings.Contains(joined, "compensation"):
		return SensComp
	case strings.Contains(joined, "date_of_birth") || strings.Contains(joined, "birth"):
		return SensDOB
	case containsAny(joined, "eeo", "self_identification", "gender", "race", "ethnic", "veteran", "disabilit", "pronoun"):
		return SensSelfID
	case strings.Contains(joined, "criminal"):
		return SensCriminal
	}
	return ""
}

func containsAny(s string, subs ...string) bool {
	for _, x := range subs {
		if strings.Contains(s, x) {
			return true
		}
	}
	return false
}

// profileSection is one fact of profile.yml.
type profileSection struct {
	label     string
	text      string
	sensitive string
}

// profileSections flattens profile.yml into one "Label: value" section per
// fact, leaving out empty values and the settings blocks.
func profileSections(data []byte) ([]profileSection, error) {
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrFrontmatter, err)
	}
	if doc.Kind != yaml.DocumentNode || len(doc.Content) == 0 || doc.Content[0].Kind != yaml.MappingNode {
		return nil, nil
	}
	var out []profileSection
	root := doc.Content[0]
	for i := 0; i+1 < len(root.Content); i += 2 {
		key := root.Content[i].Value
		if profileKeys[key] {
			walkProfile(root.Content[i+1], []string{key}, &out)
		}
	}
	return out, nil
}

func walkProfile(n *yaml.Node, path []string, out *[]profileSection) {
	switch n.Kind {
	case yaml.AliasNode:
		if n.Alias != nil {
			walkProfile(n.Alias, path, out)
		}
	case yaml.MappingNode:
		for i := 0; i+1 < len(n.Content); i += 2 {
			walkProfile(n.Content[i+1], append(append([]string{}, path...), n.Content[i].Value), out)
		}
	case yaml.SequenceNode:
		var items []string
		for _, c := range n.Content {
			if v := inlineValue(c); v != "" {
				items = append(items, v)
			}
		}
		if len(items) > 0 {
			add(out, path, strings.Join(items, "; "))
		}
	case yaml.ScalarNode:
		if n.Tag != "!!null" && strings.TrimSpace(n.Value) != "" {
			add(out, path, strings.TrimSpace(n.Value))
		}
	}
}

func add(out *[]profileSection, path []string, value string) {
	label := humanize(path)
	*out = append(*out, profileSection{label: label, text: label + ": " + value, sensitive: sensitiveOf(path)})
}

// inlineValue renders a scalar, a list or a mapping on one line.
func inlineValue(n *yaml.Node) string {
	switch n.Kind {
	case yaml.ScalarNode:
		if n.Tag == "!!null" {
			return ""
		}
		return strings.TrimSpace(n.Value)
	case yaml.SequenceNode:
		var parts []string
		for _, c := range n.Content {
			if v := inlineValue(c); v != "" {
				parts = append(parts, v)
			}
		}
		return strings.Join(parts, ", ")
	case yaml.MappingNode:
		var parts []string
		for i := 0; i+1 < len(n.Content); i += 2 {
			if v := inlineValue(n.Content[i+1]); v != "" {
				parts = append(parts, strings.ReplaceAll(n.Content[i].Value, "_", " ")+" "+v)
			}
		}
		return strings.Join(parts, ", ")
	}
	return ""
}
