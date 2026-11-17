package guard

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"path"
	"regexp"
	"strings"
)

const (
	ruleDocSources = "document-sources"

	docSourcesFile = "third_party/document-sources.json"
	docAssetsDir   = "internal/documents/assets"
	docSchema      = 1

	// gzSuffix marks a file stored gzip-compressed; the manifest hashes and
	// NOTICE name the real file inside it.
	gzSuffix = ".gz"
)

// Kinds of manifest entry. An original file is owned by the product and lists
// no source; the other two are exact-file reuse of someone else's work.
const (
	kindAttributed = "attributed-source"
	kindAsset      = "asset-shipped"
	kindOriginal   = "original"
)

var sha256RE = regexp.MustCompile(`^[0-9a-f]{64}$`)

type docSources struct {
	SchemaVersion int        `json:"schema_version"`
	Entries       []docEntry `json:"entries"`
}

type docEntry struct {
	Destination    string   `json:"destination"`
	Kind           string   `json:"kind"`
	Source         docSrc   `json:"source"`
	Ancestry       []string `json:"ancestry,omitempty"`
	OriginalSHA256 string   `json:"original_sha256"`
	ShippedSHA256  string   `json:"shipped_sha256"`
	Licence        docLic   `json:"licence"`
	Copyright      string   `json:"copyright"`
	Version        string   `json:"version,omitempty"`
	Modifications  []string `json:"modifications"`
	Review         string   `json:"review"`
}

type docSrc struct {
	URL      string `json:"url"`
	Path     string `json:"path"`
	Revision string `json:"revision"`
	// CrossCheck names a second distribution whose bytes were compared.
	CrossCheck string `json:"cross_check,omitempty"`
}

type docLic struct {
	SPDX string `json:"spdx"`
	// Texts are the retained licence files; a file with layered ancestry has
	// one per layer.
	Texts    []string `json:"texts"`
	Evidence string   `json:"evidence"`
}

// checkDocumentSources holds every file of the document assets directory to
// the exact-file manifest: listed once, hash-locked, with a licence text, a
// notice and no overlap with a clean-room area.
func checkDocumentSources(t *Tree) ([]Finding, error) {
	var assets []string
	for _, p := range t.Paths {
		if underDir(p, docAssetsDir) && ext(p) != ".go" {
			assets = append(assets, p)
		}
	}
	if !t.Has(docSourcesFile) {
		if len(assets) == 0 {
			return nil, nil
		}
		return []Finding{{Rule: ruleDocSources, Path: docSourcesFile, Detail: "document assets exist but the manifest is missing"}}, nil
	}
	data, err := t.Read(docSourcesFile)
	if err != nil {
		return nil, err
	}
	var m docSources
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	bad := func(detail string) Finding {
		return Finding{Rule: ruleDocSources, Path: docSourcesFile, Detail: detail}
	}
	if err := dec.Decode(&m); err != nil {
		return []Finding{bad("the manifest is not valid: " + err.Error())}, nil
	}
	if m.SchemaVersion != docSchema {
		return []Finding{bad(fmt.Sprintf("schema_version must be %d", docSchema))}, nil
	}

	notice := ""
	if t.Has(noticeFile) {
		b, err := t.Read(noticeFile)
		if err != nil {
			return nil, err
		}
		notice = string(b)
	}
	cleanRoom, err := cleanRoomGlobs(t)
	if err != nil {
		return nil, err
	}

	var out []Finding
	listed := map[string]bool{}
	for _, e := range m.Entries {
		add := func(detail string) {
			out = append(out, Finding{Rule: ruleDocSources, Path: e.Destination, Detail: detail})
		}
		switch {
		case !underDir(e.Destination, docAssetsDir) || strings.Contains(e.Destination, "*"):
			add("destination must be one exact file below " + docAssetsDir)
			continue
		case listed[e.Destination]:
			add("destination is listed twice")
			continue
		case !t.Has(e.Destination):
			add("destination is not a tracked file")
			continue
		}
		listed[e.Destination] = true
		out = append(out, checkDocEntry(t, e, notice, cleanRoom)...)
	}
	for _, p := range assets {
		if !listed[p] {
			out = append(out, Finding{Rule: ruleDocSources, Path: p, Detail: "file in the document assets is not in " + docSourcesFile})
		}
	}
	return out, nil
}

func checkDocEntry(t *Tree, e docEntry, notice string, cleanRoom []string) []Finding {
	var out []Finding
	add := func(detail string) {
		out = append(out, Finding{Rule: ruleDocSources, Path: e.Destination, Detail: detail})
	}

	src, err := t.Read(e.Destination)
	if err != nil {
		add("destination cannot be read")
		return out
	}
	if strings.HasSuffix(e.Destination, gzSuffix) {
		zr, err := gzip.NewReader(bytes.NewReader(src))
		if err == nil {
			src, err = io.ReadAll(zr)
		}
		if err != nil {
			add("destination is not a valid gzip file: " + err.Error())
			return out
		}
	}
	sum := sha256.Sum256(src)
	if hex.EncodeToString(sum[:]) != e.ShippedSHA256 {
		add("shipped_sha256 differs from the file; review the change and update the manifest")
	}
	if !sha256RE.MatchString(e.ShippedSHA256) || e.Review == "" {
		add("shipped_sha256 must be 64 lowercase hex digits and review must be set")
	}
	for _, g := range cleanRoom {
		if globToRegexp(g).MatchString(e.Destination) {
			add("destination overlaps the clean-room glob " + g)
		}
	}
	if e.Kind == kindOriginal {
		return out
	}

	switch e.Kind {
	case kindAttributed, kindAsset:
	default:
		add("kind must be " + kindAttributed + ", " + kindAsset + " or " + kindOriginal)
		return out
	}
	if e.Source.URL == "" || e.Source.Path == "" || e.Source.Revision == "" || !sha256RE.MatchString(e.OriginalSHA256) {
		add("source url, path, revision and original_sha256 are required")
	}
	if e.Copyright == "" || e.Licence.SPDX == "" || e.Licence.Evidence == "" {
		add("copyright, licence identity and licence evidence are required")
	}
	if len(e.Licence.Texts) == 0 {
		add("at least one licence text is required")
	}
	for _, txt := range e.Licence.Texts {
		if !t.Has(txt) || !strings.HasPrefix(txt, licenseDir) {
			add("licence text must be a tracked file under " + licenseDir)
		}
	}
	if e.OriginalSHA256 != e.ShippedSHA256 && len(e.Modifications) == 0 {
		add("the shipped file differs from the original but no modification is recorded")
	}
	if !strings.Contains(notice, path.Base(strings.TrimSuffix(e.Destination, gzSuffix))) {
		add("NOTICE does not list the file by name")
	}
	return out
}

// cleanRoomGlobs returns the globs of every clean-room row of the register.
func cleanRoomGlobs(t *Tree) ([]string, error) {
	if !t.Has(provenanceFile) {
		return nil, nil
	}
	data, err := t.Read(provenanceFile)
	if err != nil {
		return nil, err
	}
	rows, _ := parseProvenance(string(data))
	var out []string
	for _, r := range rows {
		if r.Treatment == cleanRoomTreatment {
			out = append(out, r.Globs...)
		}
	}
	return out, nil
}
