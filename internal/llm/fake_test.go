package llm

import (
	"applysmith/internal/vault"
	"net"
	"strings"
	"testing"
)

func fakeEnv(t *testing.T, fx ...Fixture) *env {
	t.Helper()
	e := newEnv(t, WithFake(NewFake(fx...)))
	e.provider(ProviderConfig{ID: "fk", Kind: KindFake}, "")
	e.provider(ProviderConfig{ID: "fk2", Kind: KindFake}, "")
	e.route("narrative", Route{Provider: "fk", Model: "m1", Thinking: ThinkOff, Fallbacks: []Fallback{{"fk2", "m2"}}})
	return e
}

func TestLLM_Fake_MissingFixtureFails(t *testing.T) {
	e := fakeEnv(t)
	_, err := e.r.Complete(bg(), Request{Task: "narrative", Prompt: "a secret prompt"})
	ce := wantKind(t, err, KindFakeFixture)
	if !strings.Contains(ce.Error(), "narrative") || strings.Contains(ce.Error(), "secret") {
		t.Fatalf("error = %v", ce)
	}
	if !strings.Contains(ce.Error(), PromptHash("a secret prompt")[:12]) {
		t.Fatalf("error does not carry the fixture identifier: %v", ce)
	}
}

func TestLLM_Offline_SimulationMakesNoNetworkOrSubprocessCall(t *testing.T) {
	// A listener that fails the test on any connection, and an empty PATH.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		if c, err := ln.Accept(); err == nil {
			t.Error("a network connection was made")
			c.Close()
		}
	}()
	t.Setenv("PATH", t.TempDir())
	e := newEnv(t, WithOfflineSimulation(), WithFake(NewFake(
		Fixture{Task: "quick-screen", Contains: "p", Steps: []Step{{Text: "sim"}}},
		Fixture{Task: "interview-prep", Contains: "p", Steps: []Step{{Text: "sim2"}}})))
	// A stored row pointing at a real endpoint is ignored by the simulation.
	e.provider(ProviderConfig{ID: "real", Kind: KindOpenAI, BaseURL: "http://" + ln.Addr().String()}, fakeKey("of"))
	e.route("quick-screen", Route{Provider: "real", Model: "m"})
	for _, task := range []string{"quick-screen", "interview-prep"} {
		resp, err := e.r.Complete(bg(), Request{Task: task, Prompt: "p"})
		if err != nil || resp.Provider != offlineProvider {
			t.Fatalf("%s: resp = %+v err = %v", task, resp, err)
		}
	}
}

func TestLLM_NotSelectedSimulation_DoesNotRouteToTheFakeProvider(t *testing.T) {
	// Only an explicit selection routes everything to the fake provider.
	e := newEnv(t)
	_, err := e.r.Complete(bg(), Request{Task: "quick-screen", Prompt: "p"})
	wantKind(t, err, KindNoRoute)
}

func TestLLM_Credentials_EnvOverridesKeychain(t *testing.T) {
	e := newEnv(t)
	stored, override := fakeKey("st"), fakeKey("ov")
	s := newOAServer(t, okHandler("ok"))
	e.provider(oaProvider("deepseek", s), stored)
	e.route("narrative", Route{Provider: "deepseek", Model: "m", Thinking: ThinkOff})

	if _, err := e.r.Complete(bg(), Request{Task: "narrative", Prompt: "hi"}); err != nil || s.auth != "Bearer "+stored {
		t.Fatalf("keychain key not used: %v %q", err, s.auth)
	}
	e.envs["APPLYSMITH_SECRET_API_KEY_DEEPSEEK"] = override
	if _, err := e.r.Complete(bg(), Request{Task: "narrative", Prompt: "hi"}); err != nil || s.auth != "Bearer "+override {
		t.Fatalf("environment override not used: %v %q", err, s.auth)
	}
	// A key set on a headless host through the environment alone is enough.
	if err := e.r.ClearKey("deepseek"); err != nil {
		t.Fatal(err)
	}
	if _, err := e.r.Complete(bg(), Request{Task: "narrative", Prompt: "hi"}); err != nil {
		t.Fatalf("environment-only key: %v", err)
	}
}

func TestLLM_Credentials_OnlyTheScopedVaultPath(t *testing.T) {
	e := newEnv(t)
	if err := e.r.SetKey("openrouter", fakeKey("sc")); err != nil {
		t.Fatal(err)
	}
	accounts := e.mem.Accounts()
	if len(accounts) != 1 || accounts[0] != testWorkspaceID+":api-key-openrouter" {
		t.Fatalf("keychain accounts = %v, want one workspace-scoped api-key entry", accounts)
	}
	if err := e.r.SetKey("Bad Id", fakeKey("sc")); err == nil {
		t.Fatal("a provider id outside the registry shape was accepted")
	}
}

func TestLLM_Credentials_KeychainUnavailableIsUnconfigured(t *testing.T) {
	e := newEnv(t)
	s := newOAServer(t, okHandler("ok"))
	e.provider(oaProvider("a", s), fakeKey("kc"))
	e.route("narrative", Route{Provider: "a", Model: "m", Thinking: ThinkOff})
	e.mem.FailWith(vault.ErrUnavailable)
	_, err := e.r.Complete(bg(), Request{Task: "narrative", Prompt: "hi"})
	ce := wantKind(t, err, KindUnconfigured)
	if s.count() != 0 || strings.Contains(ce.Message, "security") {
		t.Fatalf("calls %d message %q", s.count(), ce.Message)
	}
}
