package workspace

import (
	"bytes"
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"

	"gopkg.in/yaml.v3"
)

// seg is one step of a key path such as targets.roles[2].priority.
type seg struct {
	key   string
	idx   int
	isIdx bool
}

func parsePath(p string) ([]seg, error) {
	if strings.TrimSpace(p) == "" {
		return nil, newError(CodeEditPath, p, "path is empty")
	}
	var segs []seg
	rest := p
	for rest != "" {
		switch rest[0] {
		case '.':
			if len(segs) == 0 {
				return nil, newError(CodeEditPath, p, "path starts with a dot")
			}
			rest = rest[1:]
			if rest == "" || rest[0] == '.' || rest[0] == '[' {
				return nil, newError(CodeEditPath, p, "empty key in path")
			}
		case '[':
			end := strings.IndexByte(rest, ']')
			if end < 0 {
				return nil, newError(CodeEditPath, p, "unclosed [ in path")
			}
			n, err := strconv.Atoi(rest[1:end])
			if err != nil || n < 0 {
				return nil, newError(CodeEditPath, p, "list index %q is not a non-negative integer", rest[1:end])
			}
			segs = append(segs, seg{idx: n, isIdx: true})
			rest = rest[end+1:]
			continue
		}
		end := strings.IndexAny(rest, ".[")
		if end < 0 {
			end = len(rest)
		}
		segs = append(segs, seg{key: rest[:end]})
		rest = rest[end:]
	}
	return segs, nil
}

// lineDoc is the file as lines, remembering its line ending so that inserted
// lines match and every untouched line stays byte-identical.
type lineDoc struct {
	lines        []string
	eol          string
	finalNewline bool
}

func splitLines(src []byte) *lineDoc {
	s := string(src)
	d := &lineDoc{eol: "\n"}
	if strings.Contains(s, "\r\n") {
		d.eol = "\r\n"
	}
	if d.finalNewline = strings.HasSuffix(s, "\n"); d.finalNewline {
		s = strings.TrimSuffix(s, d.eol)
		s = strings.TrimSuffix(s, "\n")
	}
	if s != "" || !d.finalNewline {
		d.lines = strings.Split(s, d.eol)
	}
	return d
}

// oddBreaks reports a line break other than the file's one LF or CRLF style:
// YAML counts a lone CR, NEL, LS and PS as line breaks too, so splitLines
// would number lines differently from the parser and edit the wrong one.
func oddBreaks(s string) bool {
	if strings.ContainsAny(s, "\u0085\u2028\u2029") {
		return true
	}
	cr, crlf := strings.Count(s, "\r"), strings.Count(s, "\r\n")
	return cr > 0 && (cr != crlf || crlf != strings.Count(s, "\n"))
}

func (d *lineDoc) bytes() []byte {
	out := strings.Join(d.lines, d.eol)
	if d.finalNewline && len(d.lines) > 0 {
		out += d.eol
	}
	return []byte(out)
}

// replace swaps 1-based inclusive lines [from, to] for repl; to == from-1
// inserts before from.
func (d *lineDoc) replace(from, to int, repl []string) {
	out := make([]string, 0, len(d.lines)-(to-from+1)+len(repl))
	out = append(out, d.lines[:from-1]...)
	out = append(out, repl...)
	out = append(out, d.lines[to:]...)
	d.lines = out
}

// byteOffset converts a 1-based rune column on a line to a byte offset.
func byteOffset(line string, col int) int {
	off := 0
	for i := 1; i < col && off < len(line); i++ {
		_, w := utf8.DecodeRuneInString(line[off:])
		off += w
	}
	return off
}

// nodeIndex lists nodes in document order with their subtree sizes, so the
// end of a block is known from where the next node starts. shared holds every
// alias and every node under an anchor: an edit there would change, or would
// mean, a value that other keys share.
type nodeIndex struct {
	order  []*yaml.Node
	pos    map[*yaml.Node]int
	size   map[*yaml.Node]int
	shared map[*yaml.Node]bool
}

func buildIndex(root *yaml.Node) *nodeIndex {
	ix := &nodeIndex{pos: map[*yaml.Node]int{}, size: map[*yaml.Node]int{}, shared: map[*yaml.Node]bool{}}
	var visit func(n *yaml.Node, shared bool)
	visit = func(n *yaml.Node, shared bool) {
		shared = shared || n.Kind == yaml.AliasNode || n.Anchor != ""
		if shared {
			ix.shared[n] = true
		}
		ix.pos[n] = len(ix.order)
		ix.order = append(ix.order, n)
		for _, c := range n.Content {
			visit(c, shared)
		}
		ix.size[n] = len(ix.order) - ix.pos[n]
	}
	visit(root, false)
	return ix
}

func lastLeaf(n *yaml.Node) *yaml.Node {
	for len(n.Content) > 0 {
		n = n.Content[len(n.Content)-1]
	}
	return n
}

func isBlank(s string) bool { return strings.TrimSpace(s) == "" }

func isCommentOnly(s string) bool { return strings.HasPrefix(strings.TrimSpace(s), "#") }

// encodeLines renders a node as block YAML with two-space indentation.
func encodeLines(n *yaml.Node) ([]string, error) {
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(n); err != nil {
		return nil, err
	}
	if err := enc.Close(); err != nil {
		return nil, err
	}
	return strings.Split(strings.TrimSuffix(buf.String(), "\n"), "\n"), nil
}

func indentLines(lines []string, indent int) []string {
	pad := strings.Repeat(" ", indent)
	out := make([]string, len(lines))
	for i, l := range lines {
		if l == "" {
			out[i] = l
			continue
		}
		out[i] = pad + l
	}
	return out
}

func scalarNode(tag, value string) *yaml.Node {
	return &yaml.Node{Kind: yaml.ScalarNode, Tag: tag, Value: value}
}

func mapEntry(key, val *yaml.Node) *yaml.Node {
	k := scalarNode("!!str", key.Value)
	k.Style = key.Style &^ yaml.TaggedStyle
	return &yaml.Node{Kind: yaml.MappingNode, Content: []*yaml.Node{k, val}}
}

func parseValue(text string) (*yaml.Node, error) {
	var doc yaml.Node
	if err := yaml.Unmarshal([]byte(text), &doc); err != nil {
		return nil, newError(CodeEditPath, "", "value is not valid YAML: %s", strings.TrimPrefix(err.Error(), "yaml: "))
	}
	if len(doc.Content) == 0 {
		return scalarNode("!!null", ""), nil
	}
	n := doc.Content[0]
	if hasAnchors(n) {
		return nil, newError(CodeEditPath, "", "value must not use anchors or aliases")
	}
	clearComments(n)
	setFlow(n, false)
	return n, nil
}

// setFlow switches the collections under n between block and flow layout.
// Empty collections always stay flow ({} or []).
func setFlow(n *yaml.Node, flow bool) {
	if n.Kind == yaml.MappingNode || n.Kind == yaml.SequenceNode {
		if flow || len(n.Content) == 0 {
			n.Style |= yaml.FlowStyle
		} else {
			n.Style &^= yaml.FlowStyle
		}
	}
	for _, c := range n.Content {
		setFlow(c, flow)
	}
}

func clearComments(n *yaml.Node) {
	n.HeadComment, n.LineComment, n.FootComment = "", "", ""
	for _, c := range n.Content {
		clearComments(c)
	}
}

func pathErr(path, format string, args ...any) error {
	return newError(CodeEditPath, path, "%s", fmt.Sprintf(format, args...))
}
