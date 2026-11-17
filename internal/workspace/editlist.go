package workspace

import (
	"slices"
	"strings"

	"gopkg.in/yaml.v3"
)

func seqOf(items ...*yaml.Node) *yaml.Node {
	return &yaml.Node{Kind: yaml.SequenceNode, Content: items}
}

func (e *editor) unset(op Op, s slot, missing []seg) error {
	if len(missing) > 0 {
		return nil // already absent
	}
	if s.parent == nil {
		return newError(CodeEditUnsupported, op.Path, "the whole document cannot be removed")
	}
	if s.key == nil {
		return e.removeItem(s.parent, s.idx)
	}
	if s.parent.Style&yaml.FlowStyle != 0 {
		return flowRefused(op.Path)
	}
	from := s.key.Line
	line := e.d.lines[from-1]
	if !isBlank(line[:byteOffset(line, s.key.Column)]) {
		return newError(CodeEditUnsupported, op.Path, "the key shares its line with a list dash; remove the list item or use the raw editor")
	}
	to := e.endLine(s.node, from)
	floor := 0 // the comment sweep stops at the content of a block scalar above
	if prev := e.ix.order[e.ix.pos[s.key]-1]; isBlockScalar(prev) {
		floor = e.blockScalarEnd(prev, from-1)
	}
	for from > floor+1 && isCommentOnly(e.d.lines[from-2]) {
		from--
	}
	e.d.replace(from, to, nil)
	return nil
}

func (e *editor) appendItem(op Op, s slot, missing []seg) error {
	item, err := parseValue(op.Value)
	if err != nil {
		return err
	}
	if len(missing) > 0 {
		return e.create(op.Path, s, missing, seqOf(item))
	}
	t := s.node
	switch {
	case isNull(t):
		return e.replaceSlot(op.Path, s, seqOf(item))
	case t.Kind != yaml.SequenceNode:
		return pathErr(op.Path, "%s is a %s, not a list", op.Path, foundType(t))
	case t.Style&yaml.FlowStyle != 0:
		items := append(slices.Clone(t.Content), item)
		if slices.ContainsFunc(items, isCollection) {
			return e.toBlock(op.Path, s, items) // a list of mappings goes block, as a whole-list write would
		}
		return e.rewriteFlow(t, items)
	}
	at := e.endLine(t, t.Line)
	e.d.replace(at+1, at, indentLines(mustEncode(seqOf(item)), t.Column-1))
	return nil
}

func (e *editor) remove(op Op, s slot, missing []seg) error {
	if len(missing) > 0 {
		return pathErr(op.Path, "%s does not exist", op.Path)
	}
	t := s.node
	if t.Kind != yaml.SequenceNode {
		return pathErr(op.Path, "%s is a %s, not a list", op.Path, foundType(t))
	}
	idx := -1
	switch {
	case op.Index != nil:
		if *op.Index < 0 || *op.Index >= len(t.Content) {
			return pathErr(op.Path, "list index %d is out of range (%d items)", *op.Index, len(t.Content))
		}
		idx = *op.Index
	default:
		want := op.Match
		if m, err := parseValue(op.Match); err == nil && m.Kind == yaml.ScalarNode {
			want = m.Value
		}
		for i, item := range t.Content {
			if item.Kind == yaml.ScalarNode && (item.Value == op.Match || item.Value == want) {
				idx = i
				break
			}
		}
		if idx < 0 {
			return pathErr(op.Path, "no item matches %q", op.Match)
		}
	}
	if t.Style&yaml.FlowStyle != 0 && slices.ContainsFunc(t.Content, isCollection) {
		return e.toBlock(op.Path, s, slices.Delete(slices.Clone(t.Content), idx, idx+1))
	}
	return e.removeItem(t, idx)
}

func (e *editor) removeItem(seq *yaml.Node, i int) error {
	if seq.Style&yaml.FlowStyle != 0 {
		rest := append(append([]*yaml.Node(nil), seq.Content[:i]...), seq.Content[i+1:]...)
		return e.rewriteFlow(seq, rest)
	}
	s := slot{parent: seq, idx: i, node: seq.Content[i]}
	from, err := e.itemStart(s)
	if err != nil {
		return err
	}
	e.d.replace(from, e.endLine(s.node, from), nil)
	return nil
}

// rewriteFlow re-renders a flow sequence on one line in place of its source
// span, leaving whatever surrounds it on those lines.
func (e *editor) rewriteFlow(seq *yaml.Node, content []*yaml.Node) error {
	cp := *seq
	cp.Content = content
	setFlow(&cp, true)
	cp.HeadComment, cp.LineComment, cp.FootComment = "", "", ""
	text := strings.Join(mustEncode(&cp), " ")
	startLine := seq.Line
	startOff := byteOffset(e.d.lines[startLine-1], seq.Column)
	endLine, endOff, ok := e.flowEnd(startLine, startOff)
	if !ok {
		return newError(CodeEditUnsupported, "", "flow list layout is not supported; use the raw editor")
	}
	merged := e.d.lines[startLine-1][:startOff] + text + e.d.lines[endLine-1][endOff:]
	e.d.replace(startLine, endLine, []string{merged})
	return nil
}

// flowEnd finds the offset just past the bracket that closes the flow
// collection opening at (line, off). A quote opens a quoted scalar only where
// a scalar starts (after [, {, a comma or a colon); elsewhere, as in
// O'Reilly, it is part of a plain scalar.
func (e *editor) flowEnd(line, off int) (endLine, endOff int, ok bool) {
	depth := 0
	var quote, prev byte
	for l := line; l <= len(e.d.lines); l++ {
		text := e.d.lines[l-1]
		start := 0
		if l == line {
			start = off
		}
		for i := start; i < len(text); i++ {
			c := text[i]
			switch {
			case quote == '"' && c == '\\', quote == '\'' && c == '\'' && i+1 < len(text) && text[i+1] == '\'':
				i++
			case quote != 0 && c == quote:
				quote = 0
			case quote != 0:
			case (c == '"' || c == '\'') && strings.IndexByte("[{,:", prev) >= 0:
				quote = c
			case c == '#' && i > 0 && text[i-1] == ' ':
				i = len(text)
			case c == '[' || c == '{':
				depth++
			case c == ']' || c == '}':
				depth--
				if depth == 0 {
					return l, i + 1, true
				}
			}
			if c != ' ' && c != '\t' {
				prev = c
			}
		}
	}
	return 0, 0, false
}
