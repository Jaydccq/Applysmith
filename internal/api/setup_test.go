package api

import (
	"archive/zip"
	"bytes"
	"context"
	"net/http"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"applysmith/internal/api/wire"
	"applysmith/internal/demo"
	"applysmith/internal/documents/toolchain"
	"applysmith/internal/workspace"
)

// fakeSetup answers the setup routes from a script so the route layer can be
// tested without the onboarding catalog.
type fakeSetup struct {
	applyErr error
	wrote    bool
	hosts    []SetupHost
	mu       sync.Mutex
	states   []SetupHost // the State calls, the start's warm-up among them
}

func (f *fakeSetup) State(_ context.Context, h SetupHost) (wire.SetupState, error) {
	f.mu.Lock()
	f.states = append(f.states, h)
	f.mu.Unlock()
	return wire.SetupState{Progress: 50, Next: "cv", Steps: []wire.SetupStep{{ID: "cv", Status: "partial"}}}, nil
}

func (f *fakeSetup) Apply(_ context.Context, _ SetupHost, id string, input map[string]any, dry bool) (wire.SetupApplied, error) {
	if f.applyErr != nil {
		return wire.SetupApplied{}, f.applyErr
	}
	return wire.SetupApplied{Step: wire.SetupStep{ID: id}, DryRun: dry, Wrote: f.wrote && !dry}, nil
}

func (f *fakeSetup) Decide(_ context.Context, _ SetupHost, id, kind string) (wire.SetupStep, error) {
	return wire.SetupStep{ID: id, Status: kind}, nil
}

func (f *fakeSetup) Doctor(context.Context, SetupHost) (wire.DoctorReport, error) {
	return wire.DoctorReport{Platform: "p", Results: []wire.DoctorResult{{ID: "a.b", Status: "pass"}}}, nil
}

func (f *fakeSetup) IntakePreview(_ context.Context, _ SetupHost, req wire.IntakeRequest) (wire.IntakePreview, error) {
	if f.applyErr != nil {
		return wire.IntakePreview{}, f.applyErr
	}
	return wire.IntakePreview{Revision: "rev-1", Documents: []wire.IntakeDocInfo{{Name: req.Documents[0].Name}}, Items: []wire.IntakeItem{}, Notes: []string{}}, nil
}

func (f *fakeSetup) IntakeAccept(_ context.Context, _ SetupHost, req wire.IntakeAcceptRequest) (wire.IntakeResult, error) {
	if f.applyErr != nil {
		return wire.IntakeResult{}, f.applyErr
	}
	return wire.IntakeResult{Wrote: f.wrote, Revision: req.Revision, Applied: []wire.IntakeItem{}, Notes: []string{}}, nil
}

func (f *fakeSetup) Demo(_ context.Context, h SetupHost) (wire.DemoCreated, error) {
	f.hosts = append(f.hosts, h)
	return wire.DemoCreated{Workspace: "/demo", Created: []string{}, Postings: 3, Notes: []string{}, Start: "applysmith serve --workspace /demo"}, nil
}

func setupHarness(t *testing.T, f *fakeSetup) *harness {
	return newDomainHarness(t, func(c *Config) { c.Setup = f })
}

// warmed waits for the State call the server makes while it starts.
func (f *fakeSetup) warmed(t *testing.T) []SetupHost {
	t.Helper()
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(5 * time.Millisecond) {
		f.mu.Lock()
		n := append([]SetupHost(nil), f.states...)
		f.mu.Unlock()
		if len(n) > 0 {
			return n
		}
	}
	t.Fatal("the server did not compute the setup state while it started")
	return nil
}

func TestSetupRoutes_StateCarriesTheServersOwnWorkspaceAndStore(t *testing.T) {
	f := &fakeSetup{}
	h := setupHarness(t, f)
	f.warmed(t)
	r := h.get("/api/setup/state")
	var env wire.Success[wire.SetupState]
	strictInto(t, r.body, &env)
	got := f.warmed(t)
	if r.StatusCode != 200 || env.Data.Next != "cv" || len(got) != 2 || got[1].Workspace == nil || got[1].Store == nil || got[1].Vault == nil || got[1].Router == nil {
		t.Fatalf("%d %s %+v", r.StatusCode, r.body, got)
	}
}

func TestSetupRoutes_RejectedFieldsReachTheClientPerField(t *testing.T) {
	f := &fakeSetup{applyErr: RejectFields(map[string]string{"email": "is not an address", "city": "is empty"})}
	h := setupHarness(t, f)
	r := h.post("/api/setup/steps/identity/apply", wire.SetupApplyRequest{Input: map[string]any{"email": "x"}})
	var env wire.Failure
	strictInto(t, r.body, &env)
	d, _ := env.Error.Details.(map[string]any)
	fields, _ := d["fields"].(map[string]any)
	if r.StatusCode != 422 || env.Error.Code != wire.CodeValidationFailed || fields["email"] != "is not an address" || fields["city"] != "is empty" {
		t.Fatalf("%d %s", r.StatusCode, r.body)
	}
}

func TestSetupRoutes_AStepOnlyAPersonCanFinishIsAConflictWithItsInstruction(t *testing.T) {
	f := &fakeSetup{applyErr: ErrNeedsPerson("Open BotFather and send /newbot.")}
	h := setupHarness(t, f)
	r := h.post("/api/setup/steps/telegram/apply", wire.SetupApplyRequest{Input: map[string]any{}})
	var env wire.Failure
	strictInto(t, r.body, &env)
	d, _ := env.Error.Details.(map[string]any)
	if r.StatusCode != 409 || d["reason"] != "needs_person" || !strings.Contains(env.Error.Message, "/newbot") {
		t.Fatalf("%d %s", r.StatusCode, r.body)
	}
}

func TestSetupRoutes_DecisionsAndDoctorAnswer(t *testing.T) {
	h := setupHarness(t, &fakeSetup{})
	step := h.post("/api/setup/steps/gmail/skip", nil)
	var se wire.Success[wire.SetupStep]
	strictInto(t, step.body, &se)
	if step.StatusCode != 200 || se.Data.Status != "skip" {
		t.Errorf("skip: %d %s", step.StatusCode, step.body)
	}
	r := h.post("/api/setup/doctor", nil)
	var env wire.Success[wire.DoctorReport]
	strictInto(t, r.body, &env)
	if r.StatusCode != 200 || len(env.Data.Results) != 1 {
		t.Fatalf("%d %s", r.StatusCode, r.body)
	}
}

func TestSetupRoutes_IntakePreviewAndAcceptAnswerAndAWriteMovesTheTopic(t *testing.T) {
	f := &fakeSetup{}
	h := setupHarness(t, f)
	docs := []wire.IntakeDocument{{Name: "cv.md", Kind: wire.IntakeKindCV, Text: "x"}}
	r := h.post("/api/setup/intake/preview", wire.IntakeRequest{Documents: docs})
	var pv wire.Success[wire.IntakePreview]
	strictInto(t, r.body, &pv)
	if r.StatusCode != 200 || pv.Data.Revision != "rev-1" {
		t.Fatalf("%d %s", r.StatusCode, r.body)
	}
	before := h.topicRev("setup")
	h.post("/api/setup/intake/accept", wire.IntakeAcceptRequest{Revision: "rev-1", Documents: docs})
	if got := h.topicRev("setup"); got != before {
		t.Fatalf("an acceptance that wrote nothing moved the topic: %d -> %d", before, got)
	}
	f.wrote = true
	r = h.post("/api/setup/intake/accept", wire.IntakeAcceptRequest{Revision: "rev-1", Documents: docs})
	var res wire.Success[wire.IntakeResult]
	strictInto(t, r.body, &res)
	if r.StatusCode != 200 || !res.Data.Wrote || h.topicRev("setup") != before+1 {
		t.Fatalf("%d %s", r.StatusCode, r.body)
	}
}

func TestSetupRoutes_AStaleIntakeRevisionIsARevisionConflict(t *testing.T) {
	h := setupHarness(t, &fakeSetup{applyErr: ErrSetupStale("profile.yml changed since the preview was built; preview the documents again")})
	r := h.post("/api/setup/intake/accept", wire.IntakeAcceptRequest{Revision: "old", Documents: []wire.IntakeDocument{{Name: "a.md", Kind: wire.IntakeKindCV, Text: "x"}}})
	var env wire.Failure
	strictInto(t, r.body, &env)
	if r.StatusCode != http.StatusConflict || env.Error.Code != wire.CodeRevisionConflict {
		t.Fatalf("%d %s", r.StatusCode, r.body)
	}
}

func TestSetupRoutes_WithoutAServiceTheyAreUnavailable(t *testing.T) {
	h := newDomainHarness(t)
	if r := h.get("/api/setup/state"); r.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("%d %s", r.StatusCode, r.body)
	}
}

func TestDocumentToolchainRoute_NamesMissingTools(t *testing.T) {
	empty := t.TempDir()
	h := newDomainHarness(t, func(c *Config) {
		c.Domain.DocumentToolchainConfig = &toolchain.Config{Root: toolchain.Root(t.TempDir()), SearchDirs: []string{empty}}
	})
	r := h.get("/api/documents/toolchain")
	var env wire.Success[wire.DocumentToolchain]
	strictInto(t, r.body, &env)
	tc := env.Data
	if r.StatusCode != 200 || tc.State != "unavailable" || tc.Code != toolchain.CodeUnavailable || len(tc.Missing) == 0 || tc.Fix == "" {
		t.Fatalf("%d %s", r.StatusCode, r.body)
	}
	// No path or command comes from the request.
	if entries, _ := os.ReadDir(empty); len(entries) != 0 {
		t.Errorf("the probe wrote into the search directory")
	}
}

// The render test compiles only with every component present; the passing
// path runs the installed TeX (TestToolchain_RealSetupAndSmoke).
func TestDocumentToolchainSmokeRoute_RefusesWhileAToolIsMissingAndWritesNoRecord(t *testing.T) {
	root := t.TempDir()
	h := newDomainHarness(t, func(c *Config) {
		c.Domain.DocumentToolchainConfig = &toolchain.Config{Root: toolchain.Root(root), SearchDirs: []string{t.TempDir()}}
	})
	r := h.post("/api/documents/toolchain/smoke", nil)
	if r.StatusCode != http.StatusServiceUnavailable || r.errReason() != wire.ReasonToolchainUnavailable || !strings.Contains(string(r.body), "lualatex") {
		t.Fatalf("%d %s", r.StatusCode, r.body)
	}
	if entries, _ := os.ReadDir(root); len(entries) != 0 {
		t.Errorf("a refused render test wrote under the toolchain root: %v", entries)
	}
}

func TestSetupCVExtract_DOCXIsPreviewUntilConfirmed(t *testing.T) {
	h := newDomainHarness(t)
	before, _ := os.ReadFile(h.ws.Path(workspace.FileCV))
	profile, _ := os.ReadFile(h.ws.Path(workspace.FileProfile))
	var b bytes.Buffer
	z := zip.NewWriter(&b)
	rels, _ := z.Create("_rels/.rels")
	rels.Write([]byte(`<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/officeDocument" Target="word/document.xml" Id="rId1"/></Relationships>`))
	f, _ := z.Create("word/document.xml")
	f.Write([]byte(`<w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"><w:body><w:p><w:r><w:t>Riley Sample</w:t></w:r></w:p><w:p><w:r><w:t>Platform Engineer</w:t></w:r></w:p><w:p><w:r><w:t>` + strings.Repeat("Built internal tooling and maintained platform services. ", 5) + `</w:t></w:r></w:p></w:body></w:document>`))
	z.Close()
	r := h.do(call{method: "POST", path: "/api/setup/cv/extract?name=cv.docx", useToken: true, contentType: "application/octet-stream", body: b.Bytes()})
	if r.StatusCode != 200 {
		t.Fatalf("extract=%d %s", r.StatusCode, r.body)
	}
	text, _ := r.data()["markdown"].(string)
	if !strings.Contains(text, "Riley Sample") || !strings.Contains(text, "Platform Engineer") {
		t.Fatalf("missing extracted text=%q", text)
	}
	after, _ := os.ReadFile(h.ws.Path(workspace.FileCV))
	pafter, _ := os.ReadFile(h.ws.Path(workspace.FileProfile))
	if !bytes.Equal(before, after) || !bytes.Equal(profile, pafter) {
		t.Fatal("extraction wrote CV/profile before confirmation")
	}
	rev := h.get("/api/config/cv").data()["revision"]
	saved := h.put("/api/config/cv", map[string]any{"content": text, "expectedRevision": rev})
	if saved.StatusCode != 200 {
		t.Fatalf("confirm=%d %s", saved.StatusCode, saved.body)
	}
	after, _ = os.ReadFile(h.ws.Path(workspace.FileCV))
	if string(after) != text {
		t.Fatal("confirmed text missing")
	}
}

func TestSetupCVExtract_RefusalsDoNotChangeWorkspace(t *testing.T) {
	h := newDomainHarness(t)
	before, _ := os.ReadFile(h.ws.Path(workspace.FileCV))
	pdf := demo.ResumePDF()
	extract := func(name string, data []byte) result {
		return h.do(call{method: "POST", path: "/api/setup/cv/extract?name=" + name, useToken: true, contentType: "application/octet-stream", headers: map[string]string{"Expect": "100-continue"}, body: data})
	}
	for _, tc := range []struct {
		name   string
		data   []byte
		status int
	}{
		{"cv.pdf", []byte("not PDF"), 422},
		{"cv.docx", make([]byte, (10<<20)+1), 413},
	} {
		if r := extract(tc.name, tc.data); r.StatusCode != tc.status {
			t.Fatalf("%s=%d want%d %s", tc.name, r.StatusCode, tc.status, r.body)
		}
	}
	t.Setenv(toolchain.EnvToolPath, t.TempDir())
	if r := extract("cv.pdf", pdf); r.StatusCode != 422 || !strings.Contains(string(r.body), "Poppler") {
		t.Fatalf("missing Poppler=%d %s", r.StatusCode, r.body)
	}
	after, _ := os.ReadFile(h.ws.Path(workspace.FileCV))
	if !bytes.Equal(before, after) {
		t.Fatal("extraction changed stored CV")
	}
}
