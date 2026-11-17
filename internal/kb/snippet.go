package kb

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"gopkg.in/yaml.v3"

	"applysmith/internal/lazyregexp"
)

// The four snippet categories; a file directly in knowledge/ has none.
const (
	CatProjects       = "projects"
	CatExperience     = "experience"
	CatBehavioral     = "behavioral"
	CatAnswerPatterns = "answer-patterns"
)

// Categories lists the snippet categories in directory order.
var Categories = []string{CatProjects, CatExperience, CatBehavioral, CatAnswerPatterns}

// ErrFrontmatter marks a document whose frontmatter cannot be parsed.
var ErrFrontmatter = errors.New("kb: unparseable frontmatter")

// Section is one heading-delimited part of a snippet body.
type Section struct {
	Heading string   `json:"heading"`
	Level   int      `json:"level"`
	Text    string   `json:"text"`
	Facts   []string `json:"facts,omitempty"`
}

// Link is a bundle-relative link to another snippet.
type Link struct {
	Label string `json:"label"`
	Path  string `json:"path"`
}

// Snippet is one parsed knowledge file.
type Snippet struct {
	ID                    string              `json:"id"`
	Path                  string              `json:"path"`
	Category              string              `json:"category"`
	Title                 string              `json:"title,omitempty"`
	Description           string              `json:"description,omitempty"`
	Tags                  []string            `json:"tags,omitempty"`
	Triggers              []string            `json:"triggers,omitempty"`
	AllowedFacts          []string            `json:"allowed_facts,omitempty"`
	SectionFacts          map[string][]string `json:"section_facts,omitempty"`
	PreferForCompanyKinds []string            `json:"prefer_for_company_kinds,omitempty"`
	Voice                 string              `json:"voice,omitempty"`
	MaxLength             int                 `json:"max_length,omitempty"`
	Principles            []string            `json:"principles,omitempty"`
	StorySourceType       string              `json:"story_source_type,omitempty"`
	Dimensions            []string            `json:"dimensions,omitempty"`
	Sections              []Section           `json:"sections,omitempty"`
	Links                 []Link              `json:"links,omitempty"`
	Citations             []string            `json:"citations,omitempty"`
	// Extra holds the scalar frontmatter keys this package does not model, such
	// as a brief's source_url and fetched_at.
	Extra       map[string]string `json:"extra,omitempty"`
	ContentHash string            `json:"content_hash"`
	// ExplicitFacts is true when the frontmatter carries allowed_facts, even as
	// an empty list.
	ExplicitFacts bool `json:"explicit_facts,omitempty"`
	SectionCount  int  `json:"section_count"`

	body string
}

// Facts returns the document-level facts: the explicit allowed_facts list, or
// the union of the section facts when the list is absent.
func (s *Snippet) Facts() []string {
	if s.ExplicitFacts {
		return s.AllowedFacts
	}
	var out []string
	seen := map[string]bool{}
	for _, sec := range s.Sections {
		for _, f := range sec.Facts {
			if !seen[f] {
				seen[f] = true
				out = append(out, f)
			}
		}
	}
	for _, h := range sortedKeys(s.SectionFacts) {
		for _, f := range s.SectionFacts[h] {
			if !seen[f] {
				seen[f] = true
				out = append(out, f)
			}
		}
	}
	return out
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// BodyOf is a document's Markdown without its frontmatter, what a reader
// sees; a broken frontmatter block leaves the whole text.
func BodyOf(markdown string) string {
	if _, body, _, err := splitFrontmatter(markdown); err == nil {
		return strings.TrimSpace(body)
	}
	return markdown
}

// splitFrontmatter separates a leading "---" delimited block from the body.
// A document with no leading delimiter has no frontmatter.
func splitFrontmatter(data string) (fm string, body string, has bool, err error) {
	data = strings.TrimPrefix(data, "\ufeff")
	data = strings.ReplaceAll(data, "\r\n", "\n")
	if !strings.HasPrefix(data, "---\n") && data != "---" {
		return "", data, false, nil
	}
	rest := strings.TrimPrefix(data, "---\n")
	i := 0
	for i <= len(rest) {
		j := strings.IndexByte(rest[i:], '\n')
		line := rest[i:]
		if j >= 0 {
			line = rest[i : i+j]
		}
		if strings.TrimRight(line, " \t") == "---" {
			body = ""
			if j >= 0 {
				body = rest[i+j+1:]
			}
			return rest[:i], body, true, nil
		}
		if j < 0 {
			break
		}
		i += j + 1
	}
	return "", "", true, fmt.Errorf("%w: the opening delimiter has no closing delimiter", ErrFrontmatter)
}

// frontmatterNode decodes the frontmatter block into a mapping node, so keys
// this package does not know survive a rewrite.
func frontmatterNode(fm string) (*yaml.Node, error) {
	var doc yaml.Node
	if err := yaml.Unmarshal([]byte(fm), &doc); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrFrontmatter, err)
	}
	if doc.Kind == 0 || (doc.Kind == yaml.DocumentNode && len(doc.Content) == 0) {
		return &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}, nil
	}
	n := &doc
	if n.Kind == yaml.DocumentNode {
		n = n.Content[0]
	}
	if n.Kind == yaml.ScalarNode && n.Tag == "!!null" {
		return &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}, nil
	}
	if n.Kind != yaml.MappingNode {
		return nil, fmt.Errorf("%w: the frontmatter is not a mapping", ErrFrontmatter)
	}
	return n, nil
}

func mapSet(m *yaml.Node, key string, v *yaml.Node) {
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			m.Content[i+1] = v
			return
		}
	}
	m.Content = append(m.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key}, v)
}

func mapDelete(m *yaml.Node, key string) {
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			m.Content = append(m.Content[:i], m.Content[i+2:]...)
			return
		}
	}
}

func strNode(s string) *yaml.Node { return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: s} }

func listNode(items []string) *yaml.Node {
	n := &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq"}
	for _, it := range items {
		n.Content = append(n.Content, strNode(it))
	}
	return n
}

func fieldErr(key, msg string) error {
	return fmt.Errorf("%w: %s %s", ErrFrontmatter, key, msg)
}

func scalarString(n *yaml.Node, key string) (string, error) {
	if n.Kind != yaml.ScalarNode {
		return "", fieldErr(key, "must be a string")
	}
	if n.Tag == "!!null" {
		return "", nil
	}
	return strings.TrimSpace(n.Value), nil
}

func stringList(n *yaml.Node, key string) ([]string, error) {
	if n.Kind == yaml.ScalarNode && n.Tag == "!!null" {
		return nil, nil
	}
	if n.Kind != yaml.SequenceNode {
		return nil, fieldErr(key, "must be a list")
	}
	out := make([]string, 0, len(n.Content))
	for _, c := range n.Content {
		if c.Kind != yaml.ScalarNode || c.Tag == "!!null" {
			return nil, fieldErr(key, "must hold only text items")
		}
		out = append(out, strings.TrimSpace(c.Value))
	}
	return out, nil
}

// ParseSnippet parses one knowledge file. rel is its path relative to the
// workspace (forward slashes); category comes from the directory.
func ParseSnippet(rel string, data []byte) (*Snippet, error) {
	fm, body, has, err := splitFrontmatter(string(data))
	if err != nil {
		return nil, err
	}
	s := &Snippet{Path: rel, Category: categoryOf(rel), body: normalizeBody(body)}
	if has {
		node, err := frontmatterNode(fm)
		if err != nil {
			return nil, err
		}
		if err := s.readFrontmatter(node); err != nil {
			return nil, err
		}
	}
	if s.ID == "" {
		s.ID = strings.TrimSuffix(path.Base(rel), path.Ext(rel))
	}
	s.parseBody()
	s.ContentHash = s.computeHash()
	return s, nil
}

func normalizeBody(b string) string {
	return strings.TrimSpace(strings.ReplaceAll(strings.TrimPrefix(b, "\ufeff"), "\r\n", "\n"))
}

// categoryOf reads the category from the first directory below knowledge/.
func categoryOf(rel string) string {
	parts := strings.Split(rel, "/")
	if len(parts) >= 3 && parts[0] == KnowledgeDir {
		for _, c := range Categories {
			if parts[1] == c {
				return c
			}
		}
	}
	return ""
}

func (s *Snippet) readFrontmatter(m *yaml.Node) error {
	for i := 0; i+1 < len(m.Content); i += 2 {
		key, v := m.Content[i].Value, m.Content[i+1]
		var err error
		switch key {
		case "id":
			s.ID, err = scalarString(v, key)
		case "title":
			s.Title, err = scalarString(v, key)
		case "description":
			s.Description, err = scalarString(v, key)
		case "voice":
			s.Voice, err = scalarString(v, key)
			if err == nil && v.Tag != "!!null" && s.Voice == "" {
				err = fieldErr(key, "must not be empty")
			}
		case "tags":
			s.Tags, err = stringList(v, key)
		case "allowed_facts":
			s.AllowedFacts, err = stringList(v, key)
			s.ExplicitFacts = err == nil && !(v.Kind == yaml.ScalarNode && v.Tag == "!!null")
		case "prefer_for_company_kinds":
			s.PreferForCompanyKinds, err = stringList(v, key)
		case "principles":
			s.Principles, err = stringList(v, key)
		case "dimensions":
			s.Dimensions, err = stringList(v, key)
		case "story_source_type":
			s.StorySourceType, err = scalarString(v, key)
		case "triggers":
			s.Triggers, err = stringList(v, key)
			if err == nil && len(s.Triggers) > maxTriggers {
				err = fieldErr(key, fmt.Sprintf("holds more than %d triggers", maxTriggers))
			}
			for _, t := range s.Triggers {
				if err == nil && t == "" {
					err = fieldErr(key, "must not hold an empty trigger")
				}
				if err == nil && len(t) > maxTriggerBytes {
					err = fieldErr(key, fmt.Sprintf("holds a trigger longer than %d bytes", maxTriggerBytes))
				}
			}
		case "max_length":
			if v.Kind != yaml.ScalarNode {
				return fieldErr(key, "must be a positive integer")
			}
			n, perr := strconv.Atoi(v.Value)
			if perr != nil || n <= 0 {
				return fieldErr(key, "must be a positive integer")
			}
			s.MaxLength = n
		case "section_facts":
			s.SectionFacts, err = sectionFacts(v)
		default:
			if v.Kind == yaml.ScalarNode && v.Tag != "!!null" {
				if s.Extra == nil {
					s.Extra = map[string]string{}
				}
				s.Extra[key] = strings.TrimSpace(v.Value)
			}
		}
		if err != nil {
			return err
		}
	}
	return nil
}

func sectionFacts(n *yaml.Node) (map[string][]string, error) {
	if n.Kind == yaml.ScalarNode && n.Tag == "!!null" {
		return nil, nil
	}
	if n.Kind != yaml.MappingNode {
		return nil, fieldErr("section_facts", "must map a heading to a list of facts")
	}
	out := map[string][]string{}
	for i := 0; i+1 < len(n.Content); i += 2 {
		facts, err := stringList(n.Content[i+1], "section_facts")
		if err != nil {
			return nil, err
		}
		out[strings.TrimSpace(n.Content[i].Value)] = facts
	}
	return out, nil
}

var (
	headingRE   = lazyregexp.New(`^(#{1,6})[ \t]+(.*?)(?:[ \t]+#+)?[ \t]*$`)
	fenceRE     = lazyregexp.New("^\\s*(```|~~~)")
	linkRE      = lazyregexp.New(`\[([^\]]*)\]\((/[^)\s#]+\.md)(?:#[^)]*)?\)`)
	citationRE  = lazyregexp.New(`^\s*(?:\d+[.)]|\[\d+\])\s+(.+)$`)
	citationsRE = lazyregexp.New(`(?i)^citations?$`)
)

// parseBody splits the body at Markdown headings. Content before the first
// heading is its own section, a body with no headings is one section, and the
// Citations section is read into Citations instead of being indexed.
func (s *Snippet) parseBody() {
	type raw struct {
		heading string
		level   int
		lines   []string
	}
	cur := &raw{}
	var parts []*raw
	inFence := false
	for _, line := range strings.Split(s.body, "\n") {
		if fenceRE.MatchString(line) {
			inFence = !inFence
		}
		if m := headingRE.FindStringSubmatch(line); m != nil && !inFence {
			parts = append(parts, cur)
			cur = &raw{heading: strings.TrimSpace(m[2]), level: len(m[1])}
			continue
		}
		cur.lines = append(cur.lines, line)
	}
	parts = append(parts, cur)

	for i, p := range parts {
		text := strings.TrimSpace(strings.Join(p.lines, "\n"))
		if i == 0 && p.heading == "" && text == "" {
			continue
		}
		if citationsRE.MatchString(p.heading) {
			for _, l := range p.lines {
				if m := citationRE.FindStringSubmatch(l); m != nil {
					s.Citations = append(s.Citations, strings.TrimSpace(m[1]))
				}
			}
			continue
		}
		sec := Section{Heading: p.heading, Level: p.level, Text: text}
		sec.Facts = s.factsForHeading(p.heading)
		s.Sections = append(s.Sections, sec)
	}
	seen := map[string]bool{}
	for _, m := range linkRE.FindAllStringSubmatch(s.body, -1) {
		if !seen[m[2]] {
			seen[m[2]] = true
			s.Links = append(s.Links, Link{Label: m[1], Path: m[2]})
		}
	}
}

func (s *Snippet) factsForHeading(h string) []string {
	for k, v := range s.SectionFacts {
		if strings.EqualFold(strings.TrimSpace(k), h) {
			return v
		}
	}
	return nil
}

// hashInput is the retrieval-relevant part of a snippet. Descriptive fields
// (title, description) and unknown keys such as timestamps are left out.
type hashInput struct {
	ID         string              `json:"id"`
	Tags       []string            `json:"tags"`
	Triggers   []string            `json:"triggers"`
	Facts      []string            `json:"allowed_facts"`
	Explicit   bool                `json:"explicit_facts"`
	Section    map[string][]string `json:"section_facts"`
	Kinds      []string            `json:"prefer_for_company_kinds"`
	Voice      string              `json:"voice"`
	MaxLength  int                 `json:"max_length"`
	Principles []string            `json:"principles"`
	SourceType string              `json:"story_source_type"`
	Dimensions []string            `json:"dimensions"`
	Body       string              `json:"body"`
}

func sortedCopy(in []string) []string {
	out := append([]string{}, in...)
	sort.Strings(out)
	return out
}

func (s *Snippet) computeHash() string {
	in := hashInput{
		ID: s.ID, Tags: sortedCopy(s.Tags), Triggers: sortedCopy(s.Triggers),
		Facts: sortedCopy(s.AllowedFacts), Explicit: s.ExplicitFacts, Section: map[string][]string{},
		Kinds: sortedCopy(s.PreferForCompanyKinds), Voice: s.Voice, MaxLength: s.MaxLength,
		Principles: sortedCopy(s.Principles), SourceType: s.StorySourceType, Dimensions: sortedCopy(s.Dimensions),
		Body: s.body,
	}
	for k, v := range s.SectionFacts {
		in.Section[k] = sortedCopy(v)
	}
	b, _ := json.Marshal(in)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func fileHash(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func textHash(s string) string { return fileHash([]byte(s)) }

func runeLen(s string) int { return utf8.RuneCountInString(s) }
