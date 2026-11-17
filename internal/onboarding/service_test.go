package onboarding

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"

	"applysmith/internal/answerbank"
	"applysmith/internal/api"
	"applysmith/internal/api/apikit"
	"applysmith/internal/workspace"
)

func serviceFor(t *testing.T) (*Service, api.SetupHost, *Env) {
	t.Helper()
	env := setupEnv(t)
	return &Service{Backend: env.Backend, Getenv: env.Getenv}, api.SetupHost{Workspace: env.Workspace, Store: env.Store, Vault: env.Vault, Router: env.Router}, env
}

func TestService_StateListsTheCatalogInOrderAndNamesTheStepToOpenOn(t *testing.T) {
	svc, host, _ := serviceFor(t)
	st, err := svc.State(bg(), host)
	if err != nil {
		t.Fatal(err)
	}
	cat := Catalog()
	if len(st.Steps) != len(cat) {
		t.Fatalf("%d steps, the catalog has %d", len(st.Steps), len(cat))
	}
	for i, s := range st.Steps {
		c := cat[i]
		if s.ID != c.ID || s.N != c.N || s.Title != c.Title || s.Required != c.Required || len(s.Fields) != len(c.Schema) || s.Guide != c.Guide {
			t.Errorf("step %d: %+v does not match the catalog", i, s)
		}
		if s.Reasons == nil || s.Warnings == nil || s.Commands == nil || s.Fields == nil {
			t.Errorf("step %s has a nil list, which encodes as null", s.ID)
		}
	}
	first, _ := FirstIncomplete(EvaluateAll(bg(), serviceEnv(host)))
	if st.Next != first.ID {
		t.Errorf("next = %q, want %q", st.Next, first.ID)
	}
}

func serviceEnv(h api.SetupHost) *Env {
	return (&Service{}).env(h)
}

func TestService_AnAcknowledgeableReasonIsMarkedAndTheWorkspaceCannotBeMoved(t *testing.T) {
	svc, host, _ := serviceFor(t)
	if _, err := svc.Apply(bg(), host, "identity", map[string]any{"full_name": "Alex Example", "email": goodEmail(), "phone": goodPhone(), "city": "Leeds",
		"region": "West Yorkshire", "country": "GB", "street_address": "@ask-me", "postal_code": "@ask-me"}, false); err != nil {
		t.Fatal(err)
	}
	st, _ := svc.State(bg(), host)
	var ackable bool
	for _, s := range st.Steps {
		if s.ID == "identity" {
			for _, r := range s.Reasons {
				ackable = ackable || r.Ackable
			}
		}
	}
	if !ackable {
		t.Error("the persona reason is not marked ackable")
	}
	step, err := svc.Decide(bg(), host, "identity", "ack")
	if err != nil || step.Status != "complete" {
		t.Fatalf("ack: %+v %v", step, err)
	}
	_, err = svc.Apply(bg(), host, "workspace", map[string]any{"path": "/somewhere/else"}, false)
	var ae *apikit.Error
	if !errors.As(err, &ae) || ae.Details == nil || ae.Details.Fields["path"] == "" {
		t.Fatalf("moving the workspace: %v", err)
	}
	// Inside the wizard no note sends the user to the wizard or a terminal.
	t.Setenv("HOME", t.TempDir())
	res, err := svc.Apply(bg(), host, "workspace", map[string]any{}, false)
	if err != nil || strings.Contains(strings.Join(res.Notes, " "), "applysmith") {
		t.Fatalf("workspace notes in the wizard: %v %v", res.Notes, err)
	}
}

func TestService_ErrorsKeepTheirMeaningAcrossTheAPI(t *testing.T) {
	svc, host, _ := serviceFor(t)
	cases := map[string]struct {
		id    string
		input map[string]any
		want  string
	}{
		"fields":  {"identity", map[string]any{"email": "nope"}, "validation_failed"},
		"unknown": {"frobnicate", map[string]any{}, "validation_failed"},
		"person":  {"telegram", map[string]any{}, "conflict"},
	}
	for name, c := range cases {
		_, err := svc.Apply(bg(), host, c.id, c.input, false)
		var ae *apikit.Error
		if !errors.As(err, &ae) || string(ae.Code) != c.want {
			t.Errorf("%s: %v", name, err)
		}
	}
	if _, err := svc.Decide(bg(), host, "identity", "skip"); err == nil || !strings.Contains(err.Error(), "required") {
		t.Errorf("skipping a required step: %v", err)
	}
}

func TestService_DoctorOpensWithThePlatformStatementAndHoldsNoSecret(t *testing.T) {
	svc, host, env := serviceFor(t)
	if _, err := env.Vault.Set(secretCaptchaKey, "sk-test-"+strings.Repeat("a", 24)); err != nil {
		t.Fatal(err)
	}
	rep, err := svc.Doctor(bg(), host)
	if err != nil || rep.Platform != PlatformStatement || len(rep.Results) < 30 {
		t.Fatalf("%v %d", err, len(rep.Results))
	}
	for _, r := range rep.Results {
		if strings.Contains(r.Summary+r.Fix, "sk-test-") {
			t.Errorf("%s carries a secret", r.ID)
		}
	}
}

// The server hands setup its own answer bank, which keeps its parse of the file
// until the file changes, so an edit made outside the app is seen at once.
func TestService_StateOnTheServersBankSeesAnEditOfTheFile(t *testing.T) {
	svc, host, env := serviceFor(t)
	host.Bank = answerbank.New(env.Workspace, env.Store)
	reasons := func() string {
		st, err := svc.State(bg(), host)
		if err != nil {
			t.Fatal(err)
		}
		for _, s := range st.Steps {
			if s.ID == "answer-bank" {
				return fmt.Sprint(s.Reasons)
			}
		}
		t.Fatal("no answer-bank step")
		return ""
	}
	if r := reasons(); strings.Contains(r, "validation") {
		t.Fatalf("the seeded bank is invalid: %s", r)
	}
	if err := os.WriteFile(env.Workspace.Path(workspace.FileAnswerBank), []byte("version: 1\nentries: [\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if r := reasons(); !strings.Contains(r, "fails the answer bank's validation") {
		t.Fatalf("after a broken edit: %s", r)
	}
}
