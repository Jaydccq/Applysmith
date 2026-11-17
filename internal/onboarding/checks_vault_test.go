package onboarding

import (
	"strings"
	"testing"

	"applysmith/internal/credentials"
	"applysmith/internal/vault"
)

func TestDoctor_CredentialChecks(t *testing.T) {
	env := setupEnv(t)
	mem := env.Backend.(*vault.Memory)
	svc := credentials.New(env.Store, env.Vault, nil)
	check := func() Result { return run(t, env, "credentials.vault") }

	if r := check(); r.Status != Pass || !strings.Contains(r.Summary, "secrets from the environment: 0") {
		t.Fatalf("empty vault: %+v", r)
	}
	if _, err := svc.SetDefault(bg(), "alex@example.com", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.SetSite(bg(), "careers.example.test", "", "Site-Canary-Pw-1"); err != nil {
		t.Fatal(err)
	}
	if r := check(); r.Status != Pass {
		t.Fatalf("healthy: %+v", r)
	}

	// The keychain entry disappears outside the app: the index row is stale.
	if err := mem.Delete(env.Vault.Account("signin-site-careers.example.test")); err != nil {
		t.Fatal(err)
	}
	r := check()
	if r.Status != Warn || !strings.Contains(r.Summary, "signin-site-careers.example.test") || !strings.Contains(r.Fix, "applysmith secrets delete") {
		t.Fatalf("stale row: %+v", r)
	}
	if err := svc.Delete(bg(), "signin-site-careers.example.test"); err != nil {
		t.Fatal(err)
	}
	if r := check(); r.Status != Pass {
		t.Fatalf("after the row was removed: %+v", r)
	}

	// Two sites whose names differ only in a character the variable name flattens.
	for _, site := range []string{"a.b.example.test", "a-b.example.test"} {
		if _, err := svc.SetSite(bg(), site, "", "Site-Canary-Pw-2"); err != nil {
			t.Fatal(err)
		}
	}
	r = check()
	if r.Status != Warn || !strings.Contains(r.Summary, "APPLYSMITH_SECRET_SIGNIN_SITE_A_B_EXAMPLE_TEST") {
		t.Fatalf("collision: %+v", r)
	}
	if strings.Contains(r.Summary+r.Fix, "Canary") {
		t.Fatal("a finding carries a value")
	}
}

func TestDoctor_CredentialChecks_EnvironmentCountAndKeychainTrouble(t *testing.T) {
	env := setupEnv(t)
	env.Getenv = func(k string) string {
		if k == vault.EnvName("twocaptcha-key") {
			return "env-supplied-canary-1"
		}
		return ""
	}
	closeFn, err := env.Open(bg(), true) // rebuild the vault with the new environment
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(closeFn)
	if r := run(t, env, "credentials.vault"); r.Status != Pass || !strings.Contains(r.Summary, "secrets from the environment: 1") || strings.Contains(r.Summary, "canary") {
		t.Fatalf("environment count: %+v", r)
	}
	svc := credentials.New(env.Store, env.Vault, nil)
	if _, err := svc.SetDefault(bg(), "alex@example.com", "Default-Pw-1!xyz"); err != nil {
		t.Fatal(err)
	}
	env.Backend.(*vault.Memory).FailWith(vault.ErrDenied)
	r := run(t, env, "credentials.vault")
	if r.Status != Warn || !strings.Contains(r.Summary, "signin-default") || !strings.Contains(r.Fix, "runtime.keychain") {
		t.Fatalf("keychain trouble is not a stale row: %+v", r)
	}
}
