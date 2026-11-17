package workspace

import (
	"fmt"
	"math"
	"reflect"
	"strconv"
	"strings"
	"sync"

	"gopkg.in/yaml.v3"
)

// Schemas are Go structs. The `yaml` tag names a key, `v` carries the rules for
// the field itself and `ev` the rules for the elements of a list or mapping.
// Rule tokens: required, nonnull, nonblank, askme, enum=a|b, keys=a|b, min=N,
// max=N, gt=N and format=name (see formats.go).

type rules struct {
	required, nonnull, nonblank, askme bool
	enum, keys                         []string
	min, max, gt                       *float64
	format                             string
}

func parseRules(tag string) rules {
	var r rules
	if tag == "" {
		return r
	}
	for _, tok := range strings.Split(tag, ",") {
		name, val, _ := strings.Cut(strings.TrimSpace(tok), "=")
		switch name {
		case "required":
			r.required, r.nonnull = true, true
		case "nonnull":
			r.nonnull = true
		case "nonblank":
			r.nonblank = true
		case "askme":
			r.askme = true
		case "enum":
			r.enum = strings.Split(val, "|")
		case "keys":
			r.keys = strings.Split(val, "|")
		case "min", "max", "gt":
			f, err := strconv.ParseFloat(val, 64)
			if err != nil {
				panic("workspace: bad rule " + tok)
			}
			switch name {
			case "min":
				r.min = &f
			case "max":
				r.max = &f
			default:
				r.gt = &f
			}
		case "format":
			r.format = val
		default:
			panic("workspace: unknown rule " + tok)
		}
	}
	return r
}

type fieldInfo struct {
	name   string
	index  int
	typ    reflect.Type
	rules  rules
	erules rules
}

var (
	nodeType   = reflect.TypeOf(yaml.Node{})
	fieldCache sync.Map // reflect.Type -> map[string]*fieldInfo
)

func structFields(t reflect.Type) map[string]*fieldInfo {
	if v, ok := fieldCache.Load(t); ok {
		return v.(map[string]*fieldInfo)
	}
	m := map[string]*fieldInfo{}
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		name := strings.Split(f.Tag.Get("yaml"), ",")[0]
		if name == "" || name == "-" {
			continue
		}
		m[name] = &fieldInfo{
			name: name, index: i, typ: f.Type,
			rules: parseRules(f.Tag.Get("v")), erules: parseRules(f.Tag.Get("ev")),
		}
	}
	fieldCache.Store(t, m)
	return m
}

// crossChecker is implemented (on the pointer type) by structs with rules that
// span fields. It runs only when the struct's own fields raised no issue.
type crossChecker interface {
	cross(c *collector, n *yaml.Node, path string)
}

type collector struct {
	file    string
	issues  []Issue
	secrets map[*yaml.Node]bool
	// leaked holds the secret-shaped values found in the document. Reports are
	// served to the API, so no message may repeat one.
	leaked []string
}

func (c *collector) redact(s string) string {
	for _, v := range c.leaked {
		s = strings.ReplaceAll(s, v, "[redacted]")
	}
	return s
}

func (c *collector) add(n *yaml.Node, path, code, msg string, allowed ...string) {
	i := Issue{File: c.file, Path: c.redact(path), Code: code, Message: c.redact(msg), Allowed: allowed}
	if n != nil {
		i.Line, i.Column = n.Line, n.Column
	}
	c.issues = append(c.issues, i)
}

// addAt reports an issue at a key of mapping n, positioned on that key.
func (c *collector) addAt(n *yaml.Node, path, key, code, msg string, allowed ...string) {
	at := n
	if k := keyNode(n, key); k != nil {
		at = k
	}
	c.add(at, join(path, key), code, msg, allowed...)
}

func join(path, key string) string {
	if path == "" {
		return key
	}
	return path + "." + key
}

func keyNode(m *yaml.Node, key string) *yaml.Node {
	if m == nil || m.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			return m.Content[i]
		}
	}
	return nil
}

func valueNode(m *yaml.Node, key string) *yaml.Node {
	if m == nil || m.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			return deref(m.Content[i+1])
		}
	}
	return nil
}

func deref(n *yaml.Node) *yaml.Node {
	for n != nil && n.Kind == yaml.AliasNode {
		n = n.Alias
	}
	return n
}

func isNull(n *yaml.Node) bool {
	return n.Kind == yaml.ScalarNode && n.ShortTag() == "!!null"
}

func foundType(n *yaml.Node) string {
	switch n.Kind {
	case yaml.MappingNode:
		return "mapping"
	case yaml.SequenceNode:
		return "list"
	case yaml.ScalarNode:
		switch n.ShortTag() {
		case "!!null":
			return "null"
		case "!!bool":
			return "boolean"
		case "!!int":
			return "integer"
		case "!!float":
			return "number"
		case "!!timestamp":
			return "date"
		}
		return "string"
	}
	return "unknown"
}

func (c *collector) mismatch(n *yaml.Node, path, want string) {
	c.add(n, path, CodeTypeMismatch, fmt.Sprintf("expected %s, found %s", want, foundType(n)))
}

func (c *collector) walk(n *yaml.Node, t reflect.Type, r, er rules, path string) {
	n = deref(n)
	if t == nodeType {
		return
	}
	for t.Kind() == reflect.Ptr {
		t = t.Elem()
	}
	if isNull(n) {
		if r.nonnull {
			c.add(n, path, CodeTypeMismatch, nullMessage(t, r))
		}
		return
	}
	switch t.Kind() {
	case reflect.String:
		c.walkString(n, path, r)
	case reflect.Bool:
		if n.Kind != yaml.ScalarNode || n.ShortTag() != "!!bool" {
			c.mismatch(n, path, "boolean (true or false)")
		}
	case reflect.Int, reflect.Int64:
		c.walkNumber(n, path, r, true)
	case reflect.Float64:
		c.walkNumber(n, path, r, false)
	case reflect.Slice:
		if n.Kind != yaml.SequenceNode {
			c.mismatch(n, path, "list")
			return
		}
		et := t.Elem()
		for et.Kind() == reflect.Ptr {
			et = et.Elem()
		}
		for i, item := range n.Content {
			p := fmt.Sprintf("%s[%d]", path, i)
			if isNull(deref(item)) { // the decoder drops a null item, so its neighbours' indexes would shift
				c.mismatch(deref(item), p, typeLabel(et, er))
				continue
			}
			c.walk(item, t.Elem(), er, rules{}, p)
		}
	case reflect.Map:
		c.walkMap(n, t, r, er, path)
	case reflect.Struct:
		c.walkStruct(n, t, path)
	}
}

func nullMessage(t reflect.Type, r rules) string {
	if (t.Kind() == reflect.Int || t.Kind() == reflect.Int64) && r.min != nil && r.max != nil {
		return fmt.Sprintf("an integer from %s to %s is required", fmtNum(*r.min), fmtNum(*r.max))
	}
	return fmt.Sprintf("a value is required: expected %s, found null", typeLabel(t, r))
}

func typeLabel(t reflect.Type, r rules) string {
	switch t.Kind() {
	case reflect.String:
		if len(r.enum) > 0 {
			return "one of " + strings.Join(r.enum, ", ")
		}
		return "string"
	case reflect.Int, reflect.Int64:
		return "integer"
	case reflect.Float64:
		return "number"
	case reflect.Bool:
		return "boolean"
	case reflect.Slice:
		return "list"
	}
	return "mapping"
}

func (c *collector) walkString(n *yaml.Node, path string, r rules) {
	if n.Kind != yaml.ScalarNode {
		c.mismatch(n, path, "string")
		return
	}
	switch n.ShortTag() {
	case "!!str", "!!int", "!!float", "!!timestamp":
	default:
		c.mismatch(n, path, "string")
		return
	}
	v := n.Value
	if r.askme && v == askMeMarker {
		return
	}
	if r.nonblank && strings.TrimSpace(v) == "" {
		c.add(n, path, CodeRequired, "must not be blank")
		return
	}
	if len(r.enum) > 0 && !contains(r.enum, v) {
		c.add(n, path, CodeNotAllowed, fmt.Sprintf("%q is not an allowed value", v), r.enum...)
		return
	}
	if r.format != "" {
		if want, ok := checkFormat(r.format, v); !ok {
			c.add(n, path, CodeInvalidFormat, fmt.Sprintf("expected %s, found %q", want, v))
		}
	}
}

func (c *collector) walkNumber(n *yaml.Node, path string, r rules, integer bool) {
	tag := n.ShortTag()
	want := "number"
	if integer {
		want = "integer"
	}
	if n.Kind != yaml.ScalarNode || !(tag == "!!int" || (!integer && tag == "!!float")) {
		c.mismatch(n, path, want)
		return
	}
	var f float64
	if err := n.Decode(&f); err != nil {
		c.mismatch(n, path, want)
		return
	}
	if math.IsNaN(f) || math.IsInf(f, 0) {
		c.add(n, path, CodeOutOfRange, "expected a finite "+want)
		return
	}
	if len(r.enum) > 0 && !contains(r.enum, strconv.FormatFloat(f, 'f', -1, 64)) {
		c.add(n, path, CodeNotAllowed, fmt.Sprintf("%s is not an allowed value", n.Value), r.enum...)
		return
	}
	if (r.min != nil && f < *r.min) || (r.max != nil && f > *r.max) || (r.gt != nil && f <= *r.gt) {
		c.add(n, path, CodeOutOfRange, "expected "+rangeText(want, r))
	}
}

func fmtNum(f float64) string { return strconv.FormatFloat(f, 'f', -1, 64) }

func rangeText(kind string, r rules) string {
	num := fmtNum
	a := "a"
	if kind == "integer" {
		a = "an"
	}
	switch {
	case r.gt != nil && r.max != nil:
		return fmt.Sprintf("%s %s above %s and at most %s", a, kind, num(*r.gt), num(*r.max))
	case r.min != nil && r.max != nil:
		return fmt.Sprintf("%s %s in the range %s to %s", a, kind, num(*r.min), num(*r.max))
	case r.gt != nil && *r.gt == 0:
		return "a positive " + kind
	case r.min != nil && *r.min == 0:
		return a + " non-negative " + kind
	case r.min != nil:
		return fmt.Sprintf("%s %s of at least %s", a, kind, num(*r.min))
	case r.max != nil:
		return fmt.Sprintf("%s %s of at most %s", a, kind, num(*r.max))
	}
	return a + " " + kind
}

// pairs iterates a mapping's key/value pairs, reporting duplicate and
// non-scalar keys, and calls fn for each first occurrence.
func (c *collector) pairs(n *yaml.Node, path string, fn func(key string, k, v *yaml.Node)) {
	seen := map[string]int{}
	for i := 0; i+1 < len(n.Content); i += 2 {
		k, v := n.Content[i], n.Content[i+1]
		if k.Kind != yaml.ScalarNode {
			c.mismatch(k, path, "a scalar key")
			continue
		}
		if first, dup := seen[k.Value]; dup {
			c.add(k, join(path, k.Value), CodeDuplicateKey, fmt.Sprintf("key %q appears twice (first on line %d)", k.Value, first))
			continue
		}
		seen[k.Value] = k.Line
		fn(k.Value, k, v)
	}
}

func (c *collector) walkMap(n *yaml.Node, t reflect.Type, r, er rules, path string) {
	if n.Kind != yaml.MappingNode {
		c.mismatch(n, path, "mapping")
		return
	}
	c.pairs(n, path, func(key string, k, v *yaml.Node) {
		p := join(path, key)
		if len(r.keys) > 0 && !contains(r.keys, key) {
			if !c.secrets[k] {
				c.add(k, p, CodeNotAllowed, fmt.Sprintf("%q is not an allowed key", key), r.keys...)
			}
			return
		}
		c.walk(v, t.Elem(), er, rules{}, p)
	})
}

func (c *collector) walkStruct(n *yaml.Node, t reflect.Type, path string) {
	if n.Kind != yaml.MappingNode {
		c.mismatch(n, path, "mapping")
		return
	}
	before := len(c.issues)
	fields := structFields(t)
	present := map[string]bool{}
	c.pairs(n, path, func(key string, k, v *yaml.Node) {
		f, ok := fields[key]
		if !ok {
			if !c.secrets[k] {
				c.unknownKey(k, path, key, fields)
			}
			return
		}
		present[key] = true
		c.walk(v, f.typ, f.rules, f.erules, join(path, key))
	})
	for i := 0; i < t.NumField(); i++ {
		name := strings.Split(t.Field(i).Tag.Get("yaml"), ",")[0]
		if f, ok := fields[name]; ok && f.rules.required && !present[name] {
			c.add(n, join(path, name), CodeRequired, "required key is missing")
		}
	}
	if len(c.issues) != before {
		return
	}
	ptr := reflect.New(t)
	if cc, ok := ptr.Interface().(crossChecker); ok {
		if err := n.Decode(ptr.Interface()); err == nil {
			cc.cross(c, n, path)
		}
	}
}

func (c *collector) unknownKey(k *yaml.Node, path, key string, fields map[string]*fieldInfo) {
	names := make([]string, 0, len(fields))
	for name := range fields {
		names = append(names, name)
	}
	msg := fmt.Sprintf("unknown key %q", key)
	if best, ok := Closest(key, names); ok {
		msg += fmt.Sprintf("; did you mean %q?", best)
	}
	if hint := unknownHints[path]; hint != "" {
		msg += "; " + hint
	}
	c.add(k, join(path, key), CodeUnknownKey, msg)
}

// unknownHints maps a mapping path to the sentence appended to its unknown-key
// errors.
var unknownHints = map[string]string{
	"notifications": "the per-channel notification switches are runtime settings, change them in the dashboard notification settings",
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// validateDoc parses src and checks it against schema type T. It returns the
// report and, when src parsed as a mapping, the document's root node.
func validateDoc[T any](file string, src []byte) (Report, *yaml.Node) {
	c := &collector{file: file, secrets: map[*yaml.Node]bool{}}
	var doc yaml.Node
	if err := yaml.Unmarshal(src, &doc); err != nil {
		c.issues = append(c.issues, parseIssue(file, err))
		return finishReport(c.issues), nil
	}
	if doc.Kind == 0 || len(doc.Content) == 0 {
		c.add(nil, "", CodeTypeMismatch, "the file is empty: expected a mapping")
		return finishReport(c.issues), nil
	}
	root := deref(doc.Content[0])
	if isNull(root) {
		c.mismatch(root, "", "a mapping")
		return finishReport(c.issues), nil
	}
	c.scanSecrets(root, "")
	var zero T
	c.walk(root, reflect.TypeOf(zero), rules{}, rules{}, "")
	return finishReport(c.issues), root
}

func parseIssue(file string, err error) Issue {
	msg := strings.TrimPrefix(err.Error(), "yaml: ")
	i := Issue{File: file, Code: CodeInvalidYAML, Message: msg}
	if rest, ok := strings.CutPrefix(msg, "line "); ok {
		num, after, _ := strings.Cut(rest, ":")
		if line, convErr := strconv.Atoi(num); convErr == nil {
			i.Line, i.Column = line, 1
			i.Message = strings.TrimSpace(after)
		}
	}
	return i
}

func decodeDoc[T any](root *yaml.Node, into *T) {
	if root != nil {
		_ = root.Decode(into)
	}
}
