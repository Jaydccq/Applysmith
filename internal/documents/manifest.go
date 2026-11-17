package documents

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// ManifestVersion is the schema of the frozen manifest.
const ManifestVersion = 1

// Evidence is one source line a document may rest on: a bullet of the CV, a
// retrieved snippet line, a profile fact. IDs are positional (E1, E2, ...) and
// belong to one frozen manifest.
type Evidence struct {
	ID     string `json:"id"`
	Source string `json:"source"` // file#heading
	Kind   string `json:"kind"`   // cv, profile or snippet
	Entry  string `json:"entry,omitempty"`
	Text   string `json:"text"`
}

// Entry kinds.
const (
	EntryExperience = "experience"
	EntryProject    = "project"
	EntryEducation  = "education"
)

// Entry is a dated experience or education line, or a project. Titles,
// organizations, places and dates come from the profile or the CV and are
// never written by a model.
type Entry struct {
	Key          string   `json:"key"`
	Kind         string   `json:"kind"`
	Title        string   `json:"title"`
	Organization string   `json:"organization,omitempty"`
	Location     string   `json:"location,omitempty"`
	Start        string   `json:"start,omitempty"`
	End          string   `json:"end,omitempty"` // empty or "present" for current
	Note         string   `json:"note,omitempty"`
	Evidence     []string `json:"evidence,omitempty"`
}

// Requirement is one ranked requirement of the posting.
type Requirement struct {
	ID       string   `json:"id"`
	Text     string   `json:"text"`
	Keywords []string `json:"keywords"`
}

// Coverage maps a requirement to the selected evidence that supports it.
type Coverage struct {
	Requirement string   `json:"requirement"`
	Evidence    []string `json:"evidence"`
}

// Exclusion records an entry left out of the selection and why.
type Exclusion struct {
	Key    string `json:"key"`
	Reason string `json:"reason"`
}

// CompanyFact is verified company context from a brief, with its source.
type CompanyFact struct {
	ID     string `json:"id"`
	Text   string `json:"text"`
	Source string `json:"source"`
}

// Link is a labelled contact address.
type Link struct {
	Label string `json:"label"`
	URL   string `json:"url"`
}

// Contact is the contact block, copied from the profile. Documents render it
// from here and nowhere else.
type Contact struct {
	FullName   string `json:"fullName"`
	Email      string `json:"email"`
	Phone      string `json:"phone,omitempty"`
	Location   string `json:"location,omitempty"`
	Relocation bool   `json:"relocation,omitempty"`
	Links      []Link `json:"links,omitempty"`
}

// Inputs are the revisions the manifest was built from.
type Inputs struct {
	CVSHA256      string `json:"cvSha256"`
	ProfileSHA256 string `json:"profileSha256"`
	PostingSHA256 string `json:"postingSha256,omitempty"`
	ReportNumber  int64  `json:"reportNumber,omitempty"`
}

// Manifest is the binding selection every later stage works from. Drafting,
// the grounding gate and the renderer may use only the entries and evidence
// it lists. Hash covers every other field, so a changed manifest is a
// different manifest.
type Manifest struct {
	Version int    `json:"version"`
	Mode    Mode   `json:"mode"`
	Limited bool   `json:"limitedContext"`
	Company string `json:"company,omitempty"`
	Role    string `json:"role,omitempty"`
	// TargetRoles are the titles the user lists as roles they seek. They may
	// name the user in a summary; they never ground a technology or a figure.
	TargetRoles []string `json:"targetRoles,omitempty"`
	Inputs      Inputs   `json:"inputs"`
	Contact     Contact  `json:"contact"`
	// CVLinks are the profile links (a code-hosting or professional-network
	// address) that cv.md states, so a link the contact block lacks is seen.
	CVLinks          []string      `json:"cvLinks,omitempty"`
	Requirements     []Requirement `json:"requirements,omitempty"`
	Keywords         []string      `json:"keywords,omitempty"`
	NiceToHave       []string      `json:"niceToHave,omitempty"`
	Responsibilities []string      `json:"responsibilities,omitempty"` // the posting's duty lines
	Evidence         []Evidence    `json:"evidence"`
	Entries          []Entry       `json:"entries"`
	SelectedEntries  []string      `json:"selectedEntries"`
	SelectedProjects []string      `json:"selectedProjects"`
	Selected         []string      `json:"selectedEvidence"`
	Excluded         []Exclusion   `json:"excluded,omitempty"`
	Coverage         []Coverage    `json:"coverage,omitempty"`
	Gaps             []string      `json:"gaps,omitempty"`
	CompanyFacts     []CompanyFact `json:"companyFacts,omitempty"`
	Recipient        string        `json:"recipient,omitempty"`
	Retrieval        Retrieval     `json:"retrieval"`
	Warnings         []string      `json:"warnings,omitempty"`
	Hash             string        `json:"hash"`
}

// Freeze computes the hash over everything else. It is the last step of
// building a manifest.
func (m *Manifest) Freeze() error {
	m.Hash = ""
	b, err := json.Marshal(m)
	if err != nil {
		return err
	}
	sum := sha256.Sum256(b)
	m.Hash = hex.EncodeToString(sum[:])
	return nil
}

// Verify reports whether the manifest still has the hash it was frozen with.
func (m Manifest) Verify() error {
	want := m.Hash
	if err := m.Freeze(); err != nil {
		return err
	}
	if m.Hash != want {
		return fmt.Errorf("documents: the evidence manifest changed after it was frozen")
	}
	return nil
}

func (m Manifest) evidence(id string) (Evidence, bool) {
	for _, e := range m.Evidence {
		if e.ID == id {
			return e, true
		}
	}
	return Evidence{}, false
}

func (m Manifest) entry(key string) (Entry, bool) {
	for _, e := range m.Entries {
		if e.Key == key {
			return e, true
		}
	}
	return Entry{}, false
}

func (m Manifest) isSelected(id string) bool {
	for _, s := range m.Selected {
		if s == id {
			return true
		}
	}
	return false
}

func (m Manifest) entrySelected(key string) bool {
	for _, k := range m.SelectedEntries {
		if k == key {
			return true
		}
	}
	for _, k := range m.SelectedProjects {
		if k == key {
			return true
		}
	}
	return false
}

// corpus is the text of the selected evidence a quantified claim must come
// from: that of one entry plus the entry-less lines, or all of it.
func (m Manifest) corpus(entry string, whole bool) []string {
	var out []string
	for _, e := range m.Evidence {
		if !m.isSelected(e.ID) {
			continue
		}
		if whole || entry == "" || e.Entry == entry || e.Entry == "" {
			out = append(out, e.Text)
		}
	}
	return out
}

// Budget bounds a résumé by its page limit. The compaction rounds of a later
// task refine it from the compiled page count; this is the first cut.
type Budget struct {
	Experience, Bullets, Projects int
}

func budgetFor(pages int) Budget {
	if pages == 1 {
		return Budget{Experience: 3, Bullets: 3, Projects: 1}
	}
	return Budget{Experience: 5, Bullets: 4, Projects: 3}
}

func sortedUnique(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range in {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	sort.Strings(out)
	return out
}

func lowerContains(hay, needle string) bool {
	return needle != "" && strings.Contains(strings.ToLower(hay), strings.ToLower(needle))
}
