// Clean-room: docs/provenance.md#CR-SUBSCRIPTIONS
// Written from OpenAI's public Sign in with ChatGPT documentation and the
// research spec (section 1a) alone.

package llm

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"
)

var uuidV4 = regexp.MustCompile(`^urn:uuid:[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

func TestLLM_ChatGPT_SignInFollowsThePublishedProtocol(t *testing.T) {
	e, f := chatgptEnv(t)
	b := e.beginSignIn(f)

	// First registration: the entry client, the app name and the stable host id.
	want := map[string]string{"client_id": chatgptEntryID, "agent_name_hint": chatgptAppName, "response_type": "code", "resource": chatgptResource,
		"scope": chatgptScope, "code_challenge_method": "S256"}
	for k, v := range want {
		if got := b.auth.Get(k); got != v {
			t.Errorf("authorize %s = %q, want %q", k, got, v)
		}
	}
	if !regexp.MustCompile(`^http://127\.0\.0\.1:\d+/auth/callback$`).MatchString(b.redirect) {
		t.Errorf("redirect_uri = %q: it must be http on 127.0.0.1, never localhost", b.redirect)
	}
	hostID := b.auth.Get("ext_agent_host_id")
	if !uuidV4.MatchString(hostID) {
		t.Errorf("ext_agent_host_id = %q, want urn:uuid:<v4>", hostID)
	}
	for _, k := range []string{"state", "nonce", "code_challenge"} {
		if b.auth.Get(k) == "" {
			t.Errorf("authorize has no %s", k)
		}
	}
	if b.auth.Has("id_token_hint") || b.auth.Has("login_hint") {
		t.Error("a first registration sends no hint")
	}
	if reg, _ := e.r.chatgptRegistration(bg()); reg.HostID != hostID {
		t.Errorf("the host id was not saved before the sign-in: %+v", reg)
	}

	// A callback that is not for this attempt changes nothing and the attempt keeps waiting.
	stray := b.consent()
	stray.Set("state", "not-the-state")
	if status, _ := b.callback(stray); status != http.StatusBadRequest {
		t.Fatalf("a callback with a foreign state answered %d", status)
	}
	if st, _ := e.r.ChatGPTStatus(bg()); !st.Pending || st.SignedIn {
		t.Fatalf("status after a stray callback = %+v", st)
	}

	if status, body := b.callback(b.consent()); status != http.StatusOK {
		t.Fatalf("callback = %d %s", status, body)
	}
	st, err := e.r.ChatGPTStatus(bg())
	if err != nil || !st.SignedIn || !st.PlanUsage || st.Account != "me@example.test" || st.Pending || st.Message != "" {
		t.Fatalf("status = %+v, err %v", st, err)
	}
	if n := len(f.tokenCalls("authorization_code")); n != 1 {
		t.Fatalf("%d code exchanges, want 1", n)
	}
	if !e.hasToken(vaultChatGPTAccess) || e.refreshToken() == "" {
		t.Error("the family is not in the vault")
	}
	var row string
	if err := e.st.Read().QueryRow(`SELECT value FROM llm_settings WHERE key = 'chatgpt'`).Scan(&row); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(row, e.refreshToken()) || strings.Contains(row, "eyJ") {
		t.Errorf("a token reached the database: %s", row)
	}

	// Signing in again reuses the issued client and the host, sends no name and hints the account.
	if _, err := e.r.SignOutChatGPT(bg(), false); err != nil {
		t.Fatal(err)
	}
	again := e.beginSignIn(f)
	if again.auth.Get("client_id") != f.issued || again.auth.Get("ext_agent_host_id") != hostID ||
		again.auth.Has("agent_name_hint") || again.auth.Get("login_hint") != "me@example.test" || again.auth.Get("scope") != chatgptScope {
		t.Errorf("re-authorization sent %v", again.auth)
	}
	if status, _ := again.callback(again.consent()); status != http.StatusOK {
		t.Fatalf("re-authorization callback = %d", status)
	}
}

func TestLLM_ChatGPT_SignInAddsTheProviderAndLeavesTheRouting(t *testing.T) {
	f := newFakeOpenAI(t)
	e := newEnv(t, f.options()...)
	f.now = e.clock.now
	e.provider(ProviderConfig{ID: "deepseek", Kind: KindOpenAI, BaseURL: "https://api.deepseek.com", Preset: "deepseek"}, fakeKey("ds"))
	if err := e.r.ConfigurePreset(bg(), "deepseek", ConfigureOptions{}); err != nil {
		t.Fatal(err)
	}
	before, err := LoadSettings(bg(), e.st)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := before.Provider("chatgpt"); ok {
		t.Fatal("the chatgpt provider exists before any sign-in")
	}

	b := e.beginSignIn(f)
	if status, body := b.callback(b.consent()); status != http.StatusOK {
		t.Fatalf("callback = %d %s", status, body)
	}
	after, err := LoadSettings(bg(), e.st)
	if err != nil {
		t.Fatal(err)
	}
	p, ok := after.Provider("chatgpt")
	if !ok || p.Kind != KindChatGPT {
		t.Fatalf("after a sign-in the chatgpt provider is %+v (present %v), so no task row can name it", p, ok)
	}
	if after.Preset != before.Preset || after.PresetFallback != before.PresetFallback {
		t.Errorf("the sign-in changed the active preset: %q/%q, was %q/%q", after.Preset, after.PresetFallback, before.Preset, before.PresetFallback)
	}
}

func TestLLM_ChatGPT_SignInRefusals(t *testing.T) {
	cases := []struct {
		name string
		set  func(f *fakeOpenAI, q url.Values)
		msg  string
	}{
		{"declined", func(f *fakeOpenAI, q url.Values) { q.Del("code"); q.Set("error", "access_denied") }, "declined"},
		{"foreign audience", func(f *fakeOpenAI, q url.Values) { f.idAud = "someone-else" }, "not issued to this app"},
		{"wrong nonce", func(f *fakeOpenAI, q url.Values) { f.idNonce = "replayed" }, "nonce"},
		{"expired id token", func(f *fakeOpenAI, q url.Values) { f.idExpiry = -2 * time.Hour }, "expired"},
		{"forged signature", func(f *fakeOpenAI, q url.Values) { f.idKey = rigKey(1) }, "signature"},
		{"no issued client id", func(f *fakeOpenAI, q url.Values) { q.Del("client_id") }, "no client id"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e, f := chatgptEnv(t)
			b := e.beginSignIn(f)
			q := b.consent()
			tc.set(f, q)
			status, page := b.callback(q)
			st, _ := e.r.ChatGPTStatus(bg())
			if status == http.StatusOK || st.SignedIn || st.Pending || !contains(st.Message+page, tc.msg) {
				t.Fatalf("status %d, %+v, page %q", status, st, page)
			}
			if e.hasToken(vaultChatGPTRefresh) || e.hasToken(vaultChatGPTAccess) {
				t.Error("a refused sign-in stored tokens")
			}
		})
	}
}

func TestLLM_ChatGPT_SignInWithoutPlanUsageKeepsNoTokensAndRunsNothing(t *testing.T) {
	e, f := chatgptEnv(t)
	f.scope = "openid profile email offline_access" // consent declined chatgpt.tokens.use.direct
	b := e.beginSignIn(f)
	b.callback(b.consent())
	st, _ := e.r.ChatGPTStatus(bg())
	if !st.SignedIn || st.PlanUsage || !contains(st.Message, "plan usage") {
		t.Fatalf("status = %+v", st)
	}
	if e.hasToken(vaultChatGPTRefresh) || e.hasToken(vaultChatGPTAccess) {
		t.Error("an identity-only sign-in stored tokens")
	}
	e.route("narrative", Route{Provider: "chatgpt", Model: "gpt-6.1-sol"})
	err := func() error { _, err := e.r.Complete(bg(), Request{Task: "narrative", Prompt: "hi"}); return err }()
	if e := wantKind(t, err, KindUnconfigured); !contains(e.Message, "plan usage") {
		t.Errorf("message = %q", e.Message)
	}
	if reg, _ := e.r.chatgptRegistration(bg()); reg.ClientID != f.issued {
		t.Errorf("the registration was not kept: %+v", reg)
	}
}

func TestLLM_ChatGPT_ReauthorizationRefusals(t *testing.T) {
	cases := []struct {
		name string
		set  func(f *fakeOpenAI, q url.Values)
		msg  string
	}{
		{"different client id", func(f *fakeOpenAI, q url.Values) { q.Set("client_id", "client_other") }, "different client"},
		{"different account", func(f *fakeOpenAI, q url.Values) { f.idSub = "user-2" }, "different ChatGPT account"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e, f := chatgptEnv(t)
			e.signIn(f)
			if _, err := e.r.SignOutChatGPT(bg(), false); err != nil {
				t.Fatal(err)
			}
			b := e.beginSignIn(f)
			q := b.consent()
			tc.set(f, q)
			if status, page := b.callback(q); status == http.StatusOK || !contains(page, tc.msg) {
				t.Fatalf("callback = %d %s", status, page)
			}
			if st, _ := e.r.ChatGPTStatus(bg()); st.SignedIn {
				t.Fatal("the credentials were replaced")
			}
		})
	}
	t.Run("forgetting the account lets another sign in", func(t *testing.T) {
		e, f := chatgptEnv(t)
		e.signIn(f)
		if _, err := e.r.SignOutChatGPT(bg(), true); err != nil {
			t.Fatal(err)
		}
		reg, _ := e.r.chatgptRegistration(bg())
		if reg.HostID == "" || reg.ClientID != "" || reg.Subject != "" {
			t.Fatalf("after forget: %+v (the host id stays, the account goes)", reg)
		}
		f.idSub = "user-2"
		e.signIn(f)
	})
}

// A sign-in started while another is being started replaces it: the attempt it
// stops is whole by the time it can be seen.
func TestLLM_ChatGPT_SignInsStartedTogetherReplaceEachOther(t *testing.T) {
	e, f := chatgptEnv(t)
	e.beginSignIn(f)
	var wg sync.WaitGroup
	for range 400 {
		wg.Go(func() {
			if _, err := e.r.BeginChatGPTSignIn(bg()); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	if st, err := e.r.ChatGPTStatus(bg()); err != nil || !st.Pending {
		t.Fatalf("status %+v err %v: the last sign-in is not waiting", st, err)
	}
	if _, err := e.r.SignOutChatGPT(bg(), false); err != nil {
		t.Fatal(err)
	}
}

func TestLLM_ChatGPT_SignOutRevokesClearsAndKeepsTheRegistration(t *testing.T) {
	e, f := chatgptEnv(t)
	e.signIn(f)
	refresh := e.refreshToken()
	revoked, err := e.r.SignOutChatGPT(bg(), false)
	if err != nil || !revoked {
		t.Fatalf("revoked %v err %v", revoked, err)
	}
	if len(f.revokes) != 1 || f.revokes[0].Get("token") != refresh || f.revokes[0].Get("token_type_hint") != "refresh_token" || f.revokes[0].Get("client_id") != f.issued {
		t.Fatalf("revocation = %v", f.revokes)
	}
	if e.hasToken(vaultChatGPTRefresh) || e.hasToken(vaultChatGPTAccess) {
		t.Error("the family is still in the vault")
	}
	reg, _ := e.r.chatgptRegistration(bg())
	if reg.SignedIn || reg.ClientID != f.issued || reg.HostID == "" {
		t.Errorf("registration = %+v", reg)
	}

	t.Run("an unconfirmed revocation still clears and says so", func(t *testing.T) {
		e, f := chatgptEnv(t)
		e.signIn(f)
		f.revokeStatus = []int{500, 503, 500}
		revoked, err := e.r.SignOutChatGPT(bg(), false)
		if err != nil || revoked || len(f.revokes) != 3 {
			t.Fatalf("revoked %v err %v after %d tries", revoked, err, len(f.revokes))
		}
		if e.hasToken(vaultChatGPTRefresh) {
			t.Error("the refresh token was kept")
		}
	})
}

func TestLLM_ChatGPT_RefreshRotatesAndSavesTheFamily(t *testing.T) {
	e, f := chatgptEnv(t)
	e.signIn(f)
	first, err := e.r.chatgptToken(bg())
	if err != nil || len(f.tokenCalls("refresh_token")) != 0 {
		t.Fatalf("a fresh token needs no refresh: %v, %d calls", err, len(f.tokenCalls("refresh_token")))
	}
	before := e.refreshToken()
	e.clock.advance(59 * time.Minute) // inside the two minutes before expiry
	second, err := e.r.chatgptToken(bg())
	if err != nil || second == first {
		t.Fatalf("token after expiry: %v, same=%v", err, second == first)
	}
	calls := f.tokenCalls("refresh_token")
	if len(calls) != 1 || calls[0].Get("refresh_token") != before || calls[0].Get("client_id") != f.issued {
		t.Fatalf("refresh calls = %v", calls)
	}
	if after := e.refreshToken(); after == before {
		t.Fatal("the rotated refresh token was not saved")
	}
	if got, _ := e.vault.Get(vaultChatGPTAccess); got != second {
		t.Error("the new access token was not saved for other processes")
	}
	if again, _ := e.r.chatgptToken(bg()); again != second || len(f.tokenCalls("refresh_token")) != 1 {
		t.Error("a fresh token was refreshed again")
	}
}

func TestLLM_ChatGPT_ConcurrentCallsRefreshOnce(t *testing.T) {
	e, f := chatgptEnv(t)
	e.signIn(f)
	e.clock.advance(2 * time.Hour)
	var wg sync.WaitGroup
	tokens := make([]string, 8)
	errs := make([]error, 8)
	for i := range tokens {
		wg.Add(1)
		go func() {
			defer wg.Done()
			tokens[i], errs[i] = e.r.chatgptToken(bg())
		}()
	}
	wg.Wait()
	for i := range tokens {
		if errs[i] != nil || tokens[i] != tokens[0] {
			t.Fatalf("call %d: %v (a second refresh would find the rotated token used)", i, errs[i])
		}
	}
	if n := len(f.tokenCalls("refresh_token")); n != 1 {
		t.Fatalf("%d refreshes, want 1", n)
	}
}

func TestLLM_ChatGPT_ARefreshFinishesAndSavesWhenTheCallerGivesUp(t *testing.T) {
	e, f := chatgptEnv(t)
	e.signIn(f)
	e.clock.advance(2 * time.Hour)
	ctx, cancel := context.WithCancel(bg())
	f.refreshGate = cancel // the caller leaves while the issuer is rotating the token
	if _, err := e.r.chatgptToken(ctx); err != nil {
		t.Fatalf("the refresh was cut off with its caller: %v", err)
	}
	f.mu.Lock()
	ok := f.live[e.refreshToken()]
	f.mu.Unlock()
	if !ok {
		t.Fatal("the vault holds a refresh token the issuer already rotated away")
	}
}

func TestLLM_ChatGPT_TerminalRefreshErrorsSignOutAndFallBack(t *testing.T) {
	for _, code := range []string{"invalid_grant", "invalid_refresh_token", "token_expired", "refresh_token_expired", "refresh_token_invalidated", "refresh_token_reused", "invalid_client"} {
		t.Run(code, func(t *testing.T) {
			e, f := chatgptEnv(t)
			e.signIn(f)
			e.clock.advance(2 * time.Hour)
			f.refreshFail = func(url.Values) (int, string) { return 400, fmt.Sprintf(`{"error":%q}`, code) }
			_, err := e.r.chatgptToken(bg())
			if e := wantKind(t, err, KindUnconfigured); !contains(e.Message, code, "sign in") {
				t.Errorf("message = %q", e.Message)
			}
			st, _ := e.r.ChatGPTStatus(bg())
			if st.SignedIn || !contains(st.Message, code) {
				t.Errorf("status = %+v", st)
			}
			if e.hasToken(vaultChatGPTRefresh) || e.hasToken(vaultChatGPTAccess) {
				t.Error("the dead family was kept")
			}
			reg, _ := e.r.chatgptRegistration(bg())
			if (code == "invalid_client") == (reg.ClientID != "") || reg.HostID == "" {
				t.Errorf("registration = %+v (only a refused client is dropped)", reg)
			}
		})
	}
	t.Run("the walk moves on to the fallback", func(t *testing.T) {
		e, f := chatgptEnv(t)
		e.signIn(f)
		e.clock.advance(2 * time.Hour)
		f.refreshFail = func(url.Values) (int, string) { return 400, `{"error":"invalid_grant"}` }
		b := newOAServer(t, okHandler("from fallback"))
		e.provider(oaProvider("b", b), fakeKey("fb"))
		e.route("narrative", Route{Provider: "chatgpt", Model: "gpt-6.1-sol", Fallbacks: []Fallback{{"b", "m"}}})
		resp, err := e.r.Complete(bg(), Request{Task: "narrative", Prompt: "hi"})
		if err != nil || resp.Text != "from fallback" {
			t.Fatalf("%v %+v", err, resp)
		}
		if got := outcomes(e.ledger()); got != "chatgpt:unconfigured,b:ok" {
			t.Errorf("ledger = %s", got)
		}
	})
}

func TestLLM_ChatGPT_ATransientRefreshFailureKeepsTheFamily(t *testing.T) {
	cases := []struct {
		name   string
		status int
		body   string
		kind   Kind
	}{
		{"server error", 503, `{"error":"temporarily_unavailable"}`, KindOverloaded},
		{"no body", 500, ``, KindOverloaded},
		{"throttled", 429, `{"error":"rate_limit_exceeded"}`, KindRateLimited},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e, f := chatgptEnv(t)
			e.signIn(f)
			before := e.refreshToken()
			e.clock.advance(2 * time.Hour)
			f.refreshFail = func(url.Values) (int, string) { return tc.status, tc.body }
			_, err := e.r.chatgptToken(bg())
			wantKind(t, err, tc.kind)
			if st, _ := e.r.ChatGPTStatus(bg()); !st.SignedIn || e.refreshToken() != before {
				t.Fatalf("a temporary failure ended the sign-in: %+v", st)
			}
			f.refreshFail = nil
			if _, err := e.r.chatgptToken(bg()); err != nil {
				t.Fatalf("the next attempt: %v", err)
			}
		})
	}
}

func TestLLM_ChatGPT_OnlyTheOwnerRefreshes(t *testing.T) {
	e, f := chatgptEnv(t)
	e.signIn(f)
	// A second process on the same workspace: it reads the vault and never rotates the token.
	cli := New(e.st, e.vault, WithClock(e.clock.now), WithChatGPTIssuer(f.srv.URL))
	if tok, err := cli.chatgptToken(bg()); err != nil || tok == "" {
		t.Fatalf("a valid access token is shared: %v", err)
	}
	e.clock.advance(2 * time.Hour)
	_, err := cli.chatgptToken(bg())
	if e := wantKind(t, err, KindUnconfigured); !contains(e.Message, "only the running app renews it") {
		t.Errorf("message = %q", e.Message)
	}
	if n := len(f.tokenCalls("refresh_token")); n != 0 {
		t.Fatalf("a process that does not own the family refreshed it %d times", n)
	}
	if _, err := e.r.chatgptToken(bg()); err != nil {
		t.Fatalf("the owner: %v", err)
	}
	if tok, err := cli.chatgptToken(bg()); err != nil || tok == "" {
		t.Fatalf("the renewed token is shared: %v", err)
	}
}
