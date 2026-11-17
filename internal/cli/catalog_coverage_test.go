package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"testing"

	"applysmith/internal/api/wire"
	"applysmith/internal/onboarding"
)

// specLeaves are the doctor checks the setup specification names, one list for
// the whole catalog.
var specLeaves = strings.Fields(`prerequisites.platform prerequisites.chrome prerequisites.disk workspace.location workspace.files workspace.migrations
	identity.fields cv.content work-authorization.answers targets.fields story.narrative story.voice resumes.files answer-bank.blocking
	llm.provider llm.test-age browser.apply-window browser.fingerprint browser.signins gmail.login gmail.fetch
	telegram.token telegram.pairing credentials.default credentials.vault captcha.key discovery.sources discovery.registry discovery.schedules discovery.schedule-hygiene discovery.schedule-audit
	extension.pairing orientation.ack runtime.server-lock runtime.port runtime.keychain`)

func (rs *runningServer) call(method, path, token string, body any) (int, []byte) {
	rs.te.t.Helper()
	var rd *bytes.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	} else {
		rd = bytes.NewReader(nil)
	}
	req, _ := http.NewRequest(method, rs.origin+path, rd)
	req.Header.Set("Authorization", "Bearer "+token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		rs.te.t.Fatal(err)
	}
	defer resp.Body.Close()
	var buf bytes.Buffer
	_, _ = buf.ReadFrom(resp.Body)
	return resp.StatusCode, buf.Bytes()
}

// The command line, the wizard and the doctor all take their steps from the one
// catalog: the same 18 ids in the same order from `setup status`, from the
// wizard's state endpoint, and from the doctor's groups, with every spec leaf
// present and every step's schema served by the same writer.
func TestCatalogIsCoveredByTheCommandLineTheWizardAndTheDoctor(t *testing.T) {
	te := newTestEnv(t)
	te.hook = machineProbes
	ctx := context.Background()
	if out := te.run(ctx, "init", "--workspace", te.ws); out.code != 0 {
		t.Fatal(out)
	}
	rs := te.startServe()
	token := strings.TrimSpace(te.run(ctx, "pair", "--workspace", te.ws).stdout)
	cat := onboarding.Catalog()
	var ids []string
	for _, c := range cat {
		ids = append(ids, c.ID)
	}
	if len(ids) != 18 {
		t.Fatalf("the catalog has %d steps", len(ids))
	}

	// Command line.
	cli := te.status()
	var cliIDs []string
	for _, s := range cli.Steps {
		cliIDs = append(cliIDs, s.ID)
	}
	if !slices.Equal(cliIDs, ids) {
		t.Errorf("setup status lists %v, the catalog %v", cliIDs, ids)
	}

	// Wizard.
	code, body := rs.call("GET", "/api/setup/state", token, nil)
	var env struct {
		Data wire.SetupState `json:"data"`
	}
	if code != 200 || json.Unmarshal(body, &env) != nil {
		t.Fatalf("GET /api/setup/state: %d %s", code, body)
	}
	var wizardIDs []string
	for i, s := range env.Data.Steps {
		wizardIDs = append(wizardIDs, s.ID)
		c := cat[i]
		if s.Title != c.Title || s.Required != c.Required || s.Phase != c.Phase || len(s.Fields) != len(c.Schema) || s.Guide != c.Guide || s.Status != cli.of(s.ID) {
			t.Errorf("wizard step %s differs from the catalog or the command line: %+v", s.ID, s)
		}
		if s.NeedsHuman && s.Instruction == "" {
			t.Errorf("wizard step %s needs a person but gives no instruction", s.ID)
		}
	}
	if !slices.Equal(wizardIDs, ids) {
		t.Errorf("the wizard lists %v, the catalog %v", wizardIDs, ids)
	}
	if env.Data.Progress != cli.Progress {
		t.Errorf("wizard progress %d, command line %d", env.Data.Progress, cli.Progress)
	}
	// Every step has a writer the wizard can reach; none answers an internal error.
	for _, id := range ids {
		if id == "browser" { // launches the managed browser
			continue
		}
		code, body := rs.call("POST", "/api/setup/steps/"+id+"/apply", token, wire.SetupApplyRequest{Input: map[string]any{}, DryRun: true})
		if code >= 500 && code != 503 || code == 404 {
			t.Errorf("apply %s: %d %s", id, code, body)
		}
		if out := te.run(ctx, "setup", "schema", id, "--json"); out.code != 0 {
			t.Errorf("schema %s: %+v", id, out)
		}
	}
	if code, _ := rs.call("POST", "/api/setup/steps/frobnicate/apply", token, wire.SetupApplyRequest{Input: map[string]any{}}); code != 422 {
		t.Errorf("an unknown step: %d", code)
	}

	// Doctor, through the command line and through the wizard's button.
	type report struct{ Results []wire.DoctorResult }
	var cliDoctor report
	out := te.run(ctx, "doctor", "--workspace", te.ws, "--port", "0", "--json", "--offline")
	if err := json.Unmarshal([]byte(out.stdout), &cliDoctor); err != nil {
		t.Fatalf("doctor: %v\n%+v", err, out)
	}
	code, body = rs.call("POST", "/api/setup/doctor", token, nil)
	var wizardDoctor struct {
		Data wire.DoctorReport `json:"data"`
	}
	if code != 200 || json.Unmarshal(body, &wizardDoctor) != nil {
		t.Fatalf("POST /api/setup/doctor: %d %s", code, body)
	}
	if wizardDoctor.Data.Platform != onboarding.PlatformStatement {
		t.Errorf("the wizard's doctor does not open with the platform statement")
	}
	groups := map[string]bool{}
	cliLeaves, wizardLeaves := map[string]bool{}, map[string]bool{}
	for _, r := range cliDoctor.Results {
		cliLeaves[r.ID] = true
		group, _, _ := strings.Cut(r.ID, ".")
		groups[group] = true
	}
	for _, r := range wizardDoctor.Data.Results {
		wizardLeaves[r.ID] = true
	}
	for _, id := range ids {
		if !groups[id] {
			t.Errorf("the doctor has no check for step %s", id)
		}
	}
	for _, leaf := range specLeaves {
		if !cliLeaves[leaf] || !wizardLeaves[leaf] {
			t.Errorf("leaf %s: command line %v, wizard %v", leaf, cliLeaves[leaf], wizardLeaves[leaf])
		}
	}
	if len(cliLeaves) != len(wizardLeaves) {
		t.Errorf("the doctor runs %d leaves, the wizard's button %d", len(cliLeaves), len(wizardLeaves))
	}
}
