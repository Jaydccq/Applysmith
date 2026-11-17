package vault

import (
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/zalando/go-keyring"
)

// spy counts keychain calls so a test can prove the keychain was not touched.
type spy struct {
	Backend
	calls int
}

func (s *spy) Get(a string) (string, error) { s.calls++; return s.Backend.Get(a) }
func (s *spy) Set(a, v string) error        { s.calls++; return s.Backend.Set(a, v) }
func (s *spy) Delete(a string) error        { s.calls++; return s.Backend.Delete(a) }

type env map[string]string

func (e env) get(k string) string { return e[k] }

func newVault(t *testing.T, ws string, b Backend, e env) *Vault {
	t.Helper()
	v, err := New(ws, b, WithGetenv(e.get))
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func mustSet(t *testing.T, v *Vault, name, value string) SetResult {
	t.Helper()
	r, err := v.Set(name, value)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestRoundTripUsesTheWorkspaceNamespacedAccount(t *testing.T) {
	m := NewMemory()
	v := newVault(t, "ws-one", m, nil)
	mustSet(t, v, "twocaptcha-key", "test-value-1")
	got, err := v.Get("twocaptcha-key")
	if err != nil || got != "test-value-1" {
		t.Fatalf("%q %v", got, err)
	}
	if acc := m.Accounts(); len(acc) != 1 || acc[0] != "ws-one:twocaptcha-key" {
		t.Fatalf("accounts = %v", acc)
	}
}

func TestReplaceDoesNotDuplicate(t *testing.T) {
	m := NewMemory()
	v := newVault(t, "ws", m, nil)
	mustSet(t, v, "github-token", "first")
	mustSet(t, v, "github-token", "second")
	if got, _ := v.Get("github-token"); got != "second" {
		t.Fatalf("got %q", got)
	}
	if len(m.Accounts()) != 1 {
		t.Fatalf("accounts = %v", m.Accounts())
	}
}

func TestInvalidNamesAreRejectedBeforeTheKeychainIsTouched(t *testing.T) {
	long := "api-key-" + strings.Repeat("a", 200)
	tests := []string{
		"", "Signin Default", "misc-token", "SIGNIN-DEFAULT", "-signin-default", ".hidden",
		"signin-alternate-123", "signin-alternate-ZZZZZZZZZZZZ", "signin-alternate-0123456789abc",
		"signin-site-", "signin-site-Example.com", "signin-site--x", "tenant-workday", "tenant--x",
		"api-key-", "api-key-OpenRouter", long, "twocaptcha-key ", "twocaptcha key", "api_token",
	}
	for _, name := range tests {
		t.Run(name, func(t *testing.T) {
			s := &spy{Backend: NewMemory()}
			v := newVault(t, "ws", s, nil)
			if _, err := v.Set(name, "x"); !IsCode(err, CodeNameInvalid) {
				t.Errorf("Set: %v", err)
			}
			if _, err := v.Get(name); !IsCode(err, CodeNameInvalid) {
				t.Errorf("Get: %v", err)
			}
			if err := v.Delete(name); !IsCode(err, CodeNameInvalid) {
				t.Errorf("Delete: %v", err)
			}
			if _, err := v.Test(name); !IsCode(err, CodeNameInvalid) {
				t.Errorf("Test: %v", err)
			}
			if s.calls != 0 {
				t.Errorf("keychain touched %d times", s.calls)
			}
		})
	}
}

func TestUnregisteredNameMessagePointsAtTheRegistry(t *testing.T) {
	_, err := newVault(t, "ws", NewMemory(), nil).Set("misc-token", "x")
	if !strings.Contains(err.Error(), "registry") {
		t.Fatalf("err = %v", err)
	}
}

func TestEveryRegisteredKindAcceptsItsNames(t *testing.T) {
	tests := map[string]string{
		"signin-default":                   "signin_default",
		"signin-alternate-0123456789ab":    "signin_alternate",
		"signin-site-careers.example.test": "signin_site",
		"tenant-workday-example":           "tenant",
		"tenant-greenhouse-example-corp":   "tenant",
		"api-key-openrouter":               "api_key",
		"api-key-exa":                      "api_key",
		"api-key-x":                        "api_key",
		"chatgpt-refresh":                  "token",
		"chatgpt-access":                   "token",
		"gmail-app-password":               "token",
		"telegram-bot-token":               "token",
		"twocaptcha-key":                   "token",
		"github-token":                     "token",
		"cloudflared-tunnel-token":         "token",
		"api-token":                        "token",
	}
	for name, kind := range tests {
		row, err := Lookup(name)
		if err != nil {
			t.Errorf("%s: %v", name, err)
			continue
		}
		if row.Kind != kind {
			t.Errorf("%s: kind %s, want %s", name, row.Kind, kind)
		}
	}
}

func TestRegistryRowsAreComplete(t *testing.T) {
	seen := map[string]bool{}
	for _, r := range Registry() {
		if r.Pattern == nil || r.NamePattern == "" || r.Description == "" || r.AccountTemplate != "<workspaceId>:<name>" || r.EnvPattern != "APPLYSMITH_SECRET_<NAME>" {
			t.Errorf("incomplete row %+v", r)
		}
		if seen[r.NamePattern] {
			t.Errorf("duplicate row %s", r.NamePattern)
		}
		seen[r.NamePattern] = true
	}
	row, err := Lookup(APIToken)
	if err != nil || !row.Required || row.SetupStep != "" {
		t.Fatalf("api-token row = %+v %v", row, err)
	}
	if row, _ := Lookup("twocaptcha-key"); row.SetupStep != "captcha" {
		t.Fatalf("captcha step = %q", row.SetupStep)
	}
}

func TestEnvName(t *testing.T) {
	tests := map[string]string{
		"twocaptcha-key":                   "APPLYSMITH_SECRET_TWOCAPTCHA_KEY",
		"api-token":                        "APPLYSMITH_SECRET_API_TOKEN",
		"signin-site-careers.example.test": "APPLYSMITH_SECRET_SIGNIN_SITE_CAREERS_EXAMPLE_TEST",
		"telegram-bot-token":               "APPLYSMITH_SECRET_TELEGRAM_BOT_TOKEN",
	}
	for name, want := range tests {
		if got := EnvName(name); got != want {
			t.Errorf("%s: %s, want %s", name, got, want)
		}
	}
}

func TestEnvironmentWinsOverTheKeychain(t *testing.T) {
	e := env{"APPLYSMITH_SECRET_TWOCAPTCHA_KEY": "from-env"}
	v := newVault(t, "ws", NewMemory(), e)
	mustSet(t, v, "twocaptcha-key", "from-keychain")
	if got, _ := v.Get("twocaptcha-key"); got != "from-env" {
		t.Fatalf("got %q", got)
	}
	e["APPLYSMITH_SECRET_TWOCAPTCHA_KEY"] = "changed"
	if got, _ := v.Get("twocaptcha-key"); got != "changed" {
		t.Fatalf("env is cached: %q", got)
	}
	delete(e, "APPLYSMITH_SECRET_TWOCAPTCHA_KEY")
	if got, _ := v.Get("twocaptcha-key"); got != "from-keychain" {
		t.Fatalf("got %q", got)
	}
}

func TestWhitespaceOnlyEnvironmentIsIgnored(t *testing.T) {
	v := newVault(t, "ws", NewMemory(), env{"APPLYSMITH_SECRET_API_TOKEN": "   \t\n"})
	mustSet(t, v, "api-token", "kc-token")
	got, err := v.Get("api-token")
	if err != nil || got != "kc-token" {
		t.Fatalf("%q %v", got, err)
	}
	if src, _ := v.Test("api-token"); src != SourceKeychain {
		t.Fatalf("source = %s", src)
	}
}

func TestHeadlessHostReadsTheEnvironmentWithoutAKeychain(t *testing.T) {
	m := NewMemory()
	m.FailWith(ErrUnavailable)
	v := newVault(t, "ws", m, env{"APPLYSMITH_SECRET_TELEGRAM_BOT_TOKEN": "bot-secret"})
	got, err := v.Get("telegram-bot-token")
	if err != nil || got != "bot-secret" {
		t.Fatalf("%q %v", got, err)
	}
	entries := v.List([]string{"telegram-bot-token", "twocaptcha-key"})
	if !entries[0].Present || entries[0].Source != SourceEnv || entries[0].Err != nil {
		t.Fatalf("entry = %+v", entries[0])
	}
	if entries[1].Present || !IsCode(entries[1].Err, CodeKeychainUnavailable) {
		t.Fatalf("entry = %+v", entries[1])
	}
}

func TestSetWarnsWhenTheEnvironmentShadowsIt(t *testing.T) {
	v := newVault(t, "ws", NewMemory(), env{"APPLYSMITH_SECRET_GITHUB_TOKEN": "env-value"})
	res := mustSet(t, v, "github-token", "new-value")
	if res.ShadowedBy != "APPLYSMITH_SECRET_GITHUB_TOKEN" {
		t.Fatalf("result = %+v", res)
	}
	if res := mustSet(t, newVault(t, "ws", NewMemory(), nil), "github-token", "v"); res.ShadowedBy != "" {
		t.Fatalf("unexpected shadow %+v", res)
	}
}

func TestDeletingAnEnvironmentOnlySecret(t *testing.T) {
	v := newVault(t, "ws", NewMemory(), env{"APPLYSMITH_SECRET_GITHUB_TOKEN": "env-value"})
	err := v.Delete("github-token")
	if !IsCode(err, CodeEnvManaged) || !strings.Contains(err.Error(), "APPLYSMITH_SECRET_GITHUB_TOKEN") {
		t.Fatalf("err = %v", err)
	}
	// A keychain entry that the environment shadows is still deletable.
	mustSet(t, v, "github-token", "kc")
	if err := v.Delete("github-token"); err != nil {
		t.Fatal(err)
	}
}

func TestDeleteMissingIsNotFound(t *testing.T) {
	if err := newVault(t, "ws", NewMemory(), nil).Delete("github-token"); !IsCode(err, CodeNotFound) {
		t.Fatalf("err = %v", err)
	}
}

func TestThreeDistinctKeychainErrors(t *testing.T) {
	tests := []struct {
		name    string
		backend error
		code    string
		contain string
	}{
		{"absent", nil, CodeNotFound, "no secret"},
		{"denied", ErrDenied, CodeKeychainDenied, "unlock the keychain"},
		{"unavailable", ErrUnavailable, CodeKeychainUnavailable, "APPLYSMITH_SECRET_TWOCAPTCHA_KEY"},
		{"unknown failure reads as unavailable", errors.New("dbus: boom"), CodeKeychainUnavailable, "No OS keychain"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			m := NewMemory()
			if tc.backend != nil {
				m.FailWith(tc.backend)
			}
			v := newVault(t, "ws", m, nil)
			_, err := v.Get("twocaptcha-key")
			if !IsCode(err, tc.code) {
				t.Fatalf("err = %v", err)
			}
			if !strings.Contains(strings.ToLower(err.Error()), strings.ToLower(tc.contain)) {
				t.Fatalf("message %q lacks %q", err, tc.contain)
			}
			for _, other := range []string{CodeNotFound, CodeKeychainDenied, CodeKeychainUnavailable} {
				if other != tc.code && IsCode(err, other) {
					t.Fatalf("error also matches %s", other)
				}
			}
		})
	}
}

func TestUnavailableTextNamesTheSecretsVariable(t *testing.T) {
	m := NewMemory()
	m.FailWith(ErrUnavailable)
	_, err := newVault(t, "ws", m, nil).Get("api-token")
	want := "No OS keychain is available on this platform. Set the secret through the environment variable APPLYSMITH_SECRET_API_TOKEN (for example APPLYSMITH_SECRET_TWOCAPTCHA_KEY) and restart the server."
	var ve *Error
	if !errors.As(err, &ve) || ve.Message != want {
		t.Fatalf("message = %q", ve.Message)
	}
}

func TestDeniedTextMentionsRebuiltBinaries(t *testing.T) {
	m := NewMemory()
	m.FailWith(ErrDenied)
	_, err := newVault(t, "ws", m, nil).Get("api-token")
	if !strings.Contains(err.Error(), "rebuilt unsigned binaries") {
		t.Fatalf("err = %v", err)
	}
}

func TestErrorTextNeverQuotesTheBackend(t *testing.T) {
	m := NewMemory()
	m.FailWith(fmt.Errorf("security: password is hunter2-canary"))
	v := newVault(t, "ws", m, nil)
	_, errGet := v.Get("api-token")
	_, errSet := v.Set("api-token", "hunter2-canary")
	for _, err := range []error{errGet, errSet, v.Delete("api-token")} {
		if strings.Contains(err.Error(), "hunter2") {
			t.Fatalf("backend text leaked: %v", err)
		}
	}
}

func TestEmptyAndOversizedValues(t *testing.T) {
	m := NewMemory()
	m.SetMaxLen(10)
	v := newVault(t, "ws", m, nil)
	if _, err := v.Set("api-token", ""); !IsCode(err, CodeEmpty) {
		t.Fatalf("empty: %v", err)
	}
	if _, err := v.Set("api-token", strings.Repeat("x", 11)); !IsCode(err, CodeTooLarge) {
		t.Fatalf("large: %v", err)
	}
	if len(m.Accounts()) != 0 {
		t.Fatal("a refused value was stored")
	}
}

func TestOversizedValueIsRefusedBeforeTheBackend(t *testing.T) {
	s := &spy{Backend: NewMemory()}
	v := newVault(t, "ws", s, nil)
	if _, err := v.Set("api-token", strings.Repeat("x", maxValueBytes+1)); !IsCode(err, CodeTooLarge) {
		t.Fatalf("err = %v", err)
	}
	if s.calls != 0 {
		t.Fatalf("backend called %d times", s.calls)
	}
	mustSet(t, v, "api-token", strings.Repeat("x", maxValueBytes))
}

func TestListCarriesNoValue(t *testing.T) {
	v := newVault(t, "ws", NewMemory(), nil)
	mustSet(t, v, "api-key-openrouter", "canary-key-1")
	mustSet(t, v, "telegram-bot-token", "canary-key-2")
	entries := v.List([]string{"api-key-openrouter", "telegram-bot-token", "github-token", "bad name"})
	out := fmt.Sprintf("%+v", entries)
	if strings.Contains(out, "canary") {
		t.Fatalf("list leaked a value: %s", out)
	}
	if !entries[0].Present || entries[0].Source != SourceKeychain || entries[2].Present || entries[2].Err != nil {
		t.Fatalf("entries = %+v", entries)
	}
	if !IsCode(entries[3].Err, CodeNameInvalid) {
		t.Fatalf("bad name entry = %+v", entries[3])
	}
}

func TestTestReportsSourceWithoutTheValue(t *testing.T) {
	v := newVault(t, "ws", NewMemory(), nil)
	if _, err := v.Test("github-token"); !IsCode(err, CodeNotFound) {
		t.Fatalf("err = %v", err)
	}
	mustSet(t, v, "github-token", "tok")
	if src, err := v.Test("github-token"); err != nil || src != SourceKeychain {
		t.Fatalf("%s %v", src, err)
	}
}

func TestHasKeepsTheKeychainAnswerUntilAWriteHere(t *testing.T) {
	mem := NewMemory()
	v := newVault(t, "ws", mem, nil)
	if v.Has("github-token") {
		t.Fatal("present before any write")
	}
	if err := mem.Set(v.Account("github-token"), "tok"); err != nil {
		t.Fatal(err)
	}
	if v.Has("github-token") {
		t.Fatal("a write by another process showed before the answer expired")
	}
	mustSet(t, v, "github-token", "tok")
	if !v.Has("github-token") {
		t.Fatal("a write through the vault was not seen at once")
	}
	if err := v.Delete("github-token"); err != nil {
		t.Fatal(err)
	}
	if v.Has("github-token") {
		t.Fatal("a delete through the vault was not seen at once")
	}
	if v.Has("not a name") {
		t.Fatal("an invalid name is present")
	}
}

// gated holds keychain reads until released and counts them.
type gated struct {
	Backend
	mu   sync.Mutex
	hold chan struct{}
	gets int
}

func (g *gated) Get(a string) (string, error) {
	g.mu.Lock()
	g.gets++
	hold := g.hold
	g.mu.Unlock()
	if hold != nil {
		<-hold
	}
	return g.Backend.Get(a)
}

// An answer past presenceTTL is still given at once while one lookup runs
// beside the callers; the asks after it see what that lookup found.
func TestHasAnswersAtOnceWhileAnExpiredAnswerIsLookedUpAgain(t *testing.T) {
	mem := NewMemory()
	g := &gated{Backend: mem}
	v := newVault(t, "ws", g, nil)
	acct := v.Account("github-token")
	if v.Has("github-token") {
		t.Fatal("present before any write")
	}
	if err := mem.Set(acct, "tok"); err != nil {
		t.Fatal(err)
	}
	v.mu.Lock()
	v.presence[acct] = presenceEntry{present: false, at: time.Now().Add(-presenceTTL)}
	v.mu.Unlock()
	g.mu.Lock()
	g.hold, g.gets = make(chan struct{}), 0
	g.mu.Unlock()

	answered := make(chan bool)
	go func() {
		for range 3 {
			answered <- v.Has("github-token")
		}
	}()
	for range 3 {
		select {
		case present := <-answered:
			if present {
				t.Fatal("the lookup's answer was given before it ended")
			}
		case <-time.After(time.Second):
			t.Fatal("Has waited for the keychain although it kept an answer")
		}
	}
	lookups := func() int {
		g.mu.Lock()
		defer g.mu.Unlock()
		return g.gets
	}
	deadline := time.Now().Add(time.Second)
	for lookups() == 0 {
		if time.Now().After(deadline) {
			t.Fatal("the expired answer was not looked up again")
		}
		time.Sleep(5 * time.Millisecond)
	}
	v.Has("github-token")
	if n := lookups(); n != 1 {
		t.Fatalf("%d keychain lookups for one expired answer, want 1", n)
	}
	g.mu.Lock()
	close(g.hold)
	g.hold = nil
	g.mu.Unlock()
	deadline = time.Now().Add(time.Second)
	for !v.Has("github-token") {
		if time.Now().After(deadline) {
			t.Fatal("the write by another process never showed")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// Check answers as Test does, error codes included, from the kept answer; a
// keychain refusal is returned, never kept.
func TestCheckAnswersAsTestDoes(t *testing.T) {
	mem := NewMemory()
	v := newVault(t, "ws", mem, nil)
	for i := 0; i < 2; i++ { // the second answer comes from the kept absence
		if err := v.Check("github-token"); !IsCode(err, CodeNotFound) {
			t.Fatalf("absent: %v", err)
		}
	}
	mustSet(t, v, "github-token", "tok")
	if err := v.Check("github-token"); err != nil {
		t.Fatalf("present: %v", err)
	}
	mem.FailWith(ErrDenied)
	if err := v.Check("github-token"); err != nil {
		t.Fatalf("a kept answer asked the keychain again: %v", err)
	}
	if err := v.Check("api-token"); !IsCode(err, CodeKeychainDenied) {
		t.Fatalf("refused: %v", err)
	}
	mem.FailWith(nil)
	mustSet(t, v, "api-token", "tok")
	if err := v.Check("api-token"); err != nil {
		t.Fatalf("a refusal was kept: %v", err)
	}
}

func TestEnvCollisions(t *testing.T) {
	got := EnvCollisions([]string{"signin-site-a.b", "signin-site-a-b", "signin-default"})
	if len(got) != 1 || len(got["APPLYSMITH_SECRET_SIGNIN_SITE_A_B"]) != 2 {
		t.Fatalf("collisions = %v", got)
	}
}

func TestWorkspaceIDValidation(t *testing.T) {
	for _, id := range []string{"", "a:b"} {
		if _, err := New(id, NewMemory()); err == nil {
			t.Errorf("%q accepted", id)
		}
	}
}

// Independent workspaces resolve the same logical name to different entries.
func TestWorkspaceCredentialIsolation(t *testing.T) {
	shared := NewMemory() // one physical keychain
	a := newVault(t, "workspace-a", shared, nil)
	b := newVault(t, "workspace-b", shared, nil)
	mustSet(t, a, APIToken, "token-a-1")
	mustSet(t, b, APIToken, "token-b-1")
	mustSet(t, a, "signin-default", "pw-a")

	// Workspace A rotates its API token while B keeps serving.
	mustSet(t, a, APIToken, "token-a-2")
	if got, _ := b.Get(APIToken); got != "token-b-1" {
		t.Fatalf("B's token changed: %q", got)
	}
	if got, _ := a.Get(APIToken); got != "token-a-2" {
		t.Fatalf("A's token = %q", got)
	}
	if got, _ := a.Get(APIToken); got == "token-b-1" {
		t.Fatal("A reads B's token")
	}
	if _, err := b.Get("signin-default"); !IsCode(err, CodeNotFound) {
		t.Fatalf("B sees A's sign-in password: %v", err)
	}
	if err := a.Delete(APIToken); err != nil {
		t.Fatal(err)
	}
	if got, err := b.Get(APIToken); err != nil || got != "token-b-1" {
		t.Fatalf("deleting A's token touched B: %q %v", got, err)
	}
	want := []string{"workspace-a:signin-default", "workspace-b:api-token"}
	if got := shared.Accounts(); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("accounts = %v", got)
	}
}

func TestEnvironmentOverrideDoesNotCopyIntoAnotherWorkspace(t *testing.T) {
	shared := NewMemory()
	a := newVault(t, "workspace-a", shared, env{"APPLYSMITH_SECRET_API_TOKEN": "env-token"})
	b := newVault(t, "workspace-b", shared, nil)
	if got, _ := a.Get(APIToken); got != "env-token" {
		t.Fatalf("got %q", got)
	}
	if _, err := b.Get(APIToken); !IsCode(err, CodeNotFound) {
		t.Fatalf("env value reached B: %v", err)
	}
	if len(shared.Accounts()) != 0 {
		t.Fatal("reading an env override wrote to the keychain")
	}
}

func TestMapKeyringErr(t *testing.T) {
	exitErr := exec.Command("sh", "-c", "exit 1").Run()
	tests := []struct {
		in   error
		want error
	}{
		{nil, nil},
		{keyring.ErrNotFound, ErrNotFound},
		{keyring.ErrSetDataTooBig, ErrTooLarge},
		{keyring.ErrUnsupportedPlatform, ErrUnavailable},
		{exitErr, ErrDenied},
		{errors.New("The name org.freedesktop.secrets was not provided"), ErrUnavailable},
	}
	for _, tc := range tests {
		got := mapKeyringErr(tc.in)
		if tc.want == nil && got != nil || tc.want != nil && !errors.Is(got, tc.want) {
			t.Errorf("%v -> %v, want %v", tc.in, got, tc.want)
		}
	}
	if err := mapKeyringErr(errors.New("secret=canary")); strings.Contains(err.Error(), "canary") {
		t.Fatal("library text kept")
	}
}

func TestKeychainServiceName(t *testing.T) {
	if k, ok := NewKeychain().(keyringBackend); !ok || k.service != "applysmith" {
		t.Fatalf("service = %+v", k)
	}
}
