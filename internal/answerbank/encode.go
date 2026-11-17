package answerbank

import (
	"bytes"
	"fmt"
	"reflect"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	"applysmith/internal/answerbank/match"
)

// Encode renders a bank as answer-bank.yml text. Runtime values are never
// written, and every field the file format requires for a kind is written
// even when empty, so the output loads back to the same bank.
func Encode(b match.Bank) ([]byte, error) {
	root, err := encodeBank(b)
	if err != nil {
		return nil, err
	}
	return render(&yaml.Node{Kind: yaml.DocumentNode, Content: []*yaml.Node{root}})
}

func render(doc *yaml.Node) ([]byte, error) {
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(doc); err != nil {
		return nil, err
	}
	if err := enc.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func encodeBank(b match.Bank) (*yaml.Node, error) {
	root := &yaml.Node{Kind: yaml.MappingNode}
	addPair(root, "version", scalar(fmt.Sprint(b.Version), "!!int"))
	emp := &yaml.Node{Kind: yaml.SequenceNode}
	for _, e := range b.Employers {
		var n yaml.Node
		if err := n.Encode(struct {
			Name    string   `yaml:"name"`
			Aliases []string `yaml:"aliases"`
		}{e.Name, nonNil(e.Aliases)}); err != nil {
			return nil, err
		}
		emp.Content = append(emp.Content, &n)
	}
	addPair(root, "employers", emp)
	entries := &yaml.Node{Kind: yaml.SequenceNode}
	for _, e := range b.Entries {
		n, err := encodeEntry(e)
		if err != nil {
			return nil, err
		}
		entries.Content = append(entries.Content, n)
	}
	addPair(root, "entries", entries)
	ov, err := encodeOverrides(b)
	if err != nil {
		return nil, err
	}
	addPair(root, "overrides", ov)
	return root, nil
}

func encodeOverrides(b match.Bank) (*yaml.Node, error) {
	out := &yaml.Node{Kind: yaml.MappingNode}
	hosts := make([]string, 0, len(b.Overrides))
	for h := range b.Overrides {
		hosts = append(hosts, h)
	}
	sort.Strings(hosts)
	for _, host := range hosts {
		list := &yaml.Node{Kind: yaml.SequenceNode}
		for _, o := range b.Overrides[host] {
			n := &yaml.Node{Kind: yaml.MappingNode}
			addPair(n, "id", str(o.ID))
			if o.Kind != "" {
				addPair(n, "kind", str(string(o.Kind)))
			}
			for _, key := range o.Fields {
				v, err := bodyValue(o.Body, key)
				if err != nil {
					return nil, err
				}
				addPair(n, key, v)
			}
			list.Content = append(list.Content, n)
		}
		addPair(out, b.HostName(host), list)
	}
	return out, nil
}

func encodeEntry(e match.Entry) (*yaml.Node, error) {
	n := &yaml.Node{Kind: yaml.MappingNode}
	addPair(n, "id", str(e.ID))
	addPair(n, "description", str(e.Description))
	addPair(n, "kind", str(string(e.Kind)))
	required := map[string]bool{}
	for _, k := range match.RequiredKeys(e.Kind) {
		required[k] = true
	}
	for _, key := range match.BodyKeys(e.Kind) {
		if !required[key] && bodyFieldZero(e.Body, key) {
			continue
		}
		v, err := bodyValue(e.Body, key)
		if err != nil {
			return nil, err
		}
		addPair(n, key, v)
	}
	var meta yaml.Node
	if err := meta.Encode(e.Meta); err != nil {
		return nil, err
	}
	n.Content = append(n.Content, meta.Content...)
	return n, nil
}

// bodyFieldZero reports whether an optional field holds nothing to write.
func bodyFieldZero(b match.Body, key string) bool {
	switch key {
	case "fallback_any_option":
		return !b.FallbackAnyOption
	case "proficiency_options":
		return len(b.ProficiencyOptions) == 0
	case "abstain_if_ambiguous":
		return b.AbstainIfAmbiguous == nil
	case "selected":
		return b.Selected == nil
	}
	return false
}

func bodyValue(b match.Body, key string) (*yaml.Node, error) {
	var v any
	switch key {
	case "answer":
		v = b.Answer
	case "preferred":
		v = nonNil(b.Preferred)
	case "fallback_text":
		v = b.FallbackText
	case "fallback_any_option":
		v = b.FallbackAnyOption
	case "minimum":
		v = b.Minimum
	case "maximum":
		v = b.Maximum
	case "default_answer":
		v = b.DefaultAnswer
	case "currency":
		v = b.Currency
	case "language":
		v = b.Language
	case "speaking":
		v = b.Speaking
	case "listening":
		v = b.Listening
	case "reading":
		v = b.Reading
	case "writing":
		v = b.Writing
	case "proficiency_options":
		v = nonNil(b.ProficiencyOptions)
	case "answer_if_match":
		v = b.AnswerIfMatch
	case "answer_if_no_match":
		v = b.AnswerIfNoMatch
	case "abstain_if_ambiguous":
		v = b.AbstainsWhenAmbiguous()
	case "selected":
		v = nonNil(b.Selected)
	default:
		return nil, fmt.Errorf("answer-bank: unknown answer field %q", key)
	}
	var n yaml.Node
	if err := n.Encode(v); err != nil {
		return nil, err
	}
	return &n, nil
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

func addPair(m *yaml.Node, key string, v *yaml.Node) {
	m.Content = append(m.Content, str(key), v)
}

func str(s string) *yaml.Node {
	var n yaml.Node
	_ = n.Encode(s)
	return &n
}

func scalar(value, tag string) *yaml.Node {
	return &yaml.Node{Kind: yaml.ScalarNode, Tag: tag, Value: value}
}

// mergeBank renders b onto the existing file text, so comments survive: an
// entry whose content is unchanged keeps its node, a changed entry keeps the
// comments around it, and everything else is rebuilt from b. Empty existing
// text gives a plain render. Text that is not a bank file is an error, never
// overwritten: re-rendering would destroy whatever the broken file held.
func mergeBank(existing []byte, b match.Bank) ([]byte, error) {
	if len(bytes.TrimSpace(existing)) == 0 {
		return Encode(b)
	}
	old, err := readDocumentNode(FileName, existing)
	if err != nil {
		return nil, err
	}
	if old == nil || len(old.Content) == 0 || isNull(old.Content[0]) {
		return Encode(b)
	}
	if top := old.Content[0]; top.Kind != yaml.MappingNode {
		return nil, &LoadError{File: FileName, Line: top.Line, Message: "the file must hold a mapping with version, employers, entries and overrides"}
	}
	fresh, err := encodeBank(b)
	if err != nil {
		return nil, err
	}
	root := old.Content[0]
	changed := false
	for i := 0; i+1 < len(fresh.Content); i += 2 {
		key, val := fresh.Content[i].Value, fresh.Content[i+1]
		slot := mapValueSlot(root, key)
		switch {
		case slot == nil:
			root.Content = append(root.Content, fresh.Content[i], val)
			changed = true
		case sameContent(*slot, val):
		case key == "entries":
			*slot = mergeEntries(*slot, val)
			changed = true
		default:
			carryComments(val, *slot)
			*slot = val
			changed = true
		}
	}
	if !changed {
		// Re-rendering would normalise spacing the user chose.
		return existing, nil
	}
	return render(old)
}

// mapValueSlot returns the address of the value node stored under key.
func mapValueSlot(m *yaml.Node, key string) **yaml.Node {
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			return &m.Content[i+1]
		}
	}
	return nil
}

func mergeEntries(old, fresh *yaml.Node) *yaml.Node {
	byID := map[string]*yaml.Node{}
	if old != nil && old.Kind == yaml.SequenceNode {
		for _, item := range old.Content {
			if v := mapValue(item, "id"); v != nil {
				if _, dup := byID[v.Value]; !dup {
					byID[v.Value] = item
				}
			}
		}
	}
	out := &yaml.Node{Kind: yaml.SequenceNode, Style: styleOf(old)}
	if old != nil {
		out.HeadComment, out.LineComment, out.FootComment = old.HeadComment, old.LineComment, old.FootComment
	}
	for _, item := range fresh.Content {
		prev := byID[mapValue(item, "id").Value]
		switch {
		case prev == nil:
			out.Content = append(out.Content, item)
		case sameContent(prev, item):
			out.Content = append(out.Content, prev)
		default:
			out.Content = append(out.Content, mergeEntry(prev, item))
		}
	}
	return out
}

// mergeEntry applies the fields that changed to the entry's existing node, so
// the comments written inside the entry stay with the fields they annotate.
func mergeEntry(prev, fresh *yaml.Node) *yaml.Node {
	if prev.Kind != yaml.MappingNode || fresh.Kind != yaml.MappingNode {
		carryComments(fresh, prev)
		return fresh
	}
	out := &yaml.Node{Kind: yaml.MappingNode, Style: prev.Style}
	carryComments(out, prev)
	for i := 0; i+1 < len(fresh.Content); i += 2 {
		key, val := fresh.Content[i], fresh.Content[i+1]
		j := 0
		for j+1 < len(prev.Content) && prev.Content[j].Value != key.Value {
			j += 2
		}
		if j+1 >= len(prev.Content) {
			out.Content = append(out.Content, key, val)
			continue
		}
		if sameContent(prev.Content[j+1], val) {
			val = prev.Content[j+1]
		} else {
			carryComments(val, prev.Content[j+1])
		}
		out.Content = append(out.Content, prev.Content[j], val)
	}
	return out
}

func styleOf(n *yaml.Node) yaml.Style {
	if n == nil || n.Style&yaml.FlowStyle != 0 {
		return 0
	}
	return n.Style
}

func carryComments(dst, src *yaml.Node) {
	dst.HeadComment, dst.LineComment, dst.FootComment = src.HeadComment, src.LineComment, src.FootComment
}

// sameContent compares two nodes by the values they hold, ignoring comments,
// quoting and layout.
func sameContent(a, b *yaml.Node) bool {
	var va, vb any
	return a.Decode(&va) == nil && b.Decode(&vb) == nil && reflect.DeepEqual(va, vb)
}

// entryText renders one entry as the text of a list item, for appending.
func entryText(e match.Entry) (string, error) {
	n, err := encodeEntry(e)
	if err != nil {
		return "", err
	}
	out, err := render(n)
	return strings.TrimRight(string(out), "\n"), err
}
