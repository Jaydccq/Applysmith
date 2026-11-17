// Package skills holds the Ask Agent's skills: a name, the tools a turn needs
// for one job and an instruction block of at most MaxBlockLines lines. A skill
// is a file of this directory, embedded in the binary and versioned with the
// harness. The package only parses; internal/agent checks each skill against
// the tool catalog and the screening of instruction text before a turn loads
// one.
package skills

import (
	"embed"
	"fmt"
	"io/fs"
	"slices"
	"strings"
)

// MaxBlockLines is the most lines an instruction block may have.
const MaxBlockLines = 60

//go:embed *.md
var files embed.FS

// Skill is one parsed skill file.
type Skill struct {
	Name  string
	Tools []string
	// Block is the instruction text, one line per rule.
	Block string
}

// All parses every skill file, in name order. A file that is not a skill is an
// error that names it.
func All() ([]Skill, error) {
	entries, err := fs.ReadDir(files, ".")
	if err != nil {
		return nil, err
	}
	var out []Skill
	for _, e := range entries {
		raw, err := files.ReadFile(e.Name())
		if err != nil {
			return nil, err
		}
		s, err := parse(strings.TrimSuffix(e.Name(), ".md"), string(raw))
		if err != nil {
			return nil, fmt.Errorf("skill %s: %w", e.Name(), err)
		}
		out = append(out, s)
	}
	slices.SortFunc(out, func(a, b Skill) int { return strings.Compare(a.Name, b.Name) })
	return out, nil
}

// parse reads the front matter (name and tools, one line each) and the block.
func parse(file, raw string) (Skill, error) {
	head, block, ok := strings.Cut(strings.TrimPrefix(raw, "---\n"), "\n---\n")
	if !ok || !strings.HasPrefix(raw, "---\n") {
		return Skill{}, fmt.Errorf("the front matter is missing")
	}
	var s Skill
	for _, line := range strings.Split(head, "\n") {
		key, val, found := strings.Cut(line, ":")
		switch strings.TrimSpace(key) {
		case "name":
			s.Name = strings.TrimSpace(val)
		case "tools":
			for _, t := range strings.Split(val, ",") {
				if t = strings.TrimSpace(t); t != "" {
					s.Tools = append(s.Tools, t)
				}
			}
		default:
			if found || strings.TrimSpace(line) != "" {
				return Skill{}, fmt.Errorf("unknown front matter line %q", line)
			}
		}
	}
	if s.Name != file {
		return Skill{}, fmt.Errorf("the name %q is not the file's name", s.Name)
	}
	s.Block = strings.TrimSpace(block)
	return s, nil
}
