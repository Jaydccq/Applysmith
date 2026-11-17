// Package intake turns several local career documents into one reviewable
// change set against the profile. It parses with the setup's own CV parser,
// proposes additions, existing matches and conflicts, and builds the profile
// edits for the items the user selected. It writes nothing itself.
package intake

import (
	"fmt"
	"path"
	"sort"
	"strings"
	"unicode/utf8"

	"applysmith/internal/api/wire"
	"applysmith/internal/workspace"
)

const (
	// MaxDocuments caps one batch.
	MaxDocuments = 20
	// MaxDocBytes is the per-file size limit the CV import already uses.
	MaxDocBytes = 10 << 20
)

// factKinds are read by the CV parser; every other kind is a signal.
var factKinds = map[string]bool{
	wire.IntakeKindCV: true, wire.IntakeKindProfileExport: true, wire.IntakeKindEducationRecord: true,
}

var signalKinds = map[string]string{
	wire.IntakeKindProject: "project description", wire.IntakeKindReference: "reference",
	wire.IntakeKindCourse: "course outline", wire.IntakeKindRepository: "repository file",
}

// Kinds lists the document kinds in the order the interfaces show them.
func Kinds() []string {
	return []string{
		wire.IntakeKindCV, wire.IntakeKindProfileExport, wire.IntakeKindEducationRecord,
		wire.IntakeKindProject, wire.IntakeKindReference, wire.IntakeKindCourse, wire.IntakeKindRepository,
	}
}

// IsSignal reports a kind that can only suggest questions.
func IsSignal(kind string) bool { _, ok := signalKinds[kind]; return ok }

// Rejection is input refused per field.
type Rejection map[string]string

func (r Rejection) Error() string {
	keys := make([]string, 0, len(r))
	for k := range r {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, len(keys))
	for i, k := range keys {
		parts[i] = k + ": " + r[k]
	}
	return strings.Join(parts, "; ")
}

func (r Rejection) add(field, msg string) {
	if _, ok := r[field]; !ok {
		r[field] = msg
	}
}

// Doc is a validated document with its content hash.
type Doc struct {
	wire.IntakeDocument
	Hash      string
	Duplicate bool
}

// Label is the name shown beside every fact from the document.
func (d Doc) label() string {
	if l := strings.TrimSpace(d.Label); l != "" {
		return l
	}
	return strings.TrimSpace(d.Name)
}

// Prepare checks the batch and hashes each document. Only the file types the
// CV import reads are accepted; a later document with the content hash of an
// earlier one is marked Duplicate and adds nothing.
func Prepare(in []wire.IntakeDocument) ([]Doc, error) {
	rej := Rejection{}
	switch {
	case len(in) == 0:
		rej.add("documents", "select at least one document")
	case len(in) > MaxDocuments:
		rej.add("documents", fmt.Sprintf("a batch holds at most %d documents, got %d", MaxDocuments, len(in)))
	}
	if len(rej) > 0 {
		return nil, rej
	}
	out := make([]Doc, 0, len(in))
	seen := map[string]bool{}
	for i, d := range in {
		field := func(f string) string { return fmt.Sprintf("documents[%d].%s", i, f) }
		if strings.TrimSpace(d.Name) == "" {
			rej.add(field("name"), "is empty")
		}
		if !factKinds[d.Kind] && !IsSignal(d.Kind) {
			rej.add(field("kind"), "must be one of "+strings.Join(Kinds(), ", "))
		}
		switch ext := strings.ToLower(path.Ext(strings.ReplaceAll(d.Name, "\\", "/"))); ext {
		case "", ".md", ".markdown", ".txt":
		default:
			rej.add(field("name"), "only .md and .txt documents can be read here; paste the text of a PDF or DOCX document instead")
		}
		switch {
		case strings.TrimSpace(d.Text) == "":
			rej.add(field("text"), "is empty")
		case !utf8.ValidString(d.Text):
			rej.add(field("text"), "is not valid UTF-8")
		case len(d.Text) > MaxDocBytes:
			rej.add(field("text"), "is larger than 10 MiB")
		}
		doc := Doc{IntakeDocument: d, Hash: workspace.Revision([]byte(d.Text))}
		doc.Duplicate = seen[doc.Hash]
		seen[doc.Hash] = true
		out = append(out, doc)
	}
	if len(rej) > 0 {
		return nil, rej
	}
	return out, nil
}

// Infos describes how each document was read.
func Infos(docs []Doc) []wire.IntakeDocInfo {
	out := make([]wire.IntakeDocInfo, 0, len(docs))
	for _, d := range docs {
		out = append(out, wire.IntakeDocInfo{Name: d.Name, Label: d.label(), Kind: d.Kind, Hash: d.Hash, Signal: IsSignal(d.Kind), Duplicate: d.Duplicate})
	}
	return out
}
