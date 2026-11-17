package guard

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"
)

func hexOf(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

// docFiles builds a tree with one attributed script, its licence text, a
// notice and a manifest that is consistent with them.
func docFiles(t *testing.T, mutate func(*docSources, map[string]string)) *Tree {
	t.Helper()
	const script = "print('x')\n"
	files := map[string]string{
		"internal/documents/assets/verify_x.py": script,
		"third_party/licenses/MIT-x.txt":        "MIT text",
		"NOTICE":                                "verify_x.py MIT\n",
		"docs/provenance.md":                    register("| CR-A | `internal/other/**` | o | clean-room | s | r | planned |"),
	}
	m := docSources{SchemaVersion: docSchema, Entries: []docEntry{{
		Destination:    "internal/documents/assets/verify_x.py",
		Kind:           kindAttributed,
		Source:         docSrc{URL: "https://example.com/x.py", Path: "x.py", Revision: "abc"},
		OriginalSHA256: hexOf("original"),
		ShippedSHA256:  hexOf(script),
		Licence:        docLic{SPDX: "MIT", Texts: []string{"third_party/licenses/MIT-x.txt"}, Evidence: "the repository licence"},
		Copyright:      "Copyright (c) Example",
		Modifications:  []string{"JSON output"},
		Review:         "pending",
	}}}
	if mutate != nil {
		mutate(&m, files)
	}
	b, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	files[docSourcesFile] = string(b)
	return dirTree(t, files)
}

func TestDocumentSources_Consistent(t *testing.T) {
	found, err := checkDocumentSources(docFiles(t, nil))
	if err != nil || len(found) != 0 {
		t.Fatalf("findings = %v, err = %v", found, err)
	}
}

func TestDocumentSources_NoAssetsNoManifest(t *testing.T) {
	found, err := checkDocumentSources(dirTree(t, map[string]string{"README.md": "x"}))
	if err != nil || len(found) != 0 {
		t.Fatalf("findings = %v, err = %v", found, err)
	}
}

func TestDocumentSources_Violations(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*docSources, map[string]string)
		want   string
	}{
		{"shipped hash differs", func(m *docSources, f map[string]string) {
			f["internal/documents/assets/verify_x.py"] = "changed\n"
		}, "shipped_sha256 differs"},
		{"unlisted asset", func(m *docSources, f map[string]string) {
			f["internal/documents/assets/other.tex"] = "x"
		}, "not in third_party/document-sources.json"},
		{"missing licence text", func(m *docSources, f map[string]string) {
			m.Entries[0].Licence.Texts = []string{"third_party/licenses/none.txt"}
		}, "licence text must be a tracked file"},
		{"modification not recorded", func(m *docSources, f map[string]string) {
			m.Entries[0].Modifications = nil
		}, "no modification is recorded"},
		{"not in the notice", func(m *docSources, f map[string]string) {
			f["NOTICE"] = "nothing\n"
		}, "NOTICE does not list"},
		{"wildcard destination", func(m *docSources, f map[string]string) {
			m.Entries[0].Destination = "internal/documents/assets/*.py"
		}, "one exact file"},
		{"duplicate destination", func(m *docSources, f map[string]string) {
			m.Entries = append(m.Entries, m.Entries[0])
		}, "listed twice"},
		{"unknown kind", func(m *docSources, f map[string]string) {
			m.Entries[0].Kind = "vendored"
		}, "kind must be"},
		{"missing source", func(m *docSources, f map[string]string) {
			m.Entries[0].Source.URL = ""
		}, "source url, path, revision"},
		{"unknown licence evidence", func(m *docSources, f map[string]string) {
			m.Entries[0].Licence.Evidence = ""
		}, "licence evidence are required"},
		{"clean-room overlap", func(m *docSources, f map[string]string) {
			f["docs/provenance.md"] = register("| CR-A | `internal/documents/**` | o | clean-room | s | r | planned |")
		}, "overlaps the clean-room glob"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			found, err := checkDocumentSources(docFiles(t, tc.mutate))
			if err != nil {
				t.Fatal(err)
			}
			var all []string
			for _, f := range found {
				all = append(all, f.Detail)
			}
			if !strings.Contains(strings.Join(all, "\n"), tc.want) {
				t.Fatalf("findings %q lack %q", all, tc.want)
			}
		})
	}
}

func TestDocumentSources_ManifestMissingOrMalformed(t *testing.T) {
	found, _ := checkDocumentSources(dirTree(t, map[string]string{"internal/documents/assets/a.tex": "x"}))
	if len(found) != 1 || !strings.Contains(found[0].Detail, "manifest is missing") {
		t.Fatalf("findings = %v", found)
	}
	found, _ = checkDocumentSources(dirTree(t, map[string]string{docSourcesFile: `{"schema_version":1,"surprise":true}`}))
	if len(found) != 1 || !strings.Contains(found[0].Detail, "not valid") {
		t.Fatalf("findings = %v", found)
	}
	found, _ = checkDocumentSources(dirTree(t, map[string]string{docSourcesFile: `{"schema_version":2}`}))
	if len(found) != 1 || !strings.Contains(found[0].Detail, "schema_version") {
		t.Fatalf("findings = %v", found)
	}
}

func TestDocumentSources_OriginalFileNeedsNoSource(t *testing.T) {
	tree := docFiles(t, func(m *docSources, f map[string]string) {
		m.Entries[0] = docEntry{Destination: m.Entries[0].Destination, Kind: kindOriginal, ShippedSHA256: m.Entries[0].ShippedSHA256, Review: "pending"}
	})
	if found, err := checkDocumentSources(tree); err != nil || len(found) != 0 {
		t.Fatalf("findings = %v, err = %v", found, err)
	}
}

// A font stored gzip-compressed is hashed by the bytes inside the archive and
// is listed in NOTICE by the name of the font.
func TestDocumentSources_GzipDestinationIsHashedDecompressed(t *testing.T) {
	const font = "font bytes"
	gz := func(s string) string {
		var buf bytes.Buffer
		zw := gzip.NewWriter(&buf)
		_, _ = zw.Write([]byte(s))
		_ = zw.Close()
		return buf.String()
	}
	tree := func(stored string, notice string) *Tree {
		return docFiles(t, func(m *docSources, f map[string]string) {
			const dest = "internal/documents/assets/fonts/X-Regular.ttf.gz"
			delete(f, "internal/documents/assets/verify_x.py")
			f[dest] = stored
			f["NOTICE"] = notice
			m.Entries[0].Destination = dest
			m.Entries[0].ShippedSHA256 = hexOf(font)
			m.Entries[0].OriginalSHA256 = hexOf(font)
			m.Entries[0].Modifications = nil
		})
	}
	if found, err := checkDocumentSources(tree(gz(font), "X-Regular.ttf MIT\n")); err != nil || len(found) != 0 {
		t.Fatalf("findings = %v, err = %v", found, err)
	}
	for name, tc := range map[string]struct{ stored, notice, want string }{
		"other content": {gz("other"), "X-Regular.ttf\n", "shipped_sha256 differs"},
		"not gzip":      {font, "X-Regular.ttf\n", "not a valid gzip file"},
		"not in NOTICE": {gz(font), "nothing\n", "NOTICE does not list"},
	} {
		found, _ := checkDocumentSources(tree(tc.stored, tc.notice))
		if len(found) == 0 || !strings.Contains(found[0].Detail, tc.want) {
			t.Errorf("%s: findings = %v, want %q", name, found, tc.want)
		}
	}
}
