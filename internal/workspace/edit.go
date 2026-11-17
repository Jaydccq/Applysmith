package workspace

import (
	"bytes"
	"errors"
	"fmt"
	"maps"
	"reflect"
	"slices"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// OpKind names a structured edit operation.
type OpKind string

const (
	OpSet    OpKind = "set"    // create or replace a scalar, list or mapping
	OpUnset  OpKind = "unset"  // delete a key (or list item) if present
	OpAppend OpKind = "append" // add an item to the end of a list
	OpRemove OpKind = "remove" // delete a list item by Index or by matching scalar
)

// Op is one edit by key path, for example targets.compensation.minimum or
// batch_skip_companies. Value is YAML text.
type Op struct {
	Kind  OpKind
	Path  string
	Value string
	Index *int
	Match string
}

// ApplyOps edits YAML source at the text level, guided by the parsed node
// positions, so comments, key order, blank lines, quoting and every untouched
// line keep their exact bytes. In a document with YAML anchors and
// aliases (PyYAML writes &id001 for a value that appears twice) an edit beside
// them leaves them as they are; an edit of a shared value, or one that would
// drop an anchor still in use, first writes every alias out as a copy of its
// value and drops the anchors, so the data stays identical and each copy is
// then edited on its own. An edit inside a flow collection, or an append or
// remove on a flow list of mappings, first rewrites that collection in block
// layout. Each op is checked by decoding the file before and after it: data
// outside the op's path must be unchanged, or the edit is refused. The result
// is not schema-validated here.
func ApplyOps(src []byte, ops []Op) ([]byte, error) {
	cur := src
	var doc *yaml.Node // the parse of cur an op has already made
	for _, op := range ops {
		next, parsed, err := applyOp(cur, doc, op)
		if err != nil {
			return nil, err
		}
		cur, doc = next, parsed
	}
	return cur, nil
}

type editor struct {
	d    *lineDoc
	ix   *nodeIndex
	root *yaml.Node
}

type slot struct {
	parent *yaml.Node // mapping or sequence holding node; nil for the root
	key    *yaml.Node // key node when parent is a mapping
	idx    int        // item index when parent is a sequence
	node   *yaml.Node
}

// applyOp applies op to src, whose parse is parsed when the caller has it,
// and returns the edited bytes with their parse.
func applyOp(src []byte, parsed *yaml.Node, op Op) ([]byte, *yaml.Node, error) {
	segs, err := parsePath(op.Path)
	if err != nil {
		return nil, nil, err
	}
	if parsed == nil {
		parsed = &yaml.Node{}
		if err := yaml.Unmarshal(src, parsed); err != nil {
			return nil, nil, newError(CodeInvalidYAML, op.Path, "the file is not valid YAML: %s", strings.TrimPrefix(err.Error(), "yaml: "))
		}
	}
	doc := parsed
	if len(doc.Content) == 0 || doc.Content[0].Kind != yaml.MappingNode || len(doc.Content[0].Content) == 0 {
		return nil, nil, newError(CodeEditUnsupported, op.Path, "the file has no top-level mapping to edit; use the raw editor")
	}
	if oddBreaks(string(src)) {
		return nil, nil, newError(CodeEditUnsupported, op.Path, "the file mixes line endings or holds a CR, NEL, LS or PS line break; use the raw editor")
	}
	ix := buildIndex(doc.Content[0])
	e := &editor{d: splitLines(src), ix: ix, root: doc.Content[0]}
	s, missing, err := e.resolve(op.Path, segs)
	if err != nil {
		return nil, nil, err
	}
	if ix.shared[s.node] {
		return expandThenApply(src, op)
	}
	if f, ok := e.flowToOpen(segs, len(missing) == 0); ok {
		if hasAnchors(f.node) {
			return nil, nil, flowRefused(op.Path)
		}
		if err := e.toBlock(op.Path, f, f.node.Content); err != nil {
			return nil, nil, err
		}
		return applyOp(e.d.bytes(), nil, op)
	}
	switch op.Kind {
	case OpSet:
		err = e.set(op, s, missing)
	case OpUnset:
		err = e.unset(op, s, missing)
	case OpAppend:
		err = e.appendItem(op, s, missing)
	case OpRemove:
		err = e.remove(op, s, missing)
	default:
		err = newError(CodeEditPath, op.Path, "unknown operation %q", op.Kind)
	}
	if err != nil {
		return nil, nil, err
	}
	out := e.d.bytes()
	var check yaml.Node
	if yaml.Unmarshal(out, &check) != nil {
		if len(ix.shared) > 0 {
			return expandThenApply(src, op) // the edit dropped an anchor an alias still names
		}
		return nil, nil, newError(CodeEditUnsupported, op.Path, "the edit would produce invalid YAML; use the raw editor")
	}
	if !editedOnlyPath(doc, &check, op, segs) {
		return nil, nil, newError(CodeEditUnsupported, op.Path, "the edit would change data beyond %s; use the raw editor", op.Path)
	}
	return out, &check, nil
}

// editedOnlyPath reports whether the decoded data after a text edit equals the
// data before everywhere outside the op's path (its list, for a list item it
// deletes), and a set or append left the op's value there. It is the proof
// that the text surgery edited the node the parser placed at the path.
func editedOnlyPath(before, after *yaml.Node, op Op, segs []seg) bool {
	var a, b any
	if before.Decode(&a) != nil || after.Decode(&b) != nil {
		return false
	}
	if op.Kind == OpUnset && len(segs) > 1 && segs[len(segs)-1].isIdx {
		segs = segs[:len(segs)-1]
	}
	if !sameData(masked(a, segs), masked(b, segs)) {
		return false
	}
	var want any
	switch op.Kind {
	case OpSet:
		return yaml.Unmarshal([]byte(op.Value), &want) == nil && sameData(dataAt(b, segs), want)
	case OpAppend:
		l, ok := dataAt(b, segs).([]any)
		return ok && len(l) > 0 && yaml.Unmarshal([]byte(op.Value), &want) == nil && sameData(l[len(l)-1], want)
	}
	return true
}

// sameData compares decoded YAML by its canonical encoding, so NaN equals NaN.
func sameData(a, b any) bool {
	x, errA := yaml.Marshal(a)
	y, errB := yaml.Marshal(b)
	return errA == nil && errB == nil && bytes.Equal(x, y)
}

// masked returns a copy of v with the value at segs replaced by a marker,
// creating the mappings on the way as an edit that creates keys does.
func masked(v any, segs []seg) any {
	if len(segs) == 0 {
		return "\x00edited"
	}
	sg := segs[0]
	if sg.isIdx {
		l, ok := v.([]any)
		if !ok || sg.idx >= len(l) {
			return v
		}
		l = slices.Clone(l)
		l[sg.idx] = masked(l[sg.idx], segs[1:])
		return l
	}
	m := map[string]any{}
	switch x := v.(type) {
	case map[string]any:
		m = maps.Clone(x)
	case map[any]any:
		c := map[any]any{}
		maps.Copy(c, x)
		c[sg.key] = masked(x[sg.key], segs[1:])
		return c
	}
	m[sg.key] = masked(m[sg.key], segs[1:])
	return m
}

// dataAt returns the decoded value at segs, or nil when there is none.
func dataAt(v any, segs []seg) any {
	for _, sg := range segs {
		switch x := v.(type) {
		case []any:
			if !sg.isIdx || sg.idx >= len(x) {
				return nil
			}
			v = x[sg.idx]
		case map[string]any:
			v = x[sg.key]
		case map[any]any:
			v = x[sg.key]
		default:
			return nil
		}
	}
	return v
}

// flowToOpen finds the outermost non-empty flow collection that encloses the
// op's target (or the key it creates). It is rewritten in block layout first,
// as a whole-list write would leave it, so a per-entry edit works on a file
// written [{...}, ...]; appendItem and remove handle a flow target list.
func (e *editor) flowToOpen(segs []seg, exists bool) (slot, bool) {
	s := slot{node: e.root, idx: -1}
	for i, sg := range segs {
		cur := s.node
		if sg.isIdx {
			s = slot{parent: cur, idx: sg.idx, node: cur.Content[sg.idx]}
		} else if v := valueNode(cur, sg.key); cur.Kind == yaml.MappingNode && v != nil {
			s = slot{parent: cur, key: keyNode(cur, sg.key), idx: -1, node: v}
		} else {
			break
		}
		if s.node.Style&yaml.FlowStyle != 0 && len(s.node.Content) > 0 && (i < len(segs)-1 || !exists) {
			return s, true
		}
	}
	return slot{}, false
}

func isCollection(n *yaml.Node) bool {
	return n.Kind == yaml.MappingNode || n.Kind == yaml.SequenceNode
}

// toBlock replaces the flow collection at s with content in block layout; a
// comment after it moves to the end of its key's line.
func (e *editor) toBlock(path string, s slot, content []*yaml.Node) error {
	c := *s.node
	c.Content = content
	b := detachedCopy(&c)
	setFlow(b, false)
	return e.replaceSlot(path, s, b)
}

// expandThenApply applies op after writing out the document's aliases, for an
// edit that touches a value shared through an anchor.
func expandThenApply(src []byte, op Op) ([]byte, *yaml.Node, error) {
	expanded, err := expandAliases(src)
	if err != nil {
		var te *Error
		if errors.As(err, &te) {
			te.Path = op.Path
			te.Message = op.Path + " is shared through a YAML anchor or alias, and " + te.Message
		}
		return nil, nil, err
	}
	if bytes.Equal(expanded, src) {
		return nil, nil, newError(CodeEditUnsupported, op.Path, "%s is shared through a YAML anchor or alias; use the raw editor", op.Path)
	}
	return applyOp(expanded, nil, op)
}

// resolve walks the path. When a key is missing it returns the slot of the
// deepest existing node and the segments still to create.
func (e *editor) resolve(path string, segs []seg) (slot, []seg, error) {
	s := slot{node: e.root, idx: -1}
	for i, sg := range segs {
		cur := s.node
		if sg.isIdx {
			if cur.Kind != yaml.SequenceNode {
				return s, nil, pathErr(path, "%s is a %s, not a list", walked(segs[:i]), foundType(cur))
			}
			if sg.idx >= len(cur.Content) {
				return s, nil, pathErr(path, "list index %d is out of range (%d items)", sg.idx, len(cur.Content))
			}
			s = slot{parent: cur, idx: sg.idx, node: cur.Content[sg.idx]}
			if s.node.Kind == yaml.AliasNode {
				return s, nil, nil // shared: the caller writes the aliases out and walks again
			}
			continue
		}
		switch {
		case cur.Kind == yaml.MappingNode:
			if v := valueNode(cur, sg.key); v != nil {
				s = slot{parent: cur, key: keyNode(cur, sg.key), idx: -1, node: v}
				continue
			}
			return s, segs[i:], nil
		case isNull(cur):
			return s, segs[i:], nil
		default:
			return s, nil, pathErr(path, "%s is a %s, not a mapping", walked(segs[:i]), foundType(cur))
		}
	}
	return s, nil, nil
}

func walked(segs []seg) string {
	var b strings.Builder
	for _, s := range segs {
		switch {
		case s.isIdx:
			b.WriteString("[" + strconv.Itoa(s.idx) + "]")
		case b.Len() == 0:
			b.WriteString(s.key)
		default:
			b.WriteString("." + s.key)
		}
	}
	return b.String()
}

func (e *editor) set(op Op, s slot, missing []seg) error {
	n, err := parseValue(op.Value)
	if err != nil {
		return err
	}
	if len(missing) == 0 {
		return e.replaceSlot(op.Path, s, n)
	}
	return e.create(op.Path, s, missing, n)
}

// create adds the missing keys, as nested mappings, beneath the deepest
// existing node.
func (e *editor) create(path string, holder slot, missing []seg, val *yaml.Node) error {
	for i := len(missing) - 1; i >= 1; i-- {
		if missing[i].isIdx {
			return pathErr(path, "cannot create list item %s: append to the list instead", walked(missing[i:i+1]))
		}
		val = mapEntry(scalarNode("!!str", missing[i].key), val)
	}
	if missing[0].isIdx {
		return pathErr(path, "cannot create list item %s: append to the list instead", walked(missing[:1]))
	}
	first := scalarNode("!!str", missing[0].key)
	h := holder.node
	switch {
	case h.Kind == yaml.MappingNode && len(h.Content) > 0:
		if h.Style&yaml.FlowStyle != 0 {
			return flowRefused(path)
		}
		indent := h.Content[0].Column - 1
		at := e.endLine(h, h.Content[0].Line)
		e.d.replace(at+1, at, indentLines(mustEncode(mapEntry(first, val)), indent))
		return nil
	case isNull(h) || (h.Kind == yaml.MappingNode && len(h.Content) == 0):
		return e.replaceSlot(path, holder, mapEntry(first, val))
	}
	return pathErr(path, "cannot create key under a %s", foundType(h))
}

func flowRefused(path string) error {
	return newError(CodeEditUnsupported, path, "the target sits inside a flow-style collection ({...} or [...]); use the raw editor")
}

func mustEncode(n *yaml.Node) []string {
	lines, err := encodeLines(n)
	if err != nil {
		panic(err) // nodes built here are always encodable
	}
	return lines
}

// replaceSlot replaces the value held by s with n.
func (e *editor) replaceSlot(path string, s slot, n *yaml.Node) error {
	if s.parent == nil {
		return newError(CodeEditUnsupported, path, "the whole document cannot be replaced by key; use the raw editor")
	}
	if s.parent.Style&yaml.FlowStyle != 0 {
		return flowRefused(path)
	}
	old := s.node
	if old.Kind == yaml.ScalarNode && n.Kind == yaml.ScalarNode && n.ShortTag() == "!!str" && n.Style == 0 {
		n.Style = old.Style & (yaml.DoubleQuotedStyle | yaml.SingleQuotedStyle)
	}
	if text, ok := inlineText(n); ok {
		if line, from, to, ok := e.extent(old); ok {
			l := e.d.lines[line-1]
			if text == "" && from > 0 && l[from-1] == ' ' && to > from {
				from--
			}
			if text != "" && from == to && from > 0 && l[from-1] != ' ' {
				text = " " + text
			}
			e.d.lines[line-1] = l[:from] + text + l[to:]
			return nil
		}
	}
	if s.key != nil {
		return e.rewriteEntry(s, n)
	}
	return e.rewriteItem(s, n)
}

func (e *editor) rewriteEntry(s slot, n *yaml.Node) error {
	from := s.key.Line
	to := e.endLine(s.node, from)
	line := e.d.lines[from-1]
	off := byteOffset(line, s.key.Column)
	repl := indentLines(mustEncode(mapEntry(s.key, n)), s.key.Column-1)
	if prefix := line[:off]; !isBlank(prefix) {
		repl[0] = prefix + repl[0][s.key.Column-1:]
	}
	if c := keyLineComment(s); c != "" {
		repl[0] += " " + c
	}
	e.d.replace(from, to, repl)
	return nil
}

// keyLineComment is the comment at the end of the key's line: the parser puts
// it on the key when the value is blank or starts below, else on the value.
func keyLineComment(s slot) string {
	if s.key.LineComment != "" {
		return s.key.LineComment
	}
	if s.node.Line == s.key.Line {
		return s.node.LineComment
	}
	return ""
}

func (e *editor) itemStart(s slot) (int, error) {
	n := s.node
	line := e.d.lines[n.Line-1]
	if !strings.HasSuffix(strings.TrimSpace(line[:byteOffset(line, n.Column)]), "-") {
		return 0, newError(CodeEditUnsupported, "", "list item layout is not supported; use the raw editor")
	}
	return n.Line, nil
}

func (e *editor) rewriteItem(s slot, n *yaml.Node) error {
	from, err := e.itemStart(s)
	if err != nil {
		return err
	}
	to := e.endLine(s.node, from)
	seq := &yaml.Node{Kind: yaml.SequenceNode, Content: []*yaml.Node{n}}
	e.d.replace(from, to, indentLines(mustEncode(seq), s.parent.Column-1))
	return nil
}

// inlineText renders n as the single-line text that follows "key:", reporting
// false for values that need block layout. A null renders as nothing.
func inlineText(n *yaml.Node) (string, bool) {
	if isNull(n) {
		return "", true
	}
	if n.Kind == yaml.MappingNode || n.Kind == yaml.SequenceNode {
		if len(n.Content) > 0 && n.Style&yaml.FlowStyle == 0 {
			return "", false
		}
	}
	lines := mustEncode(n)
	if len(lines) != 1 {
		return "", false
	}
	return lines[0], true
}

// extent locates the source text of a single-line plain or quoted scalar (or
// the empty spot after "key:" for a null) as byte offsets within its line.
func (e *editor) extent(n *yaml.Node) (line, from, to int, ok bool) {
	if n.Kind != yaml.ScalarNode || n.Style&(yaml.LiteralStyle|yaml.FoldedStyle|yaml.TaggedStyle) != 0 {
		return 0, 0, 0, false
	}
	l := e.d.lines[n.Line-1]
	from = byteOffset(l, n.Column)
	if isNull(n) && n.Value == "" {
		return n.Line, from, from, from > 0 && (l[from-1] == ':' || l[from-1] == '-')
	}
	rest := l[from:]
	switch {
	case n.Style&yaml.DoubleQuotedStyle != 0:
		end := closingQuote(rest, '"', true)
		return n.Line, from, from + end, end > 0
	case n.Style&yaml.SingleQuotedStyle != 0:
		end := closingQuote(rest, '\'', false)
		return n.Line, from, from + end, end > 0
	default:
		if strings.Contains(n.Value, "\n") || !strings.HasPrefix(rest, n.Value) {
			return 0, 0, 0, false
		}
		return n.Line, from, from + len(n.Value), true
	}
}

// closingQuote returns the length of the quoted scalar at the start of s
// (including both quotes), or 0 when it does not close on this line.
func closingQuote(s string, q byte, backslash bool) int {
	for i := 1; i < len(s); i++ {
		switch {
		case backslash && s[i] == '\\':
			i++
		case s[i] == q && !backslash && i+1 < len(s) && s[i+1] == q:
			i++
		case s[i] == q:
			return i + 1
		}
	}
	return 0
}

// endLine returns the last line (1-based) that belongs to n, scanning from
// line from: everything up to the next node, minus trailing blank lines and
// comment lines that belong to what follows.
func (e *editor) endLine(n *yaml.Node, from int) int {
	end := len(e.d.lines)
	if next := e.ix.pos[n] + e.ix.size[n]; next < len(e.ix.order) {
		end = e.ix.order[next].Line - 1
	}
	if leaf := lastLeaf(n); isBlockScalar(leaf) {
		return max(e.blockScalarEnd(leaf, end), from)
	}
	for end > from && (isBlank(e.d.lines[end-1]) || isCommentOnly(e.d.lines[end-1])) {
		end--
	}
	return max(end, from)
}

func isBlockScalar(n *yaml.Node) bool {
	return n.Kind == yaml.ScalarNode && n.Style&(yaml.LiteralStyle|yaml.FoldedStyle) != 0
}

// blockScalarEnd returns the last content line of the literal or folded
// scalar n, looking no further than line limit. The content is indented as
// its first non-blank line is and deeper than the line holding the | or >; the
// first line indented less, a comment included, follows the scalar.
func (e *editor) blockScalarEnd(n *yaml.Node, limit int) int {
	last := n.Line
	floor := indentOf(e.d.lines[n.Line-1])
	want := -1
	for l := n.Line + 1; l <= limit; l++ {
		t := e.d.lines[l-1]
		if isBlank(t) {
			continue
		}
		w := indentOf(t)
		if want < 0 {
			want = w
		}
		if w < want || w <= floor {
			break
		}
		last = l
	}
	return last
}

func indentOf(s string) int { return len(s) - len(strings.TrimLeft(s, " ")) }

// expandAliases replaces every alias with a copy of the value it names and
// removes the anchors, editing the text the way an op does so comments, order
// and untouched lines are kept. A document without anchors, or one that does
// not parse (the op reports that), is returned as it is. The expansion is
// refused unless the decoded data is exactly what it was.
func expandAliases(src []byte) ([]byte, error) {
	var doc yaml.Node
	if yaml.Unmarshal(src, &doc) != nil || len(doc.Content) == 0 || !hasAnchors(doc.Content[0]) {
		return src, nil
	}
	refuse := func(why string) error {
		return newError(CodeEditUnsupported, "", "the anchors cannot be written out safely (%s); use the raw editor", why)
	}
	cur := src
	for {
		var d yaml.Node
		if err := yaml.Unmarshal(cur, &d); err != nil {
			return nil, refuse("an expansion produced invalid YAML")
		}
		s, found := lastAlias(d.Content[0])
		if !found {
			break
		}
		if s.parent == nil || (s.key != nil && s.key.Kind == yaml.AliasNode) {
			return nil, refuse("an alias is used as a key or as the whole document")
		}
		e := &editor{d: splitLines(cur), ix: buildIndex(d.Content[0]), root: d.Content[0]}
		if err := e.expandAlias(s); err != nil {
			return nil, refuse(err.Error())
		}
		cur = e.d.bytes()
	}
	out, err := dropAnchors(cur)
	if err != nil {
		return nil, refuse(err.Error())
	}
	var before, after any
	if yaml.Unmarshal(src, &before) != nil || yaml.Unmarshal(out, &after) != nil || !reflect.DeepEqual(before, after) {
		return nil, refuse("the expanded data would differ")
	}
	return out, nil
}

func hasAnchors(n *yaml.Node) bool {
	if n.Kind == yaml.AliasNode || n.Anchor != "" {
		return true
	}
	for _, c := range n.Content {
		if hasAnchors(c) {
			return true
		}
	}
	return false
}

// lastAlias finds the alias that comes last in the document, with its slot;
// expanding from the end keeps the positions of the ones before it valid.
func lastAlias(root *yaml.Node) (slot, bool) {
	var found slot
	ok := false
	var visit func(parent *yaml.Node)
	visit = func(parent *yaml.Node) {
		for i, c := range parent.Content {
			if c.Kind == yaml.AliasNode {
				s := slot{parent: parent, idx: i, node: c}
				if parent.Kind == yaml.MappingNode {
					if i%2 == 0 {
						s = slot{parent: parent, key: c, node: c}
					} else {
						s = slot{parent: parent, key: parent.Content[i-1], idx: -1, node: c}
					}
				}
				found, ok = s, true
				continue
			}
			visit(c)
		}
	}
	visit(root)
	return found, ok
}

// expandAlias writes the alias at s out as a copy of the value it names: in
// place of the *name token when the copy fits on the line, otherwise as a
// block entry or list item.
func (e *editor) expandAlias(s slot) error {
	val := detachedCopy(s.node.Alias)
	flowParent := s.parent.Style&yaml.FlowStyle != 0
	if flowParent {
		setFlow(val, true)
	}
	line := e.d.lines[s.node.Line-1]
	from := byteOffset(line, s.node.Column)
	tok := "*" + s.node.Value
	if !strings.HasPrefix(line[from:], tok) {
		return fmt.Errorf("alias %s is not where the parser placed it", tok)
	}
	if text, ok := inlineText(val); ok && text != "" {
		e.d.lines[s.node.Line-1] = line[:from] + text + line[from+len(tok):]
		return nil
	}
	if flowParent {
		return fmt.Errorf("alias %s sits in a flow collection and its value spans lines", tok)
	}
	if s.key != nil {
		return e.rewriteEntry(s, val)
	}
	return e.rewriteItem(s, val)
}

// detachedCopy deep-copies n without anchors or comments, resolving nested
// aliases to copies of their values.
func detachedCopy(n *yaml.Node) *yaml.Node {
	if n.Kind == yaml.AliasNode {
		return detachedCopy(n.Alias)
	}
	c := *n
	c.Anchor, c.HeadComment, c.LineComment, c.FootComment = "", "", "", ""
	c.Content = make([]*yaml.Node, len(n.Content))
	for i, ch := range n.Content {
		c.Content[i] = detachedCopy(ch)
	}
	return &c
}

// dropAnchors removes each &name token (and the space after it), from the
// last to the first so earlier positions stay valid.
func dropAnchors(src []byte) ([]byte, error) {
	var doc yaml.Node
	if err := yaml.Unmarshal(src, &doc); err != nil {
		return nil, err
	}
	var anchored []*yaml.Node
	var visit func(n *yaml.Node)
	visit = func(n *yaml.Node) {
		if n.Anchor != "" {
			anchored = append(anchored, n)
		}
		for _, c := range n.Content {
			visit(c)
		}
	}
	visit(&doc)
	d := splitLines(src)
	for i := len(anchored) - 1; i >= 0; i-- {
		n := anchored[i]
		line := d.lines[n.Line-1]
		from := byteOffset(line, n.Column)
		tok := "&" + n.Anchor
		if !strings.HasPrefix(line[from:], tok) {
			return nil, fmt.Errorf("anchor %s is not where the parser placed it", tok)
		}
		rest := strings.TrimLeft(line[from+len(tok):], " \t")
		head := line[:from]
		if rest == "" || strings.HasPrefix(rest, "#") {
			head = strings.TrimRight(head, " \t")
			if rest != "" {
				rest = " " + rest
			}
		}
		d.lines[n.Line-1] = head + rest
	}
	return d.bytes(), nil
}
