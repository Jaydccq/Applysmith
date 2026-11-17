package documents

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"applysmith/internal/documents/assets"
	"applysmith/internal/documents/toolchain"
	"applysmith/internal/llm"
	"applysmith/internal/store"
	"applysmith/internal/tracker"
	"applysmith/internal/vault"
)

// TestResumePDFFlagFollowsAStoredPDF publishes a version with a compiled PDF
// in each state without running a compiler: the flag follows what is stored,
// not whether the PDF is attachable.
func TestResumePDFFlagFollowsAStoredPDF(t *testing.T) {
	for _, c := range []struct {
		name  string
		state string
		pdf   []byte
		flag  bool
		ready bool
	}{
		{"ready", PDFReady, []byte("%PDF-1.7 synthetic"), true, true},
		{"failed but stored", PDFFailed, []byte("%PDF-1.7 synthetic"), true, false},
		{"unverified but stored", PDFUnverified, []byte("%PDF-1.7 synthetic"), true, false},
		{"unavailable, nothing stored", PDFUnavailable, nil, false, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := newFixture(t, fixtureOpts{})
			draft := f.run(Request{Action: ActionResume, Mode: ModeTailored, CandidacyID: f.cand, Posting: testPosting})
			d := f.svc.mustGet(t, draft.Documents[0])
			var m Manifest
			must(t, json.Unmarshal([]byte(f.read(d.Path+"/evidence-manifest.json")), &m))
			rd := deterministicResume(m, "en")
			req := Request{Action: ActionResume, Mode: ModeTailored, CandidacyID: f.cand, PDF: true, Actor: tracker.ActorUser}
			j := &job{s: f.svc, runID: draft.RunID, req: req, partial: map[string][]byte{}, gen: GenDeterministic}
			v := &version{m: m, req: req, lang: "en", paper: assets.PaperA4, pages: 2, resume: &rd, resumeGate: checkResume(m, rd), review: &ReviewRecord{Schema: reviewRecordSchema, Status: ContentUnavailable}}
			builds := map[Kind]*built{KindResume: {state: c.state, out: toolchain.BuildOutput{PDF: c.pdf, PDFSHA256: sha(c.pdf), Pages: 2}}}
			now := f.clk.now()
			files, err := v.files(j, []byte("tex"), nil, builds, now)
			must(t, err)
			ids, err := j.publish(t.Context(), v, files, builds, now)
			must(t, err)
			got := f.svc.mustGet(t, ids[0])
			if got.PDFState != c.state || got.Ready != c.ready {
				t.Errorf("document = state %s ready %v", got.PDFState, got.Ready)
			}
			if c.ready {
				must(t, os.WriteFile(filepath.Join(f.ws.Root, filepath.FromSlash(got.PDFPath)), []byte("%PDF-1.7 replaced"), 0o600))
				if again := f.svc.mustGet(t, ids[0]); again.Ready || again.PDFState != PDFReady {
					t.Errorf("a replaced PDF is still ready: %+v", again)
				}
				list, err := f.svc.List(t.Context(), f.cand)
				must(t, err)
				for _, d := range list {
					if d.ID == ids[0] && d.Ready {
						t.Errorf("List reports a replaced PDF as ready: %+v", d)
					}
				}
			}
			e, _ := f.tr.Get(t.Context(), f.cand)
			if e.Entry.ResumePDF != c.flag {
				t.Errorf("resume_pdf = %v, want %v", e.Entry.ResumePDF, c.flag)
			}
		})
	}
}

func TestManifestRecordsTheTemplateVersionAndHashes(t *testing.T) {
	f := newFixture(t, fixtureOpts{})
	r := f.run(Request{Action: ActionPair, Mode: ModeTailored, CandidacyID: f.cand, Posting: testPosting})
	d := f.svc.mustGet(t, r.Documents[0])
	var man struct {
		Templates struct {
			Version string
			Hashes  map[string]string
		}
	}
	must(t, json.Unmarshal([]byte(f.read(d.Path+"/document-manifest.json")), &man))
	if man.Templates.Version != assets.TemplateVersion {
		t.Errorf("template version = %q, want %q", man.Templates.Version, assets.TemplateVersion)
	}
	for _, n := range []string{"resume.tex", "cover.tex", "cover.cls"} {
		b, _ := assets.Read(n)
		if man.Templates.Hashes[n] != sha(b) {
			t.Errorf("hash of %s = %q", n, man.Templates.Hashes[n])
		}
	}
}

// The version in the manifest names the upstream revision that the
// exact-file provenance manifest records for the templates.
func TestTemplateVersionMatchesTheProvenanceManifest(t *testing.T) {
	raw, err := os.ReadFile("../../third_party/document-sources.json")
	must(t, err)
	var src struct {
		Entries []struct {
			Destination string
			Source      struct{ Revision string }
		}
	}
	must(t, json.Unmarshal(raw, &src))
	seen := 0
	for _, e := range src.Entries {
		switch strings.TrimPrefix(e.Destination, "internal/documents/assets/") {
		case "resume.tex", "cover.tex", "cover.cls":
			seen++
			if assets.TemplateVersion != "ai-job-search@"+e.Source.Revision {
				t.Errorf("%s: provenance revision %s, TemplateVersion %s", e.Destination, e.Source.Revision, assets.TemplateVersion)
			}
		}
	}
	if seen != 3 {
		t.Errorf("the provenance manifest lists %d of the 3 templates", seen)
	}
}

// A router with no provider configured fails the call with a no-route error;
// the run must take the same documented no-provider path as a service that was
// given no model.
func TestRouterWithoutProviderTakesTheNoProviderPath(t *testing.T) {
	router := func(st *store.Store) Model {
		v, err := vault.New("0123456789abcdef0123456789abcdef", vault.NewMemory(), vault.WithGetenv(func(string) string { return "" }))
		must(t, err)
		return llm.New(st, v, llm.WithGetenv(func(string) string { return "" }))
	}
	req := Request{Action: ActionPair, Mode: ModeTailored, CandidacyID: 1, Posting: testPosting}
	viaRouter := newFixture(t, fixtureOpts{router: router})
	got := viaRouter.run(req)
	bare := newFixture(t, fixtureOpts{})
	want := bare.run(req)
	for name, r := range map[string]Result{"router": got, "no model": want} {
		if !r.Published || len(r.Documents) != 2 || r.Generation != GenDeterministic {
			t.Fatalf("%s: result = %+v", name, r)
		}
	}
	if strings.Join(got.Warnings, "|") != strings.Join(want.Warnings, "|") {
		t.Errorf("warnings differ:\n router: %v\n none:   %v", got.Warnings, want.Warnings)
	}
	if w := strings.Join(got.Warnings, "|"); !strings.Contains(w, "no model route is configured; the deterministic builder wrote the content") ||
		!strings.Contains(w, "the deterministic letter was written") {
		t.Errorf("warnings = %v; want the no-provider disclosure for both documents", got.Warnings)
	}
	docs, _ := viaRouter.svc.List(t.Context(), viaRouter.cand)
	review := viaRouter.read(docs[0].Path + "/document-review.json")
	if !strings.Contains(review, "no usable model route") {
		t.Errorf("document-review.json = %s", review)
	}
}
