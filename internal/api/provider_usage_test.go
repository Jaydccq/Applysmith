package api

import (
	"strings"
	"testing"

	"applysmith/internal/api/wire"
	"applysmith/internal/providerusage"
)

func providerAccountResult(t *testing.T, r result) wire.ProviderAccount {
	t.Helper()
	if r.StatusCode != 200 {
		t.Fatalf("account request: %d %s", r.StatusCode, r.body)
	}
	var env wire.Success[wire.ProviderAccount]
	strictInto(t, r.body, &env)
	return env.Data
}
func TestProviderAccountsCredentialLifecycle(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	h := newRoutingHarness(t)
	var initial wire.Success[wire.ProviderAccounts]
	r := h.get("/api/llm/accounts")
	strictInto(t, r.body, &initial)
	if r.StatusCode != 200 || len(initial.Data.Accounts) != len(providerusage.Definitions()) {
		t.Fatalf("account list: %d %s", r.StatusCode, r.body)
	}
	path := "/api/llm/accounts/minimax-global/credentials"
	saved := h.send("PUT", path, map[string]any{"kind": "key", "value": testSecret})
	a := providerAccountResult(t, saved)
	if !a.KeyConfigured || a.CredentialStatus != "configured" || strings.Contains(string(saved.body), testSecret) {
		t.Fatal("key was not saved privately")
	}
	if got, err := h.srv.cfg.Vault.Get(providerusage.SecretName("minimax-global", "key")); err != nil || got != testSecret {
		t.Fatal("credential did not reach the vault")
	}
	// A usage result obtained with old credentials must disappear after removal.
	if !h.srv.provider.Usage.Begin("minimax-global") {
		t.Fatal("cache busy")
	}
	h.srv.provider.Usage.End("minimax-global", providerusage.Unknown("ok", "old quota"))
	a = providerAccountResult(t, h.send("DELETE", path+"/key", nil))
	if a.KeyConfigured || a.Usage.Status != "unconfigured" {
		t.Fatalf("stale credential or usage %+v", a)
	}
	a = providerAccountResult(t, h.post("/api/llm/accounts/minimax-global/refresh", nil))
	if a.Usage.Status != "unconfigured" || len(a.Usage.Windows) != 0 {
		t.Fatal("missing quota claimed as success")
	}
}
func TestProviderAccountsRefuseNativeImportAndInvalidTargets(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	h := newRoutingHarness(t)
	for _, entry := range []struct{ id, kind, value string }{{"codex", "key", "secret"}, {"glm-zai", "cookie", "auth=secret"}, {"minimax-cn", "key", "secret\r\nX-Injected: yes"}, {"opencode-go", "workspace", "https://evil.example"}} {
		r := h.send("PUT", "/api/llm/accounts/"+entry.id+"/credentials", map[string]any{"kind": entry.kind, "value": entry.value})
		if r.StatusCode != 422 {
			t.Fatalf("accepted unsupported credential for %s: %d %s", entry.id, r.StatusCode, r.body)
		}
	}
	if r := h.post("/api/llm/accounts/arbitrary-host/refresh", nil); r.StatusCode != 404 {
		t.Fatal("accepted arbitrary provider")
	}
	a := providerAccountResult(t, h.post("/api/llm/accounts/codex/refresh", nil))
	if a.CredentialStatus != "unknown" || a.Usage.Status != "unsupported" {
		t.Fatalf("native login falsely verified %+v", a)
	}
}
