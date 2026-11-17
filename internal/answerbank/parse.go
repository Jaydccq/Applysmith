// Package answerbank loads, validates, seeds and saves the user's answer bank
// (answer-bank.yml), decides which entries still need the user's answer,
// derives entries from the profile and keeps the answer-bank runtime settings.
// The entry types and the matcher live in the pure package match.
package answerbank

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"reflect"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"

	"applysmith/internal/answerbank/match"
	"applysmith/internal/lazyregexp"
)

// FileName is the bank file inside the workspace.
const FileName = "answer-bank.yml"

// Result is a successfully parsed bank with the warnings the load produced.
type Result struct {
	Bank     match.Bank
	Warnings []string
}

// LoadError is a failure that makes the whole file unusable. The bank already
// in memory stays in use.
type LoadError struct {
	File    string
	Path    string // key path, for example entries[3].answer
	Line    int
	Message string
}

func (e *LoadError) Error() string {
	var b strings.Builder
	b.WriteString(e.File)
	if e.Path != "" {
		b.WriteString(": " + e.Path)
	}
	if e.Line > 0 {
		fmt.Fprintf(&b, " (line %d)", e.Line)
	}
	b.WriteString(": " + e.Message)
	return b.String()
}

// Parse reads an answer bank. An empty file is the empty bank with a warning.
// A file over MaxBankBytes, a syntax error, a second YAML document, an alias
// or merge key, a duplicate mapping key, a schema error, a missing or wrong
// version, an unknown top-level key or an unknown category fail the load;
// every other problem drops the offending entry or value with a warning. An
// override may name a shipped default the file lacks, since every load adds
// that default (withDefaults). No message repeats what the file holds beyond
// short vocabulary words.
func Parse(file string, data []byte) (Result, error) {
	def, err := DefaultBank()
	if err != nil {
		return Result{}, err
	}
	return parse(file, data, def.Bank.Entries)
}

// parse is Parse with shipped as the defaults an override may name; the
// defaults themselves are parsed with none.
func parse(file string, data []byte, shipped []match.Entry) (Result, error) {
	if len(data) > MaxBankBytes {
		return Result{}, &LoadError{File: file, Message: fmt.Sprintf("the file is larger than %d MiB", MaxBankBytes>>20)}
	}
	root, err := readDocument(file, data)
	if err != nil {
		return Result{}, err
	}
	empty := Result{Bank: match.Bank{Version: 1, Overrides: map[string][]match.Override{}}}
	if root == nil || isNull(root) {
		empty.Warnings = []string{"answer-bank: file is empty"}
		return empty, nil
	}
	if err := checkDuplicateKeys(file, root, ""); err != nil {
		return Result{}, err
	}
	if root.Kind != yaml.MappingNode {
		return Result{}, &LoadError{File: file, Line: root.Line, Message: "the file must hold a mapping with version, employers, entries and overrides"}
	}
	p := &parser{file: file, shipped: shipped}
	return p.bank(root)
}

// readDocument decodes the single YAML document of a bank file and returns its
// root, nil when the file holds no content. A second document, an alias and a
// merge key are refused: the bank is plain data, and an alias can expand a few
// lines into a very large structure.
func readDocument(file string, data []byte) (*yaml.Node, error) {
	doc, err := readDocumentNode(file, data)
	if err != nil || doc == nil || len(doc.Content) == 0 {
		return nil, err
	}
	return doc.Content[0], nil
}

// readDocumentNode is readDocument's document node, which also holds the
// comments above and below the root; nil when the file is empty.
func readDocumentNode(file string, data []byte) (*yaml.Node, error) {
	dec := yaml.NewDecoder(bytes.NewReader(data))
	var doc yaml.Node
	if err := dec.Decode(&doc); err != nil {
		if errors.Is(err, io.EOF) {
			return nil, nil
		}
		return nil, syntaxError(file, err)
	}
	var next yaml.Node
	switch err := dec.Decode(&next); {
	case err == nil:
		return nil, &LoadError{File: file, Line: next.Line, Message: "the file holds more than one YAML document; keep a single document"}
	case !errors.Is(err, io.EOF):
		return nil, syntaxError(file, err)
	}
	if err := rejectIndirection(file, &doc, ""); err != nil {
		return nil, err
	}
	return &doc, nil
}

// rejectIndirection fails on an alias node, as a value or as a key, or a merge
// key.
func rejectIndirection(file string, n *yaml.Node, path string) error {
	if n.Kind == yaml.AliasNode {
		return &LoadError{File: file, Path: path, Line: n.Line, Message: "YAML aliases (*name) are not supported; write the value out"}
	}
	for i, c := range n.Content {
		sub := path
		if n.Kind == yaml.MappingNode && i%2 == 0 {
			if c.Tag == "!!merge" {
				return &LoadError{File: file, Path: path, Line: c.Line, Message: "YAML merge keys (<<) are not supported; write the fields out"}
			}
			if err := rejectIndirection(file, c, path); err != nil {
				return err
			}
			continue
		}
		switch n.Kind {
		case yaml.MappingNode:
			sub = joinPath(path, n.Content[i-1].Value)
		case yaml.SequenceNode:
			sub = fmt.Sprintf("%s[%d]", path, i)
		}
		if err := rejectIndirection(file, c, sub); err != nil {
			return err
		}
	}
	return nil
}

func joinPath(path, key string) string {
	if path == "" {
		return key
	}
	return path + "." + key
}

type parser struct {
	file     string
	warnings []string
	shipped  []match.Entry
	held     map[string]bool // the id of every entry the file holds, dropped or not
}

func (p *parser) warn(format string, args ...any) {
	p.warnings = append(p.warnings, fmt.Sprintf(format, args...))
}

func (p *parser) fail(n *yaml.Node, path, format string, args ...any) error {
	line := 0
	if n != nil {
		line = n.Line
	}
	return &LoadError{File: p.file, Path: path, Line: line, Message: fmt.Sprintf(format, args...)}
}

func (p *parser) bank(root *yaml.Node) (Result, error) {
	var versionNode, employers, entries, overrides *yaml.Node
	for i := 0; i+1 < len(root.Content); i += 2 {
		k, v := root.Content[i], root.Content[i+1]
		switch k.Value {
		case "version":
			versionNode = v
		case "employers":
			employers = v
		case "entries":
			entries = v
		case "overrides":
			overrides = v
		default:
			return Result{}, p.fail(k, word(k.Value), "unknown top-level key %s; the allowed keys are version, employers, entries and overrides", quoted(k.Value))
		}
	}
	if versionNode == nil {
		return Result{}, p.fail(root, "version", "version is required and must be 1")
	}
	if versionNode.Value != "1" || versionNode.Tag != "!!int" {
		return Result{}, p.fail(versionNode, "version", "unsupported version %s; this product reads version 1", word(versionNode.Value))
	}
	b := match.Bank{Version: 1, Overrides: map[string][]match.Override{}}
	var err error
	if b.Employers, err = p.parseEmployers(employers); err != nil {
		return Result{}, err
	}
	if b.Entries, err = p.parseEntries(entries); err != nil {
		return Result{}, err
	}
	if err := p.parseOverrides(overrides, &b); err != nil {
		return Result{}, err
	}
	return Result{Bank: b, Warnings: p.warnings}, nil
}

func (p *parser) parseEmployers(n *yaml.Node) ([]match.Employer, error) {
	if n == nil || isNull(n) {
		return nil, nil
	}
	if n.Kind != yaml.SequenceNode {
		return nil, p.fail(n, "employers", "employers must be a list")
	}
	var out []match.Employer
	for i, item := range n.Content {
		path := fmt.Sprintf("employers[%d]", i)
		var e match.Employer
		if err := item.Decode(&e); err != nil {
			return nil, p.fail(item, path, "%s", typeMessage(err))
		}
		if strings.TrimSpace(e.Name) == "" {
			return nil, p.fail(item, path+".name", "name is required")
		}
		out = append(out, e)
	}
	return out, nil
}

func (p *parser) parseEntries(n *yaml.Node) ([]match.Entry, error) {
	if n == nil || isNull(n) {
		return nil, nil
	}
	if n.Kind != yaml.SequenceNode {
		return nil, p.fail(n, "entries", "entries must be a list")
	}
	var out []match.Entry
	seen := map[string]bool{}
	p.held = seen
	for i, item := range n.Content {
		path := fmt.Sprintf("entries[%d]", i)
		if item.Kind != yaml.MappingNode {
			return nil, p.fail(item, path, "an entry must be a mapping")
		}
		id, err := p.entryID(item, path)
		if err != nil {
			return nil, err
		}
		// The duplicate check runs before validation, so an invalid first
		// occurrence still shadows a valid later one.
		if seen[id] {
			p.warn("answer-bank: duplicate entry id %q: keeping the first occurrence, dropping the later one", id)
			continue
		}
		seen[id] = true
		e, keep, err := p.entry(item, path, id)
		if err != nil {
			return nil, err
		}
		if keep {
			out = append(out, e)
		}
	}
	return out, nil
}

func (p *parser) entryID(item *yaml.Node, path string) (string, error) {
	v := mapValue(item, "id")
	if v == nil || v.Kind != yaml.ScalarNode || strings.TrimSpace(v.Value) == "" {
		return "", p.fail(item, path+".id", "id is required and must be a non-empty string")
	}
	return v.Value, nil
}

// entry decodes one entry mapping. keep is false when the entry is dropped
// with a warning.
func (p *parser) entry(n *yaml.Node, path, id string) (e match.Entry, keep bool, err error) {
	if err := n.Decode(&e); err != nil {
		return e, false, p.fail(n, path, "%s", typeMessage(err))
	}
	for i := 0; i+1 < len(n.Content); i += 2 {
		if key := n.Content[i].Value; !knownEntryKeys[key] {
			p.warn("answer-bank: entry %q: unknown field %s ignored", id, quoted(key))
		}
	}
	if !e.Kind.Valid() {
		p.warn("answer-bank: entry %q has unknown kind %s; dropped", id, quoted(string(e.Kind)))
		return e, false, nil
	}
	present := presentKeys(n)
	if missing := missingRequired(e.Kind, present); len(missing) > 0 {
		p.warn("answer-bank: entry %q kind=%s missing %s; dropped", id, e.Kind, strings.Join(missing, ", "))
		return e, false, nil
	}
	normalizeBody(&e.Body, n)
	if e.Category != "" && !match.ValidCategory(e.Category) {
		return e, false, p.fail(n, path+".category", "entry %q: unknown category %s; the allowed values are %s", id, quoted(e.Category), strings.Join(match.Categories, ", "))
	}
	repairEntry(&e, func(format string, args ...any) {
		p.warn("answer-bank: entry %q: %s", id, fmt.Sprintf(format, args...))
	})
	return e, true, nil
}

func (p *parser) parseOverrides(n *yaml.Node, b *match.Bank) error {
	if n == nil || isNull(n) {
		return nil
	}
	if n.Kind == yaml.SequenceNode && len(n.Content) == 0 {
		return nil
	}
	if n.Kind != yaml.MappingNode {
		return p.fail(n, "overrides", "overrides must map a host to a list of overrides")
	}
	pending := map[string][]match.Override{}
	var order []string
	for i := 0; i+1 < len(n.Content); i += 2 {
		host, list := strings.TrimSpace(n.Content[i].Value), n.Content[i+1]
		path := "overrides." + host
		if isNull(list) {
			continue
		}
		if list.Kind != yaml.SequenceNode {
			return p.fail(list, path, "the overrides of a host must be a list")
		}
		key := strings.ToLower(host)
		if first, seen := b.HostSpelling[key]; seen && first != host {
			p.warn("answer-bank: overrides for hosts %s and %s differ only in case; they are merged under the first", quoted(first), quoted(host))
		}
		if _, seen := pending[key]; !seen {
			order = append(order, key)
			if b.HostSpelling == nil {
				b.HostSpelling = map[string]string{}
			}
			b.HostSpelling[key] = host
		}
		for j, item := range list.Content {
			o, err := p.override(item, fmt.Sprintf("%s[%d]", path, j), host)
			if err != nil {
				return err
			}
			pending[key] = append(pending[key], o)
		}
	}
	// The load adds each shipped default the file lacks, and its overrides
	// apply to it as to the file's own entries.
	known := *b
	known.Entries = slices.Clone(b.Entries)
	for _, d := range p.shipped {
		if !p.held[d.ID] {
			known.Entries = append(known.Entries, d)
		}
	}
	for _, key := range order {
		host := b.HostSpelling[key]
		kept, _ := resolveOverrides(known, pending[key], func(o match.Override, format string, args ...any) {
			p.warn("answer-bank: override of %q on %s was ignored; the base entry stays as it is: %s", o.ID, quoted(host), fmt.Sprintf(format, args...))
		})
		if len(kept) > 0 {
			b.Overrides[key] = kept
		}
	}
	return nil
}

// resolveOverrides replays the overrides of one host over the bank in file
// order, the way matching applies them, and returns the ones that apply plus
// the entry each touched id ends as. An override that cannot apply, or a field
// the entry's kind does not use, is reported and left out; the load drops what
// is reported and a save refuses it.
func resolveOverrides(b match.Bank, list []match.Override, report func(o match.Override, format string, args ...any)) ([]match.Override, map[string]match.Entry) {
	cur := map[string]match.Entry{}
	var kept []match.Override
	for _, o := range list {
		base, ok := cur[o.ID]
		if !ok {
			if base, ok = b.Find(o.ID); !ok {
				report(o, "it targets an unknown entry id")
				continue
			}
		}
		merged, err := o.Apply(base)
		if err != nil {
			report(o, "%v", err)
			continue
		}
		var fields []string
		for _, f := range o.Fields {
			if slices.Contains(match.BodyKeys(merged.Kind), f) {
				fields = append(fields, f)
				continue
			}
			report(o, "field %s is not used by kind %s", f, merged.Kind)
		}
		o.Fields = fields
		cur[o.ID] = merged
		kept = append(kept, o)
	}
	return kept, cur
}

func (p *parser) override(item *yaml.Node, path, host string) (match.Override, error) {
	if item.Kind != yaml.MappingNode {
		return match.Override{}, p.fail(item, path, "an override must be a mapping")
	}
	id, err := p.entryID(item, path)
	if err != nil {
		return match.Override{}, err
	}
	var e match.Entry
	if err := item.Decode(&e); err != nil {
		return match.Override{}, p.fail(item, path, "%s", typeMessage(err))
	}
	for i := 0; i+1 < len(item.Content); i += 2 {
		if key := item.Content[i].Value; key != "id" && key != "kind" && !slices.Contains(allAnswerKeys, key) {
			p.warn("answer-bank: override of %q on %s: %s is not an answer field and was ignored", id, quoted(host), quoted(key))
		}
	}
	normalizeBody(&e.Body, item)
	present := presentKeys(item)
	var fields []string
	for _, key := range allAnswerKeys {
		if present[key] {
			fields = append(fields, key)
		}
	}
	return match.Override{ID: id, Kind: e.Kind, Body: e.Body, Fields: fields}, nil
}

// allAnswerKeys lists the answer-field keys of every kind once, in kind order.
var allAnswerKeys = func() []string {
	var out []string
	for _, kind := range match.Kinds {
		for _, key := range match.BodyKeys(kind) {
			if !slices.Contains(out, key) {
				out = append(out, key)
			}
		}
	}
	return out
}()

// normalizeBody keeps absent and empty lists apart: a key that is present
// holds a non-nil slice, an absent or null key holds nil.
func normalizeBody(b *match.Body, n *yaml.Node) {
	for key, field := range map[string]*[]string{"preferred": &b.Preferred, "proficiency_options": &b.ProficiencyOptions, "selected": &b.Selected} {
		v := mapValue(n, key)
		switch {
		case v == nil || isNull(v):
			*field = nil
		case *field == nil:
			*field = []string{}
		}
	}
}

func missingRequired(k match.Kind, present map[string]bool) []string {
	var missing []string
	for _, key := range match.RequiredKeys(k) {
		if !present[key] {
			missing = append(missing, key)
		}
	}
	return missing
}

// presentKeys lists the keys a mapping states with a value (a null counts as
// stated: the empty answer is a legitimate body).
func presentKeys(n *yaml.Node) map[string]bool {
	out := map[string]bool{}
	for i := 0; i+1 < len(n.Content); i += 2 {
		out[n.Content[i].Value] = true
	}
	return out
}

func mapValue(n *yaml.Node, key string) *yaml.Node {
	for i := 0; i+1 < len(n.Content); i += 2 {
		if n.Content[i].Value == key {
			return n.Content[i+1]
		}
	}
	return nil
}

func isNull(n *yaml.Node) bool {
	return n == nil || n.Kind == 0 || (n.Kind == yaml.ScalarNode && n.Tag == "!!null")
}

// checkDuplicateKeys fails on a repeated key in any mapping, naming the file,
// the key path and the line of the second occurrence.
func checkDuplicateKeys(file string, n *yaml.Node, path string) error {
	switch n.Kind {
	case yaml.MappingNode:
		seen := map[string]int{}
		for i := 0; i+1 < len(n.Content); i += 2 {
			k := n.Content[i]
			keyPath := joinPath(path, word(k.Value))
			if first, dup := seen[k.Value]; dup {
				return &LoadError{File: file, Path: keyPath, Line: k.Line, Message: fmt.Sprintf("duplicate key %s (first on line %d)", quoted(k.Value), first)}
			}
			seen[k.Value] = k.Line
			if err := checkDuplicateKeys(file, n.Content[i+1], keyPath); err != nil {
				return err
			}
		}
	case yaml.SequenceNode:
		for i, c := range n.Content {
			if err := checkDuplicateKeys(file, c, fmt.Sprintf("%s[%d]", path, i)); err != nil {
				return err
			}
		}
	}
	return nil
}

var yamlLine = lazyregexp.New(`line (\d+)`)

func syntaxError(file string, err error) error {
	msg := scrub(strings.TrimPrefix(err.Error(), "yaml: "))
	le := &LoadError{File: file, Message: "the file is not valid YAML: " + msg}
	if m := yamlLine.FindStringSubmatch(msg); m != nil {
		fmt.Sscanf(m[1], "%d", &le.Line)
	}
	return le
}

// typeMessage reduces a yaml decode error to its mismatch lines.
func typeMessage(err error) string {
	if te, ok := err.(*yaml.TypeError); ok {
		return scrub(strings.Join(te.Errors, "; "))
	}
	return scrub(strings.TrimPrefix(err.Error(), "yaml: "))
}

// knownEntryKeys are the keys an entry may state.
var knownEntryKeys = func() map[string]bool {
	out := map[string]bool{}
	var walk func(t reflect.Type)
	walk = func(t reflect.Type) {
		for i := 0; i < t.NumField(); i++ {
			f := t.Field(i)
			tag := f.Tag.Get("yaml")
			if tag == "-" {
				continue
			}
			if strings.Contains(tag, ",inline") {
				walk(f.Type)
				continue
			}
			out[strings.Split(tag, ",")[0]] = true
		}
	}
	walk(reflect.TypeOf(match.Entry{}))
	return out
}()
