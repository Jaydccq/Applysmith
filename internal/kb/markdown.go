package kb

import (
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"
)

// mdDoc is a Markdown document whose frontmatter keeps every key it had, so a
// rewrite changes only what it sets.
type mdDoc struct {
	fm   *yaml.Node
	body string
}

func parseMD(text string) (*mdDoc, error) {
	fm, body, has, err := splitFrontmatter(text)
	if err != nil {
		return nil, err
	}
	node := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
	if has {
		if node, err = frontmatterNode(fm); err != nil {
			return nil, err
		}
	}
	return &mdDoc{fm: node, body: strings.TrimSpace(body)}, nil
}

func (d *mdDoc) render() []byte {
	out, err := renderDocument(d.fm, d.body)
	if err != nil {
		// A node built from parsed or string values always marshals.
		panic(fmt.Sprintf("kb: render frontmatter: %v", err))
	}
	return out
}

// setList sets a list key, or removes the key when the list is empty and
// keepEmpty is false.
func (d *mdDoc) setList(key string, items []string, keepEmpty bool) {
	if len(items) == 0 && !keepEmpty {
		mapDelete(d.fm, key)
		return
	}
	mapSet(d.fm, key, listNode(items))
}

func (d *mdDoc) setString(key, v string) { mapSet(d.fm, key, strNode(v)) }

// rawSection is a heading line with the lines that follow it.
type rawSection struct {
	headingLine string
	heading     string
	lines       []string
}

// splitRaw splits a body at headings outside code fences. The first part holds
// the content before the first heading and has no heading line.
func splitRaw(body string) []*rawSection {
	cur := &rawSection{}
	parts := []*rawSection{cur}
	inFence := false
	for _, line := range strings.Split(body, "\n") {
		if fenceRE.MatchString(line) {
			inFence = !inFence
		}
		if m := headingRE.FindStringSubmatch(line); m != nil && !inFence {
			cur = &rawSection{headingLine: line, heading: strings.TrimSpace(m[2])}
			parts = append(parts, cur)
			continue
		}
		cur.lines = append(cur.lines, line)
	}
	return parts
}

func joinRaw(parts []*rawSection) string {
	var lines []string
	for _, p := range parts {
		if p.headingLine != "" {
			lines = append(lines, p.headingLine)
		}
		lines = append(lines, p.lines...)
	}
	return strings.TrimSpace(strings.Join(lines, "\n"))
}

func (p *rawSection) text() string { return strings.TrimSpace(strings.Join(p.lines, "\n")) }

func (p *rawSection) setText(t string) {
	p.lines = []string{"", strings.TrimSpace(t), ""}
}

// editSection applies fn to the text of the first section with the heading and
// reports whether it exists.
func editSection(body, heading string, fn func(string) string) (string, bool) {
	parts := splitRaw(body)
	for _, p := range parts {
		if p.headingLine != "" && strings.EqualFold(p.heading, heading) {
			p.setText(fn(p.text()))
			return joinRaw(parts), true
		}
	}
	return body, false
}

func newMapping() *yaml.Node { return &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"} }

func boolNode(v bool) *yaml.Node {
	return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!bool", Value: fmt.Sprint(v)}
}
